package guestinit

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func socketClient(t *testing.T, s *Server) *http.Client {
	t.Helper()
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(context.Context, string, string) (net.Conn, error) {
		pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
		if err != nil {
			return nil, err
		}
		var conns [2]net.Conn
		for n, fd := range pair {
			file := os.NewFile(uintptr(fd), "pair")
			conns[n], err = net.FileConn(file)
			_ = file.Close()
			if err != nil {
				return nil, err
			}
		}
		listener := &testListener{conn: conns[1], done: make(chan struct{})}
		go func() { _ = http.Serve(listener, s) }()
		t.Cleanup(func() { _ = listener.Close(); _ = conns[1].Close() })
		return conns[0], nil
	}}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}
}

type testListener struct {
	conn net.Conn
	done chan struct{}
}

func (l *testListener) Accept() (net.Conn, error) {
	if l.conn == nil {
		<-l.done
		return nil, net.ErrClosed
	}
	c := l.conn
	l.conn = nil
	return c, nil
}
func (l *testListener) Close() error {
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	return nil
}
func (l *testListener) Addr() net.Addr { return &net.UnixAddr{Net: "unix", Name: "pair"} }
func request(t *testing.T, c *http.Client, path string, body any) *http.Response {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	response, err := c.Post("http://guest"+path, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 && response.StatusCode != 204 {
		data, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		t.Fatalf("%s: %d %s", path, response.StatusCode, data)
	}
	return response
}
func TestProtocolOutputWaitAndArtifact(t *testing.T) {
	s := New()
	c := socketClient(t, s)
	dir := t.TempDir()
	response := request(t, c, "/start", Start{Argv: []string{"sh", "-c", "printf first; printf stderr >&2; printf '%s' \"$VALUE\" > report; exit 7"}, Env: []string{"VALUE=artifact"}, Cwd: dir})
	_ = response.Body.Close()
	response = request(t, c, "/wait", nil)
	var exit struct{ Code int }
	if err := json.NewDecoder(response.Body).Decode(&exit); err != nil || exit.Code != 7 {
		t.Fatalf("exit: %v %v", exit, err)
	}
	_ = response.Body.Close()
	for _, path := range []string{"/logs", "/logs?follow=true"} {
		response = request(t, c, path, nil)
		data, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || !strings.Contains(string(data), "first") || !strings.Contains(string(data), "stderr") {
			t.Fatalf("logs %q %v", data, err)
		}
	}
	response = request(t, c, "/copyout?path="+filepath.Join(dir, "report"), nil)
	defer response.Body.Close()
	tr := tar.NewReader(response.Body)
	header, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(tr)
	if err != nil || header.Name != "report" || string(data) != "artifact" {
		t.Fatalf("artifact %v %q %v", header, data, err)
	}
}
func TestProtocolStopAndFollow(t *testing.T) {
	s := New()
	c := socketClient(t, s)
	response := request(t, c, "/start", Start{Argv: []string{"sh", "-c", "trap '' TERM; echo ready; sleep 300 & wait"}})
	_ = response.Body.Close()
	response = request(t, c, "/logs?follow=true", nil)
	reader := bufio.NewReader(response.Body)
	first, err := reader.ReadString('\n')
	if err != nil || first != "ready\n" {
		t.Fatalf("ready %q %v", first, err)
	}
	done := make(chan []byte, 1)
	go func() { data, _ := io.ReadAll(reader); _ = response.Body.Close(); done <- data }()
	stop := request(t, c, "/stop?grace=20ms", nil)
	_ = stop.Body.Close()
	select {
	case data := <-done:
		if len(data) != 0 {
			t.Fatalf("output %q", data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("follow did not end")
	}
	response = request(t, c, "/wait", nil)
	defer response.Body.Close()
	var exit struct{ Code int }
	if err := json.NewDecoder(response.Body).Decode(&exit); err != nil || exit.Code == 0 {
		t.Fatalf("stopped exit %v %v", exit, err)
	}
	response = request(t, c, "/logs", nil)
	_ = response.Body.Close()
}
func TestProtocolFollowCancellationAndInvalidStart(t *testing.T) {
	s := New()
	c := socketClient(t, s)
	response, err := c.Post("http://guest/start", "application/json", strings.NewReader(`{"argv":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 400 {
		t.Fatal(response.StatusCode)
	}
	_ = response.Body.Close()
	response = request(t, c, "/start", Start{Argv: []string{"sleep", "300"}})
	_ = response.Body.Close()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://guest/logs?follow=true", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err = io.ReadAll(response.Body); err == nil {
		t.Fatal("cancelled stream read succeeded")
	}
	_ = response.Body.Close()
	response = request(t, c, "/stop?grace=0s", nil)
	_ = response.Body.Close()
}

func TestProtocolUsesEnvironmentPathAndCreatesWorkingDirectory(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "command"), []byte("#!/bin/sh\nprintf '%s' \"$VALUE\" > result\n"), 0755); err != nil {
		t.Fatal(err)
	}
	working := filepath.Join(directory, "work")
	c := socketClient(t, New())
	response := request(t, c, "/start", Start{Argv: []string{"command"}, Env: []string{"PATH=" + directory, "VALUE=custom"}, Cwd: working})
	_ = response.Body.Close()
	response = request(t, c, "/wait", nil)
	_ = response.Body.Close()
	data, err := os.ReadFile(filepath.Join(working, "result"))
	if err != nil || string(data) != "custom" {
		t.Fatalf("result %q %v", data, err)
	}
}

func TestProtocolDrainsMergedOutputWhenDescendantHoldsPipe(t *testing.T) {
	client := socketClient(t, New())
	response := request(t, client, "/start", Start{Argv: []string{"sh", "-c", "sleep 300 & i=0; while [ $i -lt 8192 ]; do printf out; printf err >&2; i=$((i+1)); done"}})
	_ = response.Body.Close()
	response = request(t, client, "/logs?follow=true", nil)
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || string(data) != strings.Repeat("outerr", 8192) {
		t.Fatalf("merged output: %d bytes, %v", len(data), err)
	}
	response = request(t, client, "/wait", nil)
	defer response.Body.Close()
	var exit struct{ Code int }
	if err := json.NewDecoder(response.Body).Decode(&exit); err != nil || exit.Code != 0 {
		t.Fatalf("exit %+v %v", exit, err)
	}
}

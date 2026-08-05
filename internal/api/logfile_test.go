package api

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestResolveLogFilePath_StaysInsideDeployment(t *testing.T) {
	base := t.TempDir()

	got, err := resolveLogFilePath(base, "storage/logs/laravel.log")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(base, "storage/logs/laravel.log")
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestResolveLogFilePath_RejectsTraversal(t *testing.T) {
	base := t.TempDir()

	for _, bad := range []string{
		"../../../etc/passwd",
		"/etc/passwd",
		"storage/../../secret",
		"",
	} {
		if _, err := resolveLogFilePath(base, bad); err == nil {
			t.Errorf("expected %q to be rejected, but it was allowed", bad)
		}
	}
}

func TestReadFileTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	// Distinguish each line so tail ordering is verifiable.
	content := ""
	for i := 1; i <= 500; i++ {
		content += "line-" + strconv.Itoa(i) + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tail, err := readFileTail(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	if tail != "line-498\nline-499\nline-500" {
		t.Errorf("tail = %q", tail)
	}

	all, err := readFileTail(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(all, "\n") < 499 {
		t.Errorf("expected the whole file, got %d newlines", strings.Count(all, "\n"))
	}
}

func TestReadFileTail_FewerLinesThanRequested(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "short.log")
	if err := os.WriteFile(path, []byte("only\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tail, err := readFileTail(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	if tail != "only\ntwo" {
		t.Errorf("tail = %q", tail)
	}
}

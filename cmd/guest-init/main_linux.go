package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/flatrun/agent/internal/guestinit"
	"golang.org/x/sys/unix"
)

func main() {
	if os.Getpid() != 1 {
		fatal("guest init must run as PID 1")
	}
	if err := os.Setenv("PATH", guestinit.DefaultPath); err != nil {
		fatal(err)
	}
	for _, mount := range mountPlan() {
		if err := os.MkdirAll(mount.target, 0755); err != nil {
			fatal(err)
		}
		if err := mountFilesystem(mount.kind, mount.target); err != nil {
			fatal(err)
		}
	}
	for _, link := range []struct{ name, target string }{{"/dev/fd", "/proc/self/fd"}, {"/dev/stdin", "/proc/self/fd/0"}, {"/dev/stdout", "/proc/self/fd/1"}, {"/dev/stderr", "/proc/self/fd/2"}} {
		if err := os.Symlink(link.target, link.name); err != nil && !os.IsExist(err) {
			fatal(err)
		}
	}
	if err := loopback(); err != nil {
		fatal(err)
	}
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		fatal(err)
	}
	if err = unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: guestinit.Port}); err != nil {
		fatal(err)
	}
	if err = unix.Listen(fd, 128); err != nil {
		fatal(err)
	}
	server := guestinit.New()
	go server.Reap()
	fatal(http.Serve(&vsockListener{fd: fd}, server))
}

type vsockListener struct{ fd int }

func (l *vsockListener) Accept() (net.Conn, error) {
	fd, _, err := unix.Accept4(l.fd, unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK)
	if err != nil {
		return nil, err
	}
	return &vsockConn{File: os.NewFile(uintptr(fd), "vsock")}, nil
}
func (l *vsockListener) Close() error   { return unix.Close(l.fd) }
func (l *vsockListener) Addr() net.Addr { return &net.UnixAddr{Name: "vsock", Net: "vsock"} }

type vsockConn struct{ *os.File }

func (c *vsockConn) LocalAddr() net.Addr           { return &net.UnixAddr{Name: "guest", Net: "vsock"} }
func (c *vsockConn) RemoteAddr() net.Addr          { return &net.UnixAddr{Name: "host", Net: "vsock"} }
func (c *vsockConn) SetDeadline(t time.Time) error { return c.File.SetDeadline(t) }

func loopback() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	request, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	if err = unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, request); err != nil {
		return err
	}
	request.SetUint16(request.Uint16() | unix.IFF_UP)
	return unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, request)
}

func fatal(message any) {
	text := fmt.Sprintf("guest init: %v\n", message)
	console, err := os.OpenFile("/dev/console", os.O_WRONLY|unix.O_SYNC, 0)
	if err == nil {
		_, _ = console.WriteString(text)
		_ = console.Sync()
		_ = unix.IoctlSetInt(int(console.Fd()), unix.TCSBRK, 1)
		_ = console.Close()
	} else {
		_, _ = os.Stderr.WriteString(text)
		_ = os.Stderr.Sync()
	}
	unix.Sync()
	os.Exit(1)
}

func mountedTarget(info, target string) bool {
	for _, line := range strings.Split(info, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		path := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(fields[4])
		if path == target {
			return true
		}
	}
	return false
}

func mountFilesystem(kind, target string) error {
	info, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if mountedTarget(string(info), target) {
		return nil
	}
	if err := unix.Mount(kind, target, kind, 0, ""); err != nil {
		return fmt.Errorf("mount %s: %w", target, err)
	}
	return nil
}

func mountPlan() []struct{ target, kind string } {
	return []struct{ target, kind string }{{"/proc", "proc"}, {"/sys", "sysfs"}, {"/dev", "devtmpfs"}, {"/dev/shm", "tmpfs"}}
}

package main

import (
	"os"
	"testing"
)

func TestMountedTarget(t *testing.T) {
	info, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		target string
		want   bool
	}{
		{"/", true}, {"/proc", true}, {"/proc/self", false},
		{"/flatrun-nonexistent-mount", false},
	} {
		if got := mountedTarget(string(info), tc.target); got != tc.want {
			t.Errorf("mountedTarget(%q) = %v, want %v", tc.target, got, tc.want)
		}
	}
	if err := mountFilesystem("proc", "/proc"); err != nil {
		t.Fatalf("already mounted proc must be skipped: %v", err)
	}
}

func TestMountPlan(t *testing.T) {
	want := map[string]string{"/proc": "proc", "/sys": "sysfs", "/dev": "devtmpfs", "/dev/shm": "tmpfs"}
	for _, mount := range mountPlan() {
		if kind, ok := want[mount.target]; !ok || kind != mount.kind {
			t.Fatalf("unexpected mount: %+v", mount)
		}
		delete(want, mount.target)
	}
	if len(want) != 0 {
		t.Fatalf("missing mounts: %v", want)
	}
}

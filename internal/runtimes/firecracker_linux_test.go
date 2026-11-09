package runtimes

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestFirecrackerAvailabilityReportsMissingRequirements(t *testing.T) {
	dir := t.TempDir()
	runtime := NewFirecracker(FirecrackerConfig{Binary: filepath.Join(dir, "missing-firecracker"), Jailer: filepath.Join(dir, "missing-jailer"), Kernel: filepath.Join(dir, "missing-kernel"), GuestInit: filepath.Join(dir, "missing-init")}, nil, "scope")
	err := runtime.Available(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("availability: %v", err)
	}
	for _, reason := range []string{"missing-firecracker", "missing-jailer", "missing-kernel", "missing-init", "Docker image exporter"} {
		if !strings.Contains(err.Error(), reason) {
			t.Fatalf("missing %s: %v", reason, err)
		}
	}
}

func TestFirecrackerCreateCancellationLeavesNoContainer(t *testing.T) {
	d := dockerRuntime(t)
	dir := t.TempDir()
	init := filepath.Join(dir, "guest-init")
	if err := os.WriteFile(init, []byte("unused after cancellation"), 0600); err != nil {
		t.Fatal(err)
	}
	f := NewFirecracker(FirecrackerConfig{StateDir: dir, ChrootBaseDir: filepath.Join(dir, "jailer"), GuestInit: init}, d, d.Scope)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hook := cancelDuringDockerCreate(t, d, cancel)
	instance, err := f.Create(ctx, Spec{Image: testImage, Command: []string{"true"}})
	if !hook.called {
		t.Fatalf("create never reached cancellation hook: %v", err)
	}
	if instance != nil || !errors.Is(err, context.Canceled) {
		t.Errorf("create = %v, %v; want nil, canceled", instance, err)
	}
	left, err := d.Leftovers(context.Background())
	if err != nil || len(left) != 0 {
		t.Fatalf("containers remain after canceled create: %v (err %v)", left, err)
	}
	left, err = f.Leftovers(context.Background())
	if err != nil || len(left) != 0 {
		t.Fatalf("VM state remains after canceled create: %v (err %v)", left, err)
	}
}

func TestFirecrackerStateAndScope(t *testing.T) {
	root := t.TempDir()
	first := NewFirecracker(FirecrackerConfig{StateDir: root, Kernel: "/configured-kernel"}, nil, "first")
	second := NewFirecracker(FirecrackerConfig{StateDir: root, Kernel: "/configured-kernel"}, nil, "second")
	id := uuid.NewString()
	instance := &firecrackerInstance{runtime: first, id: id}
	if err := os.MkdirAll(first.directory(id), 0700); err != nil {
		t.Fatal(err)
	}
	if err := instance.writeState(first.directory(id)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(first.directory(id), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state fcState
	if err = json.Unmarshal(raw, &state); err != nil || state.ID != id || state.Scope != "first" {
		t.Fatalf("state %+v %v", state, err)
	}
	leftovers, err := first.Leftovers(context.Background())
	if err != nil || !slices.Equal(leftovers, []string{id}) {
		t.Fatalf("first leftovers: %v %v", leftovers, err)
	}
	if err = second.DestroyByID(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	leftovers, err = second.Leftovers(context.Background())
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("second leftovers: %v %v", leftovers, err)
	}
	if _, err = os.Stat(first.directory(id)); err != nil {
		t.Fatal("other scope removed state", err)
	}
	partial := uuid.NewString()
	if err = os.MkdirAll(first.directory(partial), 0700); err != nil {
		t.Fatal(err)
	}
	leftovers, err = first.Leftovers(context.Background())
	if err != nil || len(leftovers) != 2 || !slices.Contains(leftovers, partial) {
		t.Fatalf("partial state: %v %v", leftovers, err)
	}
	if err = first.DestroyByID(context.Background(), "../outside"); err == nil {
		t.Fatal("accepted invalid ID")
	}
	if len(first.jail(id)+"/v.sock_4294967295") >= 108 {
		t.Fatal("socket path too long")
	}
}

func TestFirecrackerChrootFilesystemAndSocketLimit(t *testing.T) {
	f := NewFirecracker(FirecrackerConfig{}, nil, "scope")
	if f.Config.ChrootBaseDir != "/srv/jailer" {
		t.Fatal(f.Config.ChrootBaseDir)
	}
	f.Config.Kernel = "/missing-kernel"
	f.Config.ChrootBaseDir = "/proc/nonexistent-jailer"
	if err := f.Available(context.Background()); err == nil || !strings.Contains(err.Error(), "chroot base /proc/nonexistent-jailer is on a nodev filesystem") {
		t.Fatal(err)
	}
	f.Config.ChrootBaseDir = "/" + strings.Repeat("a", 108)
	if err := f.Available(context.Background()); err == nil || !strings.Contains(err.Error(), "socket path exceeds") {
		t.Fatal(err)
	}
}

func TestFirecrackerCloneAndKernelReuse(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(source, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "clone")
	if err := cloneFile(ctx, source, target, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("modified"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(source)
	if err != nil || string(data) != "original" {
		t.Fatalf("source changed: %q %v", data, err)
	}
	f := NewFirecracker(FirecrackerConfig{StateDir: dir, Kernel: source}, nil, "scope")
	if err = os.MkdirAll(filepath.Join(dir, "cache"), 0700); err != nil {
		t.Fatal(err)
	}
	kernel, err := f.cachedKernel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(dir, "kernel")
	if err = linkKernel(ctx, kernel, linked); err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(kernel)
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.Stat(linked)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(first, second) || second.Mode().Perm() != 0444 {
		t.Fatal("kernel was not linked read-only")
	}
}

func TestFirecrackerDestroyPrunesJailParents(t *testing.T) {
	dir := t.TempDir()
	f := NewFirecracker(FirecrackerConfig{Kernel: "/configured-kernel", StateDir: filepath.Join(dir, "state"), ChrootBaseDir: filepath.Join(dir, "jailer")}, nil, "scope")
	id := uuid.NewString()
	instance := &firecrackerInstance{runtime: f, id: id}
	if err := os.MkdirAll(f.jail(id), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.directory(id), 0700); err != nil {
		t.Fatal(err)
	}
	if err := instance.writeState(f.directory(id)); err != nil {
		t.Fatal(err)
	}
	if err := f.DestroyByID(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.base()); !os.IsNotExist(err) {
		t.Fatalf("scope remains: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(f.directory(id))); !os.IsNotExist(err) {
		t.Fatalf("state scope remains: %v", err)
	}
	if err := instance.Destroy(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFirecrackerBootExitDiagnostics(t *testing.T) {
	runtime := NewFirecracker(FirecrackerConfig{StateDir: t.TempDir()}, nil, "boot")
	instance := &firecrackerInstance{runtime: runtime, id: uuid.NewString(), done: make(chan struct{})}
	dir := runtime.directory(instance.id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"console.log": strings.Repeat("old boot output\n", 1000) + "guest init: device or resource busy\n",
		"vmm.log":     "Firecracker exiting successfully. exit_code=0\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	close(instance.done)
	err := instance.retry(context.Background(), func() error { return errors.New("guest unavailable") })
	for _, want := range []string{"firecracker exited during boot", "console.log:", "guest init: device or resource busy", "vmm.log:", "exit_code=0"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q in %v", want, err)
		}
	}
	tail, err := bootLogTail(filepath.Join(dir, "console.log"))
	if err != nil || len(tail) > 4096 || len(strings.Split(tail, "\n")) > 20 {
		t.Fatalf("invalid tail: %q, %v", tail, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "console.log"), []byte(strings.Repeat("x", 8192)), 0600); err != nil {
		t.Fatal(err)
	}
	tail, err = bootLogTail(filepath.Join(dir, "console.log"))
	if err != nil || len(tail) != 4096 {
		t.Fatalf("byte bound: %d, %v", len(tail), err)
	}
}

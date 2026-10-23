package build

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func write(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestRegistry_Detect(t *testing.T) {
	compose := t.TempDir()
	write(t, compose, "compose.yaml")
	write(t, compose, "Dockerfile")
	dockerfile := t.TempDir()
	write(t, dockerfile, "Dockerfile")
	plain := t.TempDir()
	write(t, plain, "package.json")

	r := DefaultRegistry()
	cases := map[string]Kind{compose: KindCompose, dockerfile: KindDockerfile, plain: KindRailpack}
	for dir, want := range cases {
		if got := r.Detect(dir); got != want {
			t.Errorf("Detect(%s) = %s, want %s", filepath.Base(dir), got, want)
		}
	}
}

type fakeBuilder struct {
	name  Kind
	built *Request
}

func (f *fakeBuilder) Name() Kind         { return f.name }
func (f *fakeBuilder) Detect(string) bool { return true }
func (f *fakeBuilder) Build(_ context.Context, req Request, _ func(string)) error {
	f.built = &req
	return nil
}

func TestRegistry_OrderAndReplacement(t *testing.T) {
	custom := &fakeBuilder{name: "buildpacks"}
	r := NewRegistry(custom, Railpack{})
	dir := t.TempDir()
	write(t, dir, "main.go")

	if got := r.Detect(dir); got != "buildpacks" {
		t.Fatalf("Detect = %s, want the first registered builder", got)
	}
	req := Request{Dir: dir, Scratch: t.TempDir(), Tag: "flatrun/app:1"}
	if err := r.Build(context.Background(), "buildpacks", req, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if custom.built == nil || *custom.built != req {
		t.Errorf("custom builder got %+v", custom.built)
	}
	if err := r.Build(context.Background(), "nixpacks", req, func(string) {}); err == nil {
		t.Error("building with an unregistered builder should fail")
	}
	if got := NewRegistry().Detect(dir); got != "" {
		t.Errorf("empty registry Detect = %q, want none", got)
	}
}

type call struct {
	name string
	args []string
}

func recorder(calls *[]call) Runner {
	return func(_ context.Context, name string, args []string, _ func(string)) error {
		*calls = append(*calls, call{name, args})
		return nil
	}
}

func TestRailpack_Build(t *testing.T) {
	var calls []call
	b := Railpack{Run: recorder(&calls), LookPath: func(string) (string, error) { return "/usr/local/bin/railpack", nil }}
	if err := b.Build(context.Background(), Request{Dir: "/src", Scratch: "/scratch", Tag: "flatrun/app:abc"}, func(string) {}); err != nil {
		t.Fatal(err)
	}
	want := []call{
		{"railpack", []string{"prepare", "/src", "--plan-out", "/scratch/railpack-plan.json", "--info-out", "/scratch/railpack-info.json"}},
		{"docker", []string{"buildx", "build", "--build-arg", "BUILDKIT_SYNTAX=ghcr.io/railwayapp/railpack-frontend", "-f", "/scratch/railpack-plan.json", "--load", "-t", "flatrun/app:abc", "/src"}},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %+v\nwant  %+v", calls, want)
	}
}

func TestRailpack_Missing(t *testing.T) {
	var calls []call
	b := Railpack{Run: recorder(&calls), LookPath: func(string) (string, error) { return "", errors.New("not found") }}
	err := b.Build(context.Background(), Request{Dir: "/src", Scratch: "/scratch", Tag: "t"}, func(string) {})
	if !errors.Is(err, ErrRailpackMissing) || len(calls) != 0 {
		t.Errorf("err = %v, calls = %+v; want ErrRailpackMissing and no commands", err, calls)
	}
}

func TestDockerfile_Build(t *testing.T) {
	var calls []call
	if err := (Dockerfile{Run: recorder(&calls)}).Build(context.Background(), Request{Dir: "/src", Tag: "flatrun/app:abc"}, func(string) {}); err != nil {
		t.Fatal(err)
	}
	want := []call{{"docker", []string{"build", "-t", "flatrun/app:abc", "/src"}}}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %+v", calls)
	}
}

func TestImageTag(t *testing.T) {
	if got := ImageTag("MyApp", "0123456789abcdef"); got != "flatrun/myapp:0123456789ab" {
		t.Errorf("ImageTag = %q", got)
	}
	if got := ImageTag("app", ""); got != "flatrun/app:latest" {
		t.Errorf("ImageTag without revision = %q", got)
	}
}

func TestRunCommand_LongOutputLine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var lines []string
	var mu sync.Mutex
	err := runCommand(ctx, "sh", []string{"-c", `head -c 2500000 /dev/zero | tr '\0' a; echo; head -c 300000 /dev/zero | tr '\0' b; echo; echo done`}, func(line string) {
		mu.Lock()
		lines = append(lines, line)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("runCommand = %v", err)
	}
	total := 0
	for _, l := range lines {
		total += len(l)
	}
	if lines[len(lines)-1] != "done" || total != 2500000+300000+len("done") {
		t.Fatalf("got %d lines, %d bytes, last %.10q", len(lines), total, lines[len(lines)-1])
	}
}

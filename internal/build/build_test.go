package build

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func write(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestDetect(t *testing.T) {
	compose := t.TempDir()
	write(t, compose, "compose.yaml")
	write(t, compose, "Dockerfile")
	dockerfile := t.TempDir()
	write(t, dockerfile, "Dockerfile")
	plain := t.TempDir()
	write(t, plain, "package.json")

	cases := map[string]Kind{compose: KindCompose, dockerfile: KindDockerfile, plain: KindRailpack}
	for dir, want := range cases {
		if got := Detect(dir); got != want {
			t.Errorf("Detect(%s) = %s, want %s", filepath.Base(dir), got, want)
		}
	}
}

type call struct {
	name string
	args []string
}

func recordingBuilder(calls *[]call, railpack bool) *Builder {
	return &Builder{
		Run: func(_ context.Context, name string, args []string, _ func(string)) error {
			*calls = append(*calls, call{name, args})
			return nil
		},
		LookPath: func(string) (string, error) {
			if railpack {
				return "/usr/local/bin/railpack", nil
			}
			return "", errors.New("not found")
		},
	}
}

func TestBuild_Railpack(t *testing.T) {
	var calls []call
	b := recordingBuilder(&calls, true)
	if err := b.Build(context.Background(), KindRailpack, "/src", "/scratch", "flatrun/app:abc", func(string) {}); err != nil {
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

func TestBuild_RailpackMissing(t *testing.T) {
	var calls []call
	b := recordingBuilder(&calls, false)
	err := b.Build(context.Background(), KindRailpack, "/src", "/scratch", "flatrun/app:abc", func(string) {})
	if !errors.Is(err, ErrRailpackMissing) {
		t.Errorf("err = %v, want ErrRailpackMissing", err)
	}
	if len(calls) != 0 {
		t.Errorf("no command should run without railpack, got %+v", calls)
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

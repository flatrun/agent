package runtimes

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRootImageSizeHardLinks(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "busybox")
	if err := os.WriteFile(original, make([]byte, 4097), 0600); err != nil {
		t.Fatal(err)
	}
	for i := range 400 {
		if err := os.Link(original, filepath.Join(root, fmt.Sprintf("link-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	size, err := rootImageSize(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	want := int64(32<<20) + 2*(3*4096+((info.Size()+4095)/4096+1)*4096)
	if size != want {
		t.Fatalf("size = %d, want %d", size, want)
	}
	if err = os.WriteFile(filepath.Join(root, "distinct"), make([]byte, 4097), 0600); err != nil {
		t.Fatal(err)
	}
	larger, err := rootImageSize(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if larger < size+2*3*4096 {
		t.Fatalf("distinct inode not counted: %d -> %d", size, larger)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = rootImageSize(ctx, root); err != context.Canceled {
		t.Fatalf("canceled estimate: %v", err)
	}
}

func TestCopySparseFile(t *testing.T) {
	for _, kind := range []string{"empty", "holes", "data"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			source, err := os.Create(filepath.Join(dir, "source"))
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			if kind != "empty" {
				if err = source.Truncate(16 << 20); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "data" {
				for _, offset := range []int64{4096, 8 << 20} {
					if _, err = source.WriteAt([]byte("sparse file contents"), offset); err != nil {
						t.Fatal(err)
					}
				}
			}
			output, err := os.Create(filepath.Join(dir, "output"))
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			if err = copySparseFile(context.Background(), source, output); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(source.Name())
			if err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(output.Name())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("copy contents differ")
			}
			info, err := output.Stat()
			if err != nil {
				t.Fatal(err)
			}
			allocated := info.Sys().(*syscall.Stat_t).Blocks * 512
			if info.Size() > 0 && allocated >= info.Size()/2 {
				t.Fatalf("copy is not sparse: %d allocated / %d logical", allocated, info.Size())
			}
		})
	}
}

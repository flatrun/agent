package runs

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/flatrun/agent/internal/runtimes"
	"github.com/google/uuid"
)

func TestStartupRecoversDisabledFirecracker(t *testing.T) {
	root := t.TempDir()
	scope := "recovery"
	sum := sha256.Sum256([]byte(scope))
	state := filepath.Join(root, "state", fmt.Sprintf("%x", sum[:6]), uuid.NewString())
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]string{"id": filepath.Base(state), "scope": scope})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "state.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	fc := runtimes.NewFirecracker(runtimes.FirecrackerConfig{StateDir: filepath.Join(root, "state"), ChrootBaseDir: filepath.Join(root, "jailer")}, nil, scope)
	manager, err := NewManager(root, fc, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("leftover remains: %v", err)
	}
	if err := manager.CheckRuntime(context.Background(), runtimes.TypeFirecracker); !errors.Is(err, runtimes.ErrUnavailable) {
		t.Fatalf("disabled runtime: %v", err)
	}
}

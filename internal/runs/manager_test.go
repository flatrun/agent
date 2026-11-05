package runs

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/runtimes"
	"github.com/google/uuid"
)

func TestDockerManagerRestartAndClose(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Docker")
	}
	d, err := runtimes.NewDocker()
	if err != nil {
		t.Fatal(err)
	}
	probe, cancelProbe := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelProbe()
	if err = d.Available(probe); err != nil {
		t.Skipf("Docker unreachable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	d.Scope = uuid.NewString()
	d.EgressSubnet = os.Getenv("FLATRUN_TEST_EGRESS_SUBNET")
	root := t.TempDir()
	inst, err := d.Create(ctx, runtimes.Spec{Image: "busybox:latest", Command: []string{"sleep", "30"}})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Destroy(context.Background())
	if err = inst.Start(ctx); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(&Run{ID: "interrupted", Status: StatusRunning}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	m, err := NewManager(root, d, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	r, err := m.Store.Get("interrupted")
	if err != nil || r.Error != "agent restarted" {
		t.Fatalf("restart: %+v %v", r, err)
	}
	r, err = m.Submit(1, "test", Spec{Image: "busybox:latest", Command: []string{"sleep", "30"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, err = m.Store.Get(r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status == StatusRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not running: %+v", r)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelCleanup()
	left, err := d.Leftovers(cleanupContext)
	if err != nil || len(left) != 0 {
		t.Fatalf("leftovers after close: %v %v", left, err)
	}
}

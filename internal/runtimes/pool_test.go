package runtimes

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type poolTestRuntime struct {
	mu        sync.Mutex
	instances []*poolTestInstance
}

func (*poolTestRuntime) Type() Type                      { return TypeFirecracker }
func (*poolTestRuntime) Available(context.Context) error { return nil }
func (r *poolTestRuntime) Create(_ context.Context, spec Spec) (Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := &poolTestInstance{spec: spec}
	r.instances = append(r.instances, i)
	return i, nil
}

type poolTestInstance struct {
	mu                                      sync.Mutex
	spec                                    Spec
	prepared, started, destroyed, unhealthy bool
}

func (*poolTestInstance) ID() string { return "test" }
func (i *poolTestInstance) Prepare(context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.prepared = true
	return nil
}
func (i *poolTestInstance) Ready(context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.unhealthy {
		return errors.New("unhealthy")
	}
	return nil
}
func (i *poolTestInstance) Configure(_ context.Context, spec Spec) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.spec = spec
	return nil
}
func (i *poolTestInstance) Start(context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.started = true
	return nil
}
func (*poolTestInstance) Wait(context.Context) (Exit, error) { return Exit{}, nil }
func (*poolTestInstance) Logs(context.Context, bool) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (*poolTestInstance) CopyOut(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("no artifacts")
}
func (*poolTestInstance) Stop(context.Context, time.Duration) error { return nil }
func (i *poolTestInstance) Destroy(context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.destroyed = true
	return nil
}
func awaitPool(t *testing.T, r *WarmRuntime) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if r.Status()[0].Ready == 1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("pool did not fill: %+v", r.Status())
}
func TestWarmPoolBootsWithoutCredentialsAndDestroysEachJob(t *testing.T) {
	backend := &poolTestRuntime{}
	r := NewWarmRuntime(backend)
	defer r.Close()
	template := Spec{Image: "runner@sha256:test", Resources: Resources{CPUs: 2, Memory: 4 << 30}, Network: Network{Egress: true}, CacheKey: "repository-main"}
	if err := r.AddPool(template, 1); err != nil {
		t.Fatal(err)
	}
	awaitPool(t, r)
	backend.mu.Lock()
	warm := backend.instances[0]
	backend.mu.Unlock()
	warm.mu.Lock()
	if !warm.prepared || warm.started || len(warm.spec.Env) > 0 {
		t.Errorf("warm instance executed work: %+v", warm.spec)
	}
	warm.mu.Unlock()
	job := template
	job.Command = []string{"runner"}
	job.Env = map[string]string{"JIT": "job-credential"}
	instance, err := r.Create(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if instance != warm {
		t.Fatal("ready VM was not used")
	}
	if err := instance.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := instance.Destroy(context.Background()); err != nil {
		t.Fatal(err)
	}
	awaitPool(t, r)
	next, err := r.Create(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if next == instance {
		t.Fatal("used VM recycled into warm pool")
	}
	_ = next.Destroy(context.Background())
}
func TestWarmPoolRejectsUnhealthyVMAndDoesNotCrossCacheScopes(t *testing.T) {
	backend := &poolTestRuntime{}
	r := NewWarmRuntime(backend)
	defer r.Close()
	template := Spec{Image: "runner@sha256:test", CacheKey: "main"}
	if err := r.AddPool(template, 1); err != nil {
		t.Fatal(err)
	}
	awaitPool(t, r)
	backend.mu.Lock()
	warm := backend.instances[0]
	backend.mu.Unlock()
	other := template
	other.CacheKey = "pull-request"
	instance, err := r.Create(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	if instance == warm {
		t.Fatal("cross-scope warm instance")
	}
	_ = instance.Destroy(context.Background())
	warm.mu.Lock()
	warm.unhealthy = true
	warm.mu.Unlock()
	instance, err = r.Create(context.Background(), template)
	if err != nil {
		t.Fatal(err)
	}
	_ = instance.Destroy(context.Background())
	warm.mu.Lock()
	defer warm.mu.Unlock()
	if !warm.destroyed || instance == warm {
		t.Fatal("unhealthy VM was reused")
	}
}
func TestWarmPoolCloseRemovesIdleVMs(t *testing.T) {
	backend := &poolTestRuntime{}
	r := NewWarmRuntime(backend)
	if err := r.AddPool(Spec{Image: "runner@sha256:test"}, 1); err != nil {
		t.Fatal(err)
	}
	awaitPool(t, r)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	for _, i := range backend.instances {
		i.mu.Lock()
		destroyed := i.destroyed
		i.mu.Unlock()
		if !destroyed {
			t.Fatal("idle VM leaked")
		}
	}
	if _, err := r.Create(context.Background(), Spec{Image: "runner@sha256:test"}); err == nil {
		t.Fatal("closed pool accepted work")
	}
}
func TestCacheSlotsKeepConcurrentWritersSeparateAndReuseReleasedSlot(t *testing.T) {
	d := &Docker{Scope: "agent"}
	first, release := d.leaseCache("repository-main")
	second, releaseSecond := d.leaseCache("repository-main")
	defer releaseSecond()
	if first == second {
		t.Fatal("concurrent cache writers share tool cache")
	}
	release()
	again, releaseAgain := d.leaseCache("repository-main")
	defer releaseAgain()
	if first != again {
		t.Fatal("cache slot not reused")
	}
	other, releaseOther := d.leaseCache("repository-pr")
	defer releaseOther()
	if other == first {
		t.Fatal("branches share cache")
	}
}

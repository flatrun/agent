package runtimes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type PreparedInstance interface {
	Instance
	Prepare(context.Context) error
	Ready(context.Context) error
	Configure(context.Context, Spec) error
}

type poolEntry struct {
	spec      Spec
	target    int
	idle      []PreparedInstance
	starting  int
	lastError string
	retryAt   time.Time
	blocked   bool
}

type PoolStatus struct {
	Target   int    `json:"target"`
	Ready    int    `json:"ready"`
	Starting int    `json:"starting"`
	Error    string `json:"error,omitempty"`
}

type WarmRuntime struct {
	Runtime
	mu     sync.Mutex
	pools  map[string]*poolEntry
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	closed bool
}

func NewWarmRuntime(runtime Runtime) *WarmRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	return &WarmRuntime{Runtime: runtime, pools: map[string]*poolEntry{}, ctx: ctx, cancel: cancel}
}

func poolKey(spec Spec) string {
	data, _ := json.Marshal(struct {
		Image     string
		Resources Resources
		Network   Network
		CacheKey  string
	}{spec.Image, spec.Resources, spec.Network, spec.CacheKey})
	return string(data)
}

func (r *WarmRuntime) AddPool(spec Spec, target int) error {
	if target < 1 || target > 32 {
		return errors.New("warm pool target must be 1 to 32")
	}
	if !strings.Contains(spec.Image, "@sha256:") && !strings.HasPrefix(spec.Image, "sha256:") {
		return errors.New("warm pool requires an immutable image digest")
	}
	if spec.Image == "" || len(spec.Env) != 0 {
		return errors.New("warm pool requires an image and no job environment")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("warm runtime closed")
	}
	key := poolKey(spec)
	if _, ok := r.pools[key]; ok {
		return errors.New("duplicate warm pool")
	}
	spec.Command = []string{"true"}
	p := &poolEntry{spec: spec, target: target}
	r.pools[key] = p
	r.wg.Add(1)
	go r.maintain(p)
	return nil
}

func (r *WarmRuntime) maintain(p *poolEntry) {
	defer r.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		r.mu.Lock()
		if !r.closed && !p.blocked && !time.Now().Before(p.retryAt) {
			for len(p.idle)+p.starting < p.target {
				p.starting++
				r.wg.Add(1)
				go r.fill(p)
			}
		}
		r.mu.Unlock()
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func destroyPrepared(instance Instance) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return instance.Destroy(ctx)
}

func (r *WarmRuntime) fill(p *poolEntry) {
	defer r.wg.Done()
	ctx, cancel := context.WithTimeout(r.ctx, 5*time.Minute)
	defer cancel()
	var instance Instance
	err := r.Runtime.Available(ctx)
	if err == nil {
		instance, err = r.Runtime.Create(ctx, p.spec)
	}
	var prepared PreparedInstance
	if err == nil {
		var ok bool
		prepared, ok = instance.(PreparedInstance)
		if !ok {
			err = errors.New("runtime does not support preparation")
		} else {
			err = prepared.Prepare(ctx)
		}
	}
	r.mu.Lock()
	p.starting--
	closed := r.closed
	if err == nil && !closed {
		p.idle = append(p.idle, prepared)
		if !p.blocked {
			p.lastError = ""
		}
		instance = nil
	} else if err != nil {
		p.lastError = err.Error()
		p.retryAt = time.Now().Add(30 * time.Second)
	}
	r.mu.Unlock()
	if instance != nil {
		if cleanup := destroyPrepared(instance); cleanup != nil {
			r.mu.Lock()
			p.blocked = true
			p.lastError = errors.Join(err, cleanup).Error()
			r.mu.Unlock()
		}
	}
}

func (r *WarmRuntime) Create(ctx context.Context, spec Spec) (Instance, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, errors.New("warm runtime closed")
	}
	p := r.pools[poolKey(spec)]
	var instance PreparedInstance
	if p != nil && len(p.idle) > 0 {
		instance = p.idle[0]
		p.idle = p.idle[1:]
	}
	r.mu.Unlock()
	if instance == nil {
		return r.Runtime.Create(ctx, spec)
	}
	if err := instance.Ready(ctx); err != nil {
		if cleanup := destroyPrepared(instance); cleanup != nil {
			return nil, fmt.Errorf("unhealthy warm instance cleanup: %w", cleanup)
		}
		return r.Runtime.Create(ctx, spec)
	}
	if err := instance.Configure(ctx, spec); err != nil {
		cleanup := destroyPrepared(instance)
		return nil, errors.Join(err, cleanup)
	}
	return instance, nil
}

func (r *WarmRuntime) Status() []PoolStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]PoolStatus, 0, len(r.pools))
	for _, p := range r.pools {
		result = append(result, PoolStatus{Target: p.target, Ready: len(p.idle), Starting: p.starting, Error: p.lastError})
	}
	return result
}

func (r *WarmRuntime) Close() error {
	r.mu.Lock()
	r.closed = true
	r.cancel()
	r.mu.Unlock()
	r.wg.Wait()
	var failures []error
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.pools {
		for _, instance := range p.idle {
			failures = append(failures, destroyPrepared(instance))
		}
		p.idle = nil
	}
	return errors.Join(failures...)
}

func (r *WarmRuntime) Leftovers(ctx context.Context) ([]string, error) {
	if cleaner, ok := r.Runtime.(LeftoverCleaner); ok {
		return cleaner.Leftovers(ctx)
	}
	return nil, nil
}
func (r *WarmRuntime) DestroyByID(ctx context.Context, id string) error {
	if cleaner, ok := r.Runtime.(LeftoverCleaner); ok {
		return cleaner.DestroyByID(ctx, id)
	}
	return nil
}

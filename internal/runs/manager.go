package runs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/flatrun/agent/internal/runtimes"
	"github.com/google/uuid"
)

var ErrQueueFull = errors.New("run queue is full")

var ErrClosed = errors.New("runs manager is closed")

type pending struct {
	run     Run
	secrets map[string]string
	ctx     context.Context
	cancel  context.CancelFunc
}

type Manager struct {
	Store   *Store
	root    string
	runtime runtimes.Runtime
	mu      sync.Mutex
	queue   chan *pending
	active  map[string]*pending
	closed  bool
	wg      sync.WaitGroup
}

func NewManager(root string, runtime runtimes.Runtime, workers int) (*Manager, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := NewStore(root)
	if err != nil {
		return nil, err
	}
	initialized := false
	defer func() {
		if !initialized {
			store.Close()
		}
	}()
	if err := store.Recover(); err != nil {
		return nil, err
	}
	if err := runtime.Available(ctx); err != nil {
		return nil, err
	}
	if cleaner, ok := runtime.(runtimes.LeftoverCleaner); ok {
		ids, err := cleaner.Leftovers(ctx)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if err := cleaner.DestroyByID(ctx, id); err != nil {
				return nil, err
			}
		}
	}
	if workers <= 0 {
		workers = 2
	}
	manager := &Manager{
		Store:   store,
		root:    filepath.Join(root, ".flatrun", "runs"),
		runtime: runtime,
		queue:   make(chan *pending, 128),
		active:  map[string]*pending{},
	}
	for range workers {
		manager.wg.Add(1)
		go manager.worker()
	}
	initialized = true
	return manager, nil
}

func (m *Manager) Submit(owner int64, username string, spec Spec, secrets map[string]string) (*Run, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	environment := map[string]string{}
	for name, value := range spec.Env {
		environment[name] = value
	}
	spec.Env = environment
	secretValues := map[string]string{}
	for name, value := range secrets {
		secretValues[name] = value
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	if len(m.queue) == cap(m.queue) {
		return nil, ErrQueueFull
	}
	run := Run{
		ID:            uuid.NewString(),
		OwnerUserID:   owner,
		OwnerUsername: username,
		Runtime:       spec.Runtime,
		Spec:          spec,
		Status:        StatusQueued,
		Artifacts:     []Artifact{},
		CreatedAt:     time.Now().UTC(),
	}
	if err := m.Store.Save(&run); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	queued := &pending{run: run, secrets: secretValues, ctx: ctx, cancel: cancel}
	m.active[run.ID] = queued
	m.queue <- queued
	return &run, nil
}

func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	queued := m.active[id]
	if queued == nil {
		return nil
	}
	queued.cancel()
	if queued.run.Status == StatusQueued {
		clear(queued.secrets)
		queued.secrets = nil
		queued.run.Status = StatusCancelled
		now := time.Now().UTC()
		queued.run.FinishedAt = &now
		return m.Store.Save(&queued.run)
	}
	return nil
}

func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	for _, queued := range m.active {
		queued.cancel()
	}
	close(m.queue)
	m.mu.Unlock()
	m.wg.Wait()
	return m.Store.Close()
}

func (m *Manager) LogPath(id string) string {
	return filepath.Join(m.root, id, "output.log")
}

func (m *Manager) ArtifactPath(id, name string) string {
	return filepath.Join(m.root, id, "artifacts", name)
}

func (m *Manager) worker() {
	defer m.wg.Done()
	for queued := range m.queue {
		m.execute(queued)
	}
}

func (m *Manager) finishRun(run *Run) {
	now := time.Now().UTC()
	run.FinishedAt = &now
	if err := m.Store.Save(run); err != nil {
		log.Printf("Run %s: cannot persist final status", run.ID)
	}
}

func (m *Manager) execute(queued *pending) {
	ctx, cancel := context.WithTimeout(queued.ctx, time.Duration(queued.run.Spec.TimeoutSeconds)*time.Second)
	defer cancel()
	m.mu.Lock()
	if queued.ctx.Err() != nil {
		clear(queued.secrets)
		queued.secrets = nil
		queued.run.Status = StatusCancelled
		m.finishRun(&queued.run)
		delete(m.active, queued.run.ID)
		m.mu.Unlock()
		return
	}
	queued.run.Status = StatusStarting
	if err := m.Store.Save(&queued.run); err != nil {
		queued.cancel()
		clear(queued.secrets)
		delete(m.active, queued.run.ID)
		m.mu.Unlock()
		log.Printf("Run %s: cannot persist starting status", queued.run.ID)
		return
	}
	run := queued.run
	m.mu.Unlock()
	defer func() {
		queued.cancel()
		clear(queued.secrets)
		m.mu.Lock()
		delete(m.active, run.ID)
		m.mu.Unlock()
	}()
	defer m.finishRun(&run)

	artifactDirectory := filepath.Join(m.root, run.ID, "artifacts")
	if err := os.MkdirAll(artifactDirectory, 0700); err != nil {
		run.Status = StatusFailed
		run.Error = "cannot create run directory"
		return
	}
	sanitizer := newRedactor(queued.secrets)
	instance, err := m.startRun(ctx, queued, &run)
	if instance != nil {
		defer func() {
			cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := instance.Destroy(cleanupContext); err != nil {
				run.Status = StatusFailed
				run.Error = sanitizer.redact(fmt.Sprintf("instance cleanup failed: %v", err))
			}
		}()
	}
	if err != nil {
		run.Status = StatusFailed
		if ctx.Err() != nil {
			run.Status = StatusCancelled
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				run.Status = StatusTimedOut
			}
		}
		run.Error = sanitizer.redact(err.Error())
		return
	}

	logContext, cancelLogs := context.WithCancel(context.Background())
	defer cancelLogs()
	logsDone := make(chan error, 1)
	go func() {
		logsDone <- m.collectLogs(logContext, run.ID, instance, sanitizer)
	}()
	m.waitRun(ctx, &run, instance, sanitizer)
	select {
	case logError := <-logsDone:
		if logError != nil {
			run.Error = sanitizer.redact(fmt.Sprintf("log collection failed: %v", logError))
			if run.Status == StatusSucceeded {
				run.Status = StatusFailed
			}
		}
	case <-time.After(3 * time.Second):
		run.Error = "log collection timed out"
		if run.Status == StatusSucceeded {
			run.Status = StatusFailed
		}
		cancelLogs()
		<-logsDone
	}
	m.collectArtifacts(&run, instance, artifactDirectory, sanitizer)
}

func (m *Manager) startRun(ctx context.Context, queued *pending, run *Run) (runtimes.Instance, error) {
	environment := map[string]string{}
	for name, value := range run.Spec.Env {
		environment[name] = value
	}
	for name, value := range queued.secrets {
		environment[name] = value
	}
	clear(queued.secrets)
	queued.secrets = nil
	defer clear(environment)
	instance, err := m.runtime.Create(ctx, runtimes.Spec{
		Image:     run.Spec.Image,
		Command:   run.Spec.Command,
		Env:       environment,
		WorkDir:   run.Spec.WorkDir,
		Resources: runtimes.Resources{CPUs: run.Spec.CPUs, Memory: run.Spec.MemoryBytes},
		Network:   runtimes.Network{Egress: run.Spec.Egress},
	})
	if err != nil {
		return instance, fmt.Errorf("instance creation failed: %w", err)
	}
	if err := instance.Start(ctx); err != nil {
		return instance, fmt.Errorf("instance start failed: %w", err)
	}
	now := time.Now().UTC()
	run.StartedAt = &now
	run.Status = StatusRunning
	m.mu.Lock()
	queued.run.Status = StatusRunning
	err = m.Store.Save(run)
	m.mu.Unlock()
	if err != nil {
		return instance, fmt.Errorf("cannot persist running status: %w", err)
	}
	return instance, nil
}

func (m *Manager) waitRun(ctx context.Context, run *Run, instance runtimes.Instance, sanitizer redactor) {
	exit, err := instance.Wait(ctx)
	cause := ctx.Err()
	run.Status = StatusSucceeded
	if cause != nil {
		stopContext, cancelStop := context.WithTimeout(context.Background(), 5*time.Second)
		stopError := instance.Stop(stopContext, time.Second)
		cancelStop()
		run.Status = StatusCancelled
		if errors.Is(cause, context.DeadlineExceeded) {
			run.Status = StatusTimedOut
		}
		if stopError != nil {
			run.Error = sanitizer.redact(fmt.Sprintf("instance stop failed: %v", stopError))
		}
	} else if err != nil {
		run.Status = StatusFailed
		run.Error = sanitizer.redact(fmt.Sprintf("instance wait failed: %v", err))
	} else {
		run.ExitCode = &exit.Code
		if exit.Code != 0 {
			run.Status = StatusFailed
		}
	}
}

func (m *Manager) collectLogs(ctx context.Context, id string, instance runtimes.Instance, sanitizer redactor) error {
	output, err := os.OpenFile(m.LogPath(id), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer output.Close()
	stream, err := instance.Logs(ctx, true)
	if err != nil {
		return err
	}
	defer stream.Close()
	return sanitizer.copy(output, stream)
}

func (m *Manager) collectArtifacts(run *Run, instance runtimes.Instance, directory string, sanitizer redactor) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, artifactPath := range run.Spec.ArtifactPaths {
		stream, err := instance.CopyOut(ctx, artifactPath)
		if err != nil {
			run.Error = sanitizer.redact(fmt.Sprintf("artifact collection failed: %v", err))
			continue
		}
		name := path.Base(artifactPath) + ".tar"
		output, err := os.OpenFile(filepath.Join(directory, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			stream.Close()
			run.Error = "artifact storage failed"
			continue
		}
		size, copyError := io.Copy(output, stream)
		closeError := output.Close()
		stream.Close()
		if copyError != nil || closeError != nil {
			run.Error = "artifact storage failed"
			continue
		}
		run.Artifacts = append(run.Artifacts, Artifact{Name: name, Size: size})
	}
}

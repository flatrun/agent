package api

import (
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type JobStatus string

const (
	JobPending   JobStatus = "pending"
	JobRunning   JobStatus = "running"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
)

// jobRetention is how long a finished job stays queryable so a page reloaded
// shortly after the action still sees its result. Jobs are in-memory only and
// do not survive an agent restart.
const jobRetention = 15 * time.Minute

// ActionJob tracks one start/stop/restart/rebuild run for a deployment. Its
// output lines are buffered as they arrive and fanned out to any live
// subscribers (the websocket stream).
type ActionJob struct {
	id         string
	deployment string
	service    string
	action     string
	activeKey  string

	mu          sync.Mutex
	status      JobStatus
	lines       []string
	errMsg      string
	startedAt   time.Time
	finishedAt  time.Time
	subscribers map[int]chan string
	nextSub     int
}

func (j *ActionJob) ID() string         { return j.id }
func (j *ActionJob) Deployment() string { return j.deployment }
func (j *ActionJob) Service() string    { return j.service }

func (j *ActionJob) setRunning() {
	j.mu.Lock()
	if j.status == JobPending {
		j.status = JobRunning
	}
	j.mu.Unlock()
}

func (j *ActionJob) appendLine(line string) {
	j.mu.Lock()
	j.lines = append(j.lines, line)
	for _, c := range j.subscribers {
		select {
		case c <- line:
		default:
			// Slow subscriber: drop the live line rather than block the action.
			// The full buffer is still available via the status endpoint.
		}
	}
	j.mu.Unlock()
}

func (j *ActionJob) finish(status JobStatus, errMsg string) {
	j.mu.Lock()
	j.status = status
	j.errMsg = errMsg
	j.finishedAt = time.Now()
	for id, c := range j.subscribers {
		close(c)
		delete(j.subscribers, id)
	}
	j.mu.Unlock()
}

// subscribe atomically returns the lines buffered so far plus a channel that
// receives every subsequent line. The channel is closed when the job finishes.
// If the job is already finished, the returned channel is closed immediately.
func (j *ActionJob) subscribe() (buffered []string, ch <-chan string, cancel func()) {
	j.mu.Lock()
	defer j.mu.Unlock()

	buffered = append([]string(nil), j.lines...)

	if j.status == JobSucceeded || j.status == JobFailed {
		closed := make(chan string)
		close(closed)
		return buffered, closed, func() {}
	}

	id := j.nextSub
	j.nextSub++
	c := make(chan string, 256)
	j.subscribers[id] = c
	cancel = func() {
		j.mu.Lock()
		if sc, ok := j.subscribers[id]; ok {
			delete(j.subscribers, id)
			close(sc)
		}
		j.mu.Unlock()
	}
	return buffered, c, cancel
}

type JobSnapshot struct {
	ID         string     `json:"id"`
	Deployment string     `json:"deployment"`
	Service    string     `json:"service,omitempty"`
	Action     string     `json:"action"`
	Status     JobStatus  `json:"status"`
	Output     string     `json:"output"`
	Lines      []string   `json:"lines"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

func (j *ActionJob) snapshot() JobSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()

	snap := JobSnapshot{
		ID:         j.id,
		Deployment: j.deployment,
		Service:    j.service,
		Action:     j.action,
		Status:     j.status,
		Output:     strings.Join(j.lines, "\n"),
		Lines:      append([]string(nil), j.lines...),
		Error:      j.errMsg,
		StartedAt:  j.startedAt,
	}
	if !j.finishedAt.IsZero() {
		ft := j.finishedAt
		snap.FinishedAt = &ft
	}
	return snap
}

// jobRegistry holds action jobs in memory and enforces one in-flight action
// per deployment.
type jobRegistry struct {
	mu     sync.Mutex
	jobs   map[string]*ActionJob
	active map[string]string // deployment -> active job id
}

func newJobRegistry() *jobRegistry {
	return &jobRegistry{
		jobs:   make(map[string]*ActionJob),
		active: make(map[string]string),
	}
}

// create registers a new pending job for the deployment. If an action is
// already in flight for that deployment it returns the existing job and false,
// so the caller can reject the request and point the client at the live job.
func (r *jobRegistry) create(deployment, action string) (*ActionJob, bool) {
	return r.createScoped(deployment, "", action)
}

// createScoped registers a job serialized on the deployment, or on a single
// service within it when service is set, so two different services can act
// concurrently while the same service cannot.
func (r *jobRegistry) createScoped(deployment, service, action string) (*ActionJob, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.evictLocked(time.Now())

	key := deployment
	if service != "" {
		key = deployment + "\x00" + service
	}

	if id, ok := r.active[key]; ok {
		if existing := r.jobs[id]; existing != nil {
			return existing, false
		}
		delete(r.active, key)
	}

	job := &ActionJob{
		id:          uuid.NewString(),
		deployment:  deployment,
		service:     service,
		action:      action,
		activeKey:   key,
		status:      JobPending,
		startedAt:   time.Now(),
		subscribers: make(map[int]chan string),
	}
	r.jobs[job.id] = job
	r.active[key] = job.id
	return job, true
}

// finish marks the job terminal and frees its slot for the next action.
func (r *jobRegistry) finish(job *ActionJob, status JobStatus, errMsg string) {
	r.mu.Lock()
	if r.active[job.activeKey] == job.id {
		delete(r.active, job.activeKey)
	}
	r.mu.Unlock()

	job.finish(status, errMsg)
}

func (r *jobRegistry) get(id string) *ActionJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.jobs[id]
}

func (r *jobRegistry) activeFor(deployment string) *ActionJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id, ok := r.active[deployment]; ok {
		return r.jobs[id]
	}
	return nil
}

func (r *jobRegistry) evictLocked(now time.Time) {
	for id, job := range r.jobs {
		job.mu.Lock()
		finished := job.status == JobSucceeded || job.status == JobFailed
		expired := finished && !job.finishedAt.IsZero() && now.Sub(job.finishedAt) > jobRetention
		job.mu.Unlock()
		if expired {
			delete(r.jobs, id)
		}
	}
}

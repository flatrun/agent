package api

import (
	"log"
	"sync"

	"github.com/flatrun/agent/internal/deploys"
)

// recordedActions are the actions that produce a new running version and so
// get a deploy record with stored output.
var recordedActions = map[string]bool{"redeploy": true, "rebuild": true}

// deployProgress collects what a running deploy reports about itself. Its
// methods do nothing on a nil receiver, so actions run outside a recorded
// deploy can call them freely.
type deployProgress struct {
	setPhase func(deploys.Phase)

	mu       sync.Mutex
	revision string
	message  string
}

func (p *deployProgress) phase(phase deploys.Phase) {
	if p != nil && p.setPhase != nil {
		p.setPhase(phase)
	}
}

func (p *deployProgress) fetched(revision, message string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.revision, p.message = revision, message
	p.mu.Unlock()
}

// recordDeploy starts a deploy record for a job and returns the emit function
// that also stores output, the options that report progress, and the function
// that closes the record when the action ends.
func (s *Server) recordDeploy(job *ActionJob) (func(string), actionOptions, func(error)) {
	opts := job.opts
	rec := deploys.Record{Action: job.action, Revision: opts.Revision, Trigger: deploys.Trigger(opts.Trigger)}
	if d, err := s.manager.GetDeployment(job.deployment); err == nil && d.Metadata != nil && d.Metadata.Source != nil {
		rec.Branch = d.Metadata.Source.Branch
	}
	started, err := s.deploysStore.Start(job.deployment, rec)
	if err != nil {
		job.appendLine("Warning: failed to record deploy: " + err.Error())
		return job.appendLine, opts, func(error) {}
	}

	emit := job.appendLine
	writer, err := s.deploysStore.OpenLog(job.deployment, started.ID)
	if err != nil {
		log.Printf("Warning: failed to store output of deploy %s/%s: %v", job.deployment, started.ID, err)
	} else {
		emit = func(line string) {
			job.appendLine(line)
			writer.Line(line)
		}
	}

	progress := &deployProgress{setPhase: func(phase deploys.Phase) {
		job.setPhase(string(phase))
		if err := s.deploysStore.SetPhase(job.deployment, started.ID, phase); err != nil {
			log.Printf("Warning: failed to record deploy phase for %s: %v", job.deployment, err)
		}
	}}
	opts.progress = progress
	job.setDeploy(started.ID)
	job.setPhase(string(deploys.PhaseQueued))

	finish := func(runErr error) {
		progress.mu.Lock()
		revision, message := progress.revision, progress.message
		progress.mu.Unlock()
		if err := s.deploysStore.Finish(job.deployment, started.ID, revision, message, runErr); err != nil {
			log.Printf("Warning: failed to close deploy record for %s: %v", job.deployment, err)
		}
		if writer != nil {
			_ = writer.Close()
		}
		if runErr != nil {
			job.setPhase(string(deploys.PhaseFailed))
		} else {
			job.setPhase(string(deploys.PhaseActive))
		}
	}
	return emit, opts, finish
}

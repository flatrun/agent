package securityreview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/flatrun/agent/pkg/models"
	"github.com/flatrun/agent/pkg/securityprofile"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

type Engine struct {
	DeliveryEnabled bool
	ModelEnabled    bool
	Store           *Store
	Runner          Runner
	Keys            *EvidenceKeyring
	Deployment      func(string) (*models.Deployment, error)
	mu              sync.Mutex
	active          map[string]bool
	capacity        chan struct{}
	workers         sync.WaitGroup
}

func NewEngine(store *Store, runner Runner, keys *EvidenceKeyring, get func(string) (*models.Deployment, error)) *Engine {
	return &Engine{Store: store, Runner: runner, Keys: keys, Deployment: get, active: map[string]bool{}, capacity: make(chan struct{}, 2)}
}

func (e *Engine) Queue(dep, mode, profileRevision, deploymentRevision string) (Report, error) {
	if !slices.Contains([]string{"incremental", "retrospective", "full"}, mode) {
		return Report{}, fmt.Errorf("invalid review mode")
	}
	state, err := e.Store.State(dep)
	if err != nil {
		return Report{}, err
	}
	if state.Profile == nil || state.Approval == nil || state.Revision != profileRevision || state.Approval.ProfileRevision != profileRevision || state.Approval.DeploymentRevision != deploymentRevision {
		return Report{}, ErrConflict
	}
	r := Report{APIVersion: "flatrun.security-report/v1", ID: uuid.NewString(), Deployment: dep, ProfileRevision: profileRevision, DeploymentRevision: deploymentRevision, Mode: mode, Status: "queued", ModelStatus: "unavailable", Coverage: []Coverage{}, Findings: []Finding{}, RequestedAt: time.Now().UTC(), DeliveryStatus: "not_configured", Limitations: []string{"Historical application authorization evidence is not connected", "Off-host evidence delivery is not configured", "Schedule capacity has not been measured"}}
	r.ProposalID = state.Approval.ProposalID
	r.WindowEnd = r.RequestedAt
	r.WindowStart = r.WindowEnd.Add(-5 * time.Minute)
	if mode == "retrospective" {
		r.WindowStart = r.WindowEnd.Add(-time.Hour - 15*time.Minute)
	}
	if mode == "full" {
		r.WindowStart = r.WindowEnd.Add(-24 * time.Hour)
	}
	if e.ModelEnabled {
		r.ModelStatus = "pending"
	}
	if e.DeliveryEnabled {
		r.DeliveryStatus = "pending"
		r.Limitations = slices.DeleteFunc(r.Limitations, func(v string) bool { return v == "Off-host evidence delivery is not configured" })
	}
	return r, e.Store.SaveReport(r)
}

func (e *Engine) Process(ctx context.Context, r Report) error {
	if !e.claim(r.Deployment) {
		return nil
	}
	defer e.release(r.Deployment)
	return e.process(ctx, r)
}
func (e *Engine) claim(dep string) bool {
	e.mu.Lock()
	if e.active[dep] {
		e.mu.Unlock()
		return false
	}
	select {
	case e.capacity <- struct{}{}:
	default:
		e.mu.Unlock()
		return false
	}
	e.active[dep] = true
	e.mu.Unlock()
	return true
}
func (e *Engine) release(dep string) { <-e.capacity; e.mu.Lock(); delete(e.active, dep); e.mu.Unlock() }
func (e *Engine) Wait()              { e.workers.Wait() }
func (e *Engine) process(ctx context.Context, r Report) error {
	d, err := e.Deployment(r.Deployment)
	if err != nil {
		return e.fail(r, "Deployment discovery unavailable")
	}
	discovery, err := Discover(d)
	if err != nil {
		return e.fail(r, "Deployment discovery unavailable")
	}
	state, err := e.Store.State(d.Name)
	if err != nil {
		return err
	}
	if state.Profile == nil || state.Approval == nil || state.Revision != r.ProfileRevision || state.Approval.ProfileRevision != r.ProfileRevision || state.Approval.DeploymentRevision != discovery.Revision || discovery.Revision != r.DeploymentRevision {
		return e.fail(r, "Approved profile or deployment revision changed")
	}
	if err = state.Profile.Validate(); err != nil {
		return e.fail(r, "Approved profile is unsupported")
	}
	p := *state.Profile
	r.Status = "collecting"
	r.Coverage = discovery.Coverage
	if err = e.Store.SaveReport(r); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	ids := make([]string, 0, len(p.Reviews))
	for id := range p.Reviews {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		review := p.Reviews[id]
		if !slices.Contains(review.Runs, r.Mode) {
			continue
		}
		target := p.Targets[review.Target]
		var service *ServiceScope
		for i := range discovery.Services {
			if discovery.Services[i].Name == target.Service {
				service = &discovery.Services[i]
				break
			}
		}
		if service == nil {
			r.Coverage = append(r.Coverage, Coverage{review.Concern, target.Service, "", "unsupported", "Approved service was not discovered"})
			continue
		}
		for _, collector := range review.Collect {
			if ctx.Err() != nil {
				r.Limitations = append(r.Limitations, "Run budget exhausted; requested work remains uncovered")
				break
			}
			inputs := Input{}
			unsupported := ""
			if len(collector.MetadataSources) > 0 {
				metadata := map[string]any{}
				for _, source := range collector.MetadataSources {
					if source == "access_policy" {
						metadata[source] = map[string]any{"policy": p.AccessPolicy, "profile_revision": r.ProfileRevision}
						continue
					}
					if source != "inventory" {
						unsupported = "Required metadata exporter is unavailable"
						break
					}
					metadata[source] = service
				}
				if unsupported == "" {
					inputs.Metadata, err = WriteMetadata(e.Runner.ScratchRoot, metadata)
					if err != nil {
						unsupported = "Metadata snapshot unavailable"
					}
				}
			}
			var snapshot Snapshot
			if len(collector.SnapshotPaths) > 0 {
				if len(target.Roots) != 1 {
					unsupported = "File collectors require one explicit root per target"
				} else {
					source := ""
					for _, mount := range service.Mounts {
						if mount.Target == target.Roots[0] && mount.Type == "bind" {
							source = mount.Source
							if !filepath.IsAbs(source) {
								source = filepath.Join(d.Path, source)
							}
							break
						}
					}
					if source == "" {
						unsupported = "Approved root has no supported retained bind snapshot"
					} else {
						snapshot, err = SnapshotFiles(e.Runner.ScratchRoot, source, collector.SnapshotPaths, p.Budget)
						if err != nil {
							unsupported = "Approved snapshot is unavailable"
						} else {
							inputs.Snapshot = snapshot.Path
						}
					}
				}
			}
			if unsupported != "" {
				if inputs.Metadata != "" {
					_ = os.RemoveAll(inputs.Metadata)
				}
				if inputs.Snapshot != "" {
					_ = os.RemoveAll(inputs.Snapshot)
				}
				r.Coverage = append(r.Coverage, Coverage{review.Concern, target.Service, "", "unsupported", unsupported})
				continue
			}
			manifestProcessing := "unknown"
			var pendingManifest *Manifest
			if inputs.Snapshot != "" {
				manifest, manifestErr := e.Store.ReconcileSnapshot(d.Name, id+"."+collector.ID, snapshot)
				if manifestErr != nil {
					return manifestErr
				}
				manifestProcessing = manifest.ManifestProcessing
				pendingManifest = &manifest
				inputs.Manifest, err = WriteMetadata(e.Runner.ScratchRoot, manifest)
				if err != nil {
					return err
				}
			}
			result, runErr := e.Runner.Run(ctx, collector, p.Budget, inputs)
			for _, temporary := range []string{inputs.Metadata, inputs.Snapshot, inputs.Manifest} {
				if temporary != "" {
					_ = os.RemoveAll(temporary)
				}
			}
			result.Coverage.ManifestProcessing = manifestProcessing
			if runErr != nil || result.ProcessStatus != "completed" {
				result.Coverage.ManifestProcessing = "unknown"
			}
			result.Omissions = append(result.Omissions, snapshot.Omissions...)
			result.Boundaries = append(result.Boundaries, snapshot.Boundaries...)
			if len(result.Omissions) > 0 || len(result.Boundaries) > 0 {
				result.Coverage.DeclaredObservation = "partial"
			}
			status := result.Coverage.DeclaredObservation
			reason := "Collector observations cover only the declared scope"
			if runErr != nil {
				status = "unsupported"
				reason = runErr.Error()
			}
			r.Coverage = append(r.Coverage, Coverage{review.Concern, target.Service, "", status, reason})
			body, err := json.Marshal(result)
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			envelope := Envelope{APIVersion: "flatrun.security-evidence/v1", Source: id + "." + collector.ID, SourceVersion: p.Version, Deployment: d.Name, Run: r.ID, ProfileRevision: r.ProfileRevision, DeploymentRevision: r.DeploymentRevision, WindowStart: r.RequestedAt, WindowEnd: now, Cursor: 0, NextCursor: 1, Count: 1, Total: 1, Final: true, WindowComplete: false, Items: []json.RawMessage{body}}
			if e.Keys == nil {
				return e.fail(r, "Evidence signing unavailable")
			}
			if err = e.Keys.Sign(&envelope, "v1"); err != nil {
				return err
			}
			var findings []Finding
			if runErr != nil || result.ProcessStatus != "completed" {
				findings = append(findings, Finding{ID: securityprofile.Digest([]string{d.Name, id, collector.ID, "collection_incomplete"}), RunID: r.ID, Status: "open", Concern: "monitoring", Classification: "suspicious_condition", Signal: "collection_incomplete", EvidenceID: securityprofile.Digest(envelope), FirstSeen: now, LastSeen: now, Occurrences: 1})
			}
			for _, signal := range collector.Signals {
				matched, known := SignalMatch(signal, body)
				if !known {
					r.Limitations = append(r.Limitations, "Signal "+signal.ID+" has unknown evidence")
				}
				if known && matched {
					classification := signal.Classification
					if classification == "" {
						classification = "suspicious_condition"
					}
					findings = append(findings, Finding{ID: securityprofile.Digest([]string{d.Name, review.Concern, id, collector.ID, signal.ID, observationRevision(result.Items)}), RunID: r.ID, Status: "open", Concern: review.Concern, Classification: classification, Signal: signal.ID, EvidenceID: securityprofile.Digest(envelope), FirstSeen: now, LastSeen: now, Occurrences: 1})
				}
			}
			var completedManifests []Manifest
			if pendingManifest != nil && runErr == nil && result.ProcessStatus == "completed" {
				completedManifests = append(completedManifests, *pendingManifest)
			}
			if err = e.Store.CommitPage(envelope, r, findings, completedManifests...); err != nil {
				return err
			}
			r.Findings = append(r.Findings, findings...)
		}
	}
	r.Status = "partial"
	completed := time.Now().UTC()
	r.CompletedAt = &completed
	if err = e.Store.SaveReport(r); err != nil {
		return err
	}
	return e.Store.Retain(completed)
}
func (e *Engine) fail(r Report, reason string) error {
	if stored, err := e.Store.Report(r.Deployment, r.ID); err == nil {
		r = stored
	}
	r.Status = "failed"
	now := time.Now().UTC()
	r.CompletedAt = &now
	r.Limitations = append(r.Limitations, reason)
	return e.Store.SaveReport(r)
}

func (e *Engine) Poll(ctx context.Context) error {
	rows, err := e.Store.db.Query("SELECT body FROM reports WHERE json_extract(body,'$.status')='queued' ORDER BY CASE WHEN created<strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 hour') THEN -1 WHEN json_extract(body,'$.mode')='incremental' THEN 0 WHEN json_extract(body,'$.mode')='retrospective' THEN 1 ELSE 2 END,created LIMIT 1000")
	if err != nil {
		return err
	}
	var queued []Report
	for rows.Next() {
		var body []byte
		if err = rows.Scan(&body); err != nil {
			rows.Close()
			return err
		}
		var r Report
		if err = json.Unmarshal(body, &r); err != nil {
			rows.Close()
			return err
		}
		if r.Status == "queued" {
			queued = append(queued, r)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range queued {
		if ctx.Err() != nil {
			break
		}
		if !e.claim(r.Deployment) {
			continue
		}
		e.workers.Add(1)
		go func(r Report) {
			defer e.workers.Done()
			defer e.release(r.Deployment)
			if err := e.process(ctx, r); err != nil {
				_ = e.fail(r, "Review interrupted before durable completion")
			}
		}(r)
	}
	return nil
}

func (e *Engine) Schedule(now time.Time) error {
	rows, err := e.Store.db.Query("SELECT deployment,body FROM profiles")
	if err != nil {
		return err
	}
	type entry struct {
		dep   string
		state State
	}
	var entries []entry
	for rows.Next() {
		var dep string
		var body []byte
		if err = rows.Scan(&dep, &body); err != nil {
			rows.Close()
			return err
		}
		var state State
		if err = json.Unmarshal(body, &state); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, entry{dep, state})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		state := entry.state
		if state.Profile == nil || state.Approval == nil || state.Approval.ProfileRevision != state.Revision {
			continue
		}
		if e.Deployment != nil {
			deployment, discoveryErr := e.Deployment(entry.dep)
			if discoveryErr != nil {
				continue
			}
			scope, discoveryErr := Discover(deployment)
			if discoveryErr != nil || scope.Revision != state.Approval.DeploymentRevision {
				continue
			}
		}
		var backlog int
		if err = e.Store.db.QueryRow("SELECT COUNT(*) FROM reports WHERE deployment=? AND json_extract(body,'$.status')='queued'", entry.dep).Scan(&backlog); err != nil {
			return err
		}
		if backlog >= 100 {
			continue
		}
		for _, mode := range []string{"incremental", "retrospective", "full"} {
			spec := state.Profile.Schedule.Incremental
			if mode == "retrospective" {
				spec = state.Profile.Schedule.Retrospective
			}
			if mode == "full" {
				spec = state.Profile.Schedule.Full
			}
			schedule, err := cron.ParseStandard(spec)
			if err != nil {
				continue
			}
			last := state.Approval.ApprovedAt
			revision := state.Revision + ":" + state.Approval.DeploymentRevision
			var saved string
			err = e.Store.db.QueryRow("SELECT last_due FROM schedule_ticks WHERE deployment=? AND mode=? AND revision=?", entry.dep, mode, revision).Scan(&saved)
			if err == nil {
				last, err = time.Parse(time.RFC3339Nano, saved)
				if err != nil {
					return err
				}
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			for count := 0; count < 20 && backlog < 100; count++ {
				due := schedule.Next(last.UTC())
				if due.After(now.UTC()) {
					break
				}
				lookback := time.Hour
				if mode == "full" {
					lookback = 24 * time.Hour
				}
				cutoff := now.UTC().Add(-lookback).Truncate(time.Minute)
				coalesced := due.Before(cutoff)
				if coalesced {
					due = cutoff
				}
				backlog++
				r := Report{APIVersion: "flatrun.security-report/v1", ID: "schedule-" + securityprofile.Digest([]string{entry.dep, mode, revision, due.Format(time.RFC3339Nano)}), Deployment: entry.dep, ProfileRevision: state.Revision, DeploymentRevision: state.Approval.DeploymentRevision, Mode: mode, Status: "queued", ModelStatus: "unavailable", WindowStart: last, WindowEnd: due, RequestedAt: now.UTC(), Coverage: []Coverage{}, Findings: []Finding{}, DeliveryStatus: "not_configured", Limitations: []string{"Historical window completeness is unknown", "Off-host delivery is not configured", "Schedule capacity has not been measured"}}
				if coalesced {
					r.Limitations = append(r.Limitations, "Missed schedule windows coalesced; historical completeness remains unknown")
				}
				r.ProposalID = state.Approval.ProposalID
				if e.ModelEnabled {
					r.ModelStatus = "pending"
				}
				if e.DeliveryEnabled {
					r.DeliveryStatus = "pending"
					r.Limitations = slices.DeleteFunc(r.Limitations, func(v string) bool { return v == "Off-host delivery is not configured" })
				}
				body, err := json.Marshal(r)
				if err != nil {
					return err
				}
				tx, err := e.Store.db.Begin()
				if err != nil {
					return err
				}
				if err = reportGeneration(tx, r); err != nil {
					tx.Rollback()
					if errors.Is(err, ErrConflict) {
						break
					}
					return err
				}
				if _, err = tx.Exec("INSERT OR IGNORE INTO reports VALUES (?,?,?,?)", r.Deployment, r.ID, r.RequestedAt.Format(time.RFC3339Nano), body); err == nil {
					_, err = tx.Exec("INSERT INTO schedule_ticks VALUES (?,?,?,?) ON CONFLICT(deployment,mode,revision) DO UPDATE SET last_due=excluded.last_due", entry.dep, mode, revision, due.Format(time.RFC3339Nano))
				}
				if err != nil {
					tx.Rollback()
					return err
				}
				if err = tx.Commit(); err != nil {
					return err
				}
				last = due
			}
		}
	}
	return nil
}

func (e *Engine) Prepare() error {
	rows, err := e.Store.db.Query("SELECT body FROM reports WHERE json_extract(body,'$.status') IN ('collecting','reviewing')")
	if err != nil {
		return err
	}
	var interrupted []Report
	for rows.Next() {
		var body []byte
		if err = rows.Scan(&body); err != nil {
			rows.Close()
			return err
		}
		var r Report
		if err = json.Unmarshal(body, &r); err != nil {
			rows.Close()
			return err
		}
		interrupted = append(interrupted, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range interrupted {
		if err = e.fail(r, "Collection interrupted; completed evidence retained and historical coverage remains unknown"); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(e.Runner.ScratchRoot, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(e.Runner.ScratchRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "snapshot-") || strings.HasPrefix(entry.Name(), "metadata-") {
			if err = os.RemoveAll(filepath.Join(e.Runner.ScratchRoot, entry.Name())); err != nil {
				return err
			}
		}
	}
	if e.Keys != nil {
		if err := e.Keys.EnsureKey("v1"); err != nil {
			return err
		}
	}
	return nil
}

func observationRevision(items []json.RawMessage) string {
	hashes := []string{}
	fallback := []string{}
	for _, item := range items {
		var observation map[string]json.RawMessage
		if json.Unmarshal(item, &observation) != nil {
			continue
		}
		if _, ok := observation["manifest_changes"]; ok {
			continue
		}
		if _, ok := observation["indicator_count"]; ok {
			continue
		}
		fallback = append(fallback, securityprofile.Digest(item))
		if _, ok := observation["indicators"]; ok {
			hashes = append(hashes, securityprofile.Digest(item))
		}
	}
	if len(hashes) == 0 {
		hashes = fallback
	}
	slices.Sort(hashes)
	return securityprofile.Digest(hashes)
}

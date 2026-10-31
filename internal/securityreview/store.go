package securityreview

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flatrun/agent/pkg/securityprofile"
	"github.com/gofrs/flock"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

var ErrConflict = errors.New("security revision changed")
var ErrNotFound = errors.New("security record not found")

type Store struct {
	root string
	db   *sql.DB
	lock *flock.Flock
}
type Approval struct {
	ProposalID         string    `json:"proposal_id"`
	ProfileRevision    string    `json:"profile_revision"`
	DeploymentRevision string    `json:"deployment_revision"`
	Actor              string    `json:"actor"`
	ApprovedAt         time.Time `json:"approved_at"`
}
type State struct {
	Profile  *securityprofile.Profile `json:"profile"`
	Revision string                   `json:"revision"`
	Approval *Approval                `json:"approval"`
	Status   string                   `json:"status"`
}
type Proposal struct {
	Error              string                  `json:"error,omitempty"`
	APIVersion         string                  `json:"api_version"`
	ID                 string                  `json:"id"`
	Deployment         string                  `json:"deployment"`
	BaseRevision       string                  `json:"base_revision"`
	DeploymentRevision string                  `json:"deployment_revision"`
	ProfileRevision    string                  `json:"profile_revision"`
	Profile            securityprofile.Profile `json:"profile"`
	Source             string                  `json:"source"`
	Status             string                  `json:"status"`
	CreatedAt          time.Time               `json:"created_at"`
}
type Coverage struct {
	Concern string `json:"concern"`
	Service string `json:"service,omitempty"`
	Root    string `json:"root,omitempty"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
}
type Finding struct {
	RunID          string    `json:"run_id"`
	Status         string    `json:"status"`
	ID             string    `json:"id"`
	Concern        string    `json:"concern"`
	Classification string    `json:"classification"`
	Signal         string    `json:"signal"`
	EvidenceID     string    `json:"evidence_id"`
	FirstSeen      time.Time `json:"first_seen"`
	LastSeen       time.Time `json:"last_seen"`
	Occurrences    int       `json:"occurrences"`
}
type Report struct {
	ProposalID         string           `json:"proposal_id"`
	WindowStart        time.Time        `json:"window_start"`
	WindowEnd          time.Time        `json:"window_end"`
	QueueState         string           `json:"queue_state,omitempty"`
	ModelAssessment    *ModelAssessment `json:"model_assessment,omitempty"`
	APIVersion         string           `json:"api_version"`
	ID                 string           `json:"id"`
	Deployment         string           `json:"deployment"`
	ProfileRevision    string           `json:"profile_revision"`
	DeploymentRevision string           `json:"deployment_revision"`
	Mode               string           `json:"mode"`
	Status             string           `json:"status"`
	ModelStatus        string           `json:"model_status"`
	Coverage           []Coverage       `json:"coverage"`
	Findings           []Finding        `json:"findings"`
	RequestedAt        time.Time        `json:"requested_at"`
	CompletedAt        *time.Time       `json:"completed_at"`
	Hold               bool             `json:"hold"`
	DeliveryStatus     string           `json:"delivery_status"`
	Limitations        []string         `json:"limitations"`
}

func NewStore(root, deployments string) (*Store, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("security state requires an absolute private directory")
	}
	resolved, err := filepath.EvalSymlinks(deployments)
	if err != nil {
		return nil, err
	}
	clean := filepath.Clean(root)
	for p := clean; p != filepath.Dir(p); p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e == nil && info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("security state cannot use symbolic links")
		}
		if e != nil && !os.IsNotExist(e) {
			return nil, e
		}
	}
	rel, err := filepath.Rel(resolved, clean)
	if err != nil || (rel != ".." && !startsParent(rel)) {
		return nil, fmt.Errorf("security state must be outside deployment storage")
	}
	if err := os.MkdirAll(clean, 0700); err != nil {
		return nil, err
	}
	info, err := os.Stat(clean)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("security state directory must have mode 0700")
	}
	lock := flock.New(filepath.Join(clean, "agent.lock"))
	locked, err := lock.TryLock()
	if err != nil || !locked {
		_ = lock.Close()
		return nil, fmt.Errorf("security state is already owned by another agent or cannot be locked")
	}
	db, err := sql.Open("sqlite", filepath.Join(clean, "reviews.db"))
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS retired_deployments (deployment TEXT PRIMARY KEY, retired_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS profiles (deployment TEXT PRIMARY KEY, body BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS proposals (deployment TEXT NOT NULL, id TEXT NOT NULL, body BLOB NOT NULL, PRIMARY KEY(deployment,id));
CREATE TABLE IF NOT EXISTS reports (deployment TEXT NOT NULL, id TEXT NOT NULL, created TEXT NOT NULL, body BLOB NOT NULL, PRIMARY KEY(deployment,id));
CREATE TABLE IF NOT EXISTS evidence (deployment TEXT NOT NULL, run TEXT NOT NULL, id TEXT NOT NULL, body BLOB NOT NULL, PRIMARY KEY(deployment,run,id));
CREATE TABLE IF NOT EXISTS findings (deployment TEXT NOT NULL, id TEXT NOT NULL, body BLOB NOT NULL, PRIMARY KEY(deployment,id));
CREATE TABLE IF NOT EXISTS finding_history (deployment TEXT NOT NULL,id TEXT NOT NULL,body BLOB NOT NULL,changed TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS manifests (deployment TEXT, scope TEXT, sequence INTEGER, body BLOB, files BLOB, PRIMARY KEY(deployment,scope,sequence));
CREATE TABLE IF NOT EXISTS holds (deployment TEXT NOT NULL, run TEXT NOT NULL, PRIMARY KEY(deployment,run));
CREATE TABLE IF NOT EXISTS schedule_ticks (deployment TEXT NOT NULL, mode TEXT NOT NULL, revision TEXT NOT NULL, last_due TEXT NOT NULL, PRIMARY KEY(deployment,mode,revision));
CREATE TABLE IF NOT EXISTS cursors (deployment TEXT NOT NULL, source TEXT NOT NULL, cursor INTEGER NOT NULL, PRIMARY KEY(deployment,source));`)
	if err != nil {
		db.Close()
		_ = lock.Close()
		return nil, err
	}
	if _, err = db.Exec("UPDATE proposals SET body=json_set(body,'$.status','failed','$.error','Profile generation was interrupted') WHERE json_extract(body,'$.status')='queued'"); err != nil {
		db.Close()
		_ = lock.Close()
		return nil, err
	}
	return &Store{root: root, db: db, lock: lock}, nil
}
func startsParent(p string) bool { return len(p) > 3 && p[:3] == ".."+string(filepath.Separator) }
func (s *Store) Close() error {
	err := s.db.Close()
	lockErr := s.lock.Close()
	if err != nil {
		return err
	}
	return lockErr
}
func loadState(q interface{ QueryRow(string, ...any) *sql.Row }, dep string) (State, error) {
	var data []byte
	err := q.QueryRow("SELECT body FROM profiles WHERE deployment=?", dep).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return State{Status: "missing"}, nil
	}
	if err != nil {
		return State{}, err
	}
	var state State
	err = json.Unmarshal(data, &state)
	return state, err
}
func (s *Store) State(dep string) (State, error) { return loadState(s.db, dep) }
func writeState(tx *sql.Tx, dep string, state State) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO profiles VALUES (?,?) ON CONFLICT(deployment) DO UPDATE SET body=excluded.body", dep, data)
	return err
}
func (s *Store) Draft(dep, base string, p securityprofile.Profile) (State, error) {
	if err := p.Validate(); err != nil {
		return State{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return State{}, err
	}
	defer tx.Rollback()
	state, err := loadState(tx, dep)
	if err != nil {
		return State{}, err
	}
	if state.Revision != base {
		return State{}, ErrConflict
	}
	state.Profile = &p
	state.Revision = securityprofile.Digest(p)
	state.Status = "draft"
	if state.Approval != nil && state.Approval.ProfileRevision != state.Revision {
		state.Status = "outdated"
	}
	if err = writeState(tx, dep, state); err != nil {
		return State{}, err
	}
	return state, tx.Commit()
}
func (s *Store) Propose(dep, base, deploymentRevision, source string, p securityprofile.Profile) (Proposal, error) {
	return s.newProposal(dep, base, deploymentRevision, source, p, "ready")
}
func (s *Store) QueueProposal(dep, base, deploymentRevision string, p securityprofile.Profile) (Proposal, error) {
	return s.newProposal(dep, base, deploymentRevision, "agent", p, "queued")
}
func (s *Store) newProposal(dep, base, deploymentRevision, source string, p securityprofile.Profile, status string) (Proposal, error) {
	started := time.Now().UTC()
	if err := p.Validate(); err != nil {
		return Proposal{}, err
	}
	state, err := s.State(dep)
	if err != nil {
		return Proposal{}, err
	}
	if state.Revision != base {
		return Proposal{}, ErrConflict
	}
	proposal := Proposal{APIVersion: "flatrun.security-proposal/v1", ID: uuid.NewString(), Deployment: dep, BaseRevision: base, DeploymentRevision: deploymentRevision, ProfileRevision: securityprofile.Digest(p), Profile: p, Source: source, Status: status, CreatedAt: time.Now().UTC()}
	data, err := json.Marshal(proposal)
	if err != nil {
		return Proposal{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Proposal{}, err
	}
	defer tx.Rollback()
	if err = reportGeneration(tx, Report{Deployment: dep, RequestedAt: started}); err != nil {
		return Proposal{}, err
	}
	if _, err = tx.Exec("INSERT INTO proposals VALUES (?,?,?)", dep, proposal.ID, data); err != nil {
		return Proposal{}, err
	}
	return proposal, tx.Commit()
}

func (s *Store) FinishProposal(dep, id string, profile securityprofile.Profile, generationErr error) error {
	p, err := s.Proposal(dep, id)
	if err != nil {
		return err
	}
	if p.Status != "queued" {
		return ErrConflict
	}
	state, err := s.State(dep)
	if err != nil {
		return err
	}
	if generationErr != nil || profile.Validate() != nil || state.Revision != p.BaseRevision {
		p.Status = "failed"
		p.Error = "Profile generation failed validation or its base revision changed"
	} else {
		p.Profile = profile
		p.ProfileRevision = securityprofile.Digest(profile)
		p.Status = "ready"
	}
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("UPDATE proposals SET body=? WHERE deployment=? AND id=?", body, dep, id)
	return err
}
func (s *Store) Proposal(dep, id string) (Proposal, error) {
	var data []byte
	err := s.db.QueryRow("SELECT body FROM proposals WHERE deployment=? AND id=?", dep, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return Proposal{}, ErrNotFound
	}
	if err != nil {
		return Proposal{}, err
	}
	var p Proposal
	err = json.Unmarshal(data, &p)
	return p, err
}
func (s *Store) Approve(dep, id, base, deploymentRevision, profileRevision, actor string) (State, error) {
	p, err := s.Proposal(dep, id)
	if err != nil {
		return State{}, err
	}
	if p.Status != "ready" || p.BaseRevision != base || p.DeploymentRevision != deploymentRevision || p.ProfileRevision != profileRevision || securityprofile.Digest(p.Profile) != profileRevision {
		return State{}, ErrConflict
	}
	tx, err := s.db.Begin()
	if err != nil {
		return State{}, err
	}
	defer tx.Rollback()
	state, err := loadState(tx, dep)
	if err != nil {
		return State{}, err
	}
	if state.Revision != base {
		return State{}, ErrConflict
	}
	state = State{Profile: &p.Profile, Revision: profileRevision, Status: "approved", Approval: &Approval{ProposalID: id, ProfileRevision: profileRevision, DeploymentRevision: deploymentRevision, Actor: actor, ApprovedAt: time.Now().UTC()}}
	if err = writeState(tx, dep, state); err != nil {
		return State{}, err
	}
	p.Status = "approved"
	data, err := json.Marshal(p)
	if err != nil {
		return State{}, err
	}
	if _, err = tx.Exec("UPDATE proposals SET body=? WHERE deployment=? AND id=?", data, dep, id); err != nil {
		return State{}, err
	}
	return state, tx.Commit()
}
func (s *Store) SaveReport(r Report) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = reportGeneration(tx, r); err != nil {
		return err
	}
	var holds int
	if err := tx.QueryRow("SELECT COUNT(*) FROM holds WHERE deployment=? AND run=?", r.Deployment, r.ID).Scan(&holds); err != nil {
		return err
	}
	r.Hold = holds > 0
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO reports VALUES (?,?,?,?) ON CONFLICT(deployment,id) DO UPDATE SET body=excluded.body", r.Deployment, r.ID, r.RequestedAt.Format(time.RFC3339Nano), data)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Report(dep, id string) (Report, error) {
	var data []byte
	err := s.db.QueryRow("SELECT body FROM reports WHERE deployment=? AND id=?", dep, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return Report{}, ErrNotFound
	}
	if err != nil {
		return Report{}, err
	}
	var r Report
	err = json.Unmarshal(data, &r)
	return r, err
}
func (s *Store) Reports(dep string, limit, offset int) ([]Report, error) {
	rows, err := s.db.Query("SELECT body FROM reports WHERE deployment=? ORDER BY created DESC LIMIT ? OFFSET ?", dep, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Report{}
	for rows.Next() {
		var data []byte
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		var r Report
		if err = json.Unmarshal(data, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) Evidence(dep, run, id string) (json.RawMessage, error) {
	var data []byte
	err := s.db.QueryRow("SELECT body FROM evidence WHERE deployment=? AND run=? AND id=?", dep, run, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var envelope Envelope
	if err = json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("invalid stored evidence")
	}
	report, err := s.Report(dep, run)
	if err != nil {
		return nil, err
	}
	expected := envelope
	expected.Deployment, expected.Run = dep, run
	expected.ProfileRevision, expected.DeploymentRevision = report.ProfileRevision, report.DeploymentRevision
	if _, err = os.Stat(filepath.Join(s.root, "evidence-keyring.json")); err != nil {
		s.evidenceIntegrityFailure(dep, run, id)
		return nil, fmt.Errorf("evidence verification unavailable")
	}
	keys, err := OpenEvidenceKeys(s.root)
	if err != nil {
		s.evidenceIntegrityFailure(dep, run, id)
		return nil, fmt.Errorf("evidence verification unavailable")
	}
	if securityprofile.Digest(envelope) != id || keys.Verify(envelope, expected) != nil {
		s.evidenceIntegrityFailure(dep, run, id)
		return nil, fmt.Errorf("stored evidence integrity failed")
	}
	return data, nil
}

func (s *Store) Retain(now time.Time) error {
	rows, err := s.db.Query("SELECT deployment,id,body FROM reports WHERE created<?", now.AddDate(0, 0, -90).Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	type key struct{ dep, id string }
	var expired []key
	for rows.Next() {
		var dep, id string
		var data []byte
		if err = rows.Scan(&dep, &id, &data); err != nil {
			rows.Close()
			return err
		}
		var r Report
		if err = json.Unmarshal(data, &r); err != nil {
			rows.Close()
			return err
		}
		if !r.Hold && r.CompletedAt != nil {
			expired = append(expired, key{dep, id})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, k := range expired {
		var holds int
		if err = tx.QueryRow("SELECT COUNT(*) FROM holds WHERE deployment=? AND run=?", k.dep, k.id).Scan(&holds); err != nil {
			return err
		}
		var unresolved int
		if err = tx.QueryRow("SELECT COUNT(*) FROM findings WHERE deployment=? AND json_extract(body,'$.run_id')=? AND COALESCE(json_extract(body,'$.status'),'open') NOT IN ('closed','retired')", k.dep, k.id).Scan(&unresolved); err != nil {
			return err
		}
		if holds > 0 || unresolved > 0 {
			continue
		}
		if _, err = tx.Exec("DELETE FROM evidence WHERE deployment=? AND run=?", k.dep, k.id); err != nil {
			return err
		}
		if _, err = tx.Exec("DELETE FROM reports WHERE deployment=? AND id=?", k.dep, k.id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("DELETE FROM manifests WHERE json_extract(body,'$.observed_at')<? AND sequence<(SELECT MAX(other.sequence) FROM manifests other WHERE other.deployment=manifests.deployment AND other.scope=manifests.scope) AND NOT EXISTS (SELECT 1 FROM holds WHERE holds.deployment=manifests.deployment)", now.AddDate(0, 0, -90).Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Findings(dep string, limit, offset int) ([]Finding, error) {
	rows, err := s.db.Query("SELECT body FROM findings WHERE deployment=? AND COALESCE(json_extract(body,'$.status'),'open') NOT IN ('closed','retired') ORDER BY json_extract(body,'$.last_seen') DESC LIMIT ? OFFSET ?", dep, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Finding{}
	for rows.Next() {
		var data []byte
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		var f Finding
		if err = json.Unmarshal(data, &f); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
func (s *Store) CloseFinding(dep, id, reason, actor string) error {
	if strings.TrimSpace(reason) == "" || len(reason) > 1024 {
		return fmt.Errorf("closure requires a bounded reason")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previous []byte
	err = tx.QueryRow("SELECT body FROM findings WHERE deployment=? AND id=?", dep, id).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.Exec("INSERT INTO finding_history VALUES (?,?,?,?)", dep, id, previous, now); err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE findings SET body=json_set(body,'$.status','closed','$.closure_reason',?,'$.closed_by',?,'$.closed_at',?) WHERE deployment=? AND id=?", Sanitize(reason), actor, now, dep, id)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO finding_history SELECT deployment,id,body,? FROM findings WHERE deployment=? AND id=?", now, dep, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Hold(dep, run string, hold bool) error {
	if _, err := s.Report(dep, run); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if hold {
		_, err = tx.Exec("INSERT OR IGNORE INTO holds VALUES (?,?)", dep, run)
	} else {
		_, err = tx.Exec("DELETE FROM holds WHERE deployment=? AND run=?", dep, run)
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE reports SET body=json_set(body,'$.hold',json(?),'$.delivery_status',CASE WHEN json_extract(body,'$.delivery_status')='delivered' THEN 'pending' ELSE json_extract(body,'$.delivery_status') END) WHERE deployment=? AND id=?", fmt.Sprintf("%t", hold), dep, run)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RetireDeployment(dep string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	archive := "retired:" + uuid.NewString() + ":" + dep
	if _, err = tx.Exec("INSERT INTO retired_deployments VALUES (?,?) ON CONFLICT(deployment) DO UPDATE SET retired_at=excluded.retired_at", dep, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	for _, table := range []string{"profiles", "proposals", "reports", "evidence", "findings", "finding_history", "manifests", "holds", "schedule_ticks", "cursors"} {
		if _, err = tx.Exec("UPDATE "+table+" SET deployment=? WHERE deployment=?", archive, dep); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("UPDATE findings SET body=json_set(body,'$.status','retired') WHERE deployment=?", archive); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM profiles WHERE deployment=?", archive); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE reports SET body=json_set(body,'$.model_status','unavailable','$.delivery_status','retired','$.status','retired','$.completed_at',COALESCE(json_extract(body,'$.completed_at'),?)) WHERE deployment=?", time.Now().UTC().Format(time.RFC3339Nano), archive); err != nil {
		return err
	}
	return tx.Commit()
}

func reportGeneration(tx *sql.Tx, r Report) error {
	var retired string
	err := tx.QueryRow("SELECT retired_at FROM retired_deployments WHERE deployment=?", r.Deployment).Scan(&retired)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	at, err := time.Parse(time.RFC3339Nano, retired)
	if err != nil {
		return err
	}
	if !r.RequestedAt.After(at) {
		return ErrConflict
	}
	return nil
}

func (s *Store) ReadyProposals(dep, base, deploymentRevision string) ([]Proposal, error) {
	rows, err := s.db.Query("SELECT body FROM proposals WHERE deployment=? AND json_extract(body,'$.status')='ready' AND json_extract(body,'$.base_revision')=? AND json_extract(body,'$.deployment_revision')=? ORDER BY json_extract(body,'$.created_at') DESC LIMIT 100", dep, base, deploymentRevision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Proposal{}
	for rows.Next() {
		var data []byte
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		var p Proposal
		if err = json.Unmarshal(data, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) evidenceIntegrityFailure(dep, run, id string) {
	now := time.Now().UTC()
	finding := Finding{ID: securityprofile.Digest([]string{dep, run, id, "evidence_integrity_failed"}), RunID: run, Status: "open", Concern: "monitoring", Classification: "suspicious_condition", Signal: "evidence_integrity_failed", EvidenceID: id, FirstSeen: now, LastSeen: now, Occurrences: 1}
	body, err := json.Marshal(finding)
	if err != nil {
		return
	}
	_, _ = s.db.Exec("INSERT INTO findings SELECT ?,?,? WHERE EXISTS (SELECT 1 FROM reports WHERE deployment=? AND id=?) ON CONFLICT(deployment,id) DO UPDATE SET body=json_set(findings.body,'$.status','open','$.last_seen',?,'$.occurrences',COALESCE(json_extract(findings.body,'$.occurrences'),0)+1)", dep, finding.ID, body, dep, run, now.Format(time.RFC3339Nano))
}

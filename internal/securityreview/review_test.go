package securityreview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flatrun/agent/pkg/securityprofile"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	base := t.TempDir()
	dep := filepath.Join(base, "deployments")
	if err := os.MkdirAll(dep, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(filepath.Join(base, "private"), dep)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestApprovalBindsEverySemanticField(t *testing.T) {
	for _, field := range []string{"command", "image", "scope", "policy", "schedule", "budget", "expectations", "exceptions"} {
		t.Run(field, func(t *testing.T) {
			s := testStore(t)
			p := securityprofile.Draft("php")
			p.Targets["web"] = securityprofile.Target{Service: "web", Roots: []string{"/srv/project"}}
			p.Reviews["source"] = securityprofile.Review{Concern: "integrity", Target: "web", Runs: []string{"full"}, Expectations: []string{"Reviewed release"}, Collect: []securityprofile.Collector{{ID: "files", Image: "registry/collector@sha256:" + strings.Repeat("a", 64), Command: []string{"python3", "/collectors/inventory.py", "--runtime", "php"}, SnapshotPaths: []string{"."}}}}
			proposal, err := s.Propose("web", "", "deployment-v1", "operator", p)
			if err != nil {
				t.Fatal(err)
			}
			approved, err := s.Approve("web", proposal.ID, "", "deployment-v1", proposal.ProfileRevision, "user:1")
			if err != nil {
				t.Fatal(err)
			}
			review := p.Reviews["source"]
			switch field {
			case "command":
				review.Collect[0].Command = append(review.Collect[0].Command, "changed")
			case "image":
				review.Collect[0].Image = "registry/collector@sha256:" + strings.Repeat("b", 64)
			case "scope":
				p.Targets["web"] = securityprofile.Target{Service: "web", Roots: []string{"/srv/other"}}
			case "policy":
				p.AccessPolicy.Actions["download"] = securityprofile.ActionRule{ResourceType: "attachment", RequireAuthentication: true}
			case "schedule":
				p.Schedule.Full = "0 3 * * *"
			case "budget":
				p.Budget.MaxFiles--
			case "expectations":
				review.Expectations = []string{"Changed expectation"}
			case "exceptions":
				p.Exceptions = []securityprofile.Exception{{Concern: "integrity", Scope: "web", Reason: "Maintenance", ExpiresAt: "2026-11-01T00:00:00Z"}}
			}
			p.Reviews["source"] = review
			draft, err := s.Draft("web", approved.Revision, p)
			if err != nil {
				t.Fatal(err)
			}
			if draft.Status != "outdated" || draft.Revision == approved.Revision {
				t.Fatal("semantic edit retained approval")
			}
			if _, err = s.Approve("web", proposal.ID, approved.Revision, "deployment-v1", proposal.ProfileRevision, "user:1"); err != ErrConflict {
				t.Fatalf("stale approval: %v", err)
			}
		})
	}
}

func TestEvidenceIdentityIntegrityAndCursor(t *testing.T) {
	s := testStore(t)
	keys, err := OpenEvidenceKeys(s.root)
	if err != nil {
		t.Fatal(err)
	}
	if err = keys.EnsureKey("v1"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	e := Envelope{APIVersion: "flatrun.security-evidence/v1", Source: "inventory", SourceVersion: "1", Deployment: "web", Run: "run-1", ProfileRevision: "profile", DeploymentRevision: "deployment", WindowStart: now.Add(-time.Minute), WindowEnd: now, Count: 1, NextCursor: 1, Total: 1, Final: true, Items: []json.RawMessage{json.RawMessage(`{"name":"example","version":"1"}`)}}
	if err = keys.Sign(&e, "v1"); err != nil {
		t.Fatal(err)
	}
	expected := e
	if err = keys.Verify(e, expected); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"deployment", "run", "profile", "source", "version", "window", "cursor", "count", "mac"} {
		t.Run(field, func(t *testing.T) {
			forged := e
			switch field {
			case "deployment":
				forged.Deployment = "other"
			case "run":
				forged.Run = "other"
			case "profile":
				forged.ProfileRevision = "other"
			case "source":
				forged.Source = "other"
			case "version":
				forged.SourceVersion = "other"
			case "window":
				forged.WindowStart = now
			case "cursor":
				forged.NextCursor = 0
			case "count":
				forged.Count = 0
			case "mac":
				forged.MAC = strings.Repeat("0", 64)
			}
			if keys.Verify(forged, expected) == nil {
				t.Fatal("forged envelope accepted")
			}
		})
	}
	r := Report{ProfileRevision: e.ProfileRevision, DeploymentRevision: e.DeploymentRevision, ID: e.Run, Deployment: e.Deployment, RequestedAt: now, Status: "collecting", Findings: []Finding{}}
	if err = s.SaveReport(r); err != nil {
		t.Fatal(err)
	}
	if err = s.CommitPage(e, r, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.CommitPage(e, r, nil); err != ErrConflict {
		t.Fatalf("replay advanced cursor: %v", err)
	}
	stored, err := s.Evidence(e.Deployment, e.Run, securityprofile.Digest(e))
	if err != nil || len(stored) == 0 {
		t.Fatalf("durable evidence: %v", err)
	}
}

func TestSignalsKeepUnknownDistinct(t *testing.T) {
	for _, test := range []struct {
		signal       securityprofile.Signal
		data         string
		match, known bool
	}{
		{securityprofile.Signal{Kind: "json_array_nonempty", Pointer: "/items"}, `{"items":[1]}`, true, true},
		{securityprofile.Signal{Kind: "json_array_nonempty", Pointer: "/items"}, `{}`, false, false},
		{securityprofile.Signal{Kind: "json_count_gt", Pointer: "/count", Value: float64(3)}, `{"count":"4"}`, false, false},
		{securityprofile.Signal{Kind: "json_count_gt", Pointer: "/count", Value: float64(3)}, `{"count":4}`, true, true},
		{securityprofile.Signal{Kind: "json_equals", Pointer: "/a~1b/~0key", Value: true}, `{"a/b":{"~key":true}}`, true, true},
	} {
		matched, known := SignalMatch(test.signal, []byte(test.data))
		if matched != test.match || known != test.known {
			t.Fatalf("%+v: got %v/%v", test, matched, known)
		}
	}
}

func TestRunnerIsolationAndMetadataScope(t *testing.T) {
	root := t.TempDir()
	metadata, err := WriteMetadata(root, map[string]any{"inventory": map[string]any{"name": "web"}})
	if err != nil {
		t.Fatal(err)
	}
	image := "registry/collector@sha256:" + strings.Repeat("a", 64)
	r := Runner{ScratchRoot: root, Images: []string{image}}
	c := securityprofile.Collector{ID: "inventory", Image: image, Command: []string{"python3", "/collectors/inventory.py"}, MetadataSources: []string{"inventory"}}
	args, err := r.Args(c, securityprofile.Draft("php").Budget, Input{Metadata: metadata, Snapshot: root}, "test")
	if err != nil {
		t.Fatal(err)
	}
	rendered := strings.Join(args, " ")
	for _, required := range []string{"--pull=never", "--network=none", "--read-only", "--cap-drop=ALL", "--user=65532:65532", "no-new-privileges", "--pids-limit=128", "--memory=256m", "--cpus=1", "--workdir=/work", "GOTOOLCHAIN=local", "GOWORK=off", "GOENV=off", "GOFLAGS=", "python3", "-I"} {
		if !strings.Contains(rendered, required) {
			t.Fatalf("missing isolation setting %s", required)
		}
	}
	if strings.Contains(rendered, "dst=/evidence") {
		t.Fatal("metadata collector received filesystem")
	}
	r.Images = nil
	if _, err = r.Args(c, securityprofile.Draft("php").Budget, Input{Metadata: metadata}, "test"); err == nil {
		t.Fatal("unapproved image accepted")
	}
}

func TestPrivateStateRejectsApplicationAndSymlinkPaths(t *testing.T) {
	base := t.TempDir()
	dep := filepath.Join(base, "deployments")
	if err := os.MkdirAll(dep, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(filepath.Join(dep, "state"), dep); err == nil {
		t.Fatal("application-writable state accepted")
	}
	if err := os.Symlink(dep, filepath.Join(base, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(filepath.Join(base, "linked", "state"), dep); err == nil {
		t.Fatal("symbolic state parent accepted")
	}
}

func TestStoredEvidenceRejectsTampering(t *testing.T) {
	s := testStore(t)
	keys, err := OpenEvidenceKeys(s.root)
	if err != nil {
		t.Fatal(err)
	}
	if err = keys.EnsureKey("v1"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r := Report{ID: "run", Deployment: "shop", ProfileRevision: "profile", DeploymentRevision: "deployment", RequestedAt: now}
	if err = s.SaveReport(r); err != nil {
		t.Fatal(err)
	}
	e := Envelope{APIVersion: "flatrun.security-evidence/v1", Source: "files", SourceVersion: "1", Deployment: r.Deployment, Run: r.ID, ProfileRevision: r.ProfileRevision, DeploymentRevision: r.DeploymentRevision, WindowStart: now, WindowEnd: now.Add(time.Second), Count: 1, NextCursor: 1, Total: 1, Final: true, Items: []json.RawMessage{json.RawMessage(`{"count":1}`)}}
	if err = keys.Sign(&e, "v1"); err != nil {
		t.Fatal(err)
	}
	id := securityprofile.Digest(e)
	if err = s.CommitPage(e, r, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Evidence(r.Deployment, r.ID, id); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE evidence SET body=json_set(body,'$.items[0].count',2)"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Evidence(r.Deployment, r.ID, id); err == nil {
		t.Fatal("tampered evidence returned")
	}
}

func TestRetentionKeepsEvidenceForUnresolvedFindings(t *testing.T) {
	s := testStore(t)
	old := time.Now().UTC().AddDate(0, 0, -100)
	r := Report{ID: "run", Deployment: "shop", Status: "partial", RequestedAt: old, CompletedAt: &old}
	if err := s.SaveReport(r); err != nil {
		t.Fatal(err)
	}
	e := Envelope{Deployment: "shop", Run: "run", Source: "files", Count: 1, NextCursor: 1, Total: 1, Final: true, Items: []json.RawMessage{json.RawMessage(`{}`)}}
	f := Finding{ID: "open", RunID: "run", Status: "open", EvidenceID: securityprofile.Digest(e)}
	if err := s.CommitPage(e, r, []Finding{f}); err != nil {
		t.Fatal(err)
	}
	if err := s.Retain(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Report("shop", "run"); err != nil {
		t.Fatal("unresolved finding lost its report", err)
	}
	if err := s.CloseFinding("shop", "open", "Remediated", "user:1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Retain(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Report("shop", "run"); err != ErrNotFound {
		t.Fatal("closed finding prevented expiry", err)
	}
}

func TestRetirementSeparatesReusedNamesAndRejectsLateWrites(t *testing.T) {
	s := testStore(t)
	r := Report{Deployment: "shop", ID: "old-run", RequestedAt: time.Now().UTC()}
	if err := s.SaveReport(r); err != nil {
		t.Fatal(err)
	}
	if err := s.RetireDeployment("shop"); err != nil {
		t.Fatal(err)
	}
	reports, err := s.Reports("shop", 20, 0)
	if err != nil || len(reports) != 0 {
		t.Fatal("retired reports leaked", err)
	}
	if err = s.SaveReport(r); err != ErrConflict {
		t.Fatal("late report returned to reused scope", err)
	}
	r.ID = "new-run"
	r.RequestedAt = time.Now().UTC()
	if err = s.SaveReport(r); err != nil {
		t.Fatal(err)
	}
}

func TestManifestAdvancesOnlyWithCommittedEvidence(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "new.php"), []byte("<?php echo 1;"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot := Snapshot{Path: root}
	first, err := s.ReconcileSnapshot("shop", "files", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.ReconcileSnapshot("shop", "files", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != retry.Sequence || len(retry.Changes) != 1 {
		t.Fatal("unprocessed manifest advanced")
	}
	r := Report{ID: "run", Deployment: "shop", RequestedAt: time.Now().UTC()}
	if err = s.SaveReport(r); err != nil {
		t.Fatal(err)
	}
	e := Envelope{Deployment: "shop", Run: r.ID, Source: "files", Total: 1, Count: 1, NextCursor: 1, Final: true, Items: []json.RawMessage{json.RawMessage(`{}`)}}
	if err = s.CommitPage(e, r, nil, first); err != nil {
		t.Fatal(err)
	}
	next, err := s.ReconcileSnapshot("shop", "files", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if next.Sequence != first.Sequence+1 || len(next.Changes) != 0 {
		t.Fatal("committed manifest was not reconciled")
	}
}

func TestObservationRevisionIgnoresManifestSummaries(t *testing.T) {
	indicator := json.RawMessage(`{"path":"upload.php","sha256":"first","indicators":["php_tag"]}`)
	first := observationRevision([]json.RawMessage{indicator, json.RawMessage(`{"manifest_changes":[{"path":"upload.php","kind":"added"}]}`), json.RawMessage(`{"indicator_count":1}`)})
	next := observationRevision([]json.RawMessage{indicator, json.RawMessage(`{"manifest_changes":[]}`), json.RawMessage(`{"indicator_count":1}`)})
	if first != next {
		t.Fatal("manifest summary duplicated a finding")
	}
	changed := observationRevision([]json.RawMessage{json.RawMessage(`{"path":"upload.php","sha256":"second","indicators":["php_tag"]}`)})
	if changed == next {
		t.Fatal("changed executable content reused an old observation")
	}
}

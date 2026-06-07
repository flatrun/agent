package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestPlan(t *testing.T) *Plan {
	t.Helper()
	return New("deployment.env.update",
		Resource{Type: "deployment", ID: "myapp"},
		Actor{ID: "u1", Name: "admin", Type: "user"},
		time.Hour)
}

func TestNewPlanDefaults(t *testing.T) {
	p := newTestPlan(t)
	if p.FormatVersion != FormatVersion {
		t.Errorf("format_version = %d, want %d", p.FormatVersion, FormatVersion)
	}
	if p.Status != StatusAvailable {
		t.Errorf("status = %q, want available", p.Status)
	}
	if !strings.HasPrefix(p.ID, "pln_") {
		t.Errorf("id %q missing pln_ prefix", p.ID)
	}
	if !p.ExpiresAt.After(p.CreatedAt) {
		t.Error("expires_at not after created_at")
	}
}

func TestSummarize(t *testing.T) {
	p := newTestPlan(t)
	p.Changes = []Change{
		{Actions: []string{ActionCreate}},
		{Actions: []string{ActionUpdate}},
		{Actions: []string{ActionUpdate}},
		{Actions: []string{ActionDelete}},
		{Actions: []string{ActionDelete, ActionCreate}},
		{Actions: []string{ActionNoOp}},
	}
	p.Summarize()
	want := Summary{Create: 1, Update: 2, Delete: 1, Replace: 1, NoOp: 1}
	if p.Summary != want {
		t.Errorf("summary = %+v, want %+v", p.Summary, want)
	}
}

func TestRedacted(t *testing.T) {
	p := newTestPlan(t)
	p.Request.Body = json.RawMessage(`{"env_vars":[{"key":"SECRET","value":"hunter2"}]}`)
	p.Changes = []Change{
		{ID: ".env.flatrun", Actions: []string{ActionUpdate}, Before: StrPtr("SECRET=old"), After: StrPtr("SECRET=hunter2"), Sensitive: true},
		{ID: "web", Actions: []string{ActionDelete, ActionCreate}, Reason: "recreate"},
	}
	r := p.Redacted()
	if *r.Changes[0].Before != RedactedPlaceholder || *r.Changes[0].After != RedactedPlaceholder {
		t.Error("sensitive change not redacted")
	}
	if strings.Contains(string(r.Request.Body), "hunter2") {
		t.Error("request body not redacted when plan has sensitive changes")
	}
	if *p.Changes[0].After != "SECRET=hunter2" {
		t.Error("original plan mutated by Redacted")
	}
	if r.Changes[1].Before != nil {
		t.Error("non-sensitive change should be untouched")
	}
}

func TestSnapshotAndVerify(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}

	snap := SnapshotFiles(dir, "a.txt", "missing.txt")
	if snap["missing.txt"] != "absent" {
		t.Errorf("missing file hash = %q, want absent", snap["missing.txt"])
	}
	if !strings.HasPrefix(snap["a.txt"], "sha256:") {
		t.Errorf("hash %q missing sha256 prefix", snap["a.txt"])
	}

	if err := VerifySnapshot(dir, snap); err != nil {
		t.Fatalf("unchanged snapshot should verify, got %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two"), 0600); err != nil {
		t.Fatal(err)
	}
	err := VerifySnapshot(dir, snap)
	drift, ok := err.(*DriftError)
	if !ok {
		t.Fatalf("want *DriftError, got %T (%v)", err, err)
	}
	if len(drift.Paths) != 1 || drift.Paths[0] != "a.txt" {
		t.Errorf("drift paths = %v, want [a.txt]", drift.Paths)
	}

	// Creating a previously absent file is drift too.
	if err := os.WriteFile(filepath.Join(dir, "missing.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if VerifySnapshot(dir, snap) == nil {
		t.Error("created file should count as drift")
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	p := newTestPlan(t)
	p.Changes = []Change{{Type: "file", ID: ".env.flatrun", Actions: []string{ActionUpdate}}}
	if err := store.Save(p); err != nil {
		t.Fatal(err)
	}

	onDisk := filepath.Join(store.Root(), "deployment", "myapp", p.ID+".json")
	info, err := os.Stat(onDisk)
	if err != nil {
		t.Fatalf("plan file not at expected path: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("plan file mode = %v, want 0600", info.Mode().Perm())
	}

	got, err := store.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != p.Action || got.Resource != p.Resource {
		t.Errorf("round trip mismatch: %+v", got)
	}

	plans, err := store.List(ListFilter{ResourceType: "deployment", ResourceID: "myapp"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 {
		t.Fatalf("list returned %d plans, want 1", len(plans))
	}

	if err := store.Delete(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(p.ID); err != ErrNotFound {
		t.Errorf("after delete, Get err = %v, want ErrNotFound", err)
	}
}

func TestStoreRejectsBadIDs(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Get("../../etc/passwd"); err != ErrNotFound {
		t.Errorf("traversal id should be ErrNotFound, got %v", err)
	}
	p := newTestPlan(t)
	p.ID = "pln_not-a-uuid"
	if err := store.Save(p); err == nil {
		t.Error("Save should reject malformed plan id")
	}
}

func TestPruneOnce(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	now := time.Now().UTC()

	expired := newTestPlan(t)
	expired.ExpiresAt = now.Add(-time.Minute)

	oldApplied := newTestPlan(t)
	oldApplied.Status = StatusApplied
	oldApplied.CreatedAt = now.Add(-31 * 24 * time.Hour)

	oldObsolete := newTestPlan(t)
	oldObsolete.Status = StatusObsolete
	oldObsolete.CreatedAt = now.Add(-8 * 24 * time.Hour)

	fresh := newTestPlan(t)

	for _, p := range []*Plan{expired, oldApplied, oldObsolete, fresh} {
		if err := store.Save(p); err != nil {
			t.Fatal(err)
		}
	}

	store.PruneOnce(now, 30*24*time.Hour)

	got, err := store.Get(expired.ID)
	if err != nil || got.Status != StatusExpired {
		t.Errorf("ttl-passed plan: status %v err %v, want expired", got, err)
	}
	if _, err := store.Get(oldApplied.ID); err != ErrNotFound {
		t.Errorf("old applied plan should be deleted, got %v", err)
	}
	if _, err := store.Get(oldObsolete.ID); err != ErrNotFound {
		t.Errorf("old obsolete plan should be deleted, got %v", err)
	}
	got, err = store.Get(fresh.ID)
	if err != nil || got.Status != StatusAvailable {
		t.Errorf("fresh plan should be untouched, got %v err %v", got, err)
	}
}

package runs

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestStorePersistenceScopeAndRecovery(t *testing.T) {
	root := t.TempDir()
	s, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	for i, status := range []Status{StatusQueued, StatusStarting, StatusRunning, StatusSucceeded, StatusFailed, StatusTimedOut, StatusCancelled} {
		r := Run{ID: string(rune('a' + i)), OwnerUserID: int64(i % 2), Status: status, Spec: Spec{Image: "busybox:latest"}, CreatedAt: time.Now()}
		if err = s.Save(&r); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	owner := int64(1)
	rs, err := s.List(&owner, 100)
	if err != nil || len(rs) != 3 {
		t.Fatalf("scoped records: %v %v", rs, err)
	}
	if _, err = s.Get("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing record: %v", err)
	}
	if err = s.Recover(); err != nil {
		t.Fatal(err)
	}
	rs, err = s.List(nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if r.ID <= "c" {
			if r.Status != StatusFailed || r.Error != "agent restarted" || r.FinishedAt == nil {
				t.Fatalf("unrecovered: %+v", r)
			}
		} else if r.Error != "" {
			t.Fatalf("terminal run changed: %+v", r)
		}
	}
}

func TestSpecValidation(t *testing.T) {
	s := Spec{Image: "busybox:latest"}
	if err := s.Validate(); err != nil || s.TimeoutSeconds != 600 {
		t.Fatalf("defaults: %+v %v", s, err)
	}
	for _, s := range []Spec{{}, {Image: "x", TimeoutSeconds: -1}, {Image: "x", TimeoutSeconds: 3601}, {Image: "x", ArtifactPaths: []string{"relative"}}, {Image: "x", ArtifactPaths: []string{"/a/out", "/b/out"}}, {Image: "x", ArtifactPaths: []string{"/"}}, {Image: "x", CPUs: -1}} {
		if s.Validate() == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
}

func TestStoreListLimitsAndRecovery(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for index := range 503 {
		run := Run{
			ID:          fmt.Sprintf("run-%03d", index),
			OwnerUserID: int64(index % 2),
			Status:      StatusQueued,
			Spec:        Spec{Image: "busybox:latest", TimeoutSeconds: 42},
			CreatedAt:   time.Now().UTC(),
		}
		if err := store.Save(&run); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct{ limit, count int }{{0, 100}, {2, 2}, {500, 500}, {501, 500}} {
		records, err := store.List(nil, test.limit)
		if err != nil || len(records) != test.count {
			t.Fatalf("limit %d: %d records, %v", test.limit, len(records), err)
		}
		if records[0].ID != "run-502" || records[1].ID != "run-501" {
			t.Fatalf("not newest first: %v", records[:2])
		}
	}
	owner := int64(1)
	records, err := store.List(&owner, 2)
	if err != nil || len(records) != 2 || records[0].ID != "run-501" || records[1].ID != "run-499" {
		t.Fatalf("owner limit: %v %v", records, err)
	}
	if err := store.Recover(); err != nil {
		t.Fatal(err)
	}
	var pendingCount int
	if err := store.db.QueryRow(`SELECT count(*) FROM runs WHERE status = 'queued'`).Scan(&pendingCount); err != nil || pendingCount != 0 {
		t.Fatalf("recovery was limited: %d %v", pendingCount, err)
	}
	var record string
	if err := store.db.QueryRow(`SELECT record FROM runs WHERE id = 'run-000'`).Scan(&record); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(record, `"timeout_seconds":42`) || strings.Contains(record, `"timeout":`) {
		t.Fatalf("stored timeout: %s", record)
	}
}

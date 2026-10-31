package securityreview

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/flatrun/agent/pkg/securityprofile"
)

func TestScheduleRecoversWindowsWithoutDuplicateRuns(t *testing.T) {
	s := testStore(t)
	profile := securityprofile.Draft("unknown")
	p, err := s.Propose("shop", "", "deployment", "operator", profile)
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.Approve("shop", p.ID, "", "deployment", p.ProfileRevision, "user:1")
	if err != nil {
		t.Fatal(err)
	}
	end := state.Approval.ApprovedAt.Add(16 * time.Minute)
	engine := NewEngine(s, Runner{ScratchRoot: t.TempDir()}, nil, nil)
	if err = engine.Schedule(end); err != nil {
		t.Fatal(err)
	}
	reports, err := s.Reports("shop", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	incremental := 0
	for _, r := range reports {
		if r.Mode == "incremental" {
			incremental++
		}
		if !r.WindowEnd.After(r.WindowStart) || r.WindowEnd.After(end) {
			t.Fatal("invalid scheduled window")
		}
	}
	if incremental < 3 {
		t.Fatal("missed incremental windows disappeared")
	}
	restarted := NewEngine(s, Runner{ScratchRoot: t.TempDir()}, nil, nil)
	if err = restarted.Schedule(end); err != nil {
		t.Fatal(err)
	}
	again, err := s.Reports("shop", 100, 0)
	if err != nil || len(again) != len(reports) {
		t.Fatal("schedule replay duplicated work", err)
	}
}

func TestInterruptedRunPreservesDurableFindings(t *testing.T) {
	s := testStore(t)
	r := Report{ID: "interrupted", Deployment: "shop", Status: "collecting", RequestedAt: time.Now().UTC(), Findings: []Finding{{ID: "candidate", Concern: "integrity", EvidenceID: "durable-reference", Occurrences: 1}}}
	if err := s.SaveReport(r); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(s, Runner{ScratchRoot: t.TempDir()}, nil, nil)
	if err := engine.Prepare(); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.Report("shop", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != "failed" || len(recovered.Findings) != 1 {
		t.Fatal("interrupted findings discarded")
	}
}

func TestRestrictedDeliveryThroughHTTPS(t *testing.T) {
	s := testStore(t)
	now := time.Now().UTC()
	r := Report{APIVersion: "flatrun.security-report/v1", ID: "delivery", Deployment: "shop", Status: "partial", ModelStatus: "unavailable", RequestedAt: now, CompletedAt: &now, DeliveryStatus: "pending", Coverage: []Coverage{}, Findings: []Finding{}}
	if err := s.SaveReport(r); err != nil {
		t.Fatal(err)
	}
	calls := 0
	redirect := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		if req.Header.Get("Authorization") != "Bearer delivery-test-placeholder" || req.Header.Get("Idempotency-Key") == "" {
			t.Error("missing delivery authentication or identity")
		}
		if redirect {
			http.Redirect(w, req, "https://unapproved.invalid/", http.StatusTemporaryRedirect)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["kind"] != "report" || body["deployment"] != "shop" {
			t.Error("wrong delivery scope")
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	delivery := Delivery{URL: server.URL, AllowedHosts: []string{u.Hostname()}, Client: server.Client(), Credential: func() (string, error) { return "delivery-test-placeholder", nil }}
	if err := s.Deliver(context.Background(), delivery, r); err != nil {
		t.Fatal(err)
	}
	saved, err := s.Report("shop", r.ID)
	if err != nil || saved.DeliveryStatus != "delivered" {
		t.Fatal("delivery acknowledgement not retained", err)
	}
	redirect = true
	if s.Deliver(context.Background(), delivery, r) == nil {
		t.Fatal("delivery followed redirect")
	}
	if calls != 2 {
		t.Fatal("unexpected destination requests")
	}
	delivery.AllowedHosts = nil
	if delivery.Validate() == nil {
		t.Fatal("unapproved destination accepted")
	}
}

func TestScheduleCoalescesLongOutage(t *testing.T) {
	s := testStore(t)
	profile := securityprofile.Draft("unknown")
	proposal, err := s.Propose("shop", "", "deployment", "owner", profile)
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.Approve("shop", proposal.ID, "", "deployment", proposal.ProfileRevision, "user:1")
	if err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(s, Runner{ScratchRoot: t.TempDir()}, nil, nil)
	if err = engine.Schedule(state.Approval.ApprovedAt.AddDate(0, 0, 30)); err != nil {
		t.Fatal(err)
	}
	reports, err := s.Reports("shop", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) > 20 {
		t.Fatal("outage generated unbounded catch-up", len(reports))
	}
	coalesced := 0
	for _, report := range reports {
		for _, limitation := range report.Limitations {
			if limitation == "Missed schedule windows coalesced; historical completeness remains unknown" {
				coalesced++
			}
		}
	}
	if coalesced != 3 {
		t.Fatal("missing explicit historical gaps", coalesced)
	}
}

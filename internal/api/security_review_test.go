package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/ai"
	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/securityreview"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/securityprofile"
	"github.com/gin-gonic/gin"
)

type securityGenerationGate struct {
	gate   chan struct{}
	output string
}

func (p *securityGenerationGate) Name() string { return "test" }
func (p *securityGenerationGate) Complete(ctx context.Context, _ ai.Request) (*ai.Response, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.gate:
		return &ai.Response{Content: p.output}, nil
	}
}

func TestSecurityProfileHTTPApprovalAndKeyIntersection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := t.TempDir()
	deployments := filepath.Join(base, "deployments")
	if err := os.MkdirAll(deployments, 0700); err != nil {
		t.Fatal(err)
	}
	for _, dep := range []string{"shop", "other"} {
		dir := filepath.Join(deployments, dep)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("name: "+dep+"\nservices:\n  frontend:\n    image: nginx:alpine\n  worker:\n    image: python:3\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{DeploymentsPath: deployments, Auth: config.AuthConfig{Enabled: true, JWTSecret: "http-security-test-key"}, SecurityReviews: config.SecurityReviewsConfig{StatePath: filepath.Join(base, "private")}}
	manager, err := auth.NewManager(deployments, &cfg.Auth, false)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	permissions := []string{"security_profiles:read", "security_profiles:write", "security_profiles:approve", "security_reviews:read", "security_reviews:run"}
	owner, err := manager.CreateUser("owner", "", "password-for-test-only", auth.RoleViewer, permissions)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.AssignDeployment(owner.ID, "shop", auth.AccessLevelAdmin, owner.ID); err != nil {
		t.Fatal(err)
	}
	if err = manager.AssignDeployment(owner.ID, "other", auth.AccessLevelAdmin, owner.ID); err != nil {
		t.Fatal(err)
	}
	_, key, err := manager.CreateAPIKey(owner.ID, "shop only", "", auth.RoleViewer, permissions, auth.DeploymentAccess{"shop": auth.AccessLevelAdmin}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{config: cfg, manager: docker.NewManager(deployments), authMiddleware: auth.NewMiddlewareWithManager(&cfg.Auth, manager)}
	t.Cleanup(func() {
		if s.securityReviewCancel != nil {
			s.securityReviewCancel()
		}
		if s.securityReviewStore != nil {
			_ = s.securityReviewStore.Close()
		}
	})
	router := gin.New()
	group := router.Group("/api", s.authMiddleware.RequireAuth())
	s.registerSecurityReviewRoutes(group)
	group.PUT("/security-permission-probe", s.authMiddleware.RequirePermission(auth.PermSettingsWrite), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	request := func(method, path string, body any, token string) *httptest.ResponseRecorder {
		t.Helper()
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	if out := request("GET", "/api/deployments/shop/security-profile", nil, ""); out.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous %d: %s", out.Code, out.Body.String())
	}
	if out := request("GET", "/api/deployments/other/security-profile", nil, key); out.Code != http.StatusForbidden {
		t.Fatalf("key widened scope %d: %s", out.Code, out.Body.String())
	}
	initial := request("GET", "/api/deployments/shop/security-profile", nil, key)
	if initial.Code != http.StatusOK {
		t.Fatalf("profile read %d: %s", initial.Code, initial.Body.String())
	}
	if capture := os.Getenv("FLATRUN_SECURITY_UI_CAPTURE"); capture != "" {
		if err = os.WriteFile(capture, initial.Body.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	generated, _ := json.Marshal(securityprofile.Draft("unknown"))
	gate := make(chan struct{})
	s.aiProvider = &securityGenerationGate{gate: gate, output: string(generated)}
	queued := request("POST", "/api/deployments/shop/security-profile/generate", map[string]string{"base_revision": ""}, key)
	if queued.Code != http.StatusAccepted {
		t.Fatalf("generation queue %d: %s", queued.Code, queued.Body.String())
	}
	var queuedBody struct {
		Proposal securityreview.Proposal `json:"proposal"`
	}
	if err = json.Unmarshal(queued.Body.Bytes(), &queuedBody); err != nil {
		t.Fatal(err)
	}
	premature := request("POST", "/api/deployments/shop/security-profile/proposals/"+queuedBody.Proposal.ID+"/approve", map[string]string{"base_revision": "", "deployment_revision": queuedBody.Proposal.DeploymentRevision, "profile_revision": queuedBody.Proposal.ProfileRevision}, key)
	if premature.Code != http.StatusConflict {
		t.Fatalf("queued generation approved: %d", premature.Code)
	}
	close(gate)
	now := time.Now().UTC()
	findingRun := securityreview.Report{ID: "finding-run", Deployment: "shop", Status: "partial", RequestedAt: now, CompletedAt: &now, Coverage: []securityreview.Coverage{}, Findings: []securityreview.Finding{}}
	if err = s.securityReviewStore.SaveReport(findingRun); err != nil {
		t.Fatal(err)
	}
	envelope := securityreview.Envelope{Deployment: "shop", Run: findingRun.ID, Source: "runtime-files", Count: 1, NextCursor: 1, Total: 1, Final: true, Items: []json.RawMessage{json.RawMessage(`{"indicator_count":1}`)}}
	finding := securityreview.Finding{ID: "candidate", RunID: findingRun.ID, Status: "open", Concern: "integrity", EvidenceID: securityprofile.Digest(envelope), FirstSeen: now, LastSeen: now, Occurrences: 1}
	if err = s.securityReviewStore.CommitPage(envelope, findingRun, []securityreview.Finding{finding}); err != nil {
		t.Fatal(err)
	}
	if out := request("GET", "/api/deployments/other/security-findings", nil, key); out.Code != http.StatusForbidden {
		t.Fatalf("findings crossed key scope %d", out.Code)
	}
	if out := request("POST", "/api/deployments/other/security-findings/candidate/close", map[string]string{"reason": "Reviewed"}, key); out.Code != http.StatusForbidden {
		t.Fatalf("closure crossed key scope %d", out.Code)
	}
	if out := request("POST", "/api/deployments/shop/security-findings/candidate/close", map[string]string{"reason": "Reviewed with remediation evidence"}, key); out.Code != http.StatusOK {
		t.Fatalf("owner closure %d: %s", out.Code, out.Body.String())
	}
	closed := request("GET", "/api/deployments/shop/security-findings", nil, key)
	var active struct {
		Findings []securityreview.Finding `json:"findings"`
	}
	if err = json.Unmarshal(closed.Body.Bytes(), &active); err != nil || len(active.Findings) != 0 {
		t.Fatal("closed finding remained active", err)
	}
	old := now.AddDate(0, 0, -91)
	record := securityreview.Report{APIVersion: "flatrun.security-report/v1", ID: "retention-run", Deployment: "shop", Status: "partial", RequestedAt: old, CompletedAt: &old, Coverage: []securityreview.Coverage{}, Findings: []securityreview.Finding{}}
	if err = s.securityReviewStore.SaveReport(record); err != nil {
		t.Fatal(err)
	}
	if out := request("PUT", "/api/deployments/shop/security-reviews/retention-run/hold", map[string]bool{"hold": true}, key); out.Code != http.StatusOK {
		t.Fatalf("incident hold %d: %s", out.Code, out.Body.String())
	}
	if err = s.securityReviewStore.Retain(now); err != nil {
		t.Fatal(err)
	}
	if out := request("GET", "/api/deployments/shop/security-reviews/retention-run", nil, key); out.Code != http.StatusOK {
		t.Fatalf("held evidence expired %d", out.Code)
	}
	if out := request("PUT", "/api/deployments/shop/security-reviews/retention-run/hold", map[string]bool{"hold": false}, key); out.Code != http.StatusOK {
		t.Fatalf("incident release %d", out.Code)
	}
	if err = s.securityReviewStore.Retain(now); err != nil {
		t.Fatal(err)
	}
	if out := request("GET", "/api/deployments/shop/security-reviews/retention-run", nil, key); out.Code != http.StatusNotFound {
		t.Fatalf("expired evidence retained %d", out.Code)
	}
	profile := securityprofile.Draft("unknown")
	proposalResponse := request("POST", "/api/deployments/shop/security-profile/proposals", securityProfileRequest{Profile: profile}, key)
	if proposalResponse.Code != http.StatusCreated {
		t.Fatalf("proposal %d: %s", proposalResponse.Code, proposalResponse.Body.String())
	}
	var proposal struct {
		Proposal struct {
			ID                 string `json:"id"`
			ProfileRevision    string `json:"profile_revision"`
			DeploymentRevision string `json:"deployment_revision"`
		} `json:"proposal"`
	}
	if err = json.Unmarshal(proposalResponse.Body.Bytes(), &proposal); err != nil {
		t.Fatal(err)
	}
	approval := map[string]string{"base_revision": "", "deployment_revision": proposal.Proposal.DeploymentRevision, "profile_revision": proposal.Proposal.ProfileRevision}
	approved := request("POST", "/api/deployments/shop/security-profile/proposals/"+proposal.Proposal.ID+"/approve", approval, key)
	if approved.Code != http.StatusOK {
		t.Fatalf("approval %d: %s", approved.Code, approved.Body.String())
	}
	profile.Schedule.Full = "0 3 * * *"
	draft := request("PUT", "/api/deployments/shop/security-profile", securityProfileRequest{BaseRevision: proposal.Proposal.ProfileRevision, Profile: profile}, key)
	if draft.Code != http.StatusOK {
		t.Fatalf("draft %d: %s", draft.Code, draft.Body.String())
	}
	stale := request("POST", "/api/deployments/shop/security-profile/proposals/"+proposal.Proposal.ID+"/approve", approval, key)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale %d: %s", stale.Code, stale.Body.String())
	}
	if out := request("GET", "/api/deployments/shop/security-reviews/run/evidence/evidence", nil, key); out.Code != http.StatusForbidden {
		t.Fatalf("evidence permission %d: %s", out.Code, out.Body.String())
	}
	serviceUser, err := manager.CreateUser("service-reviewer", "", "password-for-test-only", auth.RoleService, permissions)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.AssignDeployment(serviceUser.ID, "shop", auth.AccessLevelAdmin, owner.ID); err != nil {
		t.Fatal(err)
	}
	_, serviceKey, err := manager.CreateAPIKey(serviceUser.ID, "service-admin-override", "", auth.RoleAdmin, permissions, auth.DeploymentAccess{"shop": auth.AccessLevelAdmin}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/deployments/shop/security-profile/proposals/" + queuedBody.Proposal.ID + "/approve", map[string]string{}},
		{"PUT", "/api/deployments/shop/security-reviews/retention-run/hold", map[string]bool{"hold": true}},
		{"POST", "/api/deployments/shop/security-findings/candidate/close", map[string]string{"reason": "Reviewed"}},
	} {
		if out := request(operation.method, operation.path, operation.body, serviceKey); out.Code != http.StatusForbidden {
			t.Fatalf("service identity approved owner decision: %d %s", out.Code, out.Body.String())
		}
	}
	outsider, err := manager.CreateUser("outsider", "", "password-for-test-only", auth.RoleViewer, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.AssignDeployment(outsider.ID, "other", auth.AccessLevelRead, owner.ID); err != nil {
		t.Fatal(err)
	}
	_, wideKey, err := manager.CreateAPIKey(outsider.ID, "overbroad", "", auth.RoleAdmin, permissions, auth.DeploymentAccess{"shop": auth.AccessLevelAdmin, "other": auth.AccessLevelAdmin}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if out := request("PUT", "/api/security-permission-probe", nil, wideKey); out.Code != http.StatusForbidden {
		t.Fatalf("key widened non-security permissions: %d", out.Code)
	}
	if out := request("PUT", "/api/deployments/other/security-profile", securityProfileRequest{Profile: profile}, wideKey); out.Code != http.StatusForbidden {
		t.Fatalf("key widened owner permissions %d: %s", out.Code, out.Body.String())
	}
}

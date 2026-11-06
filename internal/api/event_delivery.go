package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/events"
	"github.com/flatrun/agent/internal/runs"
	"github.com/flatrun/agent/internal/runtimes"
	"github.com/flatrun/agent/internal/webhook"
	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
)

type deliveryRecord struct {
	ID            string            `json:"id"`
	Source        string            `json:"source"`
	Type          string            `json:"type"`
	Time          time.Time         `json:"time"`
	Outcome       string            `json:"outcome"`
	Subscriptions map[string]string `json:"subscriptions"`
}

type deliveryState struct {
	mu      sync.Mutex
	records []*deliveryRecord
	active  int
	wg      sync.WaitGroup
	closed  bool
}

func (s *Server) receiveEvent(c *gin.Context) {
	var endpoint *config.EventEndpoint
	for i := range s.config.Events.Endpoints {
		candidate := &s.config.Events.Endpoints[i]
		if candidate.Name == c.Param("endpoint") {
			endpoint = candidate
			break
		}
	}
	notFound := func() { c.JSON(http.StatusNotFound, gin.H{"error": "webhook not found"}) }
	if endpoint == nil {
		notFound()
		return
	}
	provider, ok := s.webhooks.Get(endpoint.Provider)
	if !ok {
		notFound()
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxWebhookBody))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	if provider.Verify(c.Request.Header, body, endpoint.Secret) != nil {
		notFound()
		return
	}
	event, err := webhook.Normalize(provider, endpoint.Name, c.Request.Header, body)
	if errors.Is(err, webhook.ErrUnsupported) {
		c.JSON(http.StatusOK, gin.H{"status": "ignored"})
		return
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	state := &s.deliveries
	record, duplicate, err := state.begin(event, s.config.Events.DeliveryLimit)
	if duplicate {
		c.JSON(http.StatusOK, gin.H{"status": "duplicate"})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "event queue full"})
		return
	}
	go func() {
		defer state.wg.Done()
		outcome := "ignored"
		outcomes := make(map[string]string, len(record.Subscriptions))
		for name, result := range record.Subscriptions {
			outcomes[name] = result
		}
		for _, subscription := range s.config.Events.Subscriptions {
			if len(record.Subscriptions) > 0 && record.Subscriptions[subscription.Name] == "" {
				continue
			}
			if outcomes[subscription.Name] == "succeeded" {
				if outcome != "failed" {
					outcome = "succeeded"
				}
				continue
			}
			if !event.Matches(subscription.Source, subscription.Type, subscription.Attributes, subscription.Labels) {
				continue
			}
			if err := s.actOnEvent(subscription, event); err != nil {
				outcome = "failed"
				outcomes[subscription.Name] = "failed"
				continue
			}
			outcomes[subscription.Name] = "succeeded"
			if outcome != "failed" {
				outcome = "succeeded"
			}
		}
		state.finish(record, outcome, outcomes)
	}()
	c.JSON(http.StatusAccepted, gin.H{"status": "accepted"})
}

func (s *Server) actOnEvent(subscription config.EventSubscription, e *webhook.Envelope) error {
	switch subscription.Action {
	case "run":
		if s.runsManager == nil {
			return errors.New("runs unavailable")
		}
		template := subscription.Run
		spec := runs.Spec{Runtime: runtimes.Type(template.Runtime), Image: template.Image, Command: append([]string(nil), template.Command...), Env: map[string]string{}, WorkDir: template.WorkDir, ArtifactPaths: append([]string(nil), template.ArtifactPaths...), CPUs: template.CPUs, MemoryBytes: template.MemoryBytes, Egress: template.Egress, TimeoutSeconds: template.TimeoutSeconds}
		for k, v := range template.Env {
			spec.Env[k] = v
		}
		spec.Env["FLATRUN_EVENT_BRANCH"] = e.Attributes["branch"]
		spec.Env["FLATRUN_EVENT_REVISION"] = e.Attributes["revision"]
		spec.Env["FLATRUN_EVENT_JOB_ID"] = e.Attributes["job_id"]
		spec.Env["FLATRUN_EVENT_TYPE"] = e.Type
		spec.Env["FLATRUN_EVENT_SUBJECT"] = e.Subject
		secrets := map[string]string{}
		defer clear(secrets)
		if subscription.GitHubRunner {
			if e.Type != "github.workflow_job.queued" {
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			app := s.githubApp
			if app == nil {
				app = &webhook.GitHubApp{AppID: s.config.Events.GitHubApp.AppID, PrivateKeyFile: s.config.Events.GitHubApp.PrivateKeyFile}
			}
			if err := s.runsManager.CheckRuntime(ctx, spec.Runtime); err != nil {
				log.Printf("Event runner delivery failed: runtime %s unavailable: %v", spec.Runtime, err)
				return fmt.Errorf("runner runtime %s unavailable: %w", spec.Runtime, err)
			}
			jit, err := app.JIT(ctx, e, subscription.RunnerGroupID)
			if err != nil {
				return err
			}
			secrets["JIT"] = jit
			spec.Command = []string{"sh", "-c", `exec ./run.sh --jitconfig "$JIT"`}
			spec.WorkDir = "/home/runner"
			spec.Egress = true
			if spec.Env == nil {
				spec.Env = map[string]string{}
			}
			spec.Env["RUNNER_ALLOW_RUNASROOT"] = "1"
			if spec.Image == "" {
				spec.Image = "ghcr.io/actions/actions-runner"
			}
		}
		_, err := s.runsManager.Submit(0, "events", spec, secrets)
		return err
	case "deploy", "preview":
		deployment, err := s.manager.GetDeployment(subscription.Deployment)
		if err != nil {
			return err
		}
		if deployment.Metadata == nil || deployment.Metadata.Source == nil || !e.Event.MatchesRepo(deployment.Metadata.Source.Ref) {
			return errors.New("deployment source mismatch")
		}
		recorder := &eventActionResponse{}
		allowed := false
		for _, builtin := range builtinWebhookSubscriptions {
			if builtin.action == subscription.Action && builtin.matches(e.Event.Kind) {
				allowed = true
			}
		}
		if !allowed {
			return errors.New("event does not support deployment action")
		}
		s.applyWebhookSubscription(recorder, subscription.Action, subscription.Deployment, deployment.Metadata.Source, strings.SplitN(e.Source, "/", 2)[0], e.Event)
		if recorder.status >= 400 {
			return fmt.Errorf("action returned %d", recorder.status)
		}
		return nil
	case "notify":
		if s.notify == nil {
			return errors.New("notifications unavailable")
		}
		_, err := s.notify.Publish(events.Event{ID: e.ID, Source: e.Source, Type: e.Type, Title: e.Type, Message: e.Subject, Severity: events.SeverityInfo, OccurredAt: e.Time})
		return err
	default:
		return errors.New("unknown event action")
	}
}

func (s *Server) listEventSubscriptions(c *gin.Context) {
	if !globalEventsAccess(c) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"subscriptions": s.config.Events.Subscriptions})
}
func (s *Server) listEventDeliveries(c *gin.Context) {
	if !globalEventsAccess(c) {
		return
	}
	s.deliveries.mu.Lock()
	defer s.deliveries.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"deliveries": s.deliveries.records})
}
func globalEventsAccess(c *gin.Context) bool {
	actor := auth.GetActorFromContext(c)
	if actor == nil || !hasGlobalRunAccess(actor) {
		c.JSON(http.StatusForbidden, gin.H{"error": "global access required"})
		return false
	}
	return true
}

type eventActionResponse struct{ status int }

func (w *eventActionResponse) JSON(status int, _ any) { w.status = status }

func (state *deliveryState) begin(event *webhook.Envelope, limit int) (*deliveryRecord, bool, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	retry := -1
	outcomes := map[string]string{}
	for i, record := range state.records {
		if record.ID == event.ID && record.Source == event.Source {
			if record.Outcome != "failed" {
				return nil, true, nil
			}
			for name, outcome := range record.Subscriptions {
				outcomes[name] = outcome
			}
			retry = i
			break
		}
	}
	if state.closed || state.active >= 128 {
		return nil, false, errors.New("event queue full")
	}
	if retry >= 0 {
		state.records = append(state.records[:retry], state.records[retry+1:]...)
	}
	if limit <= 0 {
		limit = 1000
	}
	if limit > 10000 {
		limit = 10000
	}
	if len(state.records) >= limit {
		removed := false
		for i, record := range state.records {
			if record.Outcome != "processing" {
				state.records = append(state.records[:i], state.records[i+1:]...)
				removed = true
				break
			}
		}
		if !removed {
			return nil, false, errors.New("event queue full")
		}
	}
	record := &deliveryRecord{ID: event.ID, Source: event.Source, Type: event.Type, Time: event.Time, Outcome: "processing", Subscriptions: outcomes}
	state.records = append(state.records, record)
	state.active++
	state.wg.Add(1)
	return record, false, nil
}

func (state *deliveryState) finish(record *deliveryRecord, outcome string, outcomes map[string]string) {
	state.mu.Lock()
	defer state.mu.Unlock()
	record.Outcome = outcome
	record.Subscriptions = outcomes
	state.active--
}

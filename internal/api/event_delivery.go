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
	"github.com/flatrun/agent/internal/webhook"
	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
)

type deliveryRecord = events.Delivery

type deliveryState struct {
	mu     sync.Mutex
	store  *events.Store
	active int
	wg     sync.WaitGroup
	closed bool
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
	event, parsed, err := webhook.Normalize(provider, endpoint.Name, c.Request.Header, body)
	if errors.Is(err, webhook.ErrUnsupported) {
		c.JSON(http.StatusOK, gin.H{"status": "ignored"})
		return
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	state := &s.deliveries
	record, duplicate, err := s.beginDelivery(event, s.config.Events.DeliveryLimit)
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
			kind := subscription.Type
			if subscription.Action == "github_runner" {
				kind = "github.workflow_job.queued"
			}
			if !event.Matches(subscription.Source, kind, subscription.Attributes, subscription.Labels) {
				continue
			}
			if err := s.actOnEvent(subscription, event, parsed); err != nil {
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

func (s *Server) actOnEvent(subscription config.EventSubscription, e events.Event, parsed *webhook.Event) error {
	switch subscription.Action {
	case "run", "github_runner":
		if s.runsManager == nil {
			return errors.New("runs unavailable")
		}
		spec := subscription.Run
		if subscription.Action == "github_runner" {
			r := subscription.GitHubRunner
			spec = runs.Spec{Runtime: r.Runtime, Image: r.Image, TimeoutSeconds: r.TimeoutSeconds, CPUs: r.CPUs, MemoryBytes: r.MemoryBytes}
		}
		spec.Env = make(map[string]string, len(subscription.Run.Env)+5)
		for k, v := range subscription.Run.Env {
			spec.Env[k] = v
		}
		spec.Env["FLATRUN_EVENT_BRANCH"] = e.Attribute("branch")
		spec.Env["FLATRUN_EVENT_REVISION"] = e.Attribute("revision")
		spec.Env["FLATRUN_EVENT_JOB_ID"] = e.Attribute("job_id")
		spec.Env["FLATRUN_EVENT_TYPE"] = e.Type
		spec.Env["FLATRUN_EVENT_SUBJECT"] = e.Attribute("subject")

		secrets := map[string]string{}
		defer clear(secrets)
		if subscription.Action == "github_runner" {
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
			jit, err := app.JIT(ctx, e, subscription.GitHubRunner.RunnerGroupID, subscription.Labels)
			if err != nil {
				return err
			}
			secrets["JIT"] = jit
			spec.Command = []string{"sh", "-c", `exec ./run.sh --jitconfig "$JIT"`}
			spec.WorkDir = "/home/runner"
			spec.Egress = true
			spec.Env["RUNNER_ALLOW_RUNASROOT"] = "1"
		}
		_, err := s.runsManager.Submit(0, "events", spec, secrets)
		return err
	case "deploy", "preview":
		deployment, err := s.manager.GetDeployment(subscription.Deployment)
		if err != nil {
			return err
		}
		if deployment.Metadata == nil || deployment.Metadata.Source == nil || !parsed.MatchesRepo(deployment.Metadata.Source.Ref) {
			return errors.New("deployment source mismatch")
		}
		recorder := &eventActionResponse{}
		allowed := false
		for _, builtin := range builtinWebhookSubscriptions {
			if builtin.action == subscription.Action && builtin.matches(parsed.Kind) {
				allowed = true
			}
		}
		if !allowed {
			return errors.New("event does not support deployment action")
		}
		s.applyWebhookSubscription(recorder, subscription.Action, subscription.Deployment, deployment.Metadata.Source, strings.SplitN(e.Source, "/", 2)[0], parsed)
		if recorder.status >= 400 {
			return fmt.Errorf("action returned %d", recorder.status)
		}
		return nil
	case "notify":
		if s.notify == nil {
			return errors.New("notifications unavailable")
		}
		_, err := s.notify.Publish(e)
		return err
	default:
		return errors.New("unknown event action")
	}
}

func (s *Server) listEventSubscriptions(c *gin.Context) {
	if !globalEventsAccess(c) {
		return
	}
	c.JSON(http.StatusOK, struct {
		Subscriptions []config.EventSubscription `json:"subscriptions"`
	}{Subscriptions: s.config.Events.Subscriptions})
}
func (s *Server) listEventDeliveries(c *gin.Context) {
	if !globalEventsAccess(c) {
		return
	}
	records, err := s.deliveryRecords()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delivery log unavailable"})
		return
	}
	c.JSON(http.StatusOK, struct {
		Deliveries []events.Delivery `json:"deliveries"`
	}{Deliveries: records})
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

func (s *Server) deliveryStore() (*events.Store, error) {
	if s.deliveries.store != nil {
		return s.deliveries.store, nil
	}
	if s.notify != nil {
		store, err := s.notify.EventStore()
		if err != nil {
			return nil, err
		}
		s.deliveries.store = store
	} else {
		if s.config == nil || s.config.DeploymentsPath == "" {
			return nil, errors.New("event store path unavailable")
		}
		store, err := events.NewStore(s.config.DeploymentsPath)
		if err != nil {
			return nil, err
		}
		s.deliveries.store = store
	}
	return s.deliveries.store, nil
}

func (s *Server) deliveryRecords() ([]events.Delivery, error) {
	s.deliveries.mu.Lock()
	defer s.deliveries.mu.Unlock()
	store, err := s.deliveryStore()
	if err != nil {
		return nil, err
	}
	return store.ListDeliveries()
}

func (s *Server) beginDelivery(event events.Event, limit int) (*deliveryRecord, bool, error) {
	state := &s.deliveries
	state.mu.Lock()
	defer state.mu.Unlock()
	store, err := s.deliveryStore()
	if err != nil {
		return nil, false, err
	}
	if state.closed || state.active >= 128 {
		return nil, false, errors.New("event queue full")
	}
	record, duplicate, err := store.BeginDelivery(event, limit)
	if err != nil || duplicate {
		return record, duplicate, err
	}
	state.active++
	state.wg.Add(1)
	return record, false, nil
}

func (state *deliveryState) finish(record *deliveryRecord, outcome string, outcomes map[string]string) {
	state.mu.Lock()
	defer state.mu.Unlock()
	record.Outcome, record.Subscriptions = outcome, outcomes
	if err := state.store.FinishDelivery(*record); err != nil {
		log.Printf("Record event delivery: %v", err)
	}
	state.active--
}

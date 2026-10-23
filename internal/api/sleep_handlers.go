package api

import (
	"context"
	"fmt"
	"html"
	"log"
	"net/http"
	"time"

	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/sleep"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

const (
	sleepCheckInterval = time.Minute
	// sleepNetworkThreshold is the received-bytes rise per check that counts as
	// traffic rather than background chatter.
	sleepNetworkThreshold = 16 * 1024
	minIdleMinutes        = 5
	// wakeGracePeriod keeps serving the waiting page while a woken app boots.
	wakeGracePeriod = 2 * time.Minute
)

// trafficActivity reports the last request nginx logged for a deployment.
type trafficActivity struct{ s *Server }

func (t trafficActivity) LastActive(_ context.Context, deployment string, _ time.Time) (time.Time, error) {
	if t.s.trafficManager == nil {
		return time.Time{}, nil
	}
	return t.s.trafficManager.LastSeen(deployment)
}

func deploymentReceivedBytes(_ context.Context, deployment string) (uint64, error) {
	stats, err := docker.GetDeploymentStats(deployment)
	if err != nil {
		return 0, err
	}
	var total uint64
	for _, c := range stats {
		total += c.NetworkRx
	}
	return total, nil
}

func (s *Server) newSleepController() *sleep.Controller {
	return sleep.NewController(time.Now(),
		trafficActivity{s},
		&sleep.NetworkActivity{ReceivedBytes: deploymentReceivedBytes, Threshold: sleepNetworkThreshold},
	)
}

func (s *Server) runSleepLoop(ctx context.Context) {
	ticker := time.NewTicker(sleepCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.putIdleDeploymentsToSleep(ctx, time.Now())
		}
	}
}

// putIdleDeploymentsToSleep stops every running, sleep-enabled deployment that
// has been idle for its configured time.
func (s *Server) putIdleDeploymentsToSleep(ctx context.Context, now time.Time) []string {
	deployments, err := s.manager.ListDeployments()
	if err != nil {
		log.Printf("Warning: sleep check could not list deployments: %v", err)
		return nil
	}
	var targets []sleep.Target
	for _, d := range deployments {
		cfg := sleepConfigOf(d.Metadata)
		if cfg == nil || cfg.Sleeping || d.Status != "running" {
			continue
		}
		targets = append(targets, sleep.Target{Name: d.Name, IdleFor: time.Duration(cfg.IdleMinutes) * time.Minute})
	}

	due := s.sleeper.Due(ctx, targets, now)
	for _, name := range due {
		if err := s.setSleeping(name, true); err != nil {
			log.Printf("Warning: failed to mark %s sleeping: %v", name, err)
			continue
		}
		if _, created := s.startDeploymentJob(name, "stop", actionOptions{putToSleep: true}); !created {
			log.Printf("Sleep for %s deferred: an action is running", name)
			_ = s.setSleeping(name, false)
		}
	}
	return due
}

func sleepConfigOf(meta *models.ServiceMetadata) *models.SleepConfig {
	if meta == nil || meta.Sleep == nil || !meta.Sleep.Enabled {
		return nil
	}
	return meta.Sleep
}

func (s *Server) setSleeping(name string, sleeping bool) error {
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		return err
	}
	if deployment.Metadata == nil || deployment.Metadata.Sleep == nil || deployment.Metadata.Sleep.Sleeping == sleeping {
		return nil
	}
	deployment.Metadata.Sleep.Sleeping = sleeping
	return s.manager.SaveMetadata(name, deployment.Metadata)
}

// markAwake clears the sleeping flag when a deployment is started by any path,
// and restarts its idle clock.
func (s *Server) markAwake(name string) {
	if s.sleeper != nil {
		s.sleeper.NoteAwake(name, time.Now())
	}
	if err := s.setSleeping(name, false); err != nil {
		log.Printf("Warning: failed to clear sleeping flag for %s: %v", name, err)
	}
}

const wakePage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="3"><title>Starting up</title>
<style>body{margin:0;min-height:100vh;display:grid;place-items:center;font:16px system-ui,sans-serif;background:#0f1115;color:#e8eaf0}
main{text-align:center;padding:24px}p{color:#9aa3b2}</style></head>
<body><main><h1>Waking up</h1><p>%s was asleep and is starting. This page reloads on its own.</p></main></body></html>`

// wakeDeployment is reached through nginx when a sleeping deployment's
// containers are gone. It starts the deployment and serves a page that
// reloads until the app answers.
func (s *Server) wakeDeployment(c *gin.Context) {
	name := c.Param("deployment")
	deployment, err := s.manager.GetDeployment(name)
	if err != nil || sleepConfigOf(deployment.Metadata) == nil {
		c.String(http.StatusBadGateway, "Service unavailable")
		return
	}
	// Only a deployment the agent put to sleep is woken; one a person stopped
	// stays stopped.
	recentlyWoken := s.sleeper != nil && time.Since(s.sleeper.AwakeSince(name)) < wakeGracePeriod
	if !deployment.Metadata.Sleep.Sleeping && !recentlyWoken {
		c.String(http.StatusBadGateway, "Service unavailable")
		return
	}
	if deployment.Metadata.Sleep.Sleeping && deployment.Status != "running" {
		if _, created := s.startDeploymentJob(name, "start", actionOptions{}); created {
			log.Printf("Waking %s on request", name)
		}
	}
	c.Header("Retry-After", "3")
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusServiceUnavailable, "text/html; charset=utf-8",
		[]byte(fmt.Sprintf(wakePage, html.EscapeString(name))))
}

// updateDeploymentSleep turns sleeping on or off and regenerates the virtual
// host so nginx knows to wake the deployment.
func (s *Server) updateDeploymentSleep(c *gin.Context) {
	name := c.Param("name")
	var req struct {
		Enabled     bool `json:"enabled"`
		IdleMinutes int  `json:"idle_minutes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Enabled && req.IdleMinutes < minIdleMinutes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "idle_minutes must be at least 5"})
		return
	}
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	meta := deployment.Metadata
	if meta == nil {
		meta = &models.ServiceMetadata{Name: name}
	}
	sleeping := meta.Sleep != nil && meta.Sleep.Sleeping && req.Enabled
	meta.Sleep = &models.SleepConfig{Enabled: req.Enabled, IdleMinutes: req.IdleMinutes, Sleeping: sleeping}
	if err := s.manager.SaveMetadata(name, meta); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if s.proxyOrchestrator != nil && (meta.Networking.Expose || len(meta.GetDomains()) > 0) {
		if updated, err := s.manager.GetDeployment(name); err == nil {
			if _, err := s.proxyOrchestrator.UpdateDeployment(updated); err != nil {
				log.Printf("Warning: failed to update virtual host for %s: %v", name, err)
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"sleep": meta.Sleep})
}

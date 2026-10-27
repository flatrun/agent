package api

import (
	"context"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/flatrun/agent/internal/access"
	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/sleep"
	"github.com/flatrun/agent/pkg/models"
)

// A service's idle clock is keyed "deployment/service"; deployment names
// cannot contain "/", so the key never collides with a deployment's own.
func serviceSleepKey(deployment, service string) string {
	return deployment + "/" + service
}

func splitSleepKey(key string) (deployment, service string, isService bool) {
	return strings.Cut(key, "/")
}

func sleepTargetReceivedBytes(_ context.Context, key string) (uint64, error) {
	deployment, service, isService := splitSleepKey(key)
	var stats []docker.ContainerStats
	var err error
	if isService {
		stats, err = docker.GetServiceStats(deployment, service)
	} else {
		stats, err = docker.GetDeploymentStats(deployment)
	}
	if err != nil {
		return 0, err
	}
	var total uint64
	for _, c := range stats {
		total += c.NetworkRx
	}
	return total, nil
}

func serviceRunning(d models.Deployment, service string) bool {
	for _, svc := range d.Services {
		if svc.Name == service {
			return svc.RunningReplicas > 0 || svc.Status == "running"
		}
	}
	return false
}

// serviceSleepTargets lists the services of d that sleep on their own and
// may be put to sleep now: running, not already asleep, and not needed by a
// running service that is awake.
func serviceSleepTargets(d models.Deployment) []sleep.Target {
	meta := d.Metadata
	if meta == nil || (meta.Sleep != nil && meta.Sleep.Enabled) {
		return nil
	}
	var targets []sleep.Target
	for service, cfg := range meta.ServiceSleep {
		if !cfg.Enabled || cfg.Sleeping || !serviceRunning(d, service) || neededByAwakeService(d, service) {
			continue
		}
		targets = append(targets, sleep.Target{
			Name:    serviceSleepKey(d.Name, service),
			IdleFor: time.Duration(cfg.IdleMinutes) * time.Minute,
		})
	}
	return targets
}

func neededByAwakeService(d models.Deployment, service string) bool {
	for _, other := range d.Services {
		if other.Name == service || !slices.Contains(other.DependsOn, service) {
			continue
		}
		if serviceRunning(d, other.Name) && !d.Metadata.ServiceSleep[other.Name].Sleeping {
			return true
		}
	}
	return false
}

func (s *Server) putServiceToSleep(deployment, service string) {
	if err := s.setServiceSleeping(deployment, service, true); err != nil {
		log.Printf("Warning: failed to mark %s/%s sleeping: %v", deployment, service, err)
		return
	}
	job, created := s.jobs.createScoped(deployment, service, "stop", actionOptions{putToSleep: true})
	if !created {
		log.Printf("Sleep for %s/%s deferred: an action is running", deployment, service)
		_ = s.setServiceSleeping(deployment, service, false)
		return
	}
	go s.runServiceActionJob(job)
}

func (s *Server) setServiceSleeping(name, service string, sleeping bool) error {
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		return err
	}
	meta := deployment.Metadata
	if meta == nil {
		return nil
	}
	cfg, ok := meta.ServiceSleep[service]
	if !ok || cfg.Sleeping == sleeping {
		return nil
	}
	cfg.Sleeping = sleeping
	meta.ServiceSleep[service] = cfg
	return s.manager.SaveMetadata(name, meta)
}

// markServiceAwake clears a service's sleeping flag when it is started by
// any path, and restarts its idle clock.
func (s *Server) markServiceAwake(name, service string) {
	if s.sleeper != nil {
		s.sleeper.NoteAwake(serviceSleepKey(name, service), time.Now())
	}
	if err := s.setServiceSleeping(name, service, false); err != nil {
		log.Printf("Warning: failed to clear sleeping flag for %s/%s: %v", name, service, err)
	}
}

// wakeOrder returns the services to start for service to answer: its
// dependencies, theirs first, then the service itself.
func wakeOrder(d *models.Deployment, service string) []string {
	dependsOn := map[string][]string{}
	for _, svc := range d.Services {
		dependsOn[svc.Name] = svc.DependsOn
	}
	var order []string
	seen := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		for _, dep := range dependsOn[name] {
			visit(dep)
		}
		order = append(order, name)
	}
	visit(service)
	return order
}

// runWakeJob starts the sleeping or stopped services the woken service
// needs, then the service itself. compose's own start of one service does
// not start its dependencies.
func (s *Server) runWakeJob(job *ActionJob) {
	job.setRunning()
	deployment, err := s.manager.GetDeployment(job.deployment)
	if err != nil {
		s.jobs.finish(job, JobFailed, err.Error())
		return
	}
	for _, service := range wakeOrder(deployment, job.service) {
		if service != job.service && serviceRunning(*deployment, service) {
			continue
		}
		job.appendLine("Starting " + service)
		if err := s.runServiceAction("start", job.deployment, service, actionOptions{}, job.appendLine); err != nil {
			s.jobs.finish(job, JobFailed, err.Error())
			return
		}
	}
	s.jobs.finish(job, JobSucceeded, "")
}

// startSleepingDependencies starts the services that service depends on and
// that are asleep on their own, so starting it by hand does not leave it
// without its database or other dependencies.
func (s *Server) startSleepingDependencies(job *ActionJob) error {
	if s.manager == nil {
		return nil
	}
	deployment, err := s.manager.GetDeployment(job.deployment)
	if err != nil || deployment.Metadata == nil {
		return nil
	}
	for _, dep := range wakeOrder(deployment, job.service) {
		if dep == job.service || !deployment.Metadata.ServiceSleep[dep].Sleeping {
			continue
		}
		job.appendLine("Starting " + dep + ", which " + job.service + " depends on")
		if err := s.runServiceAction("start", job.deployment, dep, actionOptions{}, job.appendLine); err != nil {
			return err
		}
	}
	return nil
}

// wakeService is reached through nginx when a location's service sleeps on
// its own and its container is gone.
func (s *Server) wakeService(c *gin.Context) {
	name, service := c.Param("deployment"), c.Param("service")
	c.Header("Cache-Control", "no-store")
	deployment, err := s.manager.GetDeployment(name)
	if err != nil || !deployment.Metadata.ServiceSleepEnabled(service) {
		c.Data(http.StatusBadGateway, "text/html; charset=utf-8", []byte(unavailablePage()))
		return
	}
	cfg := deployment.Metadata.ServiceSleep[service]
	recentlyWoken := s.sleeper != nil && time.Since(s.sleeper.AwakeSince(serviceSleepKey(name, service))) < wakeGracePeriod
	if !cfg.Sleeping && !recentlyWoken {
		c.Data(http.StatusBadGateway, "text/html; charset=utf-8", []byte(unavailablePage()))
		return
	}
	if cfg.Sleeping {
		if job, created := s.jobs.createScoped(name, service, "start", actionOptions{}); created {
			log.Printf("Waking %s/%s on request", name, service)
			go s.runWakeJob(job)
		}
	}
	c.Header("Retry-After", "3")
	c.Data(http.StatusServiceUnavailable, "text/html; charset=utf-8", []byte(wakePage(access.Hostname(c.GetHeader("X-Original-Host")))))
}

// updateServiceSleep turns sleeping on or off for one service and
// regenerates the virtual host so nginx knows to wake it.
func (s *Server) updateServiceSleep(c *gin.Context) {
	name, service := c.Param("name"), c.Param("service")
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
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}
	if _, err := s.resolveService(name, service); err != nil || service == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "Service not found"})
		return
	}
	previous, err := cloneMetadata(deployment.Metadata)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	meta := deployment.Metadata
	if meta == nil {
		meta = &models.ServiceMetadata{Name: name}
	}
	if meta.ServiceSleep == nil {
		meta.ServiceSleep = map[string]models.SleepConfig{}
	}
	sleeping := meta.ServiceSleep[service].Sleeping && req.Enabled
	resume := !req.Enabled && meta.ServiceSleep[service].Sleeping
	if req.Enabled {
		meta.ServiceSleep[service] = models.SleepConfig{Enabled: true, IdleMinutes: req.IdleMinutes, Sleeping: sleeping}
	} else {
		delete(meta.ServiceSleep, service)
	}
	if len(meta.ServiceSleep) == 0 {
		meta.ServiceSleep = nil
	}
	if err := s.manager.SaveMetadata(name, meta); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if s.proxyOrchestrator != nil && (meta.Networking.Expose || len(meta.GetDomains()) > 0) {
		if updated, err := s.manager.GetDeployment(name); err == nil {
			if _, err := s.proxyOrchestrator.UpdateDeployment(updated); err != nil {
				if !s.config.Nginx.Enabled {
					log.Printf("Warning: failed to update virtual host for %s: %v", name, err)
				} else {
					if previous != nil {
						if err := s.manager.SaveMetadata(name, previous); err != nil {
							log.Printf("warning: failed to restore metadata for %s: %v", name, err)
						}
					}
					c.JSON(http.StatusConflict, gin.H{
						"error": "Proxy update failed, the previous settings are still active: " + err.Error(),
					})
					return
				}
			}
		}
	}
	// The wake route goes away with the setting, so a service turned off while
	// asleep must be started here or no request can start it again.
	if resume {
		if job, created := s.jobs.createScoped(name, service, "start", actionOptions{}); created {
			go s.runWakeJob(job)
		}
	}
	cfg := meta.ServiceSleep[service]
	c.JSON(http.StatusOK, gin.H{
		"service": service,
		"sleep":   cfg,
		// False while the whole deployment sleeps, which takes precedence.
		"active": meta.ServiceSleepEnabled(service),
	})
}

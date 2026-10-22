// Package sleep decides when an idle deployment should be stopped. How a
// deployment shows activity is an Activity; the controller takes the most
// recent activity any of them reports, so signals can be added or replaced
// without touching the decision.
package sleep

import (
	"context"
	"sync"
	"time"
)

// Activity reports the last time a deployment was seen doing work. A zero time
// means this signal has no evidence either way.
type Activity interface {
	LastActive(ctx context.Context, deployment string, now time.Time) (time.Time, error)
}

type Target struct {
	Name    string
	IdleFor time.Duration
}

type Controller struct {
	activities []Activity
	mu         sync.Mutex
	awake      map[string]time.Time
	started    time.Time
}

// NewController starts the idle clock for every deployment at now, so a
// restarted agent never puts deployments to sleep on its first pass.
func NewController(now time.Time, activities ...Activity) *Controller {
	return &Controller{activities: activities, awake: map[string]time.Time{}, started: now}
}

// NoteAwake records that a deployment was just started, which counts as
// activity.
func (c *Controller) NoteAwake(name string, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.awake[name] = at
}

// AwakeSince returns when the deployment was last noted awake, or zero.
func (c *Controller) AwakeSince(name string) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.awake[name]
}

// Due returns the targets that have been idle for at least their IdleFor.
func (c *Controller) Due(ctx context.Context, targets []Target, now time.Time) []string {
	var due []string
	for _, t := range targets {
		if t.IdleFor <= 0 {
			continue
		}
		last := c.lastActive(ctx, t.Name, now)
		if now.Sub(last) >= t.IdleFor {
			due = append(due, t.Name)
		}
	}
	return due
}

func (c *Controller) lastActive(ctx context.Context, name string, now time.Time) time.Time {
	c.mu.Lock()
	last := c.started
	if awake, ok := c.awake[name]; ok && awake.After(last) {
		last = awake
	}
	c.mu.Unlock()
	for _, a := range c.activities {
		seen, err := a.LastActive(ctx, name, now)
		if err == nil && seen.After(last) {
			last = seen
		}
	}
	return last
}

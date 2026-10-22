package sleep

import (
	"context"
	"reflect"
	"testing"
	"time"
)

type fixedActivity map[string]time.Time

func (f fixedActivity) LastActive(_ context.Context, d string, _ time.Time) (time.Time, error) {
	return f[d], nil
}

func TestController_Due(t *testing.T) {
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	now := start.Add(30 * time.Minute)
	c := NewController(start, fixedActivity{"busy": now.Add(-time.Minute)})
	c.NoteAwake("woken", now.Add(-2*time.Minute))

	targets := []Target{
		{Name: "idle", IdleFor: 15 * time.Minute},
		{Name: "busy", IdleFor: 15 * time.Minute},
		{Name: "woken", IdleFor: 15 * time.Minute},
		{Name: "patient", IdleFor: time.Hour},
		{Name: "off", IdleFor: 0},
	}
	got := c.Due(context.Background(), targets, now)
	if !reflect.DeepEqual(got, []string{"idle"}) {
		t.Errorf("Due = %v, want [idle]", got)
	}
}

func TestController_AgentStartCountsAsActivity(t *testing.T) {
	start := time.Now()
	c := NewController(start)
	if got := c.Due(context.Background(), []Target{{Name: "app", IdleFor: time.Minute}}, start.Add(30*time.Second)); len(got) != 0 {
		t.Errorf("nothing should sleep right after the agent starts, got %v", got)
	}
}

func TestNetworkActivity(t *testing.T) {
	rx := map[string]uint64{"app": 1000}
	n := &NetworkActivity{
		ReceivedBytes: func(_ context.Context, d string) (uint64, error) { return rx[d], nil },
		Threshold:     500,
	}
	t0 := time.Now()
	ctx := context.Background()

	if seen, _ := n.LastActive(ctx, "app", t0); !seen.IsZero() {
		t.Errorf("first observation is a baseline, got %v", seen)
	}
	rx["app"] = 1200
	if seen, _ := n.LastActive(ctx, "app", t0.Add(time.Minute)); !seen.IsZero() {
		t.Errorf("chatter below threshold counted as activity: %v", seen)
	}
	rx["app"] = 5000
	t2 := t0.Add(2 * time.Minute)
	if seen, _ := n.LastActive(ctx, "app", t2); !seen.Equal(t2) {
		t.Errorf("traffic above threshold = %v, want %v", seen, t2)
	}
	rx["app"] = 10
	t3 := t0.Add(3 * time.Minute)
	if seen, _ := n.LastActive(ctx, "app", t3); !seen.Equal(t3) {
		t.Errorf("counter reset should count as activity, got %v", seen)
	}
}

package sleep

import (
	"context"
	"sync"
	"time"
)

// NetworkActivity treats a rise in a deployment's received bytes between two
// observations as activity. Threshold absorbs background chatter such as DNS
// and health probes.
type NetworkActivity struct {
	ReceivedBytes func(ctx context.Context, deployment string) (uint64, error)
	Threshold     uint64

	mu   sync.Mutex
	last map[string]uint64
	seen map[string]time.Time
}

func (n *NetworkActivity) LastActive(ctx context.Context, deployment string, now time.Time) (time.Time, error) {
	rx, err := n.ReceivedBytes(ctx, deployment)
	if err != nil {
		return time.Time{}, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.last == nil {
		n.last = map[string]uint64{}
		n.seen = map[string]time.Time{}
	}
	prev, known := n.last[deployment]
	n.last[deployment] = rx
	// A drop means the containers restarted and their counters reset.
	if known && (rx < prev || rx-prev > n.Threshold) {
		n.seen[deployment] = now
	}
	return n.seen[deployment], nil
}

package plan

import (
	"context"
	"log"
	"time"
)

const terminalRetention = 7 * 24 * time.Hour

// PruneOnce expires available plans past their TTL, deletes applied and
// failed plans older than retention, and deletes expired and obsolete
// plans older than a week. Returns how many files were touched.
func (s *Store) PruneOnce(now time.Time, retention time.Duration) int {
	plans, err := s.List(ListFilter{})
	if err != nil {
		log.Printf("Warning: plan prune failed to list plans: %v", err)
		return 0
	}
	touched := 0
	for _, p := range plans {
		switch p.Status {
		case StatusAvailable:
			if p.Expired(now) {
				p.Status = StatusExpired
				if err := s.Save(p); err == nil {
					touched++
				}
			}
		case StatusApplied, StatusFailed:
			if now.Sub(p.CreatedAt) > retention {
				if err := s.Delete(p.ID); err == nil {
					touched++
				}
			}
		case StatusExpired, StatusObsolete:
			if now.Sub(p.CreatedAt) > terminalRetention {
				if err := s.Delete(p.ID); err == nil {
					touched++
				}
			}
		}
	}
	return touched
}

func (s *Store) StartPruneLoop(ctx context.Context, interval, retention time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.PruneOnce(time.Now().UTC(), retention)
			}
		}
	}()
}

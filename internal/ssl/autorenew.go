package ssl

import (
	"context"
	"log"
	"sync"
	"time"
)

// Renewer runs periodic auto-renewal checks for certificates that have
// auto-renew enabled and are within the configured expiry threshold.
type Renewer struct {
	manager       *Manager
	thresholdDays int
	interval      time.Duration
	onRenew       func(domain string)

	mu     sync.Mutex
	cancel context.CancelFunc
}

func NewRenewer(manager *Manager, thresholdDays int, interval time.Duration, onRenew func(domain string)) *Renewer {
	if thresholdDays <= 0 {
		thresholdDays = 30
	}
	if interval <= 0 {
		interval = 12 * time.Hour
	}
	return &Renewer{
		manager:       manager,
		thresholdDays: thresholdDays,
		interval:      interval,
		onRenew:       onRenew,
	}
}

func (r *Renewer) Start(ctx context.Context) {
	r.mu.Lock()
	if r.cancel != nil {
		r.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.mu.Unlock()

	go r.loop(ctx)
}

func (r *Renewer) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
}

func (r *Renewer) loop(ctx context.Context) {
	// Run shortly after start so certs near expiry get picked up promptly.
	initial := time.NewTimer(30 * time.Second)
	defer initial.Stop()

	select {
	case <-ctx.Done():
		return
	case <-initial.C:
		r.Run()
	}

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.Run()
		}
	}
}

// Run performs one pass of the renewal check. Exported for tests and manual triggers.
func (r *Renewer) Run() {
	expiring, err := r.manager.GetExpiringCertificates(r.thresholdDays)
	if err != nil {
		log.Printf("auto-renew: failed to list expiring certificates: %v", err)
		return
	}

	for _, cert := range expiring {
		if !cert.AutoRenew {
			continue
		}
		if _, err := r.manager.RenewCertificate(cert.Domain); err != nil {
			log.Printf("auto-renew: failed to renew %s (days_left=%d): %v", cert.Domain, cert.DaysLeft, err)
			continue
		}
		log.Printf("auto-renew: renewed %s (was %d days from expiry)", cert.Domain, cert.DaysLeft)
		if r.onRenew != nil {
			r.onRenew(cert.Domain)
		}
	}
}

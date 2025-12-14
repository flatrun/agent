package security

import (
	"fmt"
	"sync"
	"time"
)

type Detector struct {
	ipRequestCount map[string]*requestWindow
	mu             sync.RWMutex

	// Thresholds
	rateWindowDuration time.Duration
	rateThreshold      int
	autoBlockThreshold int
}

type requestWindow struct {
	count     int
	windowEnd time.Time
}

func NewDetector() *Detector {
	return &Detector{
		ipRequestCount:     make(map[string]*requestWindow),
		rateWindowDuration: time.Minute,
		rateThreshold:      100,
		autoBlockThreshold: 50,
	}
}

// SetThresholds configures detection thresholds
func (d *Detector) SetThresholds(rateThreshold, autoBlockThreshold int, windowDuration time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rateThreshold = rateThreshold
	d.autoBlockThreshold = autoBlockThreshold
	d.rateWindowDuration = windowDuration
}

// Classify analyzes an incoming event and creates a SecurityEvent if it's security-relevant
func (d *Detector) Classify(event *IngestEvent) *SecurityEvent {
	// Track request rate
	highRate := d.trackRequestRate(event.SourceIP)

	var eventType, severity, message string

	switch {
	// Scanner detection (highest priority)
	case IsScanner(event.UserAgent):
		eventType = EventTypeScannerDetected
		severity = SeverityCritical
		message = fmt.Sprintf("Scanner detected: %s", event.UserAgent)

	// High request rate
	case highRate:
		eventType = EventTypeHighRequestRate
		severity = SeverityCritical
		message = fmt.Sprintf("High request rate from IP: %s", event.SourceIP)

	// Rate limit exceeded (429)
	case event.StatusCode == 429:
		eventType = EventTypeRateLimitExceeded
		severity = SeverityHigh
		message = fmt.Sprintf("Rate limit exceeded for: %s", event.RequestPath)

	// Unauthorized (401)
	case event.StatusCode == 401:
		eventType = EventTypeUnauthorizedAccess
		severity = SeverityMedium
		message = fmt.Sprintf("Unauthorized access attempt: %s", event.RequestPath)

	// Forbidden (403)
	case event.StatusCode == 403:
		eventType = EventTypeForbiddenAccess
		severity = SeverityMedium
		message = fmt.Sprintf("Forbidden access: %s", event.RequestPath)

	// Server error (5xx)
	case event.StatusCode >= 500:
		eventType = EventTypeServerError
		severity = SeverityHigh
		message = fmt.Sprintf("Server error %d: %s", event.StatusCode, event.RequestPath)

	// 404 on suspicious path
	case event.StatusCode == 404 && IsSuspiciousPath(event.RequestPath):
		eventType = EventTypeSuspiciousPath
		severity = SeverityLow
		message = GetSuspiciousPathDescription(event.RequestPath)

	// Suspicious path with any status
	case IsSuspiciousPath(event.RequestPath) && event.StatusCode != 200:
		eventType = EventTypeSuspiciousPath
		severity = SeverityLow
		message = GetSuspiciousPathDescription(event.RequestPath)

	default:
		return nil
	}

	createdAt := time.Now()
	if event.Timestamp > 0 {
		createdAt = time.Unix(event.Timestamp, 0)
	}

	return &SecurityEvent{
		EventType:      eventType,
		Severity:       severity,
		SourceIP:       event.SourceIP,
		RequestPath:    event.RequestPath,
		RequestMethod:  event.RequestMethod,
		StatusCode:     event.StatusCode,
		UserAgent:      event.UserAgent,
		Message:        message,
		DeploymentName: event.DeploymentName,
		CreatedAt:      createdAt,
	}
}

// trackRequestRate tracks request rate per IP and returns true if rate is too high
func (d *Detector) trackRequestRate(ip string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()

	window, exists := d.ipRequestCount[ip]
	if !exists || now.After(window.windowEnd) {
		d.ipRequestCount[ip] = &requestWindow{
			count:     1,
			windowEnd: now.Add(d.rateWindowDuration),
		}
		return false
	}

	window.count++
	return window.count > d.rateThreshold
}

// ShouldAutoBlock determines if an IP should be automatically blocked
func (d *Detector) ShouldAutoBlock(ip string, event *SecurityEvent) bool {
	// Always auto-block scanners
	if event.EventType == EventTypeScannerDetected {
		return true
	}

	// Auto-block high request rates
	if event.EventType == EventTypeHighRequestRate {
		return true
	}

	// Check for repeated critical events
	d.mu.RLock()
	window, exists := d.ipRequestCount[ip]
	d.mu.RUnlock()

	if exists && window.count > d.autoBlockThreshold && event.Severity == SeverityCritical {
		return true
	}

	return false
}

// CleanupOldWindows removes expired rate tracking windows
func (d *Detector) CleanupOldWindows() {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	for ip, window := range d.ipRequestCount {
		if now.After(window.windowEnd) {
			delete(d.ipRequestCount, ip)
		}
	}
}

// GetIPRequestCount returns the current request count for an IP
func (d *Detector) GetIPRequestCount(ip string) int {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if window, exists := d.ipRequestCount[ip]; exists {
		if time.Now().Before(window.windowEnd) {
			return window.count
		}
	}
	return 0
}

package security

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type Detector struct {
	ipRequestCount map[string]*requestWindow
	mu             sync.RWMutex

	// Thresholds
	windowDuration        time.Duration
	rateThreshold         int // high request rate
	notFoundThreshold     int // 404 responses
	authFailureThreshold  int // 401/403 responses
	uniquePathsThreshold  int // scanning many different paths
	repeatedHitsThreshold int // hammering same path
}

type requestWindow struct {
	count        int
	notFoundHits int            // 404 responses
	authFailures int            // 401/403 responses
	pathHits     map[string]int // path -> hit count
	windowEnd    time.Time
}

func NewDetector() *Detector {
	return &Detector{
		ipRequestCount:        make(map[string]*requestWindow),
		windowDuration:        2 * time.Minute,
		rateThreshold:         60, // 60 requests in 2 min
		notFoundThreshold:     10, // 10 404s in 2 min
		authFailureThreshold:  5,  // 5 auth failures in 2 min
		uniquePathsThreshold:  20, // 20 different paths in 2 min
		repeatedHitsThreshold: 30, // 30 hits to same path in 2 min
	}
}

// SetThresholds configures detection thresholds
func (d *Detector) SetThresholds(rateThreshold, notFoundThreshold, authFailureThreshold, uniquePathsThreshold, repeatedHitsThreshold int, windowDuration time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rateThreshold = rateThreshold
	d.notFoundThreshold = notFoundThreshold
	d.authFailureThreshold = authFailureThreshold
	d.uniquePathsThreshold = uniquePathsThreshold
	d.repeatedHitsThreshold = repeatedHitsThreshold
	d.windowDuration = windowDuration
}

// Classify analyzes an incoming event and creates a SecurityEvent if it's security-relevant
func (d *Detector) Classify(event *IngestEvent) *SecurityEvent {
	// Track request behavior
	highRate := d.trackRequest(event.SourceIP, event)

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

	case event.IncidentID != "" && event.StatusCode >= 400:
		eventType = EventTypeHTTPError
		severity = SeverityLow
		message = fmt.Sprintf("HTTP %d response: %s", event.StatusCode, event.RequestPath)

	default:
		return nil
	}

	createdAt := time.Now()
	if event.Timestamp > 0 {
		createdAt = time.Unix(event.Timestamp, 0)
	}

	return &SecurityEvent{
		IncidentID:     event.IncidentID,
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

// trackRequest tracks request behavior per IP and returns true if rate is too high
func (d *Detector) trackRequest(ip string, event *IngestEvent) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()

	window, exists := d.ipRequestCount[ip]
	if !exists || now.After(window.windowEnd) {
		d.ipRequestCount[ip] = &requestWindow{
			count:     1,
			pathHits:  make(map[string]int),
			windowEnd: now.Add(d.windowDuration),
		}
		window = d.ipRequestCount[ip]
	} else {
		window.count++
	}

	// Track hits per path
	window.pathHits[event.RequestPath]++

	// Track 404 responses (probing for files/paths)
	if event.StatusCode == 404 {
		window.notFoundHits++
	}

	// Track auth failures (401, 403)
	if event.StatusCode == 401 || event.StatusCode == 403 {
		window.authFailures++
	}

	return window.count > d.rateThreshold
}

// ShouldAutoBlock reports whether an IP should be auto-blocked, and when it
// should, a human-readable reason naming the rule and the counts or paths that
// tripped it, so a blocked IP carries a trace of what led to the block.
func (d *Detector) ShouldAutoBlock(ip string, event *SecurityEvent) (bool, string) {
	if event.EventType == EventTypeScannerDetected {
		return true, event.Message
	}
	if event.EventType == EventTypeHighRequestRate {
		return true, event.Message
	}

	// The window fields (including pathHits) are mutated by trackRequest under
	// the lock, so read them while holding it rather than through a bare pointer.
	d.mu.RLock()
	defer d.mu.RUnlock()

	window, exists := d.ipRequestCount[ip]
	if !exists {
		return false, ""
	}

	if window.notFoundHits >= d.notFoundThreshold {
		return true, fmt.Sprintf("%d not-found responses in the detection window; paths: %s",
			window.notFoundHits, summarizePaths(window.pathHits))
	}
	if window.authFailures >= d.authFailureThreshold {
		return true, fmt.Sprintf("%d authentication failures in the detection window", window.authFailures)
	}
	if len(window.pathHits) >= d.uniquePathsThreshold {
		return true, fmt.Sprintf("probed %d distinct paths in the detection window; paths: %s",
			len(window.pathHits), summarizePaths(window.pathHits))
	}
	for path, hits := range window.pathHits {
		if hits >= d.repeatedHitsThreshold {
			return true, fmt.Sprintf("%d requests to %s in the detection window", hits, path)
		}
	}

	return false, ""
}

// summarizePaths renders the busiest few paths in a window for a block reason,
// most-hit first so the output is stable regardless of map order.
func summarizePaths(pathHits map[string]int) string {
	type ph struct {
		path string
		hits int
	}
	items := make([]ph, 0, len(pathHits))
	for p, h := range pathHits {
		items = append(items, ph{p, h})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].hits != items[j].hits {
			return items[i].hits > items[j].hits
		}
		return items[i].path < items[j].path
	})

	const maxPaths = 3
	parts := make([]string, 0, maxPaths)
	for i, it := range items {
		if i >= maxPaths {
			parts = append(parts, fmt.Sprintf("and %d more", len(items)-maxPaths))
			break
		}
		parts = append(parts, fmt.Sprintf("%s (%d)", it.path, it.hits))
	}
	return strings.Join(parts, ", ")
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

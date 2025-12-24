package security

import (
	"fmt"
	"sync"
	"time"
)

type Manager struct {
	db              *DB
	detector        *Detector
	deploymentsPath string
	mu              sync.RWMutex
}

func NewManager(deploymentsPath string) (*Manager, error) {
	db, err := NewDB(deploymentsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize security database: %w", err)
	}

	return &Manager{
		db:              db,
		detector:        NewDetector(),
		deploymentsPath: deploymentsPath,
	}, nil
}

// InitNginxConfigs ensures the nginx security config files exist.
// This should be called after manager initialization with the nginx config path.
func (m *Manager) InitNginxConfigs(nginxConfigPath string) error {
	generator := NewNginxConfigGenerator(m, nginxConfigPath)
	return generator.EnsureSecurityConfigFiles()
}

func (m *Manager) Close() error {
	return m.db.Close()
}

// SetDetectorThresholds updates the detector's behavior thresholds
func (m *Manager) SetDetectorThresholds(rateThreshold, notFoundThreshold, authFailureThreshold, uniquePathsThreshold, repeatedHitsThreshold int, windowDuration time.Duration) {
	m.detector.SetThresholds(rateThreshold, notFoundThreshold, authFailureThreshold, uniquePathsThreshold, repeatedHitsThreshold, windowDuration)
}

// IngestResult contains the result of event ingestion
type IngestResult struct {
	Event       *SecurityEvent
	AutoBlocked bool
	BlockedIP   string
	BlockTTL    int // TTL in seconds for the block
}

// IngestEvent processes an incoming event from nginx and stores it
func (m *Manager) IngestEvent(event *IngestEvent, autoBlockDuration time.Duration) (*IngestResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := &IngestResult{}

	// Check if IP is blocked - if so, don't process
	blocked, err := m.db.IsIPBlocked(event.SourceIP)
	if err != nil {
		return nil, err
	}
	if blocked {
		return result, nil
	}

	// Classify the event
	secEvent := m.detector.Classify(event)
	if secEvent == nil {
		return result, nil
	}

	// Store the event
	id, err := m.db.InsertEvent(secEvent)
	if err != nil {
		return nil, err
	}
	secEvent.ID = id
	result.Event = secEvent

	// Check if we should auto-block the IP
	if m.detector.ShouldAutoBlock(event.SourceIP, secEvent) {
		expiresAt := time.Now().Add(autoBlockDuration)
		_, err := m.db.BlockIP(event.SourceIP, "Auto-blocked due to suspicious activity", &expiresAt, true)
		if err == nil {
			result.AutoBlocked = true
			result.BlockedIP = event.SourceIP
			result.BlockTTL = int(autoBlockDuration.Seconds())
		}
	}

	return result, nil
}

// GetEvents retrieves events with optional filtering
func (m *Manager) GetEvents(filter *EventFilter) ([]SecurityEvent, int, error) {
	return m.db.GetEvents(filter)
}

// GetEventByID retrieves a single event
func (m *Manager) GetEventByID(id int64) (*SecurityEvent, error) {
	return m.db.GetEventByID(id)
}

// GetEventsByIP retrieves all events for a specific IP
func (m *Manager) GetEventsByIP(ip string) ([]SecurityEvent, error) {
	events, _, err := m.db.GetEvents(&EventFilter{SourceIP: ip, Limit: 1000})
	return events, err
}

// GetEventsByDeployment retrieves events for a specific deployment
func (m *Manager) GetEventsByDeployment(name string, limit int) ([]SecurityEvent, int, error) {
	return m.db.GetEvents(&EventFilter{DeploymentName: name, Limit: limit})
}

// GetStats retrieves security statistics
func (m *Manager) GetStats() (*SecurityStats, error) {
	return m.db.GetStats()
}

// GetBlockedIPs retrieves all blocked IPs
func (m *Manager) GetBlockedIPs() ([]BlockedIP, error) {
	return m.db.GetBlockedIPs()
}

// GetActiveBlockedIPs retrieves currently active blocked IPs
func (m *Manager) GetActiveBlockedIPs() ([]BlockedIP, error) {
	return m.db.GetActiveBlockedIPs()
}

// BlockIP blocks an IP address
func (m *Manager) BlockIP(ip, reason string, durationSeconds int) (int64, error) {
	var expiresAt *time.Time
	if durationSeconds > 0 {
		t := time.Now().Add(time.Duration(durationSeconds) * time.Second)
		expiresAt = &t
	}
	return m.db.BlockIP(ip, reason, expiresAt, false)
}

// UnblockIP unblocks an IP address
func (m *Manager) UnblockIP(ip string) error {
	return m.db.UnblockIP(ip)
}

// IsIPBlocked checks if an IP is blocked
func (m *Manager) IsIPBlocked(ip string) (bool, error) {
	return m.db.IsIPBlocked(ip)
}

// GetProtectedRoutes retrieves all protected routes
func (m *Manager) GetProtectedRoutes() ([]ProtectedRoute, error) {
	return m.db.GetProtectedRoutes()
}

// GetEnabledProtectedRoutes retrieves only enabled routes
func (m *Manager) GetEnabledProtectedRoutes() ([]ProtectedRoute, error) {
	return m.db.GetEnabledProtectedRoutes()
}

// AddProtectedRoute adds a new protected route
func (m *Manager) AddProtectedRoute(route *ProtectedRoute) (int64, error) {
	return m.db.AddProtectedRoute(route)
}

// UpdateProtectedRoute updates an existing protected route
func (m *Manager) UpdateProtectedRoute(route *ProtectedRoute) error {
	return m.db.UpdateProtectedRoute(route)
}

// DeleteProtectedRoute deletes a protected route
func (m *Manager) DeleteProtectedRoute(id int64) error {
	return m.db.DeleteProtectedRoute(id)
}

func (m *Manager) GetWhitelist() ([]WhitelistEntry, error) {
	return m.db.GetWhitelist()
}

func (m *Manager) AddWhitelistEntry(value, entryType, reason string) (int64, error) {
	return m.db.AddWhitelistEntry(value, entryType, reason, false)
}

func (m *Manager) RemoveWhitelistEntry(id int64) error {
	return m.db.RemoveWhitelistEntry(id)
}

func (m *Manager) IsWhitelisted(value string) (bool, error) {
	return m.db.IsWhitelisted(value)
}

func (m *Manager) AddDockerGatewayToWhitelist(gatewayIP string) error {
	if gatewayIP == "" {
		return nil
	}
	_, err := m.db.AddWhitelistEntry(gatewayIP, "ip", "Docker gateway", true)
	return err
}

// Cleanup removes old events and expired blocks
func (m *Manager) Cleanup(retentionDays int) (int64, int64, error) {
	eventsDeleted, err := m.db.CleanupOldEvents(time.Duration(retentionDays) * 24 * time.Hour)
	if err != nil {
		return 0, 0, err
	}

	blocksDeleted, err := m.db.CleanupExpiredBlocks()
	if err != nil {
		return eventsDeleted, 0, err
	}

	return eventsDeleted, blocksDeleted, nil
}

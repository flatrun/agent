package security

import "time"

type SecurityEvent struct {
	ID             int64     `json:"id"`
	EventType      string    `json:"event_type"`
	Severity       string    `json:"severity"`
	SourceIP       string    `json:"source_ip"`
	RequestPath    string    `json:"request_path,omitempty"`
	RequestMethod  string    `json:"request_method,omitempty"`
	StatusCode     int       `json:"status_code,omitempty"`
	UserAgent      string    `json:"user_agent,omitempty"`
	Message        string    `json:"message"`
	RawLog         string    `json:"raw_log,omitempty"`
	DeploymentName string    `json:"deployment_name,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type EventFilter struct {
	EventType      string
	Severity       string
	SourceIP       string
	DeploymentName string
	StartTime      time.Time
	EndTime        time.Time
	Limit          int
	Offset         int
}

type BlockedIP struct {
	ID          int64      `json:"id"`
	IP          string     `json:"ip"`
	Reason      string     `json:"reason,omitempty"`
	BlockedAt   time.Time  `json:"blocked_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	AutoBlocked bool       `json:"auto_blocked"`
}

type ProtectedRoute struct {
	ID            int64     `json:"id"`
	PathPattern   string    `json:"path_pattern"`
	RateLimit     int       `json:"rate_limit"`
	BlockDuration int       `json:"block_duration"`
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"created_at"`
}

type SecurityStats struct {
	TotalEvents          int               `json:"total_events"`
	Last24Hours          int               `json:"last_24_hours"`
	Last7Days            int               `json:"last_7_days"`
	BlockedIPsCount      int               `json:"blocked_ips_count"`
	ProtectedRoutesCount int               `json:"protected_routes_count"`
	BySeverity           map[string]int    `json:"by_severity"`
	ByType               map[string]int    `json:"by_type"`
	TopOffendingIPs      []IPStats         `json:"top_offending_ips"`
	TopDeployments       []DeploymentStats `json:"top_deployments"`
	RecentCritical       []SecurityEvent   `json:"recent_critical"`
	EventsTrend          []TrendPoint      `json:"events_trend"`
}

type IPStats struct {
	IP         string    `json:"ip"`
	EventCount int       `json:"event_count"`
	LastSeen   time.Time `json:"last_seen"`
}

type DeploymentStats struct {
	Name       string `json:"name"`
	EventCount int    `json:"event_count"`
	Critical   int    `json:"critical"`
	High       int    `json:"high"`
}

type TrendPoint struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

// Event types
const (
	EventTypeUnauthorizedAccess = "unauthorized_access"
	EventTypeForbiddenAccess    = "forbidden_access"
	EventTypeNotFoundProbe      = "not_found_probe"
	EventTypeServerError        = "server_error"
	EventTypeRateLimitExceeded  = "rate_limit_exceeded"
	EventTypeSuspiciousPath     = "suspicious_path"
	EventTypeHighRequestRate    = "high_request_rate"
	EventTypeScannerDetected    = "scanner_detected"
)

// Severity levels
const (
	SeverityLow      = "low"
	SeverityMedium   = "medium"
	SeverityHigh     = "high"
	SeverityCritical = "critical"
)

// IngestEvent is the payload sent from nginx Lua
type IngestEvent struct {
	SourceIP       string `json:"source_ip"`
	RequestPath    string `json:"request_path"`
	RequestMethod  string `json:"request_method"`
	StatusCode     int    `json:"status_code"`
	UserAgent      string `json:"user_agent"`
	DeploymentName string `json:"deployment_name,omitempty"`
	Timestamp      int64  `json:"timestamp"`
}

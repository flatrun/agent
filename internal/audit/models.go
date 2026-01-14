package audit

import "time"

type ActorType string

const (
	ActorTypeAPIKey    ActorType = "api_key"
	ActorTypeJWT       ActorType = "jwt"
	ActorTypeSystem    ActorType = "system"
	ActorTypeAnonymous ActorType = "anonymous"
)

type AuditEvent struct {
	ID             int64     `json:"id"`
	EventID        string    `json:"event_id"`
	Timestamp      time.Time `json:"timestamp"`
	ActorType      ActorType `json:"actor_type"`
	ActorID        string    `json:"actor_id,omitempty"`
	ActorName      string    `json:"actor_name,omitempty"`
	APIKeyPrefix   string    `json:"api_key_prefix,omitempty"`
	Action         string    `json:"action"`
	Method         string    `json:"method"`
	Path           string    `json:"path"`
	ResourceType   string    `json:"resource_type,omitempty"`
	ResourceID     string    `json:"resource_id,omitempty"`
	ClientIP       string    `json:"client_ip"`
	UserAgent      string    `json:"user_agent,omitempty"`
	RequestID      string    `json:"request_id,omitempty"`
	RequestBody    string    `json:"request_body,omitempty"`
	ResponseStatus int       `json:"response_status"`
	ResponseTimeMs int64     `json:"response_time_ms"`
	Success        bool      `json:"success"`
	ErrorMessage   string    `json:"error_message,omitempty"`
	Metadata       string    `json:"metadata,omitempty"`
}

type AuditFilter struct {
	ActorID      string
	ActorType    ActorType
	Action       string
	ResourceType string
	ResourceID   string
	Success      *bool
	ClientIP     string
	StartTime    time.Time
	EndTime      time.Time
	Limit        int
	Offset       int
}

type AuditStats struct {
	TotalEvents    int            `json:"total_events"`
	Last24Hours    int            `json:"last_24_hours"`
	Last7Days      int            `json:"last_7_days"`
	ByAction       map[string]int `json:"by_action"`
	ByActorType    map[string]int `json:"by_actor_type"`
	ByResourceType map[string]int `json:"by_resource_type"`
	FailureCount   int            `json:"failure_count"`
	TopActors      []ActorStats   `json:"top_actors"`
	EventsTrend    []TrendPoint   `json:"events_trend"`
}

type ActorStats struct {
	ActorID    string `json:"actor_id"`
	ActorType  string `json:"actor_type"`
	EventCount int    `json:"event_count"`
	LastSeen   string `json:"last_seen"`
}

type TrendPoint struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

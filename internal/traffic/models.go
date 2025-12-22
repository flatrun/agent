package traffic

import "time"

type TrafficLog struct {
	ID             int64     `json:"id"`
	DeploymentName string    `json:"deployment_name"`
	RequestPath    string    `json:"request_path"`
	RequestMethod  string    `json:"request_method"`
	StatusCode     int       `json:"status_code"`
	SourceIP       string    `json:"source_ip"`
	ResponseTimeMs int       `json:"response_time_ms"`
	BytesSent      int       `json:"bytes_sent"`
	RequestLength  int       `json:"request_length"`
	UpstreamTimeMs *int      `json:"upstream_time_ms,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type TrafficFilter struct {
	DeploymentName string
	RequestMethod  string
	StatusCode     *int
	StatusGroup    string // "2xx", "3xx", "4xx", "5xx"
	SourceIP       string
	RequestPath    string
	StartTime      time.Time
	EndTime        time.Time
	Limit          int
	Offset         int
}

type TrafficStats struct {
	TotalRequests     int64                    `json:"total_requests"`
	TotalBytes        int64                    `json:"total_bytes"`
	AvgResponseTimeMs float64                  `json:"avg_response_time_ms"`
	ByStatusGroup     map[string]int64         `json:"by_status_group"`
	ByDeployment      map[string]int64         `json:"by_deployment"`
	ByMethod          map[string]int64         `json:"by_method"`
	TopPaths          []PathStats              `json:"top_paths"`
	TopIPs            []IPTrafficStats         `json:"top_ips"`
	RequestsPerHour   []HourlyStats            `json:"requests_per_hour"`
	DeploymentStats   []DeploymentTrafficStats `json:"deployment_stats"`
}

type PathStats struct {
	Path         string  `json:"path"`
	RequestCount int64   `json:"request_count"`
	AvgTimeMs    float64 `json:"avg_time_ms"`
	ErrorCount   int64   `json:"error_count"`
}

type IPTrafficStats struct {
	IP           string    `json:"ip"`
	RequestCount int64     `json:"request_count"`
	BytesSent    int64     `json:"bytes_sent"`
	LastSeen     time.Time `json:"last_seen"`
}

type HourlyStats struct {
	Hour         string `json:"hour"`
	RequestCount int64  `json:"request_count"`
}

type DeploymentTrafficStats struct {
	Name            string  `json:"name"`
	TotalRequests   int64   `json:"total_requests"`
	AvgResponseTime float64 `json:"avg_response_time_ms"`
	ErrorRate       float64 `json:"error_rate"`
	Status2xx       int64   `json:"status_2xx"`
	Status3xx       int64   `json:"status_3xx"`
	Status4xx       int64   `json:"status_4xx"`
	Status5xx       int64   `json:"status_5xx"`
}

type IngestTrafficLog struct {
	DeploymentName string `json:"deployment_name"`
	RequestPath    string `json:"request_path"`
	RequestMethod  string `json:"request_method"`
	StatusCode     int    `json:"status_code"`
	SourceIP       string `json:"source_ip"`
	ResponseTimeMs int    `json:"response_time_ms"`
	BytesSent      int    `json:"bytes_sent"`
	RequestLength  int    `json:"request_length"`
	UpstreamTimeMs *int   `json:"upstream_time_ms,omitempty"`
	Timestamp      int64  `json:"timestamp"`
}

package audit

import (
	"encoding/json"
	"log"
	"strings"
	"time"
)

type Config struct {
	Enabled            bool
	RetentionDays      int
	CaptureRequestBody bool
	ExcludedPaths      []string
	SensitiveFields    []string
	CleanupInterval    time.Duration
}

type Manager struct {
	db     *DB
	config *Config
	stopCh chan struct{}
}

func NewManager(deploymentsPath string, config *Config) (*Manager, error) {
	db, err := NewDB(deploymentsPath)
	if err != nil {
		return nil, err
	}

	if config == nil {
		config = &Config{
			Enabled:            true,
			RetentionDays:      30,
			CaptureRequestBody: true,
			ExcludedPaths:      []string{"/api/health"},
			SensitiveFields:    []string{"password", "token", "secret", "api_key", "authorization"},
			CleanupInterval:    24 * time.Hour,
		}
	}

	m := &Manager{
		db:     db,
		config: config,
		stopCh: make(chan struct{}),
	}

	if config.Enabled && config.RetentionDays > 0 {
		go m.cleanupLoop()
	}

	return m, nil
}

func (m *Manager) Close() error {
	close(m.stopCh)
	return m.db.Close()
}

func (m *Manager) IsEnabled() bool {
	return m.config.Enabled
}

func (m *Manager) ShouldCapturePath(path string) bool {
	for _, excluded := range m.config.ExcludedPaths {
		if strings.HasPrefix(path, excluded) {
			return false
		}
	}
	return true
}

func (m *Manager) ShouldCaptureBody() bool {
	return m.config.CaptureRequestBody
}

func (m *Manager) LogEvent(event *AuditEvent) error {
	if !m.config.Enabled {
		return nil
	}

	if event.RequestBody != "" && len(m.config.SensitiveFields) > 0 {
		event.RequestBody = m.sanitizeBody(event.RequestBody)
	}

	_, err := m.db.InsertEvent(event)
	return err
}

func (m *Manager) GetEvents(filter *AuditFilter) ([]AuditEvent, int, error) {
	return m.db.GetEvents(filter)
}

func (m *Manager) GetEventByID(id int64) (*AuditEvent, error) {
	return m.db.GetEventByID(id)
}

func (m *Manager) GetEventByEventID(eventID string) (*AuditEvent, error) {
	return m.db.GetEventByEventID(eventID)
}

func (m *Manager) GetStats() (*AuditStats, error) {
	return m.db.GetStats()
}

func (m *Manager) Cleanup() (int64, error) {
	retention := time.Duration(m.config.RetentionDays) * 24 * time.Hour
	return m.db.CleanupOldEvents(retention)
}

func (m *Manager) cleanupLoop() {
	ticker := time.NewTicker(m.config.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			deleted, err := m.Cleanup()
			if err != nil {
				log.Printf("Audit cleanup error: %v", err)
			} else if deleted > 0 {
				log.Printf("Audit cleanup: deleted %d old events", deleted)
			}
		case <-m.stopCh:
			return
		}
	}
}

func (m *Manager) sanitizeBody(body string) string {
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return body
	}

	m.redactSensitiveFields(data)

	sanitized, err := json.Marshal(data)
	if err != nil {
		return body
	}
	return string(sanitized)
}

func (m *Manager) redactSensitiveFields(data map[string]interface{}) {
	for key, value := range data {
		keyLower := strings.ToLower(key)
		for _, sensitive := range m.config.SensitiveFields {
			if strings.Contains(keyLower, sensitive) {
				data[key] = "[REDACTED]"
				break
			}
		}

		if nested, ok := value.(map[string]interface{}); ok {
			m.redactSensitiveFields(nested)
		}
	}
}

func (m *Manager) ExportEvents(filter *AuditFilter, format string) ([]byte, error) {
	events, _, err := m.db.GetEvents(filter)
	if err != nil {
		return nil, err
	}

	switch format {
	case "csv":
		return m.exportCSV(events)
	default:
		return json.MarshalIndent(events, "", "  ")
	}
}

func (m *Manager) exportCSV(events []AuditEvent) ([]byte, error) {
	var sb strings.Builder
	sb.WriteString("id,event_id,timestamp,actor_type,actor_id,action,method,path,resource_type,resource_id,client_ip,response_status,response_time_ms,success,error_message\n")

	for _, e := range events {
		sb.WriteString(formatCSVRow(
			e.ID, e.EventID, e.Timestamp.Format(time.RFC3339),
			string(e.ActorType), e.ActorID, e.Action, e.Method, e.Path,
			e.ResourceType, e.ResourceID, e.ClientIP,
			e.ResponseStatus, e.ResponseTimeMs, e.Success, e.ErrorMessage,
		))
		sb.WriteString("\n")
	}

	return []byte(sb.String()), nil
}

func formatCSVRow(values ...interface{}) string {
	var parts []string
	for _, v := range values {
		str := ""
		switch val := v.(type) {
		case string:
			str = escapeCSV(val)
		case int:
			str = formatInt(val)
		case int64:
			str = formatInt64(val)
		case bool:
			if val {
				str = "true"
			} else {
				str = "false"
			}
		default:
			str = ""
		}
		parts = append(parts, str)
	}
	return strings.Join(parts, ",")
}

func escapeCSV(s string) string {
	if strings.ContainsAny(s, ",\"\n") {
		return "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\""
	}
	return s
}

func formatInt(i int) string {
	return strings.TrimPrefix(strings.TrimPrefix(formatInt64(int64(i)), "-"), "+")
}

func formatInt64(i int64) string {
	if i == 0 {
		return "0"
	}
	negative := i < 0
	if negative {
		i = -i
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

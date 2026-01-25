package audit

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"log"
	"strconv"
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
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)

	header := []string{"id", "event_id", "timestamp", "actor_type", "actor_id", "action", "method", "path", "resource_type", "resource_id", "client_ip", "response_status", "response_time_ms", "success", "error_message"}
	if err := w.Write(header); err != nil {
		return nil, err
	}

	for _, e := range events {
		row := []string{
			strconv.FormatInt(e.ID, 10),
			e.EventID,
			e.Timestamp.Format(time.RFC3339),
			string(e.ActorType),
			e.ActorID,
			e.Action,
			e.Method,
			e.Path,
			e.ResourceType,
			e.ResourceID,
			e.ClientIP,
			strconv.Itoa(e.ResponseStatus),
			strconv.FormatInt(e.ResponseTimeMs, 10),
			strconv.FormatBool(e.Success),
			e.ErrorMessage,
		}
		if err := w.Write(row); err != nil {
			return nil, err
		}
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

package api

import (
	"encoding/json"
	"regexp"
	"strings"
)

type logRecord struct {
	Timestamp string            `json:"timestamp,omitempty"`
	Service   string            `json:"service,omitempty"`
	Level     string            `json:"level,omitempty"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields,omitempty"`
	Raw       string            `json:"raw"`
}

var composePrefix = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)\s*\|\s?`)

var leadingTimestamp = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2}))\s+`)

const levelWords = `TRACE|DEBUG|INFO(?:RMATION)?|NOTICE|WARN(?:ING)?|ERROR|ERR|FATAL|CRIT(?:ICAL)?|PANIC|EMERG(?:ENCY)?|ALERT`

var levelPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^\s*\[?\s*(` + levelWords + `)\s*\]?\s*[:\-\s]`),
	regexp.MustCompile(`(?i)\b[a-z][a-z0-9_-]*\.(` + levelWords + `)\b`),
	regexp.MustCompile(`(?i)\blevel\s*[=:]\s*"?(` + levelWords + `)\b`),
}

func detectTextLevel(s string) string {
	for _, re := range levelPatterns {
		if m := re.FindStringSubmatch(s); m != nil {
			return canonicalLevel(m[1])
		}
	}
	return ""
}

func parseLogRecord(raw string) logRecord {
	rec := logRecord{Raw: raw}
	rest := raw

	if m := composePrefix.FindStringSubmatch(rest); m != nil {
		rec.Service = strings.TrimSpace(m[1])
		rest = rest[len(m[0]):]
	}

	if m := leadingTimestamp.FindStringSubmatch(rest); m != nil {
		rec.Timestamp = m[1]
		rest = rest[len(m[0]):]
	}

	rest = strings.TrimRight(rest, "\r")

	if lvl, fields, msg, ok := parseJSONLine(rest); ok {
		rec.Level = lvl
		rec.Fields = fields
		rec.Message = msg
		return rec
	}

	rec.Message = rest
	rec.Level = detectTextLevel(rest)
	return rec
}

func parseJSONLine(s string) (level string, fields map[string]string, message string, ok bool) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return "", nil, "", false
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return "", nil, "", false
	}

	fields = map[string]string{}
	for k, v := range obj {
		key := strings.ToLower(k)
		val := rawToString(v)
		switch key {
		case "level", "severity", "lvl", "loglevel", "log.level":
			if level == "" {
				level = canonicalLevel(val)
			}
		case "message", "msg", "log", "text":
			if message == "" {
				message = val
			}
		default:
			fields[k] = val
		}
	}

	if message == "" {
		message = s
	}
	if len(fields) == 0 {
		fields = nil
	}
	return level, fields, message, true
}

func rawToString(v json.RawMessage) string {
	var str string
	if err := json.Unmarshal(v, &str); err == nil {
		return str
	}
	return strings.TrimSpace(string(v))
}

func canonicalLevel(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace":
		return "trace"
	case "debug":
		return "debug"
	case "info", "information", "informational", "notice":
		return "info"
	case "warn", "warning":
		return "warn"
	case "err", "error":
		return "error"
	case "fatal", "crit", "critical", "panic", "emerg", "emergency", "alert":
		return "fatal"
	default:
		return strings.ToLower(strings.TrimSpace(s))
	}
}

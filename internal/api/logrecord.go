package api

import (
	"encoding/json"
	"regexp"
	"strings"
)

// logRecord is one log line broken into the parts a viewer can render as a
// structured row: which service wrote it, when, at what level, and the message
// itself, plus any structured fields when the line was JSON.
//
// Raw is always the untouched compose line so a viewer can fall back to showing
// exactly what the container emitted.
type logRecord struct {
	Timestamp string            `json:"timestamp,omitempty"`
	Service   string            `json:"service,omitempty"`
	Level     string            `json:"level,omitempty"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields,omitempty"`
	Raw       string            `json:"raw"`
}

// composePrefix matches the `service-1  | ` marker compose puts in front of
// every line so several containers can share one stream.
var composePrefix = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)\s*\|\s?`)

// leadingTimestamp matches the RFC3339 stamp `docker compose logs --timestamps`
// prints at the start of each message, with or without a zone offset.
var leadingTimestamp = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2}))\s+`)

const levelWords = `TRACE|DEBUG|INFO(?:RMATION)?|NOTICE|WARN(?:ING)?|ERROR|ERR|FATAL|CRIT(?:ICAL)?|PANIC|EMERG(?:ENCY)?|ALERT`

// levelPatterns detect a severity only where a log format actually puts one:
// at the start of the line, as a `channel.LEVEL` tag (monolog/syslog), or a
// `level=` field (logfmt). Matching the bare word anywhere would wrongly tag a
// stack frame like `#5 App\ErrorHandler->handle()` as an error.
var levelPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^\s*\[?\s*(` + levelWords + `)\s*\]?\s*[:\-\s]`),
	regexp.MustCompile(`(?i)\b[a-z][a-z0-9_-]*\.(` + levelWords + `)\b`),
	regexp.MustCompile(`(?i)\blevel\s*[=:]\s*"?(` + levelWords + `)\b`),
}

// detectTextLevel returns the canonical severity of a plain-text line, or "" if
// none of the recognised header positions carry one.
func detectTextLevel(s string) string {
	for _, re := range levelPatterns {
		if m := re.FindStringSubmatch(s); m != nil {
			return canonicalLevel(m[1])
		}
	}
	return ""
}

// parseLogRecord turns one raw compose log line into a structured record. It is
// deliberately tolerant: anything it cannot recognise stays in Message, and Raw
// always holds the original line.
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

// parseJSONLine recognises a structured log line (JSON object) and pulls out the
// level and message, leaving the remaining keys as string fields.
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
		// A JSON line with no obvious message still reads better as its own text
		// than as an empty row, so keep the object as the message.
		message = s
	}
	if len(fields) == 0 {
		fields = nil
	}
	return level, fields, message, true
}

// rawToString renders a JSON value as a plain string: bare strings lose their
// quotes, everything else keeps its JSON form.
func rawToString(v json.RawMessage) string {
	var str string
	if err := json.Unmarshal(v, &str); err == nil {
		return str
	}
	return strings.TrimSpace(string(v))
}

// canonicalLevel folds the many spellings of a severity onto one lowercase word
// so a viewer can colour and filter on a small fixed set.
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

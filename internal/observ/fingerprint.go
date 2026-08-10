package observ

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

var (
	fpHex  = regexp.MustCompile(`\b(?:0x)?[0-9a-fA-F]{8,}\b`)
	fpUUID = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	// Case-insensitive: normalization lowercases first, turning the ISO "T" into a "t".
	fpTimestamp = regexp.MustCompile(`(?i)\b\d{4}-\d{2}-\d{2}[t ]\d{2}:\d{2}:\d{2}(?:\.\d+)?z?\b`)
	fpDuration  = regexp.MustCompile(`\b\d+(?:\.\d+)?(?:ms|s|m|h|us|µs|ns)\b`)
	fpIPPort    = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}(?::\d+)?\b`)
	fpQuoted    = regexp.MustCompile(`"[^"]*"|'[^']*'`)
	fpPath      = regexp.MustCompile(`(?:/[\w.\-@+]+){2,}`)
	fpNumber    = regexp.MustCompile(`\b\d+\b`)
	fpSpace     = regexp.MustCompile(`\s+`)
)

// fingerprint reduces a message to what stays the same across occurrences of one fault.
//
// It errs toward collapsing: two faults sharing a fingerprint costs one missed notification,
// while one fault spread across many costs a notification and a triage per line written.
func fingerprint(message string) string {
	normalized := normalizeMessage(message)
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:8])
}

func normalizeMessage(message string) string {
	s := strings.ToLower(message)
	// Specific shapes first, or the hex rule eats UUIDs and the number rule eats timestamps.
	s = fpUUID.ReplaceAllString(s, "<id>")
	s = fpTimestamp.ReplaceAllString(s, "<time>")
	s = fpIPPort.ReplaceAllString(s, "<addr>")
	s = fpDuration.ReplaceAllString(s, "<dur>")
	s = fpHex.ReplaceAllString(s, "<hex>")
	s = fpQuoted.ReplaceAllString(s, "<str>")
	s = fpPath.ReplaceAllString(s, "<path>")
	s = fpNumber.ReplaceAllString(s, "<n>")
	s = fpSpace.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)

	// In a stack trace or a dump, the opening identifies the fault and the tail varies.
	const fingerprintPrefix = 300
	if len(s) > fingerprintPrefix {
		s = s[:fingerprintPrefix]
	}
	return s
}

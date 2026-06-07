package ai

import (
	"regexp"
	"sort"
	"strings"
)

const redactedMarker = "[REDACTED]"

// minSecretLength keeps short values like "true", port numbers or
// single words from poisoning the whole text with replacements.
const minSecretLength = 6

var credentialPattern = regexp.MustCompile(`(?i)([a-z0-9_\-]*(?:password|passwd|secret|token|api_?key))(\s*[=:]\s*)("[^"\n]*"|'[^'\n]*'|[^\s,;]+)`)

// Redactor removes known secret values and credential-shaped
// assignments from text before it leaves the host.
type Redactor struct {
	secrets []string
}

func NewRedactor(secrets []string) *Redactor {
	seen := map[string]struct{}{}
	var filtered []string
	for _, s := range secrets {
		s = strings.TrimSpace(s)
		if len(s) < minSecretLength {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		filtered = append(filtered, s)
	}
	// Longest first, so a secret that contains another secret is
	// replaced whole instead of being broken by the shorter match.
	sort.Slice(filtered, func(i, j int) bool { return len(filtered[i]) > len(filtered[j]) })
	return &Redactor{secrets: filtered}
}

func (r *Redactor) Redact(text string) (string, int) {
	count := 0
	for _, s := range r.secrets {
		if n := strings.Count(text, s); n > 0 {
			text = strings.ReplaceAll(text, s, redactedMarker)
			count += n
		}
	}
	text = credentialPattern.ReplaceAllStringFunc(text, func(m string) string {
		sub := credentialPattern.FindStringSubmatch(m)
		if sub[3] == redactedMarker {
			return m
		}
		count++
		return sub[1] + sub[2] + redactedMarker
	})
	return text, count
}

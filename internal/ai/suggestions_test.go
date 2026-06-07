package ai

import (
	"strings"
	"testing"
)

func TestParseSuggestions(t *testing.T) {
	content := "## Diagnosis\nDB is down.\n\n```suggestions\n[\n" +
		`{"kind":"service_action","service":"db","action":"restart","title":"Restart the database","reason":"connection refused"},` + "\n" +
		`{"kind":"exec","service":"web","command":"php artisan config:clear","title":"Clear config cache"},` + "\n" +
		`{"kind":"service_action","service":"db","action":"explode","title":"Invalid action"},` + "\n" +
		`{"kind":"exec","service":"web","command":"","title":"Empty command"},` + "\n" +
		`{"kind":"weird","title":"Unknown kind"}` + "\n]\n```\n"

	cleaned, suggestions := ParseSuggestions(content)

	if strings.Contains(cleaned, "suggestions") || strings.Contains(cleaned, "artisan") {
		t.Errorf("block not stripped from analysis: %q", cleaned)
	}
	if !strings.Contains(cleaned, "DB is down.") {
		t.Errorf("analysis text lost: %q", cleaned)
	}
	if len(suggestions) != 2 {
		t.Fatalf("got %d suggestions, want 2 valid: %+v", len(suggestions), suggestions)
	}
	if suggestions[0].Kind != SuggestionKindServiceAction || suggestions[0].Action != "restart" {
		t.Errorf("first = %+v", suggestions[0])
	}
	if suggestions[1].Kind != SuggestionKindExec || suggestions[1].Command != "php artisan config:clear" {
		t.Errorf("second = %+v", suggestions[1])
	}
}

func TestParseSuggestionsNoBlock(t *testing.T) {
	cleaned, suggestions := ParseSuggestions("## Diagnosis\nAll good.")
	if cleaned != "## Diagnosis\nAll good." || suggestions != nil {
		t.Errorf("content without block should pass through, got %q %v", cleaned, suggestions)
	}
}

func TestParseSuggestionsMalformedJSON(t *testing.T) {
	cleaned, suggestions := ParseSuggestions("Text\n```suggestions\nnot json\n```")
	if suggestions != nil {
		t.Errorf("malformed block should yield no suggestions, got %v", suggestions)
	}
	if !strings.Contains(cleaned, "Text") {
		t.Errorf("analysis lost: %q", cleaned)
	}
}

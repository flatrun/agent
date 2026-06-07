package ai

import (
	"encoding/json"
	"regexp"
	"strings"
)

// SuggestedAction is a machine-actionable proposal extracted from a
// model response. It is never executed by the agent on its own; the
// client decides to run it through the normal, guarded APIs.
type SuggestedAction struct {
	Kind    string `json:"kind"`
	Service string `json:"service,omitempty"`
	Action  string `json:"action,omitempty"`
	Command string `json:"command,omitempty"`
	Title   string `json:"title"`
	Reason  string `json:"reason,omitempty"`
}

const (
	SuggestionKindExec          = "exec"
	SuggestionKindServiceAction = "service_action"
)

var validServiceActions = map[string]bool{
	"start": true, "stop": true, "restart": true, "rebuild": true, "pull": true,
}

var suggestionsBlock = regexp.MustCompile("(?s)```suggestions\\s*(.*?)```")

// ParseSuggestions extracts the fenced suggestions block from a model
// response, returning the response without the block and the valid
// actions found in it. Malformed blocks and invalid entries are
// dropped; diagnosis text is never lost over a bad suggestion.
func ParseSuggestions(content string) (string, []SuggestedAction) {
	match := suggestionsBlock.FindStringSubmatch(content)
	if match == nil {
		return content, nil
	}
	cleaned := strings.TrimSpace(suggestionsBlock.ReplaceAllString(content, ""))

	var raw []SuggestedAction
	if err := json.Unmarshal([]byte(strings.TrimSpace(match[1])), &raw); err != nil {
		return cleaned, nil
	}

	var valid []SuggestedAction
	for _, s := range raw {
		if s.Title == "" {
			continue
		}
		switch s.Kind {
		case SuggestionKindExec:
			if strings.TrimSpace(s.Command) == "" || s.Service == "" {
				continue
			}
		case SuggestionKindServiceAction:
			if s.Service == "" || !validServiceActions[s.Action] {
				continue
			}
		default:
			continue
		}
		valid = append(valid, s)
	}
	return cleaned, valid
}

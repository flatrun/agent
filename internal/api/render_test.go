package api

import (
	"encoding/json"
	"testing"
)

func TestListAnswersBothNames(t *testing.T) {
	raw, err := json.Marshal(NewList([]string{"a", "b"}, "deployments"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["total"] != float64(2) {
		t.Errorf("total = %v", decoded["total"])
	}
	if items, ok := decoded["items"].([]any); !ok || len(items) != 2 {
		t.Errorf("items = %v", decoded["items"])
	}
	// Older clients read the resource name, and keep working until they are moved off it.
	if legacy, ok := decoded["deployments"].([]any); !ok || len(legacy) != 2 {
		t.Errorf("deployments = %v", decoded["deployments"])
	}
}

func TestListWithoutALegacyNameAnswersOnlyTheShape(t *testing.T) {
	raw, err := json.Marshal(NewList([]int{1}, ""))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 2 {
		t.Errorf("expected items and total only, got %v", decoded)
	}
}

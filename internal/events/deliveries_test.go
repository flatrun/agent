package events

import "testing"

func TestDeliveryProcessingRecoveredOnOpen(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	event := Event{ID: "interrupted", Source: "generic/ci"}
	if _, duplicate, err := store.BeginDelivery(event, 1); err != nil || duplicate {
		t.Fatalf("begin: %v %v", duplicate, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	records, err := store.ListDeliveries()
	if err != nil || len(records) != 1 || records[0].Outcome != "failed" {
		t.Fatalf("recovered: %+v %v", records, err)
	}
	var outcome string
	if err := store.db.QueryRow("SELECT outcome FROM deliveries").Scan(&outcome); err != nil || outcome != "failed" {
		t.Fatalf("column: %s %v", outcome, err)
	}
	if _, duplicate, err := store.BeginDelivery(event, 1); err != nil || duplicate {
		t.Fatalf("retry: %v %v", duplicate, err)
	}
	records, err = store.ListDeliveries()
	if err != nil || len(records) != 1 || records[0].ID != event.ID {
		t.Fatalf("listing: %+v %v", records, err)
	}
}

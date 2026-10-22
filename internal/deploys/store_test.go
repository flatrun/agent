package deploys

import (
	"errors"
	"testing"
)

func TestStore_StartFinishList(t *testing.T) {
	s := NewStore(t.TempDir())

	first, err := s.Start("app", Record{Branch: "main", Trigger: TriggerPush})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Finish("app", first.ID, "aaa111", nil); err != nil {
		t.Fatal(err)
	}
	second, _ := s.Start("app", Record{Revision: "aaa111", Trigger: TriggerRollback})
	if err := s.Finish("app", second.ID, "", errors.New("boom")); err != nil {
		t.Fatal(err)
	}

	records, err := s.List("app")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].ID != second.ID {
		t.Fatalf("records = %+v, want newest first", records)
	}
	if records[1].Status != StatusSucceeded || records[1].Revision != "aaa111" {
		t.Errorf("first record = %+v", records[1])
	}
	if records[0].Status != StatusFailed || records[0].Error != "boom" || records[0].Revision != "aaa111" {
		t.Errorf("second record = %+v", records[0])
	}
}

func TestStore_CapsHistory(t *testing.T) {
	s := NewStore(t.TempDir())
	for i := 0; i < maxRecords+5; i++ {
		if _, err := s.Start("app", Record{}); err != nil {
			t.Fatal(err)
		}
	}
	records, _ := s.List("app")
	if len(records) != maxRecords {
		t.Errorf("kept %d records, want %d", len(records), maxRecords)
	}
}

func TestStore_UnknownRecord(t *testing.T) {
	s := NewStore(t.TempDir())
	if _, err := s.Get("app", "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get unknown = %v, want ErrNotFound", err)
	}
}

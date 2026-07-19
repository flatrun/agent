package ai

import (
	"testing"
	"time"
)

func TestSessionTitle(t *testing.T) {
	// First visible user turn becomes the title.
	s := NewSession(SessionScopeSystem, "", true, SessionActor{ID: "u1"}, "sys")
	s.AddUserMessage("api.example.com:443 request with logs...", "why is wordpress unhealthy?", true) // hidden, skipped
	s.AddUserMessage("summarize recent security events", "", false)
	if got := s.Title(); got != "summarize recent security events" {
		t.Errorf("title should come from the first visible user turn, got %q", got)
	}

	// No user turn yet: fall back to the deployment name.
	empty := NewSession(SessionScopeDeployment, "shop", true, SessionActor{ID: "u1"}, "sys")
	if got := empty.Title(); got != "shop chat" {
		t.Errorf("deployment fallback title, got %q", got)
	}

	// System scope with no user turn.
	sys := NewSession(SessionScopeSystem, "", true, SessionActor{ID: "u1"}, "sys")
	if got := sys.Title(); got != "System chat" {
		t.Errorf("system fallback title, got %q", got)
	}
}

func TestSessionStoreList(t *testing.T) {
	st := NewSessionStore(t.TempDir())

	older := NewSession(SessionScopeSystem, "", true, SessionActor{ID: "u1", Name: "alice"}, "sys")
	older.AddUserMessage("first question", "", false)
	if err := st.Save(older); err != nil {
		t.Fatal(err)
	}

	newer := NewSession(SessionScopeDeployment, "shop", true, SessionActor{ID: "u2", Name: "bob"}, "sys")
	newer.UpdatedAt = time.Now().UTC().Add(time.Minute)
	if err := st.Save(newer); err != nil {
		t.Fatal(err)
	}

	list, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 summaries, got %d", len(list))
	}
	if list[0].ID != newer.ID {
		t.Errorf("expected the most recently updated session first")
	}
	if list[0].Title != "shop chat" {
		t.Errorf("unexpected title %q", list[0].Title)
	}
	if list[1].Title != "first question" {
		t.Errorf("unexpected title %q", list[1].Title)
	}
	// The owner must ride along so the handler can filter by actor.
	if list[1].CreatedBy.ID != "u1" {
		t.Errorf("summary should carry the creating actor, got %q", list[1].CreatedBy.ID)
	}
}

func TestSessionStoreListEmpty(t *testing.T) {
	st := NewSessionStore(t.TempDir())
	list, err := st.List()
	if err != nil {
		t.Fatalf("empty store should not error: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected no sessions, got %d", len(list))
	}
}

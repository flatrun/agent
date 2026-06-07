package ai

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	SessionStatusReady            = "ready"
	SessionStatusAwaitingApproval = "awaiting_approval"
	SessionScopeSystem            = "system"
	SessionScopeDeployment        = "deployment"
	maxSessionToolSteps           = 8
	maxSessionMessages            = 200
)

type SessionActor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Session is one ongoing AI conversation. It owns the full model
// transcript (including tool calls and results) plus the derived state
// the UI needs. Stored as a flat JSON file, true to FlatRun.
type Session struct {
	ID         string            `json:"id"`
	Scope      string            `json:"scope"`
	Deployment string            `json:"deployment,omitempty"`
	AutoRun    bool              `json:"auto_run"`
	Status     string            `json:"status"`
	Model      string            `json:"model,omitempty"`
	CreatedBy  SessionActor      `json:"created_by"`
	Messages   []Message         `json:"messages"`
	Pending    []ToolCall        `json:"pending,omitempty"`
	Suggested  []SuggestedAction `json:"suggested_actions"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

func NewSession(scope, deployment string, autoRun bool, actor SessionActor, systemPrompt string) *Session {
	now := time.Now().UTC()
	return &Session{
		ID:         "ais_" + uuid.New().String(),
		Scope:      scope,
		Deployment: deployment,
		AutoRun:    autoRun,
		Status:     SessionStatusReady,
		CreatedBy:  actor,
		Messages:   []Message{{Role: "system", Content: systemPrompt}},
		Suggested:  []SuggestedAction{},
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

func (s *Session) touch() {
	s.UpdatedAt = time.Now().UTC()
	if len(s.Messages) > maxSessionMessages {
		// Keep the system prompt and the most recent window.
		head := s.Messages[:1]
		tail := s.Messages[len(s.Messages)-(maxSessionMessages-1):]
		s.Messages = append(head, tail...)
	}
}

// AddUserMessage records a user turn. When display differs from
// content, the model sees content (e.g. message plus embedded logs)
// while the UI shows display (e.g. a short label).
func (s *Session) AddUserMessage(content, display string) {
	s.Messages = append(s.Messages, Message{Role: "user", Content: content, Display: display})
	s.touch()
}

func (s *Session) AddAssistantMessage(content string, toolCalls []ToolCall) {
	s.Messages = append(s.Messages, Message{Role: "assistant", Content: content, ToolCalls: toolCalls})
	s.touch()
}

func (s *Session) AddToolResult(call ToolCall, result string) {
	s.Messages = append(s.Messages, Message{Role: "tool", ToolCallID: call.ID, Name: call.Name, Content: result})
	s.touch()
}

// MaxToolSteps is the per-turn cap on consecutive tool rounds, so a
// misbehaving model cannot loop forever.
func (s *Session) MaxToolSteps() int { return maxSessionToolSteps }

var sessionIDPattern = regexp.MustCompile(`^ais_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var ErrSessionNotFound = fmt.Errorf("session not found")

type SessionStore struct {
	dir string
	mu  sync.Mutex
}

func NewSessionStore(deploymentsPath string) *SessionStore {
	return &SessionStore{dir: filepath.Join(deploymentsPath, ".flatrun", "ai-sessions")}
}

func (st *SessionStore) path(id string) string {
	return filepath.Join(st.dir, id+".json")
}

func (st *SessionStore) Save(sess *Session) error {
	if !sessionIDPattern.MatchString(sess.ID) {
		return fmt.Errorf("invalid session id %q", sess.ID)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := os.MkdirAll(st.dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	tmp := st.path(sess.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, st.path(sess.ID))
}

func (st *SessionStore) Get(id string) (*Session, error) {
	if !sessionIDPattern.MatchString(id) {
		return nil, ErrSessionNotFound
	}
	data, err := os.ReadFile(st.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("corrupt session file: %w", err)
	}
	return &sess, nil
}

func (st *SessionStore) Delete(id string) error {
	if !sessionIDPattern.MatchString(id) {
		return ErrSessionNotFound
	}
	return os.Remove(st.path(id))
}

// PruneOlderThan removes sessions whose last update predates the
// cutoff, keeping the flat-file directory from growing without bound.
func (st *SessionStore) PruneOlderThan(cutoff time.Time) int {
	entries, err := os.ReadDir(st.dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		sess, err := st.Get(id)
		if err != nil {
			continue
		}
		if sess.UpdatedAt.Before(cutoff) {
			if st.Delete(id) == nil {
				removed++
			}
		}
	}
	return removed
}

// DisplayMessages projects the transcript into UI-facing turns,
// dropping the system prompt and pairing tool calls with their results.
type DisplayToolStep struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Result    string `json:"result,omitempty"`
}

type DisplayTurn struct {
	Role      string            `json:"role"`
	Content   string            `json:"content,omitempty"`
	ToolSteps []DisplayToolStep `json:"tool_steps,omitempty"`
}

func (s *Session) DisplayMessages() []DisplayTurn {
	results := map[string]string{}
	for _, m := range s.Messages {
		if m.Role == "tool" {
			results[m.ToolCallID] = m.Content
		}
	}
	turns := make([]DisplayTurn, 0, len(s.Messages))
	for _, m := range s.Messages {
		switch m.Role {
		case "user":
			shown := m.Content
			if m.Display != "" {
				shown = m.Display
			}
			turns = append(turns, DisplayTurn{Role: "user", Content: shown})
		case "assistant":
			turn := DisplayTurn{Role: "assistant", Content: m.Content}
			for _, tc := range m.ToolCalls {
				turn.ToolSteps = append(turn.ToolSteps, DisplayToolStep{
					Name:      tc.Name,
					Arguments: tc.Arguments,
					Result:    results[tc.ID],
				})
			}
			turns = append(turns, turn)
		}
	}
	return turns
}

package ai

import (
	"context"
	"errors"

	"github.com/flatrun/agent/pkg/config"
)

// ErrDisabled is returned by New when no provider is configured. The
// caller treats it as "feature off", not as a failure.
var ErrDisabled = errors.New("ai is not enabled")

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// Display, when set, is what the UI shows for this turn instead of
	// Content. Used to send bulky context (logs, output) to the model
	// while showing the operator a short label. Never sent to the
	// provider.
	Display string `json:"display,omitempty"`
	// Hidden marks a turn the UI must not show at all: prompts composed
	// by the product (e.g. "analyze these logs") rather than typed by
	// the operator. The model still sees Content.
	Hidden bool `json:"hidden,omitempty"`
	// ToolCalls is set on an assistant message that wants tools run.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID and Name identify a role:"tool" result message.
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
}

// ToolCall is one tool invocation requested by the model. Arguments is
// a JSON object string as produced by the model.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool is a function the model may call. Parameters is a JSON Schema
// object describing the arguments.
type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

type Request struct {
	Messages    []Message
	Tools       []Tool
	MaxTokens   int
	Temperature float64
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type Response struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Model     string     `json:"model"`
	Usage     Usage      `json:"usage"`
}

// Provider is the model-agnostic boundary: everything above this
// interface (handlers, prompts, redaction) is provider-neutral, and new
// backends plug in behind it without touching callers.
type Provider interface {
	Name() string
	Complete(ctx context.Context, req Request) (*Response, error)
}

func New(cfg *config.AIConfig) (Provider, error) {
	if cfg == nil || !cfg.Enabled {
		return nil, ErrDisabled
	}
	return newOpenAICompatible(cfg), nil
}

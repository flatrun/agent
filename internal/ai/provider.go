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
}

type Request struct {
	Messages    []Message
	MaxTokens   int
	Temperature float64
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type Response struct {
	Content string `json:"content"`
	Model   string `json:"model"`
	Usage   Usage  `json:"usage"`
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

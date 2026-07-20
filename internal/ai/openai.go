package ai

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/flatrun/agent/pkg/config"
	"github.com/whilesmartgo/agents"
	"github.com/whilesmartgo/agents/engine/openai"
)

// openAICompatible adapts the whilesmartgo/agents OpenAI-compatible engine to
// FlatRun's Provider. The chat-completions wire format lives in the library;
// this maps FlatRun's request and response types across that boundary.
type openAICompatible struct {
	engine *openai.Engine
}

func newOpenAICompatible(cfg *config.AIConfig) *openAICompatible {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return &openAICompatible{
		engine: openai.New(
			strings.TrimRight(cfg.BaseURL, "/"),
			cfg.Model,
			openai.WithAPIKey(cfg.APIKey),
			openai.WithHTTPClient(&http.Client{Timeout: timeout}),
		),
	}
}

func (p *openAICompatible) Name() string {
	return "openai-compatible"
}

func (p *openAICompatible) Complete(ctx context.Context, req Request) (*Response, error) {
	resp, err := p.engine.Complete(ctx, agents.Request{
		Messages:    MessagesToAgents(req.Messages),
		Tools:       toEngineTools(req.Tools),
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
	})
	if err != nil {
		return nil, err
	}
	return &Response{
		Content:   resp.Content,
		ToolCalls: fromEngineToolCalls(resp.ToolCalls),
		Model:     resp.Model,
		Usage:     Usage{PromptTokens: resp.Usage.PromptTokens, CompletionTokens: resp.Usage.CompletionTokens},
	}, nil
}

// MessagesToAgents converts the stored transcript to the library's message type.
// Display and Hidden are UI-only and dropped here: the model sees Content.
func MessagesToAgents(messages []Message) []agents.Message {
	out := make([]agents.Message, 0, len(messages))
	for _, m := range messages {
		out = append(out, agents.Message{
			Role:       m.Role,
			Content:    m.Content,
			ToolCalls:  toEngineToolCalls(m.ToolCalls),
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		})
	}
	return out
}

func toEngineTools(tools []Tool) []agents.ToolSchema {
	out := make([]agents.ToolSchema, 0, len(tools))
	for _, t := range tools {
		out = append(out, agents.ToolSchema{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	return out
}

func toEngineToolCalls(calls []ToolCall) []agents.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]agents.ToolCall, 0, len(calls))
	for _, c := range calls {
		out = append(out, agents.ToolCall{ID: c.ID, Name: c.Name, Arguments: c.Arguments})
	}
	return out
}

func fromEngineToolCalls(calls []agents.ToolCall) []ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]ToolCall, 0, len(calls))
	for _, c := range calls {
		out = append(out, ToolCall{ID: c.ID, Name: c.Name, Arguments: c.Arguments})
	}
	return out
}

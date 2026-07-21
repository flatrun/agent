package ai

import (
	"context"

	"github.com/whilesmartgo/agents"
)

// MessageFromAgents converts a message the runner appended back to the stored
// type. Runner-created messages (assistant and tool turns) carry no UI fields.
func MessageFromAgents(m agents.Message) Message {
	return Message{
		Role:       m.Role,
		Content:    m.Content,
		ToolCalls:  fromEngineToolCalls(m.ToolCalls),
		ToolCallID: m.ToolCallID,
		Name:       m.Name,
	}
}

// ToolCallsFromAgents converts the runner's tool calls to the stored type.
func ToolCallsFromAgents(calls []agents.ToolCall) []ToolCall {
	return fromEngineToolCalls(calls)
}

// CapturingEngine adapts a Provider to the library's Engine. It records the
// model each response reports, which the runner does not otherwise surface, so
// a session can still note which model answered.
type CapturingEngine struct {
	provider  Provider
	lastModel string
}

func NewCapturingEngine(p Provider) *CapturingEngine {
	return &CapturingEngine{provider: p}
}

func (e *CapturingEngine) Complete(ctx context.Context, req agents.Request) (*agents.Response, error) {
	resp, err := e.provider.Complete(ctx, Request{
		Messages:    messagesFromAgents(req.Messages),
		Tools:       toolsFromAgents(req.Tools),
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
	})
	if err != nil {
		return nil, err
	}
	if resp.Model != "" {
		e.lastModel = resp.Model
	}
	return &agents.Response{
		Content:   resp.Content,
		ToolCalls: toEngineToolCalls(resp.ToolCalls),
		Model:     resp.Model,
		Usage:     agents.Usage{PromptTokens: resp.Usage.PromptTokens, CompletionTokens: resp.Usage.CompletionTokens},
	}, nil
}

// LastModel is the model of the most recent response, or "" if none.
func (e *CapturingEngine) LastModel() string { return e.lastModel }

func messagesFromAgents(messages []agents.Message) []Message {
	out := make([]Message, 0, len(messages))
	for _, m := range messages {
		out = append(out, MessageFromAgents(m))
	}
	return out
}

func toolsFromAgents(schemas []agents.ToolSchema) []Tool {
	out := make([]Tool, 0, len(schemas))
	for _, s := range schemas {
		out = append(out, Tool{Name: s.Name, Description: s.Description, Parameters: s.Parameters})
	}
	return out
}

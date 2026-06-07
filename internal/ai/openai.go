package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/flatrun/agent/pkg/config"
)

const maxResponseBytes = 4 << 20

// openAICompatible speaks the OpenAI chat-completions wire format,
// which OpenAI, Ollama, vLLM, LM Studio and most gateways all accept.
type openAICompatible struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

func newOpenAICompatible(cfg *config.AIConfig) *openAICompatible {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return &openAICompatible{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:  cfg.APIKey,
		model:   cfg.Model,
		client:  &http.Client{Timeout: timeout},
	}
}

func (p *openAICompatible) Name() string {
	return "openai-compatible"
}

// wireMessages converts internal messages to the OpenAI chat wire
// format, where assistant tool calls and tool results are nested
// differently than our flat representation.
func wireMessages(messages []Message) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(messages))
	for _, m := range messages {
		wm := map[string]interface{}{"role": m.Role, "content": m.Content}
		if len(m.ToolCalls) > 0 {
			calls := make([]map[string]interface{}, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				calls = append(calls, map[string]interface{}{
					"id":   tc.ID,
					"type": "function",
					"function": map[string]interface{}{
						"name":      tc.Name,
						"arguments": tc.Arguments,
					},
				})
			}
			wm["tool_calls"] = calls
		}
		if m.ToolCallID != "" {
			wm["tool_call_id"] = m.ToolCallID
		}
		if m.Name != "" {
			wm["name"] = m.Name
		}
		out = append(out, wm)
	}
	return out
}

func wireTools(tools []Tool) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Parameters,
			},
		})
	}
	return out
}

func (p *openAICompatible) Complete(ctx context.Context, req Request) (*Response, error) {
	payload := map[string]interface{}{
		"model":    p.model,
		"messages": wireMessages(req.Messages),
	}
	if len(req.Tools) > 0 {
		payload["tools"] = wireTools(req.Tools)
	}
	if req.MaxTokens > 0 {
		payload["max_tokens"] = req.MaxTokens
	}
	if req.Temperature > 0 {
		payload["temperature"] = req.Temperature
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// Local servers (Ollama, LM Studio) typically run keyless.
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ai provider request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("ai provider response read failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		msg := strings.TrimSpace(string(raw))
		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Error.Message != "" {
			msg = apiErr.Error.Message
		}
		return nil, fmt.Errorf("ai provider returned %d: %s", resp.StatusCode, msg)
	}

	var parsed struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage Usage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("ai provider returned invalid JSON: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("ai provider returned no choices")
	}

	model := parsed.Model
	if model == "" {
		model = p.model
	}

	choice := parsed.Choices[0].Message
	var toolCalls []ToolCall
	for _, tc := range choice.ToolCalls {
		toolCalls = append(toolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
	}

	return &Response{
		Content:   choice.Content,
		ToolCalls: toolCalls,
		Model:     model,
		Usage:     parsed.Usage,
	}, nil
}

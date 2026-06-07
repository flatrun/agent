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

func (p *openAICompatible) Complete(ctx context.Context, req Request) (*Response, error) {
	payload := map[string]interface{}{
		"model":    p.model,
		"messages": req.Messages,
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
			Message Message `json:"message"`
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
	return &Response{
		Content: parsed.Choices[0].Message.Content,
		Model:   model,
		Usage:   parsed.Usage,
	}, nil
}

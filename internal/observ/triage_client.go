package observ

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// triageCache makes a fault recurring next week reuse its verdict rather than pay again.
type triageCache struct {
	mu      sync.Mutex
	entries map[string]cachedTriage
	ttl     time.Duration
	max     int
}

type cachedTriage struct {
	verdict Triage
	at      time.Time
}

func newTriageCache(ttl time.Duration, max int) *triageCache {
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	if max <= 0 {
		max = 500
	}
	return &triageCache{entries: map[string]cachedTriage{}, ttl: ttl, max: max}
}

func (c *triageCache) get(key string) (Triage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || time.Since(entry.at) > c.ttl {
		return Triage{}, false
	}
	return entry.verdict, true
}

func (c *triageCache) put(key string, verdict Triage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.max {
		// Any victim will do: a wrong choice costs one extra call.
		for k := range c.entries {
			delete(c.entries, k)
			break
		}
	}
	c.entries[key] = cachedTriage{verdict: verdict, at: time.Now()}
}

// TriageClient asks the agent to explain an incident. The model, its key and the daily
// ceiling live in the agent; this decides what to send and remembers the answer.
type TriageClient struct {
	base   string
	token  string
	client *http.Client
	cache  *triageCache
}

func NewTriageClient(base, token string) *TriageClient {
	return &TriageClient{
		base:   strings.TrimRight(base, "/"),
		token:  token,
		client: &http.Client{Timeout: 60 * time.Second},
		cache:  newTriageCache(7*24*time.Hour, 500),
	}
}

type triageEnvelope struct {
	Triage struct {
		Summary    string `json:"summary"`
		Cause      string `json:"cause"`
		NextStep   string `json:"next_step"`
		Severity   string `json:"severity"`
		Confidence string `json:"confidence"`
	} `json:"triage"`
	Error string `json:"error"`
}

func (t *TriageClient) Explain(ctx context.Context, incident Incident) (*Triage, error) {
	if t.base == "" || t.token == "" {
		return nil, fmt.Errorf("no route to the assistant")
	}

	cacheKey := incident.Deployment + "/" + incident.Fingerprint
	if verdict, ok := t.cache.get(cacheKey); ok {
		return &verdict, nil
	}

	body, err := json.Marshal(map[string]any{
		"rule_name":  incident.RuleName,
		"deployment": incident.Deployment,
		"service":    incident.Service,
		"level":      incident.Level,
		"count":      incident.Count,
		"sample":     incident.Sample,
		"context":    incident.Context,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base+"/internal/ai/triage", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Plugin-Token", t.token)

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var envelope triageEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("the assistant's reply could not be read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		if envelope.Error != "" {
			return nil, fmt.Errorf("%s", envelope.Error)
		}
		return nil, fmt.Errorf("triage returned %s", resp.Status)
	}

	verdict := Triage{
		Summary:    envelope.Triage.Summary,
		Cause:      envelope.Triage.Cause,
		NextStep:   envelope.Triage.NextStep,
		Severity:   envelope.Triage.Severity,
		Confidence: envelope.Triage.Confidence,
		At:         time.Now().UTC(),
	}
	t.cache.put(cacheKey, verdict)
	return &verdict, nil
}

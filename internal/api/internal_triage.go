package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/flatrun/agent/internal/ai"
	"github.com/gin-gonic/gin"
)

// What one triage may cost. The app's funnel should keep it far below these; they exist so a
// bug in the funnel cannot become a bill.
const (
	maxTriageContextLines = 40
	maxTriageContextChars = 8000
	maxTriageOutputTokens = 400
	defaultTriageDailyCap = 25
)

// triageBudget counts triages against a daily ceiling. In-memory on purpose: this bounds a
// runaway, and a restart clearing the count costs less than persisting a counter for it.
type triageBudget struct {
	mu    sync.Mutex
	day   string
	spent int
	cap   int
}

func newTriageBudget(dailyCap int) *triageBudget {
	if dailyCap <= 0 {
		dailyCap = defaultTriageDailyCap
	}
	return &triageBudget{cap: dailyCap}
}

func (b *triageBudget) take(now time.Time) (int, int, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	today := now.UTC().Format("2006-01-02")
	if b.day != today {
		b.day = today
		b.spent = 0
	}
	if b.spent >= b.cap {
		return b.spent, b.cap, false
	}
	b.spent++
	return b.spent, b.cap, true
}

type triageRequest struct {
	RuleName   string   `json:"rule_name"`
	Deployment string   `json:"deployment"`
	Service    string   `json:"service"`
	Level      string   `json:"level"`
	Count      int      `json:"count"`
	Sample     string   `json:"sample"`
	Context    []string `json:"context"`
}

type triageResponse struct {
	Summary    string `json:"summary,omitempty"`
	Cause      string `json:"cause,omitempty"`
	NextStep   string `json:"next_step,omitempty"`
	Severity   string `json:"severity,omitempty"`
	Confidence string `json:"confidence,omitempty"`
}

const triageSystemPrompt = `You triage one recurring error from a container's logs on a self-hosted server.

You are given the failing line, a little of the output around it, and how often it has just occurred. You are not given the application's source, and you cannot run anything. Say what the evidence supports and no more.

Reply with JSON only, no prose and no code fence, with these keys:
  summary    one sentence an operator can read at 3am
  cause      the most likely cause, or "" if the lines do not say
  next_step  the single most useful thing to do next
  severity   one of: low, medium, high
  confidence one of: low, medium, high

If the lines are not enough to tell what is wrong, say so in summary, leave cause empty, and set confidence to low. A wrong confident answer is worse than an honest empty one.`

// triageLogIncident explains one log incident for a built-in app. The app's funnel decides
// what is worth asking; this decides what the asking may cost.
func (s *Server) triageLogIncident(c *gin.Context) {
	if s.pluginToken == "" || c.GetHeader("X-Plugin-Token") != s.pluginToken {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	if s.aiProvider == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "the assistant is not configured"})
		return
	}

	var req triageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if strings.TrimSpace(req.Sample) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "nothing to triage"})
		return
	}

	spent, cap, ok := s.triageBudget.take(time.Now())
	if !ok {
		// The ceiling lives here so it holds whatever asks for a triage.
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error": fmt.Sprintf("daily triage budget spent (%d of %d)", spent, cap),
		})
		return
	}

	prompt := buildTriagePrompt(req, s.redactorFor(req.Deployment))

	ctx, cancel := context.WithTimeout(c.Request.Context(), 40*time.Second)
	defer cancel()

	resp, err := s.aiProvider.Complete(ctx, ai.Request{
		Messages: []ai.Message{
			{Role: "system", Content: triageSystemPrompt},
			{Role: "user", Content: prompt},
		},
		MaxTokens:   maxTriageOutputTokens,
		Temperature: 0,
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	verdict, err := parseTriageVerdict(resp.Content)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"triage": verdict,
		"usage":  resp.Usage,
		"budget": gin.H{"spent": spent, "cap": cap},
	})
}

func (s *Server) redactorFor(deployment string) *ai.Redactor {
	secrets := s.systemSecretValues()
	if deployment != "" {
		secrets = s.deploymentSecretValues(deployment)
	}
	return ai.NewRedactor(secrets)
}

// buildTriagePrompt caps by line count and by total size, since one line of a base64 payload
// can be larger than forty ordinary ones.
func buildTriagePrompt(req triageRequest, redactor *ai.Redactor) string {
	lines := req.Context
	if len(lines) > maxTriageContextLines {
		lines = lines[len(lines)-maxTriageContextLines:]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Deployment: %s\n", req.Deployment)
	if req.Service != "" {
		fmt.Fprintf(&b, "Service: %s\n", req.Service)
	}
	if req.RuleName != "" {
		fmt.Fprintf(&b, "Rule: %s\n", req.RuleName)
	}
	fmt.Fprintf(&b, "Level: %s\nOccurrences just now: %d\n\nFailing line:\n%s\n", req.Level, req.Count, req.Sample)
	if len(lines) > 0 {
		b.WriteString("\nSurrounding output:\n")
		b.WriteString(strings.Join(lines, "\n"))
	}

	text := b.String()
	if len(text) > maxTriageContextChars {
		// The head carries the deployment, the rule and the failing line.
		text = text[:maxTriageContextChars] + "\n[truncated]"
	}
	if redactor != nil {
		text, _ = redactor.Redact(text)
	}
	return text
}

// parseTriageVerdict tolerates a code fence, the usual way a model ignores "JSON only".
func parseTriageVerdict(content string) (triageResponse, error) {
	text := strings.TrimSpace(content)
	if fence := strings.Index(text, "```"); fence >= 0 {
		rest := text[fence+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if end := strings.Index(rest, "```"); end >= 0 {
			rest = rest[:end]
		}
		text = strings.TrimSpace(rest)
	}
	start := strings.IndexByte(text, '{')
	end := strings.LastIndexByte(text, '}')
	if start < 0 || end <= start {
		return triageResponse{}, fmt.Errorf("the assistant did not answer with a verdict")
	}

	var verdict triageResponse
	if err := json.Unmarshal([]byte(text[start:end+1]), &verdict); err != nil {
		return triageResponse{}, fmt.Errorf("the assistant's verdict could not be read: %w", err)
	}
	if strings.TrimSpace(verdict.Summary) == "" {
		return triageResponse{}, fmt.Errorf("the assistant's verdict had no summary")
	}
	return verdict, nil
}

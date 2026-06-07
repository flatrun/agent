package ai

import (
	"fmt"
	"strings"
)

// Intent selects what the model is asked to do with the gathered
// context. Adding a capability is adding an entry here; the pipeline,
// endpoints and UI do not change.
type Intent struct {
	Key              string
	Task             string
	AllowSuggestions bool
}

var intents = map[string]Intent{
	"diagnose": {
		Key:              "diagnose",
		AllowSuggestions: true,
		Task: `First decide whether the context actually shows a problem. Normal startup messages, successful health checks and graceful shutdowns are signs of healthy operation, not failures; restarts without error output are usually operator actions. If everything indicates normal operation, state that plainly under ## Diagnosis and stop; do not propose changes. Never construct a problem so that there is something to fix.

If there is a problem, answer with sections:
## Diagnosis
The most likely root cause, stated plainly in one or two sentences.
## Evidence
The specific lines or config fragments that support the diagnosis.
## Fix
Concrete steps the operator should take; show the exact command or config snippet when one applies.
If the context is insufficient for a confident diagnosis, say so and list what to check next.`,
	},
	"improve": {
		Key:              "improve",
		AllowSuggestions: true,
		Task: `Review the context for improvements to reliability, performance and operability. Answer with sections:
## Findings
What could be better and why it matters, grounded in the context.
## Recommendations
Concrete, prioritized changes with the exact config or command for each where applicable.`,
	},
	"secure": {
		Key:              "secure",
		AllowSuggestions: true,
		Task: `Review the context for security weaknesses and hardening opportunities. Answer with sections:
## Risks
Each weakness found and its impact, grounded in the context. Do not invent vulnerabilities the context does not support.
## Hardening
Concrete, prioritized steps with the exact config or command for each where applicable.`,
	},
	"explain": {
		Key:              "explain",
		AllowSuggestions: false,
		Task: `Explain what the context shows in plain language for an operator who did not build this system. Answer with sections:
## Summary
What is happening, in a few sentences.
## Details
The notable parts explained simply, defining jargon briefly when unavoidable.`,
	},
}

func GetIntent(key string) (Intent, bool) {
	intent, ok := intents[key]
	return intent, ok
}

func IntentKeys() []string {
	keys := make([]string, 0, len(intents))
	for k := range intents {
		keys = append(keys, k)
	}
	return keys
}

const assistBasePrompt = `You are the assistant of FlatRun, a flat-file container hosting platform: a single Go agent manages deployments (each a directory with a docker-compose.yml), Docker networks, an nginx reverse proxy and Let's Encrypt certificates on one host.

FlatRun conventions: each deployment is a directory containing docker-compose.yml, an optional .env.flatrun env file and a service.yml metadata file. Services join pre-created external Docker networks: the configured proxy network connects apps to the nginx reverse proxy that serves them on the web, and the database network connects apps to shared databases. The agent generates one nginx virtual host per exposed deployment and manages Let's Encrypt certificates. Routing is defined in the deployment metadata: the reverse proxy forwards each domain to a service name and container port stored there; the compose "expose" field plays no role in FlatRun routing or health checks. Data uses bind mounts inside the deployment directory, never named volumes.

Answers must fit this installation, not Docker in general. The "FlatRun platform context" section in the user message states this host's actual configuration and state; reconcile everything you recommend against it, and when the generic fix and the platform's way of doing things differ, recommend the platform's way. A finding must be supported by the context; when the evidence shows normal operation or is inconclusive, saying so is the correct answer and speculative fixes are wrong.

%s

Ground every statement in the provided context; never invent log lines or configuration. Secret values appear as [REDACTED]; that is expected and not an error.%s%s`

const suggestionInstructions = `

If concrete steps can be run on the server, append ONE fenced code block with language tag "suggestions" containing a JSON array. Each entry is one of:
{"kind":"exec","service":"<compose service>","command":"<shell command run inside the service container>","title":"<short imperative label>","reason":"<one sentence why>"}
{"kind":"service_action","service":"<compose service>","action":"start|stop|restart|rebuild|pull","title":"<short imperative label>","reason":"<one sentence why>"}
Suggest at most 3 actions, only ones directly supported by the evidence, never destructive commands (no rm, no DROP, no down). The operator reviews and runs them manually. Omit the block entirely when nothing safe applies.`

// Section is one labeled piece of gathered context, already redacted.
type Section struct {
	Label   string
	Content string
	Format  string
}

// BuildAssistMessages assembles the chat for an analysis. Sections
// must already be redacted. The newest end of long sections survives
// truncation since it matters most.
func BuildAssistMessages(intent Intent, scopeLabel string, sections []Section, question, docsURL string) []Message {
	suggestions := ""
	if intent.AllowSuggestions {
		suggestions = suggestionInstructions
	}
	docs := ""
	if strings.TrimSpace(docsURL) != "" {
		docs = "\n\nProduct documentation you may reference in answers: " + strings.TrimSpace(docsURL)
	}
	system := fmt.Sprintf(assistBasePrompt, intent.Task, suggestions, docs)

	perSection := contextBudget
	if len(sections) > 0 {
		perSection = contextBudget / len(sections)
	}

	var user strings.Builder
	fmt.Fprintf(&user, "Scope: %s\n", scopeLabel)
	for _, section := range sections {
		format := section.Format
		if format == "" {
			format = "text"
		}
		fmt.Fprintf(&user, "\n## %s\n```%s\n%s\n```\n", section.Label, format, TruncateHead(section.Content, perSection))
	}
	if strings.TrimSpace(question) != "" {
		fmt.Fprintf(&user, "\n## Operator question\n%s\n", strings.TrimSpace(question))
	}

	return []Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user.String()},
	}
}

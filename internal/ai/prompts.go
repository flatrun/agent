package ai

import "fmt"

// contextBudget bounds the prompt so small local models with limited
// context windows still work; logs are truncated head-first since the
// newest lines matter most for diagnosis.
const contextBudget = 24000

const diagnosticSystemPrompt = `You are the diagnostic assistant of FlatRun, a flat-file container hosting platform where each app is a directory with a docker-compose.yml, an optional .env.flatrun file and an nginx reverse proxy in front.

Analyze the provided context and answer in Markdown with exactly these sections:
## Diagnosis
The most likely root cause, stated plainly in one or two sentences.
## Evidence
The specific log lines or config fragments that support the diagnosis.
## Fix
Concrete steps the operator should take, referencing the deployment's own files and services. If a compose or env change is needed, show the exact snippet.

If the context is insufficient for a confident diagnosis, say so and list what to check next. Never invent log lines or configuration that is not in the context. Secret values appear as [REDACTED]; that is expected and not an error.

If concrete remediation steps can be run on the server, append ONE fenced code block with language tag "suggestions" containing a JSON array. Each entry is one of:
{"kind":"exec","service":"<compose service>","command":"<shell command run inside the service container>","title":"<short imperative label>","reason":"<one sentence why>"}
{"kind":"service_action","service":"<compose service>","action":"start|stop|restart|rebuild|pull","title":"<short imperative label>","reason":"<one sentence why>"}
Suggest at most 3 actions, only ones directly supported by the evidence, never destructive commands (no rm, no DROP, no down). The operator reviews and runs them manually. Omit the block entirely when nothing safe applies.`

func TruncateHead(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return "[... truncated ...]\n" + s[len(s)-max:]
}

// BuildDiagnosisMessages assembles the chat for a log or failed
// operation analysis. All inputs must already be redacted.
func BuildDiagnosisMessages(deploymentName, composeContent, contextLabel, contextBody string) []Message {
	composeBudget := contextBudget / 4
	logBudget := contextBudget - min(len(composeContent), composeBudget)

	user := fmt.Sprintf("Deployment: %s\n\n## docker-compose.yml\n```yaml\n%s\n```\n\n## %s\n```\n%s\n```",
		deploymentName,
		TruncateHead(composeContent, composeBudget),
		contextLabel,
		TruncateHead(contextBody, logBudget),
	)

	return []Message{
		{Role: "system", Content: diagnosticSystemPrompt},
		{Role: "user", Content: user},
	}
}

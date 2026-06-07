package ai

// contextBudget bounds the prompt so small local models with limited
// context windows still work; long sections are truncated head-first
// since the newest lines matter most.
const contextBudget = 24000

func TruncateHead(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return "[... truncated ...]\n" + s[len(s)-max:]
}

package ai

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseAgentPolicyAndBudget(t *testing.T) {
	agent, err := ParseAgent("governed", `---
max_steps: 20
policy:
  auto_approve: [write_deployment_file]
  require_approval: [list_networks]
  deny: [control_deployment]
---
Do the work.`)
	if err != nil {
		t.Fatal(err)
	}
	if agent.MaxSteps != 20 {
		t.Errorf("max_steps = %d", agent.MaxSteps)
	}
	p := agent.Policy
	if p.Denies("list_networks") || !p.Denies("control_deployment") {
		t.Error("deny list misread")
	}
	if p.RequiresPause("write_deployment_file", true) {
		t.Error("an auto-approved write must not pause")
	}
	if !p.RequiresPause("list_networks", false) {
		t.Error("require_approval must pause a read tool")
	}
	if !p.RequiresPause("run_quick_action", true) {
		t.Error("an unlisted mutating tool keeps the default pause")
	}

	if _, err := ParseAgent("greedy", "---\nmax_steps: 500\n---\nLoop."); err == nil {
		t.Error("max_steps beyond the ceiling must be rejected")
	}
}

func TestNilPolicyKeepsDefaults(t *testing.T) {
	var p *AgentPolicy
	if p.Denies("anything") {
		t.Error("nil policy must deny nothing")
	}
	if !p.RequiresPause("write_deployment_file", true) || p.RequiresPause("list_networks", false) {
		t.Error("nil policy must keep the mutating-pauses default")
	}
}

func TestParseAgentWithFrontmatter(t *testing.T) {
	rt, err := ParseAgent("tidy-logs", `---
description: Trim old logs
scope: deployment
deployment: myapp
---
Check the logs directory and report anything unusual.`)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Name != "tidy-logs" || rt.Description != "Trim old logs" ||
		rt.Scope != SessionScopeDeployment || rt.Deployment != "myapp" {
		t.Errorf("unexpected agent: %+v", rt)
	}
	if rt.Instructions != "Check the logs directory and report anything unusual." {
		t.Errorf("instructions = %q", rt.Instructions)
	}
}

func TestParseAgentBareMarkdown(t *testing.T) {
	rt, err := ParseAgent("hello", "Say hello to the operator.")
	if err != nil {
		t.Fatal(err)
	}
	if rt.Scope != SessionScopeSystem {
		t.Errorf("a bare file should default to system scope, got %q", rt.Scope)
	}
	if rt.Instructions != "Say hello to the operator." {
		t.Errorf("instructions = %q", rt.Instructions)
	}
}

func TestParseAgentRejectsBadDefinitions(t *testing.T) {
	cases := map[string]string{
		"bad scope":            "---\nscope: galaxy\n---\nDo things.",
		"deployment missing":   "---\nscope: deployment\n---\nDo things.",
		"no instructions":      "---\ndescription: empty\n---\n",
		"frontmatter not yaml": "---\n: [\n---\nDo things.",
		"only whitespace body": "---\nscope: system\n---\n   \n",
	}
	for label, content := range cases {
		if _, err := ParseAgent("x", content); err == nil {
			t.Errorf("%s: expected an error", label)
		}
	}
}

func TestAgentStoreListSkipsInvalid(t *testing.T) {
	dir := t.TempDir()
	st := NewAgentStore(dir)
	if err := os.MkdirAll(st.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	writeAgent := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(st.Dir(), name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeAgent("beta.md", "Report disk usage.")
	writeAgent("alpha.md", "---\ndescription: first\n---\nSay hi.")
	writeAgent("broken.md", "---\nscope: nope\n---\nX.")
	writeAgent("notes.txt", "not a agent")

	agents, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 || agents[0].Name != "alpha" || agents[1].Name != "beta" {
		t.Errorf("unexpected listing: %+v", agents)
	}
}

func TestAgentStoreGetRefusesTraversal(t *testing.T) {
	st := NewAgentStore(t.TempDir())
	for _, name := range []string{"../escape", "a/b", ".hidden", ""} {
		if _, err := st.Get(name); err != ErrAgentNotFound {
			t.Errorf("name %q: expected ErrAgentNotFound, got %v", name, err)
		}
	}
}

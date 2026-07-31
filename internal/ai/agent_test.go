package ai

import (
	"os"
	"path/filepath"
	"testing"
)

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

func TestParseAgentScheduleAndPermissions(t *testing.T) {
	rt, err := ParseAgent("nightly", `---
description: Nightly checkup
scope: deployment
deployment: myapp
schedule: "0 3 * * *"
permissions:
  - deployments:read
  - deployments:write
---
Restart the app if it is unhealthy.`)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Schedule != "0 3 * * *" {
		t.Errorf("schedule = %q", rt.Schedule)
	}
	if len(rt.Permissions) != 2 || rt.Permissions[0] != "deployments:read" || rt.Permissions[1] != "deployments:write" {
		t.Errorf("permissions = %v", rt.Permissions)
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

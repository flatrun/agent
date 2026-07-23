package ai

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Agent is an agent definition: a flat markdown file with YAML frontmatter for
// metadata and a body of instructions. The runtime executes it through the
// shared tool set; the file is the whole definition, so it can be read,
// edited, and versioned like any other deployment file.
type Agent struct {
	Name         string       `json:"name" yaml:"-"`
	Description  string       `json:"description" yaml:"description"`
	Scope        string       `json:"scope" yaml:"scope"`
	Deployment   string       `json:"deployment,omitempty" yaml:"deployment"`
	MaxSteps     int          `json:"max_steps,omitempty" yaml:"max_steps"`
	Policy       *AgentPolicy `json:"policy,omitempty" yaml:"policy"`
	Instructions string       `json:"-" yaml:"-"`
}

// AgentPolicy is the agent's governance: deterministic rules the runtime
// enforces before any tool runs, regardless of what the model decides.
// Precedence is Deny, then RequireApproval, then AutoApprove. Without a rule,
// the default holds: state-changing tools pause for per-call approval.
type AgentPolicy struct {
	// AutoApprove names state-changing tools that run without pausing.
	AutoApprove []string `json:"auto_approve,omitempty" yaml:"auto_approve"`
	// RequireApproval names tools that always pause, even read-only ones.
	RequireApproval []string `json:"require_approval,omitempty" yaml:"require_approval"`
	// Deny names tools the agent can never call; they are not even advertised.
	Deny []string `json:"deny,omitempty" yaml:"deny"`
}

func inList(list []string, name string) bool {
	for _, n := range list {
		if n == name {
			return true
		}
	}
	return false
}

// Denies reports whether the policy forbids a tool outright.
func (p *AgentPolicy) Denies(name string) bool {
	return p != nil && inList(p.Deny, name)
}

// RequiresPause reports whether a call to the named tool must pause for
// operator approval. mutates is the tool's own classification; policy can
// widen it in either direction, but never past a Deny.
func (p *AgentPolicy) RequiresPause(name string, mutates bool) bool {
	if p == nil {
		return mutates
	}
	if inList(p.RequireApproval, name) {
		return true
	}
	if mutates && inList(p.AutoApprove, name) {
		return false
	}
	return mutates
}

// maxAgentSteps is the hard ceiling on max_steps, so a definition cannot buy
// itself an unbounded loop.
const maxAgentSteps = 50

var frontmatterPattern = regexp.MustCompile(`(?s)\A---\s*\n(.*?)\n---\s*\n?`)

// agentNamePattern keeps agent names path-safe: they become filenames and URL
// segments.
var agentNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// ParseAgent reads an agent definition. Frontmatter is optional; a bare
// markdown file is a system-scoped agent whose whole content is the
// instructions.
func ParseAgent(name, content string) (*Agent, error) {
	agent := &Agent{Name: name, Scope: SessionScopeSystem}
	body := content
	if m := frontmatterPattern.FindStringSubmatch(content); m != nil {
		if err := yaml.Unmarshal([]byte(m[1]), agent); err != nil {
			return nil, fmt.Errorf("invalid frontmatter: %w", err)
		}
		body = content[len(m[0]):]
	}
	agent.Name = name
	if agent.Scope == "" {
		agent.Scope = SessionScopeSystem
	}
	if agent.Scope != SessionScopeSystem && agent.Scope != SessionScopeDeployment {
		return nil, fmt.Errorf("scope must be %q or %q", SessionScopeSystem, SessionScopeDeployment)
	}
	if agent.Scope == SessionScopeDeployment && agent.Deployment == "" {
		return nil, fmt.Errorf("a deployment-scoped agent must name its deployment")
	}
	if agent.MaxSteps < 0 || agent.MaxSteps > maxAgentSteps {
		return nil, fmt.Errorf("max_steps must be between 1 and %d", maxAgentSteps)
	}
	agent.Instructions = strings.TrimSpace(body)
	if agent.Instructions == "" {
		return nil, fmt.Errorf("the agent has no instructions")
	}
	return agent, nil
}

// AgentStore reads agent definitions from a flat directory of markdown files,
// one agent per file, named by the file's basename.
type AgentStore struct {
	dir string
}

func NewAgentStore(deploymentsPath string) *AgentStore {
	return &AgentStore{dir: filepath.Join(deploymentsPath, ".flatrun", "agents")}
}

// Dir is the directory agent definition files live in.
func (st *AgentStore) Dir() string { return st.dir }

var ErrAgentNotFound = fmt.Errorf("agent not found")

// Get loads one agent by name.
func (st *AgentStore) Get(name string) (*Agent, error) {
	if !agentNamePattern.MatchString(name) {
		return nil, ErrAgentNotFound
	}
	content, err := os.ReadFile(filepath.Join(st.dir, name+".md"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrAgentNotFound
		}
		return nil, err
	}
	return ParseAgent(name, string(content))
}

// Raw returns the file content of one agent, for editing.
func (st *AgentStore) Raw(name string) (string, error) {
	if !agentNamePattern.MatchString(name) {
		return "", ErrAgentNotFound
	}
	content, err := os.ReadFile(filepath.Join(st.dir, name+".md"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrAgentNotFound
		}
		return "", err
	}
	return string(content), nil
}

// Write validates and stores an agent definition. Invalid definitions are
// rejected rather than written, so the directory only ever holds runnable
// agents.
func (st *AgentStore) Write(name, content string) (*Agent, error) {
	if !agentNamePattern.MatchString(name) {
		return nil, fmt.Errorf("invalid agent name %q: use letters, digits, dots, dashes, underscores", name)
	}
	agent, err := ParseAgent(name, content)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(st.dir, 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(st.dir, name+".md"), []byte(content), 0600); err != nil {
		return nil, err
	}
	return agent, nil
}

// Delete removes an agent definition.
func (st *AgentStore) Delete(name string) error {
	if !agentNamePattern.MatchString(name) {
		return ErrAgentNotFound
	}
	err := os.Remove(filepath.Join(st.dir, name+".md"))
	if os.IsNotExist(err) {
		return ErrAgentNotFound
	}
	return err
}

// List returns every valid agent, sorted by name. A file that fails to parse
// is skipped rather than breaking the listing.
func (st *AgentStore) List() ([]*Agent, error) {
	entries, err := os.ReadDir(st.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []*Agent{}, nil
		}
		return nil, err
	}
	agents := []*Agent{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		agent, err := st.Get(name)
		if err != nil {
			continue
		}
		agents = append(agents, agent)
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	return agents, nil
}

package config

import (
	"errors"
	"strings"

	"github.com/flatrun/agent/internal/webhook"
)

type EventsConfig struct {
	Endpoints     []EventEndpoint     `yaml:"endpoints" json:"-"`
	Subscriptions []EventSubscription `yaml:"subscriptions" json:"subscriptions"`
	DeliveryLimit int                 `yaml:"delivery_limit" json:"delivery_limit"`
	GitHubApp     GitHubAppConfig     `yaml:"github_app" json:"-"`
}

type GitHubAppConfig struct {
	AppID          int64  `yaml:"app_id"`
	PrivateKeyFile string `yaml:"private_key_file"`
}

type EventEndpoint struct {
	Name     string `yaml:"name"`
	Provider string `yaml:"provider"`
	Secret   string `yaml:"secret" json:"-"`
}

type EventSubscription struct {
	Name          string            `yaml:"name" json:"name"`
	Source        string            `yaml:"source" json:"source"`
	Type          string            `yaml:"type" json:"type"`
	Attributes    map[string]string `yaml:"attributes" json:"attributes,omitempty"`
	Labels        []string          `yaml:"labels" json:"labels,omitempty"`
	Action        string            `yaml:"action" json:"action"`
	Deployment    string            `yaml:"deployment" json:"deployment,omitempty"`
	Run           EventRunSpec      `yaml:"run" json:"run,omitempty"`
	GitHubRunner  bool              `yaml:"github_runner" json:"github_runner,omitempty"`
	RunnerGroupID int64             `yaml:"runner_group_id" json:"runner_group_id,omitempty"`
}

func (c *EventsConfig) SetDefaults() {
	if c.DeliveryLimit <= 0 {
		c.DeliveryLimit = 1000
	}
	if c.DeliveryLimit > 10000 {
		c.DeliveryLimit = 10000
	}
	for i := range c.Subscriptions {
		s := &c.Subscriptions[i]
		if s.GitHubRunner {
			if s.Run.Runtime == "" {
				s.Run.Runtime = "firecracker"
			}
			if s.Run.Image == "" {
				s.Run.Image = "ghcr.io/actions/actions-runner"
			}
			if s.RunnerGroupID == 0 {
				s.RunnerGroupID = 1
			}
		}
		if s.Run.TimeoutSeconds == 0 {
			s.Run.TimeoutSeconds = 600
		}
	}
}

type EventRunSpec struct {
	ArtifactPaths  []string          `yaml:"artifact_paths" json:"artifact_paths"`
	Runtime        string            `yaml:"runtime" json:"runtime"`
	Image          string            `yaml:"image" json:"image"`
	Command        []string          `yaml:"command" json:"command"`
	Env            map[string]string `yaml:"env" json:"env"`
	WorkDir        string            `yaml:"workdir" json:"workdir"`
	CPUs           float64           `yaml:"cpus" json:"cpus"`
	MemoryBytes    uint64            `yaml:"memory_bytes" json:"memory_bytes"`
	Egress         bool              `yaml:"egress" json:"egress"`
	TimeoutSeconds int               `yaml:"timeout_seconds" json:"timeout_seconds"`
}

func (c EventsConfig) Validate() error {
	endpoints := map[string]bool{}
	for _, e := range c.Endpoints {
		if e.Name == "" || strings.ContainsAny(e.Name, "/ ") || endpoints[e.Name] {
			return errors.New("event endpoint names must be unique URL segments")
		}
		endpoints[e.Name] = true
		if e.Secret == "" {
			return errors.New("event endpoint secret is required")
		}
		if _, ok := webhook.DefaultRegistry().Get(e.Provider); !ok {
			return errors.New("unknown event provider")
		}
	}
	names := map[string]bool{}
	for _, s := range c.Subscriptions {
		if s.Name == "" || names[s.Name] {
			return errors.New("event subscription names must be unique")
		}
		names[s.Name] = true
		switch s.Action {
		case "deploy", "preview":
			if s.Deployment == "" {
				return errors.New("event deployment is required")
			}
		case "notify":
		case "run":
			if s.Run.Image == "" {
				return errors.New("event run image is required")
			}
			if s.Run.Runtime != "" && s.Run.Runtime != "docker" && s.Run.Runtime != "firecracker" {
				return errors.New("unknown event run runtime")
			}
			if s.Run.TimeoutSeconds < 1 || s.Run.TimeoutSeconds > 3600 {
				return errors.New("event run timeout must be 1 to 3600 seconds")
			}
		default:
			return errors.New("unknown event action")
		}
		if s.GitHubRunner && len(s.Labels) == 0 {
			return errors.New("GitHub runner subscriptions require at least one label")
		}
		if s.GitHubRunner && (s.Action != "run" || s.Type != "github.workflow_job.queued" || c.GitHubApp.AppID <= 0 || c.GitHubApp.PrivateKeyFile == "" || s.RunnerGroupID <= 0) {
			return errors.New("GitHub runners require a queued job run subscription and GitHub App credentials")
		}
	}
	return nil
}

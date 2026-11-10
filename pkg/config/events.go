package config

import (
	"errors"
	"strings"

	"github.com/flatrun/agent/internal/runs"
	"github.com/flatrun/agent/internal/runtimes"
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
	Name         string            `yaml:"name" json:"name"`
	Source       string            `yaml:"source" json:"source"`
	Type         string            `yaml:"type" json:"type,omitempty"`
	Attributes   map[string]string `yaml:"attributes" json:"attributes,omitempty"`
	Labels       []string          `yaml:"labels" json:"labels,omitempty"`
	Action       string            `yaml:"action" json:"action"`
	Deployment   string            `yaml:"deployment" json:"deployment,omitempty"`
	Run          runs.Spec         `yaml:"run" json:"run,omitempty"`
	GitHubRunner GitHubRunnerSpec  `yaml:"github_runner" json:"github_runner,omitempty"`
}

type GitHubRunnerSpec struct {
	RunnerGroupID  int64         `yaml:"runner_group_id" json:"runner_group_id"`
	Runtime        runtimes.Type `yaml:"runtime" json:"runtime"`
	Image          string        `yaml:"image" json:"image"`
	TimeoutSeconds int           `yaml:"timeout_seconds" json:"timeout_seconds"`
	CPUs           float64       `yaml:"cpus" json:"cpus,omitempty"`
	MemoryBytes    uint64        `yaml:"memory_bytes" json:"memory_bytes,omitempty"`
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
		if s.Run.TimeoutSeconds == 0 {
			s.Run.TimeoutSeconds = 600
		}
		if s.Action == "github_runner" {
			r := &s.GitHubRunner
			if r.Runtime == "" {
				r.Runtime = runtimes.TypeFirecracker
			}
			if r.Image == "" {
				r.Image = "ghcr.io/actions/actions-runner"
			}
			if r.RunnerGroupID == 0 {
				r.RunnerGroupID = 1
			}
			if r.TimeoutSeconds == 0 {
				r.TimeoutSeconds = 600
			}
		}
	}
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
			if err := validateEventRun(s.Run); err != nil {
				return err
			}
		case "github_runner":
			r := s.GitHubRunner
			if len(s.Labels) == 0 {
				return errors.New("GitHub runner subscriptions require at least one label")
			}
			if c.GitHubApp.AppID <= 0 || c.GitHubApp.PrivateKeyFile == "" || r.RunnerGroupID <= 0 {
				return errors.New("GitHub runners require GitHub App credentials and a positive runner group")
			}
			if s.Type != "" {
				return errors.New("GitHub runner event type is implicit")
			}
			if err := validateEventRun(runs.Spec{Runtime: r.Runtime, Image: r.Image, TimeoutSeconds: r.TimeoutSeconds, CPUs: r.CPUs, MemoryBytes: r.MemoryBytes}); err != nil {
				return err
			}

		default:
			return errors.New("unknown event action")
		}
	}
	return nil
}

func validateEventRun(spec runs.Spec) error {
	if spec.Runtime != "" && spec.Runtime != runtimes.TypeDocker && spec.Runtime != runtimes.TypeFirecracker {
		return errors.New("unknown event run runtime")
	}
	if spec.TimeoutSeconds < 1 {
		return errors.New("event run timeout must be 1 to 3600 seconds")
	}
	return spec.Validate()
}

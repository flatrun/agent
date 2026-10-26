package models

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ComposeOverlayFile is the compose file the agent generates from
// ServiceSettings and passes after the deployment's own compose file, so
// the settings apply without rewriting the user's file.
const ComposeOverlayFile = "docker-compose.flatrun.yml"

// ServiceSettings are compose settings for one service that FlatRun keeps
// in service.yml and renders into ComposeOverlayFile.
type ServiceSettings struct {
	Resources   *ServiceResources `yaml:"resources,omitempty" json:"resources,omitempty"`
	Restart     string            `yaml:"restart,omitempty" json:"restart,omitempty"`
	Logging     *ServiceLogging   `yaml:"logging,omitempty" json:"logging,omitempty"`
	Environment map[string]string `yaml:"environment,omitempty" json:"environment,omitempty"`
	Command     []string          `yaml:"command,omitempty" json:"command,omitempty"`
	Entrypoint  []string          `yaml:"entrypoint,omitempty" json:"entrypoint,omitempty"`
}

// ServiceResources are hard limits; CPUs is a decimal count ("0.5") and
// Memory a size in compose syntax ("512m").
type ServiceResources struct {
	CPUs   string `yaml:"cpus,omitempty" json:"cpus,omitempty"`
	Memory string `yaml:"memory,omitempty" json:"memory,omitempty"`
}

// ServiceLogging rotates the json-file container log.
type ServiceLogging struct {
	MaxSize  string `yaml:"max_size,omitempty" json:"max_size,omitempty"`
	MaxFiles int    `yaml:"max_files,omitempty" json:"max_files,omitempty"`
}

var (
	envNamePattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	composeSizePattern = regexp.MustCompile(`^[0-9]+[bkmg]?$`)
	restartPattern     = regexp.MustCompile(`^(no|always|unless-stopped|on-failure(:[0-9]+)?)$`)
)

// IsEmpty reports whether the settings change nothing, so the service can
// be dropped from service.yml and the overlay.
func (s ServiceSettings) IsEmpty() bool {
	return (s.Resources == nil || (s.Resources.CPUs == "" && s.Resources.Memory == "")) &&
		s.Restart == "" &&
		(s.Logging == nil || (s.Logging.MaxSize == "" && s.Logging.MaxFiles == 0)) &&
		len(s.Environment) == 0 && len(s.Command) == 0 && len(s.Entrypoint) == 0
}

// Validate checks values compose would reject or misread.
func (s ServiceSettings) Validate() error {
	if r := s.Resources; r != nil {
		if r.CPUs != "" {
			cpus, err := strconv.ParseFloat(r.CPUs, 64)
			if err != nil || cpus <= 0 || cpus > 1024 {
				return fmt.Errorf("CPU limit must be a number above 0, such as 0.5 or 2")
			}
		}
		if r.Memory != "" && !validMemory(r.Memory) {
			return fmt.Errorf("memory limit must be a size of at least 6m, such as 512m or 2g")
		}
	}
	if s.Restart != "" && !restartPattern.MatchString(s.Restart) {
		return fmt.Errorf("restart policy must be no, always, unless-stopped or on-failure")
	}
	if l := s.Logging; l != nil {
		if l.MaxSize != "" && !composeSizePattern.MatchString(strings.ToLower(l.MaxSize)) {
			return fmt.Errorf("log size must be a size such as 10m")
		}
		if l.MaxFiles < 0 || l.MaxFiles > 100 {
			return fmt.Errorf("log files must be from 1 to 100")
		}
		if l.MaxFiles > 0 && l.MaxSize == "" {
			return fmt.Errorf("set a log size to keep more than one log file")
		}
	}
	if len(s.Environment) > 200 {
		return fmt.Errorf("at most 200 environment variables can be set")
	}
	for name, value := range s.Environment {
		if !envNamePattern.MatchString(name) {
			return fmt.Errorf("environment variable name %q may contain only letters, digits and _, and cannot start with a digit", name)
		}
		if strings.ContainsRune(value, 0) {
			return fmt.Errorf("environment variable %s contains a NUL character", name)
		}
	}
	for _, list := range [][]string{s.Command, s.Entrypoint} {
		if len(list) > 100 {
			return fmt.Errorf("command and entrypoint take at most 100 arguments")
		}
	}
	return nil
}

func validMemory(value string) bool {
	value = strings.ToLower(value)
	if !composeSizePattern.MatchString(value) {
		return false
	}
	n, err := strconv.ParseInt(strings.TrimRight(value, "bkmg"), 10, 64)
	if err != nil {
		return false
	}
	shift := map[byte]uint{'k': 10, 'm': 20, 'g': 30}[value[len(value)-1]]
	// Docker refuses a memory limit below 6 MiB.
	return n<<shift >= 6<<20
}

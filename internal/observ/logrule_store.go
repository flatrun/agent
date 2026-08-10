package observ

import (
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

// LogRuleStore keeps log rules in a flat file beside the metric rules.
type LogRuleStore struct {
	path string
	mu   sync.RWMutex
}

func NewLogRuleStore(basePath string) *LogRuleStore {
	return &LogRuleStore{path: filepath.Join(basePath, ".flatrun", "log-rules.yml")}
}

type logRuleFile struct {
	Rules []LogRule `yaml:"rules"`
}

func (s *LogRuleStore) Load() []LogRule {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}

	var f logRuleFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil
	}
	for i := range f.Rules {
		f.Rules[i] = f.Rules[i].WithDefaults()
	}
	return f.Rules
}

func (s *LogRuleStore) Save(rules []LogRule) error {
	for i := range rules {
		rules[i] = rules[i].WithDefaults()
		if err := rules[i].Validate(); err != nil {
			return err
		}
		if rules[i].ID == "" {
			id, err := newRuleID()
			if err != nil {
				return err
			}
			rules[i].ID = id
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}

	data, err := yaml.Marshal(logRuleFile{Rules: rules})
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0644)
}

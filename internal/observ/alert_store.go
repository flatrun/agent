package observ

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

// AlertStore keeps the rules in a flat file beside the rest of FlatRun's state, so they are
// readable and editable without the UI, like everything else here.
type AlertStore struct {
	path string
	mu   sync.RWMutex
}

func NewAlertStore(basePath string) *AlertStore {
	return &AlertStore{path: filepath.Join(basePath, ".flatrun", "alert-rules.yml")}
}

type alertFile struct {
	Rules []AlertRule `yaml:"rules"`
}

// Load reads the rules, returning none when the file has never been written.
func (s *AlertStore) Load() []AlertRule {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}

	var f alertFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil
	}
	return f.Rules
}

// Save replaces the rules, giving any new one an id.
func (s *AlertStore) Save(rules []AlertRule) error {
	for i := range rules {
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

	data, err := yaml.Marshal(alertFile{Rules: rules})
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0644)
}

func newRuleID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate a rule id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

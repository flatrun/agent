// Package dashboards stores the operator's own views over FlatRun's telemetry: which series
// they care about, arranged how they want them. The panels name what to draw; the data behind
// each one is fetched from whichever part of the agent owns it.
package dashboards

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Panel sources. Container metrics come from the observability engine; serving metrics come
// from the proxy's record of real requests.
const (
	SourceContainer = "container"
	SourceServing   = "serving"
)

// Serving series a panel can draw, named for what an operator is asking rather than for the
// column behind it.
const (
	ServingRequests = "requests"
	ServingErrors   = "errors"
	ServingLatency  = "latency"
)

// Panel shapes.
const (
	PanelLine = "line"
	PanelStat = "stat"
)

// Panel is one chart: a series, scoped to a deployment or to the whole host.
type Panel struct {
	ID     string `json:"id" yaml:"id"`
	Title  string `json:"title" yaml:"title"`
	Source string `json:"source" yaml:"source"`
	// Series is a container metric name for the container source, or one of the serving
	// series for the serving source.
	Series string `json:"series" yaml:"series"`
	// Deployment scopes the panel. Empty means every deployment.
	Deployment string `json:"deployment,omitempty" yaml:"deployment,omitempty"`
	Type       string `json:"type" yaml:"type"`
	// Width is how many of the grid's twelve columns the panel takes.
	Width int `json:"width" yaml:"width"`
}

// Dashboard is a named set of panels.
type Dashboard struct {
	ID     string  `json:"id" yaml:"id"`
	Name   string  `json:"name" yaml:"name"`
	Panels []Panel `json:"panels" yaml:"panels"`
}

// containerSeries are the metrics the observability engine collects. A panel naming anything
// else would draw an empty chart, so it is refused at the point it is saved.
var containerSeries = map[string]bool{
	"container.cpu.usage":     true,
	"container.memory.usage":  true,
	"container.memory.limit":  true,
	"container.network.io.rx": true,
	"container.network.io.tx": true,
}

func (p Panel) validate() error {
	if strings.TrimSpace(p.Title) == "" {
		return fmt.Errorf("a panel needs a title")
	}
	switch p.Source {
	case SourceContainer:
		if !containerSeries[p.Series] {
			return fmt.Errorf("panel %q: %q is not a collected container metric", p.Title, p.Series)
		}
	case SourceServing:
		switch p.Series {
		case ServingRequests, ServingErrors, ServingLatency:
		default:
			return fmt.Errorf("panel %q: %q is not a serving series", p.Title, p.Series)
		}
		// Serving is measured per deployment at the proxy, so a panel has to say which.
		if strings.TrimSpace(p.Deployment) == "" {
			return fmt.Errorf("panel %q: a serving panel needs a deployment", p.Title)
		}
	default:
		return fmt.Errorf("panel %q: source must be %q or %q", p.Title, SourceContainer, SourceServing)
	}
	if p.Type != PanelLine && p.Type != PanelStat {
		return fmt.Errorf("panel %q: type must be %q or %q", p.Title, PanelLine, PanelStat)
	}
	if p.Width < 1 || p.Width > 12 {
		return fmt.Errorf("panel %q: width must be between 1 and 12", p.Title)
	}
	return nil
}

// Validate reports why a dashboard cannot be saved, if it cannot.
func (d Dashboard) Validate() error {
	if strings.TrimSpace(d.Name) == "" {
		return fmt.Errorf("a dashboard needs a name")
	}
	for _, p := range d.Panels {
		if err := p.validate(); err != nil {
			return err
		}
	}
	return nil
}

// Store keeps dashboards in a flat file beside the rest of FlatRun's state, so they can be
// read, edited and copied between hosts without the UI.
type Store struct {
	path string
	mu   sync.RWMutex
}

func NewStore(basePath string) *Store {
	return &Store{path: filepath.Join(basePath, ".flatrun", "dashboards.yml")}
}

type file struct {
	Dashboards []Dashboard `yaml:"dashboards"`
}

// List returns every dashboard, and none when the file has never been written.
func (s *Store) List() []Dashboard {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.load()
}

func (s *Store) load() []Dashboard {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	var f file
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil
	}
	return f.Dashboards
}

// Get returns one dashboard by id.
func (s *Store) Get(id string) (Dashboard, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.load() {
		if d.ID == id {
			return d, true
		}
	}
	return Dashboard{}, false
}

// Save creates or replaces a dashboard, giving it and any new panel an id, and returns what
// was stored.
func (s *Store) Save(d Dashboard) (Dashboard, error) {
	if err := d.Validate(); err != nil {
		return Dashboard{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if d.ID == "" {
		id, err := newID()
		if err != nil {
			return Dashboard{}, err
		}
		d.ID = id
	}
	for i := range d.Panels {
		if d.Panels[i].ID == "" {
			id, err := newID()
			if err != nil {
				return Dashboard{}, err
			}
			d.Panels[i].ID = id
		}
	}

	existing := s.load()
	replaced := false
	for i := range existing {
		if existing[i].ID == d.ID {
			existing[i] = d
			replaced = true
			break
		}
	}
	if !replaced {
		existing = append(existing, d)
	}

	if err := s.write(existing); err != nil {
		return Dashboard{}, err
	}
	return d, nil
}

// Delete removes a dashboard, reporting whether it was there.
func (s *Store) Delete(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing := s.load()
	kept := make([]Dashboard, 0, len(existing))
	for _, d := range existing {
		if d.ID != id {
			kept = append(kept, d)
		}
	}
	if len(kept) == len(existing) {
		return false, nil
	}
	return true, s.write(kept)
}

func (s *Store) write(all []Dashboard) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	data, err := yaml.Marshal(file{Dashboards: all})
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0644)
}

func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate an id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

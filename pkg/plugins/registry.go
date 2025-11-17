package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

type Registry struct {
	plugins    map[string]Plugin
	pluginsDir string
	mu         sync.RWMutex
}

func NewRegistry(pluginsDir string) *Registry {
	return &Registry{
		plugins:    make(map[string]Plugin),
		pluginsDir: pluginsDir,
	}
}

func (r *Registry) Register(plugin Plugin) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	info := plugin.Info()
	if _, exists := r.plugins[info.Name]; exists {
		return fmt.Errorf("plugin %s already registered", info.Name)
	}

	r.plugins[info.Name] = plugin
	return nil
}

func (r *Registry) Unregister(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if plugin, exists := r.plugins[name]; exists {
		if err := plugin.Stop(); err != nil {
			return err
		}
		delete(r.plugins, name)
		return nil
	}

	return fmt.Errorf("plugin %s not found", name)
}

func (r *Registry) Get(name string) (Plugin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	plugin, exists := r.plugins[name]
	return plugin, exists
}

func (r *Registry) List() []PluginInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var infos []PluginInfo
	for _, plugin := range r.plugins {
		infos = append(infos, plugin.Info())
	}
	return infos
}

func (r *Registry) LoadFromDisk() error {
	if r.pluginsDir == "" {
		return nil
	}

	entries, err := os.ReadDir(r.pluginsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		manifestPath := filepath.Join(r.pluginsDir, entry.Name(), "plugin.yaml")
		if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
			continue
		}

		info, err := r.loadManifest(manifestPath)
		if err != nil {
			continue
		}

		plugin := &ExternalPlugin{
			info:    *info,
			basePath: filepath.Join(r.pluginsDir, entry.Name()),
		}

		r.Register(plugin)
	}

	return nil
}

func (r *Registry) loadManifest(path string) (*PluginInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var info PluginInfo
	if err := yaml.Unmarshal(data, &info); err != nil {
		return nil, err
	}

	info.Enabled = true
	return &info, nil
}

func (r *Registry) StartAll() error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for name, plugin := range r.plugins {
		if err := plugin.Start(); err != nil {
			return fmt.Errorf("failed to start plugin %s: %w", name, err)
		}
	}
	return nil
}

func (r *Registry) StopAll() error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for name, plugin := range r.plugins {
		if err := plugin.Stop(); err != nil {
			return fmt.Errorf("failed to stop plugin %s: %w", name, err)
		}
	}
	return nil
}

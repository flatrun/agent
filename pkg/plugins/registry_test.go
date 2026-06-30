package plugins

import "testing"

// coreOnly implements only the core Plugin interface: no lifecycle, no routes.
type coreOnly struct{ name string }

func (c *coreOnly) Info() PluginInfo { return PluginInfo{Name: c.name} }

// lifecyclePlugin additionally manages start/stop and records that it ran.
type lifecyclePlugin struct {
	coreOnly
	started, stopped bool
}

func (l *lifecyclePlugin) Start() error { l.started = true; return nil }
func (l *lifecyclePlugin) Stop() error  { l.stopped = true; return nil }

func TestRegistrySkipsNonLifecyclePlugins(t *testing.T) {
	r := NewRegistry("")
	core := &coreOnly{name: "firewall-like"}
	life := &lifecyclePlugin{coreOnly: coreOnly{name: "service-like"}}

	if err := r.Register(core); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(life); err != nil {
		t.Fatal(err)
	}

	// StartAll/StopAll must not require a non-lifecycle plugin to start or stop.
	if err := r.StartAll(); err != nil {
		t.Fatalf("StartAll() = %v", err)
	}
	if err := r.StopAll(); err != nil {
		t.Fatalf("StopAll() = %v", err)
	}
	if !life.started || !life.stopped {
		t.Error("the lifecycle plugin should have been started and stopped")
	}

	if len(r.Plugins()) != 2 {
		t.Errorf("Plugins() returned %d, want 2", len(r.Plugins()))
	}
	if len(r.List()) != 2 {
		t.Errorf("List() returned %d, want 2", len(r.List()))
	}
}

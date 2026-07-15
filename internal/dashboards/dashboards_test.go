package dashboards

import (
	"strings"
	"testing"
)

func cpuPanel() Panel {
	return Panel{Title: "CPU", Source: SourceContainer, Series: "container.cpu.usage", Type: PanelLine, Width: 6}
}

func TestSaveAssignsIdsAndPersists(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	saved, err := s.Save(Dashboard{Name: "Shop", Panels: []Panel{cpuPanel()}})
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" {
		t.Error("dashboard has no id")
	}
	// A panel needs its own id or it cannot be reordered or removed individually.
	if saved.Panels[0].ID == "" {
		t.Error("panel has no id")
	}

	// Dashboards are flat files, so a fresh reader sees what the last writer wrote.
	loaded := NewStore(dir).List()
	if len(loaded) != 1 || loaded[0].Name != "Shop" {
		t.Fatalf("dashboards did not survive: %+v", loaded)
	}
}

func TestSaveReplacesRatherThanDuplicates(t *testing.T) {
	s := NewStore(t.TempDir())

	saved, err := s.Save(Dashboard{Name: "Shop", Panels: []Panel{cpuPanel()}})
	if err != nil {
		t.Fatal(err)
	}

	saved.Name = "Shop renamed"
	if _, err := s.Save(saved); err != nil {
		t.Fatal(err)
	}

	all := s.List()
	if len(all) != 1 {
		t.Fatalf("saving an existing dashboard added a second: %+v", all)
	}
	if all[0].Name != "Shop renamed" {
		t.Errorf("name = %q", all[0].Name)
	}
}

func TestGetAndDelete(t *testing.T) {
	s := NewStore(t.TempDir())
	saved, _ := s.Save(Dashboard{Name: "Shop", Panels: []Panel{cpuPanel()}})

	got, ok := s.Get(saved.ID)
	if !ok || got.Name != "Shop" {
		t.Fatalf("Get returned %+v, %v", got, ok)
	}

	removed, err := s.Delete(saved.ID)
	if err != nil || !removed {
		t.Fatalf("Delete = %v, %v", removed, err)
	}
	if _, ok := s.Get(saved.ID); ok {
		t.Error("dashboard still there after delete")
	}

	// Deleting what is not there is not an error, it is just nothing.
	if removed, err := s.Delete("missing"); err != nil || removed {
		t.Errorf("Delete of a missing dashboard = %v, %v", removed, err)
	}
}

func TestValidateRejectsPanelsThatCannotDraw(t *testing.T) {
	bad := map[string]Dashboard{
		"no name": {Panels: []Panel{cpuPanel()}},
		"uncollected metric": {Name: "d", Panels: []Panel{
			{Title: "Disk", Source: SourceContainer, Series: "container.disk.usage", Type: PanelLine, Width: 6},
		}},
		"unknown source": {Name: "d", Panels: []Panel{
			{Title: "X", Source: "logs", Series: "requests", Type: PanelLine, Width: 6},
		}},
		"unknown serving series": {Name: "d", Panels: []Panel{
			{Title: "X", Source: SourceServing, Series: "throughput", Deployment: "shop", Type: PanelLine, Width: 6},
		}},
		"serving without a deployment": {Name: "d", Panels: []Panel{
			{Title: "X", Source: SourceServing, Series: ServingRequests, Type: PanelLine, Width: 6},
		}},
		"bad width": {Name: "d", Panels: []Panel{
			{Title: "X", Source: SourceContainer, Series: "container.cpu.usage", Type: PanelLine, Width: 13},
		}},
		"bad type": {Name: "d", Panels: []Panel{
			{Title: "X", Source: SourceContainer, Series: "container.cpu.usage", Type: "pie", Width: 6},
		}},
	}

	for why, d := range bad {
		if err := d.Validate(); err == nil {
			t.Errorf("%s: expected rejection", why)
		}
	}
}

func TestSaveRefusesAnUnusablePanel(t *testing.T) {
	s := NewStore(t.TempDir())

	// A panel naming a metric nothing collects would draw an empty chart forever, so it is
	// refused rather than stored.
	_, err := s.Save(Dashboard{Name: "d", Panels: []Panel{
		{Title: "Disk", Source: SourceContainer, Series: "container.disk.usage", Type: PanelLine, Width: 6},
	}})
	if err == nil {
		t.Fatal("expected rejection")
	}
	if !strings.Contains(err.Error(), "not a collected container metric") {
		t.Errorf("error = %v", err)
	}
	if len(s.List()) != 0 {
		t.Error("an unusable dashboard was stored")
	}
}

func TestServingPanelIsValid(t *testing.T) {
	d := Dashboard{Name: "Shop", Panels: []Panel{
		{Title: "Requests", Source: SourceServing, Series: ServingRequests, Deployment: "shop", Type: PanelLine, Width: 6},
		{Title: "p95", Source: SourceServing, Series: ServingLatency, Deployment: "shop", Type: PanelStat, Width: 3},
	}}
	if err := d.Validate(); err != nil {
		t.Errorf("a good serving dashboard was rejected: %v", err)
	}
}

package config

import "testing"

func TestCapacityDefaultsAllowVerticalAndHorizontalScaling(t *testing.T) {
	cfg := &Config{}
	setDefaults(cfg)

	if cfg.Capacity.AllocationThresholdPercent != 90 || cfg.Capacity.HostThresholdPercent != 85 {
		t.Fatalf("thresholds = %.0f, %.0f", cfg.Capacity.AllocationThresholdPercent, cfg.Capacity.HostThresholdPercent)
	}
	if cfg.Capacity.AllowVertical == nil || !*cfg.Capacity.AllowVertical {
		t.Fatal("vertical scaling should default to enabled")
	}
	if cfg.Capacity.AllowHorizontal == nil || !*cfg.Capacity.AllowHorizontal {
		t.Fatal("horizontal scaling should default to enabled")
	}
}

func TestCapacityDefaultsPreserveExplicitDisabledScaling(t *testing.T) {
	disabled := false
	cfg := &Config{Capacity: CapacityConfig{AllowVertical: &disabled, AllowHorizontal: &disabled}}
	setDefaults(cfg)

	if *cfg.Capacity.AllowVertical || *cfg.Capacity.AllowHorizontal {
		t.Fatal("explicit scaling choices were overwritten")
	}
}

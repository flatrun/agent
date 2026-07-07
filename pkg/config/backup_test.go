package config

import "testing"

func TestBackupDestination_IsEnabledDefaultsOn(t *testing.T) {
	if !(BackupDestination{}).IsEnabled() {
		t.Fatal("expected nil Enabled to default to on")
	}
	off := false
	if (BackupDestination{Enabled: &off}).IsEnabled() {
		t.Fatal("expected explicit false to disable")
	}
}

func TestBackupDestinations_RoundTripThroughRegistry(t *testing.T) {
	cfg := &Config{}
	setDefaults(cfg)

	dests := []BackupDestination{{
		Name:         "s3-prod",
		Type:         "s3",
		Endpoint:     "https://s3.example.com",
		Bucket:       "flatrun-backups",
		CredentialID: "abc123",
	}}

	if err := Set(cfg, "backup.destinations", dests); err != nil {
		t.Fatalf("set destinations: %v", err)
	}

	if len(cfg.Backup.Destinations) != 1 || cfg.Backup.Destinations[0].Bucket != "flatrun-backups" {
		t.Fatalf("destinations not applied: %#v", cfg.Backup.Destinations)
	}

	if _, err := Get(cfg, "backup.destinations"); err != nil {
		t.Fatalf("get destinations: %v", err)
	}
}

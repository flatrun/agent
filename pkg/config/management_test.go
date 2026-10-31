package config

import (
	"strings"
	"testing"
)

func TestManagementVersionDefaultsAndMigration(t *testing.T) {
	legacy := Config{}
	setDefaults(&legacy)
	if legacy.API.Host != "0.0.0.0" {
		t.Fatal("legacy listener changed")
	}
	if err := ValidateManagement(&legacy); err != nil {
		t.Fatal(err)
	}
	preview := PreviewManagement(&legacy)
	if preview.AppliesAutomatically || preview.ProposedHost != "127.0.0.1" || len(preview.Warnings) == 0 {
		t.Fatal("unsafe migration preview")
	}
	fresh := Config{ConfigVersion: 2, Auth: AuthConfig{Enabled: true, JWTSecret: strings.Repeat("x", 32)}}
	setDefaults(&fresh)
	if fresh.API.Host != "127.0.0.1" {
		t.Fatal("new listener is public")
	}
	if err := ValidateManagement(&fresh); err != nil {
		t.Fatal(err)
	}
	fresh.API.Host = "::"
	if ValidateManagement(&fresh) == nil {
		t.Fatal("public management allowed implicitly")
	}
	fresh.API.PublicManagement = true
	if err := ValidateManagement(&fresh); err != nil {
		t.Fatal(err)
	}
	fresh.Auth.Enabled = false
	if ValidateManagement(&fresh) == nil {
		t.Fatal("unauthenticated version 2 management allowed")
	}
}

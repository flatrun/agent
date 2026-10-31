package config

import (
	"fmt"
	"net/netip"
	"strings"
)

func privateListener(host string) bool {
	addr, err := netip.ParseAddr(host)
	return err == nil && (addr.IsLoopback() || addr.IsPrivate()) && !addr.IsUnspecified()
}
func ValidateManagement(cfg *Config) error {
	if cfg.API.PrivateHost != "" && !privateListener(cfg.API.PrivateHost) {
		return fmt.Errorf("api.private_host must be a private or loopback address")
	}
	if cfg.ConfigVersion < 0 || cfg.ConfigVersion > 2 {
		return fmt.Errorf("unsupported configuration version")
	}
	if cfg.ConfigVersion < 2 {
		return nil
	}
	if !cfg.Auth.Enabled {
		return fmt.Errorf("configuration version 2 requires management authentication")
	}
	if weakJWTSecret(cfg.Auth.JWTSecret) {
		return fmt.Errorf("configuration version 2 requires a generated JWT secret of at least 32 characters")
	}
	if !privateListener(cfg.API.Host) && !cfg.API.PublicManagement {
		return fmt.Errorf("public management requires explicit api.public_management")
	}
	return nil
}

type ManagementPreview struct {
	CurrentHost            string   `json:"current_host"`
	CurrentAuthentication  bool     `json:"current_authentication"`
	ProposedVersion        int      `json:"proposed_version"`
	ProposedHost           string   `json:"proposed_host"`
	ProposedAuthentication bool     `json:"proposed_authentication"`
	Warnings               []string `json:"warnings"`
	AppliesAutomatically   bool     `json:"applies_automatically"`
}

func PreviewManagement(cfg *Config) ManagementPreview {
	warnings := []string{}
	if !cfg.Auth.Enabled {
		warnings = append(warnings, "Management authentication is disabled")
	}
	if !privateListener(cfg.API.Host) {
		warnings = append(warnings, "Management listens outside a private or loopback interface")
	}
	if cfg.Auth.Enabled && weakJWTSecret(cfg.Auth.JWTSecret) {
		warnings = append(warnings, "Management signing secret requires replacement")
	}
	if cfg.ConfigVersion < 2 {
		warnings = append(warnings, "Legacy listener settings are preserved; migration requires an explicit configuration change")
	}
	return ManagementPreview{CurrentHost: cfg.API.Host, CurrentAuthentication: cfg.Auth.Enabled, ProposedVersion: 2, ProposedHost: "127.0.0.1", ProposedAuthentication: true, Warnings: warnings, AppliesAutomatically: false}
}

func weakJWTSecret(value string) bool {
	return len(value) < 32 || value == "default-secret-change-me" || strings.HasPrefix(value, "generate-") || strings.HasPrefix(value, "your-")
}

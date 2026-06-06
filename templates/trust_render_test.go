package templates

import (
	"strings"
	"testing"
)

func TestSecurityLuaRendersTrustConfig(t *testing.T) {
	out, err := GetNginxSecurityLuaWithConfig("10.0.0.1", 8080, "tok", nil, false)
	if err != nil {
		t.Fatalf("render default: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `local TRUSTED_PROXIES_RAW = ""`) {
		t.Errorf("default TRUSTED_PROXIES_RAW not empty:\n%s", grepLua(s))
	}
	if !strings.Contains(s, `local TRUST_CF_HEADER = false`) {
		t.Errorf("default TRUST_CF_HEADER not false")
	}
	if strings.Contains(s, "{{") {
		t.Errorf("unrendered template directive remains")
	}

	out, err = GetNginxSecurityLuaWithConfig("10.0.0.1", 8080, "tok", []string{"103.21.244.0/22", "172.16.0.0/12"}, true)
	if err != nil {
		t.Fatalf("render trusted: %v", err)
	}
	s = string(out)
	if !strings.Contains(s, `local TRUSTED_PROXIES_RAW = "103.21.244.0/22,172.16.0.0/12"`) {
		t.Errorf("trusted proxies not joined:\n%s", grepLua(s))
	}
	if !strings.Contains(s, `local TRUST_CF_HEADER = true`) {
		t.Errorf("TRUST_CF_HEADER not true")
	}
}

func grepLua(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "TRUSTED_PROXIES") || strings.Contains(line, "TRUST_CF") {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

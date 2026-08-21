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

func TestSecurityLuaSanitizesTrustedProxies(t *testing.T) {
	malicious := []string{
		`10.0.0.0/8";os.execute("touch /tmp/pwned");--`,
		`fe80::1%e"vil`,
		"1.2.3.4\nlocal x = 1",
		"not-an-ip",
		"  192.168.0.0/16  ",
		"203.0.113.7",
	}
	out, err := GetNginxSecurityLuaWithConfig("10.0.0.1", 8080, "tok", malicious, false)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	s := string(out)

	if !strings.Contains(s, `local TRUSTED_PROXIES_RAW = "192.168.0.0/16,203.0.113.7"`) {
		t.Errorf("expected only the two valid entries, got:\n%s", grepLua(s))
	}
	line := ""
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, "TRUSTED_PROXIES_RAW") {
			line = l
			break
		}
	}
	for _, bad := range []string{"os.execute", `\"`, "\\", "%", "not-an-ip", "local x"} {
		if strings.Contains(line, bad) {
			t.Errorf("sanitized line still contains %q: %s", bad, line)
		}
	}
}

func TestSecurityLuaBrandsNginxErrors(t *testing.T) {
	out, err := GetNginxSecurityLuaWithConfig("10.0.0.1", 8080, "tok", nil, false)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	s := string(out)
	for _, required := range []string{
		`X-FlatRun-Incident-ID`,
		`application/problem+json`,
		`incident_id = incident_id`,
		`ERROR_TEMPLATE_PATH`,
	} {
		if !strings.Contains(s, required) {
			t.Fatalf("rendered security module does not contain %q", required)
		}
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

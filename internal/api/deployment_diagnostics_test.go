package api

import (
	"strings"
	"testing"

	"github.com/flatrun/agent/pkg/models"
)

func TestValidHealthPath(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "root", value: "/", want: true},
		{name: "path", value: "/health/ready", want: true},
		{name: "query", value: "/health?full=1", want: true},
		{name: "relative", value: "health", want: false},
		{name: "absolute URL", value: "https://example.com/health", want: false},
		{name: "invalid escape", value: "/health%zz", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validHealthPath(test.value); got != test.want {
				t.Fatalf("validHealthPath(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestShellLiteral(t *testing.T) {
	const value = "/health?value=it's ready"
	const want = `'/health?value=it'"'"'s ready'`
	if got := shellLiteral(value); got != want {
		t.Fatalf("shellLiteral(%q) = %q, want %q", value, got, want)
	}
}

func TestParseHealthResponse(t *testing.T) {
	body, status, err := parseHealthResponse("{\"status\":\"ready\"}\n200")
	if err != nil || body != `{"status":"ready"}` || status != 200 {
		t.Fatalf("parseHealthResponse returned body %q, status %d, error %v", body, status, err)
	}
}

func TestHealthResponseContract(t *testing.T) {
	if !healthStatusAccepted(204, nil) || healthStatusAccepted(404, nil) {
		t.Fatal("default status range must accept 200 through 399")
	}
	if !healthStatusAccepted(404, []int{200, 404}) {
		t.Fatal("configured statuses must override the default range")
	}
	if !healthBodyAccepted(`{\"status\":\"ready\"}`, `\"status\":\"ready\"`) {
		t.Fatal("configured response text must be found in the response body")
	}
	if healthBodyAccepted(`{\"status\":\"starting\"}`, `\"status\":\"ready\"`) {
		t.Fatal("a missing response text must fail")
	}
}

func TestDiagnosticOutput(t *testing.T) {
	if got := diagnosticOutput("  failed to connect  "); got != "failed to connect" {
		t.Fatalf("diagnosticOutput returned %q", got)
	}
	if got := diagnosticOutput(""); got != "The endpoint returned an empty response body." {
		t.Fatalf("empty diagnosticOutput returned %q", got)
	}
	if got := diagnosticOutput(strings.Repeat("x", 5000)); len(got) > 4120 || !strings.HasSuffix(got, "Response truncated.") {
		t.Fatalf("long diagnosticOutput was not bounded")
	}
}

func TestValidateHealthCheckConfig(t *testing.T) {
	if err := validateHealthCheckConfig(models.HealthCheckConfig{Path: "/", SuccessStatuses: []int{200, 204}, ResponseContains: "ready"}); err != nil {
		t.Fatalf("valid health check rejected: %v", err)
	}
	if err := validateHealthCheckConfig(models.HealthCheckConfig{Path: "health"}); err == nil {
		t.Fatal("relative health check path accepted")
	}
	if err := validateHealthCheckConfig(models.HealthCheckConfig{Path: "/", SuccessStatuses: []int{700}}); err == nil {
		t.Fatal("invalid health check status accepted")
	}
}

func TestValidIncidentID(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{
		{value: "FR-1234ABCDEF56", want: true},
		{value: "FR-123", want: false},
		{value: "fr-1234abcdef56", want: false},
		{value: "FR-1234ABCDEFX6", want: false},
	} {
		if got := validIncidentID(test.value); got != test.want {
			t.Fatalf("validIncidentID(%q) = %v, want %v", test.value, got, test.want)
		}
	}
}

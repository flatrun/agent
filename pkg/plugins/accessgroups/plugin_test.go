package accessgroups

import (
	"strings"
	"testing"

	"github.com/flatrun/agent/pkg/models"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		policy  *models.AccessGroupsConfig
		wantErr string
	}{
		{"nil is valid", nil, ""},
		{"empty egress ok", &models.AccessGroupsConfig{Enabled: true}, ""},
		{
			"bad egress",
			&models.AccessGroupsConfig{Egress: "block-everything"},
			"egress must be",
		},
		{
			"rule needs exactly one target",
			&models.AccessGroupsConfig{Allow: []models.AccessRule{{To: "db", CIDR: "10.0.0.0/8"}}},
			"exactly one of",
		},
		{
			"rule with neither target",
			&models.AccessGroupsConfig{Allow: []models.AccessRule{{Port: 5432}}},
			"exactly one of",
		},
		{
			"port out of range",
			&models.AccessGroupsConfig{Allow: []models.AccessRule{{To: "db", Port: 70000}}},
			"out of range",
		},
		{
			"bad protocol",
			&models.AccessGroupsConfig{Allow: []models.AccessRule{{To: "db", Protocol: "icmp"}}},
			"protocol must be",
		},
		{
			"valid deny-all with east-west allow",
			&models.AccessGroupsConfig{Enabled: true, Egress: EgressDenyAll, Allow: []models.AccessRule{{To: "db", Port: 5432, Protocol: "tcp"}}},
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.policy)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestPlan(t *testing.T) {
	if Plan(nil) != nil {
		t.Error("Plan(nil) should be nil")
	}
	if Plan(&models.AccessGroupsConfig{Enabled: false}) != nil {
		t.Error("Plan(disabled) should be nil")
	}

	policy := &models.AccessGroupsConfig{
		Enabled: true,
		Egress:  EgressDenyAll,
		Allow: []models.AccessRule{
			{To: "db", Port: 5432},
			{CIDR: "10.0.0.0/8", Protocol: "udp", Port: 53},
		},
	}
	plan := Plan(policy)
	joined := strings.Join(plan, "\n")
	if !strings.Contains(joined, "default egress: deny-all") {
		t.Errorf("plan should state the default egress, got:\n%s", joined)
	}
	if !strings.Contains(joined, "allow tcp/5432 -> deployment db") {
		t.Errorf("plan should describe the east-west allow with default tcp, got:\n%s", joined)
	}
	if !strings.Contains(joined, "allow udp/53 -> cidr 10.0.0.0/8") {
		t.Errorf("plan should describe the egress CIDR allow, got:\n%s", joined)
	}
}

func TestInfoListsAsApp(t *testing.T) {
	info := New(nil).Info()
	if info.Name != "access-groups" || info.DisplayName != "Access Groups" {
		t.Errorf("unexpected plugin info: %+v", info)
	}
}

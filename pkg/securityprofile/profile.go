package securityprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/robfig/cron/v3"
)

const APIVersion = "flatrun.security-profile/v1"
const BaselineVersion = "1.0.0"

var Concerns = []string{"inventory", "exposure", "identity", "privileges", "integrity", "vulnerabilities", "credentials", "data_access", "monitoring", "writable_execution_surfaces", "attachment_safety", "upload_ingress", "object_authorization"}
var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
var imageDigest = regexp.MustCompile(`^[a-zA-Z0-9./:_-]+@sha256:[a-f0-9]{64}$`)
var credentialValue = regexp.MustCompile(`(?i)(?:AIza|sk-|ghp_|gho_|xox[baprs]-|AKIA)[a-z0-9_-]{8,}`)

type Profile struct {
	Raw             map[string]any    `json:"-" yaml:"-"`
	APIVersion      string            `json:"api_version" yaml:"api_version"`
	BaselineVersion string            `json:"baseline_version" yaml:"baseline_version"`
	Version         string            `json:"version" yaml:"version"`
	Runtime         string            `json:"runtime,omitempty" yaml:"runtime,omitempty"`
	Targets         map[string]Target `json:"targets" yaml:"targets"`
	Reviews         map[string]Review `json:"reviews" yaml:"reviews"`
	Schedule        Schedule          `json:"schedule" yaml:"schedule"`
	Budget          Budget            `json:"budget" yaml:"budget"`
	AccessPolicy    AccessPolicy      `json:"access_policy" yaml:"access_policy"`
	Exceptions      []Exception       `json:"exceptions,omitempty" yaml:"exceptions,omitempty"`
}

type Target struct {
	Service         string   `json:"service" yaml:"service"`
	Roots           []string `json:"roots" yaml:"roots"`
	MetadataSources []string `json:"metadata_sources,omitempty" yaml:"metadata_sources,omitempty"`
}
type Schedule struct {
	Incremental   string `json:"incremental" yaml:"incremental"`
	Retrospective string `json:"retrospective" yaml:"retrospective"`
	Full          string `json:"full" yaml:"full"`
}
type Budget struct {
	MaxFileBytes     int64 `json:"max_file_bytes" yaml:"max_file_bytes"`
	TimeoutSeconds   int   `json:"timeout_seconds" yaml:"timeout_seconds"`
	MaxOutputBytes   int   `json:"max_output_bytes" yaml:"max_output_bytes"`
	MaxFiles         int   `json:"max_files" yaml:"max_files"`
	MaxSnapshotBytes int64 `json:"max_snapshot_bytes" yaml:"max_snapshot_bytes"`
	ModelInputBytes  int   `json:"model_input_bytes" yaml:"model_input_bytes"`
	ModelOutputBytes int   `json:"model_output_bytes" yaml:"model_output_bytes"`
}
type Review struct {
	Concern      string      `json:"concern" yaml:"concern"`
	Target       string      `json:"target" yaml:"target"`
	Runs         []string    `json:"runs" yaml:"runs"`
	Expectations []string    `json:"expectations" yaml:"expectations"`
	Collect      []Collector `json:"collect" yaml:"collect"`
}
type Collector struct {
	ID              string   `json:"id" yaml:"id"`
	Image           string   `json:"image" yaml:"image"`
	Command         []string `json:"command" yaml:"command"`
	SnapshotPaths   []string `json:"snapshot_paths" yaml:"snapshot_paths"`
	MetadataSources []string `json:"metadata_sources" yaml:"metadata_sources"`
	Required        bool     `json:"required" yaml:"required"`
	Signals         []Signal `json:"signals" yaml:"signals"`
}
type Signal struct {
	Classification string `json:"classification,omitempty" yaml:"classification,omitempty"`
	ID             string `json:"id" yaml:"id"`
	Kind           string `json:"kind" yaml:"kind"`
	Pointer        string `json:"pointer" yaml:"pointer"`
	Value          any    `json:"value,omitempty" yaml:"value,omitempty"`
}
type Exception struct {
	Concern   string `json:"concern" yaml:"concern"`
	Scope     string `json:"scope" yaml:"scope"`
	Reason    string `json:"reason" yaml:"reason"`
	ExpiresAt string `json:"expires_at" yaml:"expires_at"`
}
type AccessPolicy struct {
	Version string                `json:"version" yaml:"version"`
	Actions map[string]ActionRule `json:"actions" yaml:"actions"`
}
type ActionRule struct {
	ResourceType          string        `json:"resource_type" yaml:"resource_type"`
	RequireAuthentication bool          `json:"require_authentication" yaml:"require_authentication"`
	Public                bool          `json:"public" yaml:"public"`
	Allow                 []AllowClause `json:"allow" yaml:"allow"`
}
type AllowClause struct {
	Kind  string `json:"kind" yaml:"kind"`
	Role  string `json:"role,omitempty" yaml:"role,omitempty"`
	Scope string `json:"scope,omitempty" yaml:"scope,omitempty"`
}

func Draft(runtime string) Profile {
	return Profile{APIVersion: APIVersion, BaselineVersion: BaselineVersion, Version: "1.0.0", Runtime: runtime,
		Targets: map[string]Target{}, Reviews: map[string]Review{},
		Schedule:     Schedule{Incremental: "*/5 * * * *", Retrospective: "17 * * * *", Full: "0 2 * * *"},
		Budget:       Budget{TimeoutSeconds: 30, MaxOutputBytes: 65536, MaxFiles: 10000, MaxFileBytes: 1 << 20, MaxSnapshotBytes: 64 << 20, ModelInputBytes: 128 << 10, ModelOutputBytes: 32 << 10},
		AccessPolicy: AccessPolicy{Version: "1", Actions: map[string]ActionRule{}}}
}

func Digest(value any) string {
	data, _ := json.Marshal(value)
	var canonical any
	if json.Unmarshal(data, &canonical) == nil {
		data, _ = json.Marshal(canonical)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func ValidIdentifier(value string) bool { return identifier.MatchString(value) }
func ImmutableImage(value string) bool  { return imageDigest.MatchString(value) }

func (p Profile) Validate() error {
	if p.Raw != nil {
		return fmt.Errorf("unsupported security profile fields or format")
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("profile cannot be encoded")
	}
	if len(encoded) > 512<<10 || credentialValue.Match(encoded) {
		return fmt.Errorf("profile exceeds limits or contains credential-shaped material")
	}
	if p.APIVersion != APIVersion || p.BaselineVersion != BaselineVersion {
		return fmt.Errorf("unsupported security profile version")
	}
	if p.Version == "" {
		return fmt.Errorf("profile version is required")
	}
	b := p.Budget
	if b.MaxFileBytes < 1 || b.MaxFileBytes > 64<<20 {
		return fmt.Errorf("file budget is missing or exceeds platform limits")
	}
	if b.TimeoutSeconds < 1 || b.TimeoutSeconds > 30 || b.MaxOutputBytes < 1 || b.MaxOutputBytes > 65536 || b.MaxFiles < 1 || b.MaxFiles > 10000 || b.MaxSnapshotBytes < 1 || b.MaxSnapshotBytes > 64<<20 || b.ModelInputBytes < 1 || b.ModelInputBytes > 128<<10 || b.ModelOutputBytes < 1 || b.ModelOutputBytes > 32<<10 {
		return fmt.Errorf("budget exceeds platform limits or is missing")
	}
	for _, spec := range []string{p.Schedule.Incremental, p.Schedule.Retrospective, p.Schedule.Full} {
		if _, err := cron.ParseStandard(spec); err != nil {
			return fmt.Errorf("invalid schedule")
		}
	}
	for id, t := range p.Targets {
		if !ValidIdentifier(id) || !ValidIdentifier(t.Service) {
			return fmt.Errorf("invalid target or service")
		}
		for _, root := range t.Roots {
			if !strings.HasPrefix(root, "/") || path.Clean(root) != root || root == "/" || strings.ContainsAny(root, "\x00\n\r,:") {
				return fmt.Errorf("invalid target root")
			}
		}
		for _, source := range t.MetadataSources {
			if !ValidIdentifier(source) {
				return fmt.Errorf("invalid metadata source")
			}
		}
	}
	for id, r := range p.Reviews {
		t, ok := p.Targets[r.Target]
		if !ValidIdentifier(id) || !ok || !slices.Contains(Concerns, r.Concern) {
			return fmt.Errorf("invalid review target or concern")
		}
		if len(r.Runs) == 0 {
			return fmt.Errorf("review must declare run modes")
		}
		for _, mode := range r.Runs {
			if !slices.Contains([]string{"incremental", "retrospective", "full"}, mode) {
				return fmt.Errorf("unsupported run mode")
			}
		}
		seen := map[string]bool{}
		for _, c := range r.Collect {
			if len(c.Command) > 32 || len(c.SnapshotPaths) > 64 || len(c.MetadataSources) > 32 || len(c.Signals) > 64 {
				return fmt.Errorf("collector scope exceeds platform limits")
			}
			if !ValidIdentifier(c.ID) || seen[c.ID] || (c.Image != "" && !ImmutableImage(c.Image)) || len(c.Command) == 0 {
				return fmt.Errorf("collector requires unique id, immutable image and explicit command")
			}
			seen[c.ID] = true
			for _, arg := range c.Command {
				if strings.ContainsRune(arg, 0) {
					return fmt.Errorf("invalid command")
				}
			}
			for _, s := range c.MetadataSources {
				if !slices.Contains(t.MetadataSources, s) {
					return fmt.Errorf("metadata source outside target scope")
				}
			}
			for _, file := range c.SnapshotPaths {
				if file == "" || strings.HasPrefix(file, "/") || path.Clean(file) != file || file == ".." || strings.HasPrefix(file, "../") || strings.ContainsAny(file, "\x00\r\n,:") || len(t.Roots) == 0 {
					return fmt.Errorf("invalid snapshot path")
				}
			}
			for _, s := range c.Signals {
				if s.Classification != "" && !slices.Contains([]string{"suspicious_condition", "policy_candidate"}, s.Classification) {
					return fmt.Errorf("unsupported signal classification")
				}
				if !ValidIdentifier(s.ID) || !slices.Contains([]string{"json_equals", "json_array_nonempty", "json_count_gt", "stdout_nonempty"}, s.Kind) || (s.Pointer != "" && !strings.HasPrefix(s.Pointer, "/")) {
					return fmt.Errorf("invalid signal")
				}
				if s.Kind == "json_count_gt" {
					v, ok := Numeric(s.Value)
					if !ok || v < 0 {
						return fmt.Errorf("count threshold must be a nonnegative number")
					}
				}
			}
		}
	}
	if len(p.Targets) > 64 || len(p.Reviews) > 64 {
		return fmt.Errorf("profile scope exceeds platform limits")
	}
	if p.AccessPolicy.Version != "1" {
		return fmt.Errorf("unsupported access policy version")
	}
	for name, a := range p.AccessPolicy.Actions {
		if !ValidIdentifier(name) || !ValidIdentifier(a.ResourceType) || (a.Public && (a.RequireAuthentication || len(a.Allow) > 0)) {
			return fmt.Errorf("invalid access action")
		}
		for _, clause := range a.Allow {
			if !slices.Contains([]string{"tenant_member", "resource_owner", "explicit_share", "role"}, clause.Kind) {
				return fmt.Errorf("unsupported access clause")
			}
			if clause.Kind == "role" && (clause.Role == "" || (clause.Scope != "tenant" && clause.Scope != "global")) {
				return fmt.Errorf("role requires explicit scope")
			}
		}
	}
	return nil
}

func Numeric(value any) (float64, bool) {
	data, err := json.Marshal(value)
	if err != nil {
		return 0, false
	}
	var decoded any
	if json.Unmarshal(data, &decoded) != nil {
		return 0, false
	}
	number, ok := decoded.(float64)
	return number, ok
}

package securityreview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/flatrun/agent/pkg/securityprofile"
	"github.com/google/uuid"
)

type CollectorCoverage struct {
	DeclaredObservation string `json:"declared_observation"`
	ManifestProcessing  string `json:"manifest_processing"`
	WindowCoverage      string `json:"window_coverage"`
}
type Omission struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}
type CollectorResult struct {
	APIVersion    string            `json:"api_version"`
	ProcessStatus string            `json:"process_status"`
	Items         []json.RawMessage `json:"items"`
	Omissions     []Omission        `json:"omissions"`
	Boundaries    []Omission        `json:"boundaries"`
	Limitations   []string          `json:"limitations"`
	Coverage      CollectorCoverage `json:"coverage"`
}

type limitedBuffer struct {
	data      bytes.Buffer
	limit     int
	truncated bool
	mu        sync.Mutex
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := b.limit - b.data.Len()
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, _ = b.data.Write(p)
	return n, nil
}

type Runner struct {
	Images      []string
	ScratchRoot string
	DockerHost  string
}
type Input struct {
	Snapshot string
	Metadata string
	Manifest string
}

var secretsPattern = regexp.MustCompile(`(?i)(bearer\s+[a-z0-9._~+/=-]+|(?:sk-|ghp_|gho_|xox[baprs]-|AKIA|AIza)[a-z0-9_-]{8,})`)
var sensitiveKey = regexp.MustCompile(`(?i)(password|passwd|secret|token|authorization|cookie|session|credential|private_key|api_key|linker|ldflags|build_settings)`)
var credentialAssignment = regexp.MustCompile(`(?i)([a-z0-9_\-]*(?:password|passwd|secret|token|api_?key))(\s*[=:]\s*)("[^"\n]*"|'[^'\n]*'|[^\s,;]+)`)

func Sanitize(value any) any {
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var decoded any
	if json.Unmarshal(data, &decoded) != nil {
		return nil
	}
	return sanitizeValue(decoded)
}
func sanitizeValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, item := range v {
			if sensitiveKey.MatchString(k) {
				out[k] = "<REDACTED>"
			} else {
				out[k] = sanitizeValue(item)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = sanitizeValue(item)
		}
		return out
	case string:
		return credentialAssignment.ReplaceAllString(secretsPattern.ReplaceAllString(v, "<REDACTED>"), "${1}${2}<REDACTED>")
	default:
		return value
	}
}

func (r Runner) Args(c securityprofile.Collector, b securityprofile.Budget, input Input, name string) ([]string, error) {
	if r.DockerHost != "" && !strings.HasPrefix(r.DockerHost, "unix:///") {
		return nil, fmt.Errorf("collector isolation requires a local Unix Docker socket")
	}
	approved := false
	for _, image := range r.Images {
		if image == c.Image {
			approved = true
		}
	}
	if !approved || !securityprofile.ImmutableImage(c.Image) {
		return nil, fmt.Errorf("collector image is not administrator approved")
	}
	if len(c.Command) == 0 {
		return nil, fmt.Errorf("collector command is required")
	}
	args := []string{"create", "--name", name, "--pull=never", "--network=none", "--read-only", "--user=65532:65532", "--cap-drop=ALL", "--security-opt=no-new-privileges:true", "--pids-limit=128", "--memory=256m", "--memory-swap=256m", "--cpus=1", "--workdir=/work", "--tmpfs=/work:rw,noexec,nosuid,nodev,size=1m,mode=1777", "--tmpfs=/tmp:rw,noexec,nosuid,nodev,size=64m,mode=1777", "--env=PATH=/usr/local/bin:/usr/bin:/bin", "--env=HOME=/tmp", "--env=LANG=C.UTF-8", "--env=GOTOOLCHAIN=local", "--env=GOWORK=off", "--env=GOENV=off", "--env=GOFLAGS=", "--env=GOPROXY=off", "--env=GOSUMDB=off", "--env=PYTHONNOUSERSITE=1", "--log-driver=none"}
	for _, mount := range []struct {
		source, target string
		enabled        bool
	}{{input.Snapshot, "/evidence", len(c.SnapshotPaths) > 0}, {input.Metadata, "/input", len(c.MetadataSources) > 0}, {input.Manifest, "/manifest", input.Manifest != "" && len(c.SnapshotPaths) > 0}} {
		if !mount.enabled {
			continue
		}
		if mount.source == "" {
			return nil, fmt.Errorf("required collector input unavailable")
		}
		resolved, err := filepath.EvalSymlinks(mount.source)
		if err != nil {
			return nil, err
		}
		base, err := filepath.EvalSymlinks(r.ScratchRoot)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(base, resolved)
		if err != nil || rel == "." || rel == ".." || startsParent(rel) || strings.ContainsAny(resolved, ",\x00\n\r") {
			return nil, fmt.Errorf("collector input is outside private snapshot storage")
		}
		args = append(args, "--mount", fmt.Sprintf("type=bind,src=%s,dst=%s,readonly,bind-propagation=rprivate", resolved, mount.target))
	}
	command := append([]string{}, c.Command...)
	if filepath.Base(command[0]) == "python3" || filepath.Base(command[0]) == "python" {
		command = append([]string{command[0], "-I"}, command[1:]...)
	}
	args = append(args, "--entrypoint", command[0], c.Image)
	return append(args, command[1:]...), nil
}

func (r Runner) docker(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/nonexistent"}
	if r.DockerHost != "" {
		cmd.Env = append(cmd.Env, "DOCKER_HOST="+r.DockerHost)
	}
	return cmd
}

func (r Runner) Run(ctx context.Context, c securityprofile.Collector, b securityprofile.Budget, input Input) (CollectorResult, error) {
	result := CollectorResult{APIVersion: "flatrun.collector-result/v1", ProcessStatus: "unsupported", Items: []json.RawMessage{}, Omissions: []Omission{}, Boundaries: []Omission{}, Limitations: []string{}, Coverage: CollectorCoverage{"unsupported", "unknown", "unknown"}}
	if b.TimeoutSeconds < 1 || b.TimeoutSeconds > 30 || b.MaxOutputBytes < 1 || b.MaxOutputBytes > 65536 {
		return result, fmt.Errorf("invalid collector budget")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(b.TimeoutSeconds)*time.Second)
	defer cancel()
	name := "flatrun-security-" + uuid.NewString()
	args, err := r.Args(c, b, input, name)
	if err != nil {
		return result, err
	}
	var info struct {
		SecurityOptions []string `json:"SecurityOptions"`
		OSType          string   `json:"OSType"`
	}
	infoBytes, err := r.docker(ctx, "info", "--format", "{{json .}}").Output()
	if err != nil || json.Unmarshal(infoBytes, &info) != nil || info.OSType != "linux" {
		return result, fmt.Errorf("isolated Linux collector execution unavailable")
	}
	seccomp := false
	for _, option := range info.SecurityOptions {
		if strings.Contains(option, "name=seccomp,profile=builtin") {
			seccomp = true
		}
	}
	if !seccomp {
		return result, fmt.Errorf("collector seccomp enforcement unavailable")
	}
	if err = r.docker(ctx, args...).Run(); err != nil {
		return result, fmt.Errorf("collector container creation failed")
	}
	defer func() {
		cleanup, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = r.docker(cleanup, "rm", "--force", name).Run()
	}()
	out := &limitedBuffer{limit: b.MaxOutputBytes}
	stderr := &limitedBuffer{limit: b.MaxOutputBytes}
	cmd := r.docker(ctx, "start", "--attach", name)
	cmd.Stdout = out
	cmd.Stderr = stderr
	runErr := cmd.Run()
	var exit struct {
		ExitCode  int    `json:"ExitCode"`
		OOMKilled bool   `json:"OOMKilled"`
		Status    string `json:"Status"`
	}
	state, inspectErr := r.docker(ctx, "inspect", "--format", "{{json .State}}", name).Output()
	if runErr != nil || inspectErr != nil || json.Unmarshal(state, &exit) != nil || exit.ExitCode != 0 || exit.OOMKilled || exit.Status != "exited" {
		result.ProcessStatus = "failed"
	} else {
		result.ProcessStatus = "completed"
	}
	if out.truncated || stderr.truncated {
		result.Coverage.DeclaredObservation = "partial"
		result.Limitations = append(result.Limitations, "Collector output exceeded the approved budget")
	}
	var parsed CollectorResult
	decoder := json.NewDecoder(bytes.NewReader(out.data.Bytes()))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&parsed); err != nil {
		return result, fmt.Errorf("invalid collector result")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return result, fmt.Errorf("collector returned trailing data")
	}
	if parsed.APIVersion != "flatrun.collector-result/v1" || parsed.Items == nil || !validCoverage(parsed.Coverage.DeclaredObservation) || !validCoverage(parsed.Coverage.ManifestProcessing) || !validCoverage(parsed.Coverage.WindowCoverage) {
		return result, fmt.Errorf("unsupported collector result contract")
	}
	for _, item := range parsed.Items {
		var value any
		if json.Unmarshal(item, &value) != nil {
			continue
		}
		safe, _ := json.Marshal(Sanitize(value))
		result.Items = append(result.Items, safe)
	}
	result.Omissions = parsed.Omissions
	result.Boundaries = parsed.Boundaries
	result.Limitations = append(result.Limitations, parsed.Limitations...)
	result.Coverage = parsed.Coverage
	if result.ProcessStatus != "completed" || runErr != nil || exit.ExitCode != 0 || out.truncated || stderr.truncated || len(result.Omissions) > 0 {
		result.Coverage.DeclaredObservation = "partial"
	}
	// Collector output cannot establish historical source completeness.
	result.Coverage.WindowCoverage = "unknown"
	data, err := json.Marshal(Sanitize(result))
	if err != nil {
		return CollectorResult{ProcessStatus: "failed", Coverage: CollectorCoverage{"unknown", "unknown", "unknown"}}, fmt.Errorf("collector redaction failed")
	}
	var safe CollectorResult
	if err = json.Unmarshal(data, &safe); err != nil {
		return CollectorResult{ProcessStatus: "failed", Coverage: CollectorCoverage{"unknown", "unknown", "unknown"}}, fmt.Errorf("invalid sanitized collector result")
	}
	return safe, nil
}
func validCoverage(v string) bool {
	return v == "complete" || v == "partial" || v == "unknown" || v == "unsupported"
}

func WriteMetadata(root string, value any) (string, error) {
	dir, err := os.MkdirTemp(root, "metadata-")
	if err != nil {
		return "", err
	}
	if err = os.Chmod(dir, 0755); err != nil {
		return "", err
	}
	data, err := json.Marshal(Sanitize(value))
	if err != nil {
		return "", err
	}
	err = os.WriteFile(filepath.Join(dir, "metadata.json"), data, 0444)
	return dir, err
}

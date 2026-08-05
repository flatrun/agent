package docker

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"gopkg.in/yaml.v3"
)

type ComposeExecutor struct {
	basePath string

	// composeCmd caches the detected compose command. Detection spawns a
	// process, and every compose invocation needs the result, so it is resolved
	// once rather than per call. Only a successful detection is cached, so an
	// agent that starts before Docker is available still recovers.
	composeCmd atomic.Value
}

func NewComposeExecutor(basePath string) *ComposeExecutor {
	return &ComposeExecutor{basePath: basePath}
}

type RunOption func(*runOpts)

type runOpts struct {
	extraEnv      []string
	lineSink      func(string)
	forceRecreate bool
	noCache       bool
	freshPull     bool
}

// WithForceRecreate recreates containers even when their config and image are
// unchanged, so updated environment variables take effect.
func WithForceRecreate() RunOption {
	return func(o *runOpts) { o.forceRecreate = true }
}

// WithNoCache rebuilds images without using the build cache before bringing the
// deployment up.
func WithNoCache() RunOption {
	return func(o *runOpts) { o.noCache = true }
}

// WithFreshPull forces images to be pulled rather than served from the local
// cache during an up.
func WithFreshPull() RunOption {
	return func(o *runOpts) { o.freshPull = true }
}

func resolveRunOpts(opts []RunOption) runOpts {
	var ro runOpts
	for _, opt := range opts {
		opt(&ro)
	}
	return ro
}

// applyUpFlags appends the effective-apply flags supported by `compose up`.
func applyUpFlags(args []string, ro runOpts) []string {
	if ro.forceRecreate {
		args = append(args, "--force-recreate")
	}
	if ro.freshPull {
		args = append(args, "--pull", "always")
	}
	return args
}

func WithDockerConfig(dir string) RunOption {
	return func(o *runOpts) {
		if dir != "" {
			o.extraEnv = append(o.extraEnv, "DOCKER_CONFIG="+dir)
		}
	}
}

// WithLineSink streams the compose command's combined output to sink one line
// at a time as it is produced, instead of returning it only on completion.
func WithLineSink(sink func(string)) RunOption {
	return func(o *runOpts) {
		o.lineSink = sink
	}
}

func (c *ComposeExecutor) Up(deploymentPath string, opts ...RunOption) (string, error) {
	ro := resolveRunOpts(opts)
	if ro.noCache {
		if out, err := c.runCompose(deploymentPath, opts, "build", "--no-cache"); err != nil {
			return out, err
		}
	}
	args := applyUpFlags([]string{"up", "-d", "--remove-orphans"}, ro)
	return c.runCompose(deploymentPath, opts, args...)
}

func (c *ComposeExecutor) Down(deploymentPath string, opts ...RunOption) (string, error) {
	return c.runCompose(deploymentPath, opts, "down", "--remove-orphans")
}

func (c *ComposeExecutor) Start(deploymentPath string, opts ...RunOption) (string, error) {
	output, err := c.runCompose(deploymentPath, opts, "start")
	if err != nil {
		return c.runCompose(deploymentPath, opts, "up", "-d", "--remove-orphans")
	}
	return output, nil
}

func (c *ComposeExecutor) Stop(deploymentPath string, opts ...RunOption) (string, error) {
	return c.runCompose(deploymentPath, opts, "stop")
}

func (c *ComposeExecutor) Restart(deploymentPath string, opts ...RunOption) (string, error) {
	ro := resolveRunOpts(opts)
	_, _ = c.runCompose(deploymentPath, opts, "down", "--remove-orphans")
	if ro.noCache {
		if out, err := c.runCompose(deploymentPath, opts, "build", "--no-cache"); err != nil {
			return out, err
		}
	}
	args := applyUpFlags([]string{"up", "-d", "--remove-orphans"}, ro)
	return c.runCompose(deploymentPath, opts, args...)
}

func (c *ComposeExecutor) Rebuild(deploymentPath string, opts ...RunOption) (string, error) {
	ro := resolveRunOpts(opts)
	_, _ = c.runCompose(deploymentPath, opts, "down", "--remove-orphans")
	if ro.noCache {
		if out, err := c.runCompose(deploymentPath, opts, "build", "--no-cache"); err != nil {
			return out, err
		}
	}
	args := applyUpFlags([]string{"up", "-d", "--build", "--remove-orphans"}, ro)
	return c.runCompose(deploymentPath, opts, args...)
}

func (c *ComposeExecutor) StartService(deploymentPath, service string, opts ...RunOption) (string, error) {
	ro := resolveRunOpts(opts)
	// When an effective-apply option is set, go straight to `up` so the flags take effect;
	// a plain `start` cannot recreate, rebuild, or pull.
	if ro.forceRecreate || ro.freshPull || ro.noCache {
		if ro.noCache {
			if out, err := c.runCompose(deploymentPath, opts, "build", "--no-cache", service); err != nil {
				return out, err
			}
		}
		args := applyUpFlags([]string{"up", "-d", "--no-deps"}, ro)
		args = append(args, service)
		return c.runCompose(deploymentPath, opts, args...)
	}
	output, err := c.runCompose(deploymentPath, opts, "start", service)
	if err != nil {
		return c.runCompose(deploymentPath, opts, "up", "-d", "--no-deps", service)
	}
	return output, nil
}

func (c *ComposeExecutor) StopService(deploymentPath, service string, opts ...RunOption) (string, error) {
	return c.runCompose(deploymentPath, opts, "stop", service)
}

func (c *ComposeExecutor) RestartService(deploymentPath, service string, opts ...RunOption) (string, error) {
	return c.runCompose(deploymentPath, opts, "restart", service)
}

func (c *ComposeExecutor) RebuildService(deploymentPath, service string, opts ...RunOption) (string, error) {
	ro := resolveRunOpts(opts)
	if ro.noCache {
		if out, err := c.runCompose(deploymentPath, opts, "build", "--no-cache", service); err != nil {
			return out, err
		}
	}
	args := []string{"up", "-d", "--no-deps", "--build", "--force-recreate"}
	if ro.freshPull {
		args = append(args, "--pull", "always")
	}
	args = append(args, service)
	return c.runCompose(deploymentPath, opts, args...)
}

func (c *ComposeExecutor) PullService(deploymentPath, service string, opts ...RunOption) (string, error) {
	return c.runCompose(deploymentPath, opts, "pull", "--ignore-buildable", "--policy", "always", service)
}

func (c *ComposeExecutor) Logs(deploymentPath string, tail int) (string, error) {
	tailStr := fmt.Sprintf("%d", tail)
	return c.runCompose(deploymentPath, nil, "logs", "--no-color", "--timestamps", "--tail", tailStr)
}

func (c *ComposeExecutor) PS(deploymentPath string) (string, error) {
	return c.runCompose(deploymentPath, nil, "ps", "--format", "json")
}

type ImageInfo struct {
	Service  string `json:"service"`
	Image    string `json:"image"`
	IsLatest bool   `json:"is_latest"`
	IsBuild  bool   `json:"is_build"`
}

func (c *ComposeExecutor) Pull(deploymentPath string, onlyLatest bool, opts ...RunOption) (string, error) {
	if onlyLatest {
		services, err := c.getLatestTaggedServices(deploymentPath)
		if err != nil || len(services) == 0 {
			return "", err
		}
		args := []string{"pull", "--ignore-buildable", "--policy", "always"}
		args = append(args, services...)
		return c.runCompose(deploymentPath, opts, args...)
	}
	return c.runCompose(deploymentPath, opts, "pull", "--ignore-buildable", "--policy", "always")
}

func (c *ComposeExecutor) GetImageInfo(deploymentPath string) ([]ImageInfo, error) {
	composePath := c.findComposeFile(deploymentPath)
	if composePath == "" {
		return nil, fmt.Errorf("no compose file found in %s", deploymentPath)
	}

	data, err := os.ReadFile(composePath)
	if err != nil {
		return nil, err
	}

	var compose struct {
		Services map[string]struct {
			Image string      `yaml:"image"`
			Build interface{} `yaml:"build"`
		} `yaml:"services"`
	}

	if err := yaml.Unmarshal(data, &compose); err != nil {
		return nil, err
	}

	var images []ImageInfo
	for name, svc := range compose.Services {
		info := ImageInfo{
			Service: name,
			Image:   svc.Image,
			IsBuild: svc.Build != nil,
		}
		if svc.Image != "" {
			info.IsLatest = isLatestTag(svc.Image)
		}
		images = append(images, info)
	}

	return images, nil
}

func (c *ComposeExecutor) getLatestTaggedServices(deploymentPath string) ([]string, error) {
	images, err := c.GetImageInfo(deploymentPath)
	if err != nil {
		return nil, err
	}

	var services []string
	for _, img := range images {
		if img.IsLatest && !img.IsBuild {
			services = append(services, img.Service)
		}
	}
	return services, nil
}

func isLatestTag(image string) bool {
	if !strings.Contains(image, ":") {
		return true
	}
	parts := strings.Split(image, ":")
	tag := parts[len(parts)-1]
	return tag == "latest"
}

func (c *ComposeExecutor) getProjectName(deploymentPath string) string {
	parts := strings.Split(strings.TrimSuffix(deploymentPath, "/"), "/")
	if len(parts) == 0 {
		return "flatrun"
	}
	dirName := parts[len(parts)-1]

	// First, try to read name from compose file
	if name := c.readComposeProjectName(deploymentPath); name != "" {
		return name
	}

	// Fallback: detect existing project from running containers
	if name := c.detectExistingProject(dirName); name != "" {
		return name
	}

	// Default to directory name for compatibility
	return dirName
}

// readComposeProjectName reads the 'name:' attribute from the compose file
func (c *ComposeExecutor) readComposeProjectName(deploymentPath string) string {
	composePath := c.findComposeFile(deploymentPath)
	if composePath == "" {
		return ""
	}

	data, err := os.ReadFile(composePath)
	if err != nil {
		return ""
	}

	var compose struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal(data, &compose); err == nil && compose.Name != "" {
		return compose.Name
	}
	return ""
}

// findComposeFile finds any compose file in the deployment directory
func (c *ComposeExecutor) findComposeFile(dirPath string) string {
	standardNames := []string{
		"docker-compose.yml",
		"docker-compose.yaml",
		"compose.yml",
		"compose.yaml",
	}

	for _, name := range standardNames {
		path := dirPath + "/" + name
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	patterns := []string{
		"*compose*.yml",
		"*compose*.yaml",
	}

	for _, pattern := range patterns {
		matches, _ := filepath.Glob(dirPath + "/" + pattern)
		if len(matches) > 0 {
			return matches[0]
		}
	}

	return ""
}

// detectExistingProject checks if containers exist with common project name patterns
func (c *ComposeExecutor) detectExistingProject(dirName string) string {
	candidates := []string{
		dirName,
		"flatrun-" + dirName,
	}

	for _, candidate := range candidates {
		cmd := exec.Command("docker", "compose", "-p", candidate, "ps", "-q")
		output, err := cmd.Output()
		if err == nil && len(strings.TrimSpace(string(output))) > 0 {
			return candidate
		}
	}
	return ""
}

// composeCommand builds the compose invocation for a deployment. ctx is what lets a caller
// stop a command that would otherwise run until it decides to finish, such as following logs.
func (c *ComposeExecutor) composeCommand(ctx context.Context, deploymentPath string, args ...string) (*exec.Cmd, error) {
	composeCmd := c.findComposeCommand()
	if composeCmd == "" {
		return nil, fmt.Errorf("docker compose command not found")
	}

	composePath := c.findComposeFile(deploymentPath)
	if composePath == "" {
		return nil, fmt.Errorf("no compose file found in %s", deploymentPath)
	}

	projectName := c.getProjectName(deploymentPath)

	var baseArgs []string
	baseArgs = append(baseArgs, "-f", composePath, "-p", projectName)

	envFile := deploymentPath + "/.env.flatrun"
	if _, err := os.Stat(envFile); err == nil {
		baseArgs = append(baseArgs, "--env-file", ".env.flatrun")
	}

	var cmd *exec.Cmd
	if composeCmd == "docker-compose" {
		fullArgs := append(baseArgs, args...)
		cmd = exec.CommandContext(ctx, composeCmd, fullArgs...)
	} else {
		fullArgs := append([]string{"compose"}, baseArgs...)
		fullArgs = append(fullArgs, args...)
		cmd = exec.CommandContext(ctx, "docker", fullArgs...)
	}

	cmd.Dir = deploymentPath
	return cmd, nil
}

func (c *ComposeExecutor) runCompose(deploymentPath string, opts []RunOption, args ...string) (string, error) {
	cmd, err := c.composeCommand(context.Background(), deploymentPath, args...)
	if err != nil {
		return "", err
	}

	var ro runOpts
	for _, opt := range opts {
		opt(&ro)
	}

	// Expose the agent's own uid/gid to compose substitution so a template can
	// run its container as the user that owns the deployment directory, keeping
	// bind-mounted data host-manageable (deletable) instead of root-owned.
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("FLATRUN_UID=%d", os.Getuid()),
		fmt.Sprintf("FLATRUN_GID=%d", os.Getgid()),
	)
	if len(ro.extraEnv) > 0 {
		cmd.Env = append(cmd.Env, ro.extraEnv...)
	}

	if ro.lineSink != nil {
		return runComposeStreaming(cmd, ro.lineSink)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return stderr.String(), fmt.Errorf("%w: %s", err, stderr.String())
	}

	return stdout.String(), nil
}

// StreamLogs follows a deployment's logs, handing each line to sink as it arrives, until ctx
// is cancelled or the deployment stops producing.
//
// Reading logs as a `--tail` blob means a viewer only knows what was true when it asked, so
// a user watching a container start reloads to see the next line. Following gives them the
// line when the container writes it, and cancelling ctx stops the process rather than
// leaving it attached for the life of the agent.
func (c *ComposeExecutor) StreamLogs(ctx context.Context, deploymentPath string, tail int, sink func(string)) error {
	if tail <= 0 {
		tail = 100
	}

	cmd, err := c.composeCommand(ctx, deploymentPath, "logs", "--follow", "--no-color", "--timestamps", "--tail", fmt.Sprintf("%d", tail))
	if err != nil {
		return err
	}

	// Compose writes container output to stdout and its own notices to stderr; a viewer
	// wants both, in the order they happened.
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return err
	}

	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		sink(scanner.Text())
	}

	// A cancelled follow is the caller closing the viewer, not a failure.
	if err := cmd.Wait(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

func runComposeStreaming(cmd *exec.Cmd, sink func(string)) (string, error) {
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}

	var mu sync.Mutex
	var combined bytes.Buffer
	var wg sync.WaitGroup
	scan := func(r io.Reader) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		// Image pull/build progress lines can be long; raise the per-line cap.
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			sink(line)
			mu.Lock()
			combined.WriteString(line)
			combined.WriteByte('\n')
			mu.Unlock()
		}
	}
	wg.Add(2)
	go scan(stdoutPipe)
	go scan(stderrPipe)
	wg.Wait()

	err = cmd.Wait()
	out := combined.String()
	if err != nil {
		return out, fmt.Errorf("%w: %s", err, out)
	}
	return out, nil
}

func (c *ComposeExecutor) findComposeCommand() string {
	if cmd, ok := c.composeCmd.Load().(string); ok && cmd != "" {
		return cmd
	}
	cmd := detectComposeCommand()
	if cmd != "" {
		c.composeCmd.Store(cmd)
	}
	return cmd
}

func detectComposeCommand() string {
	if _, err := exec.LookPath("docker"); err == nil {
		cmd := exec.Command("docker", "compose", "version")
		if err := cmd.Run(); err == nil {
			return "docker"
		}
	}

	if _, err := exec.LookPath("docker-compose"); err == nil {
		return "docker-compose"
	}

	return ""
}

func (c *ComposeExecutor) GetStatus(deploymentPath string) (string, error) {
	output, err := c.PS(deploymentPath)
	if err != nil {
		if strings.Contains(err.Error(), "no such service") ||
			strings.Contains(err.Error(), "no configuration file") {
			return "stopped", nil
		}
		return "error", err
	}

	trimmed := strings.TrimSpace(output)
	if trimmed == "" || trimmed == "[]" {
		return "stopped", nil
	}

	// Check for running state in various formats from docker compose ps
	lower := strings.ToLower(output)
	if strings.Contains(lower, "\"state\":\"running\"") ||
		strings.Contains(lower, "\"state\": \"running\"") ||
		strings.Contains(lower, "running") ||
		strings.Contains(lower, "\"status\":\"up") ||
		strings.Contains(lower, "\"status\": \"up") {
		return "running", nil
	}

	// Check for exited/stopped states
	if strings.Contains(lower, "exited") ||
		strings.Contains(lower, "\"state\":\"exited\"") ||
		strings.Contains(lower, "\"state\": \"exited\"") {
		return "stopped", nil
	}

	return "unknown", nil
}

func (c *ComposeExecutor) ExecCommand(containerID string, command string) (string, error) {
	shells := []string{"/bin/bash", "/bin/sh", "bash", "sh"}

	for _, shell := range shells {
		args := []string{"exec", containerID, shell, "-c", command}
		cmd := exec.Command("docker", args...)

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		output := stdout.String() + stderr.String()

		if err != nil {
			// Only try next shell if the shell itself wasn't found
			lowerOutput := strings.ToLower(output)
			shellNotFound := strings.Contains(lowerOutput, "oci runtime exec failed") ||
				strings.Contains(lowerOutput, fmt.Sprintf("%s: not found", shell)) ||
				strings.Contains(lowerOutput, fmt.Sprintf("%s: no such file", shell)) ||
				strings.Contains(lowerOutput, "executable file not found in $path")
			if shellNotFound {
				continue
			}
			return output, fmt.Errorf("%w: %s", err, output)
		}

		return output, nil
	}

	return "", fmt.Errorf("no compatible shell found in container")
}

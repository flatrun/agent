package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/templates"
)

// TestNginxAccessLineNamesItsDeployment runs the real base config in nginx, makes a real
// request through it, and matches the line it wrote. Reading the proxy's log per deployment
// only works if the host survives into the line, which is a property of nginx's own
// formatting rather than anything this package can assert on its own.
func TestNginxAccessLineNamesItsDeployment(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real container")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker unavailable")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("docker daemon unreachable: %v", err)
	}

	conf, err := templates.GetNginxConfigWithData(false, templates.NginxConfigData{})
	if err != nil {
		t.Fatalf("rendering the base config failed: %v", err)
	}

	confDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(confDir, "nginx.conf"), conf, 0644); err != nil {
		t.Fatal(err)
	}
	siteDir := filepath.Join(confDir, "conf.d")
	if err := os.MkdirAll(siteDir, 0755); err != nil {
		t.Fatal(err)
	}
	// The base config includes a rate limit file and whatever vhosts exist, so both have to
	// be there for nginx to start at all.
	if err := os.WriteFile(filepath.Join(siteDir, "rate_limits.conf"), []byte("# none\n"), 0644); err != nil {
		t.Fatal(err)
	}
	site := `server {
    listen 80 default_server;
    server_name _;
    location / { return 200 "ok"; }
}
`
	if err := os.WriteFile(filepath.Join(siteDir, "site.conf"), []byte(site), 0644); err != nil {
		t.Fatal(err)
	}

	const container = "flatrun-accesslog-integration"
	_ = exec.Command("docker", "rm", "-f", container).Run()
	run := exec.Command("docker", "run", "-d", "--name", container,
		"-v", filepath.Join(confDir, "nginx.conf")+":/etc/nginx/nginx.conf:ro",
		"-v", siteDir+":/etc/nginx/conf.d:ro",
		"nginx:alpine")
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("starting nginx failed: %v: %s", err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command("docker", "rm", "-f", container).CombinedOutput(); err != nil {
			t.Logf("cleanup: %v: %s", err, out)
		}
	})

	const domain = "shop.example.test"
	req := exec.Command("docker", "exec", container,
		"wget", "-qO-", "--header", "Host: "+domain, "http://127.0.0.1/")
	if out, err := req.CombinedOutput(); err != nil {
		logs, _ := exec.Command("docker", "logs", container).CombinedOutput()
		t.Fatalf("request through nginx failed: %v: %s (container: %s)", err, out, logs)
	}

	// The access log is buffered and flushed on a timer, so the line is not there the moment
	// the request returns. The agent reads these logs with docker's timestamps in front, so
	// that is the shape the matcher has to cope with.
	readAccessLine := func(args ...string) string {
		var last []byte
		for attempt := 0; attempt < 20; attempt++ {
			out, err := exec.Command("docker", append([]string{"logs"}, append(args, container)...)...).CombinedOutput()
			if err != nil {
				t.Fatalf("reading nginx logs failed: %v: %s", err, out)
			}
			last = out
			for _, line := range strings.Split(string(out), "\n") {
				if strings.Contains(line, "GET /") {
					return line
				}
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("nginx wrote no access line: %s", last)
		return ""
	}

	stamped := readAccessLine("--timestamps")
	accessLine := readAccessLine()

	// A deployment serving that domain, as the agent reads it off disk.
	base := t.TempDir()
	dir := filepath.Join(base, "shop")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("name: shop\nservices:\n  web:\n    image: nginx:alpine\n"), 0644); err != nil {
		t.Fatal(err)
	}
	metadata := "domains:\n  - domain: " + domain + "\n"
	if err := os.WriteFile(filepath.Join(dir, "service.yml"), []byte(metadata), 0644); err != nil {
		t.Fatal(err)
	}

	server := &Server{manager: docker.NewManager(base)}
	matches, err := server.deploymentHostMatcher("shop")
	if err != nil {
		t.Fatalf("building the matcher failed: %v", err)
	}
	if !matches(accessLine) {
		t.Errorf("the deployment's own access line was filtered out: %q", accessLine)
	}
	if !matches(stamped) {
		t.Errorf("the deployment's own access line was filtered out once timestamped: %q", stamped)
	}

	// A second deployment on another domain must not see it.
	other := filepath.Join(base, "blog")
	if err := os.MkdirAll(other, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "docker-compose.yml"), []byte("name: blog\nservices:\n  web:\n    image: nginx:alpine\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "service.yml"), []byte("domains:\n  - domain: blog.example.test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	blogMatches, err := server.deploymentHostMatcher("blog")
	if err != nil {
		t.Fatalf("building the matcher failed: %v", err)
	}
	if blogMatches(accessLine) || blogMatches(stamped) {
		t.Errorf("another deployment's access line was let through: %q", accessLine)
	}
}

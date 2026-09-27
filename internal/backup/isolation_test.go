package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsolateRestoredDeploymentRemovesExternalConnectivity(t *testing.T) {
	dir := t.TempDir()
	compose := `services:
  app:
    image: example/app
    container_name: production-app
    network_mode: host
    ports:
      - "8080:80"
    extra_hosts:
      - "host.docker.internal:host-gateway"
  db:
    image: postgres:16
networks:
  production:
    external: true
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatal(err)
	}
	if err := isolateRestoredDeployment(dir); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(content)
	for _, forbidden := range []string{"container_name:", "network_mode:", "ports:", "extra_hosts:", "external:"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("isolated compose still contains %q:\n%s", forbidden, got)
		}
	}
	if !strings.Contains(got, "internal: true") || strings.Count(got, "flatrun_isolated") < 3 {
		t.Fatalf("isolated network is missing:\n%s", got)
	}
}

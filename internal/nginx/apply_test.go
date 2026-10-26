package nginx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/flatrun/agent/pkg/config"
)

func applyTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	m := NewManager(&config.NginxConfig{ConfigPath: dir}, t.TempDir(), "")
	return m, dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestApplyVirtualHostKeepsNewConfigWhenNginxAccepts(t *testing.T) {
	m, dir := applyTestManager(t)
	reloads := 0
	m.testConfig = func() error { return nil }
	m.reload = func() error { reloads++; return nil }

	if err := m.ApplyVirtualHost("shop", "server { listen 80; }"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "shop.conf")); got != "server { listen 80; }" {
		t.Fatalf("vhost = %q", got)
	}
	if reloads != 1 {
		t.Fatalf("reloads = %d, want 1", reloads)
	}
}

func TestApplyVirtualHostRestoresPreviousConfigWhenTestFails(t *testing.T) {
	m, dir := applyTestManager(t)
	path := filepath.Join(dir, "shop.conf")
	if err := os.WriteFile(path, []byte("working"), 0644); err != nil {
		t.Fatal(err)
	}
	reloads := 0
	m.testConfig = func() error { return errors.New("nginx: [emerg] unknown directive") }
	m.reload = func() error { reloads++; return nil }

	err := m.ApplyVirtualHost("shop", "broken")
	if err == nil {
		t.Fatal("expected the failed nginx test to be returned")
	}
	if got := readFile(t, path); got != "working" {
		t.Fatalf("vhost = %q, want the previous working config", got)
	}
	if reloads != 0 {
		t.Fatalf("reloads = %d, a rejected config must not be reloaded", reloads)
	}
}

func TestApplyVirtualHostRemovesNewFileWhenTestFails(t *testing.T) {
	m, dir := applyTestManager(t)
	m.testConfig = func() error { return errors.New("invalid") }
	m.reload = func() error { return nil }

	if err := m.ApplyVirtualHost("shop", "broken"); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := os.Stat(filepath.Join(dir, "shop.conf")); !os.IsNotExist(err) {
		t.Fatalf("a rejected new vhost must not stay in conf.d, stat err = %v", err)
	}
}

func TestApplyVirtualHostRestoresAndReloadsWhenReloadFails(t *testing.T) {
	m, dir := applyTestManager(t)
	path := filepath.Join(dir, "shop.conf")
	if err := os.WriteFile(path, []byte("working"), 0644); err != nil {
		t.Fatal(err)
	}
	m.testConfig = func() error { return nil }
	reloads := 0
	m.reload = func() error {
		reloads++
		if reloads == 1 {
			return errors.New("reload failed")
		}
		return nil
	}

	if err := m.ApplyVirtualHost("shop", "new"); err == nil {
		t.Fatal("expected the reload error")
	}
	if got := readFile(t, path); got != "working" {
		t.Fatalf("vhost = %q, want the previous working config", got)
	}
	if reloads != 2 {
		t.Fatalf("reloads = %d, want a second reload on the restored config", reloads)
	}
}

func TestRemoveVirtualHostRestoresFilesWhenTestFails(t *testing.T) {
	m, dir := applyTestManager(t)
	path := filepath.Join(dir, "shop.conf")
	limits := filepath.Join(dir, "rate_limits.conf")
	if err := os.WriteFile(path, []byte("working"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(limits, []byte("# Deployment: shop\nlimit_req_zone x;\n"), 0644); err != nil {
		t.Fatal(err)
	}
	m.testConfig = func() error { return errors.New("invalid") }
	m.reload = func() error { return nil }

	if err := m.RemoveVirtualHost("shop"); err == nil {
		t.Fatal("expected an error")
	}
	if got := readFile(t, path); got != "working" {
		t.Fatalf("vhost = %q", got)
	}
	if got := readFile(t, limits); got != "# Deployment: shop\nlimit_req_zone x;\n" {
		t.Fatalf("rate limits = %q", got)
	}
}

func TestRemoveVirtualHostWithoutVhostSkipsNginx(t *testing.T) {
	m, _ := applyTestManager(t)
	m.testConfig = func() error { t.Fatal("nginx must not be tested when nothing changes"); return nil }
	m.reload = func() error { t.Fatal("nginx must not be reloaded when nothing changes"); return nil }

	if err := m.RemoveVirtualHost("shop"); err != nil {
		t.Fatal(err)
	}
}

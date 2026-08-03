package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
)

func TestDetectBackupDatabases_DBServerGlobalDump(t *testing.T) {
	dir := t.TempDir()
	srv := &Server{config: &config.Config{DeploymentsPath: dir}}

	name := "shared-db"
	dep := filepath.Join(dir, name)
	if err := os.MkdirAll(dep, 0o755); err != nil {
		t.Fatal(err)
	}
	compose := "name: shared-db\nservices:\n  postgres:\n    image: postgres:16\n    environment:\n      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}\n"
	os.WriteFile(filepath.Join(dep, "docker-compose.yml"), []byte(compose), 0o644)
	os.WriteFile(filepath.Join(dep, ".env"), []byte("POSTGRES_PASSWORD=topsecret\n"), 0o600)

	specs := srv.detectBackupDatabases(&models.Deployment{Name: name, Path: dep})
	if len(specs) != 1 {
		t.Fatalf("want 1 detected db, got %d: %#v", len(specs), specs)
	}
	s := specs[0]
	if s.Type != "postgres" || !s.AllDatabases || s.User != "postgres" || s.Password != "topsecret" || s.Container == "" {
		t.Fatalf("unexpected detected spec: %#v", s)
	}
}

func TestDbTypeFromImage(t *testing.T) {
	cases := map[string]string{
		"postgres:16": "postgres", "postgis/postgis": "postgres",
		"mariadb:11": "mysql", "mysql:8": "mysql", "redis:7": "", "nginx": "",
	}
	for img, want := range cases {
		if got := dbTypeFromImage(img); got != want {
			t.Errorf("%s: got %q want %q", img, got, want)
		}
	}
}

func TestDetectBackupDatabases_SharedAppSlice(t *testing.T) {
	dir := t.TempDir()
	srv := &Server{config: &config.Config{
		DeploymentsPath: dir,
		Infrastructure: config.InfrastructureConfig{Database: config.SharedDatabaseConfig{
			Enabled: true, Type: "postgres", Container: "shared-pg", RootUser: "postgres", RootPassword: "rootpw",
		}},
	}}
	name := "wordpress"
	dep := filepath.Join(dir, name)
	os.MkdirAll(dep, 0o755)
	os.WriteFile(filepath.Join(dep, "docker-compose.yml"), []byte("services:\n  app:\n    image: wordpress:6\n"), 0o644)
	os.WriteFile(filepath.Join(dep, ".env"), []byte("DB_DATABASE=wordpress_db\nDB_PASSWORD=apppw\n"), 0o600)

	specs := srv.detectBackupDatabases(&models.Deployment{
		Name: name, Path: dep,
		Metadata: &models.ServiceMetadata{Databases: []models.DatabaseConfig{{Alias: "primary", Type: "postgres", Mode: "shared", IsShared: true}}},
	})
	if len(specs) != 1 {
		t.Fatalf("want 1 slice, got %d: %#v", len(specs), specs)
	}
	s := specs[0]
	if s.Container != "shared-pg" || s.Database != "wordpress_db" || s.User != "postgres" || s.Password != "rootpw" || s.AllDatabases {
		t.Fatalf("unexpected slice: %#v", s)
	}
}

func TestEffectiveBackupSpec_ExcludesDBDataDir(t *testing.T) {
	dir := t.TempDir()
	srv := &Server{config: &config.Config{DeploymentsPath: dir}}
	name := "pg"
	dep := filepath.Join(dir, name)
	os.MkdirAll(dep, 0o755)
	compose := "name: pg\nservices:\n  postgres:\n    image: postgres:16\n    volumes:\n      - ./data:/var/lib/postgresql/data\n    environment:\n      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}\n"
	os.WriteFile(filepath.Join(dep, "docker-compose.yml"), []byte(compose), 0o644)
	os.WriteFile(filepath.Join(dep, ".env"), []byte("POSTGRES_PASSWORD=pw\n"), 0o600)

	spec := srv.effectiveBackupSpec(&models.Deployment{Name: name, Path: dep})
	if spec == nil || len(spec.Databases) != 1 {
		t.Fatalf("expected a detected database: %#v", spec)
	}
	found := false
	for _, e := range spec.ExcludePatterns {
		if e == "data" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected 'data' excluded, got %#v", spec.ExcludePatterns)
	}
}

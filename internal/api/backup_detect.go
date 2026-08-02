package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flatrun/agent/internal/backup"
	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/pkg/models"
)

// effectiveBackupSpec returns the deployment's configured backup spec, or, when
// no databases are configured, one with databases auto-detected from the
// deployment. This is what makes a database-server deployment (including the
// shared infrastructure database) get a dump without any manual setup.
func (s *Server) effectiveBackupSpec(d *models.Deployment) *backup.BackupSpec {
	var spec *backup.BackupSpec
	if d.Metadata != nil && d.Metadata.Backup != nil {
		spec = d.Metadata.Backup
	}
	if spec != nil && len(spec.Databases) > 0 {
		return spec
	}

	detected := s.detectBackupDatabases(d)
	if len(detected) == 0 {
		return spec
	}

	out := backup.BackupSpec{}
	if spec != nil {
		out = *spec
	}
	out.Databases = detected
	return &out
}

// detectBackupDatabases finds database-server services in a deployment's compose
// and returns a spec to dump each in full (pg_dumpall / --all-databases) using
// its root credentials. A shared database deployment thus backs up every app's
// data in one dump; a standalone or sidecar database backs up its own.
func (s *Server) detectBackupDatabases(d *models.Deployment) []models.DatabaseBackupSpec {
	content, err := os.ReadFile(filepath.Join(d.Path, "docker-compose.yml"))
	if err != nil {
		return nil
	}
	compose, err := docker.ParseComposeYAML(string(content))
	if err != nil {
		return nil
	}
	services, ok := compose["services"].(map[string]interface{})
	if !ok {
		return nil
	}

	env, _ := s.readDeploymentEnvMap(d.Name)

	var specs []models.DatabaseBackupSpec
	for name, raw := range services {
		svc, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		image, _ := svc["image"].(string)
		dbType := dbTypeFromImage(image)
		if dbType == "" {
			continue
		}
		user, password := dbRootCreds(dbType, svc, env)
		specs = append(specs, models.DatabaseBackupSpec{
			Service:      name,
			Type:         dbType,
			Container:    docker.ContainerNameForService(string(content), d.Name, name),
			AllDatabases: true,
			User:         user,
			Password:     password,
		})
	}
	return specs
}

func dbTypeFromImage(image string) string {
	img := strings.ToLower(image)
	switch {
	case strings.Contains(img, "postgres") || strings.Contains(img, "postgis"):
		return "postgres"
	case strings.Contains(img, "mariadb") || strings.Contains(img, "mysql") || strings.Contains(img, "percona"):
		return "mysql"
	}
	return ""
}

func dbRootCreds(dbType string, svc map[string]interface{}, env map[string]string) (user, password string) {
	switch dbType {
	case "postgres":
		user = serviceEnvValue(svc, env, "POSTGRES_USER")
		if user == "" {
			user = "postgres"
		}
		password = serviceEnvValue(svc, env, "POSTGRES_PASSWORD")
	case "mysql":
		user = "root"
		password = serviceEnvValue(svc, env, "MYSQL_ROOT_PASSWORD")
	}
	return user, password
}

// serviceEnvValue resolves an env var for a service: the deployment's env file
// wins, then the service's own environment block (resolving a ${VAR} reference
// back against the env file).
func serviceEnvValue(svc map[string]interface{}, env map[string]string, key string) string {
	if v, ok := env[key]; ok && v != "" {
		return v
	}
	switch e := svc["environment"].(type) {
	case map[string]interface{}:
		if raw, ok := e[key]; ok {
			return resolveEnvRef(fmt.Sprint(raw), env)
		}
	case []interface{}:
		for _, item := range e {
			str, ok := item.(string)
			if !ok {
				continue
			}
			if k, val, found := strings.Cut(str, "="); found && k == key {
				return resolveEnvRef(val, env)
			}
		}
	}
	return ""
}

func resolveEnvRef(v string, env map[string]string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") {
		inner := v[2 : len(v)-1]
		name, def := inner, ""
		if i := strings.Index(inner, ":-"); i >= 0 {
			name, def = inner[:i], inner[i+2:]
		}
		if val, ok := env[name]; ok && val != "" {
			return val
		}
		return def
	}
	return v
}

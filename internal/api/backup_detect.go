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
	// A database server's live data directory is captured by its dump, so drop
	// it from the file copy: it is redundant and, taken hot, potentially
	// inconsistent. Other data (app files, config) is still copied.
	out.ExcludePatterns = mergeUnique(out.ExcludePatterns, s.dbServerDataDirs(d))
	return &out
}

// dbServerDataDirs returns the bind-mount directory names of the deployment's
// own database services, so the live database files can be excluded from the
// file backup in favour of the logical dump.
func (s *Server) dbServerDataDirs(d *models.Deployment) []string {
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

	seen := map[string]bool{}
	var dirs []string
	for _, raw := range services {
		svc, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		image, _ := svc["image"].(string)
		if dbTypeFromImage(image) == "" {
			continue
		}
		for _, host := range serviceBindHostPaths(svc) {
			base := filepath.Base(host)
			if base == "." || base == "/" || base == "" || seen[base] {
				continue
			}
			seen[base] = true
			dirs = append(dirs, base)
		}
	}
	return dirs
}

func serviceBindHostPaths(svc map[string]interface{}) []string {
	vols, ok := svc["volumes"].([]interface{})
	if !ok {
		return nil
	}
	var out []string
	for _, v := range vols {
		switch vv := v.(type) {
		case string:
			host := vv
			if i := strings.Index(vv, ":"); i >= 0 {
				host = vv[:i]
			}
			if strings.HasPrefix(host, ".") || strings.Contains(host, "/") {
				out = append(out, host)
			}
		case map[string]interface{}:
			if t, _ := vv["type"].(string); t == "bind" {
				if src, _ := vv["source"].(string); src != "" {
					out = append(out, src)
				}
			}
		}
	}
	return out
}

func mergeUnique(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]string{a, b} {
		for _, s := range list {
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
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

	// Per-app slice: a deployment using the shared database dumps just its own
	// database from the shared server, so its (typically more frequent) backups
	// are self-contained rather than depending on the shared database's own,
	// possibly less frequent, global backup.
	specs = append(specs, s.detectSharedDatabaseSlices(d, env)...)
	return specs
}

func (s *Server) detectSharedDatabaseSlices(d *models.Deployment, env map[string]string) []models.DatabaseBackupSpec {
	if d.Metadata == nil {
		return nil
	}
	shared := s.config.Infrastructure.Database
	if shared.Container == "" {
		return nil
	}

	var specs []models.DatabaseBackupSpec
	for _, dbc := range d.Metadata.Databases {
		if !dbc.IsShared && dbc.Mode != "shared" {
			continue
		}
		dbName := ""
		if dbc.DatabaseName != "" {
			dbName = dbc.DatabaseName
		} else if dbc.EnvPrefix != "" {
			dbName = env[dbc.EnvPrefix+"_DATABASE"]
		}
		if dbName == "" {
			dbName = env["DB_DATABASE"]
		}
		if dbName == "" {
			continue
		}
		dbType := normalizeDBType(dbc.Type)
		if dbType == "" {
			dbType = normalizeDBType(shared.Type)
		}
		if dbType == "" {
			continue
		}
		label := dbc.Alias
		if label == "" {
			label = "app"
		}
		specs = append(specs, models.DatabaseBackupSpec{
			Service:   label,
			Type:      dbType,
			Container: shared.Container,
			Database:  dbName,
			User:      shared.RootUser,
			Password:  shared.RootPassword,
		})
	}
	return specs
}

func normalizeDBType(t string) string {
	switch strings.ToLower(t) {
	case "mysql", "mariadb":
		return "mysql"
	case "postgres", "postgresql":
		return "postgres"
	}
	return ""
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

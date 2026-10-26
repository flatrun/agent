package nginx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flatrun/agent/pkg/models"
)

// htpasswdDir sits under conf.d, which the proxy container already mounts.
// nginx only includes *.conf from there, so these files are never parsed as
// configuration.
const htpasswdDir = "htpasswd"

func (m *Manager) basicAuthFile(deployment string, d models.DomainConfig) string {
	if d.BasicAuth == nil || !d.BasicAuth.Enabled {
		return ""
	}
	id := safeLocationName(d.ID)
	if id == "" {
		return ""
	}
	if m.config.External {
		return filepath.Join(m.configPath, htpasswdDir, deployment, id)
	}
	return "/etc/nginx/conf.d/" + htpasswdDir + "/" + deployment + "/" + id
}

func (m *Manager) htpasswdPath(deployment, domainID string) string {
	return filepath.Join(m.configPath, htpasswdDir, deployment, domainID)
}

// basicAuthFiles returns the htpasswd file content for every domain of the
// deployment with basic auth on, keyed by the path on the agent's side.
func (m *Manager) basicAuthFiles(deployment *models.Deployment) map[string][]byte {
	files := map[string][]byte{}
	if deployment.Metadata == nil {
		return files
	}
	for _, d := range deployment.Metadata.GetDomains() {
		if m.basicAuthFile(deployment.Name, d) == "" {
			continue
		}
		var b strings.Builder
		for _, u := range d.BasicAuth.Users {
			if u.Username == "" || u.PasswordHash == "" {
				continue
			}
			fmt.Fprintf(&b, "%s:%s\n", u.Username, u.PasswordHash)
		}
		files[m.htpasswdPath(deployment.Name, safeLocationName(d.ID))] = []byte(b.String())
	}
	return files
}

// existingBasicAuthFiles lists the htpasswd files currently on disk for a
// deployment, so stale ones are snapshotted and removed with the vhost.
func (m *Manager) existingBasicAuthFiles(deployment string) []string {
	entries, err := os.ReadDir(filepath.Join(m.configPath, htpasswdDir, deployment))
	if err != nil {
		return nil
	}
	var paths []string
	for _, e := range entries {
		if !e.IsDir() {
			paths = append(paths, filepath.Join(m.configPath, htpasswdDir, deployment, e.Name()))
		}
	}
	return paths
}

// writeBasicAuthFiles writes the wanted htpasswd files and deletes any other
// file left in the deployment's directory. The files hold crypt hashes only
// and must be readable by the nginx worker user, hence 0644.
func writeBasicAuthFiles(wanted map[string][]byte, existing []string) error {
	for path, content := range wanted {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(path, content, 0644); err != nil {
			return err
		}
	}
	for _, path := range existing {
		if _, keep := wanted[path]; keep {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

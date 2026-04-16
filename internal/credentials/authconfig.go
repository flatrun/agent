package credentials

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type AuthEntry struct {
	Registry string
	Username string
	Password string
}

type AuthConfig struct {
	dir string
}

func (a AuthConfig) Dir() string {
	return a.dir
}

func (a AuthConfig) Close() {
	if a.dir == "" {
		return
	}
	_ = os.RemoveAll(a.dir)
}

type dockerAuthEntry struct {
	Auth string `json:"auth"`
}

type dockerAuthFile struct {
	Auths map[string]dockerAuthEntry `json:"auths"`
}

const dockerHubAuthKey = "https://index.docker.io/v1/"

func (m *Manager) BuildAuthConfig(credentialIDs []string, extras ...AuthEntry) (AuthConfig, error) {
	auths := map[string]dockerAuthEntry{}

	m.mu.RLock()
	seen := map[string]bool{}
	for _, id := range credentialIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		cred, ok := m.credentials[id]
		if !ok {
			continue
		}
		host := cred.RegistryURL
		if host == "" {
			if rt, ok := m.registryTypes[cred.RegistryTypeSlug]; ok && len(rt.URLPatterns) > 0 {
				if isFullHostname(rt.URLPatterns[0]) {
					host = rt.URLPatterns[0]
				}
			}
		}
		if host == "" {
			continue
		}
		addAuth(auths, host, cred.Username, cred.Password)
	}
	m.mu.RUnlock()

	for _, e := range extras {
		if e.Username == "" || e.Password == "" {
			continue
		}
		host := e.Registry
		if host == "" {
			host = "docker.io"
		}
		addAuth(auths, host, e.Username, e.Password)
	}

	if len(auths) == 0 {
		return AuthConfig{}, nil
	}

	dir, err := os.MkdirTemp("", "flatrun-docker-auth-*")
	if err != nil {
		return AuthConfig{}, fmt.Errorf("create auth dir: %w", err)
	}
	data, err := json.Marshal(dockerAuthFile{Auths: auths})
	if err != nil {
		_ = os.RemoveAll(dir)
		return AuthConfig{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0600); err != nil {
		_ = os.RemoveAll(dir)
		return AuthConfig{}, err
	}
	return AuthConfig{dir: dir}, nil
}

func addAuth(auths map[string]dockerAuthEntry, host, username, password string) {
	token := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	if host == "docker.io" || host == "index.docker.io" || host == "registry-1.docker.io" {
		auths[dockerHubAuthKey] = dockerAuthEntry{Auth: token}
		return
	}
	auths[host] = dockerAuthEntry{Auth: token}
}

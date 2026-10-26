package nginx

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
	"strings"
)

const hookTokenHeader = "X-Flatrun-Hook"

// HookTokenHeader is the header nginx sends with the agent hooks it calls,
// carrying HookToken.
func HookTokenHeader() string { return hookTokenHeader }

// HookToken is the key nginx sends to the agent's hooks, so a request from
// anywhere else cannot record events against a deployment. It is kept in
// .flatrun/hook.key so vhosts written before a restart keep matching.
func (m *Manager) HookToken() string {
	m.hookTokenOnce.Do(func() {
		path := filepath.Join(m.basePath, ".flatrun", "hook.key")
		if data, err := os.ReadFile(path); err == nil {
			if token := strings.TrimSpace(string(data)); len(token) == 64 {
				m.hookToken = token
				return
			}
		}
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			log.Printf("warning: cannot create the nginx hook key: %v", err)
			return
		}
		token := hex.EncodeToString(buf)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
			if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
				log.Printf("warning: cannot save the nginx hook key: %v", err)
			}
		}
		m.hookToken = token
	})
	return m.hookToken
}

// ValidHookToken reports whether a hook request carries the key nginx sends.
func (m *Manager) ValidHookToken(got string) bool {
	want := m.HookToken()
	return want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

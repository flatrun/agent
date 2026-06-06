package security

import (
	"net/netip"
	"strings"
)

// IsRequestWhitelisted reports whether a request's source IP or path matches
// any whitelist entry. IP entries match exactly, CIDR entries match contained
// addresses, and path entries match by prefix.
func (m *Manager) IsRequestWhitelisted(ip, path string) (bool, error) {
	entries, err := m.db.GetWhitelist()
	if err != nil {
		return false, err
	}

	addr, addrErr := netip.ParseAddr(ip)

	for _, entry := range entries {
		switch entry.Type {
		case "ip":
			if ip != "" && entry.Value == ip {
				return true, nil
			}
		case "cidr":
			if addrErr != nil {
				continue
			}
			if prefix, err := netip.ParsePrefix(entry.Value); err == nil && prefix.Contains(addr) {
				return true, nil
			}
		case "path":
			if path != "" && strings.HasPrefix(path, entry.Value) {
				return true, nil
			}
		}
	}

	return false, nil
}

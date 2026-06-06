package security

import (
	"net/netip"
	"strings"
)

type whitelistCache struct {
	ips      map[string]struct{}
	prefixes []netip.Prefix
	paths    []string
}

func (m *Manager) whitelistCacheLoad() (*whitelistCache, error) {
	m.wlMu.RLock()
	cache := m.wlCache
	m.wlMu.RUnlock()
	if cache != nil {
		return cache, nil
	}

	entries, err := m.db.GetWhitelist()
	if err != nil {
		return nil, err
	}

	cache = &whitelistCache{ips: make(map[string]struct{})}
	for _, entry := range entries {
		switch entry.Type {
		case "ip":
			cache.ips[entry.Value] = struct{}{}
		case "cidr":
			if prefix, err := netip.ParsePrefix(entry.Value); err == nil {
				cache.prefixes = append(cache.prefixes, prefix)
			}
		case "path":
			cache.paths = append(cache.paths, entry.Value)
		}
	}

	m.wlMu.Lock()
	m.wlCache = cache
	m.wlMu.Unlock()
	return cache, nil
}

func (m *Manager) invalidateWhitelistCache() {
	m.wlMu.Lock()
	m.wlCache = nil
	m.wlMu.Unlock()
}

// IsRequestWhitelisted reports whether a request's source IP or path matches
// any whitelist entry. IP entries match exactly, CIDR entries match contained
// addresses, and path entries match by prefix.
func (m *Manager) IsRequestWhitelisted(ip, path string) (bool, error) {
	cache, err := m.whitelistCacheLoad()
	if err != nil {
		return false, err
	}

	if ip != "" {
		if _, ok := cache.ips[ip]; ok {
			return true, nil
		}
		if addr, err := netip.ParseAddr(ip); err == nil {
			for _, prefix := range cache.prefixes {
				if prefix.Contains(addr) {
					return true, nil
				}
			}
		}
	}

	if path != "" {
		for _, p := range cache.paths {
			if strings.HasPrefix(path, p) {
				return true, nil
			}
		}
	}

	return false, nil
}

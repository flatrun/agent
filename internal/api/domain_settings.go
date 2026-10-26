package api

import (
	"net"
	"net/http"
	"regexp"
	"strings"

	"github.com/flatrun/agent/pkg/models"
)

const (
	maxResponseHeaders = 30
	maxAllowIPs        = 100
	maxBasicAuthUsers  = 20
)

var (
	headerNamePattern    = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	basicAuthUserPattern = regexp.MustCompile(`^[A-Za-z0-9._@+-]{1,64}$`)
	// Formats the system crypt() of both musl and glibc nginx images accepts.
	basicAuthHashPattern = regexp.MustCompile(`^\$(6|5|apr1)\$[./0-9A-Za-z$=]{8,200}$`)
)

// Headers the proxy itself manages; setting them per domain would break
// framing or connection handling.
var reservedResponseHeaders = map[string]bool{
	"connection":        true,
	"content-length":    true,
	"content-type":      true,
	"keep-alive":        true,
	"transfer-encoding": true,
	"upgrade":           true,
}

// normalizeDomainSettings checks the proxy settings a domain renders into
// its nginx config, so a bad value is refused before anything is written.
// It also replaces each basic auth password with its hash, so the password
// itself is never saved, planned or echoed back.
func normalizeDomainSettings(domain *models.DomainConfig) error {
	if err := models.ValidateBodySize(domain.ClientMaxBodySize); err != nil {
		return apiErrf(http.StatusBadRequest, "Maximum upload request size: %s", err.Error())
	}
	if domain.ProxyTimeout < 0 || domain.ProxyTimeout > 86400 {
		return apiErrf(http.StatusBadRequest, "Proxy timeout must be from 1 to 86400 seconds")
	}
	if domain.CanonicalRedirect != "" && domain.CanonicalRedirect != models.CanonicalRedirectPrimary {
		return apiErrf(http.StatusBadRequest, "Canonical redirect must be empty or %q", models.CanonicalRedirectPrimary)
	}
	if err := validateResponseHeaders(domain.ResponseHeaders); err != nil {
		return err
	}
	if len(domain.AllowIPs) > maxAllowIPs {
		return apiErrf(http.StatusBadRequest, "At most %d allowed addresses can be listed", maxAllowIPs)
	}
	for i, entry := range domain.AllowIPs {
		entry = strings.TrimSpace(entry)
		if net.ParseIP(entry) == nil {
			if _, _, err := net.ParseCIDR(entry); err != nil {
				return apiErrf(http.StatusBadRequest, "Allowed address %q is not an IP address or CIDR range", entry)
			}
		}
		domain.AllowIPs[i] = entry
	}
	return normalizeBasicAuth(domain.BasicAuth)
}

func validateResponseHeaders(headers map[string]string) error {
	if len(headers) > maxResponseHeaders {
		return apiErrf(http.StatusBadRequest, "At most %d response headers can be set", maxResponseHeaders)
	}
	for name, value := range headers {
		if !headerNamePattern.MatchString(name) {
			return apiErrf(http.StatusBadRequest, "Response header name %q may contain only letters, digits and dashes", name)
		}
		if reservedResponseHeaders[strings.ToLower(name)] {
			return apiErrf(http.StatusBadRequest, "Response header %s is managed by the proxy and cannot be set", name)
		}
		if len(value) > 2048 {
			return apiErrf(http.StatusBadRequest, "Response header %s is longer than 2048 characters", name)
		}
		// nginx would read $ as a variable reference and has no escape for it.
		if strings.ContainsAny(value, "$") {
			return apiErrf(http.StatusBadRequest, "Response header %s cannot contain $", name)
		}
		for _, r := range value {
			if r < 0x20 || r == 0x7f {
				return apiErrf(http.StatusBadRequest, "Response header %s contains a control character", name)
			}
		}
	}
	return nil
}

func normalizeBasicAuth(auth *models.BasicAuthConfig) error {
	if auth == nil {
		return nil
	}
	if len(auth.Users) > maxBasicAuthUsers {
		return apiErrf(http.StatusBadRequest, "At most %d basic auth users can be set", maxBasicAuthUsers)
	}
	if auth.Enabled && len(auth.Users) == 0 {
		return apiErrf(http.StatusBadRequest, "Basic auth needs at least one user")
	}
	seen := map[string]bool{}
	for i := range auth.Users {
		user := &auth.Users[i]
		if !basicAuthUserPattern.MatchString(user.Username) {
			return apiErrf(http.StatusBadRequest, "Basic auth username %q may contain only letters, digits and . _ @ + -", user.Username)
		}
		if seen[user.Username] {
			return apiErrf(http.StatusBadRequest, "Basic auth user %s is listed twice", user.Username)
		}
		seen[user.Username] = true
		if user.Password != "" {
			if len(user.Password) > 256 {
				return apiErrf(http.StatusBadRequest, "The password for %s is longer than 256 characters", user.Username)
			}
			hash, err := models.HashBasicAuthPassword(user.Password)
			if err != nil {
				return apiErrf(http.StatusInternalServerError, "Failed to hash the password for %s", user.Username)
			}
			user.PasswordHash = hash
			user.Password = ""
		}
		if user.PasswordHash == "" {
			return apiErrf(http.StatusBadRequest, "Set a password for basic auth user %s", user.Username)
		}
		if !basicAuthHashPattern.MatchString(user.PasswordHash) {
			return apiErrf(http.StatusBadRequest, "The password hash for %s is not a SHA-512, SHA-256 or apr1 crypt hash", user.Username)
		}
	}
	return nil
}

// keepBasicAuthHashes lets a client resend a domain's users without their
// passwords: a user sent with neither a password nor a hash keeps the hash
// stored for the same username on the same domain.
func keepBasicAuthHashes(updated *models.DomainConfig, stored []models.DomainConfig) {
	if updated.BasicAuth == nil {
		return
	}
	for _, previous := range stored {
		if previous.ID != updated.ID || previous.BasicAuth == nil {
			continue
		}
		for i := range updated.BasicAuth.Users {
			user := &updated.BasicAuth.Users[i]
			if user.Password != "" || user.PasswordHash != "" {
				continue
			}
			for _, old := range previous.BasicAuth.Users {
				if old.Username == user.Username {
					user.PasswordHash = old.PasswordHash
				}
			}
		}
	}
}

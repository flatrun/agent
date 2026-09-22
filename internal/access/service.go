package access

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/mail"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flatrun/agent/pkg/models"
)

const CookieName = "flatrun_access"

type Service struct {
	secret       []byte
	usedLinksDir string
	now          func() time.Time
	mu           sync.Mutex
	lastRequests map[string]time.Time
	lastPrune    time.Time
}

type tokenPayload struct {
	Kind   string `json:"kind"`
	Email  string `json:"email"`
	Host   string `json:"host"`
	Return string `json:"return,omitempty"`
	Expiry int64  `json:"expiry"`
}

func New(basePath string) (*Service, error) {
	dir := filepath.Join(basePath, ".flatrun")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create access directory: %w", err)
	}
	path := filepath.Join(dir, "access-secret")
	usedLinksDir := filepath.Join(dir, "used-access-links")
	if err := os.MkdirAll(usedLinksDir, 0700); err != nil {
		return nil, fmt.Errorf("create used access links directory: %w", err)
	}
	secret, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, fmt.Errorf("generate access secret: %w", err)
		}
		if err := os.WriteFile(path, []byte(base64.RawURLEncoding.EncodeToString(secret)), 0600); err != nil {
			return nil, fmt.Errorf("save access secret: %w", err)
		}
		return newService(secret, usedLinksDir), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read access secret: %w", err)
	}
	secret, err = base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(secret)))
	if err != nil || len(secret) != 32 {
		return nil, fmt.Errorf("access secret is invalid")
	}
	return newService(secret, usedLinksDir), nil
}

func Resolve(deployments []models.Deployment, host, requestPath string) (*models.DomainAccessConfig, bool) {
	host = hostname(host)
	bestLength := -1
	var best *models.DomainAccessConfig
	for i := range deployments {
		if deployments[i].Metadata == nil {
			continue
		}
		for _, domain := range deployments[i].Metadata.GetDomains() {
			if !matchesHost(domain, host) || domain.Access == nil || !domain.Access.Enabled {
				continue
			}
			prefix := domain.PathPrefix
			if prefix == "" {
				prefix = "/"
			}
			if !strings.HasPrefix(requestPath, prefix) || len(prefix) <= bestLength {
				continue
			}
			copy := *domain.Access
			best = &copy
			bestLength = len(prefix)
		}
	}
	return best, best != nil
}

func (s *Service) MagicLink(email, host, returnPath string) (string, error) {
	return s.sign(tokenPayload{Kind: "verify", Email: normalizeEmail(email), Host: hostname(host), Return: safeReturn(returnPath), Expiry: s.now().Add(15 * time.Minute).Unix()})
}

func (s *Service) VerifyMagicLink(value string) (string, string, string, error) {
	payload, err := s.verify(value, "verify")
	if err != nil {
		return "", "", "", err
	}
	digest := sha256.Sum256([]byte(value))
	path := filepath.Join(s.usedLinksDir, fmt.Sprintf("%d-%x", payload.Expiry, digest))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return "", "", "", fmt.Errorf("token is invalid or expired")
	}
	if err != nil {
		return "", "", "", fmt.Errorf("record used access link: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", "", "", fmt.Errorf("close used access link: %w", err)
	}
	s.pruneUsedLinks()
	return payload.Email, payload.Host, payload.Return, nil
}

func (s *Service) pruneUsedLinks() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.now().Sub(s.lastPrune) < time.Hour {
		return
	}
	s.lastPrune = s.now()
	entries, err := os.ReadDir(s.usedLinksDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		expiry, err := strconv.ParseInt(strings.SplitN(entry.Name(), "-", 2)[0], 10, 64)
		if err == nil && expiry < s.now().Unix() {
			_ = os.Remove(filepath.Join(s.usedLinksDir, entry.Name()))
		}
	}
}

func (s *Service) AllowEmailRequest(host, email string) bool {
	key := hostname(host) + "\x00" + normalizeEmail(email)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, ok := s.lastRequests[key]; ok && now.Sub(last) < time.Minute {
		return false
	}
	s.lastRequests[key] = now
	return true
}

func (s *Service) Session(email, host string, hours int) (string, error) {
	if hours <= 0 {
		hours = 24
	}
	return s.sign(tokenPayload{Kind: "session", Email: normalizeEmail(email), Host: hostname(host), Expiry: s.now().Add(time.Duration(hours) * time.Hour).Unix()})
}

func (s *Service) ValidateSession(value, host string, policy *models.DomainAccessConfig) bool {
	payload, err := s.verify(value, "session")
	return err == nil && payload.Host == hostname(host) && Allows(policy, payload.Email)
}

func Allows(policy *models.DomainAccessConfig, email string) bool {
	if policy == nil || !policy.Enabled || !ValidEmail(email) {
		return false
	}
	if policy.Mode == "any_verified" {
		return true
	}
	email = normalizeEmail(email)
	for _, allowed := range policy.AllowedEmails {
		if normalizeEmail(allowed) == email {
			return true
		}
	}
	return false
}

func ValidEmail(value string) bool {
	value = strings.TrimSpace(value)
	address, err := mail.ParseAddress(value)
	return err == nil && strings.EqualFold(address.Address, value)
}

func (s *Service) sign(payload tokenPayload) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Service) verify(value, kind string) (tokenPayload, error) {
	var payload tokenPayload
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return payload, fmt.Errorf("token is invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return payload, fmt.Errorf("token is invalid")
	}
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return payload, fmt.Errorf("token is invalid")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(data, &payload) != nil || payload.Kind != kind || payload.Expiry < s.now().Unix() {
		return tokenPayload{}, fmt.Errorf("token is invalid or expired")
	}
	return payload, nil
}

func matchesHost(domain models.DomainConfig, host string) bool {
	if hostname(domain.Domain) == host {
		return true
	}
	for _, alias := range domain.Aliases {
		if hostname(alias) == host {
			return true
		}
	}
	for _, alias := range domain.RouteOnlyAliases {
		if hostname(alias) == host {
			return true
		}
	}
	return false
}

func hostname(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if host, _, err := net.SplitHostPort(value); err == nil {
		return host
	}
	return strings.TrimSuffix(value, ".")
}

func normalizeEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func safeReturn(value string) string {
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\\\r\n") {
		return "/"
	}
	return value
}

func newService(secret []byte, usedLinksDir string) *Service {
	return &Service{
		secret: secret, usedLinksDir: usedLinksDir, now: time.Now, lastRequests: make(map[string]time.Time),
	}
}

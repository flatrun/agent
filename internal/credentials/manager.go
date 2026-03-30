package credentials

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flatrun/agent/pkg/models"
	"gopkg.in/yaml.v3"
)

type Manager struct {
	mu            sync.RWMutex
	registryTypes map[string]*models.RegistryType
	credentials   map[string]*models.RegistryCredential
	storagePath   string
	typesFilePath string
	credsFilePath string
}

func NewManager(deploymentsPath string) *Manager {
	storagePath := filepath.Join(deploymentsPath, ".flatrun")
	typesFilePath := filepath.Join(storagePath, "registry-types.yml")
	credsFilePath := filepath.Join(storagePath, "credentials.yml")

	m := &Manager{
		registryTypes: make(map[string]*models.RegistryType),
		credentials:   make(map[string]*models.RegistryCredential),
		storagePath:   storagePath,
		typesFilePath: typesFilePath,
		credsFilePath: credsFilePath,
	}

	m.initBuiltinTypes()
	_ = m.loadTypes()
	_ = m.loadCredentials()

	return m
}

func (m *Manager) initBuiltinTypes() {
	for _, rt := range models.DefaultRegistryTypes() {
		rtCopy := rt
		m.registryTypes[rt.Slug] = &rtCopy
	}
}

type registryTypesFile struct {
	RegistryTypes []*models.RegistryType `yaml:"registry_types"`
}

type credentialsFile struct {
	Credentials []*models.RegistryCredential `yaml:"credentials"`
}

func (m *Manager) loadTypes() error {
	data, err := os.ReadFile(m.typesFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var rtf registryTypesFile
	if err := yaml.Unmarshal(data, &rtf); err != nil {
		return err
	}

	for _, rt := range rtf.RegistryTypes {
		if rt.Source != models.RegistrySourceBuiltin {
			m.registryTypes[rt.Slug] = rt
		}
	}

	return nil
}

func (m *Manager) loadCredentials() error {
	data, err := os.ReadFile(m.credsFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var cf credentialsFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return err
	}

	for _, cred := range cf.Credentials {
		m.credentials[cred.ID] = cred
	}

	return nil
}

func (m *Manager) saveTypes() error {
	if err := os.MkdirAll(m.storagePath, 0700); err != nil {
		return err
	}

	types := make([]*models.RegistryType, 0)
	for _, rt := range m.registryTypes {
		if rt.Source != models.RegistrySourceBuiltin {
			types = append(types, rt)
		}
	}

	if len(types) == 0 {
		_ = os.Remove(m.typesFilePath)
		return nil
	}

	rtf := registryTypesFile{RegistryTypes: types}
	data, err := yaml.Marshal(&rtf)
	if err != nil {
		return err
	}

	return os.WriteFile(m.typesFilePath, data, 0600)
}

func (m *Manager) saveCredentials() error {
	if err := os.MkdirAll(m.storagePath, 0700); err != nil {
		return err
	}

	creds := make([]*models.RegistryCredential, 0, len(m.credentials))
	for _, cred := range m.credentials {
		creds = append(creds, cred)
	}

	if len(creds) == 0 {
		_ = os.Remove(m.credsFilePath)
		return nil
	}

	cf := credentialsFile{Credentials: creds}
	data, err := yaml.Marshal(&cf)
	if err != nil {
		return err
	}

	return os.WriteFile(m.credsFilePath, data, 0600)
}

func (m *Manager) ListRegistryTypes() []models.RegistryType {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]models.RegistryType, 0, len(m.registryTypes))
	for _, rt := range m.registryTypes {
		result = append(result, *rt)
	}
	return result
}

func (m *Manager) GetRegistryType(slug string) (*models.RegistryType, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	rt, ok := m.registryTypes[slug]
	if !ok {
		return nil, fmt.Errorf("registry type not found: %s", slug)
	}
	return rt, nil
}

func (m *Manager) CreateRegistryType(name string, urlPatterns []string, authType models.AuthType, loginURL, docsURL string) (*models.RegistryType, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	slug := generateSlug(name)
	if _, exists := m.registryTypes[slug]; exists {
		return nil, fmt.Errorf("registry type with slug %s already exists", slug)
	}

	now := time.Now()
	rt := &models.RegistryType{
		Slug:        slug,
		Name:        name,
		URLPatterns: urlPatterns,
		AuthType:    authType,
		LoginURL:    loginURL,
		DocsURL:     docsURL,
		Source:      models.RegistrySourceLocal,
		IsOfficial:  false,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	m.registryTypes[slug] = rt

	if err := m.saveTypes(); err != nil {
		delete(m.registryTypes, slug)
		return nil, err
	}

	return rt, nil
}

func (m *Manager) UpdateRegistryType(slug, name string, urlPatterns []string, authType models.AuthType, loginURL, docsURL string) (*models.RegistryType, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	rt, ok := m.registryTypes[slug]
	if !ok {
		return nil, fmt.Errorf("registry type not found: %s", slug)
	}

	if rt.Source == models.RegistrySourceBuiltin {
		return nil, fmt.Errorf("cannot modify builtin registry type: %s", slug)
	}

	if name != "" {
		rt.Name = name
	}
	if len(urlPatterns) > 0 {
		rt.URLPatterns = urlPatterns
	}
	if authType != "" {
		rt.AuthType = authType
	}
	if loginURL != "" {
		rt.LoginURL = loginURL
	}
	if docsURL != "" {
		rt.DocsURL = docsURL
	}
	rt.UpdatedAt = time.Now()

	if err := m.saveTypes(); err != nil {
		return nil, err
	}

	return rt, nil
}

func (m *Manager) DeleteRegistryType(slug string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	rt, ok := m.registryTypes[slug]
	if !ok {
		return fmt.Errorf("registry type not found: %s", slug)
	}

	if rt.Source == models.RegistrySourceBuiltin {
		return fmt.Errorf("cannot delete builtin registry type: %s", slug)
	}

	for _, cred := range m.credentials {
		if cred.RegistryTypeSlug == slug {
			return fmt.Errorf("cannot delete registry type: credentials exist for %s", slug)
		}
	}

	delete(m.registryTypes, slug)
	return m.saveTypes()
}

func (m *Manager) ListCredentials() []models.RegistryCredentialWithType {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]models.RegistryCredentialWithType, 0, len(m.credentials))
	for _, cred := range m.credentials {
		cwt := models.RegistryCredentialWithType{
			RegistryCredential: *cred,
		}
		if rt, ok := m.registryTypes[cred.RegistryTypeSlug]; ok {
			cwt.RegistryType = rt
		}
		result = append(result, cwt)
	}
	return result
}

func (m *Manager) GetCredential(id string) (*models.RegistryCredential, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cred, ok := m.credentials[id]
	if !ok {
		return nil, fmt.Errorf("credential not found: %s", id)
	}
	return cred, nil
}

func (m *Manager) CreateCredential(name, registryTypeSlug, username, password, email string, isDefault bool) (*models.RegistryCredential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.registryTypes[registryTypeSlug]; !ok {
		return nil, fmt.Errorf("registry type not found: %s", registryTypeSlug)
	}

	for _, cred := range m.credentials {
		if cred.Name == name {
			return nil, fmt.Errorf("credential with name %s already exists", name)
		}
	}

	if isDefault {
		for _, cred := range m.credentials {
			if cred.RegistryTypeSlug == registryTypeSlug && cred.IsDefault {
				cred.IsDefault = false
			}
		}
	}

	id := generateID()
	now := time.Now()

	cred := &models.RegistryCredential{
		ID:               id,
		Name:             name,
		RegistryTypeSlug: registryTypeSlug,
		Username:         username,
		Password:         password,
		Email:            email,
		IsDefault:        isDefault,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	m.credentials[id] = cred

	if err := m.saveCredentials(); err != nil {
		delete(m.credentials, id)
		return nil, err
	}

	return cred, nil
}

func (m *Manager) UpdateCredential(id, name, username, password, email string, isDefault *bool) (*models.RegistryCredential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cred, ok := m.credentials[id]
	if !ok {
		return nil, fmt.Errorf("credential not found: %s", id)
	}

	if name != "" && name != cred.Name {
		for _, c := range m.credentials {
			if c.ID != id && c.Name == name {
				return nil, fmt.Errorf("credential with name %s already exists", name)
			}
		}
		cred.Name = name
	}

	if username != "" {
		cred.Username = username
	}
	if password != "" {
		cred.Password = password
	}
	if email != "" {
		cred.Email = email
	}

	if isDefault != nil && *isDefault && !cred.IsDefault {
		for _, c := range m.credentials {
			if c.RegistryTypeSlug == cred.RegistryTypeSlug && c.IsDefault {
				c.IsDefault = false
			}
		}
		cred.IsDefault = true
	} else if isDefault != nil && !*isDefault {
		cred.IsDefault = false
	}

	cred.UpdatedAt = time.Now()

	if err := m.saveCredentials(); err != nil {
		return nil, err
	}

	return cred, nil
}

func (m *Manager) DeleteCredential(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.credentials[id]; !ok {
		return fmt.Errorf("credential not found: %s", id)
	}

	delete(m.credentials, id)
	return m.saveCredentials()
}

func (m *Manager) FindCredentialForImage(imageName string) *models.RegistryCredential {
	m.mu.RLock()
	defer m.mu.RUnlock()

	registry := extractRegistry(imageName)

	var matchedType *models.RegistryType
	for _, rt := range m.registryTypes {
		if matchesURLPatterns(rt.URLPatterns, registry) {
			matchedType = rt
			break
		}
	}

	if matchedType == nil {
		return nil
	}

	var defaultCred *models.RegistryCredential
	for _, cred := range m.credentials {
		if cred.RegistryTypeSlug == matchedType.Slug {
			if cred.IsDefault {
				return cred
			}
			if defaultCred == nil {
				defaultCred = cred
			}
		}
	}

	return defaultCred
}

func (m *Manager) GetCredentialForRegistry(registryTypeSlug string) *models.RegistryCredential {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var defaultCred *models.RegistryCredential
	for _, cred := range m.credentials {
		if cred.RegistryTypeSlug == registryTypeSlug {
			if cred.IsDefault {
				return cred
			}
			if defaultCred == nil {
				defaultCred = cred
			}
		}
	}

	return defaultCred
}

func (m *Manager) TestCredential(id string) error {
	cred, err := m.GetCredential(id)
	if err != nil {
		return err
	}

	rt, err := m.GetRegistryType(cred.RegistryTypeSlug)
	if err != nil {
		return err
	}

	return testDockerLogin(rt, cred)
}

func (m *Manager) GetLoginRegistry(cred *models.RegistryCredential) string {
	if cred == nil {
		return ""
	}
	rt, err := m.GetRegistryType(cred.RegistryTypeSlug)
	if err != nil || len(rt.URLPatterns) == 0 {
		return ""
	}
	registry := rt.URLPatterns[0]
	if registry == "docker.io" {
		return ""
	}
	return registry
}

func extractRegistry(imageName string) string {
	parts := strings.Split(imageName, "/")
	if len(parts) == 1 {
		return "docker.io"
	}

	firstPart := parts[0]
	if strings.Contains(firstPart, ".") || strings.Contains(firstPart, ":") {
		return firstPart
	}

	return "docker.io"
}

func matchesURLPatterns(patterns []string, registry string) bool {
	registry = strings.ToLower(registry)

	for _, pattern := range patterns {
		pattern = strings.ToLower(pattern)

		if registry == pattern {
			return true
		}

		if strings.Contains(registry, pattern) {
			return true
		}

		if strings.HasSuffix(registry, pattern) {
			return true
		}
	}

	return false
}

func generateSlug(name string) string {
	slug := strings.ToLower(name)
	slug = strings.ReplaceAll(slug, " ", "-")
	slug = strings.ReplaceAll(slug, "_", "-")

	var result strings.Builder
	for _, r := range slug {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			result.WriteRune(r)
		}
	}

	return result.String()
}

func generateID() string {
	bytes := make([]byte, 8)
	_, _ = rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

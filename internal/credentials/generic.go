package credentials

import (
	"fmt"
	"os"
	"time"

	"github.com/flatrun/agent/internal/secrets"
	"github.com/flatrun/agent/pkg/models"
	"gopkg.in/yaml.v3"
)

type genericCredentialsFile struct {
	Credentials []*models.Credential `yaml:"credentials"`
}

var knownCredentialKinds = map[models.CredentialKind]bool{
	models.CredentialKindS3:          true,
	models.CredentialKindGit:         true,
	models.CredentialKindWebhook:     true,
	models.CredentialKindInfisical:   true,
	models.CredentialKindVault:       true,
	models.CredentialKindDoppler:     true,
	models.CredentialKindOnePassword: true,
}

// Sealer encrypts secret values before they are written to disk.
type Sealer interface {
	Seal(plain string) (string, error)
	Open(sealed string) (string, error)
}

// SetSealer makes the store seal secret values on disk. Stored credentials are
// read again and written back, so values saved before encryption existed are
// sealed now.
func (m *Manager) SetSealer(sealer Sealer) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sealer = sealer
	if err := m.loadGenericCredentials(); err != nil {
		return err
	}
	if len(m.genericCreds) == 0 {
		return nil
	}
	return m.saveGenericCredentials()
}

// loadGenericCredentials replaces the in-memory store only when every stored
// credential could be read. Otherwise saving stays refused, because a save
// would write back only the credentials that were read and drop the rest.
func (m *Manager) loadGenericCredentials() error {
	creds, err := m.readGenericCredentials()
	if err != nil {
		m.genericLoadErr = err
		return err
	}
	m.genericCreds = creds
	m.genericLoadErr = nil
	return nil
}

func (m *Manager) readGenericCredentials() (map[string]*models.Credential, error) {
	creds := make(map[string]*models.Credential)
	data, err := os.ReadFile(m.genericCredsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return creds, nil
		}
		return nil, err
	}

	var cf genericCredentialsFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return nil, err
	}

	for _, cred := range cf.Credentials {
		if cred.Data == nil {
			cred.Data = map[string]string{}
		}
		for k, v := range cred.Data {
			if !secrets.IsSealed(v) {
				continue
			}
			if m.sealer == nil {
				return nil, fmt.Errorf("credential %s holds sealed values but no key is loaded", cred.ID)
			}
			plain, err := m.sealer.Open(v)
			if err != nil {
				return nil, fmt.Errorf("credential %s: %w", cred.ID, err)
			}
			cred.Data[k] = plain
		}
		creds[cred.ID] = cred
	}

	return creds, nil
}

func (m *Manager) saveGenericCredentials() error {
	if m.genericLoadErr != nil {
		return fmt.Errorf("stored credentials could not be read, so they are not overwritten: %w", m.genericLoadErr)
	}
	if err := os.MkdirAll(m.storagePath, 0700); err != nil {
		return err
	}

	creds := make([]*models.Credential, 0, len(m.genericCreds))
	for _, cred := range m.genericCreds {
		stored := *cred
		if m.sealer != nil {
			stored.Data = copyData(cred.Data)
			for k, v := range stored.Data {
				if v == "" || !models.IsSecretKey(cred.Kind, k) {
					continue
				}
				sealed, err := m.sealer.Seal(v)
				if err != nil {
					return err
				}
				stored.Data[k] = sealed
			}
		}
		creds = append(creds, &stored)
	}

	if len(creds) == 0 {
		_ = os.Remove(m.genericCredsFile)
		return nil
	}

	cf := genericCredentialsFile{Credentials: creds}
	data, err := yaml.Marshal(&cf)
	if err != nil {
		return err
	}

	return os.WriteFile(m.genericCredsFile, data, 0600)
}

// ListGenericCredentials returns generic credentials, optionally filtered by
// kind. Pass an empty kind to list all.
func (m *Manager) ListGenericCredentials(kind models.CredentialKind) []models.Credential {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]models.Credential, 0, len(m.genericCreds))
	for _, cred := range m.genericCreds {
		if kind != "" && cred.Kind != kind {
			continue
		}
		result = append(result, *cred)
	}
	return result
}

// GetGenericCredential returns the raw credential, including secret values, for
// internal use. API handlers marshal it to JSON, which masks secrets.
func (m *Manager) GetGenericCredential(id string) (*models.Credential, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cred, ok := m.genericCreds[id]
	if !ok {
		return nil, fmt.Errorf("credential not found: %s", id)
	}
	clone := *cred
	clone.Data = copyData(cred.Data)
	return &clone, nil
}

func (m *Manager) CreateGenericCredential(name string, kind models.CredentialKind, data map[string]string) (*models.Credential, error) {
	if name == "" {
		return nil, fmt.Errorf("credential name is required")
	}
	if !knownCredentialKinds[kind] {
		return nil, fmt.Errorf("unknown credential kind: %s", kind)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, cred := range m.genericCreds {
		if cred.Name == name {
			return nil, fmt.Errorf("credential with name %s already exists", name)
		}
	}

	now := time.Now()
	cred := &models.Credential{
		ID:        generateID(),
		Name:      name,
		Kind:      kind,
		Data:      copyData(data),
		CreatedAt: now,
		UpdatedAt: now,
	}

	m.genericCreds[cred.ID] = cred

	if err := m.saveGenericCredentials(); err != nil {
		delete(m.genericCreds, cred.ID)
		return nil, err
	}

	clone := *cred
	clone.Data = copyData(cred.Data)
	return &clone, nil
}

// UpdateGenericCredential merges provided fields. A data value that is empty or
// equal to the mask sentinel leaves the stored value untouched, so a secret can
// be kept without the caller ever having to resend it.
func (m *Manager) UpdateGenericCredential(id, name string, data map[string]string) (*models.Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cred, ok := m.genericCreds[id]
	if !ok {
		return nil, fmt.Errorf("credential not found: %s", id)
	}

	if name != "" && name != cred.Name {
		for _, c := range m.genericCreds {
			if c.ID != id && c.Name == name {
				return nil, fmt.Errorf("credential with name %s already exists", name)
			}
		}
		cred.Name = name
	}

	if cred.Data == nil {
		cred.Data = map[string]string{}
	}
	for k, v := range data {
		if v == "" || v == models.CredentialMask {
			continue
		}
		cred.Data[k] = v
	}

	cred.UpdatedAt = time.Now()

	if err := m.saveGenericCredentials(); err != nil {
		return nil, err
	}

	clone := *cred
	clone.Data = copyData(cred.Data)
	return &clone, nil
}

func (m *Manager) DeleteGenericCredential(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.genericCreds[id]; !ok {
		return fmt.Errorf("credential not found: %s", id)
	}

	delete(m.genericCreds, id)
	return m.saveGenericCredentials()
}

func copyData(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

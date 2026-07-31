package models

import (
	"encoding/json"
	"time"
)

type CredentialKind string

const (
	CredentialKindS3  CredentialKind = "s3"
	CredentialKindGit CredentialKind = "git"
)

// Credential is a generic, kind-tagged secret held by the credential manager,
// separate from the registry-specific RegistryCredential. Secret-bearing keys
// in Data are masked when marshaled to JSON but written verbatim to the 0600
// on-disk store.
type Credential struct {
	ID        string            `json:"id" yaml:"id"`
	Name      string            `json:"name" yaml:"name"`
	Kind      CredentialKind    `json:"kind" yaml:"kind"`
	Data      map[string]string `json:"data" yaml:"data"`
	CreatedAt time.Time         `json:"created_at" yaml:"created_at"`
	UpdatedAt time.Time         `json:"updated_at" yaml:"updated_at"`
}

// CredentialMask is returned in place of a secret value so callers can tell a
// secret is set without receiving it. A value equal to the mask on update is
// treated as "unchanged".
const CredentialMask = "********"

// credentialSecretKeys lists the Data keys whose values must never leave the
// agent in a JSON response, per credential kind.
var credentialSecretKeys = map[CredentialKind]map[string]bool{
	CredentialKindS3:  {"secret_access_key": true},
	CredentialKindGit: {"token": true},
}

// IsSecretKey reports whether a Data key holds a secret for the given kind.
func IsSecretKey(kind CredentialKind, key string) bool {
	return credentialSecretKeys[kind][key]
}

func (c Credential) MarshalJSON() ([]byte, error) {
	type alias Credential
	secrets := credentialSecretKeys[c.Kind]
	masked := make(map[string]string, len(c.Data))
	for k, v := range c.Data {
		if secrets[k] && v != "" {
			masked[k] = CredentialMask
			continue
		}
		masked[k] = v
	}
	clone := alias(c)
	clone.Data = masked
	return json.Marshal(clone)
}

package models

import "time"

type RegistryTypeSource string

const (
	RegistrySourceBuiltin     RegistryTypeSource = "builtin"
	RegistrySourceLocal       RegistryTypeSource = "local"
	RegistrySourceMarketplace RegistryTypeSource = "marketplace"
)

type AuthType string

const (
	AuthTypeBasic AuthType = "basic"
	AuthTypeToken AuthType = "token"
)

type RegistryType struct {
	Slug        string             `json:"slug" yaml:"slug"`
	Name        string             `json:"name" yaml:"name"`
	URLPatterns []string           `json:"url_patterns" yaml:"url_patterns"`
	AuthType    AuthType           `json:"auth_type" yaml:"auth_type"`
	LoginURL    string             `json:"login_url,omitempty" yaml:"login_url,omitempty"`
	DocsURL     string             `json:"docs_url,omitempty" yaml:"docs_url,omitempty"`
	Icon        string             `json:"icon,omitempty" yaml:"icon,omitempty"`
	Source      RegistryTypeSource `json:"source" yaml:"source"`
	IsOfficial  bool               `json:"is_official" yaml:"is_official"`
	CreatedAt   time.Time          `json:"created_at" yaml:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at" yaml:"updated_at"`
}

type RegistryCredential struct {
	ID               string    `json:"id" yaml:"id"`
	Name             string    `json:"name" yaml:"name"`
	RegistryTypeSlug string    `json:"registry_type_slug" yaml:"registry_type_slug"`
	Username         string    `json:"username" yaml:"username"`
	Password         string    `json:"-" yaml:"password"`
	Email            string    `json:"email,omitempty" yaml:"email,omitempty"`
	IsDefault        bool      `json:"is_default" yaml:"is_default"`
	CreatedAt        time.Time `json:"created_at" yaml:"created_at"`
	UpdatedAt        time.Time `json:"updated_at" yaml:"updated_at"`
}

type RegistryCredentialWithType struct {
	RegistryCredential
	RegistryType *RegistryType `json:"registry_type,omitempty"`
}

func DefaultRegistryTypes() []RegistryType {
	now := time.Now()
	return []RegistryType{
		{
			Slug:        "docker-hub",
			Name:        "Docker Hub",
			URLPatterns: []string{"docker.io", "index.docker.io", "registry-1.docker.io"},
			AuthType:    AuthTypeBasic,
			LoginURL:    "https://hub.docker.com",
			DocsURL:     "https://docs.docker.com/docker-hub/",
			Icon:        "docker",
			Source:      RegistrySourceBuiltin,
			IsOfficial:  true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			Slug:        "ghcr",
			Name:        "GitHub Container Registry",
			URLPatterns: []string{"ghcr.io"},
			AuthType:    AuthTypeToken,
			LoginURL:    "https://github.com/settings/tokens",
			DocsURL:     "https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry",
			Icon:        "github",
			Source:      RegistrySourceBuiltin,
			IsOfficial:  true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			Slug:        "gcr",
			Name:        "Google Container Registry",
			URLPatterns: []string{"gcr.io", "us.gcr.io", "eu.gcr.io", "asia.gcr.io"},
			AuthType:    AuthTypeToken,
			LoginURL:    "https://console.cloud.google.com",
			DocsURL:     "https://cloud.google.com/container-registry/docs",
			Icon:        "google",
			Source:      RegistrySourceBuiltin,
			IsOfficial:  true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			Slug:        "gar",
			Name:        "Google Artifact Registry",
			URLPatterns: []string{"-docker.pkg.dev"},
			AuthType:    AuthTypeToken,
			LoginURL:    "https://console.cloud.google.com",
			DocsURL:     "https://cloud.google.com/artifact-registry/docs",
			Icon:        "google",
			Source:      RegistrySourceBuiltin,
			IsOfficial:  true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			Slug:        "ecr",
			Name:        "Amazon ECR",
			URLPatterns: []string{".dkr.ecr.", ".amazonaws.com"},
			AuthType:    AuthTypeToken,
			LoginURL:    "https://console.aws.amazon.com/ecr",
			DocsURL:     "https://docs.aws.amazon.com/AmazonECR/latest/userguide/",
			Icon:        "aws",
			Source:      RegistrySourceBuiltin,
			IsOfficial:  true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			Slug:        "acr",
			Name:        "Azure Container Registry",
			URLPatterns: []string{".azurecr.io"},
			AuthType:    AuthTypeBasic,
			LoginURL:    "https://portal.azure.com",
			DocsURL:     "https://docs.microsoft.com/en-us/azure/container-registry/",
			Icon:        "azure",
			Source:      RegistrySourceBuiltin,
			IsOfficial:  true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			Slug:        "quay",
			Name:        "Quay.io",
			URLPatterns: []string{"quay.io"},
			AuthType:    AuthTypeBasic,
			LoginURL:    "https://quay.io",
			DocsURL:     "https://docs.quay.io/",
			Icon:        "quay",
			Source:      RegistrySourceBuiltin,
			IsOfficial:  true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			Slug:        "gitlab",
			Name:        "GitLab Container Registry",
			URLPatterns: []string{"registry.gitlab.com"},
			AuthType:    AuthTypeToken,
			LoginURL:    "https://gitlab.com/-/profile/personal_access_tokens",
			DocsURL:     "https://docs.gitlab.com/ee/user/packages/container_registry/",
			Icon:        "gitlab",
			Source:      RegistrySourceBuiltin,
			IsOfficial:  true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			Slug:        "digitalocean",
			Name:        "DigitalOcean Container Registry",
			URLPatterns: []string{"registry.digitalocean.com"},
			AuthType:    AuthTypeToken,
			LoginURL:    "https://cloud.digitalocean.com/registry",
			DocsURL:     "https://docs.digitalocean.com/products/container-registry/",
			Icon:        "digitalocean",
			Source:      RegistrySourceBuiltin,
			IsOfficial:  true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}
}

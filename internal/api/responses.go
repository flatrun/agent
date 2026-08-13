package api

import (
	"github.com/flatrun/agent/internal/backup"
	"github.com/flatrun/agent/pkg/models"
)

// Responses declared as types rather than inline maps, so the generated spec describes what an
// endpoint answers with and a client can lay it out without being told how. The `cli` tag names
// the fields worth a column; everything else is still in the payload for whoever wants it.

type DeploymentListResponse struct {
	Deployments []models.Deployment `json:"deployments"`
	Path        string              `json:"path"`
}

type BackupListResponse struct {
	Backups []backup.Backup `json:"backups"`
}

type CertificateListResponse struct {
	Certificates []models.Certificate `json:"certificates"`
}

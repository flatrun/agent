package models

import "time"

type Certificate struct {
	Domain       string    `json:"domain"`
	Issuer       string    `json:"issuer"`
	NotBefore    time.Time `json:"not_before"`
	NotAfter     time.Time `json:"not_after"`
	DaysLeft     int       `json:"days_left"`
	Status       string    `json:"status"`
	Path         string    `json:"path"`
	AutoRenew    bool      `json:"auto_renew"`
	DeploymentID string    `json:"deployment_id,omitempty"`
}

package models

import "time"

type Certificate struct {
	Domain       string    `json:"domain" cli:"column"`
	Issuer       string    `json:"issuer" cli:"column"`
	NotBefore    time.Time `json:"not_before"`
	NotAfter     time.Time `json:"not_after"`
	DaysLeft     int       `json:"days_left" cli:"column"`
	Status       string    `json:"status" cli:"column"`
	Path         string    `json:"path"`
	AutoRenew    bool      `json:"auto_renew" cli:"column"`
	DeploymentID string    `json:"deployment_id,omitempty"`
}

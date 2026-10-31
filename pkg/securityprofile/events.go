package securityprofile

import "time"

type AccessEvent struct {
	EventID         string      `json:"event_id"`
	Source          string      `json:"source"`
	Action          string      `json:"action"`
	ResourceType    string      `json:"resource_type"`
	ResourceID      string      `json:"resource_id"`
	SubjectID       *string     `json:"subject_id"`
	TenantID        *string     `json:"tenant_id"`
	PolicyRevision  string      `json:"policy_revision"`
	Integrity       string      `json:"integrity"`
	Authenticated   *bool       `json:"authenticated"`
	At              time.Time   `json:"at"`
	FactsValidFrom  *time.Time  `json:"facts_valid_from"`
	FactsValidUntil *time.Time  `json:"facts_valid_until"`
	Facts           AccessFacts `json:"facts"`
	ProtectedEffect string      `json:"protected_effect"`
}
type AccessFacts struct {
	TenantMember  *bool       `json:"tenant_member"`
	ResourceOwner *bool       `json:"resource_owner"`
	ExplicitShare *bool       `json:"explicit_share"`
	Roles         *[]RoleFact `json:"roles"`
}
type RoleFact struct {
	Role     string  `json:"role"`
	Scope    string  `json:"scope"`
	TenantID *string `json:"tenant_id"`
}

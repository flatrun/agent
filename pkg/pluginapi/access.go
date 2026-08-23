package pluginapi

import (
	"encoding/base64"
	"encoding/json"
)

const ResourceAccessHeader = "X-Flatrun-Resource-Access"

type ResourceGrant struct {
	Resource string `json:"resource"`
	ID       string `json:"id"`
	Level    string `json:"level"`
}

type ResourceAccess struct {
	Global bool            `json:"global,omitempty"`
	Grants []ResourceGrant `json:"grants,omitempty"`
}

func EncodeResourceAccess(access ResourceAccess) (string, error) {
	payload, err := json.Marshal(access)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func DecodeResourceAccess(value string) (ResourceAccess, error) {
	var access ResourceAccess
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return access, err
	}
	err = json.Unmarshal(payload, &access)
	return access, err
}

func (a ResourceAccess) Allows(resource, id, level string) bool {
	if a.Global {
		return true
	}
	for _, grant := range a.Grants {
		if grant.Resource == resource && grant.ID == id && accessRank(grant.Level) >= accessRank(level) {
			return true
		}
	}
	return false
}

func accessRank(level string) int {
	switch level {
	case "read":
		return 1
	case "write":
		return 2
	case "admin":
		return 3
	default:
		return 0
	}
}

package models

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var bodySizePattern = regexp.MustCompile(`^([0-9]+)([kKmMgG]?)$`)

// DefaultBodySize is nginx's own client_max_body_size, in force wherever
// neither the domain nor the agent sets one.
const DefaultBodySize = "1m"

// EffectiveBodySize is the limit nginx applies to a domain.
func EffectiveBodySize(domain, agentDefault string) string {
	if domain != "" {
		return domain
	}
	if agentDefault != "" {
		return agentDefault
	}
	return DefaultBodySize
}

// MaxBodySizeLimit caps configurable request sizes. Larger bodies belong on
// object storage, and nginx buffers request bodies to disk on the proxy host.
const MaxBodySizeLimit int64 = 10 << 30

// ParseBodySize returns the size in bytes of an nginx size value such as
// "512k", "64m" or "1g". "0" means no limit and returns 0.
func ParseBodySize(value string) (int64, error) {
	match := bodySizePattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return 0, fmt.Errorf("size %q must be a number with an optional k, m or g unit, for example 64m", value)
	}
	n, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("size %q is too large", value)
	}
	shift := map[string]uint{"": 0, "k": 10, "m": 20, "g": 30}[strings.ToLower(match[2])]
	if n > MaxBodySizeLimit>>shift {
		return 0, fmt.Errorf("size %q is above the 10g limit", value)
	}
	return n << shift, nil
}

// ValidateBodySize accepts an empty value, which means "use the default".
func ValidateBodySize(value string) error {
	if value == "" {
		return nil
	}
	_, err := ParseBodySize(value)
	return err
}

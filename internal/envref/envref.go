// Package envref resolves references between deployments' environment
// variables. A value written as ${{ api.DATABASE_URL }} takes the value of
// DATABASE_URL in the api deployment, and ${{ api.FLATRUN_HOST }} its
// address on the shared network. Where variables come from is a Source, so
// the resolver works the same over files, tests or any other store.
package envref

import (
	"fmt"
	"regexp"
	"strings"
)

// HostKey is the reserved key that resolves to a deployment's address.
const HostKey = "FLATRUN_HOST"

var pattern = regexp.MustCompile(`\$\{\{\s*([a-zA-Z0-9][a-zA-Z0-9_-]*)\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// Source supplies the raw (unresolved) variables of a deployment and its
// address. Raw returns ErrUnknownDeployment for a deployment that does not
// exist.
type Source interface {
	Raw(deployment string) (map[string]string, error)
	Host(deployment string) (string, error)
}

var ErrUnknownDeployment = fmt.Errorf("deployment not found")

// Error reports the variable that could not be resolved and why.
type Error struct {
	Key    string
	Reason string
}

func (e *Error) Error() string {
	return fmt.Sprintf("cannot resolve %s: %s", e.Key, e.Reason)
}

// HasReference reports whether a value contains at least one reference.
func HasReference(value string) bool {
	return pattern.MatchString(value)
}

type Resolver struct {
	Source Source
}

// Resolve returns vars with every reference replaced. self names the
// deployment vars belong to, so its own variables can reference each other.
func (r Resolver) Resolve(self string, vars map[string]string) (map[string]string, error) {
	cache := map[string]map[string]string{self: vars}
	out := make(map[string]string, len(vars))
	for key, value := range vars {
		resolved, err := r.resolveValue(self, key, value, cache, []string{self + "." + key})
		if err != nil {
			return nil, err
		}
		out[key] = resolved
	}
	return out, nil
}

func (r Resolver) resolveValue(self, key, value string, cache map[string]map[string]string, stack []string) (string, error) {
	var firstErr error
	resolved := pattern.ReplaceAllStringFunc(value, func(ref string) string {
		if firstErr != nil {
			return ref
		}
		m := pattern.FindStringSubmatch(ref)
		target, targetKey := m[1], m[2]
		id := target + "." + targetKey
		for _, seen := range stack {
			if seen == id {
				firstErr = &Error{Key: key, Reason: "reference cycle " + strings.Join(append(stack, id), " -> ")}
				return ref
			}
		}

		if targetKey == HostKey {
			host, err := r.Source.Host(target)
			if err != nil {
				firstErr = &Error{Key: key, Reason: fmt.Sprintf("%s: %v", target, err)}
				return ref
			}
			return host
		}

		vars, ok := cache[target]
		if !ok {
			loaded, err := r.Source.Raw(target)
			if err != nil {
				firstErr = &Error{Key: key, Reason: fmt.Sprintf("%s: %v", target, err)}
				return ref
			}
			cache[target] = loaded
			vars = loaded
		}
		raw, ok := vars[targetKey]
		if !ok {
			firstErr = &Error{Key: key, Reason: fmt.Sprintf("%s has no variable %s", target, targetKey)}
			return ref
		}
		value, err := r.resolveValue(target, key, raw, cache, append(stack, id))
		if err != nil {
			firstErr = err
			return ref
		}
		return value
	})
	if firstErr != nil {
		return "", firstErr
	}
	return resolved, nil
}

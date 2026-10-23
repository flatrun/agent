package secrets

import (
	"context"
	"fmt"
)

// Connections looks up the stored settings of a provider connection.
type Connections func(id string) (provider string, settings map[string]string, err error)

// Resolver merges stored secrets with what bound managers return.
type Resolver struct {
	Store       *Store
	Registry    *Registry
	Connections Connections
}

// FetchBinding reads every key a binding's manager returns.
func (r Resolver) FetchBinding(ctx context.Context, b Binding) (map[string]string, error) {
	p, ok := r.Registry.Get(b.Provider)
	if !ok {
		return nil, fmt.Errorf("unknown secret provider %q", b.Provider)
	}
	provider, settings, err := r.Connections(b.ConnectionID)
	if err != nil {
		return nil, err
	}
	if provider != b.Provider {
		return nil, fmt.Errorf("connection %s is for %s, not %s", b.ConnectionID, provider, b.Provider)
	}
	return p.Fetch(ctx, settings, b.Options)
}

// Values returns the secrets visible through a chain of scopes ordered from
// broadest to narrowest. A narrower scope overrides a broader one, and within
// a scope a stored secret overrides a bound manager.
func (r Resolver) Values(ctx context.Context, chain []Scope) (map[string]string, error) {
	out := map[string]string{}
	for _, scope := range chain {
		bindings, err := r.Store.Bindings(scope)
		if err != nil {
			return nil, err
		}
		for _, b := range bindings {
			values, err := r.FetchBinding(ctx, b)
			if err != nil {
				return nil, fmt.Errorf("%s binding %s: %w", scope, b.Provider, err)
			}
			for k, v := range values {
				out[k] = v
			}
		}
		stored, err := r.Store.Values(scope)
		if err != nil {
			return nil, err
		}
		for k, v := range stored {
			out[k] = v
		}
	}
	return out, nil
}

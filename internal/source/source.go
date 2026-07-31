// Package source fetches the code that becomes a deployment from wherever it
// lives. A deployment used to require compose content in hand; a Provider turns
// a Descriptor (a git URL, an upload token, a webhook delivery) into a directory
// on disk. Providers register behind one interface so a new source is an added
// registration, not a change to the deploy flow, and no single source (git today)
// is special.
package source

import "context"

// Descriptor names a source and how to reach it. Params carries provider-specific
// options (branch, subpath, ...). Auth is resolved by the caller from the
// credential manager, so a provider never sees a raw credential id and the
// package stays free of any credential-store dependency.
type Descriptor struct {
	Type   string
	Ref    string
	Params map[string]string
	Auth   *Auth
}

// Auth is credential material a provider needs to reach a private source.
type Auth struct {
	Username string
	Token    string
}

// Result reports where the fetched source landed and which revision it was, so
// the caller can find the compose file and record provenance.
type Result struct {
	Dir      string
	Revision string
}

// Provider fetches a source into destDir. log, when non-nil, receives progress
// lines (a provider must not send secrets through it). destDir is the provider's
// to populate; the caller owns creating and cleaning up its parent.
type Provider interface {
	Type() string
	Fetch(ctx context.Context, d Descriptor, destDir string, log func(string)) (*Result, error)
}

// Registry resolves a source type to its provider.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry indexes the providers by their Type(). A later registration for
// the same type wins, which keeps the call site a plain list.
func NewRegistry(providers ...Provider) *Registry {
	r := &Registry{providers: make(map[string]Provider, len(providers))}
	for _, p := range providers {
		r.providers[p.Type()] = p
	}
	return r
}

// Get returns the provider for a source type, or false when none is registered.
func (r *Registry) Get(typ string) (Provider, bool) {
	p, ok := r.providers[typ]
	return p, ok
}

// Types lists the registered source types.
func (r *Registry) Types() []string {
	types := make([]string, 0, len(r.providers))
	for t := range r.providers {
		types = append(types, t)
	}
	return types
}

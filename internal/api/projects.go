package api

import (
	"net/http"
	"strings"
)

const (
	projectsPrefix    = "/api/projects"
	deploymentsPrefix = "/api/deployments"
)

// Handler is what the agent serves: the router, with every project path answered by the
// deployment route of the same shape. A project and a deployment are one resource, so the two
// names cannot drift apart.
func (s *Server) Handler() http.Handler {
	return projectRoutes(s.router)
}

func projectRoutes(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, ok := deploymentPath(r.URL.Path)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		aliased := r.Clone(r.Context())
		aliased.URL.Path = path
		if raw, ok := deploymentPath(r.URL.RawPath); ok {
			aliased.URL.RawPath = raw
		}
		next.ServeHTTP(w, aliased)
	})
}

func deploymentPath(path string) (string, bool) {
	if path != projectsPrefix && !strings.HasPrefix(path, projectsPrefix+"/") {
		return "", false
	}
	return deploymentsPrefix + strings.TrimPrefix(path, projectsPrefix), true
}

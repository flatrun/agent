package api

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/source"
)

// deploymentSource is the create-deployment request field that points at code to
// deploy from, instead of inline compose content. Type selects the provider
// (e.g. "git"); Ref is the provider's locator (a repository URL for git).
type deploymentSource struct {
	Type         string `json:"type"`
	Ref          string `json:"ref"`
	Branch       string `json:"branch,omitempty"`
	Subpath      string `json:"subpath,omitempty"`
	CredentialID string `json:"credential_id,omitempty"`
}

// fetchedSource is a materialized source ready to become a deployment: the
// directory holding its files, the compose file found inside it, and the compose
// content read from it. cleanup removes the temporary fetch directory.
type fetchedSource struct {
	dir            string
	composeName    string
	composeContent string
	cleanup        func()
}

// fetchDeploymentSource resolves the source's provider, fetches into a temporary
// directory, and locates the compose file the deployment will run. The temporary
// directory is the caller's to release through the returned cleanup once the
// files have been copied into the deployment.
func (s *Server) fetchDeploymentSource(ctx context.Context, req *deploymentSource) (*fetchedSource, error) {
	provider, ok := s.sourceRegistry.Get(req.Type)
	if !ok {
		return nil, fmt.Errorf("unknown source type %q", req.Type)
	}

	auth, err := s.resolveSourceAuth(req.CredentialID)
	if err != nil {
		return nil, err
	}

	tmp, err := os.MkdirTemp("", "flatrun-source-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create fetch directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }

	descriptor := source.Descriptor{
		Type: req.Type,
		Ref:  req.Ref,
		Params: map[string]string{
			"branch":  req.Branch,
			"subpath": req.Subpath,
		},
		Auth: auth,
	}

	result, err := provider.Fetch(ctx, descriptor, filepath.Join(tmp, "src"), func(line string) {
		log.Printf("[source:%s] %s", req.Type, line)
	})
	if err != nil {
		cleanup()
		return nil, err
	}

	composePath := docker.FindComposeFile(result.Dir)
	if composePath == "" {
		cleanup()
		return nil, fmt.Errorf("no compose file found in source")
	}

	content, err := os.ReadFile(composePath)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to read compose file from source: %w", err)
	}

	return &fetchedSource{
		dir:            result.Dir,
		composeName:    filepath.Base(composePath),
		composeContent: string(content),
		cleanup:        cleanup,
	}, nil
}

// resolveSourceAuth turns a stored credential id into provider auth. A missing id
// means an anonymous fetch (a public repository).
func (s *Server) resolveSourceAuth(credentialID string) (*source.Auth, error) {
	if credentialID == "" {
		return nil, nil
	}
	cred, err := s.credentialsManager.GetGenericCredential(credentialID)
	if err != nil {
		return nil, fmt.Errorf("failed to load source credential: %w", err)
	}
	return &source.Auth{
		Username: cred.Data["username"],
		Token:    cred.Data["token"],
	}, nil
}

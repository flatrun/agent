package api

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flatrun/agent/internal/envref"
)

// envReferencesFile keeps the unresolved form of variables that reference other
// deployments. .env and .env.flatrun hold the resolved values that compose and
// containers read.
const envReferencesFile = ".env.references"

// deploymentEnvSource reads deployments' variables from their directories.
type deploymentEnvSource struct{ s *Server }

func (d deploymentEnvSource) Raw(deployment string) (map[string]string, error) {
	if _, err := os.Stat(filepath.Join(d.s.config.DeploymentsPath, deployment)); err != nil {
		return nil, envref.ErrUnknownDeployment
	}
	vars, err := d.s.readPrincipalEnv(deployment)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(vars))
	for _, v := range vars {
		out[v.Key] = v.Value
	}
	return out, nil
}

// Host is the deployment's name, which is also its primary container's name on
// the shared networks.
func (d deploymentEnvSource) Host(deployment string) (string, error) {
	if _, err := os.Stat(filepath.Join(d.s.config.DeploymentsPath, deployment)); err != nil {
		return "", envref.ErrUnknownDeployment
	}
	return deployment, nil
}

func (s *Server) readEnvReferences(name string) (map[string]string, error) {
	content, err := os.ReadFile(filepath.Join(s.config.DeploymentsPath, name, envReferencesFile))
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for _, v := range parseEnvContent(string(content)) {
		refs[v.Key] = v.Value
	}
	return refs, nil
}

// resolveEnvVars writes the reference templates and returns the variables with
// every reference resolved, in their original order.
func (s *Server) resolveEnvVars(name string, envVars []EnvVar) ([]EnvVar, error) {
	raw := make(map[string]string, len(envVars))
	var refs []EnvVar
	for _, env := range envVars {
		raw[env.Key] = env.Value
		if envref.HasReference(env.Value) {
			refs = append(refs, env)
		}
	}

	refsPath := filepath.Join(s.config.DeploymentsPath, name, envReferencesFile)
	if len(refs) == 0 {
		if err := os.Remove(refsPath); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		return envVars, nil
	}

	resolved, err := envref.Resolver{Source: scopedEnvSource{deploymentEnvSource{s}, name}}.Resolve(name, raw)
	if err != nil {
		return nil, err
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Key < refs[j].Key })
	var content strings.Builder
	for _, ref := range refs {
		content.WriteString(fmt.Sprintf("%s=%s\n", ref.Key, ref.Value))
	}
	if err := os.WriteFile(refsPath, []byte(content.String()), 0600); err != nil {
		return nil, err
	}

	out := make([]EnvVar, len(envVars))
	for i, env := range envVars {
		out[i] = EnvVar{Key: env.Key, Value: resolved[env.Key]}
	}
	return out, nil
}

// refreshEnvReferences re-resolves a deployment's references so it starts with
// the current values of the variables it points at.
func (s *Server) refreshEnvReferences(name string) error {
	refs, err := s.readEnvReferences(name)
	if err != nil || len(refs) == 0 {
		return err
	}
	vars, err := s.readPrincipalEnv(name)
	if err != nil {
		return err
	}
	return s.writeEnvFile(name, vars)
}

func isEnvReferenceError(err error) bool {
	var refErr *envref.Error
	return errors.As(err, &refErr)
}

package api

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/flatrun/agent/internal/docker"
	"github.com/gin-gonic/gin"
)

// attachStoreToDeployment injects a store's connection details into another
// deployment's environment so its app can use the store directly. A managed
// store is reached inside the cluster by its container name on the shared
// object-storage network (which the app is joined to); an external store by its
// public URL. The deployment must be restarted for the change to take effect.
func (s *Server) attachStoreToDeployment(c *gin.Context) {
	dest, ok := s.findDestinationByName(c.Param("name"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "object store not found"})
		return
	}

	var req struct {
		Deployment string `json:"deployment" binding:"required"`
		Prefix     string `json:"prefix"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	prefix := req.Prefix
	if prefix == "" {
		prefix = "S3_"
	}

	deployDir := filepath.Join(s.config.DeploymentsPath, req.Deployment)
	composePath := filepath.Join(deployDir, "docker-compose.yml")
	composeContent, err := os.ReadFile(composePath)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "could not read deployment compose: " + err.Error()})
		return
	}

	cred, err := s.credentialsManager.GetGenericCredential(dest.CredentialID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store has no usable credential: " + err.Error()})
		return
	}

	endpoint := dest.Endpoint
	joinNetwork := ""
	if dest.Kind == "managed" && dest.Deployment != "" {
		port := managedStorePort
		if u, perr := url.Parse(dest.Endpoint); perr == nil && u.Port() != "" {
			port = u.Port()
		}
		endpoint = fmt.Sprintf("http://%s:%s", dest.Deployment, port)
		joinNetwork = objectStorageNetworkName(s.config)
	}

	vars := map[string]string{
		prefix + "ENDPOINT":          endpoint,
		prefix + "BUCKET":            dest.Bucket,
		prefix + "REGION":            dest.Region,
		prefix + "ACCESS_KEY_ID":     cred.Data["access_key_id"],
		prefix + "SECRET_ACCESS_KEY": cred.Data["secret_access_key"],
		prefix + "USE_PATH_STYLE":    strconv.FormatBool(dest.UsePathStyle),
	}

	// Upsert into the deployment's .env.flatrun, preserving anything already there.
	var envVars []EnvVar
	if existing, rerr := os.ReadFile(filepath.Join(deployDir, ".env.flatrun")); rerr == nil {
		envVars = parseEnvContent(string(existing))
	}
	index := make(map[string]int, len(envVars))
	for i, e := range envVars {
		index[e.Key] = i
	}
	for k, v := range vars {
		if i, ok := index[k]; ok {
			envVars[i].Value = v
		} else {
			envVars = append(envVars, EnvVar{Key: k, Value: v})
		}
	}
	if err := s.writeEnvFile(req.Deployment, envVars); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to write environment: " + err.Error()})
		return
	}

	// Wire the service to load that env file, and (managed) join the store's network.
	updated, err := docker.EnsureServiceEnvFile(string(composeContent), ".env.flatrun")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update compose: " + err.Error()})
		return
	}
	if joinNetwork != "" {
		if s.networksManager != nil {
			_ = s.networksManager.EnsureNetwork(joinNetwork)
		}
		if withNet, nerr := docker.AddNetworkToCompose(updated, joinNetwork); nerr == nil {
			updated = withNet
		}
	}
	if err := os.WriteFile(composePath, []byte(updated), 0644); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to write compose: " + err.Error()})
		return
	}

	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	c.JSON(http.StatusOK, gin.H{
		"message":  "Store attached. Restart the deployment to apply.",
		"keys":     keys,
		"endpoint": endpoint,
		"network":  joinNetwork,
	})
}

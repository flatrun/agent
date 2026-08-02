package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flatrun/agent/internal/backup"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

const (
	defaultManagedBucket     = "backups"
	defaultManagedRegion     = "us-east-1"
	managedProvisionAttempts = 20
	managedProvisionInterval = 500 * time.Millisecond
)

// provisionManagedObjectStoreRequest carries the S3 bootstrap contract for a
// deployment FlatRun runs. Credentials come either literally (a custom store
// whose secrets the caller knows) or by naming the deployment env vars that
// hold them (a template store, where the UI forwards the template's declared
// object_store contract). The agent stays free of any per-image knowledge.
type provisionManagedObjectStoreRequest struct {
	Deployment string `json:"deployment" binding:"required"`
	StoreName  string `json:"store_name"`
	Bucket     string `json:"bucket"`

	AccessKeyEnv string `json:"access_key_env"`
	SecretKeyEnv string `json:"secret_key_env"`
	AccessKey    string `json:"access_key"`
	SecretKey    string `json:"secret_key"`

	APIPort      int    `json:"api_port"`
	Region       string `json:"region"`
	UsePathStyle *bool  `json:"use_path_style"`
}

// provisionManagedObjectStore turns a deployment FlatRun runs into a connected
// store: it resolves the store's S3 credentials, stores a credential, ensures
// the bucket exists, and registers a managed backup destination pointing at the
// deployment. It is the auto-register step behind the "Deploy a local store"
// and "use an existing deployment" flows.
func (s *Server) provisionManagedObjectStore(c *gin.Context) {
	if s.backupManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Backup manager not enabled"})
		return
	}

	var req provisionManagedObjectStoreRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.APIPort <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "api_port is required"})
		return
	}

	accessKey, secretKey, err := s.resolveStoreCredentials(req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	storeName := strings.TrimSpace(req.StoreName)
	if storeName == "" {
		storeName = req.Deployment
	}
	bucket := strings.TrimSpace(req.Bucket)
	if bucket == "" {
		bucket = defaultManagedBucket
	}
	region := strings.TrimSpace(req.Region)
	if region == "" {
		region = defaultManagedRegion
	}
	usePathStyle := true
	if req.UsePathStyle != nil {
		usePathStyle = *req.UsePathStyle
	}

	// A create returns before the store finishes starting, so wait for the
	// container to become reachable before making the bucket on it.
	endpoint, err := s.waitForManagedEndpoint(req.Deployment, req.APIPort)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("object store %q is not reachable yet: %v", req.Deployment, err)})
		return
	}

	cred, err := s.credentialsManager.CreateGenericCredential(storeName+"-keys", models.CredentialKindS3, map[string]string{
		"access_key_id":     accessKey,
		"secret_access_key": secretKey,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	enabled := true
	dest := config.BackupDestination{
		Name:         storeName,
		Type:         "s3",
		Kind:         "managed",
		Deployment:   req.Deployment,
		Endpoint:     endpoint,
		Region:       region,
		Bucket:       bucket,
		CredentialID: cred.ID,
		UsePathStyle: usePathStyle,
		Enabled:      &enabled,
	}

	if err := s.ensureManagedBucket(c, dest); err != nil {
		_ = s.credentialsManager.DeleteGenericCredential(cred.ID)
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("could not create bucket on %q: %v", storeName, err)})
		return
	}

	updated := append(append([]config.BackupDestination{}, s.config.Backup.Destinations...), dest)
	outcome, err := s.applyConfigUpdate("backup.destinations", updated)
	if err != nil {
		_ = s.credentialsManager.DeleteGenericCredential(cred.ID)
		respondAPIError(c, err)
		return
	}

	resp := gin.H{
		"message":     "Managed object store connected",
		"destination": dest,
		"credential":  cred,
		"applied":     outcome.Applied,
	}
	if outcome.ApplyErr != nil {
		resp["apply_error"] = outcome.ApplyErr.Error()
	}
	c.JSON(http.StatusCreated, resp)
}

// resolveStoreCredentials returns the S3 access key and secret for a store,
// taken from the request when supplied directly, otherwise read from the
// deployment env vars the request names.
func (s *Server) resolveStoreCredentials(req provisionManagedObjectStoreRequest) (string, string, error) {
	if req.AccessKey != "" && req.SecretKey != "" {
		return req.AccessKey, req.SecretKey, nil
	}
	if req.AccessKeyEnv == "" || req.SecretKeyEnv == "" {
		return "", "", fmt.Errorf("provide S3 credentials, or the env keys that hold them")
	}

	env, err := s.readDeploymentEnvMap(req.Deployment)
	if err != nil {
		return "", "", err
	}
	access, secret := env[req.AccessKeyEnv], env[req.SecretKeyEnv]
	if access == "" || secret == "" {
		return "", "", fmt.Errorf("object store %q has no S3 credentials in %s / %s", req.Deployment, req.AccessKeyEnv, req.SecretKeyEnv)
	}
	return access, secret, nil
}

// readDeploymentEnvMap reads a deployment's generated env into a key/value map.
// A template writes its env to whatever file it declares (MinIO uses .env,
// others use .env.flatrun), so every env file in the deployment is read and
// merged rather than assuming one fixed name. The FlatRun-managed .env.flatrun
// wins where a key appears in more than one.
func (s *Server) readDeploymentEnvMap(name string) (map[string]string, error) {
	dir := filepath.Join(s.config.DeploymentsPath, name)
	matches, _ := filepath.Glob(filepath.Join(dir, ".env*"))

	env := make(map[string]string)
	found := false
	for _, path := range matches {
		base := filepath.Base(path)
		if strings.HasSuffix(base, ".example") || strings.HasSuffix(base, ".sample") {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		found = true
		for _, v := range parseEnvContent(string(content)) {
			if base == ".env.flatrun" || env[v.Key] == "" {
				env[v.Key] = v.Value
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("could not read object store environment in %s", dir)
	}
	return env, nil
}

// waitForManagedEndpoint polls for the deployment's container to become
// reachable and returns the address its S3 API is served on.
func (s *Server) waitForManagedEndpoint(deployment string, apiPort int) (string, error) {
	if s.manager == nil {
		return "", fmt.Errorf("docker manager unavailable")
	}
	var lastErr error
	for i := 0; i < managedProvisionAttempts; i++ {
		ip, err := s.manager.ContainerPrimaryIP(deployment, objectStorageNetworkName(s.config))
		if err == nil && ip != "" {
			return fmt.Sprintf("http://%s:%d", ip, apiPort), nil
		}
		lastErr = err
		time.Sleep(managedProvisionInterval)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("container did not report an address")
	}
	return "", lastErr
}

// ensureManagedBucket makes the destination's bucket if it is missing. The
// container reports an address a moment before its S3 API is actually serving,
// so the bucket call is retried over a short warmup window.
func (s *Server) ensureManagedBucket(c *gin.Context, dest config.BackupDestination) error {
	store, err := s.buildStore(dest)
	if err != nil {
		return err
	}
	s3store, ok := store.(*backup.S3Store)
	if !ok {
		return nil
	}

	var lastErr error
	for i := 0; i < managedProvisionAttempts; i++ {
		if err := s3store.EnsureBucket(c.Request.Context()); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(managedProvisionInterval)
	}
	return lastErr
}

package api

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/flatrun/agent/internal/backup"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

// managedStorePort is the fallback S3 API port used when a managed
// destination's stored endpoint carries none. A managed object store is reached
// over the shared object-storage network (see objectStorageNetworkName); the
// agent, a host process, dials the container's address on that network.
const managedStorePort = "9000"

// buildStore resolves a destination plus its referenced credential into a
// ready-to-use remote Store.
func (s *Server) buildStore(dest config.BackupDestination) (backup.Store, error) {
	switch dest.Type {
	case "s3", "":
		cred, err := s.credentialsManager.GetGenericCredential(dest.CredentialID)
		if err != nil {
			return nil, fmt.Errorf("destination %q: %w", dest.Name, err)
		}
		if cred.Kind != models.CredentialKindS3 {
			return nil, fmt.Errorf("destination %q: credential %q is not an s3 credential", dest.Name, dest.CredentialID)
		}
		endpoint := dest.Endpoint
		if dest.Kind == "managed" && dest.Deployment != "" {
			if resolved, err := s.resolveManagedEndpoint(dest); err == nil && resolved != "" {
				endpoint = resolved
			}
		}
		return backup.NewS3Store(backup.S3Config{
			Name:         dest.Name,
			Endpoint:     endpoint,
			Region:       dest.Region,
			Bucket:       dest.Bucket,
			Prefix:       dest.Prefix,
			AccessKeyID:  cred.Data["access_key_id"],
			SecretKey:    cred.Data["secret_access_key"],
			UsePathStyle: dest.UsePathStyle,
		})
	default:
		return nil, fmt.Errorf("destination %q: unknown type %q", dest.Name, dest.Type)
	}
}

// resolveManagedEndpoint returns the address a managed store is reachable at
// right now. A managed object store only exposes its port on an internal
// compose network and its container IP changes across recreates, so the
// endpoint is resolved live from the deployment rather than trusted from
// stored config. The scheme and port of the stored endpoint are preserved.
func (s *Server) resolveManagedEndpoint(dest config.BackupDestination) (string, error) {
	if s.manager == nil {
		return "", fmt.Errorf("docker manager unavailable")
	}
	ip, err := s.manager.ContainerPrimaryIP(dest.Deployment, objectStorageNetworkName(s.config))
	if err != nil {
		return "", err
	}

	u, err := url.Parse(dest.Endpoint)
	if err != nil || u.Host == "" {
		return "http://" + ip + ":" + managedStorePort, nil
	}
	port := u.Port()
	if port == "" {
		port = managedStorePort
	}
	u.Host = ip + ":" + port
	return u.String(), nil
}

// applyBackupDestinations rebuilds the backup manager's remote stores from the
// current config. It is called at startup and by the runtime applier when the
// destinations config changes.
func (s *Server) applyBackupDestinations() error {
	if s.backupManager == nil {
		return nil
	}

	var stores []backup.Store
	var problems []string
	for _, dest := range s.config.Backup.Destinations {
		if !dest.IsEnabled() {
			continue
		}
		store, err := s.buildStore(dest)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		stores = append(stores, store)
	}

	s.backupManager.SetRemotes(stores)

	if len(problems) > 0 {
		return fmt.Errorf("backup destinations not fully applied: %s", strings.Join(problems, "; "))
	}
	log.Printf("Backup destinations applied: %d remote(s) active", len(stores))
	return nil
}

// testBackupDestination validates connectivity to a destination by writing and
// deleting a small probe object. The destination may be given inline (for the
// configure-then-test flow) or by name for an already-saved destination.
func (s *Server) testBackupDestination(c *gin.Context) {
	if s.backupManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Backup manager not enabled"})
		return
	}

	var dest config.BackupDestination
	if err := c.ShouldBindJSON(&dest); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if dest.Name == "" && dest.Bucket == "" {
		if named, ok := s.findDestinationByName(c.Query("name")); ok {
			dest = named
		}
	}

	store, err := s.buildStore(dest)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	probeKey := ".flatrun/connectivity-check"
	probe := []byte("flatrun")
	if err := store.Put(ctx, probeKey, bytes.NewReader(probe), int64(len(probe))); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}
	_ = store.Delete(ctx, probeKey)

	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Destination reachable and writable"})
}

func (s *Server) listBackupDestinations(c *gin.Context) {
	dests := s.config.Backup.Destinations
	if dests == nil {
		dests = []config.BackupDestination{}
	}
	c.JSON(http.StatusOK, gin.H{"destinations": dests})
}

func (s *Server) findDestinationByName(name string) (config.BackupDestination, bool) {
	for _, d := range s.config.Backup.Destinations {
		if d.Name == name {
			return d, true
		}
	}
	return config.BackupDestination{}, false
}

package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
)

func (s *Server) listConfig(c *gin.Context) {
	entries := config.Walk(s.config)
	c.JSON(http.StatusOK, gin.H{
		"config":  entries,
		"runtime": s.runtimeConfigKeys(),
	})
}

func (s *Server) getConfigKey(c *gin.Context) {
	key := normalizeConfigKey(c.Param("key"))
	entry, err := config.Get(s.config, key)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"entry":   entry,
		"runtime": s.runtimeConfigKeys()[key],
	})
}

func (s *Server) updateConfigKey(c *gin.Context) {
	key := normalizeConfigKey(c.Param("key"))

	var req struct {
		Value interface{} `json:"value"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := config.Set(s.config, key, req.Value); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if s.configPath != "" {
		if err := config.Save(s.config, s.configPath); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "value updated in memory but not persisted: " + err.Error()})
			return
		}
	}

	applied := false
	var applyErr error
	if apply, ok := s.runtimeAppliers()[key]; ok {
		applyErr = apply(s)
		applied = applyErr == nil
	}

	entry, _ := config.Get(s.config, key)
	resp := gin.H{
		"entry":   entry,
		"applied": applied,
	}
	if applyErr != nil {
		resp["apply_error"] = applyErr.Error()
	}
	c.JSON(http.StatusOK, resp)
}

func (s *Server) runtimeAppliers() map[string]func(*Server) error {
	applyDetectorThresholds := func(srv *Server) error {
		if srv.securityManager == nil {
			return nil
		}
		srv.securityManager.SetDetectorThresholds(
			srv.config.Security.RateThreshold,
			srv.config.Security.NotFoundThreshold,
			srv.config.Security.AuthFailureThreshold,
			srv.config.Security.UniquePathsThreshold,
			srv.config.Security.RepeatedHitsThreshold,
			srv.config.Security.DetectionWindow,
		)
		return nil
	}
	regenerateSecurityScripts := func(srv *Server) error {
		if !srv.config.Security.Enabled {
			return nil
		}
		if !srv.infraManager.IsNginxRunning() {
			return fmt.Errorf("value saved but nginx is not running; regenerate security scripts to apply")
		}
		_, err := srv.infraManager.RefreshSecurityScripts()
		return err
	}
	return map[string]func(*Server) error{
		"cleanup.timeout": func(srv *Server) error {
			srv.manager.SetCleanupTimeout(srv.config.Cleanup.Timeout)
			return nil
		},
		"security.rate_threshold":          applyDetectorThresholds,
		"security.not_found_threshold":     applyDetectorThresholds,
		"security.auth_failure_threshold":  applyDetectorThresholds,
		"security.unique_paths_threshold":  applyDetectorThresholds,
		"security.repeated_hits_threshold": applyDetectorThresholds,
		"security.detection_window":        applyDetectorThresholds,
		"security.trusted_proxies":         regenerateSecurityScripts,
		"security.trust_cf_header":         regenerateSecurityScripts,
	}
}

func (s *Server) runtimeConfigKeys() map[string]bool {
	keys := make(map[string]bool)
	for k := range s.runtimeAppliers() {
		keys[k] = true
	}
	return keys
}

func normalizeConfigKey(raw string) string {
	return strings.Trim(raw, "/")
}

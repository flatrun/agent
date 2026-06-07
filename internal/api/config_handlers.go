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

	if planRequested(c) {
		s.planConfigUpdate(c, key, req.Value)
		return
	}

	outcome, err := s.applyConfigUpdate(key, req.Value)
	if err != nil {
		respondAPIError(c, err)
		return
	}

	resp := gin.H{
		"entry":   outcome.Entry,
		"applied": outcome.Applied,
	}
	if outcome.ApplyErr != nil {
		resp["apply_error"] = outcome.ApplyErr.Error()
	}
	c.JSON(http.StatusOK, resp)
}

type configUpdateOutcome struct {
	Entry    config.Entry
	Applied  bool
	ApplyErr error
}

func (s *Server) applyConfigUpdate(key string, value interface{}) (*configUpdateOutcome, error) {
	if err := config.Set(s.config, key, value); err != nil {
		return nil, apiErrf(http.StatusBadRequest, "%s", err.Error())
	}

	if s.configPath != "" {
		if err := config.Save(s.config, s.configPath); err != nil {
			return nil, apiErrf(http.StatusInternalServerError, "value updated in memory but not persisted: %s", err.Error())
		}
	}

	applied := false
	var applyErr error
	if apply, ok := s.runtimeAppliers()[key]; ok {
		applyErr = apply(s)
		applied = applyErr == nil
	}

	entry, _ := config.Get(s.config, key)
	return &configUpdateOutcome{Entry: entry, Applied: applied, ApplyErr: applyErr}, nil
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

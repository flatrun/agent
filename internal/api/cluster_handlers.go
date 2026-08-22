package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/cluster"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/version"
	"github.com/gin-gonic/gin"
)

func (s *Server) clusterStatus(c *gin.Context) {
	mgr := s.getClusterManager()
	if mgr == nil {
		c.JSON(http.StatusOK, gin.H{
			"enabled": false,
		})
		return
	}

	peers := mgr.ListPeers()
	c.JSON(http.StatusOK, gin.H{
		"enabled":     true,
		"server_name": mgr.ServerName(),
		"peer_count":  len(peers),
		"version":     version.Get(),
	})
}

type clusterSetupRequest struct {
	ServerName   string `json:"server_name" binding:"required"`
	AdvertiseURL string `json:"advertise_url" binding:"required"`
}

func (s *Server) clusterSetup(c *gin.Context) {
	var req clusterSetupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	req.ServerName = strings.TrimSpace(req.ServerName)
	req.AdvertiseURL = strings.TrimRight(strings.TrimSpace(req.AdvertiseURL), "/")
	if req.ServerName == "" || strings.ContainsAny(req.ServerName, "/\\") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Server name must not be empty or contain slashes"})
		return
	}
	parsedURL, err := url.ParseRequestURI(req.AdvertiseURL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Advertise URL must be a valid HTTP or HTTPS URL"})
		return
	}

	s.clusterMu.Lock()
	defer s.clusterMu.Unlock()
	if s.clusterManager != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "Cluster is already enabled"})
		return
	}

	clusterDB, err := cluster.NewDB(s.config.DeploymentsPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to initialize cluster database: %v", err)})
		return
	}
	previous := s.config.Cluster
	s.config.Cluster.Enabled = true
	s.config.Cluster.ServerName = req.ServerName
	s.config.Cluster.AdvertiseURL = req.AdvertiseURL
	if s.config.Cluster.HealthInterval == "" {
		s.config.Cluster.HealthInterval = "30s"
	}
	if s.config.Cluster.RequestTimeout == "" {
		s.config.Cluster.RequestTimeout = "10s"
	}
	if s.configPath != "" {
		if err := config.Save(s.config, s.configPath); err != nil {
			s.config.Cluster = previous
			_ = clusterDB.Close()
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to save cluster configuration: %v", err)})
			return
		}
	}

	healthInterval, _ := time.ParseDuration(s.config.Cluster.HealthInterval)
	requestTimeout, _ := time.ParseDuration(s.config.Cluster.RequestTimeout)
	if healthInterval <= 0 {
		healthInterval = 30 * time.Second
	}
	if requestTimeout <= 0 {
		requestTimeout = 10 * time.Second
	}
	mgr := cluster.NewManager(clusterDB, req.ServerName, healthInterval, requestTimeout, s.config.Auth.JWTSecret)
	if err := mgr.Start(context.Background()); err != nil {
		_ = clusterDB.Close()
		s.config.Cluster = previous
		if s.configPath != "" {
			_ = config.Save(s.config, s.configPath)
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to start cluster: %v", err)})
		return
	}
	s.clusterManager = mgr

	c.JSON(http.StatusOK, gin.H{
		"enabled":       true,
		"server_name":   req.ServerName,
		"advertise_url": req.AdvertiseURL,
		"peer_count":    0,
		"version":       version.Get(),
	})
}

func (s *Server) getClusterManager() *cluster.Manager {
	s.clusterMu.RLock()
	defer s.clusterMu.RUnlock()
	return s.clusterManager
}

func (s *Server) clusterListPeers(c *gin.Context) {
	mgr := s.getClusterManager()
	if mgr == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cluster is not enabled"})
		return
	}

	peers := mgr.ListPeers()
	c.JSON(http.StatusOK, gin.H{"peers": peers})
}

func (s *Server) clusterInvite(c *gin.Context) {
	mgr := s.getClusterManager()
	if mgr == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cluster is not enabled"})
		return
	}

	actor := auth.GetActorFromContext(c)
	if actor == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Not authenticated"})
		return
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate invite token"})
		return
	}
	token := base64.URLEncoding.EncodeToString(tokenBytes)
	tokenHash := cluster.HashToken(token)

	invite := &cluster.Invite{
		TokenHash: tokenHash,
		Status:    "pending",
		CreatedBy: actor.UserID,
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}

	if _, err := mgr.DB().CreateInvite(invite); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create invite"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"invite_token": token,
		"expires_at":   invite.ExpiresAt,
	})
}

func (s *Server) clusterAccept(c *gin.Context) {
	mgr := s.getClusterManager()
	if mgr == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cluster is not enabled"})
		return
	}

	var req struct {
		InviteToken string `json:"invite_token" binding:"required"`
		PeerURL     string `json:"peer_url" binding:"required"`
		CallbackURL string `json:"callback_url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	callbackURL := req.CallbackURL
	if callbackURL == "" {
		callbackURL = s.config.Cluster.AdvertiseURL
	}
	if callbackURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "No callback URL available. Set cluster.advertise_url in config or pass callback_url in the request.",
		})
		return
	}

	apiKeyBytes := make([]byte, 32)
	if _, err := rand.Read(apiKeyBytes); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate API key"})
		return
	}
	ourAPIKeyForThem := base64.URLEncoding.EncodeToString(apiKeyBytes)

	exchangeReq := exchangeRequest{
		InviteToken: req.InviteToken,
		URL:         callbackURL,
		APIKey:      ourAPIKeyForThem,
		Name:        mgr.ServerName(),
	}

	body, err := json.Marshal(exchangeReq)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encode exchange request"})
		return
	}

	tempClient := cluster.NewClient(req.PeerURL, "", 10*time.Second)
	respData, status, err := tempClient.Post(c.Request.Context(), "/api/cluster/exchange", body)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("Failed to contact peer: %v", err)})
		return
	}
	if status != http.StatusOK {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("Peer rejected exchange: %s", string(respData))})
		return
	}

	var exchangeResp exchangeResponse
	if err := json.Unmarshal(respData, &exchangeResp); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse peer response"})
		return
	}

	if err := mgr.AddPeer(exchangeResp.Name, req.PeerURL, exchangeResp.APIKey); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to store peer: %v", err)})
		return
	}

	if s.authManager != nil {
		s.createClusterAPIKey(ourAPIKeyForThem, exchangeResp.Name)
	}

	c.JSON(http.StatusOK, gin.H{
		"peer_name": exchangeResp.Name,
		"peer_url":  req.PeerURL,
		"status":    "peered",
	})
}

type exchangeRequest struct {
	InviteToken string `json:"invite_token"`
	URL         string `json:"url"`
	APIKey      string `json:"api_key"`
	Name        string `json:"name"`
}

type exchangeResponse struct {
	APIKey string `json:"api_key"`
	Name   string `json:"name"`
}

func (s *Server) clusterExchange(c *gin.Context) {
	mgr := s.getClusterManager()
	if mgr == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cluster is not enabled"})
		return
	}

	var req exchangeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	tokenHash := cluster.HashToken(req.InviteToken)
	invite, err := mgr.DB().GetInviteByHash(tokenHash)
	if err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "Invalid or expired invite token"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to look up invite"})
		return
	}

	if invite.Status != "pending" {
		c.JSON(http.StatusConflict, gin.H{"error": "Invite has already been used"})
		return
	}

	if time.Now().After(invite.ExpiresAt) {
		c.JSON(http.StatusGone, gin.H{"error": "Invite has expired"})
		return
	}

	if err := mgr.DB().ConsumeInvite(tokenHash, req.Name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to consume invite"})
		return
	}

	apiKeyBytes := make([]byte, 32)
	if _, err := rand.Read(apiKeyBytes); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate API key"})
		return
	}
	ourAPIKeyForThem := base64.URLEncoding.EncodeToString(apiKeyBytes)

	if err := mgr.AddPeer(req.Name, req.URL, req.APIKey); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to store peer: %v", err)})
		return
	}

	if s.authManager != nil {
		s.createClusterAPIKey(ourAPIKeyForThem, req.Name)
	}

	c.JSON(http.StatusOK, exchangeResponse{
		APIKey: ourAPIKeyForThem,
		Name:   mgr.ServerName(),
	})
}

func (s *Server) createClusterAPIKey(rawKey, peerName string) {
	if s.authManager == nil {
		return
	}
	_, _ = s.authManager.CreateAPIKeyFromRaw(
		rawKey,
		1,
		fmt.Sprintf("cluster-peer-%s", peerName),
		fmt.Sprintf("Auto-generated API key for cluster peer %s", peerName),
		auth.Role(""),
		[]string{
			auth.PermClusterRead.String(),
			auth.PermDeploymentsRead.String(),
			auth.PermDeploymentsWrite.String(),
			auth.PermContainersRead.String(),
			auth.PermContainersWrite.String(),
			auth.PermSystemRead.String(),
			auth.PermTrafficRead.String(),
		},
		nil,
		time.Time{},
	)
}

func (s *Server) clusterRemovePeer(c *gin.Context) {
	mgr := s.getClusterManager()
	if mgr == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cluster is not enabled"})
		return
	}

	name := c.Param("name")
	if err := mgr.RemovePeer(name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	s.revokeClusterAPIKey(name)

	c.JSON(http.StatusOK, gin.H{"status": "removed", "peer": name})
}

func (s *Server) revokeClusterAPIKey(peerName string) {
	if s.authManager == nil {
		return
	}
	keys, err := s.authManager.GetAllAPIKeys()
	if err != nil {
		return
	}
	name := fmt.Sprintf("cluster-peer-%s", peerName)
	for _, key := range keys {
		if key.Name == name {
			_ = s.authManager.DeactivateAPIKey(key.ID)
		}
	}
}

func (s *Server) clusterProxy(c *gin.Context) {
	mgr := s.getClusterManager()
	if mgr == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cluster is not enabled"})
		return
	}

	name := c.Param("name")
	path := c.Param("path")

	client, err := mgr.GetPeer(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	var body io.Reader
	if c.Request.Body != nil {
		body = c.Request.Body
	}

	data, status, headers, err := client.Forward(c.Request.Context(), c.Request.Method, "/api"+path, body)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("Failed to proxy request: %v", err)})
		return
	}

	for k, v := range headers {
		if k != "Content-Length" && k != "Transfer-Encoding" {
			c.Header(k, v)
		}
	}
	c.Data(status, "application/json", data)
}

func (s *Server) clusterAggregateDeployments(c *gin.Context) {
	mgr := s.getClusterManager()
	if mgr == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cluster is not enabled"})
		return
	}

	deployments, err := s.manager.ListDeployments()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	localData, err := json.Marshal(NewList(deployments, "deployments").Also("path", s.manager.BasePath()))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to marshal local deployments"})
		return
	}

	result := cluster.AggregateFromPeers(c.Request.Context(), localData, mgr, "/api/deployments")
	c.JSON(http.StatusOK, result)
}

func (s *Server) clusterAggregateStats(c *gin.Context) {
	mgr := s.getClusterManager()
	if mgr == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cluster is not enabled"})
		return
	}

	localStats := gin.H{
		"status":  "healthy",
		"version": version.Get(),
	}

	localData, err := json.Marshal(localStats)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to marshal local stats"})
		return
	}

	result := cluster.AggregateFromPeers(c.Request.Context(), localData, mgr, "/api/health")
	c.JSON(http.StatusOK, result)
}

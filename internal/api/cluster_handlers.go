package api

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/cluster"
	"github.com/flatrun/agent/pkg/version"
	"github.com/gin-gonic/gin"
)

func (s *Server) clusterStatus(c *gin.Context) {
	if s.clusterManager == nil {
		c.JSON(http.StatusOK, gin.H{
			"enabled": false,
		})
		return
	}

	peers := s.clusterManager.ListPeers()
	c.JSON(http.StatusOK, gin.H{
		"enabled":     true,
		"server_name": s.clusterManager.ServerName(),
		"peer_count":  len(peers),
		"version":     version.Get(),
	})
}

func (s *Server) clusterListPeers(c *gin.Context) {
	if s.clusterManager == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cluster is not enabled"})
		return
	}

	peers := s.clusterManager.ListPeers()
	c.JSON(http.StatusOK, gin.H{"peers": peers})
}

func (s *Server) clusterInvite(c *gin.Context) {
	if s.clusterManager == nil {
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

	if _, err := s.clusterManager.DB().CreateInvite(invite); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create invite"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"invite_token": token,
		"expires_at":   invite.ExpiresAt,
	})
}

func (s *Server) clusterAccept(c *gin.Context) {
	if s.clusterManager == nil {
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
		Name:        s.clusterManager.ServerName(),
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

	if err := s.clusterManager.AddPeer(exchangeResp.Name, req.PeerURL, exchangeResp.APIKey); err != nil {
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
	if s.clusterManager == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cluster is not enabled"})
		return
	}

	var req exchangeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	tokenHash := cluster.HashToken(req.InviteToken)
	invite, err := s.clusterManager.DB().GetInviteByHash(tokenHash)
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

	if err := s.clusterManager.DB().ConsumeInvite(tokenHash, req.Name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to consume invite"})
		return
	}

	apiKeyBytes := make([]byte, 32)
	if _, err := rand.Read(apiKeyBytes); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate API key"})
		return
	}
	ourAPIKeyForThem := base64.URLEncoding.EncodeToString(apiKeyBytes)

	if err := s.clusterManager.AddPeer(req.Name, req.URL, req.APIKey); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to store peer: %v", err)})
		return
	}

	if s.authManager != nil {
		s.createClusterAPIKey(ourAPIKeyForThem, req.Name)
	}

	c.JSON(http.StatusOK, exchangeResponse{
		APIKey: ourAPIKeyForThem,
		Name:   s.clusterManager.ServerName(),
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
		auth.RoleAdmin,
		nil,
		nil,
		time.Time{},
	)
}

func (s *Server) clusterRemovePeer(c *gin.Context) {
	if s.clusterManager == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cluster is not enabled"})
		return
	}

	name := c.Param("name")
	if err := s.clusterManager.RemovePeer(name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "removed", "peer": name})
}

func (s *Server) clusterProxy(c *gin.Context) {
	if s.clusterManager == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cluster is not enabled"})
		return
	}

	name := c.Param("name")
	path := c.Param("path")

	client, err := s.clusterManager.GetPeer(name)
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
	if s.clusterManager == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cluster is not enabled"})
		return
	}

	deployments, err := s.manager.ListDeployments()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	localData, err := json.Marshal(deployments)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to marshal local deployments"})
		return
	}

	result := cluster.AggregateFromPeers(c.Request.Context(), localData, s.clusterManager, "/api/deployments")
	c.JSON(http.StatusOK, result)
}

func (s *Server) clusterAggregateStats(c *gin.Context) {
	if s.clusterManager == nil {
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

	result := cluster.AggregateFromPeers(c.Request.Context(), localData, s.clusterManager, "/api/health")
	c.JSON(http.StatusOK, result)
}

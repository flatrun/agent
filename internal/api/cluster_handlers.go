package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/capacity"
	"github.com/flatrun/agent/internal/cluster"
	"github.com/flatrun/agent/internal/orchestrator"
	"github.com/flatrun/agent/internal/routing"
	"github.com/flatrun/agent/internal/system"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/version"
	"github.com/gin-gonic/gin"
)

type clusterProviderOption struct {
	ID        string `json:"id"`
	Active    bool   `json:"active"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

type clusterProvidersResponse struct {
	Orchestrators []clusterProviderOption `json:"orchestrators"`
	Routing       []clusterProviderOption `json:"routing"`
	K3s           config.K3sConfig        `json:"k3s"`
}

type clusterCapacityClaimResponse struct {
	Enabled     bool                      `json:"enabled"`
	Reason      string                    `json:"reason"`
	Node        orchestrator.NodeIdentity `json:"node"`
	Constraint  string                    `json:"constraint,omitempty"`
	MaxCPU      float64                   `json:"max_cpu,omitempty"`
	MaxMemory   uint64                    `json:"max_memory,omitempty"`
	MaxReplicas int                       `json:"max_replicas,omitempty"`
}

func (s *Server) clusterCapacityClaim(c *gin.Context) {
	peer, err := clusterPeerFromActor(auth.GetActorFromContext(c))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	mgr := s.getClusterManager()
	if mgr == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cluster is not enabled"})
		return
	}
	policy, err := mgr.DB().GetPeerPolicy(peer)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Capacity has not been granted to this peer"})
		return
	}
	grant, ok := capacityOfferGrant(*policy)
	if !ok {
		c.JSON(http.StatusOK, clusterCapacityClaimResponse{Reason: "This server has not permitted Fleet workloads from this peer"})
		return
	}
	provider, err := orchestrator.NewSwarmProviderFromEnv()
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	defer provider.Close()
	label := capacityNodeLabel(peer)
	node, err := provider.EnsureLocalNodeLabel(c, label, "true")
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, clusterCapacityClaimResponse{
		Enabled: true, Reason: "This node permits Fleet workloads from this peer", Node: node,
		Constraint: "node.labels." + label + "==true",
		MaxCPU:     grant.MaxCPU, MaxMemory: grant.MaxMemory, MaxReplicas: grant.MaxReplicas,
	})
}

func clusterPeerFromActor(actor *auth.ActorContext) (string, error) {
	if actor == nil || actor.APIKey == nil {
		return "", fmt.Errorf("A Fleet peer credential is required")
	}
	peer, ok := strings.CutPrefix(actor.APIKey.Name, "cluster-peer-")
	if !ok || strings.TrimSpace(peer) == "" {
		return "", fmt.Errorf("A Fleet peer credential is required")
	}
	return peer, nil
}

func capacityOfferGrant(policy cluster.PeerPolicy) (cluster.Grant, bool) {
	for _, grant := range policy.Grants {
		if grant.Capability == cluster.CapabilityCapacityOffer {
			return grant, true
		}
	}
	return cluster.Grant{}, false
}

func capacityNodeLabel(peer string) string {
	sum := sha256.Sum256([]byte(peer))
	return "flatrun.capacity." + hex.EncodeToString(sum[:6])
}

func (s *Server) clusterProviders(c *gin.Context) {
	orchestratorID := s.config.Cluster.Orchestrator
	if orchestratorID == "" {
		orchestratorID = string(orchestrator.ProviderStandalone)
	}
	routingID := s.config.Cluster.Routing
	if routingID == "" {
		routingID = string(routing.ProviderNginx)
	}

	c.JSON(http.StatusOK, clusterProvidersResponse{
		Orchestrators: []clusterProviderOption{
			s.orchestratorOption(c, orchestrator.ProviderStandalone, orchestratorID),
			s.orchestratorOption(c, orchestrator.ProviderSwarm, orchestratorID),
			s.orchestratorOption(c, orchestrator.ProviderK3s, orchestratorID),
		},
		Routing: []clusterProviderOption{
			s.routingOption(c, routing.ProviderNginx, routingID),
			s.routingOption(c, routing.ProviderTraefik, routingID),
		},
		K3s: s.config.Cluster.K3s,
	})
}

func (s *Server) orchestratorOption(c *gin.Context, id orchestrator.ProviderID, active string) clusterProviderOption {
	option := clusterProviderOption{ID: string(id), Active: active == string(id)}
	if err := s.checkOrchestrator(c, id); err != nil {
		option.Reason = err.Error()
		return option
	}
	option.Available = true
	return option
}

func (s *Server) routingOption(c *gin.Context, id routing.ProviderID, active string) clusterProviderOption {
	option := clusterProviderOption{ID: string(id), Active: active == string(id)}
	if err := s.checkRouting(c, id); err != nil {
		option.Reason = err.Error()
		return option
	}
	option.Available = true
	return option
}

func (s *Server) checkOrchestrator(ctx context.Context, id orchestrator.ProviderID) error {
	if s.probeOrchestrator != nil {
		return s.probeOrchestrator(ctx, id)
	}
	switch id {
	case orchestrator.ProviderStandalone:
		return nil
	case orchestrator.ProviderSwarm:
		provider, err := orchestrator.NewSwarmProviderFromEnv()
		if err != nil {
			return err
		}
		defer provider.Close()
		return provider.Ready(ctx)
	case orchestrator.ProviderK3s:
		provider := orchestrator.NewK3sProvider(s.config.Cluster.K3s.Kubeconfig, s.config.Cluster.K3s.Namespace)
		return provider.Ready(ctx)
	default:
		return fmt.Errorf("orchestrator %q is not supported", id)
	}
}

func (s *Server) checkRouting(ctx context.Context, id routing.ProviderID) error {
	if s.probeRouting != nil {
		return s.probeRouting(ctx, id)
	}
	switch id {
	case routing.ProviderNginx:
		if !s.config.Nginx.Enabled {
			return fmt.Errorf("Nginx is not enabled")
		}
		return nil
	case routing.ProviderTraefik:
		return orchestrator.NewK3sProvider(s.config.Cluster.K3s.Kubeconfig, s.config.Cluster.K3s.Namespace).Ready(ctx)
	default:
		return fmt.Errorf("routing provider %q is not supported", id)
	}
}

type updateClusterProvidersRequest struct {
	Orchestrator string           `json:"orchestrator" binding:"required"`
	Routing      string           `json:"routing" binding:"required"`
	K3s          config.K3sConfig `json:"k3s"`
}

func (s *Server) updateClusterProviders(c *gin.Context) {
	var req updateClusterProvidersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	orchestratorID := orchestrator.ProviderID(strings.TrimSpace(req.Orchestrator))
	routingID := routing.ProviderID(strings.TrimSpace(req.Routing))
	if (orchestratorID == orchestrator.ProviderK3s && routingID != routing.ProviderTraefik) ||
		(orchestratorID == orchestrator.ProviderSwarm && routingID != routing.ProviderNginx) ||
		(orchestratorID == orchestrator.ProviderStandalone && routingID != routing.ProviderNginx) {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "The selected orchestrator and routing providers are incompatible"})
		return
	}
	var orchestratorErr error
	if orchestratorID == orchestrator.ProviderK3s && s.probeOrchestrator == nil {
		orchestratorErr = orchestrator.NewK3sProvider(req.K3s.Kubeconfig, req.K3s.Namespace).Ready(c)
	} else {
		orchestratorErr = s.checkOrchestrator(c, orchestratorID)
	}
	if orchestratorErr != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": orchestratorErr.Error()})
		return
	}
	var routingErr error
	if routingID == routing.ProviderTraefik && s.probeRouting == nil {
		routingErr = orchestrator.NewK3sProvider(req.K3s.Kubeconfig, req.K3s.Namespace).Ready(c)
	} else {
		routingErr = s.checkRouting(c, routingID)
	}
	if routingErr != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": routingErr.Error()})
		return
	}

	previous := s.config.Cluster
	s.config.Cluster.Orchestrator = string(orchestratorID)
	s.config.Cluster.Routing = string(routingID)
	s.config.Cluster.K3s = req.K3s
	if s.configPath != "" {
		if err := config.Save(s.config, s.configPath); err != nil {
			s.config.Cluster = previous
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to save provider configuration: %v", err)})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"orchestrator": s.config.Cluster.Orchestrator,
		"routing":      s.config.Cluster.Routing,
		"k3s":          s.config.Cluster.K3s,
	})
}

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

func (s *Server) clusterPeerPolicy(c *gin.Context) {
	mgr := s.getClusterManager()
	if mgr == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cluster is not enabled"})
		return
	}
	policy, err := mgr.DB().GetPeerPolicy(c.Param("name"))
	if err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "Peer not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, policy)
}

func (s *Server) updateClusterPeerPolicy(c *gin.Context) {
	mgr := s.getClusterManager()
	if mgr == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cluster is not enabled"})
		return
	}
	policy := cluster.PeerPolicy{Peer: c.Param("name")}
	if err := c.ShouldBindJSON(&policy); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	policy.Peer = c.Param("name")
	seen := make(map[cluster.Capability]bool, len(policy.Grants))
	for _, grant := range policy.Grants {
		if !cluster.ValidCapability(grant.Capability) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Unknown capability %q", grant.Capability)})
			return
		}
		if seen[grant.Capability] {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Duplicate capability %q", grant.Capability)})
			return
		}
		if grant.MaxCPU < 0 || grant.MaxReplicas < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Resource limits cannot be negative"})
			return
		}
		seen[grant.Capability] = true
	}
	previous, err := mgr.DB().GetPeerPolicy(policy.Peer)
	if err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "Peer not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := mgr.DB().SetPeerPolicy(policy); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "Peer not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := s.applyClusterPeerPolicy(policy); err != nil {
		_ = mgr.DB().SetPeerPolicy(*previous)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to apply peer policy: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, policy)
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
		if err := s.createClusterAPIKey(ourAPIKeyForThem, exchangeResp.Name); err != nil {
			_ = mgr.RemovePeer(exchangeResp.Name)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create peer credential"})
			return
		}
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
		if err := s.createClusterAPIKey(ourAPIKeyForThem, req.Name); err != nil {
			_ = mgr.RemovePeer(req.Name)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create peer credential"})
			return
		}
	}

	c.JSON(http.StatusOK, exchangeResponse{
		APIKey: ourAPIKeyForThem,
		Name:   mgr.ServerName(),
	})
}

func (s *Server) createClusterAPIKey(rawKey, peerName string) error {
	if s.authManager == nil {
		return fmt.Errorf("Authentication manager is not available")
	}
	userID, err := s.clusterServiceUserID()
	if err != nil {
		return err
	}
	permissions, deployments := clusterPolicyAccess(cluster.PeerPolicy{Peer: peerName, Grants: cluster.DefaultPeerGrants()})
	_, err = s.authManager.CreateAPIKeyFromRaw(
		rawKey,
		userID,
		fmt.Sprintf("cluster-peer-%s", peerName),
		fmt.Sprintf("Auto-generated API key for cluster peer %s", peerName),
		auth.Role(""),
		permissions,
		deployments,
		time.Time{},
	)
	return err
}

func (s *Server) clusterServiceUserID() (int64, error) {
	const username = "__flatrun_cluster"
	user, err := s.authManager.GetUserByUsername(username)
	if err == nil {
		if user.Role != auth.RoleService {
			return 0, fmt.Errorf("Reserved Fleet identity has an invalid role")
		}
		return user.ID, nil
	}
	passwordBytes := make([]byte, 32)
	if _, err := rand.Read(passwordBytes); err != nil {
		return 0, err
	}
	user, err = s.authManager.CreateUser(username, "", base64.RawURLEncoding.EncodeToString(passwordBytes), auth.RoleService, nil)
	if err != nil {
		return 0, err
	}
	return user.ID, nil
}

func clusterPolicyAccess(policy cluster.PeerPolicy) ([]string, auth.DeploymentAccess) {
	permissions := make(map[string]bool)
	deployments := make(auth.DeploymentAccess)
	unrestrictedDeployments := false
	for _, grant := range policy.Grants {
		switch grant.Capability {
		case cluster.CapabilityFleetRead:
			permissions[auth.PermClusterRead.String()] = true
		case cluster.CapabilityDeploymentsRead:
			permissions[auth.PermDeploymentsRead.String()] = true
			permissions[auth.PermContainersRead.String()] = true
			unrestrictedDeployments = mergeClusterDeploymentAccess(deployments, grant.Deployments, auth.AccessLevelRead, unrestrictedDeployments)
		case cluster.CapabilityDeploymentsRun:
			permissions[auth.PermDeploymentsRead.String()] = true
			permissions[auth.PermDeploymentsWrite.String()] = true
			permissions[auth.PermContainersRead.String()] = true
			permissions[auth.PermContainersWrite.String()] = true
			unrestrictedDeployments = mergeClusterDeploymentAccess(deployments, grant.Deployments, auth.AccessLevelWrite, unrestrictedDeployments)
		case cluster.CapabilityCapacityRead:
			permissions[auth.PermSystemRead.String()] = true
		case cluster.CapabilityRoutingManage:
			permissions[auth.PermInfrastructureRead.String()] = true
			permissions[auth.PermInfrastructureWrite.String()] = true
		}
	}
	result := make([]string, 0, len(permissions))
	for permission := range permissions {
		result = append(result, permission)
	}
	slices.Sort(result)
	if unrestrictedDeployments {
		deployments = nil
	}
	return result, deployments
}

func mergeClusterDeploymentAccess(access auth.DeploymentAccess, names []string, level string, unrestricted bool) bool {
	if unrestricted || len(names) == 0 {
		return true
	}
	for _, name := range names {
		if current, ok := access[name]; !ok || current == auth.AccessLevelRead && level == auth.AccessLevelWrite {
			access[name] = level
		}
	}
	return false
}

func (s *Server) applyClusterPeerPolicy(policy cluster.PeerPolicy) error {
	if s.authManager == nil {
		return fmt.Errorf("Authentication manager is not available")
	}
	keys, err := s.authManager.GetAllAPIKeys()
	if err != nil {
		return err
	}
	name := fmt.Sprintf("cluster-peer-%s", policy.Peer)
	for _, key := range keys {
		if key.Name != name || !key.IsActive {
			continue
		}
		permissions, deployments := clusterPolicyAccess(policy)
		_, err := s.authManager.UpdateAPIKey(key.ID, key.Name, key.Description, key.Role, permissions, deployments, key.ExpiresAt)
		return err
	}
	return fmt.Errorf("Active peer credential not found")
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

func (s *Server) clusterAggregateCapacity(c *gin.Context) {
	mgr := s.getClusterManager()
	if mgr == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cluster is not enabled"})
		return
	}

	hostStats, err := system.GetSystemStats()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	host := capacity.Host{
		CPUCores:        float64(hostStats.CPU.Cores),
		CPUUsagePercent: hostStats.CPU.UsagePercent,
		MemoryTotal:     hostStats.Memory.Total,
		MemoryAvailable: hostStats.Memory.Available,
	}
	policy := capacity.PolicyFromConfig(s.config.Capacity)
	localData, err := json.Marshal(gin.H{
		"host":   host,
		"policy": policy,
		"offer":  capacity.FleetOffer(host, policy, s.config.Capacity.OfferToFleet),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to marshal local capacity"})
		return
	}

	result := cluster.AggregateFromPeers(c.Request.Context(), localData, mgr, "/api/capacity")
	c.JSON(http.StatusOK, result)
}

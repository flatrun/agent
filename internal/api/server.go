package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/certs"
	"github.com/flatrun/agent/internal/database"
	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/files"
	"github.com/flatrun/agent/internal/networks"
	"github.com/flatrun/agent/internal/proxy"
	"github.com/flatrun/agent/internal/system"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
	"github.com/flatrun/agent/pkg/plugins"
	"github.com/flatrun/agent/pkg/subdomain"
	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

type Server struct {
	config            *config.Config
	router            *gin.Engine
	server            *http.Server
	manager           *docker.Manager
	certsDiscovery    *certs.Discovery
	networksManager   *networks.Manager
	pluginRegistry    *plugins.Registry
	authMiddleware    *auth.Middleware
	proxyOrchestrator *proxy.Orchestrator
	filesManager      *files.Manager
	servicesManager   *system.ServicesManager
	databaseManager   *database.Manager
}

func New(cfg *config.Config) *Server {
	if cfg.Logging.Level == "debug" {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.Default()

	if cfg.API.EnableCORS {
		router.Use(corsMiddleware(cfg.API.AllowedOrigins))
	}

	manager := docker.NewManager(cfg.DeploymentsPath)
	certsDiscovery := certs.NewDiscovery(cfg.DeploymentsPath)
	networksManager := networks.NewManager()
	pluginsDir := filepath.Join(cfg.DeploymentsPath, ".flatrun", "plugins")
	pluginRegistry := plugins.NewRegistry(pluginsDir)
	_ = pluginRegistry.LoadFromDisk()
	authMiddleware := auth.NewMiddleware(&cfg.Auth)
	proxyOrchestrator := proxy.NewOrchestrator(cfg)
	filesManager := files.NewManager(cfg.DeploymentsPath)
	servicesManager := system.NewServicesManager()
	databaseManager := database.NewManager()

	s := &Server{
		config:            cfg,
		router:            router,
		manager:           manager,
		certsDiscovery:    certsDiscovery,
		networksManager:   networksManager,
		pluginRegistry:    pluginRegistry,
		authMiddleware:    authMiddleware,
		proxyOrchestrator: proxyOrchestrator,
		filesManager:      filesManager,
		servicesManager:   servicesManager,
		databaseManager:   databaseManager,
	}

	s.setupRoutes()

	return s
}

func (s *Server) setupRoutes() {
	api := s.router.Group("/api")
	{
		api.GET("/health", s.healthCheck)
		api.GET("/auth/status", s.authMiddleware.GetAuthStatus)
		api.POST("/auth/login", s.authMiddleware.Login)
		api.GET("/auth/validate", s.authMiddleware.ValidateToken)

		protected := api.Group("")
		protected.Use(s.authMiddleware.RequireAuth())
		{
			protected.GET("/deployments", s.listDeployments)
			protected.GET("/deployments/:name", s.getDeployment)
			protected.POST("/deployments", s.createDeployment)
			protected.PUT("/deployments/:name", s.updateDeployment)
			protected.PUT("/deployments/:name/metadata", s.updateDeploymentMetadata)
			protected.DELETE("/deployments/:name", s.deleteDeployment)
			protected.POST("/deployments/:name/start", s.startDeployment)
			protected.POST("/deployments/:name/stop", s.stopDeployment)
			protected.POST("/deployments/:name/restart", s.restartDeployment)
			protected.GET("/deployments/:name/logs", s.getDeploymentLogs)
			protected.GET("/deployments/:name/compose", s.getDeploymentCompose)
			protected.GET("/networks", s.listNetworks)
			protected.POST("/networks", s.createNetwork)
			protected.DELETE("/networks/:name", s.deleteNetwork)
			protected.POST("/networks/:name/connect", s.connectContainer)
			protected.POST("/networks/:name/disconnect", s.disconnectContainer)
			protected.GET("/certificates", s.listCertificates)
			protected.POST("/certificates", s.requestCertificate)
			protected.POST("/certificates/renew", s.renewCertificates)
			protected.DELETE("/certificates/:domain", s.deleteCertificate)

			protected.GET("/proxy/status/:name", s.getProxyStatus)
			protected.POST("/proxy/setup/:name", s.setupProxy)
			protected.DELETE("/proxy/:name", s.teardownProxy)
			protected.GET("/proxy/vhosts", s.listVirtualHosts)

			protected.GET("/settings", s.getSettings)
			protected.PUT("/settings", s.updateSettings)
			protected.GET("/subdomain/generate", s.generateSubdomain)
			protected.GET("/plugins", s.listPlugins)
			protected.GET("/plugins/:name", s.getPlugin)
			protected.POST("/plugins/:name/deployments", s.createPluginDeployment)
			protected.GET("/templates", s.listTemplates)
			protected.GET("/stats", s.getSystemStats)
			protected.GET("/containers", s.listContainers)
			protected.POST("/containers/:id/start", s.startContainer)
			protected.POST("/containers/:id/stop", s.stopContainer)
			protected.POST("/containers/:id/restart", s.restartContainer)
			protected.DELETE("/containers/:id", s.removeContainer)
			protected.GET("/containers/:id/logs", s.getContainerLogs)
			protected.GET("/images", s.listImages)
			protected.DELETE("/images/:id", s.removeImage)
			protected.POST("/images/pull", s.pullImage)
			protected.GET("/volumes", s.listVolumes)
			protected.POST("/volumes", s.createVolume)
			protected.DELETE("/volumes/:name", s.removeVolume)
			protected.POST("/volumes/prune", s.pruneVolumes)
			protected.GET("/ports", s.listPorts)
			protected.POST("/ports/:pid/kill", s.killProcess)

			protected.GET("/system/services", s.listSystemServices)
			protected.POST("/system/services/:name/start", s.startSystemService)
			protected.POST("/system/services/:name/stop", s.stopSystemService)
			protected.POST("/system/services/:name/restart", s.restartSystemService)

			protected.GET("/deployments/:name/files", s.listDeploymentFiles)
			protected.GET("/deployments/:name/files/*path", s.getDeploymentFile)
			protected.POST("/deployments/:name/files/*path", s.uploadDeploymentFile)
			protected.DELETE("/deployments/:name/files/*path", s.deleteDeploymentFile)
			protected.POST("/deployments/:name/mkdir/*path", s.createDeploymentDir)
			protected.GET("/deployments/:name/files-info", s.getDeploymentFilesInfo)

			protected.POST("/databases/test", s.testDatabaseConnection)
			protected.POST("/databases/list", s.listDatabasesInServer)
			protected.POST("/databases/tables", s.listDatabaseTables)
			protected.POST("/databases/users", s.listDatabaseUsers)
			protected.POST("/databases/create", s.createDatabaseInServer)
			protected.POST("/databases/users/create", s.createDatabaseUser)
			protected.POST("/databases/privileges/grant", s.grantDatabasePrivileges)
		}
	}
}

func (s *Server) Start() error {
	addr := fmt.Sprintf("%s:%d", s.config.API.Host, s.config.API.Port)

	s.server = &http.Server{
		Addr:    addr,
		Handler: s.router,
	}

	return s.server.ListenAndServe()
}

func (s *Server) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.server.Shutdown(ctx)
}

func (s *Server) healthCheck(c *gin.Context) {
	stats, _ := s.manager.GetStats()

	c.JSON(http.StatusOK, gin.H{
		"status":           "healthy",
		"agent":            "flatrun",
		"deployments_path": s.config.DeploymentsPath,
		"stats":            stats,
	})
}

func (s *Server) listDeployments(c *gin.Context) {
	deployments, err := s.manager.ListDeployments()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"deployments": deployments,
		"path":        s.manager.BasePath(),
	})
}

func (s *Server) getDeployment(c *gin.Context) {
	name := c.Param("name")

	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Deployment not found",
		})
		return
	}

	composeContent, _ := s.manager.GetComposeFile(name)
	proxyStatus := s.proxyOrchestrator.GetDeploymentProxyStatus(deployment)

	c.JSON(http.StatusOK, gin.H{
		"deployment":      deployment,
		"compose_content": composeContent,
		"proxy_status":    proxyStatus,
	})
}

func (s *Server) createDeployment(c *gin.Context) {
	var req struct {
		Name           string                  `json:"name" binding:"required"`
		ComposeContent string                  `json:"compose_content" binding:"required"`
		Metadata       *models.ServiceMetadata `json:"metadata,omitempty"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	if err := s.manager.CreateDeployment(req.Name, req.ComposeContent); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	if req.Metadata != nil {
		if err := s.manager.SaveMetadata(req.Name, req.Metadata); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Deployment created but failed to save metadata: " + err.Error(),
			})
			return
		}
	}

	var proxyResult *proxy.SetupResult
	if req.Metadata != nil && req.Metadata.Networking.Expose {
		deployment, err := s.manager.GetDeployment(req.Name)
		if err == nil {
			proxyResult, _ = s.proxyOrchestrator.SetupDeployment(deployment)
		}
	}

	c.JSON(http.StatusCreated, gin.H{
		"message":      "Deployment created",
		"name":         req.Name,
		"proxy_result": proxyResult,
	})
}

func (s *Server) updateDeployment(c *gin.Context) {
	name := c.Param("name")

	var req struct {
		ComposeContent string `json:"compose_content" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	if err := s.manager.UpdateDeployment(name, req.ComposeContent); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Deployment updated",
		"name":    name,
	})
}

func (s *Server) updateDeploymentMetadata(c *gin.Context) {
	name := c.Param("name")

	var metadata models.ServiceMetadata
	if err := c.ShouldBindJSON(&metadata); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Deployment not found",
		})
		return
	}

	if err := s.manager.SaveMetadata(name, &metadata); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	var proxyResult *proxy.SetupResult
	if metadata.Networking.Expose {
		proxyResult, _ = s.proxyOrchestrator.SetupDeployment(deployment)
	} else {
		_ = s.proxyOrchestrator.TeardownDeployment(name)
	}

	c.JSON(http.StatusOK, gin.H{
		"message":      "Metadata updated",
		"name":         name,
		"proxy_result": proxyResult,
	})
}

func (s *Server) deleteDeployment(c *gin.Context) {
	name := c.Param("name")

	if err := s.manager.DeleteDeployment(name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Deployment deleted",
		"name":    name,
	})
}

func (s *Server) startDeployment(c *gin.Context) {
	name := c.Param("name")

	output, err := s.manager.StartDeployment(name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":  err.Error(),
			"output": output,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Deployment started",
		"name":    name,
		"output":  output,
	})
}

func (s *Server) stopDeployment(c *gin.Context) {
	name := c.Param("name")

	output, err := s.manager.StopDeployment(name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":  err.Error(),
			"output": output,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Deployment stopped",
		"name":    name,
		"output":  output,
	})
}

func (s *Server) restartDeployment(c *gin.Context) {
	name := c.Param("name")

	output, err := s.manager.RestartDeployment(name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":  err.Error(),
			"output": output,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Deployment restarted",
		"name":    name,
		"output":  output,
	})
}

func (s *Server) getDeploymentLogs(c *gin.Context) {
	name := c.Param("name")

	tailStr := c.DefaultQuery("tail", "100")
	tail, err := strconv.Atoi(tailStr)
	if err != nil {
		tail = 100
	}

	logs, err := s.manager.GetDeploymentLogs(name, tail)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"name": name,
		"logs": logs,
	})
}

func (s *Server) getDeploymentCompose(c *gin.Context) {
	name := c.Param("name")

	content, err := s.manager.GetComposeFile(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"name":    name,
		"content": content,
	})
}

func (s *Server) listNetworks(c *gin.Context) {
	networks, err := s.networksManager.ListNetworks()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"networks": networks,
	})
}

func (s *Server) createNetwork(c *gin.Context) {
	var req struct {
		Name   string            `json:"name" binding:"required"`
		Driver string            `json:"driver"`
		Labels map[string]string `json:"labels"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	if req.Driver == "" {
		req.Driver = "bridge"
	}

	if err := s.networksManager.CreateNetwork(req.Name, req.Driver, req.Labels); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"message": "Network created",
		"name":    req.Name,
	})
}

func (s *Server) deleteNetwork(c *gin.Context) {
	name := c.Param("name")

	if err := s.networksManager.DeleteNetwork(name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Network deleted",
		"name":    name,
	})
}

func (s *Server) connectContainer(c *gin.Context) {
	networkName := c.Param("name")

	var req struct {
		Container string `json:"container" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	if err := s.networksManager.ConnectContainer(networkName, req.Container); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":   "Container connected",
		"network":   networkName,
		"container": req.Container,
	})
}

func (s *Server) disconnectContainer(c *gin.Context) {
	networkName := c.Param("name")

	var req struct {
		Container string `json:"container" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	if err := s.networksManager.DisconnectContainer(networkName, req.Container); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":   "Container disconnected",
		"network":   networkName,
		"container": req.Container,
	})
}

func (s *Server) getSettings(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"settings": gin.H{
			"deployments_path": s.config.DeploymentsPath,
			"api_port":         s.config.API.Port,
			"enable_cors":      s.config.API.EnableCORS,
			"allowed_origins":  s.config.API.AllowedOrigins,
			"domain": gin.H{
				"default_domain":  s.config.Domain.DefaultDomain,
				"auto_subdomain":  s.config.Domain.AutoSubdomain,
				"auto_ssl":        s.config.Domain.AutoSSL,
				"subdomain_style": s.config.Domain.SubdomainStyle,
			},
		},
	})
}

func (s *Server) updateSettings(c *gin.Context) {
	var req struct {
		Domain *struct {
			DefaultDomain  string `json:"default_domain"`
			AutoSubdomain  bool   `json:"auto_subdomain"`
			AutoSSL        bool   `json:"auto_ssl"`
			SubdomainStyle string `json:"subdomain_style"`
		} `json:"domain,omitempty"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Domain != nil {
		s.config.Domain.DefaultDomain = req.Domain.DefaultDomain
		s.config.Domain.AutoSubdomain = req.Domain.AutoSubdomain
		s.config.Domain.AutoSSL = req.Domain.AutoSSL
		if req.Domain.SubdomainStyle != "" {
			s.config.Domain.SubdomainStyle = req.Domain.SubdomainStyle
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Settings updated",
		"settings": gin.H{
			"domain": gin.H{
				"default_domain":  s.config.Domain.DefaultDomain,
				"auto_subdomain":  s.config.Domain.AutoSubdomain,
				"auto_ssl":        s.config.Domain.AutoSSL,
				"subdomain_style": s.config.Domain.SubdomainStyle,
			},
		},
	})
}

func (s *Server) generateSubdomain(c *gin.Context) {
	gen := subdomain.NewGenerator(s.config.Domain.SubdomainStyle)

	subdomainName := gen.Generate()
	fullDomain := ""
	if s.config.Domain.DefaultDomain != "" {
		fullDomain = gen.GenerateForDomain(s.config.Domain.DefaultDomain)
	}

	c.JSON(http.StatusOK, gin.H{
		"subdomain":      subdomainName,
		"full_domain":    fullDomain,
		"default_domain": s.config.Domain.DefaultDomain,
		"auto_ssl":       s.config.Domain.AutoSSL,
	})
}

func (s *Server) listPlugins(c *gin.Context) {
	pluginList := s.pluginRegistry.List()

	c.JSON(http.StatusOK, gin.H{
		"plugins": pluginList,
	})
}

func (s *Server) getPlugin(c *gin.Context) {
	name := c.Param("name")

	plugin, exists := s.pluginRegistry.Get(name)
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Plugin not found",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"plugin": plugin.Info(),
	})
}

func (s *Server) createPluginDeployment(c *gin.Context) {
	pluginName := c.Param("name")

	plugin, exists := s.pluginRegistry.Get(pluginName)
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Plugin not found",
		})
		return
	}

	deploymentPlugin, ok := plugin.(plugins.DeploymentPlugin)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Plugin does not support deployments",
		})
		return
	}

	var req struct {
		Name   string                 `json:"name" binding:"required"`
		Config map[string]interface{} `json:"config"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	result, err := deploymentPlugin.CreateDeployment(req.Name, req.Config)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"message":    "Deployment created",
		"deployment": result,
	})
}

type TemplateMetadata struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Icon        string `yaml:"icon"`
	Category    string `yaml:"category"`
}

type Template struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
	Category    string `json:"category"`
	Content     string `json:"content"`
}

func (s *Server) listTemplates(c *gin.Context) {
	templatesDir := filepath.Join(s.config.DeploymentsPath, ".flatrun", "templates")

	if err := os.MkdirAll(templatesDir, 0755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to create templates directory",
		})
		return
	}

	var templates []Template

	entries, err := os.ReadDir(templatesDir)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"templates": templates,
		})
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		templateID := entry.Name()
		templatePath := filepath.Join(templatesDir, templateID)

		metadataPath := filepath.Join(templatePath, "metadata.yml")
		composePath := filepath.Join(templatePath, "docker-compose.yml")

		composeContent, err := os.ReadFile(composePath)
		if err != nil {
			continue
		}

		var metadata TemplateMetadata
		metadataContent, err := os.ReadFile(metadataPath)
		if err == nil {
			_ = yaml.Unmarshal(metadataContent, &metadata)
		}

		if metadata.Name == "" {
			metadata.Name = toTitleCase(strings.ReplaceAll(templateID, "-", " "))
		}
		if metadata.Icon == "" {
			metadata.Icon = "pi pi-box"
		}
		if metadata.Category == "" {
			metadata.Category = "general"
		}

		templates = append(templates, Template{
			ID:          templateID,
			Name:        metadata.Name,
			Description: metadata.Description,
			Icon:        metadata.Icon,
			Category:    metadata.Category,
			Content:     string(composeContent),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"templates": templates,
	})
}

func (s *Server) listCertificates(c *gin.Context) {
	certificates, err := s.proxyOrchestrator.ListCertificates()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"certificates": certificates,
	})
}

func (s *Server) requestCertificate(c *gin.Context) {
	var req struct {
		Domain string `json:"domain" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	result, err := s.proxyOrchestrator.RequestCertificate(req.Domain)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"message": "Certificate requested",
		"result":  result,
	})
}

func (s *Server) renewCertificates(c *gin.Context) {
	result, err := s.proxyOrchestrator.RenewCertificates()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Renewal completed",
		"result":  result,
	})
}

func (s *Server) deleteCertificate(c *gin.Context) {
	domain := c.Param("domain")

	if err := s.proxyOrchestrator.SSLManager().DeleteCertificate(domain); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Certificate deleted",
		"domain":  domain,
	})
}

func (s *Server) getProxyStatus(c *gin.Context) {
	name := c.Param("name")

	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Deployment not found",
		})
		return
	}

	status := s.proxyOrchestrator.GetDeploymentProxyStatus(deployment)

	c.JSON(http.StatusOK, gin.H{
		"status": status,
	})
}

func (s *Server) setupProxy(c *gin.Context) {
	name := c.Param("name")

	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Deployment not found",
		})
		return
	}

	result, err := s.proxyOrchestrator.SetupDeployment(deployment)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Proxy setup completed",
		"result":  result,
	})
}

func (s *Server) teardownProxy(c *gin.Context) {
	name := c.Param("name")

	if err := s.proxyOrchestrator.TeardownDeployment(name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Proxy removed",
		"name":    name,
	})
}

func (s *Server) listVirtualHosts(c *gin.Context) {
	vhosts, err := s.proxyOrchestrator.ListVirtualHosts()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"virtual_hosts": vhosts,
	})
}

func (s *Server) getSystemStats(c *gin.Context) {
	stats, err := s.manager.GetStats()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	containerStats, _ := s.networksManager.GetContainerStats()
	imageStats, _ := s.networksManager.GetImageStats()
	volumeStats, _ := s.networksManager.GetVolumeStats()

	c.JSON(http.StatusOK, gin.H{
		"deployments": stats,
		"containers":  containerStats,
		"images":      imageStats,
		"volumes":     volumeStats,
	})
}

func (s *Server) listContainers(c *gin.Context) {
	containers, err := s.networksManager.ListContainers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"containers": containers,
	})
}

func (s *Server) startContainer(c *gin.Context) {
	id := c.Param("id")

	if err := s.networksManager.StartContainer(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Container started",
		"id":      id,
	})
}

func (s *Server) stopContainer(c *gin.Context) {
	id := c.Param("id")

	if err := s.networksManager.StopContainer(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Container stopped",
		"id":      id,
	})
}

func (s *Server) restartContainer(c *gin.Context) {
	id := c.Param("id")

	if err := s.networksManager.RestartContainer(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Container restarted",
		"id":      id,
	})
}

func (s *Server) removeContainer(c *gin.Context) {
	id := c.Param("id")

	if err := s.networksManager.RemoveContainer(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Container removed",
		"id":      id,
	})
}

func (s *Server) getContainerLogs(c *gin.Context) {
	id := c.Param("id")

	tailStr := c.DefaultQuery("tail", "100")
	tail, err := strconv.Atoi(tailStr)
	if err != nil {
		tail = 100
	}

	logs, err := s.networksManager.GetContainerLogs(id, tail)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":   id,
		"logs": logs,
	})
}

func (s *Server) listImages(c *gin.Context) {
	images, err := s.networksManager.ListImages()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"images": images,
	})
}

func (s *Server) removeImage(c *gin.Context) {
	id := c.Param("id")

	if err := s.networksManager.RemoveImage(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Image removed",
		"id":      id,
	})
}

func (s *Server) pullImage(c *gin.Context) {
	var req struct {
		Name string `json:"name" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	if err := s.networksManager.PullImage(req.Name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Image pulled",
		"name":    req.Name,
	})
}

func (s *Server) listVolumes(c *gin.Context) {
	volumes, err := s.networksManager.ListVolumes()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"volumes": volumes,
	})
}

func (s *Server) createVolume(c *gin.Context) {
	var req struct {
		Name   string            `json:"name" binding:"required"`
		Driver string            `json:"driver"`
		Labels map[string]string `json:"labels"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	if req.Driver == "" {
		req.Driver = "local"
	}

	if err := s.networksManager.CreateVolume(req.Name, req.Driver, req.Labels); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"message": "Volume created",
		"name":    req.Name,
	})
}

func (s *Server) removeVolume(c *gin.Context) {
	name := c.Param("name")

	if err := s.networksManager.RemoveVolume(name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Volume removed",
		"name":    name,
	})
}

func (s *Server) pruneVolumes(c *gin.Context) {
	count, err := s.networksManager.PruneVolumes()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Unused volumes pruned",
		"count":   count,
	})
}

func (s *Server) listPorts(c *gin.Context) {
	ports, err := s.networksManager.ListPorts()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ports": ports,
	})
}

func (s *Server) killProcess(c *gin.Context) {
	pidStr := c.Param("pid")
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid PID",
		})
		return
	}

	if err := s.networksManager.KillProcess(pid); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Process killed",
		"pid":     pid,
	})
}

func toTitleCase(s string) string {
	words := strings.Fields(s)
	for i, word := range words {
		if len(word) > 0 {
			words[i] = strings.ToUpper(string(word[0])) + strings.ToLower(word[1:])
		}
	}
	return strings.Join(words, " ")
}

func corsMiddleware(allowedOrigins []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")

		for _, allowed := range allowedOrigins {
			if origin == allowed || allowed == "*" {
				c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
				break
			}
		}

		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

func (s *Server) listDeploymentFiles(c *gin.Context) {
	name := c.Param("name")
	path := c.DefaultQuery("path", "/")

	filesList, err := s.filesManager.ListFiles(name, path)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"files": filesList,
		"path":  path,
	})
}

func (s *Server) getDeploymentFile(c *gin.Context) {
	name := c.Param("name")
	path := c.Param("path")

	if c.Query("info") == "true" {
		info, err := s.filesManager.GetFileInfo(name, path)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{
				"error": err.Error(),
			})
			return
		}
		c.JSON(http.StatusOK, info)
		return
	}

	if c.Query("list") == "true" {
		filesList, err := s.filesManager.ListFiles(name, path)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": err.Error(),
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"files": filesList,
			"path":  path,
		})
		return
	}

	file, info, err := s.filesManager.ReadFile(name, path)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": err.Error(),
		})
		return
	}
	defer file.Close()

	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", info.Name))
	c.Header("Content-Length", fmt.Sprintf("%d", info.Size))
	c.DataFromReader(http.StatusOK, info.Size, "application/octet-stream", file, nil)
}

func (s *Server) uploadDeploymentFile(c *gin.Context) {
	name := c.Param("name")
	path := c.Param("path")

	file, _, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "No file provided",
		})
		return
	}
	defer file.Close()

	if err := s.filesManager.WriteFile(name, path, file); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	info, _ := s.filesManager.GetFileInfo(name, path)
	c.JSON(http.StatusOK, gin.H{
		"message": "File uploaded successfully",
		"file":    info,
	})
}

func (s *Server) deleteDeploymentFile(c *gin.Context) {
	name := c.Param("name")
	path := c.Param("path")

	if err := s.filesManager.DeleteFile(name, path); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Deleted successfully",
	})
}

func (s *Server) createDeploymentDir(c *gin.Context) {
	name := c.Param("name")
	path := c.Param("path")

	if err := s.filesManager.CreateDirectory(name, path); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	info, _ := s.filesManager.GetFileInfo(name, path)
	c.JSON(http.StatusOK, gin.H{
		"message":   "Directory created",
		"directory": info,
	})
}

func (s *Server) getDeploymentFilesInfo(c *gin.Context) {
	name := c.Param("name")

	usage, err := s.filesManager.GetDiskUsage(name)
	if err != nil {
		usage = 0
	}

	mountPath, _ := s.filesManager.GetMountPath(name, "/")

	c.JSON(http.StatusOK, gin.H{
		"deployment": name,
		"disk_usage": usage,
		"mount_path": mountPath,
	})
}

func (s *Server) listSystemServices(c *gin.Context) {
	services, err := s.servicesManager.ListServices()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"services": services,
	})
}

func (s *Server) startSystemService(c *gin.Context) {
	name := c.Param("name")

	if err := s.servicesManager.StartService(name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Service started",
		"name":    name,
	})
}

func (s *Server) stopSystemService(c *gin.Context) {
	name := c.Param("name")

	if err := s.servicesManager.StopService(name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Service stopped",
		"name":    name,
	})
}

func (s *Server) restartSystemService(c *gin.Context) {
	name := c.Param("name")

	if err := s.servicesManager.RestartService(name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Service restarted",
		"name":    name,
	})
}

func (s *Server) testDatabaseConnection(c *gin.Context) {
	var cfg database.ConnectionConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	if err := s.databaseManager.TestConnection(&cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   err.Error(),
			"success": false,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Connection successful",
	})
}

func (s *Server) listDatabasesInServer(c *gin.Context) {
	var cfg database.ConnectionConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	databases, err := s.databaseManager.ListDatabases(&cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"databases": databases,
	})
}

func (s *Server) listDatabaseTables(c *gin.Context) {
	var req struct {
		database.ConnectionConfig
		Database string `json:"database" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	tables, err := s.databaseManager.ListTables(&req.ConnectionConfig, req.Database)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"tables": tables,
	})
}

func (s *Server) listDatabaseUsers(c *gin.Context) {
	var cfg database.ConnectionConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	users, err := s.databaseManager.ListUsers(&cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"users": users,
	})
}

func (s *Server) createDatabaseInServer(c *gin.Context) {
	var req struct {
		database.ConnectionConfig
		DbName string `json:"db_name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	if err := s.databaseManager.CreateDatabase(&req.ConnectionConfig, req.DbName); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"message": "Database created",
		"name":    req.DbName,
	})
}

func (s *Server) createDatabaseUser(c *gin.Context) {
	var req struct {
		database.ConnectionConfig
		Username string `json:"username" binding:"required"`
		Password string `json:"user_password" binding:"required"`
		Host     string `json:"user_host"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	if err := s.databaseManager.CreateUser(&req.ConnectionConfig, req.Username, req.Password, req.Host); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"message":  "User created",
		"username": req.Username,
	})
}

func (s *Server) grantDatabasePrivileges(c *gin.Context) {
	var req struct {
		database.ConnectionConfig
		Username string `json:"username" binding:"required"`
		Database string `json:"database" binding:"required"`
		Host     string `json:"user_host"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	if err := s.databaseManager.GrantPrivileges(&req.ConnectionConfig, req.Username, req.Database, req.Host); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":  "Privileges granted",
		"username": req.Username,
		"database": req.Database,
	})
}

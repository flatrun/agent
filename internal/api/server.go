package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/certs"
	"github.com/flatrun/agent/internal/database"
	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/files"
	"github.com/flatrun/agent/internal/infra"
	"github.com/flatrun/agent/internal/networks"
	"github.com/flatrun/agent/internal/proxy"
	"github.com/flatrun/agent/internal/system"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
	"github.com/flatrun/agent/pkg/plugins"
	"github.com/flatrun/agent/pkg/subdomain"
	"github.com/flatrun/agent/pkg/version"
	"github.com/flatrun/agent/templates"
	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

type Server struct {
	config            *config.Config
	configPath        string
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
	infraManager      *infra.Manager
}

func New(cfg *config.Config, configPath string) *Server {
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
	infraManager := infra.NewManager(cfg)

	s := &Server{
		config:            cfg,
		configPath:        configPath,
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
		infraManager:      infraManager,
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
			protected.GET("/templates/categories", s.getTemplateCategories)
			protected.POST("/templates/refresh", s.refreshTemplates)
			protected.GET("/templates/:id/compose", s.getTemplateCompose)
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

			protected.GET("/infrastructure", s.listInfrastructure)
			protected.GET("/infrastructure/stats", s.getInfraStats)
			protected.GET("/infrastructure/:name", s.getInfraService)
			protected.POST("/infrastructure/:name/start", s.startInfraService)
			protected.POST("/infrastructure/:name/stop", s.stopInfraService)
			protected.POST("/infrastructure/:name/restart", s.restartInfraService)
			protected.GET("/infrastructure/:name/logs", s.getInfraServiceLogs)
			protected.POST("/infrastructure/migrate/:name", s.migrateToInfrastructure)
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
		"version":          version.Get(),
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
		Name              string                  `json:"name" binding:"required"`
		ComposeContent    string                  `json:"compose_content"`
		TemplateID        string                  `json:"template_id,omitempty"`
		Metadata          *models.ServiceMetadata `json:"metadata,omitempty"`
		EnvVars           []EnvVar                `json:"env_vars,omitempty"`
		AutoStart         bool                    `json:"auto_start"`
		UseSharedDatabase bool                    `json:"use_shared_database"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	if req.ComposeContent == "" {
		generated, err := s.generateComposeContent(req.Name, req.TemplateID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "compose_content is required when template cannot be resolved: " + err.Error(),
			})
			return
		}
		req.ComposeContent = generated
	}

	networkName := s.config.Infrastructure.DefaultProxyNetwork
	if err := s.networksManager.EnsureNetwork(networkName); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to ensure network exists: " + err.Error(),
		})
		return
	}

	if req.Metadata != nil && req.Metadata.Networking.Expose {
		if err := s.networksManager.EnsureContainerOnNetwork(networkName, s.config.Nginx.ContainerName); err != nil {
			log.Printf("Warning: failed to ensure nginx on network %s: %v", networkName, err)
		}
	}

	if err := s.manager.CreateDeployment(req.Name, req.ComposeContent); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	if req.TemplateID != "" {
		s.processTemplateFiles(req.Name, req.TemplateID)
	}

	var dbEnvVars []EnvVar
	if req.UseSharedDatabase && s.config.Infrastructure.Database.Enabled {
		dbResult, err := s.createDatabaseForDeployment(req.Name)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Deployment created but failed to create database: " + err.Error(),
			})
			return
		}
		dbEnvVars = dbResult
	}

	allEnvVars := append(req.EnvVars, dbEnvVars...)
	if len(allEnvVars) > 0 {
		if err := s.writeEnvFile(req.Name, allEnvVars); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Deployment created but failed to write .env file: " + err.Error(),
			})
			return
		}
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
		if err != nil {
			log.Printf("Warning: failed to get deployment for proxy setup: %v", err)
		} else {
			proxyResult, err = s.proxyOrchestrator.SetupDeployment(deployment)
			if err != nil {
				log.Printf("Warning: failed to setup proxy for deployment: %v", err)
			}
		}
	}

	var startOutput string
	var startError string
	if req.AutoStart {
		output, err := s.manager.StartDeployment(req.Name)
		startOutput = output
		if err != nil {
			startError = err.Error()
		}
	}

	c.JSON(http.StatusCreated, gin.H{
		"message":      "Deployment created",
		"name":         req.Name,
		"proxy_result": proxyResult,
		"auto_started": req.AutoStart,
		"start_output": startOutput,
		"start_error":  startError,
	})
}

type EnvVar struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (s *Server) createDatabaseForDeployment(deploymentName string) ([]EnvVar, error) {
	dbConfig := s.config.Infrastructure.Database
	dbName := strings.ReplaceAll(deploymentName, "-", "_") + "_db"
	dbUser := strings.ReplaceAll(deploymentName, "-", "_") + "_user"
	dbPassword := generateRandomPassword(16)

	connConfig := &database.ConnectionConfig{
		Type:     dbConfig.Type,
		Host:     dbConfig.Host,
		Port:     dbConfig.Port,
		Username: dbConfig.RootUser,
		Password: dbConfig.RootPassword,
	}

	if err := s.databaseManager.CreateDatabase(connConfig, dbName); err != nil {
		return nil, fmt.Errorf("failed to create database: %w", err)
	}

	if err := s.databaseManager.CreateUser(connConfig, dbUser, dbPassword, "%"); err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}

	if err := s.databaseManager.GrantPrivileges(connConfig, dbUser, dbName, "%"); err != nil {
		return nil, fmt.Errorf("failed to grant privileges: %w", err)
	}

	dbHost := dbConfig.Host
	if dbHost == "" {
		dbHost = dbConfig.Container
	}

	envVars := []EnvVar{
		{Key: "DB_HOST", Value: dbHost},
		{Key: "DB_PORT", Value: fmt.Sprintf("%d", dbConfig.Port)},
		{Key: "DB_DATABASE", Value: dbName},
		{Key: "DB_USERNAME", Value: dbUser},
		{Key: "DB_PASSWORD", Value: dbPassword},
	}

	var databaseURL string
	switch dbConfig.Type {
	case "mysql", "mariadb":
		databaseURL = fmt.Sprintf("mysql://%s:%s@%s:%d/%s", dbUser, dbPassword, dbHost, dbConfig.Port, dbName)
	case "postgres":
		databaseURL = fmt.Sprintf("postgres://%s:%s@%s:%d/%s", dbUser, dbPassword, dbHost, dbConfig.Port, dbName)
	}
	envVars = append(envVars, EnvVar{Key: "DATABASE_URL", Value: databaseURL})

	return envVars, nil
}

func (s *Server) writeEnvFile(deploymentName string, envVars []EnvVar) error {
	deploymentPath := filepath.Join(s.config.DeploymentsPath, deploymentName)
	envFilePath := filepath.Join(deploymentPath, ".env.flatrun")

	var content strings.Builder
	for _, env := range envVars {
		if env.Key != "" {
			content.WriteString(fmt.Sprintf("%s=%s\n", env.Key, env.Value))
		}
	}

	return os.WriteFile(envFilePath, []byte(content.String()), 0600)
}

func generateRandomPassword(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[time.Now().UnixNano()%int64(len(charset))]
		time.Sleep(time.Nanosecond)
	}
	return string(b)
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
			"nginx": gin.H{
				"enabled":        s.config.Nginx.Enabled,
				"image":          s.config.Nginx.Image,
				"container_name": s.config.Nginx.ContainerName,
				"config_path":    s.config.Nginx.ConfigPath,
				"reload_command": s.config.Nginx.ReloadCommand,
				"external":       s.config.Nginx.External,
			},
			"certbot": gin.H{
				"enabled":      s.config.Certbot.Enabled,
				"image":        s.config.Certbot.Image,
				"email":        s.config.Certbot.Email,
				"staging":      s.config.Certbot.Staging,
				"certs_path":   s.config.Certbot.CertsPath,
				"webroot_path": s.config.Certbot.WebrootPath,
				"dns_provider": s.config.Certbot.DNSProvider,
			},
			"infrastructure": gin.H{
				"default_proxy_network":    s.config.Infrastructure.DefaultProxyNetwork,
				"default_database_network": s.config.Infrastructure.DefaultDatabaseNetwork,
				"database": gin.H{
					"enabled":   s.config.Infrastructure.Database.Enabled,
					"type":      s.config.Infrastructure.Database.Type,
					"container": s.config.Infrastructure.Database.Container,
					"host":      s.config.Infrastructure.Database.Host,
					"port":      s.config.Infrastructure.Database.Port,
				},
				"redis": gin.H{
					"enabled":   s.config.Infrastructure.Redis.Enabled,
					"container": s.config.Infrastructure.Redis.Container,
					"host":      s.config.Infrastructure.Redis.Host,
					"port":      s.config.Infrastructure.Redis.Port,
				},
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
		Nginx *struct {
			Enabled       bool   `json:"enabled"`
			Image         string `json:"image"`
			ContainerName string `json:"container_name"`
			ConfigPath    string `json:"config_path"`
			ReloadCommand string `json:"reload_command"`
			External      bool   `json:"external"`
		} `json:"nginx,omitempty"`
		Certbot *struct {
			Enabled     bool   `json:"enabled"`
			Image       string `json:"image"`
			Email       string `json:"email"`
			Staging     bool   `json:"staging"`
			CertsPath   string `json:"certs_path"`
			WebrootPath string `json:"webroot_path"`
			DNSProvider string `json:"dns_provider"`
		} `json:"certbot,omitempty"`
		Infrastructure *struct {
			DefaultProxyNetwork    string `json:"default_proxy_network"`
			DefaultDatabaseNetwork string `json:"default_database_network"`
			Database               *struct {
				Enabled      bool   `json:"enabled"`
				Type         string `json:"type"`
				Container    string `json:"container"`
				Host         string `json:"host"`
				Port         int    `json:"port"`
				RootUser     string `json:"root_user"`
				RootPassword string `json:"root_password"`
			} `json:"database,omitempty"`
			Redis *struct {
				Enabled   bool   `json:"enabled"`
				Container string `json:"container"`
				Host      string `json:"host"`
				Port      int    `json:"port"`
				Password  string `json:"password"`
			} `json:"redis,omitempty"`
		} `json:"infrastructure,omitempty"`
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

	if req.Nginx != nil {
		s.config.Nginx.Enabled = req.Nginx.Enabled
		s.config.Nginx.External = req.Nginx.External
		if req.Nginx.Image != "" {
			s.config.Nginx.Image = req.Nginx.Image
		}
		if req.Nginx.ContainerName != "" {
			s.config.Nginx.ContainerName = req.Nginx.ContainerName
		}
		if req.Nginx.ConfigPath != "" {
			s.config.Nginx.ConfigPath = req.Nginx.ConfigPath
		}
		if req.Nginx.ReloadCommand != "" {
			s.config.Nginx.ReloadCommand = req.Nginx.ReloadCommand
		}
	}

	if req.Certbot != nil {
		s.config.Certbot.Enabled = req.Certbot.Enabled
		s.config.Certbot.Staging = req.Certbot.Staging
		if req.Certbot.Image != "" {
			s.config.Certbot.Image = req.Certbot.Image
		}
		if req.Certbot.Email != "" {
			s.config.Certbot.Email = req.Certbot.Email
		}
		if req.Certbot.CertsPath != "" {
			s.config.Certbot.CertsPath = req.Certbot.CertsPath
		}
		if req.Certbot.WebrootPath != "" {
			s.config.Certbot.WebrootPath = req.Certbot.WebrootPath
		}
		if req.Certbot.DNSProvider != "" {
			s.config.Certbot.DNSProvider = req.Certbot.DNSProvider
		}
	}

	if req.Infrastructure != nil {
		if req.Infrastructure.DefaultProxyNetwork != "" {
			s.config.Infrastructure.DefaultProxyNetwork = req.Infrastructure.DefaultProxyNetwork
		}
		if req.Infrastructure.DefaultDatabaseNetwork != "" {
			s.config.Infrastructure.DefaultDatabaseNetwork = req.Infrastructure.DefaultDatabaseNetwork
		}
		if req.Infrastructure.Database != nil {
			s.config.Infrastructure.Database.Enabled = req.Infrastructure.Database.Enabled
			s.config.Infrastructure.Database.Type = req.Infrastructure.Database.Type
			s.config.Infrastructure.Database.Container = req.Infrastructure.Database.Container
			s.config.Infrastructure.Database.Host = req.Infrastructure.Database.Host
			if req.Infrastructure.Database.Port > 0 {
				s.config.Infrastructure.Database.Port = req.Infrastructure.Database.Port
			}
			if req.Infrastructure.Database.RootUser != "" {
				s.config.Infrastructure.Database.RootUser = req.Infrastructure.Database.RootUser
			}
			if req.Infrastructure.Database.RootPassword != "" {
				s.config.Infrastructure.Database.RootPassword = req.Infrastructure.Database.RootPassword
			}
		}
		if req.Infrastructure.Redis != nil {
			s.config.Infrastructure.Redis.Enabled = req.Infrastructure.Redis.Enabled
			s.config.Infrastructure.Redis.Container = req.Infrastructure.Redis.Container
			s.config.Infrastructure.Redis.Host = req.Infrastructure.Redis.Host
			if req.Infrastructure.Redis.Port > 0 {
				s.config.Infrastructure.Redis.Port = req.Infrastructure.Redis.Port
			}
			if req.Infrastructure.Redis.Password != "" {
				s.config.Infrastructure.Redis.Password = req.Infrastructure.Redis.Password
			}
		}
	}

	s.infraManager.UpdateConfig(s.config)
	s.proxyOrchestrator.UpdateConfig(s.config)

	if s.configPath != "" {
		if err := config.Save(s.config, s.configPath); err != nil {
			log.Printf("Warning: failed to persist config: %v", err)
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
			"nginx": gin.H{
				"enabled":        s.config.Nginx.Enabled,
				"image":          s.config.Nginx.Image,
				"container_name": s.config.Nginx.ContainerName,
				"config_path":    s.config.Nginx.ConfigPath,
				"reload_command": s.config.Nginx.ReloadCommand,
				"external":       s.config.Nginx.External,
			},
			"certbot": gin.H{
				"enabled":      s.config.Certbot.Enabled,
				"image":        s.config.Certbot.Image,
				"email":        s.config.Certbot.Email,
				"staging":      s.config.Certbot.Staging,
				"certs_path":   s.config.Certbot.CertsPath,
				"webroot_path": s.config.Certbot.WebrootPath,
				"dns_provider": s.config.Certbot.DNSProvider,
			},
			"infrastructure": gin.H{
				"default_proxy_network":    s.config.Infrastructure.DefaultProxyNetwork,
				"default_database_network": s.config.Infrastructure.DefaultDatabaseNetwork,
				"database": gin.H{
					"enabled":   s.config.Infrastructure.Database.Enabled,
					"type":      s.config.Infrastructure.Database.Type,
					"container": s.config.Infrastructure.Database.Container,
					"host":      s.config.Infrastructure.Database.Host,
					"port":      s.config.Infrastructure.Database.Port,
				},
				"redis": gin.H{
					"enabled":   s.config.Infrastructure.Redis.Enabled,
					"container": s.config.Infrastructure.Redis.Container,
					"host":      s.config.Infrastructure.Redis.Host,
					"port":      s.config.Infrastructure.Redis.Port,
				},
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

type TemplateFile struct {
	Path    string `yaml:"path"`
	Content string `yaml:"content"`
}

type TemplateMetadata struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Icon        string         `yaml:"icon"`
	Logo        string         `yaml:"logo"`
	Category    string         `yaml:"category"`
	Priority    int            `yaml:"priority"`
	Files       []TemplateFile `yaml:"files"`
}

type Template struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
	Logo        string `json:"logo"`
	Category    string `json:"category"`
	Priority    int    `json:"priority"`
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

	s.ensureBuiltinTemplates(templatesDir)

	var templateList []Template

	entries, err := os.ReadDir(templatesDir)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"templates": templateList,
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

		templateList = append(templateList, Template{
			ID:          templateID,
			Name:        metadata.Name,
			Description: metadata.Description,
			Icon:        metadata.Icon,
			Logo:        metadata.Logo,
			Category:    metadata.Category,
			Priority:    metadata.Priority,
			Content:     string(composeContent),
		})
	}

	sort.Slice(templateList, func(i, j int) bool {
		return templateList[i].Priority > templateList[j].Priority
	})

	c.JSON(http.StatusOK, gin.H{
		"templates": templateList,
	})
}

func (s *Server) refreshTemplates(c *gin.Context) {
	templatesDir := filepath.Join(s.config.DeploymentsPath, ".flatrun", "templates")

	builtinList, err := templates.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to list builtin templates",
		})
		return
	}

	for _, tmplID := range builtinList {
		templatePath := filepath.Join(templatesDir, tmplID)

		if err := os.MkdirAll(templatePath, 0755); err != nil {
			continue
		}

		metadataContent, err := templates.GetMetadata(tmplID)
		if err == nil {
			_ = os.WriteFile(filepath.Join(templatePath, "metadata.yml"), metadataContent, 0644)
		}

		composeContent, err := templates.GetCompose(tmplID)
		if err == nil {
			_ = os.WriteFile(filepath.Join(templatePath, "docker-compose.yml"), composeContent, 0644)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Templates refreshed",
		"count":   len(builtinList),
	})
}

func (s *Server) getTemplateCategories(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"categories": templates.GetCategories(),
	})
}

func (s *Server) getTemplateCompose(c *gin.Context) {
	templateID := c.Param("id")
	name := c.DefaultQuery("name", "my-app")

	content, err := s.generateComposeContent(name, templateID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"template_id": templateID,
		"name":        name,
		"content":     content,
	})
}

func (s *Server) ensureBuiltinTemplates(templatesDir string) {
	builtinList, err := templates.List()
	if err != nil {
		return
	}

	for _, tmplID := range builtinList {
		templatePath := filepath.Join(templatesDir, tmplID)
		composePath := filepath.Join(templatePath, "docker-compose.yml")

		if _, err := os.Stat(composePath); err == nil {
			continue
		}

		if err := os.MkdirAll(templatePath, 0755); err != nil {
			continue
		}

		metadataContent, err := templates.GetMetadata(tmplID)
		if err == nil {
			_ = os.WriteFile(filepath.Join(templatePath, "metadata.yml"), metadataContent, 0644)
		}

		composeContent, err := templates.GetCompose(tmplID)
		if err == nil {
			_ = os.WriteFile(composePath, composeContent, 0644)
		}
	}
}

func (s *Server) generateComposeContent(name, templateID string) (string, error) {
	if templateID == "" {
		templateID = "static"
	}

	composeBytes, err := templates.GetCompose(templateID)
	if err != nil {
		templatesDir := filepath.Join(s.config.DeploymentsPath, ".flatrun", "templates")
		composePath := filepath.Join(templatesDir, templateID, "docker-compose.yml")
		composeBytes, err = os.ReadFile(composePath)
		if err != nil {
			return "", fmt.Errorf("template '%s' not found", templateID)
		}
	}

	content := string(composeBytes)

	content = strings.ReplaceAll(content, "${NAME}", name)

	networkName := s.config.Infrastructure.DefaultProxyNetwork
	content = strings.ReplaceAll(content, "${PROXY_NETWORK}", networkName)

	content = replaceHardcodedNetwork(content, "proxy", networkName)

	return content, nil
}

func replaceHardcodedNetwork(content, oldNetwork, newNetwork string) string {
	if oldNetwork == newNetwork {
		return content
	}

	lines := strings.Split(content, "\n")
	var result []string
	inNetworks := false
	inServicesNetworks := false
	indentLevel := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		currentIndent := len(line) - len(strings.TrimLeft(line, " \t"))

		if trimmed == "networks:" {
			if currentIndent == 0 {
				inNetworks = true
				indentLevel = currentIndent
			} else {
				inServicesNetworks = true
				indentLevel = currentIndent
			}
			result = append(result, line)
			continue
		}

		if inNetworks && currentIndent > indentLevel {
			if strings.HasPrefix(trimmed, oldNetwork+":") {
				line = strings.Replace(line, oldNetwork+":", newNetwork+":", 1)
			}
		} else if inNetworks && currentIndent <= indentLevel && trimmed != "" {
			inNetworks = false
		}

		if inServicesNetworks && currentIndent > indentLevel {
			if trimmed == "- "+oldNetwork {
				line = strings.Replace(line, "- "+oldNetwork, "- "+newNetwork, 1)
			}
		} else if inServicesNetworks && currentIndent <= indentLevel && trimmed != "" {
			inServicesNetworks = false
		}

		result = append(result, line)
	}

	return strings.Join(result, "\n")
}

func (s *Server) processTemplateFiles(deploymentName, templateID string) {
	templatesDir := filepath.Join(s.config.DeploymentsPath, ".flatrun", "templates")
	metadataPath := filepath.Join(templatesDir, templateID, "metadata.yml")

	metadataContent, err := os.ReadFile(metadataPath)
	if err != nil {
		return
	}

	var metadata TemplateMetadata
	if err := yaml.Unmarshal(metadataContent, &metadata); err != nil {
		return
	}

	if len(metadata.Files) == 0 {
		return
	}

	deploymentDir := filepath.Join(s.config.DeploymentsPath, deploymentName)

	for _, file := range metadata.Files {
		content := strings.ReplaceAll(file.Content, "${NAME}", deploymentName)

		filePath := filepath.Join(deploymentDir, file.Path)
		fileDir := filepath.Dir(filePath)

		if err := os.MkdirAll(fileDir, 0755); err != nil {
			continue
		}

		_ = os.WriteFile(filePath, []byte(content), 0644)
	}
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

func (s *Server) listInfrastructure(c *gin.Context) {
	services, err := s.infraManager.ListServices()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	infraDeployments, err := s.manager.ListInfrastructure()
	if err == nil {
		existingNames := make(map[string]bool)
		for _, svc := range services {
			existingNames[svc.Name] = true
		}

		for _, dep := range infraDeployments {
			if existingNames[dep.Name] {
				continue
			}
			depType := "infrastructure"
			if dep.Metadata != nil && dep.Metadata.Type != "" {
				depType = dep.Metadata.Type
			}
			services = append(services, models.InfraService{
				Name:    dep.Name,
				Type:    depType,
				Status:  dep.Status,
				Managed: true,
			})
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"services": services,
	})
}

func (s *Server) getInfraService(c *gin.Context) {
	name := c.Param("name")

	service, err := s.infraManager.GetService(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"service": service,
	})
}

func (s *Server) startInfraService(c *gin.Context) {
	name := c.Param("name")

	if err := s.infraManager.StartService(name); err != nil {
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

func (s *Server) stopInfraService(c *gin.Context) {
	name := c.Param("name")

	if err := s.infraManager.StopService(name); err != nil {
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

func (s *Server) restartInfraService(c *gin.Context) {
	name := c.Param("name")

	if err := s.infraManager.RestartService(name); err != nil {
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

func (s *Server) getInfraServiceLogs(c *gin.Context) {
	name := c.Param("name")

	tailStr := c.DefaultQuery("tail", "100")
	tail, err := strconv.Atoi(tailStr)
	if err != nil {
		tail = 100
	}

	logs, err := s.infraManager.GetServiceLogs(name, tail)
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

func (s *Server) getInfraStats(c *gin.Context) {
	stats, err := s.infraManager.GetStats()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"stats": stats,
	})
}

func (s *Server) migrateToInfrastructure(c *gin.Context) {
	name := c.Param("name")

	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Deployment not found",
		})
		return
	}

	metadata := deployment.Metadata
	if metadata == nil {
		metadata = &models.ServiceMetadata{
			Name: name,
		}
	}
	metadata.Type = "infrastructure"

	if err := s.manager.SaveMetadata(name, metadata); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Deployment marked as infrastructure",
		"name":    name,
	})
}

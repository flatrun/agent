package api

import (
	"context"
	cryptoRand "crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/compose-spec/compose-go/v2/loader"
	composetypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/flatrun/agent/internal/ai"
	"github.com/flatrun/agent/internal/audit"
	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/backup"
	"github.com/flatrun/agent/internal/certs"
	"github.com/flatrun/agent/internal/cluster"
	"github.com/flatrun/agent/internal/credentials"
	"github.com/flatrun/agent/internal/dashboards"
	"github.com/flatrun/agent/internal/database"
	"github.com/flatrun/agent/internal/dns"
	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/files"
	"github.com/flatrun/agent/internal/infra"
	"github.com/flatrun/agent/internal/networks"
	"github.com/flatrun/agent/internal/notify"
	"github.com/flatrun/agent/internal/plan"
	"github.com/flatrun/agent/internal/pluginhost"
	"github.com/flatrun/agent/internal/proxy"
	"github.com/flatrun/agent/internal/scheduler"
	"github.com/flatrun/agent/internal/security"
	"github.com/flatrun/agent/internal/setup"
	"github.com/flatrun/agent/internal/source"
	"github.com/flatrun/agent/internal/ssl"
	"github.com/flatrun/agent/internal/system"
	"github.com/flatrun/agent/internal/templatesource"
	"github.com/flatrun/agent/internal/traffic"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
	"github.com/flatrun/agent/pkg/plugins"
	dnsPlugins "github.com/flatrun/agent/pkg/plugins/dns"
	"github.com/flatrun/agent/pkg/plugins/firewall"
	"github.com/flatrun/agent/pkg/subdomain"
	"github.com/flatrun/agent/pkg/version"
	"github.com/flatrun/agent/templates"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

type Server struct {
	config             *config.Config
	configPath         string
	router             *gin.Engine
	server             *http.Server
	manager            *docker.Manager
	certsDiscovery     *certs.Discovery
	networksManager    *networks.Manager
	pluginRegistry     *plugins.Registry
	firewall           *firewall.Plugin
	builtinDNS         []plugins.Plugin
	pluginHost         *pluginhost.Host
	notify             *notify.Service
	pluginToken        string
	authMiddleware     *auth.Middleware
	authManager        *auth.Manager
	proxyOrchestrator  *proxy.Orchestrator
	filesManager       *files.Manager
	systemFilesManager *files.SystemManager
	servicesManager    *system.ServicesManager
	databaseManager    *database.Manager
	infraManager       *infra.Manager
	credentialsManager *credentials.Manager
	sourceRegistry     *source.Registry
	templateSyncer     *templatesource.Syncer
	securityManager    *security.Manager
	trafficManager     *traffic.Manager
	dashboards         *dashboards.Store
	backupManager      *backup.Manager
	schedulerManager   *scheduler.Manager
	auditManager       *audit.Manager
	auditMiddleware    *audit.Middleware
	powerDNSManager    *dns.PowerDNSManager
	clusterManager     *cluster.Manager
	setupManager       *setup.Manager
	setupHandlers      *setup.Handlers
	certRenewer        *ssl.Renewer
	planStore          *plan.Store
	aiProvider         ai.Provider
	aiSessions         *ai.SessionStore
	aiAgents           *ai.AgentStore
	mcpHandler         http.Handler

	jobs *jobRegistry
	// runDeploymentAction runs a deployment action and streams each output
	// line to emit. Overridable in tests so they need not shell out to docker.
	runDeploymentAction func(action, name string, opts actionOptions, emit func(line string)) error
	// runServiceAction runs an action on a single service, streaming output.
	runServiceAction func(action, name, service string, opts actionOptions, emit func(line string)) error

	statsMu    sync.RWMutex
	statsCache gin.H
	statsAt    time.Time
}

// objectStorageNetworkName resolves the shared network self-hosted object
// stores join. It reuses the database network unless a dedicated one is
// configured, so object storage rides the same data-backend network as
// databases by default.
func objectStorageNetworkName(cfg *config.Config) string {
	if n := cfg.Infrastructure.DefaultObjectStorageNetwork; n != "" {
		return n
	}
	return cfg.Infrastructure.DefaultDatabaseNetwork
}

func New(cfg *config.Config, configPath string) *Server {
	if cfg.Logging.Level == "debug" {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	// Exposed to compose substitution so an object-store template joins the
	// configured network (${FLATRUN_OBJECT_NETWORK:-database}) on every up.
	os.Setenv("FLATRUN_OBJECT_NETWORK", objectStorageNetworkName(cfg))

	router := gin.Default()

	if cfg.API.EnableCORS {
		corsConfig := cors.Config{
			AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
			AllowHeaders:     []string{"Content-Type", "Authorization", "Accept", "Origin", "Cache-Control", "X-Requested-With"},
			AllowCredentials: true,
			MaxAge:           12 * time.Hour,
		}
		origins := cfg.API.AllowedOrigins
		allowAll := false
		for _, o := range origins {
			if o == "*" {
				allowAll = true
				break
			}
		}
		if allowAll {
			corsConfig.AllowAllOrigins = true
			corsConfig.AllowCredentials = false
		} else if len(origins) > 0 {
			corsConfig.AllowOrigins = origins
		} else {
			instanceIP := setup.ResolvePublicIP()
			corsConfig.AllowOriginFunc = func(origin string) bool {
				return strings.HasPrefix(origin, "http://"+instanceIP) ||
					strings.HasPrefix(origin, "https://"+instanceIP)
			}
		}
		router.Use(cors.New(corsConfig))
	}

	manager := docker.NewManager(cfg.DeploymentsPath)
	manager.SetCleanupTimeout(cfg.Cleanup.Timeout)

	// Deploys read template copies from disk. Seed the embedded infra and
	// welcome content, then pull the app catalog from its external source into
	// the same on-disk cache.
	builtinTemplatesDir := filepath.Join(cfg.DeploymentsPath, ".flatrun", "templates")
	if err := os.MkdirAll(builtinTemplatesDir, 0755); err == nil {
		ensureBuiltinTemplates(builtinTemplatesDir)
	}
	templateSyncer := newTemplateSyncer(cfg, builtinTemplatesDir)
	certsDiscovery := certs.NewDiscovery(cfg.DeploymentsPath)
	networksManager := networks.NewManager()
	pluginsDir := filepath.Join(cfg.DeploymentsPath, ".flatrun", "plugins")
	pluginRegistry := plugins.NewRegistry(pluginsDir)
	_ = pluginRegistry.LoadFromDisk()

	firewallPlugin := firewall.New(firewall.NewStore(cfg.DeploymentsPath))
	_ = pluginRegistry.Register(firewallPlugin)
	if enforced, err := firewallPlugin.EnforceCurrent(); err != nil {
		log.Printf("firewall: failed to enforce saved policy at startup: %v", err)
	} else if enforced {
		log.Printf("firewall: enforcing saved policy")
	}

	builtinDNS := []plugins.Plugin{
		dnsPlugins.NewCloudflarePlugin(),
		dnsPlugins.NewRoute53Plugin(),
		dnsPlugins.NewDigitalOceanPlugin(),
		dnsPlugins.NewHetznerPlugin(),
	}
	for _, p := range builtinDNS {
		_ = pluginRegistry.Register(p)
	}

	agentURL := fmt.Sprintf("http://127.0.0.1:%d/api", cfg.API.Port)
	// A per-run secret handed to plugins so they can call the internal emit endpoint (e.g. to
	// raise a notification) without the full user auth flow.
	pluginToken := randomToken()
	notifyService := notify.NewService(cfg.DeploymentsPath)
	pluginHost := pluginhost.New(
		filepath.Join(cfg.DeploymentsPath, ".flatrun", "plugins"),
		filepath.Join(cfg.DeploymentsPath, ".flatrun", "run"),
		agentURL,
		pluginToken,
	)
	// Built-in apps ship inside the agent and run by re-execing it with a subcommand, so
	// there is one binary to deploy and no per-arch plugin artifact.
	if self, err := os.Executable(); err == nil {
		pluginHost.Builtin("observability", self, "observ-plugin")
	}
	authMiddleware := auth.NewMiddleware(&cfg.Auth)

	setupManager := setup.NewManager(cfg, configPath)
	setupComplete := setupManager.IsComplete()
	var authManager *auth.Manager
	if authMgr, authErr := auth.NewManager(cfg.DeploymentsPath, &cfg.Auth, setupComplete); authErr != nil {
		log.Printf("Warning: Failed to initialize auth manager: %v", authErr)
	} else {
		authManager = authMgr
		authMiddleware.SetManager(authManager)
	}

	proxyOrchestrator := proxy.NewOrchestrator(cfg)
	filesManager := files.NewManager(cfg.DeploymentsPath)
	systemFilesManager := files.NewSystemManager(cfg.SystemFilesRoot)
	servicesManager := system.NewServicesManager()
	databaseManager := database.NewManager()
	infraManager := infra.NewManager(cfg)
	credentialsManager := credentials.NewManager(cfg.DeploymentsPath)

	var securityManager *security.Manager
	if cfg.Security.Enabled {
		var err error
		securityManager, err = security.NewManager(cfg.DeploymentsPath)
		if err != nil {
			log.Printf("Warning: Failed to initialize security manager: %v", err)
		} else {
			// Apply detection thresholds from config
			securityManager.SetDetectorThresholds(
				cfg.Security.RateThreshold,
				cfg.Security.NotFoundThreshold,
				cfg.Security.AuthFailureThreshold,
				cfg.Security.UniquePathsThreshold,
				cfg.Security.RepeatedHitsThreshold,
				cfg.Security.DetectionWindow,
			)
			nginxConfigPath := cfg.Nginx.ConfigPath
			if nginxConfigPath == "" {
				nginxConfigPath = filepath.Join(cfg.DeploymentsPath, "nginx", "conf.d")
			}
			if err := securityManager.InitNginxConfigs(nginxConfigPath); err != nil {
				log.Printf("Warning: Failed to initialize security nginx configs: %v", err)
			}
			// Add Docker gateway IP to whitelist
			gatewayIP := infraManager.GetDockerHostIP()
			if err := securityManager.AddDockerGatewayToWhitelist(gatewayIP); err != nil {
				log.Printf("Warning: Failed to add Docker gateway to whitelist: %v", err)
			}
		}
	}

	var trafficManager *traffic.Manager
	trafficManager, err := traffic.NewManager(cfg.DeploymentsPath, 7)
	if err != nil {
		log.Printf("Warning: Failed to initialize traffic manager: %v", err)
	}

	var backupManager *backup.Manager
	backupManager, err = backup.NewManager(cfg.DeploymentsPath)
	if err != nil {
		log.Printf("Warning: Failed to initialize backup manager: %v", err)
	}

	var auditManager *audit.Manager
	var auditMiddleware *audit.Middleware
	if cfg.Audit.Enabled {
		auditConfig := &audit.Config{
			Enabled:            cfg.Audit.Enabled,
			RetentionDays:      cfg.Audit.RetentionDays,
			CaptureRequestBody: cfg.Audit.CaptureRequestBody,
			ExcludedPaths:      cfg.Audit.ExcludedPaths,
			SensitiveFields:    cfg.Audit.SensitiveFields,
			CleanupInterval:    cfg.Audit.CleanupInterval,
		}
		auditManager, err = audit.NewManager(cfg.DeploymentsPath, auditConfig)
		if err != nil {
			log.Printf("Warning: Failed to initialize audit manager: %v", err)
		} else {
			auditMiddleware = audit.NewMiddleware(auditManager)
		}
	}

	powerDNSManager := dns.NewPowerDNSManager(cfg)

	var clusterManager *cluster.Manager
	if cfg.Cluster.Enabled {
		clusterDB, clusterErr := cluster.NewDB(cfg.DeploymentsPath)
		if clusterErr != nil {
			log.Printf("Warning: Failed to initialize cluster database: %v", clusterErr)
		} else {
			healthInterval, _ := time.ParseDuration(cfg.Cluster.HealthInterval)
			if healthInterval == 0 {
				healthInterval = 30 * time.Second
			}
			requestTimeout, _ := time.ParseDuration(cfg.Cluster.RequestTimeout)
			if requestTimeout == 0 {
				requestTimeout = 10 * time.Second
			}
			clusterManager = cluster.NewManager(clusterDB, cfg.Cluster.ServerName, healthInterval, requestTimeout, cfg.Auth.JWTSecret)
			if startErr := clusterManager.Start(context.Background()); startErr != nil {
				log.Printf("Warning: Failed to start cluster manager: %v", startErr)
				clusterManager = nil
			}
		}
	}

	s := &Server{
		config:             cfg,
		configPath:         configPath,
		router:             router,
		manager:            manager,
		certsDiscovery:     certsDiscovery,
		networksManager:    networksManager,
		pluginRegistry:     pluginRegistry,
		firewall:           firewallPlugin,
		builtinDNS:         builtinDNS,
		pluginHost:         pluginHost,
		notify:             notifyService,
		pluginToken:        pluginToken,
		authMiddleware:     authMiddleware,
		authManager:        authManager,
		proxyOrchestrator:  proxyOrchestrator,
		filesManager:       filesManager,
		systemFilesManager: systemFilesManager,
		servicesManager:    servicesManager,
		databaseManager:    databaseManager,
		infraManager:       infraManager,
		credentialsManager: credentialsManager,
		sourceRegistry:     source.NewRegistry(source.GitProvider{}),
		templateSyncer:     templateSyncer,
		securityManager:    securityManager,
		trafficManager:     trafficManager,
		dashboards:         dashboards.NewStore(cfg.DeploymentsPath),
		backupManager:      backupManager,
		auditManager:       auditManager,
		auditMiddleware:    auditMiddleware,
		powerDNSManager:    powerDNSManager,
		clusterManager:     clusterManager,
		setupManager:       setupManager,
		setupHandlers:      setup.NewHandlers(setupManager, authManager),
		planStore:          plan.NewStore(cfg.DeploymentsPath),
		aiSessions:         ai.NewSessionStore(cfg.DeploymentsPath),
		aiAgents:           ai.NewAgentStore(cfg.DeploymentsPath),
		jobs:               newJobRegistry(),
	}
	s.runDeploymentAction = s.defaultRunDeploymentAction
	s.runServiceAction = s.defaultRunServiceAction

	// Built unconditionally: it is stateless and starts nothing, so requests are
	// gated on the live config flag instead, letting mcp.enabled toggle at runtime.
	s.mcpHandler = s.newMCPHandler()

	s.planStore.StartPruneLoop(context.Background(), time.Hour, time.Duration(cfg.Plans.RetentionDays)*24*time.Hour)

	syncInterval := 3600
	if cfg.Templates.SyncInterval != nil {
		syncInterval = *cfg.Templates.SyncInterval
	}
	s.startTemplateSyncLoop(context.Background(), time.Duration(syncInterval)*time.Second)

	if provider, aiErr := ai.New(&cfg.AI); aiErr == nil {
		s.aiProvider = provider
	} else if aiErr != ai.ErrDisabled {
		log.Printf("Warning: failed to initialize AI provider: %v", aiErr)
	}

	if backupManager != nil {
		if err := s.applyBackupDestinations(); err != nil {
			log.Printf("Warning: %v", err)
		}

		executor := scheduler.NewExecutor(backupManager, manager)
		executor.SetAgentRunner(s.runAgentHeadless)
		schedulerManager, err := scheduler.NewManager(cfg.DeploymentsPath, executor)
		if err != nil {
			log.Printf("Warning: Failed to initialize scheduler manager: %v", err)
		} else {
			s.schedulerManager = schedulerManager
			schedulerManager.Start()
		}
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

		// WebSocket endpoint handles its own auth via first-message
		api.GET("/containers/:id/exec", s.containerExec)
		api.GET("/system/terminal", s.systemTerminal)
		api.GET("/system/terminal/interactive", s.systemTerminalInteractive)
		api.GET("/deployments/:name/jobs/:jobId/stream", s.streamDeploymentJob)
		api.GET("/deployments/:name/logs/stream", s.streamDeploymentLogs)

		// Setup endpoints (public, gated by setup state)
		setupGroup := api.Group("/setup")
		{
			setupGroup.GET("/status", s.setupHandlers.GetStatus)

			guarded := setupGroup.Group("")
			guarded.GET("/info", s.setupHandlers.GetInfo)
			guarded.Use(s.setupHandlers.Guard())
			{
				guarded.POST("/validate", s.setupHandlers.ValidateSystem)
				guarded.GET("/verify-dns", s.setupHandlers.VerifyDNS)
				guarded.POST("/settings", s.setupHandlers.ConfigureSettings)
				guarded.POST("/authentication", s.setupHandlers.ConfigureAuthentication)
				guarded.POST("/complete", s.setupHandlers.Complete)
			}
		}

		protected := api.Group("")
		protected.Use(s.authMiddleware.RequireAuth())
		if s.auditMiddleware != nil {
			protected.Use(s.auditMiddleware.Capture())
		}
		{
			// Deployment endpoints
			protected.GET("/deployments", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.listDeployments)
			protected.GET("/deployments/:name", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead), s.getDeployment)
			protected.POST("/deployments", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.createDeployment)
			protected.PUT("/deployments/:name", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.updateDeployment)
			protected.PUT("/deployments/:name/metadata", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.updateDeploymentMetadata)
			protected.PUT("/deployments/:name/protected-mode", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelAdmin), s.updateDeploymentProtectedMode)
			protected.DELETE("/deployments/:name", s.authMiddleware.RequirePermission(auth.PermDeploymentsDelete), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelAdmin), s.deleteDeployment)
			protected.POST("/deployments/:name/start", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.startDeployment)
			protected.POST("/deployments/:name/stop", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.stopDeployment)
			protected.POST("/deployments/:name/restart", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.restartDeployment)
			protected.POST("/deployments/:name/rebuild", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.rebuildDeployment)
			protected.POST("/deployments/:name/deploy", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.deployDeployment)
			protected.POST("/deployments/:name/pull", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.pullDeploymentImage)
			protected.GET("/deployments/:name/images", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead), s.getDeploymentImages)
			protected.GET("/deployments/:name/jobs/active", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead), s.getActiveDeploymentJob)
			protected.GET("/deployments/:name/jobs/:jobId", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead), s.getDeploymentJob)
			protected.POST("/deployments/:name/actions/:actionId", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.executeQuickAction)
			protected.GET("/deployments/:name/logs", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead), s.getDeploymentLogs)
			protected.GET("/deployments/:name/log-sources", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead), s.getDeploymentLogSources)
			protected.PUT("/deployments/:name/log-sources", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.updateDeploymentLogSources)
			protected.GET("/deployments/:name/compose", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead), s.getDeploymentCompose)
			protected.POST("/deployments/:name/compose/mount", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.addDeploymentComposeMount)
			protected.POST("/deployments/:name/compose/unmount", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.removeDeploymentComposeMount)

			// Network endpoints
			protected.GET("/networks", s.authMiddleware.RequirePermission(auth.PermNetworksRead), s.listNetworks)
			protected.POST("/networks", s.authMiddleware.RequirePermission(auth.PermNetworksWrite), s.createNetwork)
			protected.DELETE("/networks/:name", s.authMiddleware.RequirePermission(auth.PermNetworksDelete), s.deleteNetwork)
			protected.POST("/networks/:name/connect", s.authMiddleware.RequirePermission(auth.PermNetworksWrite), s.connectContainer)
			protected.POST("/networks/:name/disconnect", s.authMiddleware.RequirePermission(auth.PermNetworksWrite), s.disconnectContainer)

			// Certificate endpoints
			protected.GET("/certificates", s.authMiddleware.RequirePermission(auth.PermCertificatesRead), s.listCertificates)
			protected.POST("/certificates", s.authMiddleware.RequirePermission(auth.PermCertificatesWrite), s.requestCertificate)
			protected.POST("/certificates/renew", s.authMiddleware.RequirePermission(auth.PermCertificatesWrite), s.renewCertificates)
			protected.GET("/certificates/:domain", s.authMiddleware.RequirePermission(auth.PermCertificatesRead), s.getCertificate)
			protected.POST("/certificates/:domain/renew", s.authMiddleware.RequirePermission(auth.PermCertificatesWrite), s.renewCertificate)
			protected.PATCH("/certificates/:domain/auto-renew", s.authMiddleware.RequirePermission(auth.PermCertificatesWrite), s.setCertificateAutoRenew)
			protected.DELETE("/certificates/:domain", s.authMiddleware.RequirePermission(auth.PermCertificatesDelete), s.deleteCertificate)
			protected.POST("/deployments/:name/certificates/renew", s.authMiddleware.RequirePermission(auth.PermCertificatesWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.renewDeploymentCertificates)

			// Proxy endpoints
			protected.GET("/proxy/status/:name", s.authMiddleware.RequirePermission(auth.PermCertificatesRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead), s.getProxyStatus)
			protected.POST("/proxy/setup/:name", s.authMiddleware.RequirePermission(auth.PermCertificatesWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.setupProxy)
			protected.DELETE("/proxy/:name", s.authMiddleware.RequirePermission(auth.PermCertificatesDelete), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.teardownProxy)
			protected.GET("/proxy/vhosts", s.authMiddleware.RequirePermission(auth.PermCertificatesRead), s.listVirtualHosts)
			protected.POST("/proxy/sync", s.authMiddleware.RequirePermission(auth.PermCertificatesWrite), s.syncAllProxies)
			protected.POST("/deployments/:name/ssl/disable", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.disableSSL)

			// Compose services endpoint
			protected.GET("/deployments/:name/services", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead), s.listDeploymentServices)

			// Domain endpoints
			protected.GET("/deployments/:name/domains", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead), s.listDomains)
			protected.POST("/deployments/:name/domains", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.addDomain)
			protected.PUT("/deployments/:name/domains/:domainId", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.updateDomain)
			protected.DELETE("/deployments/:name/domains/:domainId", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.deleteDomain)

			// Settings endpoints
			protected.GET("/settings", s.authMiddleware.RequirePermission(auth.PermSettingsRead), s.getSettings)
			protected.PUT("/settings", s.authMiddleware.RequirePermission(auth.PermSettingsWrite), s.updateSettings)
			protected.PUT("/settings/security", s.authMiddleware.RequirePermission(auth.PermSettingsWrite), s.updateSecuritySettings)

			protected.GET("/agent/update", s.authMiddleware.RequirePermission(auth.PermSettingsRead), s.getAgentUpdate)
			protected.POST("/agent/update", s.authMiddleware.RequirePermission(auth.PermSettingsWrite), s.triggerAgentUpdate)
			protected.GET("/notifications/targets", s.authMiddleware.RequirePermission(auth.PermSettingsRead), s.getNotificationTargets)
			protected.PUT("/notifications/targets", s.authMiddleware.RequirePermission(auth.PermSettingsWrite), s.updateNotificationTargets)
			protected.POST("/notifications/test", s.authMiddleware.RequirePermission(auth.PermSettingsWrite), s.testNotification)
			protected.GET("/config", s.authMiddleware.RequirePermission(auth.PermConfigRead), s.listConfig)
			protected.GET("/config/*key", s.authMiddleware.RequirePermission(auth.PermConfigRead), s.getConfigKey)
			protected.PUT("/config/*key", s.authMiddleware.RequirePermission(auth.PermConfigWrite), s.updateConfigKey)

			// Plan endpoints (previewed mutations; see internal/plan)
			protected.GET("/plans", s.listPlans)
			protected.GET("/plans/:id", s.getPlan)
			protected.POST("/plans/:id/apply", s.applyPlan)
			protected.DELETE("/plans/:id", s.deletePlan)

			// Service-level actions
			protected.POST("/deployments/:name/services/:service/start", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.serviceActionHandler("start"))
			protected.POST("/deployments/:name/services/:service/stop", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.serviceActionHandler("stop"))
			protected.POST("/deployments/:name/services/:service/restart", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.serviceActionHandler("restart"))
			protected.POST("/deployments/:name/services/:service/job", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.enqueueServiceJob)
			protected.POST("/deployments/:name/services/:service/rebuild", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.serviceActionHandler("rebuild"))
			protected.POST("/deployments/:name/services/:service/pull", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.serviceActionHandler("pull"))

			// AI assist endpoints
			protected.GET("/ai/status", s.getAIStatus)
			protected.POST("/ai/analyze", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.aiAssistSystem)
			protected.POST("/deployments/:name/ai/analyze", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead), s.aiAssistDeployment)

			// MCP server: the same tool set the assistant uses, exposed to
			// external MCP clients. Each tool self-gates on the caller's
			// permissions, so the route only requires authentication. The
			// handler itself refuses calls while mcp.enabled is off.
			protected.Any("/mcp", s.mcpHTTP)

			// Agents defined as flat markdown files, executed by the runtime
			// as AI sessions through the same tools and approval gates.
			protected.GET("/ai/agents", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.listAgents)
			protected.GET("/ai/agents/:name", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.getAgent)
			protected.PUT("/ai/agents/:name", s.authMiddleware.RequirePermission(auth.PermSettingsWrite), s.putAgent)
			protected.DELETE("/ai/agents/:name", s.authMiddleware.RequirePermission(auth.PermSettingsWrite), s.deleteAgent)
			protected.POST("/ai/agents/:name/run", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.runAgent)

			// Interactive AI sessions (agentic tool loop)
			protected.POST("/ai/sessions", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.createAISession)
			protected.GET("/ai/sessions", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.listAISessions)
			protected.GET("/ai/sessions/:id", s.getAISession)
			protected.POST("/ai/sessions/:id/messages", s.postAISessionMessage)
			protected.POST("/ai/sessions/:id/approve", s.approveAISessionTools)
			protected.DELETE("/ai/sessions/:id", s.deleteAISession)

			// Compose, stats, subdomain (deployment-scoped)
			protected.GET("/subdomain/generate", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.generateSubdomain)
			protected.POST("/compose/update", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.updateCompose)
			protected.GET("/stats", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.getSystemStats)

			// Server info and network health endpoints
			protected.GET("/server/info", s.authMiddleware.RequirePermission(auth.PermSystemRead), s.getServerInfo)
			protected.GET("/server/network-health", s.authMiddleware.RequirePermission(auth.PermSystemRead), s.getNetworkHealth)

			// Template and plugin endpoints
			protected.GET("/plugins", s.authMiddleware.RequirePermission(auth.PermTemplatesRead), s.listPlugins)
			protected.GET("/plugins/:name", s.authMiddleware.RequirePermission(auth.PermTemplatesRead), s.getPlugin)
			protected.POST("/plugins/:name/deployments", s.authMiddleware.RequirePermission(auth.PermTemplatesWrite), s.createPluginDeployment)

			protected.Any("/plugin/:name/*proxyPath", s.authMiddleware.RequirePermission(auth.PermTemplatesRead), s.proxyToPlugin)
			protected.Any("/marketplace/*path", s.authMiddleware.RequirePermission(auth.PermTemplatesRead), s.proxyMarketplace)
			protected.GET("/templates", s.authMiddleware.RequirePermission(auth.PermTemplatesRead), s.listTemplates)
			protected.GET("/templates/categories", s.authMiddleware.RequirePermission(auth.PermTemplatesRead), s.getTemplateCategories)
			protected.POST("/templates/refresh", s.authMiddleware.RequirePermission(auth.PermTemplatesWrite), s.refreshTemplates)
			protected.GET("/templates/:id/compose", s.authMiddleware.RequirePermission(auth.PermTemplatesRead), s.getTemplateCompose)
			protected.POST("/templates/:id/generate", s.authMiddleware.RequirePermission(auth.PermTemplatesWrite), s.generateTemplateCompose)
			protected.GET("/templates/infra/:name/compose", s.authMiddleware.RequirePermission(auth.PermTemplatesRead), s.getInfraTemplateCompose)
			protected.POST("/templates/infra/:name/generate", s.authMiddleware.RequirePermission(auth.PermTemplatesWrite), s.generateInfraTemplateCompose)

			// Container endpoints
			protected.GET("/containers", s.authMiddleware.RequirePermission(auth.PermContainersRead), s.listContainers)
			protected.POST("/containers/:id/start", s.authMiddleware.RequirePermission(auth.PermContainersWrite), s.startContainer)
			protected.POST("/containers/:id/stop", s.authMiddleware.RequirePermission(auth.PermContainersWrite), s.stopContainer)
			protected.POST("/containers/:id/restart", s.authMiddleware.RequirePermission(auth.PermContainersWrite), s.restartContainer)
			protected.DELETE("/containers/:id", s.authMiddleware.RequirePermission(auth.PermContainersDelete), s.removeContainer)
			protected.GET("/containers/:id/logs", s.authMiddleware.RequirePermission(auth.PermContainersRead), s.getContainerLogs)
			protected.GET("/containers/:id/stats", s.authMiddleware.RequirePermission(auth.PermContainersRead), s.getContainerStats)
			protected.GET("/containers/stats", s.authMiddleware.RequirePermission(auth.PermContainersRead), s.getAllContainerStats)
			protected.POST("/containers/:id/exec", s.authMiddleware.RequirePermission(auth.PermContainersWrite), s.containerExecHTTP)
			protected.GET("/containers/:id/resources", s.authMiddleware.RequirePermission(auth.PermContainersRead), s.getContainerResources)
			protected.PUT("/containers/:id/resources", s.authMiddleware.RequirePermission(auth.PermContainersWrite), s.updateContainerResources)
			protected.GET("/deployments/:name/stats", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead), s.getDeploymentContainerStats)
			protected.GET("/deployments/:name/resources", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead), s.getDeploymentResources)

			// Image endpoints
			protected.GET("/images", s.authMiddleware.RequirePermission(auth.PermImagesRead), s.listImages)
			protected.DELETE("/images/:id", s.authMiddleware.RequirePermission(auth.PermImagesDelete), s.removeImage)
			protected.POST("/images/pull", s.authMiddleware.RequirePermission(auth.PermImagesWrite), s.pullImage)
			protected.POST("/images/cleanup", s.authMiddleware.RequirePermission(auth.PermImagesDelete), s.cleanupSystemImages)
			protected.POST("/deployments/:name/images/cleanup", s.authMiddleware.RequirePermission(auth.PermImagesWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.cleanupDeploymentImages)

			// Volume endpoints
			protected.GET("/volumes", s.authMiddleware.RequirePermission(auth.PermVolumesRead), s.listVolumes)
			protected.POST("/volumes", s.authMiddleware.RequirePermission(auth.PermVolumesWrite), s.createVolume)
			protected.DELETE("/volumes/:name", s.authMiddleware.RequirePermission(auth.PermVolumesDelete), s.removeVolume)
			protected.POST("/volumes/prune", s.authMiddleware.RequirePermission(auth.PermVolumesWrite), s.pruneVolumes)

			// Port endpoints
			protected.GET("/ports", s.authMiddleware.RequirePermission(auth.PermSystemRead), s.listPorts)
			protected.POST("/ports/:pid/kill", s.authMiddleware.RequirePermission(auth.PermSystemWrite), s.killProcess)

			// System service endpoints
			protected.GET("/system/services", s.authMiddleware.RequirePermission(auth.PermSystemRead), s.listSystemServices)
			protected.POST("/system/services/:name/start", s.authMiddleware.RequirePermission(auth.PermSystemWrite), s.startSystemService)
			protected.POST("/system/services/:name/stop", s.authMiddleware.RequirePermission(auth.PermSystemWrite), s.stopSystemService)
			protected.POST("/system/services/:name/restart", s.authMiddleware.RequirePermission(auth.PermSystemWrite), s.restartSystemService)

			// Deployment file endpoints
			protected.GET("/deployments/:name/files", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead), s.listDeploymentFiles)
			protected.GET("/deployments/:name/files/*path", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead), s.getDeploymentFile)
			protected.POST("/deployments/:name/files/*path", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.uploadDeploymentFile)
			protected.DELETE("/deployments/:name/files/*path", s.authMiddleware.RequirePermission(auth.PermDeploymentsDelete), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelAdmin), s.deleteDeploymentFile)
			protected.POST("/deployments/:name/mkdir/*path", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.createDeploymentDir)
			protected.POST("/deployments/:name/touch/*path", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.createDeploymentFile)
			protected.PUT("/deployments/:name/permissions/*path", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.chmodDeploymentFile)
			protected.GET("/deployments/:name/files-info", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead), s.getDeploymentFilesInfo)

			// Container file endpoints: browse what a running service holds, and
			// bring a path onto the host where the file endpoints above can edit it.
			protected.GET("/deployments/:name/container-files/:service", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead), s.listContainerFiles)
			protected.POST("/deployments/:name/container-files/:service/materialize", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.materializeContainerPath)

			// System file endpoints (admin-only, scoped to SystemFilesRoot)
			protected.GET("/system/files", s.authMiddleware.RequirePermission(auth.PermSystemFiles), s.listSystemFiles)
			protected.GET("/system/files-info", s.authMiddleware.RequirePermission(auth.PermSystemFiles), s.getSystemFilesInfo)
			protected.GET("/system/files/*path", s.authMiddleware.RequirePermission(auth.PermSystemFiles), s.getSystemFile)
			protected.POST("/system/files/*path", s.authMiddleware.RequirePermission(auth.PermSystemFiles), s.uploadSystemFile)
			protected.DELETE("/system/files/*path", s.authMiddleware.RequirePermission(auth.PermSystemFiles), s.deleteSystemFile)
			protected.POST("/system/mkdir/*path", s.authMiddleware.RequirePermission(auth.PermSystemFiles), s.createSystemDir)
			protected.POST("/system/touch/*path", s.authMiddleware.RequirePermission(auth.PermSystemFiles), s.createSystemFile)
			protected.PUT("/system/permissions/*path", s.authMiddleware.RequirePermission(auth.PermSystemFiles), s.chmodSystemFile)

			// Deployment environment endpoints
			protected.GET("/deployments/:name/env", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead), s.getDeploymentEnv)
			protected.PUT("/deployments/:name/env", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.updateDeploymentEnv)

			// Database endpoints
			protected.POST("/databases/test", s.authMiddleware.RequirePermission(auth.PermDatabasesRead), s.testDatabaseConnection)
			protected.POST("/databases/list", s.authMiddleware.RequirePermission(auth.PermDatabasesRead), s.listDatabasesInServer)
			protected.POST("/databases/tables", s.authMiddleware.RequirePermission(auth.PermDatabasesRead), s.listDatabaseTables)
			protected.POST("/databases/tables/data", s.authMiddleware.RequirePermission(auth.PermDatabasesRead), s.queryTableData)
			protected.POST("/databases/tables/schema", s.authMiddleware.RequirePermission(auth.PermDatabasesRead), s.describeTable)
			protected.POST("/databases/query", s.authMiddleware.RequirePermission(auth.PermDatabasesWrite), s.executeDatabaseQuery)
			protected.POST("/databases/users", s.authMiddleware.RequirePermission(auth.PermDatabasesRead), s.listDatabaseUsers)
			protected.POST("/databases/users/by-database", s.authMiddleware.RequirePermission(auth.PermDatabasesRead), s.listUsersByDatabase)
			protected.POST("/databases/create", s.authMiddleware.RequirePermission(auth.PermDatabasesWrite), s.createDatabaseInServer)
			protected.POST("/databases/delete", s.authMiddleware.RequirePermission(auth.PermDatabasesWrite), s.deleteDatabaseInServer)
			protected.POST("/databases/users/create", s.authMiddleware.RequirePermission(auth.PermDatabasesWrite), s.createDatabaseUser)
			protected.POST("/databases/users/delete", s.authMiddleware.RequirePermission(auth.PermDatabasesWrite), s.deleteDatabaseUser)
			protected.POST("/databases/privileges/grant", s.authMiddleware.RequirePermission(auth.PermDatabasesWrite), s.grantDatabasePrivileges)

			// Infrastructure endpoints
			protected.GET("/infrastructure", s.authMiddleware.RequirePermission(auth.PermInfrastructureRead), s.listInfrastructure)
			protected.GET("/infrastructure/stats", s.authMiddleware.RequirePermission(auth.PermInfrastructureRead), s.getInfraStats)
			protected.GET("/infrastructure/:name", s.authMiddleware.RequirePermission(auth.PermInfrastructureRead), s.getInfraService)
			protected.POST("/infrastructure/:name/start", s.authMiddleware.RequirePermission(auth.PermInfrastructureWrite), s.startInfraService)
			protected.POST("/infrastructure/:name/stop", s.authMiddleware.RequirePermission(auth.PermInfrastructureWrite), s.stopInfraService)
			protected.POST("/infrastructure/:name/restart", s.authMiddleware.RequirePermission(auth.PermInfrastructureWrite), s.restartInfraService)
			protected.GET("/infrastructure/:name/logs", s.authMiddleware.RequirePermission(auth.PermInfrastructureRead), s.getInfraServiceLogs)
			protected.POST("/infrastructure/migrate/:name", s.authMiddleware.RequirePermission(auth.PermInfrastructureWrite), s.migrateToInfrastructure)

			// Registry endpoints
			protected.GET("/registries", s.authMiddleware.RequirePermission(auth.PermRegistriesRead), s.listRegistryTypes)
			protected.GET("/registries/:slug", s.authMiddleware.RequirePermission(auth.PermRegistriesRead), s.getRegistryType)
			protected.POST("/registries", s.authMiddleware.RequirePermission(auth.PermRegistriesWrite), s.createRegistryType)
			protected.PUT("/registries/:slug", s.authMiddleware.RequirePermission(auth.PermRegistriesWrite), s.updateRegistryType)
			protected.DELETE("/registries/:slug", s.authMiddleware.RequirePermission(auth.PermRegistriesDelete), s.deleteRegistryType)

			// Credential endpoints
			protected.GET("/credentials", s.authMiddleware.RequirePermission(auth.PermRegistriesRead), s.listCredentials)
			protected.GET("/credentials/:id", s.authMiddleware.RequirePermission(auth.PermRegistriesRead), s.getCredential)
			protected.POST("/credentials", s.authMiddleware.RequirePermission(auth.PermRegistriesWrite), s.createCredential)
			protected.PUT("/credentials/:id", s.authMiddleware.RequirePermission(auth.PermRegistriesWrite), s.updateCredential)
			protected.DELETE("/credentials/:id", s.authMiddleware.RequirePermission(auth.PermRegistriesDelete), s.deleteCredential)
			protected.POST("/credentials/:id/test", s.authMiddleware.RequirePermission(auth.PermRegistriesRead), s.testCredential)

			protected.GET("/source-credentials", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.listSourceCredentials)
			protected.POST("/source-credentials", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.createSourceCredential)
			protected.DELETE("/source-credentials/:id", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.deleteSourceCredential)

			// Storage credential endpoints (S3 and other object-storage secrets)
			protected.GET("/storage-credentials", s.authMiddleware.RequirePermission(auth.PermBackupsRead), s.listStorageCredentials)
			protected.POST("/storage-credentials", s.authMiddleware.RequirePermission(auth.PermBackupsWrite), s.createStorageCredential)
			protected.PUT("/storage-credentials/:id", s.authMiddleware.RequirePermission(auth.PermBackupsWrite), s.updateStorageCredential)
			protected.DELETE("/storage-credentials/:id", s.authMiddleware.RequirePermission(auth.PermBackupsDelete), s.deleteStorageCredential)

			// Security endpoints
			protected.GET("/security/stats", s.authMiddleware.RequirePermission(auth.PermSecurityRead), s.getSecurityStats)
			protected.GET("/security/events", s.authMiddleware.RequirePermission(auth.PermSecurityRead), s.listSecurityEvents)
			protected.GET("/security/events/:id", s.authMiddleware.RequirePermission(auth.PermSecurityRead), s.getSecurityEvent)
			protected.POST("/security/cleanup", s.authMiddleware.RequirePermission(auth.PermSecurityWrite), s.cleanupSecurityEvents)
			protected.GET("/security/blocked-ips", s.authMiddleware.RequirePermission(auth.PermSecurityRead), s.listBlockedIPs)
			protected.POST("/security/blocked-ips", s.authMiddleware.RequirePermission(auth.PermSecurityWrite), s.blockIP)
			protected.DELETE("/security/blocked-ips/:ip", s.authMiddleware.RequirePermission(auth.PermSecurityWrite), s.unblockIP)
			protected.GET("/security/ips/:ip/events", s.authMiddleware.RequirePermission(auth.PermSecurityRead), s.getEventsByIP)
			protected.GET("/security/protected-routes", s.authMiddleware.RequirePermission(auth.PermSecurityRead), s.listProtectedRoutes)
			protected.POST("/security/protected-routes", s.authMiddleware.RequirePermission(auth.PermSecurityWrite), s.addProtectedRoute)
			protected.PUT("/security/protected-routes/:id", s.authMiddleware.RequirePermission(auth.PermSecurityWrite), s.updateProtectedRoute)
			protected.DELETE("/security/protected-routes/:id", s.authMiddleware.RequirePermission(auth.PermSecurityWrite), s.deleteProtectedRoute)
			protected.GET("/security/whitelist", s.authMiddleware.RequirePermission(auth.PermSecurityRead), s.listWhitelist)
			protected.POST("/security/whitelist", s.authMiddleware.RequirePermission(auth.PermSecurityWrite), s.addWhitelistEntry)
			protected.DELETE("/security/whitelist/:id", s.authMiddleware.RequirePermission(auth.PermSecurityWrite), s.removeWhitelistEntry)
			protected.GET("/security/realtime-capture", s.authMiddleware.RequirePermission(auth.PermSecurityRead), s.getRealtimeCaptureStatus)
			protected.PUT("/security/realtime-capture", s.authMiddleware.RequirePermission(auth.PermSecurityWrite), s.setRealtimeCaptureStatus)
			protected.GET("/security/health", s.authMiddleware.RequirePermission(auth.PermSecurityRead), s.getSecurityHealth)
			protected.POST("/security/refresh", s.authMiddleware.RequirePermission(auth.PermSecurityWrite), s.refreshSecurityScripts)
			protected.GET("/deployments/:name/security", s.authMiddleware.RequirePermission(auth.PermSecurityRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead), s.getDeploymentSecurity)
			protected.PUT("/deployments/:name/security", s.authMiddleware.RequirePermission(auth.PermSecurityWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.updateDeploymentSecurity)
			protected.GET("/deployments/:name/security/events", s.authMiddleware.RequirePermission(auth.PermSecurityRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead), s.getDeploymentSecurityEvents)

			// Traffic endpoints
			// Dashboards an operator builds over their own telemetry.
			protected.GET("/dashboards", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.listDashboards)
			protected.GET("/dashboards/:id", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.getDashboard)
			protected.POST("/dashboards", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.saveDashboard)
			protected.DELETE("/dashboards/:id", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.deleteDashboard)

			protected.GET("/traffic/logs", s.authMiddleware.RequirePermission(auth.PermTrafficRead), s.getTrafficLogs)
			protected.GET("/traffic/stats", s.authMiddleware.RequirePermission(auth.PermTrafficRead), s.getTrafficStats)
			protected.GET("/traffic/unknown-domains", s.authMiddleware.RequirePermission(auth.PermTrafficRead), s.getUnknownDomainStats)
			protected.POST("/traffic/cleanup", s.authMiddleware.RequirePermission(auth.PermTrafficWrite), s.cleanupTrafficLogs)
			protected.GET("/deployments/:name/traffic", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead), s.getDeploymentTrafficStats)
			protected.GET("/deployments/:name/serving", s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead), s.getDeploymentRED)

			// Backup endpoints
			protected.GET("/backups", s.authMiddleware.RequirePermission(auth.PermBackupsRead), s.listBackups)
			protected.GET("/backups/:id", s.authMiddleware.RequirePermission(auth.PermBackupsRead), s.getBackup)
			protected.POST("/backups", s.authMiddleware.RequirePermission(auth.PermBackupsWrite), s.createBackup)
			protected.DELETE("/backups/:id", s.authMiddleware.RequirePermission(auth.PermBackupsDelete), s.deleteBackup)
			protected.GET("/backups/:id/download", s.authMiddleware.RequirePermission(auth.PermBackupsRead), s.downloadBackup)
			protected.GET("/deployments/:name/backups", s.authMiddleware.RequirePermission(auth.PermBackupsRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead), s.listDeploymentBackups)
			protected.POST("/deployments/:name/backups", s.authMiddleware.RequirePermission(auth.PermBackupsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.createDeploymentBackup)
			protected.GET("/deployments/:name/backup-config", s.authMiddleware.RequirePermission(auth.PermBackupsRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead), s.getDeploymentBackupConfig)
			protected.PUT("/deployments/:name/backup-config", s.authMiddleware.RequirePermission(auth.PermBackupsWrite), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite), s.updateDeploymentBackupConfig)
			protected.POST("/backups/:id/restore", s.authMiddleware.RequirePermission(auth.PermBackupsWrite), s.restoreBackup)
			protected.GET("/backups/jobs", s.authMiddleware.RequirePermission(auth.PermBackupsRead), s.listBackupJobs)
			protected.GET("/backups/jobs/:id", s.authMiddleware.RequirePermission(auth.PermBackupsRead), s.getBackupJob)

			// Remote backup destinations
			protected.GET("/backup-destinations", s.authMiddleware.RequirePermission(auth.PermBackupsRead), s.listBackupDestinations)
			protected.POST("/backup-destinations/test", s.authMiddleware.RequirePermission(auth.PermBackupsWrite), s.testBackupDestination)

			// Object stores
			protected.POST("/object-stores/provision-managed", s.authMiddleware.RequirePermission(auth.PermBackupsWrite), s.provisionManagedObjectStore)
			protected.GET("/object-stores/:name/buckets", s.authMiddleware.RequirePermission(auth.PermBackupsRead), s.listStoreBuckets)
			protected.POST("/object-stores/:name/buckets", s.authMiddleware.RequirePermission(auth.PermBackupsWrite), s.createStoreBucket)
			protected.DELETE("/object-stores/:name/buckets/:bucket", s.authMiddleware.RequirePermission(auth.PermBackupsDelete), s.deleteStoreBucket)
			protected.GET("/object-stores/:name/objects", s.authMiddleware.RequirePermission(auth.PermBackupsRead), s.listStoreObjects)
			protected.POST("/object-stores/:name/objects", s.authMiddleware.RequirePermission(auth.PermBackupsWrite), s.uploadStoreObject)
			protected.GET("/object-stores/:name/objects/download", s.authMiddleware.RequirePermission(auth.PermBackupsRead), s.downloadStoreObject)
			protected.DELETE("/object-stores/:name/objects", s.authMiddleware.RequirePermission(auth.PermBackupsWrite), s.deleteStoreObject)
			protected.POST("/object-stores/:name/attach", s.authMiddleware.RequirePermission(auth.PermDeploymentsWrite), s.attachStoreToDeployment)
			protected.POST("/object-stores/:name/replicate", s.authMiddleware.RequirePermission(auth.PermBackupsWrite), s.replicateStore)

			// Scheduler endpoints
			protected.GET("/scheduler/tasks", s.authMiddleware.RequirePermission(auth.PermSchedulerRead), s.listScheduledTasks)
			protected.GET("/scheduler/tasks/:id", s.authMiddleware.RequirePermission(auth.PermSchedulerRead), s.getScheduledTask)
			protected.POST("/scheduler/tasks", s.authMiddleware.RequirePermission(auth.PermSchedulerWrite), s.createScheduledTask)
			protected.PUT("/scheduler/tasks/:id", s.authMiddleware.RequirePermission(auth.PermSchedulerWrite), s.updateScheduledTask)
			protected.DELETE("/scheduler/tasks/:id", s.authMiddleware.RequirePermission(auth.PermSchedulerDelete), s.deleteScheduledTask)
			protected.POST("/scheduler/tasks/:id/run", s.authMiddleware.RequirePermission(auth.PermSchedulerWrite), s.runTaskNow)
			protected.GET("/scheduler/tasks/:id/executions", s.authMiddleware.RequirePermission(auth.PermSchedulerRead), s.getTaskExecutions)
			protected.GET("/scheduler/executions", s.authMiddleware.RequirePermission(auth.PermSchedulerRead), s.getRecentExecutions)

			// Audit endpoints
			protected.GET("/audit/events", s.authMiddleware.RequirePermission(auth.PermAuditRead), s.listAuditEvents)
			protected.GET("/audit/events/:id", s.authMiddleware.RequirePermission(auth.PermAuditRead), s.getAuditEvent)
			protected.GET("/audit/stats", s.authMiddleware.RequirePermission(auth.PermAuditRead), s.getAuditStats)
			protected.POST("/audit/export", s.authMiddleware.RequirePermission(auth.PermAuditRead), s.exportAuditEvents)
			protected.DELETE("/audit/cleanup", s.authMiddleware.RequirePermission(auth.PermSettingsWrite), s.cleanupAuditEvents)

			// User management endpoints (require auth manager)
			if s.authManager != nil {
				// Current user endpoints (any authenticated user)
				protected.GET("/users/me", s.getCurrentUser)
				protected.PUT("/users/me", s.updateCurrentUser)
				protected.PUT("/users/me/password", s.updateCurrentUserPassword)

				// User management (admin only)
				usersGroup := protected.Group("/users")
				usersGroup.Use(s.authMiddleware.RequirePermission(auth.PermUsersRead))
				{
					usersGroup.GET("", s.listUsers)
					usersGroup.GET("/:id", s.getUser)
					usersGroup.POST("", s.authMiddleware.RequirePermission(auth.PermUsersWrite), s.createUser)
					usersGroup.PUT("/:id", s.authMiddleware.RequirePermission(auth.PermUsersWrite), s.updateUser)
					usersGroup.DELETE("/:id", s.authMiddleware.RequirePermission(auth.PermUsersDelete), s.deleteUser)

					// User deployment access
					usersGroup.GET("/:id/deployments", s.getUserDeployments)
					usersGroup.POST("/:id/deployments", s.authMiddleware.RequirePermission(auth.PermUsersWrite), s.assignUserDeployment)
					usersGroup.PUT("/:id/deployments/:name", s.authMiddleware.RequirePermission(auth.PermUsersWrite), s.updateUserDeployment)
					usersGroup.DELETE("/:id/deployments/:name", s.authMiddleware.RequirePermission(auth.PermUsersWrite), s.removeUserDeployment)
				}

				// API key management
				apiKeysGroup := protected.Group("/apikeys")
				apiKeysGroup.Use(s.authMiddleware.RequirePermission(auth.PermAPIKeysRead))
				{
					apiKeysGroup.GET("", s.listAPIKeys)
					apiKeysGroup.GET("/:id", s.getAPIKey)
					apiKeysGroup.POST("", s.authMiddleware.RequirePermission(auth.PermAPIKeysWrite), s.createAPIKey)
					apiKeysGroup.PUT("/:id", s.authMiddleware.RequirePermission(auth.PermAPIKeysWrite), s.updateAPIKey)
					apiKeysGroup.DELETE("/:id", s.authMiddleware.RequirePermission(auth.PermAPIKeysDelete), s.deleteAPIKey)
					apiKeysGroup.POST("/:id/revoke", s.authMiddleware.RequirePermission(auth.PermAPIKeysDelete), s.revokeAPIKey)
				}

				// Get users with access to a deployment
				protected.GET("/deployments/:name/users", s.authMiddleware.RequirePermission(auth.PermUsersRead), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelAdmin), s.getDeploymentUsers)
			}

			// DNS plugin routes
			dnsGroup := protected.Group("/dns")
			dnsGroup.Use(s.authMiddleware.RequirePermission(auth.PermDNSRead))
			{
				dnsGroup.GET("/providers", s.listDNSProviders)

				for _, p := range s.builtinDNS {
					if rp, ok := p.(plugins.RoutablePlugin); ok {
						_ = rp.RegisterRoutes(dnsGroup)
					}
				}

				// PowerDNS routes
				NewPowerDNSHandlers(s.powerDNSManager).RegisterRoutes(protected)
			}

			// Firewall built-in app routes (config + plan; enforcement not wired yet)
			_ = s.firewall.RegisterRoutes(protected)

			// Cluster endpoints
			clusterGroup := protected.Group("/cluster")
			clusterGroup.Use(s.authMiddleware.RequirePermission(auth.PermClusterRead))
			{
				clusterGroup.GET("/status", s.clusterStatus)
				clusterGroup.GET("/peers", s.clusterListPeers)
				clusterGroup.POST("/invite", s.authMiddleware.RequirePermission(auth.PermClusterWrite), s.clusterInvite)
				clusterGroup.POST("/accept", s.authMiddleware.RequirePermission(auth.PermClusterWrite), s.clusterAccept)
				clusterGroup.DELETE("/peers/:name", s.authMiddleware.RequirePermission(auth.PermClusterWrite), s.clusterRemovePeer)
				clusterGroup.Any("/peers/:name/proxy/*path", s.authMiddleware.RequirePermission(auth.PermClusterWrite), s.clusterProxy)
				clusterGroup.GET("/deployments", s.clusterAggregateDeployments)
				clusterGroup.GET("/stats", s.clusterAggregateStats)
			}
		}

		// Cluster exchange endpoint (no auth - uses invite token)
		api.POST("/cluster/exchange", s.clusterExchange)

		// Plugin-emitted notifications (authenticated by the per-run plugin token).
		api.POST("/internal/notify/emit", s.emitNotification)

		// Ingest endpoints (no auth - called by nginx Lua)
		api.POST("/security/events/ingest", s.ingestSecurityEvent)
		api.POST("/traffic/ingest", s.ingestTrafficLog)

		// Internal nginx endpoints - token-authenticated
		api.GET("/_internal/blocked-ips", s.listBlockedIPsInternal)
		api.GET("/_internal/whitelist", s.listWhitelistInternal)
	}
}

func (s *Server) Start() error {
	addr := fmt.Sprintf("%s:%d", s.config.API.Host, s.config.API.Port)

	s.server = &http.Server{
		Addr:    addr,
		Handler: s.router,
	}

	go func() {
		if err := s.infraManager.EnsureBaseNginxConfig(); err != nil {
			log.Printf("[infra] failed to refresh base nginx config on startup: %v", err)
		}
	}()

	go func() {
		if err := s.pluginHost.Start(); err != nil {
			log.Printf("[pluginhost] failed to start plugins: %v", err)
		}
	}()

	// The renewer runs whenever certbot is enabled; whether each certificate renews
	// is decided per certificate (with the global setting only as the default), so a
	// certificate marked for auto-renew is never vetoed by the global flag.
	if s.config.Certbot.Enabled {
		s.certRenewer = ssl.NewRenewer(
			s.proxyOrchestrator.SSLManager(),
			s.config.Certbot.RenewalThresholdDays,
			s.config.Certbot.RenewalCheckInterval,
			func(domain string) {
				if err := s.proxyOrchestrator.NginxManager().Reload(); err != nil {
					log.Printf("auto-renew: failed to reload nginx after %s: %v", domain, err)
				}
			},
		)
		s.certRenewer.Start(context.Background())
		log.Printf("auto-renew: enabled (threshold=%d days, interval=%s)", s.config.Certbot.RenewalThresholdDays, s.config.Certbot.RenewalCheckInterval)
	}

	return s.server.ListenAndServe()
}

func (s *Server) Stop() error {
	if s.pluginHost != nil {
		s.pluginHost.Stop()
	}
	if s.certRenewer != nil {
		s.certRenewer.Stop()
	}
	if s.clusterManager != nil {
		s.clusterManager.Stop()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.server.Shutdown(ctx)
}

func (s *Server) healthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "healthy",
		"agent":   "flatrun",
		"version": version.Get(),
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

	actor := auth.GetActorFromContext(c)
	if actor != nil && actor.Role != auth.RoleAdmin {
		var filtered []models.Deployment
		for _, d := range deployments {
			if actor.CanAccessDeployment(d.Name, auth.AccessLevelRead) {
				filtered = append(filtered, d)
			}
		}
		deployments = filtered
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

	composeContent, composeFilename, _ := s.manager.GetComposeFile(name)
	proxyStatus := s.proxyOrchestrator.GetDeploymentProxyStatus(deployment)

	c.JSON(http.StatusOK, gin.H{
		"deployment":       deployment,
		"compose_content":  composeContent,
		"compose_filename": composeFilename,
		"proxy_status":     proxyStatus,
	})
}

type DatabaseConfigRequest struct {
	Alias             string `json:"alias"`
	Type              string `json:"type"`
	Mode              string `json:"mode"`
	Service           string `json:"service,omitempty"`
	ExistingContainer string `json:"existing_container,omitempty"`
	ExternalHost      string `json:"external_host,omitempty"`
	ExternalPort      int    `json:"external_port,omitempty"`
	DatabaseName      string `json:"database_name,omitempty"`
	Username          string `json:"username,omitempty"`
	Password          string `json:"password,omitempty"`
	EnvPrefix         string `json:"env_prefix,omitempty"`
}

func (d *DatabaseConfigRequest) Validate() error {
	validTypes := map[string]bool{
		"mysql": true, "postgres": true, "mariadb": true,
		"mongodb": true, "redis": true,
	}
	if !validTypes[d.Type] {
		return fmt.Errorf("invalid database type: %s (must be mysql, postgres, mariadb, mongodb, or redis)", d.Type)
	}

	validModes := map[string]bool{
		"shared": true, "create": true, "existing": true, "external": true,
	}
	if !validModes[d.Mode] {
		return fmt.Errorf("invalid database mode: %s (must be shared, create, existing, or external)", d.Mode)
	}

	switch d.Mode {
	case "existing":
		if d.ExistingContainer == "" {
			return fmt.Errorf("existing_container is required for mode 'existing'")
		}
		if d.DatabaseName == "" {
			return fmt.Errorf("database_name is required for mode 'existing'")
		}
		if d.Username == "" {
			return fmt.Errorf("username is required for mode 'existing'")
		}
		if d.Password == "" {
			return fmt.Errorf("password is required for mode 'existing'")
		}
	case "external":
		if d.ExternalHost == "" {
			return fmt.Errorf("external_host is required for mode 'external'")
		}
		if d.ExternalPort <= 0 {
			return fmt.Errorf("external_port must be a positive integer for mode 'external'")
		}
	}

	return nil
}

func (s *Server) createDeployment(c *gin.Context) {
	var req struct {
		Name                      string                  `json:"name" binding:"required"`
		Image                     string                  `json:"image,omitempty"`
		ComposeContent            string                  `json:"compose_content"`
		TemplateID                string                  `json:"template_id,omitempty"`
		Metadata                  *models.ServiceMetadata `json:"metadata,omitempty"`
		EnvVars                   []EnvVar                `json:"env_vars,omitempty"`
		ContainerPort             int                     `json:"container_port,omitempty"`
		MapPorts                  bool                    `json:"map_ports,omitempty"`
		HostPort                  string                  `json:"host_port,omitempty"`
		Ports                     []PortConfig            `json:"ports,omitempty"`
		AutoStart                 bool                    `json:"auto_start"`
		UseSharedDatabase         bool                    `json:"use_shared_database"`
		ExistingDatabaseContainer string                  `json:"existing_database_container,omitempty"`
		Databases                 []DatabaseConfigRequest `json:"databases,omitempty"`
		RegistryCredential        *struct {
			CredentialID     string `json:"credential_id,omitempty"`
			Username         string `json:"username,omitempty"`
			Password         string `json:"password,omitempty"`
			SaveCredential   bool   `json:"save_credential,omitempty"`
			CredentialName   string `json:"credential_name,omitempty"`
			RegistryTypeSlug string `json:"registry_type_slug,omitempty"`
			RegistryURL      string `json:"registry_url,omitempty"`
		} `json:"registry_credential,omitempty"`
		ServiceCredentials map[string]string `json:"service_credentials,omitempty"`
		// SeedMounts names the bind mounts, by host path, to fill from the
		// image when the host side is empty. A template's own seed mounts are
		// added to these.
		SeedMounts []string `json:"seed_mounts,omitempty"`
		// Source deploys from fetched code (a git URL today) instead of inline
		// compose content: the fetched tree becomes the deployment directory and
		// its compose file is what runs.
		Source *deploymentSource `json:"source,omitempty"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	var fetched *fetchedSource
	if req.Source != nil {
		f, err := s.fetchDeploymentSource(c.Request.Context(), req.Source)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Failed to fetch source: " + err.Error(),
			})
			return
		}
		defer f.cleanup()
		fetched = f
		req.ComposeContent = f.composeContent
	}

	if req.ComposeContent == "" {
		generated, err := s.generateDeploymentCompose(req.Name, req.Image, req.TemplateID, req.ContainerPort, req.MapPorts, req.HostPort, req.Ports)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "compose_content is required when template cannot be resolved: " + err.Error(),
			})
			return
		}
		req.ComposeContent = generated
	}

	if err := s.validateComposeContent(req.ComposeContent, req.Name); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid compose content: " + err.Error(),
		})
		return
	}

	// Ensure container name is set for proxy DNS resolution
	if content, err := docker.EnsureContainerNames(req.ComposeContent, req.Name); err == nil {
		req.ComposeContent = content
	}

	// Add proxy network if expose is enabled
	proxyNetworkName := s.config.Infrastructure.DefaultProxyNetwork
	if req.Metadata != nil && req.Metadata.Networking.Expose && proxyNetworkName != "" {
		if err := s.networksManager.EnsureNetwork(proxyNetworkName); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Failed to ensure proxy network exists: " + err.Error(),
			})
			return
		}
		req.ComposeContent = s.addProxyNetwork(req.ComposeContent)
		if err := s.networksManager.EnsureContainerOnNetwork(proxyNetworkName, s.config.Nginx.ContainerName); err != nil {
			log.Printf("Warning: failed to ensure nginx on network %s: %v", proxyNetworkName, err)
		}
	}

	// Add shared database network if using shared database
	if req.UseSharedDatabase && s.config.Infrastructure.Database.Enabled {
		dbNetworkName := s.config.Infrastructure.DefaultDatabaseNetwork
		if dbNetworkName == "" {
			dbNetworkName = "database"
		}
		if err := s.networksManager.EnsureNetwork(dbNetworkName); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Failed to ensure database network exists: " + err.Error(),
			})
			return
		}
		req.ComposeContent = s.addDatabaseNetwork(req.ComposeContent)
	}

	// Add existing database container's network
	if req.ExistingDatabaseContainer != "" {
		req.ComposeContent = s.addContainerNetwork(req.ComposeContent, req.ExistingDatabaseContainer)
	}

	var createErr error
	if fetched != nil {
		createErr = s.manager.CreateDeploymentFromSource(req.Name, fetched.dir, req.ComposeContent, fetched.composeName)
	} else {
		createErr = s.manager.CreateDeployment(req.Name, req.ComposeContent, nil)
	}
	if createErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": createErr.Error(),
		})
		return
	}

	if s.authManager != nil {
		actor := auth.GetActorFromContext(c)
		if actor != nil && actor.User != nil && actor.Role != auth.RoleAdmin {
			_ = s.authManager.AssignDeployment(actor.User.ID, req.Name, auth.AccessLevelAdmin, actor.User.ID)
		}
	}

	var dbEnvVars []EnvVar
	var databaseConfigs []models.DatabaseConfig

	if len(req.Databases) > 0 {
		for i, dbReq := range req.Databases {
			if err := dbReq.Validate(); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": fmt.Sprintf("invalid database configuration at index %d: %s", i, err.Error()),
				})
				return
			}
		}

		envVars, configs, err := s.createDatabasesForDeployment(req.Name, req.Databases)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Deployment created but failed to create databases: " + err.Error(),
			})
			return
		}
		dbEnvVars = envVars
		databaseConfigs = configs
	} else if req.UseSharedDatabase && s.config.Infrastructure.Database.Enabled {
		dbResult, err := s.createDatabaseForDeployment(req.Name)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Deployment created but failed to create database: " + err.Error(),
			})
			return
		}
		dbEnvVars = dbResult
		databaseConfigs = []models.DatabaseConfig{{
			ID:       "primary",
			Alias:    "primary",
			Type:     s.config.Infrastructure.Database.Type,
			Mode:     "shared",
			IsShared: true,
		}}
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

	if req.TemplateID != "" {
		s.processTemplateFiles(req.Name, req.TemplateID, allEnvVars)
		s.processTemplateEnv(req.Name, req.TemplateID, req.ComposeContent, allEnvVars)
		s.applyTemplateMountOwnership(req.Name, req.TemplateID)

		// An object store joins the shared object-storage network; ensure it
		// exists so the container can start (it is declared external).
		if md, err := s.loadTemplateMetadata(req.TemplateID); err == nil && md.ObjectStore != nil {
			if objNet := objectStorageNetworkName(s.config); objNet != "" && s.networksManager != nil {
				if err := s.networksManager.EnsureNetwork(objNet); err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{
						"error": fmt.Sprintf("Failed to ensure object storage network %q exists: %v", objNet, err),
					})
					return
				}
			}
		}
	}

	// Seeding reads the images, so it runs before the deployment is started and
	// only ever fills a host path that is still empty. A mount that cannot be
	// seeded is reported without failing the deployment, which exists either way.
	if seedMounts := append(req.SeedMounts, s.templateSeedMounts(req.TemplateID)...); len(seedMounts) > 0 {
		if err := s.manager.SeedMounts(req.Name, seedMounts); err != nil {
			log.Printf("Warning: failed to seed mounts for %s: %v", req.Name, err)
		}
	}

	if req.Metadata != nil {
		if len(databaseConfigs) > 0 {
			req.Metadata.Databases = databaseConfigs
		}
		if err := s.manager.SaveMetadata(req.Name, req.Metadata); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Deployment created but failed to save metadata: " + err.Error(),
			})
			return
		}
	}

	var registryLoginError string
	var credentialID string
	var username, password string
	if req.RegistryCredential != nil {
		if req.RegistryCredential.CredentialID != "" {
			credentialID = req.RegistryCredential.CredentialID
			cred, err := s.credentialsManager.GetCredential(req.RegistryCredential.CredentialID)
			if err != nil {
				registryLoginError = "Failed to load credential: " + err.Error()
				log.Printf("Warning: failed to load credential %s: %v", req.RegistryCredential.CredentialID, err)
			} else {
				username = cred.Username
				password = cred.Password
			}
		} else if req.RegistryCredential.Username != "" && req.RegistryCredential.Password != "" {
			username = req.RegistryCredential.Username
			password = req.RegistryCredential.Password

			if req.RegistryCredential.SaveCredential && req.RegistryCredential.CredentialName != "" {
				registryTypeSlug := req.RegistryCredential.RegistryTypeSlug
				if registryTypeSlug == "" {
					registryTypeSlug = s.inferRegistryTypeFromCompose(req.ComposeContent)
				}
				if registryTypeSlug == "" {
					registryTypeSlug = "docker-hub"
				}
				newCred, err := s.credentialsManager.CreateCredential(
					req.RegistryCredential.CredentialName,
					registryTypeSlug,
					req.RegistryCredential.RegistryURL,
					username,
					password,
					"",
					true,
				)
				if err != nil {
					log.Printf("Warning: failed to save credential: %v", err)
				} else {
					credentialID = newCred.ID
				}
			}
		}

	}

	if credentialID != "" || len(req.ServiceCredentials) > 0 {
		if req.Metadata == nil {
			req.Metadata = &models.ServiceMetadata{Name: req.Name}
		}
		if credentialID != "" {
			req.Metadata.CredentialID = credentialID
		}
		if len(req.ServiceCredentials) > 0 {
			req.Metadata.ServiceCredentials = req.ServiceCredentials
		}
		if err := s.manager.SaveMetadata(req.Name, req.Metadata); err != nil {
			log.Printf("Warning: failed to update metadata: %v", err)
		}
	}

	var startOutput string
	var startError string
	if req.AutoStart {
		var credIDs []string
		if credentialID != "" {
			credIDs = append(credIDs, credentialID)
		}
		for _, id := range req.ServiceCredentials {
			credIDs = append(credIDs, id)
		}
		var extras []credentials.AuthEntry
		if credentialID == "" && username != "" && password != "" {
			inlineRegistry := ""
			if req.RegistryCredential != nil {
				inlineRegistry = req.RegistryCredential.RegistryURL
			}
			if inlineRegistry == "" {
				inlineRegistry = s.inferRegistryHostFromCompose(req.ComposeContent)
			}
			extras = append(extras, credentials.AuthEntry{
				Registry: inlineRegistry,
				Username: username,
				Password: password,
			})
		}
		authCfg, err := s.credentialsManager.BuildAuthConfig(credIDs, extras...)
		if err != nil {
			log.Printf("Warning: failed to build docker auth config: %v", err)
		}
		output, err := s.manager.StartDeployment(req.Name, docker.WithDockerConfig(authCfg.Dir()))
		authCfg.Close()
		startOutput = output
		if err != nil {
			startError = err.Error()
		}
	}

	var proxyResult *proxy.SetupResult
	if req.Metadata != nil {
		log.Printf("Deployment %s: metadata.networking.expose=%v, domain=%s",
			req.Name, req.Metadata.Networking.Expose, req.Metadata.Networking.Domain)
	} else {
		log.Printf("Deployment %s: no metadata provided", req.Name)
	}

	if req.Metadata != nil && req.Metadata.Networking.Expose {
		deployment, err := s.manager.GetDeployment(req.Name)
		if err != nil {
			log.Printf("Warning: failed to get deployment for proxy setup: %v", err)
		} else {
			log.Printf("Setting up proxy for deployment %s with domain %s",
				deployment.Name, deployment.Metadata.Networking.Domain)
			proxyResult, err = s.setupProxyWithRetry(deployment, 3)
			if err != nil {
				log.Printf("Warning: failed to setup proxy for deployment after retries: %v", err)
			} else {
				log.Printf("Proxy setup result for %s: success=%v, virtual_host_created=%v, cert_requested=%v",
					deployment.Name, proxyResult.Success, proxyResult.VirtualHostCreated, proxyResult.CertificateRequested)
			}
		}
	}

	c.JSON(http.StatusCreated, gin.H{
		"message":              "Deployment created",
		"name":                 req.Name,
		"proxy_result":         proxyResult,
		"auto_started":         req.AutoStart,
		"start_output":         startOutput,
		"start_error":          startError,
		"registry_login_error": registryLoginError,
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
		Type:      dbConfig.Type,
		Host:      dbConfig.Host,
		Port:      dbConfig.Port,
		Username:  dbConfig.RootUser,
		Password:  dbConfig.RootPassword,
		Container: dbConfig.Container,
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

func (s *Server) createDatabasesForDeployment(deploymentName string, databases []DatabaseConfigRequest) ([]EnvVar, []models.DatabaseConfig, error) {
	var allEnvVars []EnvVar
	var configs []models.DatabaseConfig
	isFirst := true

	for i, dbReq := range databases {
		alias := dbReq.Alias
		if alias == "" {
			if isFirst {
				alias = "primary"
			} else {
				alias = fmt.Sprintf("db%d", i+1)
			}
		}

		envPrefix := dbReq.EnvPrefix
		if envPrefix == "" {
			envPrefix = strings.ToUpper(alias)
		}

		config := models.DatabaseConfig{
			ID:        fmt.Sprintf("%s-%s", deploymentName, alias),
			Alias:     alias,
			Type:      dbReq.Type,
			Mode:      dbReq.Mode,
			Service:   dbReq.Service,
			EnvPrefix: envPrefix,
		}

		var envVars []EnvVar

		switch dbReq.Mode {
		case "shared":
			if !s.config.Infrastructure.Database.Enabled {
				continue
			}
			dbConfig := s.config.Infrastructure.Database
			dbName := strings.ReplaceAll(deploymentName, "-", "_") + "_" + alias + "_db"
			dbUser := strings.ReplaceAll(deploymentName, "-", "_") + "_" + alias + "_user"
			dbPassword := generateRandomPassword(16)

			connConfig := &database.ConnectionConfig{
				Type:      dbConfig.Type,
				Host:      dbConfig.Host,
				Port:      dbConfig.Port,
				Username:  dbConfig.RootUser,
				Password:  dbConfig.RootPassword,
				Container: dbConfig.Container,
			}

			if err := s.databaseManager.CreateDatabase(connConfig, dbName); err != nil {
				return nil, nil, fmt.Errorf("failed to create database %s: %w", alias, err)
			}

			if err := s.databaseManager.CreateUser(connConfig, dbUser, dbPassword, "%"); err != nil {
				return nil, nil, fmt.Errorf("failed to create user for %s: %w", alias, err)
			}

			if err := s.databaseManager.GrantPrivileges(connConfig, dbUser, dbName, "%"); err != nil {
				return nil, nil, fmt.Errorf("failed to grant privileges for %s: %w", alias, err)
			}

			dbHost := dbConfig.Host
			if dbHost == "" {
				dbHost = dbConfig.Container
			}

			config.Host = dbHost
			config.Port = dbConfig.Port
			config.Container = dbConfig.Container
			config.DatabaseName = dbName
			config.Username = dbUser
			config.IsShared = true

			envVars = s.generateDatabaseEnvVars(envPrefix, dbHost, dbConfig.Port, dbName, dbUser, dbPassword, dbConfig.Type, isFirst)

		case "existing":
			config.Container = dbReq.ExistingContainer
			config.Host = dbReq.ExistingContainer
			config.DatabaseName = dbReq.DatabaseName
			config.Username = dbReq.Username

			existDbPort := dbReq.ExternalPort
			if existDbPort == 0 {
				switch dbReq.Type {
				case "mysql", "mariadb":
					existDbPort = 3306
				case "postgres":
					existDbPort = 5432
				case "mongodb":
					existDbPort = 27017
				case "redis":
					existDbPort = 6379
				default:
					return nil, nil, fmt.Errorf("unknown database type %q for %q: port must be specified explicitly", dbReq.Type, alias)
				}
			}
			config.Port = existDbPort

			envVars = s.generateDatabaseEnvVars(envPrefix, dbReq.ExistingContainer, existDbPort, dbReq.DatabaseName, dbReq.Username, dbReq.Password, dbReq.Type, isFirst)

		case "external":
			config.Host = dbReq.ExternalHost
			config.Port = dbReq.ExternalPort
			if dbReq.DatabaseName != "" {
				config.DatabaseName = dbReq.DatabaseName
			}
			if dbReq.Username != "" {
				config.Username = dbReq.Username
			}
			if dbReq.Password != "" {
				envVars = s.generateDatabaseEnvVars(envPrefix, dbReq.ExternalHost, dbReq.ExternalPort, dbReq.DatabaseName, dbReq.Username, dbReq.Password, dbReq.Type, isFirst)
			}
		}

		allEnvVars = append(allEnvVars, envVars...)
		configs = append(configs, config)
		isFirst = false
	}

	return allEnvVars, configs, nil
}

func (s *Server) generateDatabaseEnvVars(prefix string, host string, port int, dbName, username, password, dbType string, includeLegacy bool) []EnvVar {
	var envVars []EnvVar

	envVars = append(envVars,
		EnvVar{Key: prefix + "_HOST", Value: host},
		EnvVar{Key: prefix + "_PORT", Value: fmt.Sprintf("%d", port)},
		EnvVar{Key: prefix + "_DATABASE", Value: dbName},
		EnvVar{Key: prefix + "_USERNAME", Value: username},
		EnvVar{Key: prefix + "_PASSWORD", Value: password},
	)

	var databaseURL string
	switch dbType {
	case "mysql", "mariadb":
		databaseURL = fmt.Sprintf("mysql://%s:%s@%s:%d/%s", username, password, host, port, dbName)
	case "postgres":
		databaseURL = fmt.Sprintf("postgres://%s:%s@%s:%d/%s", username, password, host, port, dbName)
	case "mongodb":
		databaseURL = fmt.Sprintf("mongodb://%s:%s@%s:%d/%s", username, password, host, port, dbName)
	case "redis":
		if password != "" {
			databaseURL = fmt.Sprintf("redis://:%s@%s:%d", password, host, port)
		} else {
			databaseURL = fmt.Sprintf("redis://%s:%d", host, port)
		}
	}
	if databaseURL != "" {
		envVars = append(envVars, EnvVar{Key: prefix + "_URL", Value: databaseURL})
	}

	if includeLegacy {
		envVars = append(envVars,
			EnvVar{Key: "DB_HOST", Value: host},
			EnvVar{Key: "DB_PORT", Value: fmt.Sprintf("%d", port)},
			EnvVar{Key: "DB_DATABASE", Value: dbName},
			EnvVar{Key: "DB_USERNAME", Value: username},
			EnvVar{Key: "DB_PASSWORD", Value: password},
		)
		if databaseURL != "" {
			envVars = append(envVars, EnvVar{Key: "DATABASE_URL", Value: databaseURL})
		}
	}

	return envVars
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

func (s *Server) deleteDatabaseForDeployment(deploymentName string) error {
	dbConfig := s.config.Infrastructure.Database
	dbName := strings.ReplaceAll(deploymentName, "-", "_") + "_db"
	dbUser := strings.ReplaceAll(deploymentName, "-", "_") + "_user"

	connConfig := &database.ConnectionConfig{
		Type:      dbConfig.Type,
		Host:      dbConfig.Host,
		Port:      dbConfig.Port,
		Username:  dbConfig.RootUser,
		Password:  dbConfig.RootPassword,
		Container: dbConfig.Container,
	}

	if err := s.databaseManager.RevokePrivileges(connConfig, dbUser, dbName); err != nil {
		log.Printf("Warning: failed to revoke privileges for %s: %v", dbUser, err)
	}

	if err := s.databaseManager.DropUser(connConfig, dbUser); err != nil {
		log.Printf("Warning: failed to drop user %s: %v", dbUser, err)
	}

	if err := s.databaseManager.DropDatabase(connConfig, dbName); err != nil {
		return fmt.Errorf("failed to drop database: %w", err)
	}

	return nil
}

func (s *Server) deleteDatabaseByAlias(deploymentName, alias string) error {
	dbConfig := s.config.Infrastructure.Database
	dbName := strings.ReplaceAll(deploymentName, "-", "_") + "_" + alias + "_db"
	dbUser := strings.ReplaceAll(deploymentName, "-", "_") + "_" + alias + "_user"

	connConfig := &database.ConnectionConfig{
		Type:      dbConfig.Type,
		Host:      dbConfig.Host,
		Port:      dbConfig.Port,
		Username:  dbConfig.RootUser,
		Password:  dbConfig.RootPassword,
		Container: dbConfig.Container,
	}

	if err := s.databaseManager.RevokePrivileges(connConfig, dbUser, dbName); err != nil {
		log.Printf("Warning: failed to revoke privileges for %s: %v", dbUser, err)
	}

	if err := s.databaseManager.DropUser(connConfig, dbUser); err != nil {
		log.Printf("Warning: failed to drop user %s: %v", dbUser, err)
	}

	if err := s.databaseManager.DropDatabase(connConfig, dbName); err != nil {
		return fmt.Errorf("failed to drop database %s: %w", alias, err)
	}

	return nil
}

func (s *Server) getDeploymentEnv(c *gin.Context) {
	name := c.Param("name")
	deploymentPath := filepath.Join(s.config.DeploymentsPath, name)
	envFilePath := filepath.Join(deploymentPath, ".env.flatrun")

	content, err := os.ReadFile(envFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusOK, gin.H{"env_vars": []EnvVar{}})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	envVars := parseEnvContent(string(content))
	c.JSON(http.StatusOK, gin.H{"env_vars": envVars})
}

func (s *Server) updateDeploymentEnv(c *gin.Context) {
	name := c.Param("name")
	if !s.requireUnprotectedDeploymentAction(c, name, protectedActionUpdateEnv) {
		return
	}

	var req struct {
		EnvVars []EnvVar `json:"env_vars"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if planRequested(c) {
		s.planEnvUpdate(c, name, req.EnvVars)
		return
	}
	if !s.requirePlannedAction(c, name) {
		return
	}

	if err := s.writeEnvFile(name, req.EnvVars); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Environment variables updated"})
}

func parseEnvContent(content string) []EnvVar {
	var envVars []EnvVar
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eqIndex := strings.Index(line, "=")
		if eqIndex == -1 {
			continue
		}
		key := strings.TrimSpace(line[:eqIndex])
		value := strings.TrimSpace(line[eqIndex+1:])
		if key != "" {
			envVars = append(envVars, EnvVar{Key: key, Value: value})
		}
	}
	return envVars
}

func generateRandomPassword(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	if _, err := cryptoRand.Read(b); err != nil {
		return ""
	}
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return string(b)
}

func (s *Server) updateDeployment(c *gin.Context) {
	name := c.Param("name")
	if !s.requireUnprotectedDeploymentAction(c, name, protectedActionUpdateDeployment) {
		return
	}

	var req struct {
		ComposeContent string `json:"compose_content" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	if err := s.validateComposeContent(req.ComposeContent, name); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	if planRequested(c) {
		s.planComposeUpdate(c, name, req.ComposeContent)
		return
	}
	if !s.requirePlannedAction(c, name) {
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

	bodyBytes, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Failed to read request body",
		})
		return
	}

	var sentFields map[string]json.RawMessage
	if err := json.Unmarshal(bodyBytes, &sentFields); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	var incoming models.ServiceMetadata
	if err := json.Unmarshal(bodyBytes, &incoming); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	_, sentProtectedMode := sentFields["protected_mode"]
	_, sentRequirePlan := sentFields["require_plan"]
	if sentProtectedMode || sentRequirePlan {
		if !s.requireDeploymentAccess(c, name, auth.AccessLevelAdmin) {
			return
		}
		if err := validateProtectedModeConfig(incoming.ProtectedMode); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": err.Error(),
			})
			return
		}
	} else if !s.requireUnprotectedDeploymentAction(c, name, protectedActionUpdateMetadata) {
		return
	}

	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Deployment not found",
		})
		return
	}

	metadata := mergeMetadata(deployment.Metadata, &incoming, sentFields)

	if err := s.manager.SaveMetadata(name, metadata); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	deployment.Metadata = metadata

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

func mergeMetadata(existing, incoming *models.ServiceMetadata, sentFields map[string]json.RawMessage) *models.ServiceMetadata {
	if existing == nil {
		return incoming
	}
	if incoming == nil {
		return existing
	}

	merged := *existing

	if _, ok := sentFields["name"]; ok {
		merged.Name = incoming.Name
	}
	if _, ok := sentFields["type"]; ok {
		merged.Type = incoming.Type
	}
	if _, ok := sentFields["credential_id"]; ok {
		merged.CredentialID = incoming.CredentialID
	}
	if _, ok := sentFields["primary_service"]; ok {
		merged.PrimaryService = incoming.PrimaryService
		// Keep the default-domain upstream in sync so routing follows the pin even
		// when the networking block is not part of this update.
		if incoming.PrimaryService != "" {
			merged.Networking.Service = incoming.PrimaryService
		}
	}
	if _, ok := sentFields["networking"]; ok {
		merged.Networking = incoming.Networking
	}
	if _, ok := sentFields["ssl"]; ok {
		merged.SSL = incoming.SSL
	}
	if _, ok := sentFields["healthcheck"]; ok {
		merged.HealthCheck = incoming.HealthCheck
	}
	if _, ok := sentFields["quick_actions"]; ok {
		merged.QuickActions = incoming.QuickActions
	}
	if _, ok := sentFields["security"]; ok {
		merged.Security = incoming.Security
	}
	if _, ok := sentFields["backup"]; ok {
		merged.Backup = incoming.Backup
	}
	if _, ok := sentFields["protected_mode"]; ok {
		merged.ProtectedMode = incoming.ProtectedMode
	}
	if _, ok := sentFields["require_plan"]; ok {
		merged.RequirePlan = incoming.RequirePlan
	}

	return &merged
}

func (s *Server) updateDeploymentProtectedMode(c *gin.Context) {
	name := c.Param("name")

	var req models.ProtectedModeConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}
	if err := validateProtectedModeConfig(&req); err != nil {
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
	if deployment.Metadata == nil {
		deployment.Metadata = &models.ServiceMetadata{Name: name}
	}
	deployment.Metadata.ProtectedMode = &req

	if err := s.manager.SaveMetadata(name, deployment.Metadata); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":        "Protected mode updated",
		"name":           name,
		"protected_mode": deployment.Metadata.ProtectedMode,
	})
}

func (s *Server) deleteDeployment(c *gin.Context) {
	name := c.Param("name")
	if !s.requireUnprotectedDeploymentAction(c, name, protectedActionDeleteDeployment) {
		return
	}

	opts := deploymentDeleteOptions{
		DeleteSSL:      c.DefaultQuery("delete_ssl", "true") == "true",
		DeleteDatabase: c.DefaultQuery("delete_database", "false") == "true",
		DeleteVhost:    c.DefaultQuery("delete_vhost", "true") == "true",
	}

	if planRequested(c) {
		s.planDeploymentDelete(c, name, opts)
		return
	}
	if !s.requirePlannedAction(c, name) {
		return
	}

	deletedItems, err := s.applyDeploymentDelete(name, opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":       "Deployment deleted",
		"name":          name,
		"deleted_items": deletedItems,
	})
}

func (s *Server) startDeployment(c *gin.Context) {
	s.enqueueDeploymentAction(c, "start")
}

func (s *Server) stopDeployment(c *gin.Context) {
	s.enqueueDeploymentAction(c, "stop")
}

func (s *Server) restartDeployment(c *gin.Context) {
	s.enqueueDeploymentAction(c, "restart")
}

func (s *Server) rebuildDeployment(c *gin.Context) {
	name := c.Param("name")
	if !s.requireUnprotectedDeploymentAction(c, name, protectedActionRebuild) {
		return
	}
	s.enqueueDeploymentAction(c, "rebuild")
}

// enqueueDeploymentAction starts a deployment action as a background job and
// returns its id immediately. A second action while one is in flight for the
// same deployment is rejected with the active job's id.
func (s *Server) enqueueDeploymentAction(c *gin.Context, action string) {
	name := c.Param("name")

	// The options body is optional; an empty or non-JSON body simply means no flags.
	var opts actionOptions
	_ = c.ShouldBindJSON(&opts)

	job, created := s.jobs.create(name, action, opts)
	if !created {
		c.JSON(http.StatusConflict, gin.H{
			"error":         "An action is already running for this deployment",
			"active_job_id": job.ID(),
		})
		return
	}

	go s.runActionJob(job)

	c.JSON(http.StatusAccepted, gin.H{
		"job_id":     job.ID(),
		"deployment": name,
		"action":     action,
		"status":     string(JobPending),
	})
}

func (s *Server) runActionJob(job *ActionJob) {
	job.setRunning()
	err := s.runDeploymentAction(job.action, job.deployment, job.opts, job.appendLine)
	if err != nil {
		s.jobs.finish(job, JobFailed, err.Error())
		return
	}
	s.jobs.finish(job, JobSucceeded, "")
}

// actionOptions carries the optional effective-apply flags for a deployment or service
// action, so updated environment variables and images actually take effect instead of a
// plain start/restart reusing cached config and images.
type actionOptions struct {
	ForceRecreate bool `json:"force_recreate"`
	NoCache       bool `json:"no_cache"`
	FreshPull     bool `json:"fresh_pull"`
}

func (o actionOptions) runOptions() []docker.RunOption {
	var opts []docker.RunOption
	if o.ForceRecreate {
		opts = append(opts, docker.WithForceRecreate())
	}
	if o.NoCache {
		opts = append(opts, docker.WithNoCache())
	}
	if o.FreshPull {
		opts = append(opts, docker.WithFreshPull())
	}
	return opts
}

func (s *Server) defaultRunDeploymentAction(action, name string, actOpts actionOptions, emit func(line string)) error {
	authCfg, opts := s.deploymentAuthOptions(name)
	defer authCfg.Close()

	opts = append(opts, docker.WithLineSink(emit))
	opts = append(opts, actOpts.runOptions()...)

	var err error
	switch action {
	case "start":
		_, err = s.manager.StartDeployment(name, opts...)
	case "stop":
		_, err = s.manager.StopDeployment(name, opts...)
	case "restart":
		_, err = s.manager.RestartDeployment(name, opts...)
	case "rebuild":
		_, err = s.manager.RebuildDeployment(name, opts...)
	default:
		err = fmt.Errorf("unsupported deployment action: %s", action)
	}
	return err
}

var streamableServiceActions = map[string]bool{
	"start": true, "stop": true, "restart": true, "rebuild": true, "pull": true,
}

// enqueueServiceJob runs a single service's action as a streamed background job
// so the UI can show live progress instead of only a spinner. The action is
// taken from the body so one route covers every action.
func (s *Server) enqueueServiceJob(c *gin.Context) {
	var req struct {
		Action        string `json:"action"`
		ForceRecreate bool   `json:"force_recreate"`
		NoCache       bool   `json:"no_cache"`
		FreshPull     bool   `json:"fresh_pull"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: " + err.Error()})
		return
	}
	if !streamableServiceActions[req.Action] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unsupported service action: " + req.Action})
		return
	}

	name := c.Param("name")
	service := c.Param("service")

	opts := actionOptions{ForceRecreate: req.ForceRecreate, NoCache: req.NoCache, FreshPull: req.FreshPull}
	job, created := s.jobs.createScoped(name, service, req.Action, opts)
	if !created {
		c.JSON(http.StatusConflict, gin.H{
			"error":         "An action is already running for this service",
			"active_job_id": job.ID(),
		})
		return
	}

	go s.runServiceActionJob(job)

	c.JSON(http.StatusAccepted, gin.H{
		"job_id":     job.ID(),
		"deployment": name,
		"service":    service,
		"action":     req.Action,
		"status":     string(JobPending),
	})
}

func (s *Server) runServiceActionJob(job *ActionJob) {
	job.setRunning()
	err := s.runServiceAction(job.action, job.deployment, job.service, job.opts, job.appendLine)
	if err != nil {
		s.jobs.finish(job, JobFailed, err.Error())
		return
	}
	s.jobs.finish(job, JobSucceeded, "")
}

func (s *Server) defaultRunServiceAction(action, name, service string, actOpts actionOptions, emit func(line string)) error {
	authCfg, opts := s.deploymentAuthOptions(name)
	defer authCfg.Close()

	opts = append(opts, docker.WithLineSink(emit))
	opts = append(opts, actOpts.runOptions()...)

	var err error
	switch action {
	case "start":
		_, err = s.manager.StartService(name, service, opts...)
	case "stop":
		_, err = s.manager.StopService(name, service, opts...)
	case "restart":
		_, err = s.manager.RestartService(name, service, opts...)
	case "rebuild":
		_, err = s.manager.RebuildService(name, service, opts...)
	case "pull":
		_, err = s.manager.PullService(name, service, opts...)
	default:
		err = fmt.Errorf("unsupported service action: %s", action)
	}
	return err
}

func (s *Server) getDeploymentJob(c *gin.Context) {
	name := c.Param("name")
	job := s.jobs.get(c.Param("jobId"))
	if job == nil || job.Deployment() != name {
		c.JSON(http.StatusNotFound, gin.H{"error": "Job not found"})
		return
	}
	c.JSON(http.StatusOK, job.snapshot())
}

func (s *Server) getActiveDeploymentJob(c *gin.Context) {
	job := s.jobs.activeFor(c.Param("name"))
	if job == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "No active job for this deployment"})
		return
	}
	c.JSON(http.StatusOK, job.snapshot())
}

func (s *Server) deployDeployment(c *gin.Context) {
	name := c.Param("name")

	req := struct {
		Action     string `json:"action"`
		Pull       *bool  `json:"pull"`
		OnlyLatest bool   `json:"only_latest"`
		Cleanup    *bool  `json:"cleanup"`
	}{
		Action: "restart",
	}
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Invalid request body: " + err.Error(),
			})
			return
		}
	}
	if req.Action == "" {
		req.Action = "restart"
	}

	pull := true
	if req.Pull != nil {
		pull = *req.Pull
	}

	if req.Action != "restart" && req.Action != "rebuild" && req.Action != "start" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Unsupported deploy action. Use one of: restart, rebuild, start",
		})
		return
	}

	if req.Action == "rebuild" && !s.requireUnprotectedDeploymentAction(c, name, protectedActionRebuild) {
		return
	}

	if _, err := s.manager.GetDeployment(name); err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Deployment not found: " + err.Error(),
		})
		return
	}

	dockerAuth, opts := s.deploymentAuthOptions(name)
	defer dockerAuth.Close()

	var pullOutput string
	if pull {
		output, err := s.manager.PullDeployment(name, req.OnlyLatest, opts...)
		pullOutput = output
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":       err.Error(),
				"name":        name,
				"action":      req.Action,
				"pulled":      false,
				"pull_output": pullOutput,
			})
			return
		}
	}

	var output string
	var err error
	switch req.Action {
	case "restart":
		output, err = s.manager.RestartDeployment(name, opts...)
	case "rebuild":
		output, err = s.manager.RebuildDeployment(name, opts...)
	case "start":
		output, err = s.manager.StartDeployment(name, opts...)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":         err.Error(),
			"name":          name,
			"action":        req.Action,
			"pulled":        pull,
			"pull_output":   pullOutput,
			"deploy_output": output,
		})
		return
	}

	cleanup := docker.CleanupResult{}
	cleanupEnabled := pull
	if req.Cleanup != nil {
		cleanupEnabled = *req.Cleanup
	}
	if cleanupEnabled {
		if r, err := s.manager.CleanupDeploymentImages(name, false); err == nil {
			cleanup = r
		} else {
			log.Printf("Warning: post-deploy image cleanup for %s failed: %v", name, err)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message":         "Deployment completed",
		"name":            name,
		"action":          req.Action,
		"pulled":          pull,
		"pull_output":     pullOutput,
		"deploy_output":   output,
		"cleanup_removed": cleanup.Removed,
		"cleanup_freed":   cleanup.FreedBytes,
	})
}

func (s *Server) pullDeploymentImage(c *gin.Context) {
	name := c.Param("name")

	var req struct {
		OnlyLatest bool  `json:"only_latest"`
		Cleanup    *bool `json:"cleanup"`
	}
	_ = c.ShouldBindJSON(&req)

	if _, err := s.manager.GetDeployment(name); err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Deployment not found: " + err.Error(),
		})
		return
	}

	auth, opts := s.deploymentAuthOptions(name)
	defer auth.Close()

	output, err := s.manager.PullDeployment(name, req.OnlyLatest, opts...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":  err.Error(),
			"output": output,
		})
		return
	}

	cleanup := docker.CleanupResult{}
	cleanupEnabled := true
	if req.Cleanup != nil {
		cleanupEnabled = *req.Cleanup
	}
	if cleanupEnabled {
		if r, err := s.manager.CleanupDeploymentImages(name, false); err == nil {
			cleanup = r
		} else {
			log.Printf("Warning: post-pull image cleanup for %s failed: %v", name, err)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message":         "Images pulled successfully",
		"name":            name,
		"output":          output,
		"cleanup_removed": cleanup.Removed,
		"cleanup_freed":   cleanup.FreedBytes,
	})
}

func (s *Server) getDeploymentImages(c *gin.Context) {
	name := c.Param("name")

	images, err := s.manager.GetDeploymentImages(name)
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

func (s *Server) executeQuickAction(c *gin.Context) {
	name := c.Param("name")
	actionID := c.Param("actionId")
	if !s.requireUnprotectedDeploymentAction(c, name, protectedActionQuickAction) {
		return
	}

	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Deployment not found",
		})
		return
	}
	if deployment.Metadata != nil {
		for _, action := range deployment.Metadata.QuickActions {
			if action.ID != actionID {
				continue
			}
			blocked, rule, err := protectedCommandBlocked(deployment.Metadata.ProtectedMode, action.Command)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": "Failed to check protected command rules: " + err.Error(),
				})
				return
			}
			if blocked {
				ruleName := rule.Name
				if ruleName == "" {
					ruleName = rule.ID
				}
				c.JSON(http.StatusLocked, gin.H{
					"error":     "Quick action command blocked by deployment protected mode",
					"action_id": actionID,
					"rule":      ruleName,
					"match":     rule.Match,
					"pattern":   rule.Pattern,
				})
				return
			}
			break
		}
	}

	output, err := s.manager.ExecuteQuickAction(name, actionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":  err.Error(),
			"output": output,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":   "Action executed successfully",
		"action_id": actionID,
		"output":    output,
	})
}

func (s *Server) getDeploymentLogs(c *gin.Context) {
	name := c.Param("name")

	tailStr := c.DefaultQuery("tail", "100")
	tail, err := strconv.Atoi(tailStr)
	if err != nil {
		tail = 100
	}

	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	source, ok := resolveLogSource(deployment.Metadata, c.Query("source"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown log source"})
		return
	}

	var logs string
	if source.Type == models.LogSourceFile {
		path, err := resolveLogFilePath(deployment.Path, source.Path)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		logs, err = readFileTail(path, tail)
		if err != nil && !os.IsNotExist(err) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fileLogReadError(err).Error()})
			return
		}
	} else {
		logs, err = s.manager.GetDeploymentLogs(name, tail)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	logs = filterLogLines(logs, c.Query("filter"))

	c.JSON(http.StatusOK, gin.H{
		"name":    name,
		"source":  source.ID,
		"logs":    logs,
		"records": parseLogRecords(logs),
	})
}

func parseLogRecords(logs string) []logRecord {
	if logs == "" {
		return []logRecord{}
	}
	lines := strings.Split(strings.TrimRight(logs, "\n"), "\n")
	records := make([]logRecord, 0, len(lines))
	for _, line := range lines {
		records = append(records, parseLogRecord(line))
	}
	return records
}

func (s *Server) getDeploymentLogSources(c *gin.Context) {
	name := c.Param("name")

	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	var sources []models.LogSource
	if deployment.Metadata != nil {
		sources = deployment.Metadata.EffectiveLogSources()
	} else {
		sources = []models.LogSource{{ID: models.LogSourceStdout, Name: "Container output", Type: models.LogSourceStdout}}
	}

	c.JSON(http.StatusOK, gin.H{"name": name, "sources": sources})
}

func (s *Server) updateDeploymentLogSources(c *gin.Context) {
	name := c.Param("name")

	var req struct {
		Sources []models.LogSource `json:"sources"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}

	cleaned := make([]models.LogSource, 0, len(req.Sources))
	for _, src := range req.Sources {
		if src.Type != models.LogSourceFile {
			continue
		}
		if strings.TrimSpace(src.Path) == "" || strings.TrimSpace(src.Name) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "each file log source needs a name and a path"})
			return
		}
		if _, err := resolveLogFilePath(deployment.Path, src.Path); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if src.ID == "" {
			src.ID = src.Path
		}
		src.Builtin = false
		cleaned = append(cleaned, src)
	}

	if deployment.Metadata == nil {
		deployment.Metadata = &models.ServiceMetadata{Name: name}
	}
	deployment.Metadata.LogSources = cleaned

	if err := s.manager.SaveMetadata(name, deployment.Metadata); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"name": name, "sources": deployment.Metadata.EffectiveLogSources()})
}

func (s *Server) getDeploymentCompose(c *gin.Context) {
	name := c.Param("name")

	content, filename, err := s.manager.GetComposeFile(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"name":     name,
		"content":  content,
		"filename": filename,
	})
}

func (s *Server) addDeploymentComposeMount(c *gin.Context) {
	name := c.Param("name")

	var req struct {
		SourcePath  string `json:"source_path" binding:"required"`
		TargetPath  string `json:"target_path" binding:"required"`
		ServiceName string `json:"service_name" binding:"required"`
		ReadOnly    bool   `json:"read_only"`
		SELinux     string `json:"selinux"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	sourcePath, err := normalizeComposeMountSource(req.SourcePath)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	targetPath := strings.TrimSpace(req.TargetPath)
	if !strings.HasPrefix(targetPath, "/") {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "target_path must be an absolute container path",
		})
		return
	}

	var opts []string
	if req.ReadOnly {
		opts = append(opts, "ro")
	}
	switch req.SELinux {
	case "":
	case "z", "Z":
		opts = append(opts, req.SELinux)
	default:
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "selinux must be empty, 'z' (shared), or 'Z' (private)",
		})
		return
	}

	content, filename, err := s.manager.GetComposeFile(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": err.Error(),
		})
		return
	}

	volumeMount := sourcePath + ":" + targetPath
	if len(opts) > 0 {
		volumeMount += ":" + strings.Join(opts, ",")
	}

	alreadyMounted := docker.HasVolumeMount(content, req.ServiceName, volumeMount)
	updated, err := docker.AddVolumeToService(content, req.ServiceName, volumeMount)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}
	if updated == content && !alreadyMounted {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "service not found or compose file could not be updated",
		})
		return
	}

	if err := s.validateComposeContent(updated, name); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	if err := s.manager.UpdateDeployment(name, updated); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":      "Mount added",
		"name":         name,
		"filename":     filename,
		"content":      updated,
		"service_name": req.ServiceName,
		"mount":        volumeMount,
		"added":        !alreadyMounted,
	})
}

// removeDeploymentComposeMount unmounts a bind mount and recreates the service,
// which returns it to what its image holds at that path. The host copy stays.
func (s *Server) removeDeploymentComposeMount(c *gin.Context) {
	name := c.Param("name")
	if !s.requireUnprotectedDeploymentAction(c, name, protectedActionUpdateDeployment) {
		return
	}

	var req struct {
		SourcePath  string `json:"source_path" binding:"required"`
		TargetPath  string `json:"target_path" binding:"required"`
		ServiceName string `json:"service_name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	sourcePath, err := normalizeComposeMountSource(req.SourcePath)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := s.manager.UnmountPath(name, req.ServiceName, sourcePath, strings.TrimSpace(req.TargetPath)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	content, filename, err := s.manager.GetComposeFile(name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":      "Mount removed",
		"name":         name,
		"filename":     filename,
		"content":      content,
		"service_name": req.ServiceName,
		"source_path":  sourcePath,
		"target_path":  req.TargetPath,
	})
}

func normalizeComposeMountSource(sourcePath string) (string, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return "", fmt.Errorf("source_path is required")
	}
	if strings.Contains(sourcePath, "\x00") {
		return "", fmt.Errorf("source_path is invalid")
	}

	sourcePath = strings.ReplaceAll(sourcePath, "\\", "/")
	if strings.HasPrefix(sourcePath, "/") {
		sourcePath = "." + sourcePath
	}
	if sourcePath == "." {
		return ".", nil
	}
	if !strings.HasPrefix(sourcePath, "./") {
		sourcePath = "./" + strings.TrimPrefix(sourcePath, "/")
	}

	cleaned := path.Clean(sourcePath)
	if cleaned == "." {
		return ".", nil
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("source_path must stay inside the deployment directory")
	}
	if !strings.HasPrefix(cleaned, "./") {
		cleaned = "./" + cleaned
	}
	return cleaned, nil
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

	if !s.requireContainerAccess(c, req.Container, auth.AccessLevelWrite) {
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

	if !s.requireContainerAccess(c, req.Container, auth.AccessLevelWrite) {
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
				"enabled":                s.config.Nginx.Enabled,
				"image":                  s.config.Nginx.Image,
				"container_name":         s.config.Nginx.ContainerName,
				"config_path":            s.config.Nginx.ConfigPath,
				"reload_command":         s.config.Nginx.ReloadCommand,
				"external":               s.config.Nginx.External,
				"reject_unknown_domains": s.config.Nginx.RejectUnknownDomains,
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
			"security": gin.H{
				"enabled":              s.config.Security.Enabled,
				"realtime_capture":     s.config.Security.RealtimeCapture,
				"scan_interval":        s.config.Security.ScanInterval.String(),
				"retention_days":       s.config.Security.RetentionDays,
				"rate_threshold":       s.config.Security.RateThreshold,
				"auto_block_enabled":   s.config.Security.AutoBlockEnabled,
				"auto_block_threshold": s.config.Security.AutoBlockThreshold,
				"auto_block_duration":  s.config.Security.AutoBlockDuration.String(),
			},
			"system_terminal": gin.H{
				"protected_mode": s.config.SystemTerminal.ProtectedMode,
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
			Enabled              bool   `json:"enabled"`
			Image                string `json:"image"`
			ContainerName        string `json:"container_name"`
			ConfigPath           string `json:"config_path"`
			ReloadCommand        string `json:"reload_command"`
			External             bool   `json:"external"`
			RejectUnknownDomains *bool  `json:"reject_unknown_domains"`
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
		Security *struct {
			Enabled            bool   `json:"enabled"`
			RealtimeCapture    bool   `json:"realtime_capture"`
			ScanInterval       string `json:"scan_interval"`
			RetentionDays      int    `json:"retention_days"`
			RateThreshold      int    `json:"rate_threshold"`
			AutoBlockEnabled   bool   `json:"auto_block_enabled"`
			AutoBlockThreshold int    `json:"auto_block_threshold"`
			AutoBlockDuration  string `json:"auto_block_duration"`
		} `json:"security,omitempty"`
		SystemTerminal *struct {
			ProtectedMode *models.ProtectedModeConfig `json:"protected_mode"`
		} `json:"system_terminal,omitempty"`
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
		if req.Nginx.RejectUnknownDomains != nil {
			s.config.Nginx.RejectUnknownDomains = *req.Nginx.RejectUnknownDomains
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

	if req.Security != nil {
		prevEnabled := s.config.Security.Enabled
		prevRealtimeCapture := s.config.Security.RealtimeCapture
		s.config.Security.Enabled = req.Security.Enabled
		s.config.Security.RealtimeCapture = req.Security.RealtimeCapture
		s.config.Security.AutoBlockEnabled = req.Security.AutoBlockEnabled
		if req.Security.RetentionDays > 0 {
			s.config.Security.RetentionDays = req.Security.RetentionDays
		}
		if req.Security.RateThreshold > 0 {
			s.config.Security.RateThreshold = req.Security.RateThreshold
		}
		if req.Security.AutoBlockThreshold > 0 {
			s.config.Security.AutoBlockThreshold = req.Security.AutoBlockThreshold
		}
		if req.Security.ScanInterval != "" {
			if d, err := time.ParseDuration(req.Security.ScanInterval); err == nil {
				s.config.Security.ScanInterval = d
			}
		}
		if req.Security.AutoBlockDuration != "" {
			if d, err := time.ParseDuration(req.Security.AutoBlockDuration); err == nil {
				s.config.Security.AutoBlockDuration = d
			}
		}
		if prevRealtimeCapture != s.config.Security.RealtimeCapture || prevEnabled != s.config.Security.Enabled {
			if err := s.infraManager.SetNginxRealtimeCapture(s.config.Security.RealtimeCapture && s.config.Security.Enabled); err != nil {
				log.Printf("Warning: failed to update nginx realtime capture: %v", err)
			}
		}
	}

	if req.SystemTerminal != nil && req.SystemTerminal.ProtectedMode != nil {
		if err := validateProtectedModeConfig(req.SystemTerminal.ProtectedMode); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.config.SystemTerminal.ProtectedMode = *req.SystemTerminal.ProtectedMode
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
				"enabled":                s.config.Nginx.Enabled,
				"image":                  s.config.Nginx.Image,
				"container_name":         s.config.Nginx.ContainerName,
				"config_path":            s.config.Nginx.ConfigPath,
				"reload_command":         s.config.Nginx.ReloadCommand,
				"external":               s.config.Nginx.External,
				"reject_unknown_domains": s.config.Nginx.RejectUnknownDomains,
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
			"security": gin.H{
				"enabled":              s.config.Security.Enabled,
				"realtime_capture":     s.config.Security.RealtimeCapture,
				"scan_interval":        s.config.Security.ScanInterval.String(),
				"retention_days":       s.config.Security.RetentionDays,
				"rate_threshold":       s.config.Security.RateThreshold,
				"auto_block_enabled":   s.config.Security.AutoBlockEnabled,
				"auto_block_threshold": s.config.Security.AutoBlockThreshold,
				"auto_block_duration":  s.config.Security.AutoBlockDuration.String(),
			},
			"system_terminal": gin.H{
				"protected_mode": s.config.SystemTerminal.ProtectedMode,
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

	for _, info := range s.pluginHost.Infos() {
		exts := make([]plugins.UIExtension, len(info.UIExtensions))
		for i, e := range info.UIExtensions {
			exts[i] = plugins.UIExtension{Slot: e.Slot, Kind: e.Kind, Title: e.Title, Icon: e.Icon, Endpoint: e.Endpoint}
		}
		pluginList = append(pluginList, plugins.PluginInfo{
			Name:         info.Name,
			Version:      info.Version,
			DisplayName:  info.DisplayName,
			Description:  info.Description,
			Capabilities: info.Capabilities,
			UIExtensions: exts,
			ConfigSchema: info.ConfigSchema,
			Type:         plugins.TypeIntegration,
			Enabled:      true,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"plugins": pluginList,
	})
}

func randomToken() string {
	buf := make([]byte, 24)
	if _, err := cryptoRand.Read(buf); err != nil {
		// A predictable fallback would be a known plugin-auth secret; fail loudly at startup
		// instead, since a system without a working CSPRNG cannot be secured anyway.
		panic("randomToken: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(buf)
}

func (s *Server) getNotificationTargets(c *gin.Context) {
	c.JSON(http.StatusOK, s.notify.Load())
}

func (s *Server) updateNotificationTargets(c *gin.Context) {
	var cfg notify.Config
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
		return
	}
	if err := s.notify.Update(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, s.notify.Load())
}

func (s *Server) testNotification(c *gin.Context) {
	var req struct {
		URL string `json:"url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if err := s.notify.Test(req.URL); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "delivery failed: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "test notification sent"})
}

// emitNotification is called by out-of-process plugins (authenticated by the per-run plugin
// token) to raise a notification, which the core routes to the configured targets.
func (s *Server) emitNotification(c *gin.Context) {
	if s.pluginToken == "" || c.GetHeader("X-Plugin-Token") != s.pluginToken {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req struct {
		Title   string   `json:"title"`
		Message string   `json:"message"`
		Targets []string `json:"targets"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	_ = s.notify.NotifyTargets(req.Title, req.Message, req.Targets)
	c.JSON(http.StatusAccepted, gin.H{"status": "queued"})
}

func marketplaceAPIBase() string {
	if v := os.Getenv("FLATRUN_MARKETPLACE_API"); v != "" {
		return v
	}
	return "https://api.flatrun.dev/api/v1"
}

// newTemplateSyncer builds the app-template catalog syncer from config: the
// marketplace source first (authoritative when enabled), GitHub as the fallback,
// writing into the on-disk template cache.
func newTemplateSyncer(cfg *config.Config, cacheDir string) *templatesource.Syncer {
	marketplaceURL := cfg.Templates.Marketplace.URL
	if marketplaceURL == "" {
		marketplaceURL = marketplaceAPIBase()
	}
	githubEnabled := cfg.Templates.GitHub.Enabled == nil || *cfg.Templates.GitHub.Enabled

	resolver := templatesource.NewResolver(
		templatesource.MarketplaceSource{
			BaseURL: marketplaceURL,
			Enabled: cfg.Templates.Marketplace.Enabled,
		},
		templatesource.GitHubSource{
			Repo:    cfg.Templates.GitHub.Repo,
			Ref:     cfg.Templates.GitHub.Ref,
			Enabled: githubEnabled,
		},
	)
	return &templatesource.Syncer{Resolver: resolver, CacheDir: cacheDir}
}

// startTemplateSyncLoop pulls the app catalog once in the background, then, when
// interval > 0, keeps refreshing it. It runs off the request path so a slow or
// unreachable source never delays startup; the on-disk cache from the last run
// keeps serving deploys until the fetch completes.
func (s *Server) startTemplateSyncLoop(ctx context.Context, interval time.Duration) {
	sync := func() {
		if src, n, err := s.templateSyncer.Sync(ctx); err != nil {
			log.Printf("Warning: template sync failed: %v", err)
		} else if n > 0 {
			log.Printf("Synced %d app templates from %s", n, src)
		}
	}
	go func() {
		sync()
		if interval <= 0 {
			return
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sync()
			}
		}
	}()
}

func (s *Server) proxyMarketplace(c *gin.Context) {
	upstream, err := url.Parse(marketplaceAPIBase())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid marketplace upstream"})
		return
	}
	rel := c.Param("path")
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = upstream.Scheme
			req.URL.Host = upstream.Host
			req.URL.Path = strings.TrimRight(upstream.Path, "/") + rel
			req.Host = upstream.Host
			req.Header.Del("Authorization")
			req.Header.Del("Cookie")
			req.Header.Del("X-Api-Key")
		},
		// Drop the upstream's CORS headers; the agent sets its own, and two
		// Access-Control-Allow-Origin headers make the browser reject the response.
		ModifyResponse: func(resp *http.Response) error {
			for _, h := range []string{
				"Access-Control-Allow-Origin",
				"Access-Control-Allow-Credentials",
				"Access-Control-Allow-Methods",
				"Access-Control-Allow-Headers",
				"Access-Control-Expose-Headers",
				"Access-Control-Max-Age",
			} {
				resp.Header.Del(h)
			}
			return nil
		},
	}
	proxy.ServeHTTP(c.Writer, c.Request)
}

func (s *Server) proxyToPlugin(c *gin.Context) {
	name := c.Param("name")
	proxy, ok := s.pluginHost.Proxy(name)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin not running"})
		return
	}
	c.Request.URL.Path = c.Param("proxyPath")
	if c.Request.URL.Path == "" {
		c.Request.URL.Path = "/"
	}
	proxy.ServeHTTP(c.Writer, c.Request)
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

	if s.authManager != nil {
		actor := auth.GetActorFromContext(c)
		if actor != nil && actor.User != nil && actor.Role != auth.RoleAdmin {
			_ = s.authManager.AssignDeployment(actor.User.ID, req.Name, auth.AccessLevelAdmin, actor.User.ID)
		}
	}

	c.JSON(http.StatusCreated, gin.H{
		"message":    "Deployment created",
		"deployment": result,
	})
}

type TemplateFile struct {
	Path    string `json:"path" yaml:"path"`
	Content string `json:"content" yaml:"content"`
}

type TemplateMetadata struct {
	Name          string               `yaml:"name"`
	Description   string               `yaml:"description"`
	Icon          string               `yaml:"icon"`
	Logo          string               `yaml:"logo"`
	Category      string               `yaml:"category"`
	Type          string               `yaml:"type"`
	ObjectStore   *TemplateObjectStore `yaml:"object_store"`
	Priority      int                  `yaml:"priority"`
	ContainerPort int                  `yaml:"container_port"`
	Mounts        []TemplateMount      `yaml:"mounts"`
	Files         []TemplateFile       `yaml:"files"`
	Env           TemplateEnv          `yaml:"env"`
}

// TemplateObjectStore is a template's declaration that it runs an S3-compatible
// object store, and how to bootstrap it: which generated env vars carry the
// root credentials and which port serves the S3 API. Declaring this keeps the
// agent free of any per-image (MinIO, Garage, ...) knowledge.
type TemplateObjectStore struct {
	AccessKeyEnv string `json:"access_key_env" yaml:"access_key_env"`
	SecretKeyEnv string `json:"secret_key_env" yaml:"secret_key_env"`
	APIPort      int    `json:"api_port" yaml:"api_port"`
	Region       string `json:"region,omitempty" yaml:"region,omitempty"`
	UsePathStyle bool   `json:"use_path_style" yaml:"use_path_style"`
}

// TemplateEnv describes how a platform's environment file is produced. The
// template defines everything: which file to write (file), where the example
// shipped inside the deployed image lives (example_path, preferred base when
// readable), the fallback content (template), and which secrets to generate
// fresh per deployment. Nothing is assumed about the platform.
type TemplateEnv struct {
	File        string              `json:"file,omitempty" yaml:"file,omitempty"`
	ExamplePath string              `json:"example_path,omitempty" yaml:"example_path,omitempty"`
	Template    string              `json:"template,omitempty" yaml:"template,omitempty"`
	Secrets     []TemplateEnvSecret `json:"secrets,omitempty" yaml:"secrets,omitempty"`
}

type TemplateEnvSecret struct {
	Key      string `json:"key" yaml:"key"`
	Encoding string `json:"encoding,omitempty" yaml:"encoding,omitempty"` // base64 (default), hex, alphanumeric
	Bytes    int    `json:"bytes,omitempty" yaml:"bytes,omitempty"`
	Prefix   string `json:"prefix,omitempty" yaml:"prefix,omitempty"`
}

type TemplateMount struct {
	ID             string   `json:"id" yaml:"id"`
	Name           string   `json:"name" yaml:"name"`
	HostPath       string   `json:"host_path,omitempty" yaml:"host_path,omitempty"`
	ContainerPath  string   `json:"container_path" yaml:"container_path"`
	Description    string   `json:"description" yaml:"description"`
	Type           string   `json:"type" yaml:"type"`
	Required       bool     `json:"required" yaml:"required"`
	User           string   `json:"user,omitempty" yaml:"user,omitempty"`
	Subdirectories []string `json:"subdirectories,omitempty" yaml:"subdirectories,omitempty"`
	// Seed fills the mount from the image's own content when the host side is
	// empty, for images that do not populate their mounts themselves.
	Seed bool `json:"seed,omitempty" yaml:"seed,omitempty"`
}

// templateMountHostPath resolves where a template mount lives in the
// deployment directory: an explicit host_path from the metadata, or ./<id>.
func templateMountHostPath(m TemplateMount) string {
	if m.HostPath != "" {
		return m.HostPath
	}
	return "./" + m.ID
}

type Template struct {
	ID            string               `json:"id"`
	Name          string               `json:"name" yaml:"name"`
	Description   string               `json:"description" yaml:"description"`
	Icon          string               `json:"icon" yaml:"icon"`
	Logo          string               `json:"logo" yaml:"logo"`
	Category      string               `json:"category" yaml:"category"`
	ObjectStore   *TemplateObjectStore `json:"object_store,omitempty" yaml:"object_store,omitempty"`
	Priority      int                  `json:"priority" yaml:"priority"`
	ContainerPort int                  `json:"container_port" yaml:"container_port"`
	Mounts        []TemplateMount      `json:"mounts" yaml:"mounts"`
	Files         []TemplateFile       `json:"files" yaml:"files"`
	Content       string               `json:"content"`
}

func (s *Server) listTemplates(c *gin.Context) {
	templatesDir := filepath.Join(s.config.DeploymentsPath, ".flatrun", "templates")

	if err := os.MkdirAll(templatesDir, 0755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to create templates directory",
		})
		return
	}

	ensureBuiltinTemplates(templatesDir)

	typeFilter := c.DefaultQuery("type", "")

	var templateList []Template

	var scanDir func(dir, prefix string)
	scanDir = func(dir, prefix string) {
		dirEntries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, entry := range dirEntries {
			if !entry.IsDir() {
				continue
			}

			templateID := entry.Name()
			if prefix != "" {
				templateID = path.Join(prefix, entry.Name())
			}
			templatePath := filepath.Join(dir, entry.Name())
			composePath := filepath.Join(templatePath, "docker-compose.yml")

			composeContent, err := os.ReadFile(composePath)
			if err != nil {
				scanDir(templatePath, templateID)
				continue
			}

			var metadata TemplateMetadata
			metadataPath := filepath.Join(templatePath, "metadata.yml")
			metadataContent, err := os.ReadFile(metadataPath)
			if err == nil {
				if uerr := yaml.Unmarshal(metadataContent, &metadata); uerr != nil {
					log.Printf("Warning: failed to parse template metadata %s: %v", metadataPath, uerr)
				}
			}

			if metadata.Name == "" {
				metadata.Name = toTitleCase(strings.ReplaceAll(entry.Name(), "-", " "))
			}
			if metadata.Icon == "" {
				metadata.Icon = "pi pi-box"
			}
			if metadata.Category == "" {
				metadata.Category = "general"
			}

			isInfra := metadata.Category == "infrastructure" || metadata.Type == "infrastructure"
			switch typeFilter {
			case "infrastructure":
				if !isInfra {
					continue
				}
			case "all":
				// include everything
			default:
				if isInfra {
					continue
				}
			}

			templateList = append(templateList, Template{
				ID:            templateID,
				Name:          metadata.Name,
				Description:   metadata.Description,
				Icon:          metadata.Icon,
				Logo:          metadata.Logo,
				Category:      metadata.Category,
				ObjectStore:   metadata.ObjectStore,
				Priority:      metadata.Priority,
				ContainerPort: metadata.ContainerPort,
				Mounts:        metadata.Mounts,
				Files:         metadata.Files,
				Content:       string(composeContent),
			})
		}
	}
	scanDir(templatesDir, "")

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

	src, synced, err := s.templateSyncer.Sync(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error": fmt.Sprintf("template sync failed: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Templates refreshed",
		"source":  src,
		"count":   synced,
	})
}

func (s *Server) getTemplateCategories(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"categories": templates.GetCategories(),
	})
}

func (s *Server) resolveTemplateID(c *gin.Context) (string, bool) {
	var id string
	if prefix, ok := c.Get("_template_prefix"); ok {
		id = path.Join(prefix.(string), c.Param("name"))
	} else {
		id = c.Param("id")
	}
	cleaned := path.Clean(id)
	if strings.Contains(cleaned, "..") || strings.HasPrefix(cleaned, "/") {
		return "", false
	}
	return cleaned, true
}

func (s *Server) getInfraTemplateCompose(c *gin.Context) {
	c.Set("_template_prefix", "infra")
	s.getTemplateCompose(c)
}

func (s *Server) generateInfraTemplateCompose(c *gin.Context) {
	c.Set("_template_prefix", "infra")
	s.generateTemplateCompose(c)
}

func (s *Server) getTemplateCompose(c *gin.Context) {
	tid, ok := s.resolveTemplateID(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid template id"})
		return
	}
	name := c.DefaultQuery("name", "my-app")

	content, err := s.generateComposeContent(name, tid)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"template_id": tid,
		"name":        name,
		"content":     content,
	})
}

type MountSelection struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	Type    string `json:"type"`
}

type ComposeGenerateRequest struct {
	Name          string           `json:"name" binding:"required"`
	Image         string           `json:"image,omitempty"`
	ContainerPort int              `json:"container_port"`
	MapPorts      bool             `json:"map_ports"`
	HostPort      string           `json:"host_port"`
	Mounts        []MountSelection `json:"mounts"`
}

type PortConfig struct {
	ContainerPort int    `json:"container_port"`
	Container     int    `json:"container,omitempty"`
	HostPort      string `json:"host_port"`
	Host          string `json:"host,omitempty"`
}

type ComposeUpdateRequest struct {
	Content  string           `json:"content" binding:"required"`
	Ports    []PortConfig     `json:"ports"`
	Mounts   []MountSelection `json:"mounts"`
	Database *DatabaseConfig  `json:"database,omitempty"`
}

type DatabaseConfig struct {
	Type              string `json:"type"`
	Mode              string `json:"mode"`
	Name              string `json:"name"`
	User              string `json:"user"`
	Password          string `json:"password"`
	RootPassword      string `json:"root_password"`
	ExistingContainer string `json:"existing_container"`
	ExternalHost      string `json:"external_host"`
	ExternalPort      string `json:"external_port"`
}

func (s *Server) generateTemplateCompose(c *gin.Context) {
	tid, ok := s.resolveTemplateID(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid template id"})
		return
	}

	var req ComposeGenerateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	content, err := s.generateComposeWithOptions(tid, &req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"template_id":    tid,
		"name":           req.Name,
		"content":        content,
		"container_port": req.ContainerPort,
		"map_ports":      req.MapPorts,
		"host_port":      req.HostPort,
	})
}

func (s *Server) updateCompose(c *gin.Context) {
	var req ComposeUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	content := req.Content

	if len(req.Ports) == 0 {
		req.Ports = []PortConfig{{ContainerPort: 80, HostPort: ""}}
	}

	content = s.updateComposePorts(content, req.Ports)

	if req.Database != nil && req.Database.Type != "" && req.Database.Type != "none" {
		content = s.updateComposeDatabase(content, req.Database)
	}

	c.JSON(http.StatusOK, gin.H{
		"content": content,
	})
}

func (s *Server) updateComposePorts(content string, ports []PortConfig) string {
	var compose map[string]interface{}
	if err := yaml.Unmarshal([]byte(content), &compose); err != nil {
		return content
	}

	services, ok := compose["services"].(map[string]interface{})
	if !ok {
		return content
	}

	for _, svc := range services {
		service, ok := svc.(map[string]interface{})
		if !ok {
			continue
		}

		delete(service, "expose")
		delete(service, "ports")

		var exposeList []string
		var portsList []string

		for _, p := range ports {
			if p.HostPort != "" {
				portsList = append(portsList, fmt.Sprintf("%s:%d", p.HostPort, p.ContainerPort))
			} else {
				exposeList = append(exposeList, fmt.Sprintf("%d", p.ContainerPort))
			}
		}

		if len(portsList) > 0 {
			service["ports"] = portsList
		}
		if len(exposeList) > 0 {
			service["expose"] = exposeList
		}
		break
	}

	out, err := yaml.Marshal(compose)
	if err != nil {
		return content
	}

	return string(out)
}

func (s *Server) updateComposeDatabase(content string, db *DatabaseConfig) string {
	var compose map[string]interface{}
	if err := yaml.Unmarshal([]byte(content), &compose); err != nil {
		return content
	}

	services, ok := compose["services"].(map[string]interface{})
	if !ok {
		return content
	}

	if db.Mode == "create" {
		dbService := s.createDatabaseService(db)
		if dbService != nil {
			services["db"] = dbService

			volumes, _ := compose["volumes"].(map[string]interface{})
			if volumes == nil {
				volumes = make(map[string]interface{})
			}
			volumes["db_data"] = nil
			compose["volumes"] = volumes
		}
	}

	result, err := yaml.Marshal(compose)
	if err != nil {
		return content
	}
	return string(result)
}

func (s *Server) createDatabaseService(db *DatabaseConfig) map[string]interface{} {
	var image string
	var envVars map[string]string
	var volumePath string

	dbName := db.Name
	if dbName == "" {
		dbName = "app_db"
	}
	dbUser := db.User
	if dbUser == "" {
		dbUser = "app"
	}
	rootPassword := db.RootPassword
	if rootPassword == "" {
		rootPassword = db.Password
	}

	switch db.Type {
	case "mysql":
		image = "mysql:8"
		volumePath = "/var/lib/mysql"
		envVars = map[string]string{
			"MYSQL_ROOT_PASSWORD": rootPassword,
			"MYSQL_DATABASE":      dbName,
			"MYSQL_USER":          dbUser,
			"MYSQL_PASSWORD":      db.Password,
		}
	case "mariadb":
		image = "mariadb:10"
		volumePath = "/var/lib/mysql"
		envVars = map[string]string{
			"MYSQL_ROOT_PASSWORD": rootPassword,
			"MYSQL_DATABASE":      dbName,
			"MYSQL_USER":          dbUser,
			"MYSQL_PASSWORD":      db.Password,
		}
	case "postgres":
		image = "postgres:15"
		volumePath = "/var/lib/postgresql/data"
		envVars = map[string]string{
			"POSTGRES_DB":       dbName,
			"POSTGRES_USER":     dbUser,
			"POSTGRES_PASSWORD": db.Password,
		}
	case "mongodb":
		image = "mongo:6"
		volumePath = "/data/db"
		envVars = map[string]string{
			"MONGO_INITDB_ROOT_USERNAME": dbUser,
			"MONGO_INITDB_ROOT_PASSWORD": db.Password,
			"MONGO_INITDB_DATABASE":      dbName,
		}
	default:
		return nil
	}

	networkName := s.config.Infrastructure.DefaultDatabaseNetwork
	if networkName == "" {
		networkName = "database"
	}

	return map[string]interface{}{
		"image":       image,
		"environment": envVars,
		"volumes":     []string{"db_data:" + volumePath},
		"networks":    []string{networkName},
		"restart":     "unless-stopped",
	}
}

func (s *Server) generateComposeWithOptions(templateID string, opts *ComposeGenerateRequest) (string, error) {
	if templateID == "" || templateID == "custom" {
		return s.generateCustomCompose(opts)
	}

	templatesDir := filepath.Join(s.config.DeploymentsPath, ".flatrun", "templates")

	var metadata TemplateMetadata
	metadataPath := filepath.Join(templatesDir, templateID, "metadata.yml")
	metadataContent, err := os.ReadFile(metadataPath)
	if err == nil {
		_ = yaml.Unmarshal(metadataContent, &metadata)
	}

	if opts.Image != "" {
		return s.generateImageComposeWithTemplate(opts, &metadata)
	}

	composeBytes, err := templates.GetCompose(templateID)
	if err != nil {
		composePath := filepath.Join(templatesDir, templateID, "docker-compose.yml")
		composeBytes, err = os.ReadFile(composePath)
		if err != nil {
			return s.generateCustomCompose(opts)
		}
	}

	content := string(composeBytes)

	content = strings.ReplaceAll(content, "${NAME}", opts.Name)

	networkName := s.config.Infrastructure.DefaultProxyNetwork
	content = strings.ReplaceAll(content, "${PROXY_NETWORK}", networkName)
	content = replaceHardcodedNetwork(content, "proxy", networkName)

	if opts.MapPorts && opts.HostPort != "" {
		containerPort := opts.ContainerPort
		if containerPort == 0 && metadata.ContainerPort > 0 {
			containerPort = metadata.ContainerPort
		}
		if containerPort == 0 {
			containerPort = 80
		}

		exposePattern := fmt.Sprintf(`expose:\s*\n\s*-\s*["']?%d["']?`, containerPort)
		re := regexp.MustCompile(exposePattern)
		portMapping := fmt.Sprintf("ports:\n      - \"%s:%d\"", opts.HostPort, containerPort)
		content = re.ReplaceAllString(content, portMapping)
	}

	if len(opts.Mounts) > 0 && len(metadata.Mounts) > 0 {
		content = s.injectMounts(content, opts.Mounts, metadata.Mounts)
	}

	return content, nil
}

func (s *Server) generateDeploymentCompose(name, image, templateID string, containerPort int, mapPorts bool, hostPort string, ports []PortConfig) (string, error) {
	useCustomOptions := image != "" || containerPort != 0 || mapPorts || hostPort != "" || len(ports) > 0
	if !useCustomOptions {
		return s.generateComposeContent(name, templateID)
	}

	composeReq := ComposeGenerateRequest{
		Name:          name,
		Image:         image,
		ContainerPort: containerPort,
		MapPorts:      mapPorts,
		HostPort:      hostPort,
	}
	if len(ports) > 0 {
		composeReq.ContainerPort = ports[0].ContainerPort
		if composeReq.ContainerPort == 0 {
			composeReq.ContainerPort = ports[0].Container
		}
		hostPort := ports[0].HostPort
		if hostPort == "" {
			hostPort = ports[0].Host
		}
		if hostPort != "" {
			composeReq.MapPorts = true
			composeReq.HostPort = hostPort
		}
	}

	return s.generateComposeWithOptions(templateID, &composeReq)
}

func (s *Server) injectMounts(content string, selections []MountSelection, available []TemplateMount) string {
	mountMap := make(map[string]TemplateMount)
	for _, m := range available {
		mountMap[m.ID] = m
	}

	var newVolumes []string
	for _, sel := range selections {
		if !sel.Enabled {
			continue
		}
		mount, ok := mountMap[sel.ID]
		if !ok {
			continue
		}

		var hostPath string
		if sel.Type == "volume" {
			hostPath = sel.ID + "_data"
		} else {
			hostPath = templateMountHostPath(mount)
		}
		newVolumes = append(newVolumes, fmt.Sprintf("%s:%s", hostPath, mount.ContainerPath))
	}

	if len(newVolumes) == 0 {
		return content
	}

	var compose map[string]interface{}
	if err := yaml.Unmarshal([]byte(content), &compose); err != nil {
		return content
	}

	services, ok := compose["services"].(map[string]interface{})
	if !ok {
		return content
	}

	for serviceName, serviceData := range services {
		service, ok := serviceData.(map[string]interface{})
		if !ok {
			continue
		}

		existingByContainerPath := make(map[string]int)
		var volumesList []string

		if v, ok := service["volumes"].([]interface{}); ok {
			for _, vol := range v {
				if vs, ok := vol.(string); ok {
					containerPath := extractContainerPath(vs)
					existingByContainerPath[containerPath] = len(volumesList)
					volumesList = append(volumesList, vs)
				}
			}
		}

		for _, vol := range newVolumes {
			containerPath := extractContainerPath(vol)
			if idx, exists := existingByContainerPath[containerPath]; exists {
				if hasVolumeOptions(vol) && !hasVolumeOptions(volumesList[idx]) {
					volumesList[idx] = vol
				}
			} else {
				existingByContainerPath[containerPath] = len(volumesList)
				volumesList = append(volumesList, vol)
			}
		}

		service["volumes"] = volumesList
		services[serviceName] = service
		break
	}

	compose["services"] = services

	result, err := yaml.Marshal(compose)
	if err != nil {
		return content
	}

	return string(result)
}

func extractContainerPath(volume string) string {
	parts := strings.Split(volume, ":")
	if len(parts) >= 2 {
		return parts[1]
	}
	return volume
}

func hasVolumeOptions(volume string) bool {
	return len(strings.Split(volume, ":")) >= 3
}

// generateImageComposeWithTemplate keeps the user-provided image as the
// deployed service but applies the template's defaults (container port and
// bind mounts) on top of the generated compose. Explicit selections override
// the defaults entirely; without them the template's required mounts apply.
func (s *Server) generateImageComposeWithTemplate(opts *ComposeGenerateRequest, metadata *TemplateMetadata) (string, error) {
	if opts.ContainerPort == 0 && metadata.ContainerPort > 0 {
		opts.ContainerPort = metadata.ContainerPort
	}

	content, err := s.generateCustomCompose(opts)
	if err != nil {
		return "", err
	}

	selections := opts.Mounts
	if len(selections) == 0 {
		for _, m := range metadata.Mounts {
			if m.Required {
				selections = append(selections, MountSelection{ID: m.ID, Enabled: true, Type: m.Type})
			}
		}
	}
	if len(selections) > 0 && len(metadata.Mounts) > 0 {
		content = s.injectMounts(content, selections, metadata.Mounts)
	}

	return content, nil
}

func (s *Server) generateCustomCompose(opts *ComposeGenerateRequest) (string, error) {
	networkName := s.config.Infrastructure.DefaultProxyNetwork
	image := strings.TrimSpace(opts.Image)
	if image == "" {
		image = "nginx:alpine"
	}
	if err := validateImageName(image); err != nil {
		return "", err
	}

	containerPort := opts.ContainerPort
	if containerPort == 0 {
		containerPort = 80
	}

	portConfig := fmt.Sprintf("expose:\n      - \"%d\"", containerPort)
	if opts.MapPorts && opts.HostPort != "" {
		portConfig = fmt.Sprintf("ports:\n      - \"%s:%d\"", opts.HostPort, containerPort)
	}

	content := fmt.Sprintf(`name: %s
services:
  app:
    image: %s
    container_name: %s
    %s
    networks:
      - %s
    restart: unless-stopped

networks:
  %s:
    external: true
`, opts.Name, image, opts.Name, portConfig, networkName, networkName)

	return content, nil
}

func validateImageName(image string) error {
	if image == "" {
		return fmt.Errorf("image is required")
	}
	if len(image) > 255 {
		return fmt.Errorf("image name is too long")
	}
	validImage := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]*$`)
	if !validImage.MatchString(image) {
		return fmt.Errorf("invalid image name %q", image)
	}
	return nil
}

type composeFile struct {
	Name     string                    `yaml:"name"`
	Services map[string]composeService `yaml:"services"`
	Networks map[string]composeNetwork `yaml:"networks"`
	Volumes  map[string]interface{}    `yaml:"volumes"`
}

type composeService struct {
	Image         string      `yaml:"image"`
	ContainerName string      `yaml:"container_name"`
	Ports         interface{} `yaml:"ports"`
	Expose        interface{} `yaml:"expose"`
	Networks      interface{} `yaml:"networks"`
	Volumes       interface{} `yaml:"volumes"`
	Environment   interface{} `yaml:"environment"`
	EnvFile       interface{} `yaml:"env_file"`
}

type composeNetwork struct {
	External bool   `yaml:"external"`
	Name     string `yaml:"name"`
}

func (s *Server) inferRegistryTypeFromCompose(content string) string {
	var compose composeFile
	if err := yaml.Unmarshal([]byte(content), &compose); err != nil {
		return ""
	}
	for _, svc := range compose.Services {
		if svc.Image == "" {
			continue
		}
		if slug := s.credentialsManager.RegistryTypeForImage(svc.Image); slug != "" {
			return slug
		}
	}
	return ""
}

func (s *Server) inferRegistryHostFromCompose(content string) string {
	var compose composeFile
	if err := yaml.Unmarshal([]byte(content), &compose); err != nil {
		return ""
	}
	for _, svc := range compose.Services {
		if svc.Image == "" {
			continue
		}
		parts := strings.SplitN(svc.Image, "/", 2)
		if len(parts) != 2 {
			continue
		}
		host := parts[0]
		if strings.Contains(host, ".") || strings.Contains(host, ":") || host == "localhost" {
			return host
		}
	}
	return ""
}

func (s *Server) validateComposeContent(content, name string) error {
	var compose composeFile
	if err := yaml.Unmarshal([]byte(content), &compose); err != nil {
		return fmt.Errorf("invalid YAML syntax: %w", err)
	}

	if len(compose.Services) == 0 {
		return fmt.Errorf("compose file must define at least one service")
	}

	expectedNetwork := s.config.Infrastructure.DefaultProxyNetwork

	for serviceName, service := range compose.Services {
		if service.Image == "" {
			return fmt.Errorf("service '%s' must have an image defined", serviceName)
		}

		hasExpectedNetwork := false
		networkNames := extractNetworkNames(service.Networks)
		for _, net := range networkNames {
			if net == expectedNetwork {
				hasExpectedNetwork = true
				break
			}
		}
		if !hasExpectedNetwork && len(networkNames) > 0 {
			log.Printf("Warning: service '%s' does not use the configured proxy network '%s'", serviceName, expectedNetwork)
		}
	}

	if len(compose.Networks) > 0 {
		if netConfig, ok := compose.Networks[expectedNetwork]; ok {
			if !netConfig.External {
				return fmt.Errorf("network '%s' must be marked as external", expectedNetwork)
			}
		}
	}

	if err := validateComposeWithComposeGo(content, s.composeValidationDir(name)); err != nil {
		return err
	}

	return nil
}

// composeValidationDir returns the directory that relative compose paths (such as
// a relative env_file) should resolve against during validation. For an existing
// deployment that is the deployment directory; when it does not yet exist (the
// create path) it falls back to the agent's working directory so create keeps working.
func (s *Server) composeValidationDir(name string) string {
	if name == "" || s.manager == nil {
		return "."
	}
	dir := filepath.Join(s.manager.BasePath(), name)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "."
	}
	return dir
}

func validateComposeWithComposeGo(content, workingDir string) error {
	configDetails := composetypes.ConfigDetails{
		ConfigFiles: []composetypes.ConfigFile{{
			Filename: "compose.yml",
			Content:  []byte(content),
		}},
		WorkingDir:  workingDir,
		Environment: map[string]string{},
	}
	_, err := loader.LoadWithContext(context.Background(), configDetails, func(o *loader.Options) {
		// Resolve relative paths (notably a relative env_file) against WorkingDir so an
		// existing deployment's ./.env is found in the deployment directory rather than
		// being read relative to the agent's own working directory.
		o.ResolvePaths = true
		o.SkipConsistencyCheck = true
	})
	if err != nil {
		return fmt.Errorf("invalid compose: %w", err)
	}
	return nil
}

func extractNetworkNames(networks interface{}) []string {
	if networks == nil {
		return nil
	}

	switch v := networks.(type) {
	case []interface{}:
		var names []string
		for _, n := range v {
			if name, ok := n.(string); ok {
				names = append(names, name)
			}
		}
		return names
	case []string:
		return v
	case map[string]interface{}:
		var names []string
		for name := range v {
			names = append(names, name)
		}
		return names
	default:
		return nil
	}
}

func ensureBuiltinTemplates(templatesDir string) {
	builtinList, err := templates.List()
	if err != nil {
		return
	}

	// On-disk copies are what deploys actually read; overwrite them
	// whenever the embedded set changed (typically an agent upgrade),
	// otherwise stale metadata keeps shaping new deployments.
	checksumPath := filepath.Join(templatesDir, ".builtin-checksum")
	checksum := templates.Checksum()
	stale := true
	if current, err := os.ReadFile(checksumPath); err == nil && strings.TrimSpace(string(current)) == checksum {
		stale = false
	}

	for _, tmplID := range builtinList {
		templatePath := filepath.Join(templatesDir, tmplID)
		composePath := filepath.Join(templatePath, "docker-compose.yml")

		if !stale {
			if _, err := os.Stat(composePath); err == nil {
				continue
			}
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

	if stale && checksum != "" {
		_ = os.WriteFile(checksumPath, []byte(checksum), 0644)
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

// getContainerNetworks returns the networks a container is connected to
func (s *Server) getContainerNetworks(containerName string) ([]string, error) {
	cmd := exec.Command("docker", "inspect", "--format", "{{json .NetworkSettings.Networks}}", containerName)
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var networks map[string]interface{}
	if err := json.Unmarshal(output, &networks); err != nil {
		return nil, err
	}

	var result []string
	for name := range networks {
		result = append(result, name)
	}
	return result, nil
}

// addDatabaseNetwork adds the configured shared database network to a compose file
func (s *Server) addDatabaseNetwork(content string) string {
	dbNetworkName := s.config.Infrastructure.DefaultDatabaseNetwork
	if dbNetworkName == "" {
		dbNetworkName = "database"
	}
	result, err := docker.AddNetworkToCompose(content, dbNetworkName)
	if err != nil {
		return content
	}
	return result
}

// addProxyNetwork adds the configured proxy network to a compose file
func (s *Server) addProxyNetwork(content string) string {
	result, err := docker.AddNetworkToCompose(content, s.config.Infrastructure.DefaultProxyNetwork)
	if err != nil {
		return content
	}
	return result
}

// addContainerNetwork finds and adds the network of a specific container to a compose file
func (s *Server) addContainerNetwork(content string, containerName string) string {
	networks, err := s.getContainerNetworks(containerName)
	if err != nil || len(networks) == 0 {
		return content
	}
	// Add the first non-bridge network, or first network if all are bridge
	for _, net := range networks {
		if net != "bridge" && net != "host" && net != "none" {
			result, err := docker.AddNetworkToCompose(content, net)
			if err != nil {
				return content
			}
			return result
		}
	}
	result, err := docker.AddNetworkToCompose(content, networks[0])
	if err != nil {
		return content
	}
	return result
}

func (s *Server) processTemplateFiles(deploymentName, templateID string, envVars []EnvVar) {
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

		for _, env := range envVars {
			placeholder := "${" + env.Key + "}"
			content = strings.ReplaceAll(content, placeholder, env.Value)
		}

		filePath := filepath.Join(deploymentDir, file.Path)
		fileDir := filepath.Dir(filePath)

		if err := os.MkdirAll(fileDir, 0755); err != nil {
			continue
		}

		_ = os.WriteFile(filePath, []byte(content), 0644)
	}
}

// processTemplateEnv produces the platform's environment file for a new
// deployment, entirely driven by the template's env spec. Base content
// precedence: the example file inside the deployed image (when the template
// declares one and it is readable), then the template's own fallback content,
// then whatever the template already placed at the target path. The
// deployment's environment variables and freshly generated secrets are then
// applied on top, key by key.
func (s *Server) processTemplateEnv(deploymentName, templateID, composeContent string, envVars []EnvVar) {
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

	spec := metadata.Env
	if spec.File == "" {
		return
	}

	targetPath := filepath.Join(s.config.DeploymentsPath, deploymentName, spec.File)

	var base string
	if spec.ExamplePath != "" {
		if image := mainServiceImage(composeContent); image != "" {
			if content, err := docker.ExtractFileFromImage(image, spec.ExamplePath); err == nil {
				base = string(content)
			} else {
				log.Printf("template env: could not read %s from %s: %v", spec.ExamplePath, image, err)
			}
		}
	}
	if base == "" {
		base = spec.Template
	}
	if base == "" {
		if existing, err := os.ReadFile(targetPath); err == nil {
			base = string(existing)
		}
	}

	content := buildTemplateEnvContent(base, deploymentName, envVars, spec)
	if err := os.WriteFile(targetPath, []byte(content), 0600); err != nil {
		log.Printf("template env: failed to write %s: %v", targetPath, err)
	}
}

func buildTemplateEnvContent(base, deploymentName string, envVars []EnvVar, spec TemplateEnv) string {
	content := strings.ReplaceAll(base, "${NAME}", deploymentName)

	var keys []string
	values := make(map[string]string)
	for _, env := range envVars {
		if env.Key == "" {
			continue
		}
		if _, ok := values[env.Key]; !ok {
			keys = append(keys, env.Key)
		}
		values[env.Key] = env.Value
	}

	for _, secret := range spec.Secrets {
		if secret.Key == "" {
			continue
		}
		if _, ok := values[secret.Key]; ok {
			continue
		}
		value, err := generateSecretValue(secret)
		if err != nil {
			continue
		}
		keys = append(keys, secret.Key)
		values[secret.Key] = value
	}

	return applyEnvValues(content, keys, values)
}

// applyEnvValues sets KEY=value pairs in dotenv-formatted content, replacing
// lines whose key matches and appending the rest.
func applyEnvValues(content string, keys []string, values map[string]string) string {
	lines := strings.Split(content, "\n")
	seen := make(map[string]bool)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		eq := strings.IndexByte(trimmed, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:eq])
		if value, ok := values[key]; ok {
			lines[i] = key + "=" + quoteEnvValue(value)
			seen[key] = true
		}
	}

	content = strings.Join(lines, "\n")
	var missing []string
	for _, key := range keys {
		if !seen[key] {
			missing = append(missing, key+"="+quoteEnvValue(values[key]))
		}
	}
	if len(missing) > 0 {
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += strings.Join(missing, "\n") + "\n"
	}
	return content
}

func quoteEnvValue(value string) string {
	if strings.ContainsAny(value, " \t#\"") {
		return strconv.Quote(value)
	}
	return value
}

func generateSecretValue(spec TemplateEnvSecret) (string, error) {
	n := spec.Bytes
	if n <= 0 {
		n = 32
	}

	var value string
	switch spec.Encoding {
	case "hex":
		buf := make([]byte, n)
		if _, err := cryptoRand.Read(buf); err != nil {
			return "", err
		}
		value = hex.EncodeToString(buf)
	case "alphanumeric":
		const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		out := make([]byte, n)
		for i := range out {
			idx, err := cryptoRand.Int(cryptoRand.Reader, big.NewInt(int64(len(charset))))
			if err != nil {
				return "", err
			}
			out[i] = charset[idx.Int64()]
		}
		value = string(out)
	default:
		buf := make([]byte, n)
		if _, err := cryptoRand.Read(buf); err != nil {
			return "", err
		}
		value = base64.StdEncoding.EncodeToString(buf)
	}

	return spec.Prefix + value, nil
}

// mainServiceImage picks the image of the service a template's env example
// should come from: the conventional "app" service, or the first service with
// an image.
func mainServiceImage(content string) string {
	var compose composeFile
	if err := yaml.Unmarshal([]byte(content), &compose); err != nil {
		return ""
	}
	if svc, ok := compose.Services["app"]; ok && svc.Image != "" {
		return svc.Image
	}
	names := make([]string, 0, len(compose.Services))
	for name := range compose.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if image := compose.Services[name].Image; image != "" {
			return image
		}
	}
	return ""
}

// loadTemplateMetadata reads an installed template's metadata.
func (s *Server) loadTemplateMetadata(templateID string) (*TemplateMetadata, error) {
	metadataPath := filepath.Join(s.config.DeploymentsPath, ".flatrun", "templates", templateID, "metadata.yml")

	content, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, err
	}

	var metadata TemplateMetadata
	if err := yaml.Unmarshal(content, &metadata); err != nil {
		return nil, err
	}
	return &metadata, nil
}

// templateSeedMounts returns the host paths of a template's mounts that ask to
// be filled from the image.
func (s *Server) templateSeedMounts(templateID string) []string {
	if templateID == "" {
		return nil
	}

	metadata, err := s.loadTemplateMetadata(templateID)
	if err != nil {
		return nil
	}

	var hostPaths []string
	for _, m := range metadata.Mounts {
		if m.Seed {
			hostPaths = append(hostPaths, templateMountHostPath(m))
		}
	}
	return hostPaths
}

func (s *Server) applyTemplateMountOwnership(deploymentName, templateID string) {
	metadata, err := s.loadTemplateMetadata(templateID)
	if err != nil {
		return
	}

	if len(metadata.Mounts) == 0 {
		return
	}

	var mounts []docker.MountOwnership
	for _, m := range metadata.Mounts {
		if m.Type != "file" {
			continue
		}
		mounts = append(mounts, docker.MountOwnership{
			HostPath:       templateMountHostPath(m),
			User:           m.User,
			Subdirectories: m.Subdirectories,
		})
	}

	if len(mounts) > 0 {
		if err := s.manager.ApplyMountOwnership(deploymentName, mounts); err != nil {
			log.Printf("Warning: failed to apply mount ownership for %s: %v", deploymentName, err)
		}
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

	s.annotateCertificatesWithDeployment(certificates)

	c.JSON(http.StatusOK, gin.H{
		"certificates": certificates,
	})
}

func (s *Server) annotateCertificatesWithDeployment(certs []models.Certificate) {
	if len(certs) == 0 {
		return
	}

	deployments, err := s.manager.FindDeployments()
	if err != nil {
		log.Printf("warning: failed to list deployments for cert annotation: %v", err)
		return
	}

	domainToDeployment := make(map[string]string)
	for _, d := range deployments {
		if d.Metadata == nil {
			continue
		}
		for _, dom := range d.Metadata.Domains {
			if dom.Domain != "" {
				if _, exists := domainToDeployment[dom.Domain]; !exists {
					domainToDeployment[dom.Domain] = d.Name
				}
			}
			for _, alias := range dom.Aliases {
				if alias != "" {
					if _, exists := domainToDeployment[alias]; !exists {
						domainToDeployment[alias] = d.Name
					}
				}
			}
		}
	}

	for i := range certs {
		if name, ok := domainToDeployment[certs[i].Domain]; ok {
			certs[i].DeploymentID = name
		}
	}
}

func (s *Server) requestCertificate(c *gin.Context) {
	var req struct {
		Domain     string `json:"domain" binding:"required"`
		Deployment string `json:"deployment"`
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

	if result.Success && req.Deployment != "" {
		s.enableSSLForDeployment(req.Deployment, req.Domain)
	}

	c.JSON(http.StatusCreated, gin.H{
		"message": "Certificate requested",
		"result":  result,
	})
}

func (s *Server) enableSSLForDeployment(name, domain string) {
	dep, err := s.manager.GetDeployment(name)
	if err != nil {
		log.Printf("warning: failed to get deployment %s for SSL enable: %v", name, err)
		return
	}
	if dep.Metadata == nil {
		return
	}

	updated := false
	for i, d := range dep.Metadata.Domains {
		if d.Domain == domain && d.SSL.AutoCert && !d.SSL.Enabled {
			dep.Metadata.Domains[i].SSL.Enabled = true
			updated = true
		}
	}
	if !updated {
		return
	}

	if err := s.manager.SaveMetadata(name, dep.Metadata); err != nil {
		log.Printf("warning: failed to save metadata after SSL enable for %s: %v", name, err)
	}
	if err := s.proxyOrchestrator.NginxManager().UpdateVirtualHost(dep); err != nil {
		log.Printf("warning: failed to update vhost after SSL enable for %s: %v", name, err)
		return
	}
	if err := s.proxyOrchestrator.NginxManager().TestConfig(); err != nil {
		log.Printf("warning: SSL config test failed for %s: %v", name, err)
		return
	}
	if err := s.proxyOrchestrator.NginxManager().Reload(); err != nil {
		log.Printf("warning: failed to reload nginx after SSL enable for %s: %v", name, err)
	}
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

func (s *Server) getCertificate(c *gin.Context) {
	domain := c.Param("domain")
	cert, err := s.proxyOrchestrator.SSLManager().GetCertificate(domain)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	annotated := []models.Certificate{*cert}
	s.annotateCertificatesWithDeployment(annotated)
	c.JSON(http.StatusOK, gin.H{"certificate": annotated[0]})
}

func (s *Server) renewCertificate(c *gin.Context) {
	domain := c.Param("domain")
	force := c.Query("force") == "true"

	result, err := s.proxyOrchestrator.RenewCertificate(domain, force)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	message := "Certificate renewed"
	if !result.Renewed {
		message = "Certificate is not yet due for renewal"
	}

	c.JSON(http.StatusOK, gin.H{
		"message": message,
		"domain":  domain,
		"result":  result,
	})
}

func (s *Server) setCertificateAutoRenew(c *gin.Context) {
	domain := c.Param("domain")

	var req struct {
		AutoRenew bool `json:"auto_renew"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := s.proxyOrchestrator.SSLManager().SetAutoRenew(domain, req.AutoRenew); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"domain":     domain,
		"auto_renew": req.AutoRenew,
	})
}

func (s *Server) renewDeploymentCertificates(c *gin.Context) {
	name := c.Param("name")

	dep, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	if dep.Metadata == nil {
		c.JSON(http.StatusOK, gin.H{
			"message": "no domains configured for deployment",
			"result":  nil,
		})
		return
	}

	seen := make(map[string]bool)
	var domains []string
	for _, d := range dep.Metadata.Domains {
		if d.Domain != "" && !seen[d.Domain] {
			domains = append(domains, d.Domain)
			seen[d.Domain] = true
		}
		for _, alias := range d.Aliases {
			if alias != "" && !seen[alias] {
				domains = append(domains, alias)
				seen[alias] = true
			}
		}
	}

	if len(domains) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"message": "no domains configured for deployment",
			"result":  nil,
		})
		return
	}

	result := s.proxyOrchestrator.RenewCertificatesForDomains(domains)
	c.JSON(http.StatusOK, gin.H{
		"message":    "Deployment certificate renewal completed",
		"deployment": name,
		"result":     result,
	})
}

func (s *Server) deleteCertificate(c *gin.Context) {
	domain := c.Param("domain")
	force := c.DefaultQuery("force", "false") == "true"

	vhosts := s.proxyOrchestrator.NginxManager().GetVhostsUsingSSLDomain(domain)
	if len(vhosts) > 0 && !force {
		c.JSON(http.StatusConflict, gin.H{
			"error":  "Certificate is in use by virtual hosts",
			"vhosts": vhosts,
			"hint":   "Disable SSL for these deployments first, or use ?force=true to delete anyway",
		})
		return
	}

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

	if planRequested(c) {
		s.planProxySetup(c, deployment)
		return
	}
	if !s.requirePlannedAction(c, name) {
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

func (s *Server) disableSSL(c *gin.Context) {
	name := c.Param("name")

	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Deployment not found",
		})
		return
	}

	if deployment.Metadata == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Deployment has no metadata configured",
		})
		return
	}

	deployment.Metadata.SSL.Enabled = false
	deployment.Metadata.SSL.AutoCert = false

	if err := s.manager.SaveMetadata(name, deployment.Metadata); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to save metadata: " + err.Error(),
		})
		return
	}

	if err := s.proxyOrchestrator.NginxManager().UpdateVirtualHost(deployment); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to update virtual host: " + err.Error(),
		})
		return
	}

	if err := s.proxyOrchestrator.NginxManager().TestConfig(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Nginx config test failed: " + err.Error(),
		})
		return
	}

	if err := s.proxyOrchestrator.NginxManager().Reload(); err != nil {
		log.Printf("Warning: failed to reload nginx: %v", err)
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "SSL disabled for deployment",
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

	actor := auth.GetActorFromContext(c)
	if actor != nil && actor.Role != auth.RoleAdmin {
		filtered := vhosts[:0]
		for _, vhost := range vhosts {
			// VirtualHostInfo.Name is the deployment name derived from <deployment>.conf.
			if actor.CanAccessDeployment(vhost.Name, auth.AccessLevelRead) {
				filtered = append(filtered, vhost)
			}
		}
		vhosts = filtered
	}

	c.JSON(http.StatusOK, gin.H{
		"virtual_hosts": vhosts,
	})
}

type ProxySyncResult struct {
	Name    string `json:"name"`
	Domain  string `json:"domain"`
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Created bool   `json:"created"`
}

func (s *Server) syncAllProxies(c *gin.Context) {
	actor := auth.GetActorFromContext(c)
	if actor != nil && actor.Role != auth.RoleAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "Admin access required"})
		return
	}

	deployments, err := s.manager.FindDeployments()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	existingVhosts, err := s.proxyOrchestrator.ListVirtualHosts()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to list virtual hosts: " + err.Error(),
		})
		return
	}

	vhostMap := make(map[string]bool)
	for _, vhost := range existingVhosts {
		vhostMap[vhost.Name] = true
	}

	var results []ProxySyncResult
	var synced, skipped, failed int

	for _, deployment := range deployments {
		if deployment.Metadata == nil || !deployment.Metadata.Networking.Expose {
			continue
		}

		domain := deployment.Metadata.Networking.Domain
		if domain == "" {
			continue
		}

		if vhostMap[domain] {
			skipped++
			results = append(results, ProxySyncResult{
				Name:    deployment.Name,
				Domain:  domain,
				Success: true,
				Message: "Already exists",
				Created: false,
			})
			continue
		}

		result, err := s.proxyOrchestrator.SetupDeployment(&deployment)
		if err != nil {
			failed++
			results = append(results, ProxySyncResult{
				Name:    deployment.Name,
				Domain:  domain,
				Success: false,
				Message: err.Error(),
				Created: false,
			})
			continue
		}

		if result.VirtualHostCreated {
			synced++
			results = append(results, ProxySyncResult{
				Name:    deployment.Name,
				Domain:  domain,
				Success: true,
				Message: "Created",
				Created: true,
			})
		} else {
			skipped++
			results = append(results, ProxySyncResult{
				Name:    deployment.Name,
				Domain:  domain,
				Success: true,
				Message: "Setup completed but vhost already existed",
				Created: false,
			})
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Proxy sync completed",
		"synced":  synced,
		"skipped": skipped,
		"failed":  failed,
		"total":   len(results),
		"results": results,
	})
}

func (s *Server) setupProxyWithRetry(deployment *models.Deployment, maxRetries int) (*proxy.SetupResult, error) {
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		result, err := s.proxyOrchestrator.SetupDeployment(deployment)
		if err == nil {
			return result, nil
		}
		lastErr = err
		log.Printf("Proxy setup attempt %d/%d for %s failed: %v", i+1, maxRetries, deployment.Name, err)
		if i < maxRetries-1 {
			time.Sleep(time.Duration(i+1) * 2 * time.Second)
		}
	}
	return nil, lastErr
}

func (s *Server) listDeploymentServices(c *gin.Context) {
	name := c.Param("name")
	services, err := s.manager.GetComposeServices(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}

	if deployment, err := s.manager.GetDeployment(name); err == nil && deployment.Metadata != nil {
		primary := deployment.Metadata.EffectivePrimaryService()
		for i := range services {
			services[i].IsPrimary = primary != "" && services[i].Name == primary
		}
	}

	c.JSON(http.StatusOK, gin.H{"services": services})
}

func (s *Server) listDomains(c *gin.Context) {
	name := c.Param("name")
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}

	if deployment.Metadata == nil {
		c.JSON(http.StatusOK, gin.H{"domains": []models.DomainConfig{}})
		return
	}

	domains := deployment.Metadata.GetDomains()
	c.JSON(http.StatusOK, gin.H{"domains": domains})
}

func (s *Server) resolveService(name string, serviceName string) (string, error) {
	return s.manager.ResolveService(name, serviceName)
}

func (s *Server) addDomain(c *gin.Context) {
	name := c.Param("name")
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}

	var domain models.DomainConfig
	if err := c.ShouldBindJSON(&domain); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid domain data: " + err.Error()})
		return
	}

	if planRequested(c) {
		s.planDomainChange(c, deployment, "deployment.domain.add", &domain, func(dep *models.Deployment) (bool, error) {
			return false, s.mutateDomainAdd(dep, &domain)
		})
		return
	}

	if !s.requirePlannedAction(c, name) {
		return
	}

	if err := s.mutateDomainAdd(deployment, &domain); err != nil {
		respondAPIError(c, err)
		return
	}

	if err := s.manager.SaveMetadata(name, deployment.Metadata); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save domain: " + err.Error()})
		return
	}

	var result *proxy.SetupResult
	if s.proxyOrchestrator != nil {
		var err error
		result, err = s.proxyOrchestrator.SetupDeployment(deployment)
		if err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": "Failed to configure proxy: " + err.Error()})
			return
		}
	}

	c.JSON(http.StatusCreated, gin.H{
		"message":      "Domain added successfully",
		"domain":       domain,
		"proxy_result": result,
	})
}

func (s *Server) updateDomain(c *gin.Context) {
	name := c.Param("name")
	domainID := c.Param("domainId")

	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}

	var updatedDomain models.DomainConfig
	if err := c.ShouldBindJSON(&updatedDomain); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid domain data: " + err.Error()})
		return
	}

	if planRequested(c) {
		s.planDomainChange(c, deployment, "deployment.domain.update", &updatedDomain, func(dep *models.Deployment) (bool, error) {
			return false, s.mutateDomainUpdate(dep, domainID, &updatedDomain)
		})
		return
	}

	if !s.requirePlannedAction(c, name) {
		return
	}

	if err := s.mutateDomainUpdate(deployment, domainID, &updatedDomain); err != nil {
		respondAPIError(c, err)
		return
	}

	if err := s.manager.SaveMetadata(name, deployment.Metadata); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save domain: " + err.Error()})
		return
	}

	result, err := s.proxyOrchestrator.SetupDeployment(deployment)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "Failed to configure proxy: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":      "Domain updated successfully",
		"domain":       updatedDomain,
		"proxy_result": result,
	})
}

func (s *Server) deleteDomain(c *gin.Context) {
	name := c.Param("name")
	domainID := c.Param("domainId")

	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}

	if planRequested(c) {
		s.planDomainChange(c, deployment, "deployment.domain.delete", nil, func(dep *models.Deployment) (bool, error) {
			return mutateDomainDelete(dep, domainID)
		})
		return
	}

	if !s.requirePlannedAction(c, name) {
		return
	}

	teardown, err := mutateDomainDelete(deployment, domainID)
	if err != nil {
		respondAPIError(c, err)
		return
	}

	if err := s.manager.SaveMetadata(name, deployment.Metadata); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save metadata: " + err.Error()})
		return
	}

	if s.proxyOrchestrator != nil {
		if teardown {
			if err := s.proxyOrchestrator.TeardownDeployment(name); err != nil {
				log.Printf("Warning: failed to teardown proxy for %s: %v", name, err)
			}
		} else {
			if _, err := s.proxyOrchestrator.SetupDeployment(deployment); err != nil {
				log.Printf("Warning: failed to update proxy for %s: %v", name, err)
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Domain deleted successfully",
	})
}

func generateDomainID() string {
	return uuid.New().String()
}

func (s *Server) getSystemStats(c *gin.Context) {
	const statsTTL = 10 * time.Second

	s.statsMu.RLock()
	if s.statsCache != nil && time.Since(s.statsAt) < statsTTL {
		cached := s.statsCache
		s.statsMu.RUnlock()
		c.JSON(http.StatusOK, cached)
		return
	}
	s.statsMu.RUnlock()

	var (
		wg                 sync.WaitGroup
		deployments        []models.Deployment
		depErr             error
		containerStats     map[string]int
		imageStats         map[string]int
		volumeStats        map[string]int
		systemStats        *system.SystemStats
		networkCount       int
		portCount          int
		systemPortCount    int
		systemServiceCount int
		infraCount         int
		certCount          int
	)

	// Each of these shells out to docker or the OS and is independent of the
	// others, so run them concurrently instead of paying the sum of their
	// latencies. Every closure writes only its own variable.
	run := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f()
		}()
	}
	run(func() { deployments, depErr = s.manager.ListDeployments() })
	run(func() { containerStats, _ = s.networksManager.GetContainerStats() })
	run(func() { imageStats, _ = s.networksManager.GetImageStats() })
	run(func() { volumeStats, _ = s.networksManager.GetVolumeStats() })
	run(func() { systemStats, _ = system.GetSystemStats() })
	run(func() {
		if networks, err := s.networksManager.ListNetworks(); err == nil {
			networkCount = len(networks)
		}
	})
	run(func() {
		if containers, err := s.networksManager.ListContainers(); err == nil {
			for _, container := range containers {
				portCount += len(container.Ports)
			}
		}
	})
	run(func() {
		if ports, err := s.networksManager.ListPorts(); err == nil {
			systemPortCount = len(ports)
		}
	})
	run(func() {
		if services, err := s.servicesManager.ListServices(); err == nil {
			systemServiceCount = len(services)
		}
	})
	run(func() {
		if services, err := s.infraManager.ListServices(); err == nil {
			infraCount = len(services)
		}
	})
	run(func() {
		if certs, err := s.proxyOrchestrator.ListCertificates(); err == nil {
			certCount = len(certs)
		}
	})
	wg.Wait()

	if depErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": depErr.Error(),
		})
		return
	}

	actor := auth.GetActorFromContext(c)
	if actor != nil && actor.Role != auth.RoleAdmin {
		var filtered []models.Deployment
		for _, d := range deployments {
			if actor.CanAccessDeployment(d.Name, auth.AccessLevelRead) {
				filtered = append(filtered, d)
			}
		}
		deployments = filtered
	}

	depStats := gin.H{
		"total_deployments": len(deployments),
		"running":           0,
		"stopped":           0,
		"error":             0,
		"unknown":           0,
	}
	for _, d := range deployments {
		switch d.Status {
		case "running":
			depStats["running"] = depStats["running"].(int) + 1
		case "stopped":
			depStats["stopped"] = depStats["stopped"].(int) + 1
		case "error":
			depStats["error"] = depStats["error"].(int) + 1
		default:
			depStats["unknown"] = depStats["unknown"].(int) + 1
		}
	}

	appCount := len(s.pluginRegistry.List())

	result := gin.H{
		"deployments":    depStats,
		"containers":     containerStats,
		"images":         imageStats,
		"volumes":        volumeStats,
		"networks":       gin.H{"total": networkCount},
		"ports":          gin.H{"total": portCount},
		"system":         systemStats,
		"system_ports":   gin.H{"total": systemPortCount},
		"services":       gin.H{"total": systemServiceCount},
		"infrastructure": gin.H{"total": infraCount},
		"certificates":   gin.H{"total": certCount},
		"apps":           gin.H{"total": appCount},
	}

	s.statsMu.Lock()
	s.statsCache = result
	s.statsAt = time.Now()
	s.statsMu.Unlock()

	c.JSON(http.StatusOK, result)
}

func (s *Server) listContainers(c *gin.Context) {
	containers, err := s.networksManager.ListContainers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	actor := auth.GetActorFromContext(c)
	if actor != nil && actor.Role != auth.RoleAdmin {
		filtered := containers[:0]
		for _, container := range containers {
			// Non-admins only see FlatRun/Compose containers assigned through deployment access.
			if actor.CanAccessDeployment(container.DeploymentName, auth.AccessLevelRead) {
				filtered = append(filtered, container)
			}
		}
		containers = filtered
	}

	c.JSON(http.StatusOK, gin.H{
		"containers": containers,
	})
}

func (s *Server) startContainer(c *gin.Context) {
	id := c.Param("id")
	if !s.requireContainerAccess(c, id, auth.AccessLevelWrite) {
		return
	}

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
	if !s.requireContainerAccess(c, id, auth.AccessLevelWrite) {
		return
	}

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
	if !s.requireContainerAccess(c, id, auth.AccessLevelWrite) {
		return
	}

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
	if !s.requireContainerAccess(c, id, auth.AccessLevelAdmin) {
		return
	}

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
	if !s.requireContainerAccess(c, id, auth.AccessLevelRead) {
		return
	}

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

func (s *Server) getContainerStats(c *gin.Context) {
	id := c.Param("id")
	if !s.requireContainerAccess(c, id, auth.AccessLevelRead) {
		return
	}

	stats, err := docker.GetContainerStats(id)
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

func (s *Server) getAllContainerStats(c *gin.Context) {
	stats, err := docker.GetAllContainerStats()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	actor := auth.GetActorFromContext(c)
	if actor != nil && actor.Role != auth.RoleAdmin {
		filtered := stats[:0]
		for _, stat := range stats {
			// Non-admins only see FlatRun/Compose containers assigned through deployment access.
			if actor.CanAccessDeployment(stat.DeploymentName, auth.AccessLevelRead) {
				filtered = append(filtered, stat)
			}
		}
		stats = filtered
	}

	c.JSON(http.StatusOK, gin.H{
		"stats": stats,
	})
}

func (s *Server) getDeploymentContainerStats(c *gin.Context) {
	name := c.Param("name")

	if _, err := s.manager.GetDeployment(name); err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "deployment not found",
		})
		return
	}

	stats, _ := docker.GetDeploymentStats(name)
	if stats == nil {
		stats = []docker.ContainerStats{}
	}

	var totalCPU, totalMemPercent float64
	var totalMemUsage, totalMemLimit uint64
	for _, s := range stats {
		totalCPU += s.CPUPercent
		totalMemPercent += s.MemoryPercent
		totalMemUsage += s.MemoryUsage
		totalMemLimit += s.MemoryLimit
	}

	c.JSON(http.StatusOK, gin.H{
		"deployment": name,
		"services":   stats,
		"summary": gin.H{
			"cpu_percent":    totalCPU,
			"memory_percent": totalMemPercent,
			"memory_usage":   totalMemUsage,
			"memory_limit":   totalMemLimit,
		},
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
		Name         string `json:"name" binding:"required"`
		CredentialID string `json:"credential_id"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	var cred *models.RegistryCredential
	var err error

	if req.CredentialID != "" {
		cred, err = s.credentialsManager.GetCredential(req.CredentialID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Invalid credential_id: " + err.Error(),
			})
			return
		}
	} else {
		cred = s.credentialsManager.FindCredentialForImage(req.Name)
	}

	if cred != nil {
		if err := credentials.PullImageWithAuth(req.Name, cred); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": err.Error(),
			})
			return
		}
	} else {
		if err := s.networksManager.PullImage(req.Name); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": err.Error(),
			})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message":         "Image pulled",
		"name":            req.Name,
		"used_credential": cred != nil,
	})
}

func (s *Server) cleanupSystemImages(c *gin.Context) {
	var req struct {
		DryRun bool `json:"dry_run"`
	}
	_ = c.ShouldBindJSON(&req)

	result, err := s.manager.PruneDanglingImages(req.DryRun)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message":     "System image cleanup complete",
		"removed":     result.Removed,
		"freed_bytes": result.FreedBytes,
		"dry_run":     result.DryRun,
	})
}

func (s *Server) cleanupDeploymentImages(c *gin.Context) {
	name := c.Param("name")

	var req struct {
		DryRun bool `json:"dry_run"`
	}
	_ = c.ShouldBindJSON(&req)

	if _, err := s.manager.GetDeployment(name); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found: " + err.Error()})
		return
	}

	result, err := s.manager.CleanupDeploymentImages(name, req.DryRun)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message":     "Deployment image cleanup complete",
		"name":        name,
		"removed":     result.Removed,
		"freed_bytes": result.FreedBytes,
		"images_kept": result.ImagesKept,
		"dry_run":     result.DryRun,
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
	if !s.requireUnprotectedDeploymentAction(c, name, protectedActionUploadFile) {
		return
	}

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
	if !s.requireUnprotectedDeploymentAction(c, name, protectedActionDeleteFile) {
		return
	}

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
	if !s.requireUnprotectedDeploymentAction(c, name, protectedActionCreateDir) {
		return
	}

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

func (s *Server) createDeploymentFile(c *gin.Context) {
	name := c.Param("name")
	path := c.Param("path")
	if !s.requireUnprotectedDeploymentAction(c, name, protectedActionUploadFile) {
		return
	}

	if err := s.filesManager.CreateFile(name, path); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	info, _ := s.filesManager.GetFileInfo(name, path)
	c.JSON(http.StatusOK, gin.H{
		"message": "File created",
		"file":    info,
	})
}

func (s *Server) chmodDeploymentFile(c *gin.Context) {
	name := c.Param("name")
	path := c.Param("path")

	var req struct {
		Mode int `json:"mode" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	if req.Mode < 0 || req.Mode > 0o777 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mode must be between 0 and 0777"})
		return
	}

	if err := s.filesManager.Chmod(name, path, os.FileMode(req.Mode)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	info, _ := s.filesManager.GetFileInfo(name, path)
	c.JSON(http.StatusOK, gin.H{
		"message": "Permissions updated",
		"file":    info,
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

func (s *Server) listUsersByDatabase(c *gin.Context) {
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

	users, err := s.databaseManager.ListDatabaseUsers(&req.ConnectionConfig, req.Database)
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
		TargetUsername string `json:"target_username" binding:"required"`
		TargetPassword string `json:"target_password" binding:"required"`
		TargetHost     string `json:"target_host"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	if err := s.databaseManager.CreateUser(&req.ConnectionConfig, req.TargetUsername, req.TargetPassword, req.TargetHost); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"message":  "User created",
		"username": req.TargetUsername,
	})
}

func (s *Server) grantDatabasePrivileges(c *gin.Context) {
	var req struct {
		database.ConnectionConfig
		TargetUsername string `json:"target_username" binding:"required"`
		TargetDatabase string `json:"target_database" binding:"required"`
		TargetHost     string `json:"target_host"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	if err := s.databaseManager.GrantPrivileges(&req.ConnectionConfig, req.TargetUsername, req.TargetDatabase, req.TargetHost); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":  "Privileges granted",
		"username": req.TargetUsername,
		"database": req.TargetDatabase,
	})
}

func (s *Server) deleteDatabaseInServer(c *gin.Context) {
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

	if err := s.databaseManager.DeleteDatabase(&req.ConnectionConfig, req.DbName); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Database deleted",
		"name":    req.DbName,
	})
}

func (s *Server) deleteDatabaseUser(c *gin.Context) {
	var req struct {
		database.ConnectionConfig
		TargetUsername string `json:"target_username" binding:"required"`
		TargetHost     string `json:"target_host"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	if err := s.databaseManager.DeleteUser(&req.ConnectionConfig, req.TargetUsername, req.TargetHost); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":  "User deleted",
		"username": req.TargetUsername,
	})
}

func (s *Server) queryTableData(c *gin.Context) {
	var req struct {
		database.ConnectionConfig
		Database string `json:"database" binding:"required"`
		Table    string `json:"table" binding:"required"`
		Limit    int    `json:"limit"`
		Offset   int    `json:"offset"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	result, err := s.databaseManager.QueryTable(&req.ConnectionConfig, req.Database, req.Table, req.Limit, req.Offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, result)
}

func (s *Server) describeTable(c *gin.Context) {
	var req struct {
		database.ConnectionConfig
		Database string `json:"database" binding:"required"`
		Table    string `json:"table" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	schema, err := s.databaseManager.DescribeTable(&req.ConnectionConfig, req.Database, req.Table)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, schema)
}

func (s *Server) executeDatabaseQuery(c *gin.Context) {
	var req struct {
		database.ConnectionConfig
		Database string `json:"database" binding:"required"`
		Query    string `json:"query" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	result, err := s.databaseManager.ExecuteQuery(&req.ConnectionConfig, req.Database, req.Query)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, result)
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

func (s *Server) listRegistryTypes(c *gin.Context) {
	types := s.credentialsManager.ListRegistryTypes()
	c.JSON(http.StatusOK, gin.H{
		"registry_types": types,
	})
}

func (s *Server) getRegistryType(c *gin.Context) {
	slug := c.Param("slug")

	rt, err := s.credentialsManager.GetRegistryType(slug)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"registry_type": rt,
	})
}

func (s *Server) createRegistryType(c *gin.Context) {
	var req struct {
		Name        string   `json:"name" binding:"required"`
		URLPatterns []string `json:"url_patterns" binding:"required"`
		AuthType    string   `json:"auth_type"`
		LoginURL    string   `json:"login_url"`
		DocsURL     string   `json:"docs_url"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	authType := models.AuthTypeBasic
	if req.AuthType == "token" {
		authType = models.AuthTypeToken
	}

	rt, err := s.credentialsManager.CreateRegistryType(req.Name, req.URLPatterns, authType, req.LoginURL, req.DocsURL)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"message":       "Registry type created",
		"registry_type": rt,
	})
}

func (s *Server) updateRegistryType(c *gin.Context) {
	slug := c.Param("slug")

	var req struct {
		Name        string   `json:"name"`
		URLPatterns []string `json:"url_patterns"`
		AuthType    string   `json:"auth_type"`
		LoginURL    string   `json:"login_url"`
		DocsURL     string   `json:"docs_url"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	var authType models.AuthType
	if req.AuthType == "basic" {
		authType = models.AuthTypeBasic
	} else if req.AuthType == "token" {
		authType = models.AuthTypeToken
	}

	rt, err := s.credentialsManager.UpdateRegistryType(slug, req.Name, req.URLPatterns, authType, req.LoginURL, req.DocsURL)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":       "Registry type updated",
		"registry_type": rt,
	})
}

func (s *Server) deleteRegistryType(c *gin.Context) {
	slug := c.Param("slug")

	if err := s.credentialsManager.DeleteRegistryType(slug); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Registry type deleted",
		"slug":    slug,
	})
}

func (s *Server) listCredentials(c *gin.Context) {
	creds := s.credentialsManager.ListCredentials()
	c.JSON(http.StatusOK, gin.H{
		"credentials": creds,
	})
}

func (s *Server) getCredential(c *gin.Context) {
	id := c.Param("id")

	cred, err := s.credentialsManager.GetCredential(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": err.Error(),
		})
		return
	}

	rt, _ := s.credentialsManager.GetRegistryType(cred.RegistryTypeSlug)

	c.JSON(http.StatusOK, gin.H{
		"credential": models.RegistryCredentialWithType{
			RegistryCredential: *cred,
			RegistryType:       rt,
		},
	})
}

func (s *Server) createCredential(c *gin.Context) {
	var req struct {
		Name             string `json:"name" binding:"required"`
		RegistryTypeSlug string `json:"registry_type_slug" binding:"required"`
		RegistryURL      string `json:"registry_url"`
		Username         string `json:"username" binding:"required"`
		Password         string `json:"password" binding:"required"`
		Email            string `json:"email"`
		IsDefault        bool   `json:"is_default"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	cred, err := s.credentialsManager.CreateCredential(req.Name, req.RegistryTypeSlug, req.RegistryURL, req.Username, req.Password, req.Email, req.IsDefault)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	rt, _ := s.credentialsManager.GetRegistryType(cred.RegistryTypeSlug)

	c.JSON(http.StatusCreated, gin.H{
		"message": "Credential created",
		"credential": models.RegistryCredentialWithType{
			RegistryCredential: *cred,
			RegistryType:       rt,
		},
	})
}

func (s *Server) updateCredential(c *gin.Context) {
	id := c.Param("id")

	var req struct {
		Name        string `json:"name"`
		RegistryURL string `json:"registry_url"`
		Username    string `json:"username"`
		Password    string `json:"password"`
		Email       string `json:"email"`
		IsDefault   *bool  `json:"is_default"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	cred, err := s.credentialsManager.UpdateCredential(id, req.Name, req.RegistryURL, req.Username, req.Password, req.Email, req.IsDefault)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	rt, _ := s.credentialsManager.GetRegistryType(cred.RegistryTypeSlug)

	c.JSON(http.StatusOK, gin.H{
		"message": "Credential updated",
		"credential": models.RegistryCredentialWithType{
			RegistryCredential: *cred,
			RegistryType:       rt,
		},
	})
}

func (s *Server) deleteCredential(c *gin.Context) {
	id := c.Param("id")

	if err := s.credentialsManager.DeleteCredential(id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Credential deleted",
		"id":      id,
	})
}

func (s *Server) testCredential(c *gin.Context) {
	id := c.Param("id")

	if err := s.credentialsManager.TestCredential(id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   err.Error(),
			"success": false,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Credential test successful",
		"success": true,
	})
}

func (s *Server) deploymentAuthOptions(name string) (credentials.AuthConfig, []docker.RunOption) {
	deployment, err := s.manager.GetDeployment(name)
	if err != nil || deployment.Metadata == nil {
		return credentials.AuthConfig{}, nil
	}
	var ids []string
	if deployment.Metadata.CredentialID != "" {
		ids = append(ids, deployment.Metadata.CredentialID)
	}
	for _, id := range deployment.Metadata.ServiceCredentials {
		ids = append(ids, id)
	}
	cfg, err := s.credentialsManager.BuildAuthConfig(ids)
	if err != nil {
		log.Printf("Warning: failed to build docker auth config for %s: %v", name, err)
		return credentials.AuthConfig{}, nil
	}
	return cfg, []docker.RunOption{docker.WithDockerConfig(cfg.Dir())}
}

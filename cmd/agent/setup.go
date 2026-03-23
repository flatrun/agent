package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/networks"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/templates"
	"gopkg.in/yaml.v3"
)

type templateMetadata struct {
	Name     string         `yaml:"name"`
	Type     string         `yaml:"type"`
	Category string         `yaml:"category"`
	Mounts   mountsManifest `yaml:"mounts"`
	Setup    setupManifest  `yaml:"setup"`
}

type mountsManifest struct {
	Dirs  []string `yaml:"dirs"`
	Files []string `yaml:"files"`
}

type setupManifest struct {
	Dirs  []string      `yaml:"dirs"`
	Files []setupFileEntry `yaml:"files"`
}

type setupFileEntry struct {
	Src       string `yaml:"src"`
	Dest      string `yaml:"dest"`
	Overwrite bool   `yaml:"overwrite"`
	External  bool   `yaml:"external"`
	Empty     bool   `yaml:"empty"`
}

func handleSetup(args []string) {
	if len(args) == 0 {
		setupUsage()
		os.Exit(1)
	}

	switch args[0] {
	case "infra":
		handleSetupInfra(args[1:])
	case "help", "--help", "-h":
		setupUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown setup target: %s\n", args[0])
		setupUsage()
		os.Exit(1)
	}
}

func setupUsage() {
	fmt.Println("Usage: flatrun-agent setup <target> <service> [options]")
	fmt.Println()
	fmt.Println("Targets:")
	fmt.Println("  infra <service>   Deploy an infrastructure service from embedded templates")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  --config, -c      Path to configuration file")
	fmt.Println("  --force           Re-deploy even if already exists")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  flatrun-agent setup infra nginx")
	fmt.Println("  flatrun-agent setup infra nginx --config /etc/flatrun/config.yml")
	fmt.Println("  flatrun-agent setup infra nginx --force")
}

func handleSetupInfra(args []string) {
	setupFlags := flag.NewFlagSet("setup infra", flag.ExitOnError)
	configPath := setupFlags.String("config", "", "Path to configuration file")
	shortConfig := setupFlags.String("c", "", "Path to configuration file (short)")
	force := setupFlags.Bool("force", false, "Re-deploy even if already exists")

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: flatrun-agent setup infra <service> [options]")
		fmt.Fprintln(os.Stderr)
		printAvailableInfraTemplates()
		os.Exit(1)
	}

	serviceName := args[0]
	if err := setupFlags.Parse(args[1:]); err != nil {
		os.Exit(1)
	}

	cfgPath := *configPath
	if cfgPath == "" {
		cfgPath = *shortConfig
	}

	templateID := "infra/" + serviceName

	meta, err := loadInfraMetadata(templateID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s is not a valid infrastructure template: %v\n", serviceName, err)
		fmt.Fprintln(os.Stderr)
		printAvailableInfraTemplates()
		os.Exit(1)
	}

	if meta.Type != "infrastructure" && meta.Category != "infrastructure" {
		fmt.Fprintf(os.Stderr, "Error: %s is not an infrastructure template (type=%s, category=%s)\n",
			serviceName, meta.Type, meta.Category)
		os.Exit(1)
	}

	resolvedConfigPath := config.FindConfigPath(cfgPath)
	cfg, err := config.Load(resolvedConfigPath)
	if err != nil {
		log.Fatalf("Failed to load config from %s: %v", resolvedConfigPath, err)
	}

	deployDir := filepath.Join(cfg.DeploymentsPath, serviceName)
	if !*force {
		if _, err := os.Stat(filepath.Join(deployDir, "docker-compose.yml")); err == nil {
			fmt.Printf("%s is already deployed at %s (use --force to re-deploy)\n", serviceName, deployDir)
			return
		}
	}

	if err := deployInfraService(cfg, serviceName, templateID); err != nil {
		log.Fatalf("Failed to setup %s: %v", serviceName, err)
	}
}

func loadInfraMetadata(templateID string) (*templateMetadata, error) {
	data, err := templates.GetMetadata(templateID)
	if err != nil {
		return nil, err
	}
	var meta templateMetadata
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func printAvailableInfraTemplates() {
	fmt.Fprintln(os.Stderr, "Available infrastructure services:")
	infraTemplates, err := templates.List()
	if err != nil {
		return
	}
	for _, id := range infraTemplates {
		if !strings.HasPrefix(id, "infra/") {
			continue
		}
		meta, err := loadInfraMetadata(id)
		if err != nil {
			continue
		}
		name := strings.TrimPrefix(id, "infra/")
		fmt.Fprintf(os.Stderr, "  %-18s %s\n", name, meta.Name)
	}
}

func deployInfraService(cfg *config.Config, serviceName, templateID string) error {
	deployDir := filepath.Join(cfg.DeploymentsPath, serviceName)

	fmt.Printf("Deploying %s infrastructure...\n", serviceName)

	compose, err := templates.GetCompose(templateID)
	if err != nil {
		return fmt.Errorf("read compose template: %w", err)
	}

	content := string(compose)
	content = strings.ReplaceAll(content, "${NAME}", serviceName)
	content = strings.ReplaceAll(content, "${PROXY_NETWORK}", cfg.Infrastructure.DefaultProxyNetwork)

	meta, err := loadInfraMetadata(templateID)
	if err != nil {
		return fmt.Errorf("load metadata: %w", err)
	}

	if err := writeSetupFiles(meta, templateID, deployDir); err != nil {
		return fmt.Errorf("write template files: %w", err)
	}

	manager := docker.NewManager(cfg.DeploymentsPath)
	if err := manager.CreateDeployment(serviceName, content, meta.Mounts.Files); err != nil {
		return fmt.Errorf("create deployment: %w", err)
	}

	netManager := networks.NewManager()
	if err := netManager.EnsureNetwork(cfg.Infrastructure.DefaultProxyNetwork); err != nil {
		return fmt.Errorf("ensure proxy network: %w", err)
	}

	executor := docker.NewComposeExecutor(cfg.DeploymentsPath)
	if output, err := executor.Up(deployDir); err != nil {
		return fmt.Errorf("start service: %s: %w", output, err)
	}

	fmt.Printf("%s infrastructure deployed\n", serviceName)
	return nil
}

func writeSetupFiles(meta *templateMetadata, templateID, deployDir string) error {
	for _, dir := range meta.Setup.Dirs {
		if err := os.MkdirAll(filepath.Join(deployDir, dir), 0755); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}

	for _, f := range meta.Setup.Files {
		destPath := filepath.Join(deployDir, f.Dest)

		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return fmt.Errorf("create parent dir for %s: %w", f.Dest, err)
		}

		if !f.Overwrite {
			if _, err := os.Stat(destPath); err == nil {
				continue
			}
		}

		if f.Empty {
			if err := os.WriteFile(destPath, []byte(""), 0644); err != nil {
				return fmt.Errorf("write %s: %w", f.Dest, err)
			}
			continue
		}

		var data []byte
		var err error
		if f.External {
			data, err = templates.FS.ReadFile(f.Src)
		} else {
			data, err = templates.GetFile(templateID, f.Src)
		}
		if err != nil {
			return fmt.Errorf("read template file %s: %w", f.Src, err)
		}

		if err := os.WriteFile(destPath, data, 0644); err != nil {
			return fmt.Errorf("write %s: %w", f.Dest, err)
		}
	}

	return nil
}

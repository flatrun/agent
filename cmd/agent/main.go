package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/flatrun/agent/internal/api"
	"github.com/flatrun/agent/internal/watcher"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/updater"
	"github.com/flatrun/agent/pkg/version"
	"github.com/moby/moby/client"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "update":
			handleUpdate(os.Args[2:])
			return
		case "setup":
			handleSetup(os.Args[2:])
			return
		case "version":
			printVersion()
			return
		}
	}

	configPath := flag.String("config", "", "Path to configuration file")
	showVersion := flag.Bool("version", false, "Print version information")
	flag.Parse()

	if *showVersion {
		printVersion()
		return
	}

	resolvedConfigPath := config.FindConfigPath(*configPath)
	cfg, err := config.Load(resolvedConfigPath)
	if err != nil {
		log.Fatalf("Failed to load config from %s: %v", resolvedConfigPath, err)
	}

	ensureDockerReachable(cfg.DockerSocket)

	if err := os.MkdirAll(cfg.DeploymentsPath, 0755); err != nil {
		log.Fatalf("Failed to create deployments directory '%s': %v", cfg.DeploymentsPath, err)
	}

	log.Printf("Starting Flatrun Agent v%s", version.Version)
	log.Printf("Config loaded from: %s", resolvedConfigPath)
	log.Printf("Deployments path: %s", cfg.DeploymentsPath)
	log.Printf("API listening on: %s:%d", cfg.API.Host, cfg.API.Port)

	fileWatcher, err := watcher.New(cfg.DeploymentsPath)
	if err != nil {
		log.Fatalf("Failed to create file watcher: %v", err)
	}
	defer fileWatcher.Close()

	go fileWatcher.Start()

	apiServer := api.New(cfg, resolvedConfigPath)
	go func() {
		if err := apiServer.Start(); err != nil {
			log.Fatalf("Failed to start API server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down Flatrun Agent...")
	_ = apiServer.Stop()
}

func ensureDockerReachable(dockerHost string) {
	log.Println("Checking if Docker is reachable...")

	opts := []client.Opt{client.FromEnv}
	if dockerHost != "" {
		opts = append(opts, client.WithHost(dockerHost))
	}

	cli, err := client.New(opts...)
	if err != nil {
		log.Fatalf("Failed to create Docker client: %v", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := cli.Ping(ctx, client.PingOptions{}); err != nil {
		log.Fatalf("Docker is not reachable: %v. "+
			"Ensure the Docker daemon is running and the socket "+
			"in config is correct.", err)
	}
	log.Println("Docker is reachable")
}

func printVersion() {
	info := version.Get()
	fmt.Printf("Flatrun Agent\n")
	fmt.Printf("Version:    %s\n", info.Version)
	fmt.Printf("Build Time: %s\n", info.BuildTime)
	fmt.Printf("Git Commit: %s\n", info.GitCommit)
}

func handleUpdate(args []string) {
	updateFlags := flag.NewFlagSet("update", flag.ExitOnError)
	checkOnly := updateFlags.Bool("check", false, "Check for updates without installing")
	force := updateFlags.Bool("force", false, "Force update even if on latest version")
	restart := updateFlags.Bool("restart", false, "Restart service after update")
	rollback := updateFlags.Bool("rollback", false, "Rollback to previous version")

	updateFlags.Usage = func() {
		fmt.Println("Usage: flatrun-agent update [options]")
		fmt.Println()
		fmt.Println("Options:")
		updateFlags.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  flatrun-agent update --check     Check for available updates")
		fmt.Println("  flatrun-agent update             Download and install latest version")
		fmt.Println("  flatrun-agent update --restart   Update and restart the service")
		fmt.Println("  flatrun-agent update --rollback  Rollback to previous version")
	}

	if err := updateFlags.Parse(args); err != nil {
		os.Exit(1)
	}

	if *rollback {
		fmt.Println("Rolling back to previous version...")
		if err := updater.Rollback(); err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Rollback successful")
		fmt.Println("Restart the service to apply: sudo systemctl restart flatrun-agent")
		return
	}

	if *checkOnly {
		result, err := updater.CheckForUpdate()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Current version: %s\n", result.CurrentVersion)
		fmt.Printf("Latest version:  %s\n", result.LatestVersion)
		if result.UpdateAvailable {
			fmt.Println("Update available!")
			fmt.Println("Run 'flatrun-agent update' to install")
		} else {
			fmt.Println("Already up to date")
		}
		return
	}

	result, err := updater.Update(*force)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	if !result.UpdateAvailable && !*force {
		fmt.Printf("Already up to date (v%s)\n", result.CurrentVersion)
		return
	}

	if result.Installed {
		fmt.Printf("Successfully updated to v%s\n", result.LatestVersion)

		if *restart {
			fmt.Println("Restarting service...")
			if err := updater.RestartService(); err != nil {
				fmt.Printf("Failed to restart service: %v\n", err)
				fmt.Println("Please restart manually: sudo systemctl restart flatrun-agent")
				os.Exit(1)
			}
			fmt.Println("Service restarted successfully")
		} else {
			fmt.Println("Restart the service to apply: sudo systemctl restart flatrun-agent")
		}
	}
}

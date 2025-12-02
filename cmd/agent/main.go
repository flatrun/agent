package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/flatrun/agent/internal/api"
	"github.com/flatrun/agent/internal/watcher"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/version"
)

func main() {
	configPath := flag.String("config", "config.yml", "Path to configuration file")
	showVersion := flag.Bool("version", false, "Print version information")
	flag.Parse()

	if *showVersion {
		info := version.Get()
		fmt.Printf("Flatrun Agent\n")
		fmt.Printf("Version:    %s\n", info.Version)
		fmt.Printf("Build Time: %s\n", info.BuildTime)
		fmt.Printf("Git Commit: %s\n", info.GitCommit)
		os.Exit(0)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	log.Printf("Starting Flatrun Agent v%s", version.Version)
	log.Printf("Deployments path: %s", cfg.DeploymentsPath)
	log.Printf("API listening on: %s:%d", cfg.API.Host, cfg.API.Port)

	fileWatcher, err := watcher.New(cfg.DeploymentsPath)
	if err != nil {
		log.Fatalf("Failed to create file watcher: %v", err)
	}
	defer fileWatcher.Close()

	go fileWatcher.Start()

	apiServer := api.New(cfg)
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

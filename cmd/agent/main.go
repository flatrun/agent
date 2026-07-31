package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/flatrun/agent/internal/api"
	"github.com/flatrun/agent/internal/observ"
	"github.com/flatrun/agent/internal/watcher"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/updater"
	"github.com/flatrun/agent/pkg/version"
	"github.com/moby/moby/client"
	"github.com/spf13/cobra"
)

func main() {
	root := newRootCmd()
	root.SetArgs(normalizeLegacyFlags(os.Args[1:]))
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// normalizeLegacyFlags keeps the single-dash long forms the previous flag-based
// CLI accepted (-config, -version) working under pflag, which otherwise reads a
// single dash as a cluster of shorthand flags. This preserves existing launch
// commands such as `flatrun-agent -config /etc/flatrun/config.yml`.
func normalizeLegacyFlags(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a == "-config" || a == "-version" ||
			strings.HasPrefix(a, "-config=") || strings.HasPrefix(a, "-version=") {
			a = "-" + a
		}
		out = append(out, a)
	}
	return out
}

func newRootCmd() *cobra.Command {
	var configPath string
	var showVersion bool

	root := &cobra.Command{
		Use:          "flatrun-agent",
		Short:        "Flatrun Agent - flat-file container orchestration",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if showVersion {
				printVersion()
				return nil
			}
			// A bare invocation is most likely someone looking for guidance,
			// not an attempt to start the daemon: the service is always launched
			// with --config. Show help rather than failing on a missing default
			// config. Pass --config (or use `serve`) to actually start.
			if !cmd.Flags().Changed("config") {
				return cmd.Help()
			}
			return runServer(configPath)
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentFlags().StringVar(&configPath, "config", "", "Path to configuration file")
	root.Flags().BoolVar(&showVersion, "version", false, "Print version information")

	serve := &cobra.Command{
		Use:          "serve",
		Short:        "Start the agent server",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServer(configPath)
		},
	}

	setup := &cobra.Command{
		Use:                "setup <target> <service> [options]",
		Short:              "Deploy an infrastructure service from embedded templates",
		DisableFlagParsing: true,
		Run: func(cmd *cobra.Command, args []string) {
			handleSetup(args)
		},
	}

	update := &cobra.Command{
		Use:                "update [options]",
		Short:              "Update the agent to the latest version",
		DisableFlagParsing: true,
		Run: func(cmd *cobra.Command, args []string) {
			handleUpdate(args)
		},
	}

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			printVersion()
		},
	}

	observPlugin := &cobra.Command{
		Use:          "observ-plugin",
		Short:        "Run the built-in observability plugin (launched by the agent)",
		Hidden:       true,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return observ.RunPlugin()
		},
	}

	root.AddCommand(serve, setup, update, versionCmd, observPlugin)
	return root
}

func runServer(configPath string) error {
	resolvedConfigPath := config.FindConfigPath(configPath)
	cfg, err := config.Load(resolvedConfigPath)
	if err != nil {
		return fmt.Errorf("failed to load config from %s: %w", resolvedConfigPath, err)
	}

	if err := ensureDockerReachable(cfg.DockerSocket); err != nil {
		return err
	}

	if err := os.MkdirAll(cfg.DeploymentsPath, 0755); err != nil {
		return fmt.Errorf("failed to create deployments directory %q: %w", cfg.DeploymentsPath, err)
	}

	log.Printf("Starting Flatrun Agent v%s", version.Version)
	log.Printf("Config loaded from: %s", resolvedConfigPath)
	log.Printf("Deployments path: %s", cfg.DeploymentsPath)
	log.Printf("API listening on: %s:%d", cfg.API.Host, cfg.API.Port)

	fileWatcher, err := watcher.New(cfg.DeploymentsPath)
	if err != nil {
		return fmt.Errorf("failed to create file watcher: %w", err)
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
	return nil
}

func ensureDockerReachable(dockerHost string) error {
	log.Println("Checking if Docker is reachable...")

	opts := []client.Opt{client.FromEnv}
	if dockerHost != "" {
		opts = append(opts, client.WithHost(dockerHost))
	}

	cli, err := client.New(opts...)
	if err != nil {
		return fmt.Errorf("failed to create Docker client: %w", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := cli.Ping(ctx, client.PingOptions{}); err != nil {
		return fmt.Errorf("docker is not reachable: %w. Ensure the Docker daemon is running and the socket in config is correct", err)
	}
	log.Println("Docker is reachable")
	return nil
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
	prerelease := updateFlags.Bool("prerelease", false, "Include prereleases (beta) when checking for updates")

	updateFlags.Usage = func() {
		fmt.Println("Usage: flatrun-agent update [options]")
		fmt.Println()
		fmt.Println("Options:")
		updateFlags.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  flatrun-agent update --check       Check for available updates")
		fmt.Println("  flatrun-agent update               Download and install latest version")
		fmt.Println("  flatrun-agent update --prerelease  Include beta releases")
		fmt.Println("  flatrun-agent update --restart     Update and restart the service")
		fmt.Println("  flatrun-agent update --rollback    Rollback to previous version")
	}

	if err := updateFlags.Parse(args); err != nil {
		os.Exit(1)
	}

	channel := updater.ChannelStable
	if *prerelease {
		channel = updater.ChannelPrerelease
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
		result, err := updater.CheckForUpdate(channel)
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

	result, err := updater.Update(*force, channel)
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

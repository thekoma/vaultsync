package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	// 1. Load config.
	cfg, err := LoadConfig()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// 2. Set up structured JSON logger with configured level.
	var level slog.Level
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	// 3. Log startup info.
	logger.Info("vaultsync starting",
		"vault_addr", cfg.VaultAddr,
		"poll_interval", cfg.PollInterval.String(),
		"dry_run", cfg.DryRun,
	)

	// 4. Create Kubernetes clients.
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		logger.Error("failed to get in-cluster config", "error", err)
		os.Exit(1)
	}

	k8sClient, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		logger.Error("failed to create kubernetes client", "error", err)
		os.Exit(1)
	}

	dynClient, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		logger.Error("failed to create dynamic client", "error", err)
		os.Exit(1)
	}

	// 5. Create Vault watcher.
	vault, err := NewVaultWatcher(*cfg, logger)
	if err != nil {
		logger.Error("failed to create vault watcher", "error", err)
		os.Exit(1)
	}

	// 6. Create all components.
	state := NewStateStore(k8sClient, *cfg, logger)
	discovery := NewDiscovery(k8sClient, dynClient, logger)
	refresher := NewRefresher(k8sClient, dynClient, *cfg, logger)
	controller := NewController(vault, state, discovery, refresher, logger)

	// 7. Signal handling.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 8. Poll loop — reconcile immediately, then on each tick.
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	// Run first reconciliation immediately.
	if err := controller.Reconcile(ctx); err != nil {
		logger.Error("reconcile failed", "error", err)
	}

	for {
		select {
		case <-ctx.Done():
			logger.Info("shutting down")
			return
		case <-ticker.C:
			if err := controller.Reconcile(ctx); err != nil {
				logger.Error("reconcile failed", "error", err)
			}
		}
	}
}

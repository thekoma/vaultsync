package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Config holds all configuration for the vaultsync controller.
type Config struct {
	VaultAddr      string
	VaultRole      string
	VaultMount     string
	VaultAuthMount string
	VaultSkipTLS   bool
	PollInterval   time.Duration
	StateNamespace string
	StateConfigMap string
	DryRun         bool
	LogLevel       string
}

// LoadConfig reads configuration from environment variables.
// Returns an error if required fields are missing.
func LoadConfig() (*Config, error) {
	addr := os.Getenv("VAULT_ADDR")
	if addr == "" {
		return nil, fmt.Errorf("VAULT_ADDR is required")
	}

	interval, err := time.ParseDuration(envOr("POLL_INTERVAL", "60s"))
	if err != nil {
		return nil, fmt.Errorf("invalid POLL_INTERVAL: %w", err)
	}

	return &Config{
		VaultAddr:      addr,
		VaultRole:      envOr("VAULT_ROLE", "vaultsync"),
		VaultMount:     envOr("VAULT_MOUNT", "secret"),
		VaultAuthMount: envOr("VAULT_AUTH_MOUNT", "kubernetes"),
		VaultSkipTLS:   envBool("VAULT_SKIP_VERIFY"),
		PollInterval:   interval,
		StateNamespace: envOr("STATE_NAMESPACE", "vaultsync"),
		StateConfigMap: envOr("STATE_CONFIGMAP", "vaultsync-state"),
		DryRun:         envBool("DRY_RUN"),
		LogLevel:       envOr("LOG_LEVEL", "info"),
	}, nil
}

// envOr returns the value of the environment variable named by key,
// or fallback if the variable is not set or empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envBool returns true if the environment variable named by key
// is set to "true", "1", or "yes" (case-insensitive).
func envBool(key string) bool {
	v := strings.ToLower(os.Getenv(key))
	return v == "true" || v == "1" || v == "yes"
}

package main

import (
	"os"
	"testing"
	"time"
)

func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"VAULT_ADDR", "VAULT_ROLE", "VAULT_MOUNT", "VAULT_AUTH_MOUNT",
		"VAULT_SKIP_VERIFY", "POLL_INTERVAL", "STATE_NAMESPACE",
		"STATE_CONFIGMAP", "ARGOCD_NAMESPACE", "DRY_RUN", "LOG_LEVEL",
	} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	clearEnv(t)
	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected error when VAULT_ADDR is missing, got nil")
	}
}

func TestLoadConfigFromEnv(t *testing.T) {
	clearEnv(t)

	t.Setenv("VAULT_ADDR", "https://vault.example.com")
	t.Setenv("VAULT_ROLE", "myrole")
	t.Setenv("VAULT_MOUNT", "kv")
	t.Setenv("VAULT_AUTH_MOUNT", "jwt")
	t.Setenv("VAULT_SKIP_VERIFY", "true")
	t.Setenv("POLL_INTERVAL", "30s")
	t.Setenv("STATE_NAMESPACE", "ops")
	t.Setenv("STATE_CONFIGMAP", "my-state")
	t.Setenv("ARGOCD_NAMESPACE", "argo-system")
	t.Setenv("DRY_RUN", "true")
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.VaultAddr != "https://vault.example.com" {
		t.Errorf("VaultAddr = %q, want %q", cfg.VaultAddr, "https://vault.example.com")
	}
	if cfg.VaultRole != "myrole" {
		t.Errorf("VaultRole = %q, want %q", cfg.VaultRole, "myrole")
	}
	if cfg.VaultMount != "kv" {
		t.Errorf("VaultMount = %q, want %q", cfg.VaultMount, "kv")
	}
	if cfg.VaultAuthMount != "jwt" {
		t.Errorf("VaultAuthMount = %q, want %q", cfg.VaultAuthMount, "jwt")
	}
	if !cfg.VaultSkipTLS {
		t.Error("VaultSkipTLS = false, want true")
	}
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %v, want %v", cfg.PollInterval, 30*time.Second)
	}
	if cfg.StateNamespace != "ops" {
		t.Errorf("StateNamespace = %q, want %q", cfg.StateNamespace, "ops")
	}
	if cfg.StateConfigMap != "my-state" {
		t.Errorf("StateConfigMap = %q, want %q", cfg.StateConfigMap, "my-state")
	}
	if cfg.ArgoCDNamespace != "argo-system" {
		t.Errorf("ArgoCDNamespace = %q, want %q", cfg.ArgoCDNamespace, "argo-system")
	}
	if !cfg.DryRun {
		t.Error("DryRun = false, want true")
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "debug")
	}
}

func TestLoadConfigDefaultValues(t *testing.T) {
	clearEnv(t)

	t.Setenv("VAULT_ADDR", "https://vault.example.com")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.VaultRole != "vaultsync" {
		t.Errorf("VaultRole = %q, want %q", cfg.VaultRole, "vaultsync")
	}
	if cfg.VaultMount != "secret" {
		t.Errorf("VaultMount = %q, want %q", cfg.VaultMount, "secret")
	}
	if cfg.VaultAuthMount != "kubernetes" {
		t.Errorf("VaultAuthMount = %q, want %q", cfg.VaultAuthMount, "kubernetes")
	}
	if cfg.VaultSkipTLS {
		t.Error("VaultSkipTLS = true, want false")
	}
	if cfg.PollInterval != 60*time.Second {
		t.Errorf("PollInterval = %v, want %v", cfg.PollInterval, 60*time.Second)
	}
	if cfg.StateNamespace != "vaultsync" {
		t.Errorf("StateNamespace = %q, want %q", cfg.StateNamespace, "vaultsync")
	}
	if cfg.StateConfigMap != "vaultsync-state" {
		t.Errorf("StateConfigMap = %q, want %q", cfg.StateConfigMap, "vaultsync-state")
	}
	if cfg.ArgoCDNamespace != "argocd" {
		t.Errorf("ArgoCDNamespace = %q, want %q", cfg.ArgoCDNamespace, "argocd")
	}
	if cfg.DryRun {
		t.Error("DryRun = true, want false")
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
}

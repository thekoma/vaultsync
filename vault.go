package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	vault "github.com/hashicorp/vault/api"
	kubeauth "github.com/hashicorp/vault/api/auth/kubernetes"
)

// VaultWatcher provides methods for polling Vault KV v2 secret metadata.
type VaultWatcher interface {
	// ListSecretPaths recursively lists all secret paths under the configured KV v2 mount.
	ListSecretPaths(ctx context.Context) ([]string, error)

	// GetSecretVersion returns the current version number for a given secret path.
	GetSecretVersion(ctx context.Context, path string) (int, error)

	// GetAllVersions returns a map of all secret paths to their current versions.
	// Individual errors are logged as warnings and the path is skipped.
	GetAllVersions(ctx context.Context) (map[string]int, error)

	// GetVersionsForPaths returns versions only for the specified paths.
	GetVersionsForPaths(ctx context.Context, paths []string) (map[string]int, error)
}

// vaultWatcher is the production implementation of VaultWatcher backed by a real Vault client.
type vaultWatcher struct {
	client  *vault.Client
	mount   string
	logger  *slog.Logger
	k8sAuth *kubeauth.KubernetesAuth
}

// NewVaultWatcher creates a new VaultWatcher that connects to Vault using
// Kubernetes auth and polls KV v2 metadata.
func NewVaultWatcher(cfg Config, logger *slog.Logger) (VaultWatcher, error) {
	vaultCfg := vault.DefaultConfig()
	vaultCfg.Address = cfg.VaultAddr

	if cfg.VaultSkipTLS {
		transport := vaultCfg.HttpClient.Transport.(*http.Transport)
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{}
		}
		transport.TLSClientConfig.InsecureSkipVerify = true
	}

	client, err := vault.NewClient(vaultCfg)
	if err != nil {
		return nil, fmt.Errorf("creating vault client: %w", err)
	}

	k8sAuth, err := kubeauth.NewKubernetesAuth(
		cfg.VaultRole,
		kubeauth.WithMountPath(cfg.VaultAuthMount),
	)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes auth: %w", err)
	}

	authInfo, err := client.Auth().Login(context.Background(), k8sAuth)
	if err != nil {
		return nil, fmt.Errorf("vault kubernetes login: %w", err)
	}
	if authInfo == nil {
		return nil, fmt.Errorf("vault kubernetes login returned nil auth info")
	}

	logger.Info("authenticated to vault",
		"addr", cfg.VaultAddr,
		"mount", cfg.VaultMount,
	)

	return &vaultWatcher{
		client:  client,
		mount:   cfg.VaultMount,
		logger:  logger,
		k8sAuth: k8sAuth,
	}, nil
}

// reauth re-authenticates to Vault using Kubernetes auth if the current token
// is near expiry or already expired. This prevents silent failures after the
// initial token's TTL (default 1h) expires.
func (w *vaultWatcher) reauth(ctx context.Context) error {
	// Check if the current token is still valid with sufficient remaining TTL.
	secret, err := w.client.Auth().Token().LookupSelfWithContext(ctx)
	if err == nil && secret != nil {
		ttl, _ := secret.TokenTTL()
		if ttl > 30 { // More than 30 seconds remaining — no need to re-auth.
			return nil
		}
	}

	// Token is expired, near expiry, or lookup failed — re-authenticate.
	w.logger.Info("re-authenticating to vault")
	authInfo, err := w.client.Auth().Login(ctx, w.k8sAuth)
	if err != nil {
		return fmt.Errorf("vault re-authentication: %w", err)
	}
	if authInfo == nil {
		return fmt.Errorf("vault re-authentication returned nil auth info")
	}
	w.logger.Info("re-authenticated to vault successfully")
	return nil
}

// ListSecretPaths recursively lists all secret paths under the KV v2 metadata prefix.
func (w *vaultWatcher) ListSecretPaths(ctx context.Context) ([]string, error) {
	var paths []string
	err := w.listRecursive(ctx, "", &paths)
	if err != nil {
		return nil, err
	}
	return paths, nil
}

// listRecursive walks the metadata tree starting from prefix, appending leaf paths to result.
func (w *vaultWatcher) listRecursive(ctx context.Context, prefix string, result *[]string) error {
	listPath := fmt.Sprintf("%s/metadata/%s", w.mount, prefix)

	secret, err := w.client.Logical().ListWithContext(ctx, listPath)
	if err != nil {
		return fmt.Errorf("listing %s: %w", listPath, err)
	}
	if secret == nil || secret.Data == nil {
		return nil
	}

	keysRaw, ok := secret.Data["keys"]
	if !ok {
		return nil
	}

	keys, ok := keysRaw.([]interface{})
	if !ok {
		return fmt.Errorf("unexpected type for keys at %s: %T", listPath, keysRaw)
	}

	for _, k := range keys {
		key, ok := k.(string)
		if !ok {
			continue
		}

		fullPath := prefix + key
		if strings.HasSuffix(key, "/") {
			// Directory: recurse into it.
			if err := w.listRecursive(ctx, fullPath, result); err != nil {
				return err
			}
		} else {
			// Leaf secret.
			*result = append(*result, fullPath)
		}
	}

	return nil
}

// GetSecretVersion returns the current version of a secret by reading its KV v2 metadata.
func (w *vaultWatcher) GetSecretVersion(ctx context.Context, path string) (int, error) {
	md, err := w.client.KVv2(w.mount).GetMetadata(ctx, path)
	if err != nil {
		return 0, fmt.Errorf("getting metadata for %s: %w", path, err)
	}
	return md.CurrentVersion, nil
}

// GetAllVersions lists all secrets then fetches each version, skipping errors with a warning.
// Re-authenticates to Vault before each call to handle token expiry.
func (w *vaultWatcher) GetAllVersions(ctx context.Context) (map[string]int, error) {
	if err := w.reauth(ctx); err != nil {
		return nil, err
	}

	paths, err := w.ListSecretPaths(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing secret paths: %w", err)
	}

	versions := make(map[string]int, len(paths))
	for _, p := range paths {
		v, err := w.GetSecretVersion(ctx, p)
		if err != nil {
			w.logger.Warn("skipping secret version lookup",
				"path", p,
				"error", err,
			)
			continue
		}
		versions[p] = v
	}

	return versions, nil
}

// GetVersionsForPaths fetches versions only for the specified paths, avoiding a full
// recursive listing of all secrets in Vault.
// Re-authenticates to Vault before each call to handle token expiry.
func (w *vaultWatcher) GetVersionsForPaths(ctx context.Context, paths []string) (map[string]int, error) {
	if err := w.reauth(ctx); err != nil {
		return nil, err
	}

	versions := make(map[string]int, len(paths))
	for _, p := range paths {
		v, err := w.GetSecretVersion(ctx, p)
		if err != nil {
			w.logger.Warn("skipping secret version lookup",
				"path", p,
				"error", err,
			)
			continue
		}
		versions[p] = v
	}

	return versions, nil
}

# vaultSync Controller — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Kubernetes controller that detects HashiCorp Vault KV v2 secret version changes and refreshes the affected ArgoCD-managed resources, closing the rotation gap in a Bank-Vaults webhook setup.

**Architecture:** A single Go binary deployed as a Deployment. Polls Vault metadata for version changes, maintains state in a ConfigMap, discovers affected K8s resources via annotations, and refreshes them (delete for Secrets/ConfigMaps, finalizer-removal+delete for Application CRs). ArgoCD's selfHeal re-creates deleted resources from git, triggering webhook re-injection with new values.

**Tech Stack:** Go 1.26, `github.com/hashicorp/vault/api` (Vault client + Kubernetes auth), `k8s.io/client-go` (K8s API), standard library `log/slog` for structured logging.

---

## File Structure

```
~/src/vaultSync/
├── main.go                  # Entry point: config parsing, client init, signal handling
├── config.go                # Config struct + env var loading
├── config_test.go
├── vault.go                 # VaultWatcher: connect, auth, recursive list, get versions
├── vault_test.go
├── state.go                 # StateStore: ConfigMap-backed version tracking
├── state_test.go
├── discovery.go             # Discovery: find annotated resources, build path→resource map
├── discovery_test.go
├── refresher.go             # Refresher: delete Secrets/ConfigMaps, recreate Application CRs
├── refresher_test.go
├── controller.go            # Controller: main reconciliation loop
├── controller_test.go
├── deploy/
│   ├── namespace.yaml       # vaultsync namespace
│   ├── rbac.yaml            # ServiceAccount + ClusterRole + ClusterRoleBinding
│   ├── deployment.yaml      # Deployment manifest
│   └── vault-role.yaml      # Vault auth role config (to add to asgard-k8s vault.yaml)
├── Dockerfile
├── Makefile
├── go.mod
└── .gitignore
```

---

## Annotation Design

Resources opt-in to monitoring via a single annotation:

```yaml
annotations:
  vaultsync/watch: "secret/data/litellm"
```

Multiple vault paths (comma-separated):

```yaml
annotations:
  vaultsync/watch: "secret/data/wasabi-backup,secret/data/airvpn/qbittorrent"
```

The controller infers the refresh strategy from the resource Kind:
- **Secret, ConfigMap**: delete the resource → ArgoCD re-creates from git → webhook re-injects
- **Application** (`argoproj.io/v1alpha1`): remove `resources-finalizer.argocd.argoproj.io` finalizer → delete → parent app (`applications-include` with `selfHeal: true`) re-creates from git → webhook re-mutates with new values → child app syncs

---

## Configuration (environment variables)

| Variable | Default | Description |
|----------|---------|-------------|
| `VAULT_ADDR` | (required) | Vault server URL, e.g. `https://vault.vault.svc.cluster.local:8200` |
| `VAULT_ROLE` | `vaultsync` | Vault Kubernetes auth role |
| `VAULT_MOUNT` | `secret` | KV v2 mount path |
| `VAULT_AUTH_MOUNT` | `kubernetes` | Vault Kubernetes auth mount path |
| `VAULT_SKIP_VERIFY` | `false` | Skip TLS verification |
| `POLL_INTERVAL` | `60s` | How often to poll Vault for changes |
| `STATE_NAMESPACE` | `vaultsync` | Namespace for state ConfigMap |
| `STATE_CONFIGMAP` | `vaultsync-state` | ConfigMap name for version state |
| `DRY_RUN` | `false` | Log actions without deleting resources |
| `LOG_LEVEL` | `info` | Logging level: debug, info, warn, error |

---

### Task 1: Project Scaffolding

**Files:**
- Create: `go.mod`
- Create: `Makefile`
- Create: `.gitignore`
- Create: `main.go` (minimal placeholder)

- [ ] **Step 1: Initialize git and Go module**

```bash
cd ~/src/vaultSync
git init
go mod init github.com/thekoma/vaultsync
```

- [ ] **Step 2: Create .gitignore**

```gitignore
# Binaries
vaultsync
*.exe
*.dll
*.so
*.dylib

# Test
*.test
*.out
coverage.html

# IDE
.idea/
.vscode/
*.swp

# OS
.DS_Store
```

- [ ] **Step 3: Create Makefile**

```makefile
BINARY := vaultsync
IMAGE  := ghcr.io/thekoma/vaultsync

.PHONY: build test lint docker run clean

build:
	go build -o $(BINARY) .

test:
	go test -v -race -cover ./...

lint:
	go vet ./...

docker:
	docker build -t $(IMAGE):latest .

run: build
	./$(BINARY)

clean:
	rm -f $(BINARY)
```

- [ ] **Step 4: Create minimal main.go**

```go
package main

import "fmt"

func main() {
	fmt.Println("vaultsync starting")
}
```

- [ ] **Step 5: Verify build**

Run: `cd ~/src/vaultSync && make build`
Expected: binary `vaultsync` created, no errors

- [ ] **Step 6: Commit**

```bash
git add .
git commit -m "feat: project scaffolding"
```

---

### Task 2: Configuration

**Files:**
- Create: `config.go`
- Create: `config_test.go`

- [ ] **Step 1: Write the failing test**

```go
// config_test.go
package main

import (
	"testing"
	"time"
)

func TestLoadConfigDefaults(t *testing.T) {
	// Clear env to test defaults
	for _, key := range []string{
		"VAULT_ADDR", "VAULT_ROLE", "VAULT_MOUNT", "VAULT_AUTH_MOUNT",
		"VAULT_SKIP_VERIFY", "POLL_INTERVAL", "STATE_NAMESPACE",
		"STATE_CONFIGMAP", "DRY_RUN", "LOG_LEVEL",
	} {
		t.Setenv(key, "")
	}

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected error when VAULT_ADDR is missing")
	}
}

func TestLoadConfigFromEnv(t *testing.T) {
	t.Setenv("VAULT_ADDR", "https://vault:8200")
	t.Setenv("VAULT_ROLE", "myrole")
	t.Setenv("VAULT_MOUNT", "kv")
	t.Setenv("POLL_INTERVAL", "30s")
	t.Setenv("DRY_RUN", "true")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.VaultAddr != "https://vault:8200" {
		t.Errorf("VaultAddr = %q, want %q", cfg.VaultAddr, "https://vault:8200")
	}
	if cfg.VaultRole != "myrole" {
		t.Errorf("VaultRole = %q, want %q", cfg.VaultRole, "myrole")
	}
	if cfg.VaultMount != "kv" {
		t.Errorf("VaultMount = %q, want %q", cfg.VaultMount, "kv")
	}
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %v, want %v", cfg.PollInterval, 30*time.Second)
	}
	if !cfg.DryRun {
		t.Error("DryRun = false, want true")
	}
}

func TestLoadConfigDefaultValues(t *testing.T) {
	t.Setenv("VAULT_ADDR", "https://vault:8200")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.VaultRole != "vaultsync" {
		t.Errorf("default VaultRole = %q, want %q", cfg.VaultRole, "vaultsync")
	}
	if cfg.VaultMount != "secret" {
		t.Errorf("default VaultMount = %q, want %q", cfg.VaultMount, "secret")
	}
	if cfg.VaultAuthMount != "kubernetes" {
		t.Errorf("default VaultAuthMount = %q, want %q", cfg.VaultAuthMount, "kubernetes")
	}
	if cfg.PollInterval != 60*time.Second {
		t.Errorf("default PollInterval = %v, want %v", cfg.PollInterval, 60*time.Second)
	}
	if cfg.StateNamespace != "vaultsync" {
		t.Errorf("default StateNamespace = %q, want %q", cfg.StateNamespace, "vaultsync")
	}
	if cfg.StateConfigMap != "vaultsync-state" {
		t.Errorf("default StateConfigMap = %q, want %q", cfg.StateConfigMap, "vaultsync-state")
	}
	if cfg.DryRun {
		t.Error("default DryRun = true, want false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v -run TestLoadConfig ./...`
Expected: FAIL — `LoadConfig` not defined

- [ ] **Step 3: Implement config.go**

```go
// config.go
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

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

func LoadConfig() (Config, error) {
	cfg := Config{
		VaultAddr:      os.Getenv("VAULT_ADDR"),
		VaultRole:      envOr("VAULT_ROLE", "vaultsync"),
		VaultMount:     envOr("VAULT_MOUNT", "secret"),
		VaultAuthMount: envOr("VAULT_AUTH_MOUNT", "kubernetes"),
		VaultSkipTLS:   envBool("VAULT_SKIP_VERIFY"),
		StateNamespace: envOr("STATE_NAMESPACE", "vaultsync"),
		StateConfigMap: envOr("STATE_CONFIGMAP", "vaultsync-state"),
		DryRun:         envBool("DRY_RUN"),
		LogLevel:       envOr("LOG_LEVEL", "info"),
	}

	if cfg.VaultAddr == "" {
		return Config{}, fmt.Errorf("VAULT_ADDR is required")
	}

	interval := envOr("POLL_INTERVAL", "60s")
	dur, err := time.ParseDuration(interval)
	if err != nil {
		return Config{}, fmt.Errorf("invalid POLL_INTERVAL %q: %w", interval, err)
	}
	cfg.PollInterval = dur

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string) bool {
	v, _ := strconv.ParseBool(os.Getenv(key))
	return v
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -v -run TestLoadConfig ./...`
Expected: all 3 tests PASS

- [ ] **Step 5: Commit**

```bash
git add config.go config_test.go
git commit -m "feat: configuration loading from env vars"
```

---

### Task 3: Vault Watcher

**Files:**
- Create: `vault.go`
- Create: `vault_test.go`
- Modify: `go.mod` (add vault/api dependency)

- [ ] **Step 1: Add vault dependency**

```bash
cd ~/src/vaultSync
go get github.com/hashicorp/vault/api
go get github.com/hashicorp/vault/api/auth/kubernetes
```

- [ ] **Step 2: Write the failing test**

```go
// vault_test.go
package main

import (
	"context"
	"testing"
)

// fakeVaultWatcher implements VaultWatcher for testing
type fakeVaultWatcher struct {
	paths    []string
	versions map[string]int
	err      error
}

func (f *fakeVaultWatcher) ListSecretPaths(ctx context.Context) ([]string, error) {
	return f.paths, f.err
}

func (f *fakeVaultWatcher) GetSecretVersion(ctx context.Context, path string) (int, error) {
	v, ok := f.versions[path]
	if !ok {
		return 0, f.err
	}
	return v, nil
}

func (f *fakeVaultWatcher) GetAllVersions(ctx context.Context) (map[string]int, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.versions, nil
}

func TestVaultWatcherInterface(t *testing.T) {
	w := &fakeVaultWatcher{
		paths: []string{"litellm", "wasabi-backup", "airvpn/qbittorrent"},
		versions: map[string]int{
			"litellm":              3,
			"wasabi-backup":        7,
			"airvpn/qbittorrent":   2,
		},
	}

	ctx := context.Background()

	paths, err := w.ListSecretPaths(ctx)
	if err != nil {
		t.Fatalf("ListSecretPaths: %v", err)
	}
	if len(paths) != 3 {
		t.Errorf("got %d paths, want 3", len(paths))
	}

	v, err := w.GetSecretVersion(ctx, "litellm")
	if err != nil {
		t.Fatalf("GetSecretVersion: %v", err)
	}
	if v != 3 {
		t.Errorf("litellm version = %d, want 3", v)
	}

	all, err := w.GetAllVersions(ctx)
	if err != nil {
		t.Fatalf("GetAllVersions: %v", err)
	}
	if all["wasabi-backup"] != 7 {
		t.Errorf("wasabi-backup version = %d, want 7", all["wasabi-backup"])
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test -v -run TestVaultWatcher ./...`
Expected: FAIL — `VaultWatcher` type not defined

- [ ] **Step 4: Implement vault.go**

```go
// vault.go
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

// VaultWatcher polls Vault KV v2 metadata for secret version changes.
type VaultWatcher interface {
	ListSecretPaths(ctx context.Context) ([]string, error)
	GetSecretVersion(ctx context.Context, path string) (int, error)
	GetAllVersions(ctx context.Context) (map[string]int, error)
}

type vaultWatcher struct {
	client *vault.Client
	mount  string
	logger *slog.Logger
}

func NewVaultWatcher(cfg Config, logger *slog.Logger) (VaultWatcher, error) {
	vaultCfg := vault.DefaultConfig()
	vaultCfg.Address = cfg.VaultAddr

	if cfg.VaultSkipTLS {
		vaultCfg.HttpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
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
		return nil, fmt.Errorf("vault login: %w", err)
	}
	if authInfo == nil {
		return nil, fmt.Errorf("vault login returned nil auth info")
	}

	logger.Info("authenticated to vault", "accessor", authInfo.Auth.Accessor)

	return &vaultWatcher{
		client: client,
		mount:  cfg.VaultMount,
		logger: logger,
	}, nil
}

func (w *vaultWatcher) ListSecretPaths(ctx context.Context) ([]string, error) {
	return w.listRecursive(ctx, "")
}

func (w *vaultWatcher) listRecursive(ctx context.Context, prefix string) ([]string, error) {
	path := w.mount + "/metadata/" + prefix

	secret, err := w.client.Logical().ListWithContext(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", path, err)
	}
	if secret == nil || secret.Data == nil {
		return nil, nil
	}

	keys, ok := secret.Data["keys"].([]interface{})
	if !ok {
		return nil, nil
	}

	var results []string
	for _, k := range keys {
		key := k.(string)
		if strings.HasSuffix(key, "/") {
			sub, err := w.listRecursive(ctx, prefix+key)
			if err != nil {
				return nil, err
			}
			results = append(results, sub...)
		} else {
			results = append(results, prefix+key)
		}
	}
	return results, nil
}

func (w *vaultWatcher) GetSecretVersion(ctx context.Context, path string) (int, error) {
	kv := w.client.KVv2(w.mount)
	meta, err := kv.GetMetadata(ctx, path)
	if err != nil {
		return 0, fmt.Errorf("getting metadata for %s: %w", path, err)
	}
	return meta.CurrentVersion, nil
}

func (w *vaultWatcher) GetAllVersions(ctx context.Context) (map[string]int, error) {
	paths, err := w.ListSecretPaths(ctx)
	if err != nil {
		return nil, err
	}

	versions := make(map[string]int, len(paths))
	for _, p := range paths {
		v, err := w.GetSecretVersion(ctx, p)
		if err != nil {
			w.logger.Warn("skipping secret", "path", p, "error", err)
			continue
		}
		versions[p] = v
	}
	return versions, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -v -run TestVaultWatcher ./...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add vault.go vault_test.go go.mod go.sum
git commit -m "feat: vault watcher with KV v2 metadata polling"
```

---

### Task 4: State Store

**Files:**
- Create: `state.go`
- Create: `state_test.go`

- [ ] **Step 1: Write the failing test**

```go
// state_test.go
package main

import (
	"context"
	"testing"
)

type fakeStateStore struct {
	versions map[string]int
	saved    bool
}

func (f *fakeStateStore) Load(ctx context.Context) (map[string]int, error) {
	if f.versions == nil {
		return map[string]int{}, nil
	}
	return f.versions, nil
}

func (f *fakeStateStore) Save(ctx context.Context, versions map[string]int) error {
	f.versions = versions
	f.saved = true
	return nil
}

func TestStateStoreInterface(t *testing.T) {
	store := &fakeStateStore{}
	ctx := context.Background()

	// Load empty state
	v, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(v) != 0 {
		t.Errorf("expected empty state, got %v", v)
	}

	// Save state
	newVersions := map[string]int{"litellm": 3, "wasabi-backup": 7}
	err = store.Save(ctx, newVersions)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Load back
	v, err = store.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if v["litellm"] != 3 || v["wasabi-backup"] != 7 {
		t.Errorf("loaded state = %v, want litellm=3, wasabi-backup=7", v)
	}
}

func TestDetectChanges(t *testing.T) {
	old := map[string]int{"litellm": 2, "wasabi-backup": 7}
	current := map[string]int{"litellm": 3, "wasabi-backup": 7, "new-secret": 1}

	changed := DetectChanges(old, current)

	if _, ok := changed["litellm"]; !ok {
		t.Error("expected litellm in changed set")
	}
	if _, ok := changed["new-secret"]; !ok {
		t.Error("expected new-secret in changed set")
	}
	if _, ok := changed["wasabi-backup"]; ok {
		t.Error("wasabi-backup should not be in changed set")
	}
	if len(changed) != 2 {
		t.Errorf("expected 2 changed, got %d", len(changed))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v -run "TestStateStore|TestDetectChanges" ./...`
Expected: FAIL — types not defined

- [ ] **Step 3: Implement state.go**

```go
// state.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// StateStore persists the last-known vault secret versions.
type StateStore interface {
	Load(ctx context.Context) (map[string]int, error)
	Save(ctx context.Context, versions map[string]int) error
}

type configMapStateStore struct {
	client    kubernetes.Interface
	namespace string
	name      string
	logger    *slog.Logger
}

func NewStateStore(client kubernetes.Interface, cfg Config, logger *slog.Logger) StateStore {
	return &configMapStateStore{
		client:    client,
		namespace: cfg.StateNamespace,
		name:      cfg.StateConfigMap,
		logger:    logger,
	}
}

const stateKey = "versions.json"

func (s *configMapStateStore) Load(ctx context.Context) (map[string]int, error) {
	cm, err := s.client.CoreV1().ConfigMaps(s.namespace).Get(ctx, s.name, metav1.GetOptions{})
	if err != nil {
		s.logger.Info("state configmap not found, starting fresh", "error", err)
		return map[string]int{}, nil
	}

	data, ok := cm.Data[stateKey]
	if !ok {
		return map[string]int{}, nil
	}

	var versions map[string]int
	if err := json.Unmarshal([]byte(data), &versions); err != nil {
		return nil, fmt.Errorf("parsing state: %w", err)
	}
	return versions, nil
}

func (s *configMapStateStore) Save(ctx context.Context, versions map[string]int) error {
	data, err := json.Marshal(versions)
	if err != nil {
		return fmt.Errorf("marshaling state: %w", err)
	}

	cm, err := s.client.CoreV1().ConfigMaps(s.namespace).Get(ctx, s.name, metav1.GetOptions{})
	if err != nil {
		// Create if not exists
		_, err = s.client.CoreV1().ConfigMaps(s.namespace).Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      s.name,
				Namespace: s.namespace,
			},
			Data: map[string]string{stateKey: string(data)},
		}, metav1.CreateOptions{})
		return err
	}

	// Update existing
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[stateKey] = string(data)
	_, err = s.client.CoreV1().ConfigMaps(s.namespace).Update(ctx, cm, metav1.UpdateOptions{})
	return err
}

// DetectChanges compares old vs current versions and returns paths that changed.
func DetectChanges(old, current map[string]int) map[string]int {
	changed := map[string]int{}
	for path, version := range current {
		oldVersion, exists := old[path]
		if !exists || version > oldVersion {
			changed[path] = version
		}
	}
	return changed
}
```

Note: add `corev1 "k8s.io/api/core/v1"` to the import block. Add the k8s dependency:

```bash
go get k8s.io/client-go@latest
go get k8s.io/api@latest
go get k8s.io/apimachinery@latest
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -v -run "TestStateStore|TestDetectChanges" ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add state.go state_test.go go.mod go.sum
git commit -m "feat: ConfigMap-based state store with change detection"
```

---

### Task 5: Resource Discovery

**Files:**
- Create: `discovery.go`
- Create: `discovery_test.go`

- [ ] **Step 1: Write the failing test**

```go
// discovery_test.go
package main

import (
	"context"
	"testing"
)

func TestParseWatchAnnotation(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"secret/data/litellm", []string{"litellm"}},
		{"secret/data/wasabi-backup,secret/data/airvpn/qbittorrent", []string{"wasabi-backup", "airvpn/qbittorrent"}},
		{"  secret/data/foo , secret/data/bar  ", []string{"foo", "bar"}},
		{"litellm", []string{"litellm"}}, // without prefix
	}

	for _, tt := range tests {
		got := ParseWatchAnnotation(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("ParseWatchAnnotation(%q) = %v, want %v", tt.input, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("ParseWatchAnnotation(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestBuildPathToResourceMap(t *testing.T) {
	resources := []WatchedResource{
		{Kind: "Secret", Namespace: "litellm", Name: "litellm-secret", VaultPaths: []string{"litellm"}},
		{Kind: "Secret", Namespace: "sonarr", Name: "volsync-config", VaultPaths: []string{"wasabi-backup"}},
		{Kind: "Application", Namespace: "argocd", Name: "qbittorrent", VaultPaths: []string{"wasabi-backup", "airvpn/qbittorrent"}},
	}

	pathMap := BuildPathToResourceMap(resources)

	// wasabi-backup should map to 2 resources
	if len(pathMap["wasabi-backup"]) != 2 {
		t.Errorf("wasabi-backup has %d resources, want 2", len(pathMap["wasabi-backup"]))
	}

	// litellm should map to 1 resource
	if len(pathMap["litellm"]) != 1 {
		t.Errorf("litellm has %d resources, want 1", len(pathMap["litellm"]))
	}

	// airvpn/qbittorrent should map to 1 resource
	if len(pathMap["airvpn/qbittorrent"]) != 1 {
		t.Errorf("airvpn/qbittorrent has %d resources, want 1", len(pathMap["airvpn/qbittorrent"]))
	}
}

type fakeDiscovery struct {
	resources []WatchedResource
}

func (f *fakeDiscovery) Discover(ctx context.Context) ([]WatchedResource, error) {
	return f.resources, nil
}

func TestDiscoveryInterface(t *testing.T) {
	d := &fakeDiscovery{
		resources: []WatchedResource{
			{Kind: "Secret", Namespace: "litellm", Name: "litellm-secret", VaultPaths: []string{"litellm"}},
		},
	}

	ctx := context.Background()
	res, err := d.Discover(ctx)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(res) != 1 {
		t.Errorf("got %d resources, want 1", len(res))
	}
	if res[0].Name != "litellm-secret" {
		t.Errorf("Name = %q, want %q", res[0].Name, "litellm-secret")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v -run "TestParseWatch|TestBuildPath|TestDiscovery" ./...`
Expected: FAIL — types not defined

- [ ] **Step 3: Implement discovery.go**

```go
// discovery.go
package main

import (
	"context"
	"log/slog"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const watchAnnotation = "vaultsync/watch"

// WatchedResource represents a K8s resource annotated with vaultsync/watch.
type WatchedResource struct {
	APIVersion string
	Kind       string
	Namespace  string
	Name       string
	VaultPaths []string
}

// Discovery finds K8s resources annotated with vaultsync/watch.
type Discovery interface {
	Discover(ctx context.Context) ([]WatchedResource, error)
}

type k8sDiscovery struct {
	client    kubernetes.Interface
	dynClient dynamic.Interface
	logger    *slog.Logger
}

func NewDiscovery(client kubernetes.Interface, dynClient dynamic.Interface, logger *slog.Logger) Discovery {
	return &k8sDiscovery{client: client, dynClient: dynClient, logger: logger}
}

func (d *k8sDiscovery) Discover(ctx context.Context) ([]WatchedResource, error) {
	var resources []WatchedResource

	// Scan Secrets across all namespaces
	secrets, err := d.client.CoreV1().Secrets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, s := range secrets.Items {
		if ann, ok := s.Annotations[watchAnnotation]; ok {
			resources = append(resources, WatchedResource{
				APIVersion: "v1",
				Kind:       "Secret",
				Namespace:  s.Namespace,
				Name:       s.Name,
				VaultPaths: ParseWatchAnnotation(ann),
			})
		}
	}

	// Scan ConfigMaps across all namespaces
	configmaps, err := d.client.CoreV1().ConfigMaps("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, cm := range configmaps.Items {
		if ann, ok := cm.Annotations[watchAnnotation]; ok {
			resources = append(resources, WatchedResource{
				APIVersion: "v1",
				Kind:       "ConfigMap",
				Namespace:  cm.Namespace,
				Name:       cm.Name,
				VaultPaths: ParseWatchAnnotation(ann),
			})
		}
	}

	// Scan ArgoCD Applications in argocd namespace
	appGVR := schema.GroupVersionResource{
		Group:    "argoproj.io",
		Version:  "v1alpha1",
		Resource: "applications",
	}
	apps, err := d.dynClient.Resource(appGVR).Namespace("argocd").List(ctx, metav1.ListOptions{})
	if err != nil {
		d.logger.Warn("failed to list ArgoCD Applications", "error", err)
	} else {
		for _, app := range apps.Items {
			annotations := app.GetAnnotations()
			if ann, ok := annotations[watchAnnotation]; ok {
				resources = append(resources, WatchedResource{
					APIVersion: "argoproj.io/v1alpha1",
					Kind:       "Application",
					Namespace:  app.GetNamespace(),
					Name:       app.GetName(),
					VaultPaths: ParseWatchAnnotation(ann),
				})
			}
		}
	}

	d.logger.Info("discovery complete", "resources", len(resources))
	return resources, nil
}

// ParseWatchAnnotation parses the vaultsync/watch annotation value into vault paths.
// Accepts: "secret/data/foo,secret/data/bar" or "foo,bar" (without prefix).
// Strips the "secret/data/" prefix if present.
func ParseWatchAnnotation(value string) []string {
	parts := strings.Split(value, ",")
	var paths []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.TrimPrefix(p, "secret/data/")
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// BuildPathToResourceMap inverts the resource→paths mapping to path→resources.
func BuildPathToResourceMap(resources []WatchedResource) map[string][]WatchedResource {
	m := map[string][]WatchedResource{}
	for _, r := range resources {
		for _, p := range r.VaultPaths {
			m[p] = append(m[p], r)
		}
	}
	return m
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -v -run "TestParseWatch|TestBuildPath|TestDiscovery" ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add discovery.go discovery_test.go go.mod go.sum
git commit -m "feat: resource discovery via vaultsync/watch annotation"
```

---

### Task 6: Refresher

**Files:**
- Create: `refresher.go`
- Create: `refresher_test.go`

- [ ] **Step 1: Write the failing test**

```go
// refresher_test.go
package main

import (
	"context"
	"testing"
)

type fakeRefresher struct {
	refreshed []WatchedResource
	err       error
}

func (f *fakeRefresher) Refresh(ctx context.Context, resource WatchedResource) error {
	if f.err != nil {
		return f.err
	}
	f.refreshed = append(f.refreshed, resource)
	return nil
}

func TestRefreshStrategy(t *testing.T) {
	tests := []struct {
		kind     string
		wantFunc string
	}{
		{"Secret", "delete"},
		{"ConfigMap", "delete"},
		{"Application", "recreate"},
	}

	for _, tt := range tests {
		got := RefreshStrategy(tt.kind)
		if got != tt.wantFunc {
			t.Errorf("RefreshStrategy(%q) = %q, want %q", tt.kind, got, tt.wantFunc)
		}
	}
}

func TestRefresherInterface(t *testing.T) {
	r := &fakeRefresher{}
	ctx := context.Background()

	resource := WatchedResource{Kind: "Secret", Namespace: "litellm", Name: "litellm-secret"}
	err := r.Refresh(ctx, resource)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(r.refreshed) != 1 {
		t.Errorf("refreshed %d resources, want 1", len(r.refreshed))
	}
	if r.refreshed[0].Name != "litellm-secret" {
		t.Errorf("refreshed resource = %q, want %q", r.refreshed[0].Name, "litellm-secret")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v -run "TestRefresh" ./...`
Expected: FAIL — `RefreshStrategy` not defined

- [ ] **Step 3: Implement refresher.go**

```go
// refresher.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const argocdFinalizer = "resources-finalizer.argocd.argoproj.io"

// Refresher deletes or recreates K8s resources to trigger secret re-injection.
type Refresher interface {
	Refresh(ctx context.Context, resource WatchedResource) error
}

type k8sRefresher struct {
	client    kubernetes.Interface
	dynClient dynamic.Interface
	dryRun    bool
	logger    *slog.Logger
}

func NewRefresher(client kubernetes.Interface, dynClient dynamic.Interface, cfg Config, logger *slog.Logger) Refresher {
	return &k8sRefresher{
		client:    client,
		dynClient: dynClient,
		dryRun:    cfg.DryRun,
		logger:    logger,
	}
}

func RefreshStrategy(kind string) string {
	switch kind {
	case "Application":
		return "recreate"
	default:
		return "delete"
	}
}

func (r *k8sRefresher) Refresh(ctx context.Context, resource WatchedResource) error {
	strategy := RefreshStrategy(resource.Kind)
	r.logger.Info("refreshing resource",
		"kind", resource.Kind,
		"namespace", resource.Namespace,
		"name", resource.Name,
		"strategy", strategy,
	)

	if r.dryRun {
		r.logger.Info("[DRY RUN] would refresh resource",
			"kind", resource.Kind,
			"name", resource.Name,
		)
		return nil
	}

	switch strategy {
	case "delete":
		return r.deleteResource(ctx, resource)
	case "recreate":
		return r.recreateApplication(ctx, resource)
	default:
		return fmt.Errorf("unknown strategy %q for %s/%s", strategy, resource.Kind, resource.Name)
	}
}

func (r *k8sRefresher) deleteResource(ctx context.Context, resource WatchedResource) error {
	switch resource.Kind {
	case "Secret":
		return r.client.CoreV1().Secrets(resource.Namespace).Delete(ctx, resource.Name, metav1.DeleteOptions{})
	case "ConfigMap":
		return r.client.CoreV1().ConfigMaps(resource.Namespace).Delete(ctx, resource.Name, metav1.DeleteOptions{})
	default:
		return fmt.Errorf("delete not supported for kind %q", resource.Kind)
	}
}

// recreateApplication removes the ArgoCD finalizer from an Application CR, then deletes it.
// The parent app-of-apps (with selfHeal: true) re-creates it from git,
// triggering the Bank-Vaults webhook to re-inject new secret values.
// Child resources are NOT cascade-deleted because the finalizer is removed first.
func (r *k8sRefresher) recreateApplication(ctx context.Context, resource WatchedResource) error {
	appGVR := schema.GroupVersionResource{
		Group:    "argoproj.io",
		Version:  "v1alpha1",
		Resource: "applications",
	}

	// Step 1: Remove the ArgoCD finalizer to prevent cascade deletion
	r.logger.Info("removing argocd finalizer", "app", resource.Name)
	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"finalizers": nil,
		},
	}
	patchBytes, _ := json.Marshal(patch)
	_, err := r.dynClient.Resource(appGVR).Namespace(resource.Namespace).Patch(
		ctx, resource.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("removing finalizer from %s: %w", resource.Name, err)
	}

	// Step 2: Delete the Application CR
	r.logger.Info("deleting application", "app", resource.Name)
	err = r.dynClient.Resource(appGVR).Namespace(resource.Namespace).Delete(
		ctx, resource.Name, metav1.DeleteOptions{},
	)
	if err != nil {
		return fmt.Errorf("deleting application %s: %w", resource.Name, err)
	}

	r.logger.Info("application deleted, parent app will re-create from git", "app", resource.Name)
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -v -run "TestRefresh" ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add refresher.go refresher_test.go
git commit -m "feat: resource refresher with delete and Application CR recreate strategies"
```

---

### Task 7: Controller (Main Reconciliation Loop)

**Files:**
- Create: `controller.go`
- Create: `controller_test.go`

- [ ] **Step 1: Write the failing test**

```go
// controller_test.go
package main

import (
	"context"
	"testing"
	"log/slog"
	"os"
)

func TestReconcile(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	vault := &fakeVaultWatcher{
		versions: map[string]int{
			"litellm":            3,  // was 2 → changed
			"wasabi-backup":      7,  // unchanged
			"airvpn/qbittorrent": 2,  // unchanged
		},
	}

	state := &fakeStateStore{
		versions: map[string]int{
			"litellm":            2,
			"wasabi-backup":      7,
			"airvpn/qbittorrent": 2,
		},
	}

	discovery := &fakeDiscovery{
		resources: []WatchedResource{
			{Kind: "Secret", Namespace: "litellm", Name: "litellm-secret", VaultPaths: []string{"litellm"}},
			{Kind: "Secret", Namespace: "sonarr", Name: "volsync-config", VaultPaths: []string{"wasabi-backup"}},
			{Kind: "Application", Namespace: "argocd", Name: "qbittorrent", VaultPaths: []string{"wasabi-backup", "airvpn/qbittorrent"}},
		},
	}

	refresher := &fakeRefresher{}

	ctrl := &Controller{
		vault:     vault,
		state:     state,
		discovery: discovery,
		refresher: refresher,
		logger:    logger,
	}

	ctx := context.Background()
	err := ctrl.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Only litellm-secret should be refreshed (litellm changed, 2→3)
	if len(refresher.refreshed) != 1 {
		t.Fatalf("refreshed %d resources, want 1: %v", len(refresher.refreshed), refresher.refreshed)
	}
	if refresher.refreshed[0].Name != "litellm-secret" {
		t.Errorf("refreshed %q, want %q", refresher.refreshed[0].Name, "litellm-secret")
	}

	// State should be updated
	if !state.saved {
		t.Error("state was not saved")
	}
	if state.versions["litellm"] != 3 {
		t.Errorf("state litellm = %d, want 3", state.versions["litellm"])
	}
}

func TestReconcileMultipleResourcesForSamePath(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	vault := &fakeVaultWatcher{
		versions: map[string]int{"wasabi-backup": 8},
	}
	state := &fakeStateStore{
		versions: map[string]int{"wasabi-backup": 7},
	}
	discovery := &fakeDiscovery{
		resources: []WatchedResource{
			{Kind: "Secret", Namespace: "sonarr", Name: "volsync-sonarr", VaultPaths: []string{"wasabi-backup"}},
			{Kind: "Secret", Namespace: "radarr", Name: "volsync-radarr", VaultPaths: []string{"wasabi-backup"}},
			{Kind: "Application", Namespace: "argocd", Name: "qbittorrent", VaultPaths: []string{"wasabi-backup"}},
		},
	}
	refresher := &fakeRefresher{}

	ctrl := &Controller{vault: vault, state: state, discovery: discovery, refresher: refresher, logger: logger}
	err := ctrl.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// All 3 resources should be refreshed
	if len(refresher.refreshed) != 3 {
		t.Errorf("refreshed %d resources, want 3", len(refresher.refreshed))
	}
}

func TestReconcileNoChanges(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	vault := &fakeVaultWatcher{
		versions: map[string]int{"litellm": 3},
	}
	state := &fakeStateStore{
		versions: map[string]int{"litellm": 3},
	}
	discovery := &fakeDiscovery{
		resources: []WatchedResource{
			{Kind: "Secret", Namespace: "litellm", Name: "litellm-secret", VaultPaths: []string{"litellm"}},
		},
	}
	refresher := &fakeRefresher{}

	ctrl := &Controller{vault: vault, state: state, discovery: discovery, refresher: refresher, logger: logger}
	err := ctrl.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if len(refresher.refreshed) != 0 {
		t.Errorf("refreshed %d resources, want 0", len(refresher.refreshed))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v -run "TestReconcile" ./...`
Expected: FAIL — `Controller` not defined

- [ ] **Step 3: Implement controller.go**

```go
// controller.go
package main

import (
	"context"
	"log/slog"
)

// Controller is the main reconciliation loop.
type Controller struct {
	vault     VaultWatcher
	state     StateStore
	discovery Discovery
	refresher Refresher
	logger    *slog.Logger
}

func NewController(vault VaultWatcher, state StateStore, discovery Discovery, refresher Refresher, logger *slog.Logger) *Controller {
	return &Controller{
		vault:     vault,
		state:     state,
		discovery: discovery,
		refresher: refresher,
		logger:    logger,
	}
}

// Reconcile performs a single reconciliation cycle:
// 1. Poll Vault for current secret versions
// 2. Compare with stored state to find changes
// 3. Discover annotated K8s resources
// 4. Refresh resources affected by changed secrets
// 5. Update state with new versions
func (c *Controller) Reconcile(ctx context.Context) error {
	// 1. Get current versions from Vault
	currentVersions, err := c.vault.GetAllVersions(ctx)
	if err != nil {
		return err
	}
	c.logger.Debug("polled vault", "secrets", len(currentVersions))

	// 2. Load stored state and detect changes
	storedVersions, err := c.state.Load(ctx)
	if err != nil {
		return err
	}

	changed := DetectChanges(storedVersions, currentVersions)
	if len(changed) == 0 {
		c.logger.Debug("no vault changes detected")
		return c.state.Save(ctx, currentVersions)
	}

	c.logger.Info("vault changes detected", "changed_paths", len(changed))
	for path, version := range changed {
		c.logger.Info("secret changed", "path", path, "new_version", version)
	}

	// 3. Discover annotated resources
	resources, err := c.discovery.Discover(ctx)
	if err != nil {
		return err
	}

	pathMap := BuildPathToResourceMap(resources)

	// 4. Refresh affected resources (deduplicate)
	refreshed := map[string]bool{}
	for changedPath := range changed {
		affected, ok := pathMap[changedPath]
		if !ok {
			c.logger.Debug("no resources watching this path", "path", changedPath)
			continue
		}

		for _, resource := range affected {
			key := resource.Kind + "/" + resource.Namespace + "/" + resource.Name
			if refreshed[key] {
				continue
			}
			refreshed[key] = true

			if err := c.refresher.Refresh(ctx, resource); err != nil {
				c.logger.Error("failed to refresh resource",
					"kind", resource.Kind,
					"name", resource.Name,
					"error", err,
				)
				continue
			}
		}
	}

	// 5. Update state
	return c.state.Save(ctx, currentVersions)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -v -run "TestReconcile" ./...`
Expected: all 3 tests PASS

- [ ] **Step 5: Commit**

```bash
git add controller.go controller_test.go
git commit -m "feat: controller reconciliation loop"
```

---

### Task 8: Main Entry Point

**Files:**
- Modify: `main.go`

- [ ] **Step 1: Implement main.go with signal handling and poll loop**

```go
// main.go
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	var level slog.Level
	switch cfg.LogLevel {
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

	logger.Info("starting vaultsync",
		"vault_addr", cfg.VaultAddr,
		"poll_interval", cfg.PollInterval,
		"dry_run", cfg.DryRun,
	)

	// Create K8s clients
	k8sCfg, err := rest.InClusterConfig()
	if err != nil {
		logger.Error("failed to get in-cluster config", "error", err)
		os.Exit(1)
	}

	clientset, err := kubernetes.NewForConfig(k8sCfg)
	if err != nil {
		logger.Error("failed to create k8s client", "error", err)
		os.Exit(1)
	}

	dynClient, err := dynamic.NewForConfig(k8sCfg)
	if err != nil {
		logger.Error("failed to create dynamic client", "error", err)
		os.Exit(1)
	}

	// Create Vault watcher
	vaultWatcher, err := NewVaultWatcher(cfg, logger)
	if err != nil {
		logger.Error("failed to create vault watcher", "error", err)
		os.Exit(1)
	}

	// Create components
	state := NewStateStore(clientset, cfg, logger)
	discovery := NewDiscovery(clientset, dynClient, logger)
	refresher := NewRefresher(clientset, dynClient, cfg, logger)
	ctrl := NewController(vaultWatcher, state, discovery, refresher, logger)

	// Run reconciliation loop with graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Info("starting reconciliation loop", "interval", cfg.PollInterval)
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	// Run once immediately
	if err := ctrl.Reconcile(ctx); err != nil {
		logger.Error("reconciliation failed", "error", err)
	}

	for {
		select {
		case <-ctx.Done():
			logger.Info("shutting down")
			return
		case <-ticker.C:
			if err := ctrl.Reconcile(ctx); err != nil {
				logger.Error("reconciliation failed", "error", err)
			}
		}
	}
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd ~/src/vaultSync && go build ./...`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add main.go
git commit -m "feat: main entry point with poll loop and graceful shutdown"
```

---

### Task 9: Dockerfile

**Files:**
- Create: `Dockerfile`

- [ ] **Step 1: Create multi-stage Dockerfile**

```dockerfile
FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -o vaultsync .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /app/vaultsync /vaultsync
USER nonroot:nonroot
ENTRYPOINT ["/vaultsync"]
```

- [ ] **Step 2: Test docker build**

Run: `cd ~/src/vaultSync && docker build -t vaultsync:test .`
Expected: build succeeds

- [ ] **Step 3: Commit**

```bash
git add Dockerfile
git commit -m "feat: multi-stage Dockerfile with distroless base"
```

---

### Task 10: Kubernetes Deployment Manifests

**Files:**
- Create: `deploy/namespace.yaml`
- Create: `deploy/rbac.yaml`
- Create: `deploy/deployment.yaml`
- Create: `deploy/vault-role.yaml`

- [ ] **Step 1: Create namespace**

```yaml
# deploy/namespace.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: vaultsync
```

- [ ] **Step 2: Create RBAC**

```yaml
# deploy/rbac.yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: vaultsync
  namespace: vaultsync
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: vaultsync
rules:
  # Read/delete Secrets and ConfigMaps across all namespaces
  - apiGroups: [""]
    resources: ["secrets", "configmaps"]
    verbs: ["get", "list", "delete"]
  # Create/update state ConfigMap in vaultsync namespace
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["create", "update"]
  # Read/patch/delete ArgoCD Applications
  - apiGroups: ["argoproj.io"]
    resources: ["applications"]
    verbs: ["get", "list", "patch", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: vaultsync
subjects:
  - kind: ServiceAccount
    name: vaultsync
    namespace: vaultsync
roleRef:
  kind: ClusterRole
  name: vaultsync
  apiGroup: rbac.authorization.k8s.io
```

- [ ] **Step 3: Create deployment**

```yaml
# deploy/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: vaultsync
  namespace: vaultsync
  labels:
    app.kubernetes.io/name: vaultsync
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: vaultsync
  template:
    metadata:
      labels:
        app.kubernetes.io/name: vaultsync
    spec:
      serviceAccountName: vaultsync
      containers:
        - name: vaultsync
          image: ghcr.io/thekoma/vaultsync:latest
          env:
            - name: VAULT_ADDR
              value: "https://vault.vault.svc.cluster.local:8200"
            - name: VAULT_ROLE
              value: "vaultsync"
            - name: VAULT_MOUNT
              value: "secret"
            - name: VAULT_SKIP_VERIFY
              value: "false"
            - name: POLL_INTERVAL
              value: "60s"
            - name: STATE_NAMESPACE
              value: "vaultsync"
            - name: STATE_CONFIGMAP
              value: "vaultsync-state"
            - name: DRY_RUN
              value: "false"
            - name: LOG_LEVEL
              value: "info"
          resources:
            requests:
              cpu: 10m
              memory: 32Mi
            limits:
              cpu: 100m
              memory: 64Mi
          securityContext:
            readOnlyRootFilesystem: true
            runAsNonRoot: true
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]
```

- [ ] **Step 4: Create Vault role config**

This snippet must be added to the `externalConfig.auth` section in
`/home/koma/src/asgard-k8s/infrastructure/odin/vault/vault.yaml`:

```yaml
# deploy/vault-role.yaml
# Add this role to vault.yaml externalConfig.auth[0].roles:
- name: vaultsync
  bound_service_account_names: ["vaultsync"]
  bound_service_account_namespaces: ["vaultsync"]
  policies: allow_secrets
  ttl: 1h
```

- [ ] **Step 5: Commit**

```bash
git add deploy/
git commit -m "feat: kubernetes deployment manifests and vault role config"
```

---

### Task 11: Integration Testing Guide

**Files:** none (manual verification steps)

- [ ] **Step 1: Deploy with DRY_RUN=true**

```bash
# Build and load image
cd ~/src/vaultSync
docker build -t ghcr.io/thekoma/vaultsync:latest .
# Push or load into cluster (depends on registry setup)

# Apply manifests
kubectl apply -f deploy/namespace.yaml
kubectl apply -f deploy/rbac.yaml
kubectl apply -f deploy/deployment.yaml

# Set DRY_RUN initially
kubectl set env deployment/vaultsync -n vaultsync DRY_RUN=true
```

- [ ] **Step 2: Add vault role to asgard-k8s**

Edit `/home/koma/src/asgard-k8s/infrastructure/odin/vault/vault.yaml` and add the
`vaultsync` role to `externalConfig.auth[0].roles` as shown in Task 10 Step 4.

Commit and push to trigger ArgoCD sync.

- [ ] **Step 3: Annotate a test Secret**

Pick a low-risk Secret, e.g., `litellm-secret`:

```bash
kubectl annotate secret litellm-secret -n litellm \
  vaultsync/watch="secret/data/litellm" --overwrite
```

Or add the annotation to `applications/odin/litellm/secret.yaml` in git:

```yaml
metadata:
  annotations:
    vaultsync/watch: "secret/data/litellm"
```

- [ ] **Step 4: Verify dry-run detection**

```bash
# Check controller logs
kubectl logs -n vaultsync deployment/vaultsync -f

# Change a secret in Vault
vault kv put secret/litellm master_key=new-value-here

# Wait for next poll cycle (default 60s)
# Expected log: "[DRY RUN] would refresh resource" for litellm-secret
```

- [ ] **Step 5: Disable dry-run and verify actual refresh**

```bash
kubectl set env deployment/vaultsync -n vaultsync DRY_RUN=false

# Change the secret again
vault kv put secret/litellm master_key=another-value

# Wait for next poll cycle
# Expected: Secret deleted, ArgoCD re-creates it, webhook re-injects
kubectl get events -n litellm --field-selector reason=Killing 2>/dev/null || \
  kubectl get events -n litellm | grep litellm-secret
```

- [ ] **Step 6: Verify state ConfigMap**

```bash
kubectl get configmap vaultsync-state -n vaultsync -o jsonpath='{.data.versions\.json}' | jq .
```

Expected: JSON with current vault secret versions.

- [ ] **Step 7: Commit annotation changes to asgard-k8s**

Once verified, add `vaultsync/watch` annotations to all relevant resources in the
asgard-k8s repo and commit.

---

## Annotation Migration Guide

Below are example annotations for the existing asgard-k8s resources. Add these
to the respective YAML files in git:

**Standalone Secrets** (delete strategy, automatic):
```yaml
# applications/odin/litellm/secret.yaml
metadata:
  annotations:
    vaultsync/watch: "secret/data/litellm"

# applications/odin/semaphore/secret.yaml
metadata:
  annotations:
    vaultsync/watch: "secret/data/semaphore"

# infrastructure/odin/cert-manager/cf-credentials.yaml
metadata:
  annotations:
    vaultsync/watch: "secret/data/certmanager/cloudflare,secret/data/certmanager/heimdall"
```

**Application CRs with inline valuesObject** (recreate strategy, automatic):
```yaml
# applications/odin/qbittorrent/helm.yaml
metadata:
  annotations:
    vaultsync/watch: "secret/data/wasabi-backup,secret/data/airvpn/qbittorrent,secret/data/asgard"

# applications/odin/teslamate/helm.yaml
metadata:
  annotations:
    vaultsync/watch: "secret/data/wasabi-backup,secret/data/teslamate"
```

**Infrastructure Secrets**:
```yaml
# infrastructure/odin/argocd/mainrepo.yaml
metadata:
  annotations:
    vaultsync/watch: "secret/data/argocd"

# infrastructure/odin/monitoring/04-secrets.yaml
metadata:
  annotations:
    vaultsync/watch: "secret/data/ntfy,secret/data/argocd,secret/data/monitoring"
```

---

## What This Removes from asgard-k8s

Once vaultSync is working and all resources are annotated, the massive
`ignoreDifferences` block in `clusters/odin/include/applications.yaml` can be
**progressively simplified** — the 40+ jsonPointers for Application CR valuesObject
fields become unnecessary because Application CRs are now recreated when vault
values change.

The `ignoreDifferences` for standalone Secrets (`/data`, `/stringData`) is still needed
because ArgoCD will always see a diff between git (placeholders) and live (real values).
But the rotation problem is solved: when a vault secret changes, vaultSync deletes
the Secret, ArgoCD re-creates it with new values from the webhook.

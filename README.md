# vaultSync

[![Go](https://img.shields.io/github/go-mod/go-version/thekoma/vaultsync)](https://go.dev/)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Release](https://img.shields.io/github/v/release/thekoma/vaultsync)](https://github.com/thekoma/vaultsync/releases)
[![CI](https://img.shields.io/github/actions/workflow/status/thekoma/vaultsync/ci.yml?label=CI)](https://github.com/thekoma/vaultsync/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/thekoma/vaultsync)](https://goreportcard.com/report/github.com/thekoma/vaultsync)

Kubernetes controller that bridges HashiCorp Vault secret changes to ArgoCD-managed resources via annotation patching.

## The Problem

When using [Bank-Vaults](https://bank-vaults.dev/) webhook to inject secrets into Kubernetes resources, secret rotation in Vault does **not** automatically propagate to running workloads:

1. **Bank-Vaults webhook** only runs at admission time (pod creation). It does not watch for Vault secret version changes.
2. **ArgoCD `ignoreDifferences`** is typically set on injected fields (since the webhook mutates them), so ArgoCD never detects drift from the live secret values.
3. The result: secrets rotate in Vault, but workloads keep running with stale values until someone manually restarts them.

## How It Works

vaultSync polls Vault KV v2 metadata for version changes, then patches a `vaultsync/trigger` annotation onto resources that declared interest via a `vaultsync/watch` annotation. This causes ArgoCD's `selfHeal` to detect drift (the trigger annotation is not in git), re-apply the manifest, and the Bank-Vaults webhook re-injects the current secret value.

**This is entirely non-destructive** -- no pods are deleted, no secrets are overwritten. The annotation patch simply nudges ArgoCD to do its normal reconciliation.

```mermaid
sequenceDiagram
    participant VS as vaultSync
    participant V as Vault
    participant K as Kubernetes
    participant A as ArgoCD
    participant W as Bank-Vaults Webhook

    loop Every poll interval
        VS->>V: Poll KV v2 metadata
        V-->>VS: Secret versions
        VS->>VS: Compare with stored state
    end

    Note over VS: Secret version changed!
    VS->>K: Patch trigger annotation
    A->>K: Detect annotation drift (selfHeal)
    A->>K: Re-apply manifest from git
    K->>W: Admission webhook intercepts
    W->>V: Fetch new secret value
    W-->>K: Mutate resource with new value
```

## Architecture

```mermaid
flowchart LR
    subgraph Vault
        VS[Secret Store<br/>KV v2]
    end
    subgraph vaultSync
        VW[Vault Watcher] --> C[Controller]
        SS[State Store<br/>ConfigMap] --> C
        D[Discovery] --> C
        R[Refresher] --> C
    end
    subgraph Kubernetes
        S[Secrets]
        CM[ConfigMaps]
        APP[ArgoCD Apps]
    end
    subgraph ArgoCD
        SH[selfHeal]
        GIT[Git Source]
    end
    subgraph Bank-Vaults
        WH[Webhook]
    end

    VS -.->|poll metadata| VW
    C -->|patch annotation| S
    C -->|patch annotation| CM
    C -->|patch annotation| APP
    SH -->|detect drift| S
    SH -->|re-apply from git| GIT
    GIT -->|manifest| WH
    WH -->|inject secrets| S
```

### Components

| Component | Description |
|-----------|-------------|
| **Vault Watcher** | Authenticates to Vault via Kubernetes auth, lists KV v2 secret metadata and returns current versions |
| **State Store** | Persists the last-seen version per Vault path in a ConfigMap |
| **Discovery** | Scans Secrets, ConfigMaps, and ArgoCD Applications for the `vaultsync/watch` annotation |
| **Refresher** | Patches the `vaultsync/trigger` annotation on resources whose watched paths have new versions |
| **Controller** | Orchestrates a single reconciliation cycle: poll, diff, refresh |

## Quick Start

```bash
helm install vaultsync oci://ghcr.io/thekoma/charts/vaultsync \
  --namespace vaultsync --create-namespace \
  --set vault.addr=https://vault.vault.svc.cluster.local:8200
```

Or from source:

```bash
git clone https://github.com/thekoma/vaultsync.git
cd vaultsync
helm install vaultsync charts/vaultsync \
  --namespace vaultsync --create-namespace
```

## Vault Prerequisites

vaultSync authenticates to Vault via the [Kubernetes auth method](https://developer.hashicorp.com/vault/docs/auth/kubernetes). You need to configure three things in Vault before deploying:

### 1. Enable Kubernetes auth (if not already enabled)

```bash
vault auth enable kubernetes

vault write auth/kubernetes/config \
  kubernetes_host="https://kubernetes.default.svc:443"
```

### 2. Create a policy granting read access to secret metadata

vaultSync only reads **metadata** (version numbers), never the secret values themselves. However, Vault's KV v2 API requires the `read` capability on the metadata path and `list` for recursive discovery.

```bash
vault policy write vaultsync-policy - <<EOF
# List and read metadata for all secrets (no access to actual values)
path "secret/metadata/*" {
  capabilities = ["read", "list"]
}
EOF
```

> **Note:** If you want to use a broader existing policy (e.g., `allow_secrets` that grants full CRUD), that works too. vaultSync will never write to or read the actual secret data — only metadata.

### 3. Create a Kubernetes auth role for the vaultSync service account

```bash
vault write auth/kubernetes/role/vaultsync \
  bound_service_account_names=vaultsync \
  bound_service_account_namespaces=vaultsync \
  policies=vaultsync-policy \
  ttl=1h
```

The service account name and namespace must match the Helm release. With default values, both are `vaultsync`.

### Bank-Vaults operator

If you use the [Bank-Vaults operator](https://bank-vaults.dev/), add the role to your `Vault` CR's `externalConfig`:

```yaml
spec:
  externalConfig:
    policies:
      - name: vaultsync-policy
        rules: path "secret/metadata/*" {
          capabilities = ["read", "list"]
          }
    auth:
      - type: kubernetes
        roles:
          - name: vaultsync
            bound_service_account_names: ["vaultsync"]
            bound_service_account_namespaces: ["vaultsync"]
            policies: vaultsync-policy
            ttl: 1h
```

### Verify the setup

After deploying vaultSync, check the logs to confirm authentication:

```bash
kubectl logs -n vaultsync deployment/vaultsync | head -5
# Expected: "authenticated to vault"
```

If you see `invalid role name "vaultsync"`, the Vault role hasn't been created yet. If you see `permission denied`, check the policy bindings.

## Configuration

All configuration is via Helm values (which map to environment variables):

| Value | Env Var | Default | Description |
|-------|---------|---------|-------------|
| `vault.addr` | `VAULT_ADDR` | `https://vault.vault.svc.cluster.local:8200` | Vault server address |
| `vault.role` | `VAULT_ROLE` | `vaultsync` | Vault Kubernetes auth role |
| `vault.mount` | `VAULT_MOUNT` | `secret` | Vault KV v2 mount path |
| `vault.authMount` | `VAULT_AUTH_MOUNT` | `kubernetes` | Vault auth method mount path |
| `vault.skipVerify` | `VAULT_SKIP_VERIFY` | `false` | Skip TLS verification |
| `controller.pollInterval` | `POLL_INTERVAL` | `60s` | How often to poll Vault for changes |
| `controller.dryRun` | `DRY_RUN` | `false` | Log actions without patching resources |
| `controller.logLevel` | `LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `controller.watchAnnotation` | `WATCH_ANNOTATION` | `vaultsync/watch` | Annotation key used to discover watched resources |
| `controller.triggerAnnotation` | `TRIGGER_ANNOTATION` | `vaultsync/trigger` | Annotation key used for the drift-trigger patch |
| `state.namespace` | `STATE_NAMESPACE` | Release namespace | Namespace for the state ConfigMap |
| `state.configMap` | `STATE_CONFIGMAP` | `vaultsync-state` | Name of the state ConfigMap |
| `image.repository` | -- | `ghcr.io/thekoma/vaultsync` | Container image repository |
| `image.tag` | -- | `Chart.appVersion` | Container image tag |
| `serviceAccount.create` | -- | `true` | Create a ServiceAccount |
| `serviceAccount.name` | -- | Release fullname | ServiceAccount name override |
| `namespace.create` | -- | `true` | Create the namespace resource |

## Annotation Guide

### Opting a resource into vaultSync

Add **both** annotations to any Secret, ConfigMap, or ArgoCD Application:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: my-app-secret
  annotations:
    vaultsync/watch: "apps/my-app/config"
    vaultsync/trigger: ""
```

- `vaultsync/watch` tells vaultSync which Vault paths to monitor for this resource
- `vaultsync/trigger: ""` is a placeholder that vaultSync will patch with a timestamp when a change is detected (see [Why both annotations?](#why-both-annotations) below)

The watch value is a comma-separated list of Vault KV v2 paths (relative to the configured mount):

```yaml
annotations:
  vaultsync/watch: "apps/my-app/config, apps/my-app/certs"
  vaultsync/trigger: ""
```

The `secret/data/` prefix is automatically stripped if present.

> **Tip:** The [pre-commit hook](#pre-commit-hook) adds both annotations automatically.

## How the Refresh Works

1. **Poll**: vaultSync calls Vault's `GET /v1/secret/metadata/:path` for every path discovered across annotated resources.
2. **Diff**: The current version from Vault metadata is compared against the version stored in the state ConfigMap. If they differ, the path is marked as changed.
3. **Patch**: For each resource watching a changed path, vaultSync sends a JSON merge-patch setting `metadata.annotations["vaultsync/trigger"]` to the current UTC timestamp.
4. **selfHeal**: ArgoCD detects that the trigger annotation value differs from what's in the git manifest (`""` vs the timestamp) and re-applies the resource from git, resetting the trigger back to `""`.
5. **Webhook**: During re-application, the Bank-Vaults admission webhook intercepts the resource and fetches the current secret values from Vault, injecting them into the resource.
6. **State update**: vaultSync persists the new version to the state ConfigMap so the same change is not processed again.

The net effect: secrets in Vault rotate, and within one poll interval, all annotated resources are refreshed with the new values -- without any pod restarts, manual intervention, or destructive operations.

### Why both annotations?

ArgoCD uses a **3-way diff** to compare the desired state (rendered manifest from git/Helm) against the live state. It only tracks fields that it "owns" -- fields present in the desired state.

If `vaultsync/trigger` were only added at runtime (not in the manifest), ArgoCD would treat it as an unmanaged field and **ignore it completely**. selfHeal would never fire.

By including `vaultsync/trigger: ""` in the manifest committed to git, ArgoCD owns the field. When vaultSync patches it to a timestamp, ArgoCD sees the difference (`""` vs `"2026-04-15T00:15:00Z"`) and triggers selfHeal to restore the desired state.

```mermaid
sequenceDiagram
    participant G as Git Manifest
    participant A as ArgoCD
    participant K as K8s Resource
    participant VS as vaultSync

    G->>A: trigger: ""
    A->>K: Apply (trigger: "")
    Note over VS: Vault secret changed!
    VS->>K: Patch trigger: "2026-04-15T..."
    A->>A: 3-way diff: "" ≠ "2026-04-15T..."
    A->>K: selfHeal → re-apply from git
    Note over K: trigger reset to ""
    Note over K: Webhook re-injects new values
```

## FluxCD Compatibility

vaultSync works with any GitOps controller that reconciles drift -- not just ArgoCD.
With FluxCD, the same mechanism applies: vaultSync patches a trigger annotation,
Flux detects the drift during its reconciliation loop, re-applies from git, and the
webhook re-injects new values.

To customize the annotation names (e.g., to avoid conflicts or match your naming convention):

| Value | Env Var | Default |
|-------|---------|---------|
| `controller.watchAnnotation` | `WATCH_ANNOTATION` | `vaultsync/watch` |
| `controller.triggerAnnotation` | `TRIGGER_ANNOTATION` | `vaultsync/trigger` |

For the pre-commit hook, pass `--watch-annotation` to match:

```yaml
- repo: https://github.com/thekoma/vaultsync
  rev: 2026.4.2
  hooks:
    - id: vaultsync-annotate
      args: [--watch-annotation, "custom/watch"]
```

## Pre-commit Hook

vaultSync ships a [pre-commit](https://pre-commit.com/) hook that automatically adds `vaultsync/watch` annotations to Kubernetes manifests containing `vault:` references.

### Setup

Add to your `.pre-commit-config.yaml`:

```yaml
- repo: https://github.com/thekoma/vaultsync
  rev: 2026.4.2
  hooks:
    - id: vaultsync-annotate
```

### What it does

The hook scans YAML files for Bank-Vaults webhook patterns (`vault:MOUNT/data/PATH#key`) and:

- **Kubernetes manifests** (files with `apiVersion` + `kind`): automatically adds or updates the `vaultsync/watch` annotation with the correct vault paths
- **Non-manifest files** (Helm values, plain configs): emits a warning reminding you to ensure the parent Application CR has the annotation

```
$ git commit
vaultsync-annotate...................................................Failed
- files were modified by this hook

applications/odin/litellm/secret.yaml: added/updated vaultsync/watch annotation (secret/data/litellm)
applications/odin/qbittorrent/helm.yaml: added/updated vaultsync/watch annotation (secret/data/airvpn/qbittorrent,secret/data/asgard,secret/data/wasabi-backup)
WARNING: infrastructure/odin/argocd/values.yml: vault references found but file is not a Kubernetes manifest (no apiVersion/kind). Ensure the parent Application CR has vaultsync/watch for: secret/data/argocd
```

After the hook modifies files, `git add` the changes and commit again.

### Check-only mode

For CI or when you want to verify without modifying files:

```yaml
- repo: https://github.com/thekoma/vaultsync
  rev: 2026.4.2
  hooks:
    - id: vaultsync-annotate
      args: [--check]
```

### Skipped files

- Files with `vault.security.banzaicloud.io/mutate: skip` are excluded (parent app-of-apps that don't contain vault refs directly)
- Files without any `vault:` references are skipped
- Non-YAML files are not processed

## Development

```bash
# Run tests
make test

# Build binary
make build

# Build Docker image
make docker

# Package Helm chart
make helm-package
```

## License

[Apache License 2.0](LICENSE)

# vaultSync

## What this project does
Kubernetes controller that bridges HashiCorp Vault secret changes to ArgoCD-managed resources via annotation patching. Works with any GitOps controller that reconciles drift (ArgoCD, FluxCD).

## Tech stack
- Go 1.26, no framework (plain client-go)
- HashiCorp Vault API (`github.com/hashicorp/vault/api`) with Kubernetes auth
- Kubernetes client-go (typed + dynamic)
- Helm chart using bjw-s-labs/app-template as dependency
- CalVer versioning (YYYY.MM.MICRO)

## Key architecture
- Polling-based (not event-driven) — polls Vault KV v2 metadata every POLL_INTERVAL
- Only polls **watched paths** (from annotated resources), not all secrets
- Annotation-based discovery: resources opt-in via configurable watch annotation (default `vaultsync/watch`)
- **CRITICAL: ArgoCD 3-way diff** — the trigger annotation (`vaultsync/trigger: ""`) MUST be in the rendered manifest (git/Helm values) for ArgoCD to "own" it. Without this, externally-patched annotations are invisible to selfHeal.
- Refresh flow: vaultSync patches trigger → ArgoCD sees diff → selfHeal re-applies from git → Bank-Vaults webhook re-injects new values
- State tracked in ConfigMap (vault path → version), only writes when something changes
- Vault token renewal before each cycle (re-auths if TTL <30s)
- Exponential backoff on failures (capped at 5min)
- Health probes at /healthz and /readyz

## File structure
- `*.go` — all source in root package `main` (flat structure, no internal packages)
- `charts/vaultsync/` — Helm chart (dependency on bjw-s app-template)
- `hooks/vaultsync_annotate.py` — pre-commit hook for auto-annotation
- `.github/workflows/` — CI (test on push/PR) and Release (CalVer tag → docker + OCI helm)

## Interfaces (for testing)
- `VaultWatcher` — polls Vault metadata
- `StateStore` — ConfigMap-backed version persistence
- `Discovery` — finds annotated K8s resources
- `Refresher` — patches trigger annotation
- All tested via fakes (no mock frameworks)

## Testing
- `go test -race ./...` — unit tests with fakes
- No integration tests (require live Vault + K8s)

## Building & releasing
- `make build` — local binary
- `make test` — run tests
- `make docker` — Docker image
- `make helm-package` — Helm chart
- Push a CalVer tag (e.g., `git tag 2026.4.5 && git push origin 2026.4.5`) to trigger release workflow

## Pre-commit hook
- `hooks/vaultsync_annotate.py` — auto-adds `vaultsync/watch` and `vaultsync/trigger: ""` annotations
- Handles K8s manifests (metadata.annotations) and Helm values files (Bank-Vaults annotation blocks)
- `--check` flag for CI (report without modifying)
- `--watch-annotation` and `--trigger-annotation` flags for custom annotation names
- Mount-agnostic: works with any Vault KV v2 mount name

## Deployment in asgard-k8s
- Application CR inline in `clusters/odin/include/vault.yaml` (alongside vault-operator and vault-config)
- Uses OCI chart from `ghcr.io/thekoma/charts/vaultsync`
- TLS via trust-manager's `default-bundle` ConfigMap mounted as `VAULT_CACERT`
- Vault auth role `vaultsync` created via CLI (persisted in vault.yaml CR)

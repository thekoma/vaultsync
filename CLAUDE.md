# vaultSync

## What this project does
Kubernetes controller that bridges HashiCorp Vault secret changes to ArgoCD-managed resources via annotation patching.

## Tech stack
- Go 1.26, no framework (plain client-go)
- HashiCorp Vault API (`github.com/hashicorp/vault/api`)
- Kubernetes client-go
- Helm chart for deployment

## Key architecture
- Polling-based (not event-driven) - polls Vault KV v2 metadata every POLL_INTERVAL
- Annotation-based discovery: resources opt-in via `vaultsync/watch` annotation
- Safe refresh via annotation patch (NOT delete): patches `vaultsync/trigger` timestamp, ArgoCD selfHeal re-applies from git, webhook re-injects
- State tracked in ConfigMap (vault path -> version)

## Testing
- `go test -race ./...` -- 24 unit tests with fakes/mocks
- No integration tests (require live Vault + K8s)

## Building
- `make build` -- local binary
- `make test` -- run tests
- `make docker` -- Docker image
- `helm package charts/vaultsync` -- Helm chart

# AGENTS.md

## Project

devenv: remote development environments on Kubernetes. One Go server (API, web console,
MCP endpoint, SSH gateway, controller) with state in SQLite or PostgreSQL, a React console
embedded with `go:embed`, environment images, and a Helm chart.

- Layout and data flow: `docs/architecture.md`. Settings: `docs/configuration.md`.
- The only deployment is the owner's homelab cluster (GitOps repo
  `~/Workspaces/tinyrack/homelab`, `apps/base/devenv/`, kube context `homelab`).

## No backward compatibility

The owner is the only user. Do not add or keep compatibility code:

- No deprecated flags, environment variables, config fields, API fields, routes or aliases.
  Rename or remove things directly; unknown config fields fail at start on purpose.
- No automatic data migrations or one-time imports. The database schema
  (`internal/store`) is created on an empty database only; changing it means bumping
  `store.SchemaVersion` and updating `schema`. The server refuses to start on any other
  version.
- No code that adopts or repairs objects left by earlier versions.
- Every migration is a manual step: write it in the release notes (exact SQL, `kubectl`
  or GitOps changes) and apply it to the homelab cluster yourself as part of the release.
- Breaking changes are normal. Bump the minor version for them.

## Environment

- The server and `internal/agent` are Linux only. On Windows, build, vet and test inside
  WSL: `wsl -d Ubuntu -e zsh -lic 'cd /mnt/c/Users/winetree94/Workspaces/tinyrack/devenv && make test'`.
  `GOOS=linux go vet ./...` works natively for a quick check.

## Verify

- `make test` (unit + envtest; `DEVENV_TEST_POSTGRES=postgres://...` adds PostgreSQL)
- `make vet`, `gofmt -l cmd internal`
- `make chart-lint`
- `cd web && pnpm build` (`make types` after changing API types in Go)

## Release

1. Bump `version` and `appVersion` in `charts/devenv/Chart.yaml`, commit `release X.Y.Z`.
2. Tag `vX.Y.Z` and push. CI publishes the images (`images.yaml`) and the chart
   (`chart.yaml`, `oci://ghcr.io/tinyrack-net/charts/devenv`).
3. In the homelab repo, set the tag in `apps/base/devenv/devenv.oci-repository.yaml`,
   update values in `devenv.helm-release.yaml` if needed, and apply the manual migration
   steps. Check the HelmRelease, the server logs and the environments afterwards.

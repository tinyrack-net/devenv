# devenv

Remote development environments on Kubernetes.

- Web console: environments, a workspace with persistent terminals (survive reloads, shareable),
  VNC desktop, processes, ports and live agent activity
- Users with OIDC login, sharing, personal and shared images, per-user limits and access tokens
- SSH access through a gateway or dedicated addresses
- Works on any conformant cluster; dedicated IPs via Multus are optional
- MCP endpoint so AI agents can create environments and code in them: commands, file
  read/edit/write, ripgrep search and background processes
- Installs with a single Helm chart; state lives in SQLite (default) or PostgreSQL

> Status: early development. See [docs/architecture.md](docs/architecture.md).

## Install

```sh
helm install devenv oci://ghcr.io/tinyrack-net/charts/devenv -n devenv-system --create-namespace
kubectl -n devenv-system get secret devenv-auth -o jsonpath='{.data.password}' | base64 -d; echo
kubectl -n devenv-system port-forward svc/devenv 8080:80   # http://localhost:8080
```

Expose the console with `ingress.*` or `httpRoute.*`, and the SSH gateway with
`sshService.type=LoadBalancer` (or `NodePort`). Then connect with
`ssh -p 2222 <environment>@<host>` using a registered SSH key or the
environment password (optional, set when creating the environment).

See [docs/images.md](docs/images.md) to build your own environment images and
[docs/networking.md](docs/networking.md) for dedicated IPs (LoadBalancer, Multus/DHCP), and
[docs/mcp.md](docs/mcp.md) to connect AI agents.

## Development

```sh
make test                 # unit + envtest (DEVENV_TEST_POSTGRES=postgres://... adds PostgreSQL)
make run                  # controller + API on :8080 against current kubeconfig (auth disabled)
cd web && pnpm dev        # web console on :5173, proxies /api to :8080
make build-all            # build web console and embed it into bin/devenv
```

## Configuration

See [docs/configuration.md](docs/configuration.md): login methods (local account, OIDC), users,
sharing, limits, classes, images and the configuration file. Backups and restores:
[docs/disaster-recovery.md](docs/disaster-recovery.md).

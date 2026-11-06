# devenv

Remote development environments on Kubernetes.

- Create, start, stop and delete development environments from a web console
- SSH access, web terminal and VNC desktop
- Works on any conformant cluster; dedicated IPs via Multus are optional
- Installs with a single Helm chart

> Status: early development. See [docs/PLAN.md](docs/PLAN.md).

## Install

```sh
helm install devenv oci://ghcr.io/tinyrack-net/charts/devenv -n devenv-system --create-namespace
kubectl -n devenv-system get secret devenv-auth -o jsonpath='{.data.password}' | base64 -d; echo
kubectl -n devenv-system port-forward svc/devenv 8080:80   # http://localhost:8080
```

Expose the console with `ingress.*` or `httpRoute.*`, and the SSH gateway with
`sshService.type=LoadBalancer` (or `NodePort`). Then connect with
`ssh -p 2222 <environment>@<host>`.

See [docs/images.md](docs/images.md) to build your own environment images and
[docs/networking.md](docs/networking.md) for dedicated IPs (LoadBalancer, Multus/DHCP).

## Development

```sh
make generate manifests   # deepcopy + CRDs
make test                 # unit + envtest
make run                  # controller + API on :8080 against current kubeconfig (auth disabled)
cd web && pnpm dev        # web console on :5173, proxies /api to :8080
make build-all            # build web console and embed it into bin/devenv
```

## Configuration

| Env var / flag | Default | Description |
|---|---|---|
| `DEVENV_ENVIRONMENT_NAMESPACE` | `devenv` | Namespace for environments |
| `DEVENV_HTTP_ADDR` | `:8080` | API and web console |
| `DEVENV_AUTH_MODE` | `password` | `password` or `none` (development only) |
| `DEVENV_ADMIN_USERNAME` | `admin` | Admin username |
| `DEVENV_ADMIN_PASSWORD` / `_FILE` | required | Admin password |
| `DEVENV_KEYS_SECRET` | `devenv-keys` | Secret (in `POD_NAMESPACE`) where the server stores generated session and SSH keys |
| `DEVENV_SECURE_COOKIES` | `false` | Set `true` behind HTTPS |

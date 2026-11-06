# devenv

Remote development environments on Kubernetes.

- Create, start, stop and delete development environments from a web console
- SSH access, web terminal and VNC desktop
- Works on any conformant cluster; dedicated IPs via Multus are optional
- Installs with a single Helm chart

> Status: early development (M1). See [docs/PLAN.md](docs/PLAN.md).

## Development

```sh
make generate manifests   # deepcopy + CRDs
make test                 # unit + envtest
make run                  # run controller against current kubeconfig
```

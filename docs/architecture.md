# Architecture

devenv is a single Go binary (one Deployment) that serves the web console, the REST API,
the MCP endpoint and the SSH gateway, and runs the controller that turns environments into
Kubernetes objects.

```
[web console] ─HTTP/WS─┐
[AI agents]  ──MCP─────┼─> devenv ─┬─ REST API / MCP   users, environments, images, tokens
[ssh / VS Code] ─SSH───┘           ├─ SSH gateway      ssh -p 2222 <env>@host
                                   ├─ terminal, VNC    over SSH into the environment
                                   └─ controller ──> Pod + PVC + Secret + Service per environment
                                         │
                              database (SQLite or PostgreSQL)
```

## Principles

- One Helm chart. No cert-manager, CNI or storage requirements; SQLite on a PVC by default.
- Works on any conformant cluster. Dedicated addresses (LoadBalancer, Multus) are opt-in per
  class.
- Containers only need sshd (and a VNC server for desktops). Everything else runs on the
  server or in the `devenv-agent` helper, mounted read-only from the server image as an image
  volume.
- Recovery uses the tools the cluster already has: Git for the configuration, database backups,
  storage backups for home volumes ([disaster-recovery.md](disaster-recovery.md)).

## State

| What | Where |
|---|---|
| Auth, limits, classes, catalog images | configuration file (`devenv.yaml`, from Helm values) |
| Users, environments, host keys, password hashes, tokens, server keys, console images | database (`internal/store`) |
| Pods, Services, `<env>-ssh` Secrets, home PVCs | Kubernetes, derived by the controller |

Environment records have a spec (written by the API) and a status (written by the controller
only) with optimistic concurrency. The controller reacts to database changes (in-process
notifications plus polling for other replicas), to labelled Kubernetes objects and to a
periodic resync. Child objects carry labels, not owner references. Deleting an environment is
explicit; home volumes are never deleted otherwise.

## Packages

```
cmd/devenv            server
cmd/devenv-agent      helper inside environments (exec, processes, PTYs, search, stats)
internal/store        database
internal/model        domain types and resource defaults
internal/catalog      classes and images (configuration + database)
internal/controller   environments -> Kubernetes, image prepull
internal/api          REST API, terminal and VNC proxies, SSE stream
internal/mcpserver    MCP tools
internal/gateway      SSH gateway
internal/auth, users, tokens, access   login, OIDC, permissions
charts/devenv         Helm chart
web                   React console (embedded with go:embed)
images                environment images
```

## Not done yet

- Port preview URLs (`<port>-<env>.<domain>`). They need wildcard DNS, certificates, a host
  based proxy and a cookie scope design. The console shows dedicated IP links and `ssh -L`
  commands instead.
- NetworkPolicy isolation between environments. It should land before multi-user setups are
  opened to people who do not trust each other.

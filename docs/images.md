# Environment images

Any image can be used as a devenv environment if it follows this contract.
The official images (`ghcr.io/tinyrack-net/devenv-base`, `devenv-desktop`)
implement it and are good starting points (`FROM ghcr.io/tinyrack-net/devenv-base`).

## Contract

| Item | Requirement |
|---|---|
| User | `dev`, uid/gid 1000 |
| Home | `/home/dev` is mounted from a volume; populate it on first start |
| SSH | sshd on port 22, user `dev`, public key authentication |
| Authorized keys | read `/etc/devenv/ssh/authorized_keys` (use `AuthorizedKeysCommand`; Secret volumes fail `StrictModes`) |
| Host key | use `/etc/devenv/ssh/ssh_host_ed25519_key` (stable per environment; the server pins it) |
| Password (optional) | `/etc/devenv/ssh/password_hash` holds a bcrypt hash or is empty; apply it to `dev` in `/etc/shadow` (empty = locked) and allow `PasswordAuthentication`. For instant changes, provide `/usr/local/bin/devenv-password-sync --stdin` (the server pipes the new hash to it over SSH) |
| Desktop (optional) | VNC (RFB) on `127.0.0.1:5901`, no password. Set `desktop: true` on the DevImage |

The devenv server reaches the environment only through SSH. The web
terminal is an SSH session and the desktop is an SSH tunnel to
`127.0.0.1:5901`, so VNC must not listen on other interfaces.

Nothing else is required for the MCP tools: the server mounts its helper
(`devenv-agent`) read-only at `/opt/devenv`, so do not use that path in images.

## Environment variables

| Variable | Description |
|---|---|
| `DEVENV_NAME` | Environment name |
| `DEVENV_OWNER` | Owner user name |
| `DEVENV_DESKTOP` | `true` when the DevImage is a desktop image |

## Extending the official images

Keep `/usr/local` and `/usr/local/bin` owned by root and not writable by others: sshd refuses the
`AuthorizedKeysCommand` otherwise (the entrypoint repairs this at start). Use
`tar --no-same-owner --no-overwrite-dir` when extracting archives there.

The base entrypoint sources every `/etc/devenv/entrypoint.d/*.sh` (as root)
before starting sshd. Start long running processes in the background:

```dockerfile
FROM ghcr.io/tinyrack-net/devenv-base:latest
RUN apt-get update && apt-get install -y --no-install-recommends golang && rm -rf /var/lib/apt/lists/*
COPY my-hook.sh /etc/devenv/entrypoint.d/60-my-hook.sh
```

Register the image in the console (Images → Add image) or with a manifest:

```yaml
apiVersion: devenv.tinyrack.net/v1alpha1
kind: DevImage
metadata:
  name: go
spec:
  image: ghcr.io/example/devenv-go:1.0
  displayName: Go
  defaultResources: { cpu: "2", memory: 4Gi }
```

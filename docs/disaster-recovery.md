# Disaster recovery

devenv has no database. Its state is Kubernetes objects and home volumes:

| State | Where | Backed up by |
|---|---|---|
| Users (roles, SSH keys, limits, OIDC links) | `DevUser` | `devenv backup` |
| Images, classes | `DevImage`, `DevEnvironmentClass` | `devenv backup` (and usually Helm/GitOps) |
| Environments (image, resources, sharing, class) | `DevEnvironment` | `devenv backup` |
| Environment SSH host keys and passwords | `<env>-ssh`, `<env>-password` Secrets | `devenv backup` |
| Server keys (gateway host key, session key, legacy MCP token), access tokens | `devenv-keys`, `devenv-token-*` Secrets | `devenv backup` |
| Home volumes (`/home/dev`) | PVC `<env>-home` | your storage system |

## Backups

Enable the daily metadata backup in the chart:

```yaml
backup:
  enabled: true
  schedule: "15 3 * * *"
  dest: s3://my-bucket/devenv
  keep: 14
  s3:
    endpoint: https://s3.example.com
    existingSecret: devenv-backup-s3   # keys AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY
```

The backup contains Secrets (host keys, password hashes, token hashes, the session key): keep
the bucket private.

Back up home volumes with the storage system. Select them with class labels, which devenv
copies onto every home PVC:

```yaml
defaultClass:
  spec:
    storage:
      labels:
        recurring-job-group.longhorn.io/longhorn-backup: enabled   # Longhorn example
```

Velero or CSI snapshots work as well; select PVCs with the label
`devenv.tinyrack.net/component: home`.

## Restore

1. Install devenv with the same values (chart, classes, images).
2. Restore the home volumes with the storage system: PVC `<env>-home` in the environment
   namespace, bound to the restored volume. Restored PVCs may still reference the old
   environment as owner; devenv adopts them.
3. Restore the metadata:

   ```sh
   kubectl -n devenv-system create job devenv-restore --from=cronjob/devenv-backup --dry-run=client -o yaml \
     | sed 's/- backup$/- restore/' | kubectl apply -f -
   # or run the image directly:
   devenv restore --dest s3://my-bucket/devenv --name latest
   ```

   Restore creates server Secrets, users, images, class access lists, environment Secrets and
   environments. Environments start stopped and wait for their home volume
   (annotation `devenv.tinyrack.net/await-volume`) instead of creating an empty one; those
   that were running start once their volume is bound (`--volume-timeout`, default 30m).
   `--dry-run` lists what a backup contains; `--overwrite` replaces existing objects.

What survives a restore:

- `/home/dev` (from the storage backup)
- SSH host keys of the gateway and of every environment: clients see no host key warning
- Dedicated LAN addresses: multus MACs are recorded and pinned, so DHCP hands out the same
  lease
- Access tokens and the legacy MCP token; console sessions (same session key)

Not restored: background processes and terminal sessions (they live in the container), files
outside `/home/dev`.

An environment left waiting for a volume that will not come back can start empty by removing
the annotation: `kubectl -n devenv annotate denv <name> devenv.tinyrack.net/await-volume-`.

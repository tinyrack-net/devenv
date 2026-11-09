# Disaster recovery

devenv's state has three owners, each restored by the tool that already backs it up:

| State | Where | Restored by |
|---|---|---|
| Deployment: auth, classes, catalog images, limits | Helm values (`devenv.yaml`) | Git / Helm |
| Users, environments, SSH host keys, passwords, multus MACs, access tokens, server keys, console created images | Database (SQLite or PostgreSQL) | Database backup |
| Home directories (`/home/dev`) | PVC `<env>-home` per environment | Storage backup |

Pods, services and the `<env>-ssh` secrets are derived from the database by the
controller and need no backup.

## Backups

**Database.**

- PostgreSQL: use the database's own backups (for example CloudNativePG with
  barman-cloud and point in time recovery).
- SQLite: back up the PVC `devenv-data` with your storage system. Select it with
  `database.sqlite.labels`, e.g. Longhorn:

  ```yaml
  database:
    sqlite:
      labels:
        recurring-job.longhorn.io/source: enabled
        recurring-job-group.longhorn.io/default: enabled
  ```

**Home volumes.** Classes add labels to every home PVC, so storage backups can select
them:

```yaml
defaultClass:
  spec:
    storage:
      labels:
        recurring-job.longhorn.io/source: enabled
        recurring-job-group.longhorn.io/default: enabled
```

Velero or CSI snapshots work as well: select PVCs labelled
`devenv.tinyrack.net/component: home`.

The database holds secrets (host keys, password and token hashes, the session key):
keep its backups private.

## Restore

There is no devenv specific step. Restore in any order:

1. Reinstall devenv with the same values (GitOps does this).
2. Restore the database.
3. Restore the home volumes (`<env>-home` in the environment namespace).

The controller then recreates every environment from the database:

- **An environment never starts with an empty home directory by accident.** The database
  records that a home volume was created. While that volume is missing (the storage
  restore is still running), the environment stays `Pending` with reason
  `AwaitingVolume`. Once the PVC is back, the controller adopts it and starts the
  environment.
- **SSH host keys come from the database.** Clients see no host key warnings.
- **Multus MACs are pinned in the database.** DHCP hands out the same addresses.
- **Access tokens, the legacy MCP token and console sessions keep working**, because the
  server keys are in the database.

If a home volume is gone for good, an administrator can start the environment with an
empty volume: Administration → Environments → *Reset volume*
(`POST /api/v1/admin/environments/<name>/reset-volume`).

**Database restored to an earlier point.** Objects of environments created after that
point have no record. devenv leaves them alone, because their volumes may hold the only
copy of someone's work. Administration → System lists them as orphans. Recreate an
environment with the same name to adopt its volume, or delete the orphan.

Not restored: running processes and terminal sessions (they live in the container) and
files outside `/home/dev`.

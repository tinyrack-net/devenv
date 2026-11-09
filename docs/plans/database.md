# Database as the source of truth (0.11)

## Goal

Disaster recovery without devenv specific tooling. A cluster that restores
deployments from Git (Flux, Helm), volumes from storage backups (Longhorn) and
databases from database backups (CloudNativePG) must bring devenv back
completely: users, environments, keys, tokens and home directories, with the
same SSH host keys and LAN addresses.

Today devenv keeps its state in CRDs and Secrets, which none of those
mechanisms restore, so 0.10 added `devenv backup/restore`. 0.11 replaces that
with a database.

## Ownership

| Owner | Data | Restored by |
|---|---|---|
| Configuration file (`devenv.yaml`, Helm values) | auth, limits, **classes**, **catalog images** | Git / Helm |
| Database | users, environments (spec, host keys, password hashes, pinned MACs), console created images, access tokens, server keys, legacy MCP token | database backup |
| Storage | home volumes (`<env>-home`) | storage backup |
| Kubernetes (derived) | Pods, Services, PVCs, `<env>-ssh` Secrets | the controller, from the database |

Everything Kubernetes holds is rebuilt from the database, except home
volumes, which are adopted.

## Removed

- CRDs `DevEnvironment`, `DevUser`, `DevImage`, `DevEnvironmentClass`, the
  `api/v1alpha1` package, controller-gen and the generated manifests.
  Classes and catalog images move into the configuration file
  (`classes:`, `images:`), which the chart renders from the existing values
  (`defaultClass`, `extraClasses`, `defaultImages`, `extraImages`).
- Secrets `devenv-keys`, `devenv-token-*`, `<env>-password`;
  `spec.passwordSecretName` (password Secrets referenced by name).
- `devenv backup/restore`, the backup CronJob, `internal/backup`, the
  await-volume annotation and owner reference adoption (no owner objects
  anymore).
- Console editing of class access lists (classes are deployment
  configuration, like auth settings).

## Databases

`database.driver`:

- `sqlite` (default): file on a PVC, `modernc.org/sqlite` (pure Go, no cgo).
  One replica. Backed up with the PVC.
- `postgres`: `database.url` (or `urlFile`), `pgx`. Point in time recovery
  with the database's own tooling (CloudNativePG in the homelab).

Both use `database/sql` with the same SQL. Statements use `?` placeholders,
rewritten to `$n` for Postgres. Schema migrations are numbered and run at
start in a transaction; a database with a newer schema than the binary stops
the server.

Tables keep the domain objects as JSON with the columns needed for lookups:

```
users(name PK, data, revision)
environments(name PK, owner, data, status, revision)
images(name PK, owner, data, revision)
tokens(id PK, user_name, data)
kv(key PK, value)            -- server keys, migration markers
schema_migrations(version)
```

Writes use optimistic concurrency (`revision`). Environment status (phase, IP,
pod, node) is a separate column written only by the controller, so status
updates never conflict with user edits. `generation` in the environment data
increases on spec changes and `status.observedGeneration` follows it, as with
CRDs.

## Controller

A controller-runtime controller keyed by environment name. Sources:

- the store: in-process notifications for writes on this replica, plus a
  2 s poll of the environment table for writes from other replicas;
- Pods, Services, PVCs and Secrets labelled `devenv.tinyrack.net/environment`;
- a periodic resync.

Child objects carry labels, not owner references. Deletion is explicit: the
API marks the environment `deleting`, the controller removes its Pod,
Services, Secret and PVC, then the row.

Safety rules:

- **Home volumes are never deleted implicitly.** Only an explicit delete of
  the environment removes its PVC.
- **No empty volume for an environment that had one.** The database records
  `volumeCreated`. If the PVC is missing for such an environment (the
  database came back before the storage), the environment waits
  (`Pending`, reason `AwaitingVolume`) instead of starting empty. Admins can
  reset the volume from the console to start empty.
- **Unknown objects are left alone.** Labelled objects without a database
  row (the database was restored to an earlier point) are not deleted; the
  admin system page lists them with a delete action.
- The controller stops if the database is unreachable or its schema is newer.

The pinned multus MAC and the SSH host key are stored in the database, so a
restored environment keeps its DHCP lease and clients see no host key change.

## Server

- API, MCP, SSH gateway, dialer, users and tokens read and write the store.
- Server keys (gateway host key, client key, session key, legacy MCP token)
  live in the `kv` table, generated on first start. `mcp.legacyToken.tokenFile`
  still overrides the token.
- The SSE stream follows the store (same in-process notifications and poll).
- Single replica with SQLite. With Postgres several replicas serve the API;
  the controller runs on the leader (Lease).

## Migration from 0.10

On first start with an empty database (marker `migrated-crds` in `kv`), the
server imports, if the CRDs still exist (read as unstructured objects):

1. `devenv-keys` (server keys, legacy MCP token) and `devenv-token-*`.
2. `DevUser`s.
3. `DevImage`s that are not managed by Helm (personal and console created
   images). Helm managed ones come from the configuration now.
4. `DevEnvironment`s with their host keys (`<env>-ssh`), password hashes
   (`<env>-password` or the referenced Secret), pinned MAC (status or the
   current MAC), `volumeCreated` when the PVC exists.
5. Removes `DevEnvironment` owner references from Pods, Services, PVCs and
   Secrets, so deleting the old CRDs does not garbage collect them.

Classes with an access list in the cluster but not in the configuration are
logged. After the upgrade the CRDs can be deleted
(`kubectl delete crd -l app.kubernetes.io/part-of=devenv` is documented).

## Chart

```yaml
database:
  driver: sqlite          # sqlite | postgres
  sqlite:
    persistence:
      size: 1Gi
      storageClassName: ""
      existingClaim: ""
      labels: {}          # e.g. storage backup selection
  postgres:
    url: ""               # or existingSecret/key
    existingSecret: ""
    secretKey: uri        # CloudNativePG app secret key
```

SQLite uses a `Recreate` deployment and one replica. The `backup.*` values
and the CronJob are removed. Classes and images render into `devenv.yaml`.

## Homelab

- CloudNativePG cluster `devenv-database` in `devenv-system` (PostgreSQL 18,
  `longhorn-strict-local`, barman-cloud backups to
  `s3://tinyrack-homelab/apps/devenv/database-18`), first with
  `bootstrap.initdb`, switched to `bootstrap.recovery` after the first base
  backup like the other apps.
- devenv uses the CNPG `devenv-database-app` secret (`uri`).
- Network policies: devenv to the database, the database to Garage.
- Remove the metadata backup CronJob values and its Traefik policy.
- Keep the Longhorn backup labels on home volumes.
- Readme: devenv recovers with Flux + Longhorn + CNPG, no manual step.

## Order of work

1. `internal/model`: domain types (from `api/v1alpha1`), resource defaults.
2. `internal/store`: schema, SQLite and Postgres, tests against both.
3. Configuration: `classes`, `images`, `database`.
4. Users, tokens, server keys on the store.
5. Controller rewrite.
6. API, MCP, gateway, dialer, stream on the store.
7. Migration from CRDs.
8. Remove CRDs, `api/v1alpha1`, backup, controller-gen; chart.
9. Web console adjustments (managed images, read-only class access, orphans).
10. Tests (unit, envtest, Postgres), kind upgrade from 0.10 with migration and
    a recovery drill (delete everything but the database and volumes).
11. Release 0.11.0, homelab rollout.

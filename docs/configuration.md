# Configuration

devenv separates settings by who owns them:

| Layer | What | Where | Changed with |
|---|---|---|---|
| Deployment | Login methods, sessions, administrators, MCP, SSH address, default user limits, **classes**, **catalog images**, database | `devenv.yaml` (the chart renders it from values into a ConfigMap; secrets come from mounted Secrets) | Helm / GitOps. The console shows it read only (Administration → System) |
| Platform | Users, shared images added in the console | Database | Console (admins) |
| Personal | Environments, SSH keys, access tokens, preferences, personal images | Database | Console, API, MCP |

The database (SQLite on a PVC by default, or PostgreSQL) is the source of truth for
everything users create. Kubernetes objects of environments (pods, services, volumes,
secrets) are derived from it by the controller. See
[disaster-recovery.md](disaster-recovery.md).

## Helm values

```yaml
externalURL: https://devenv.example.com   # required for OIDC
secureCookies: true

auth:
  mode: password            # "none" disables login (development only)
  adminUsername: admin       # local administrator, a fallback next to OIDC
  passwordLogin: true        # false hides the local login
  existingSecret: ""         # key "password"; generated when empty
  admins: [alice, bob@example.com]   # always administrators
  sessionTTL: 168h
  oidc:
    - id: sso                # used in the callback URL and user identities
      name: Company SSO
      issuer: https://sso.example.com
      clientID: devenv
      existingSecret: devenv-oidc   # Secret with the client secret
      secretKey: client-secret      # default
      scopes: [openid, profile, email, groups]
      claims:                       # defaults shown
        username: preferred_username
        email: email
        name: name
        groups: groups
      autoCreate: true              # create users on first login
      allowedGroups: []             # empty: everyone the IdP lets in
      allowedEmails: []             # "*@example.com" matches a domain
      adminGroups: [devenv-admins]

users:
  defaultLimits:             # empty: unlimited
    maxEnvironments: 5
    maxRunning: 3
    cpu: "8"
    memory: 32Gi
    disk: 200Gi
  adminLimits: null          # admins are unlimited unless set

mcp:
  enabled: true              # agents use personal access tokens

database:
  driver: sqlite             # or postgres
  sqlite:
    size: 1Gi
    storageClassName: ""
    labels: {}               # e.g. select the PVC for storage backups
  postgres:
    existingSecret: ""       # Secret with the URL (CloudNativePG: <cluster>-app)
    secretKey: uri

defaultClass:                # the class "default"
  create: true
  spec: {storage: {mode: persistent, defaultSize: 10Gi}, network: {mode: gateway}}
extraClasses: {}             # name -> class spec
defaultImages: {...}         # base, desktop, agent
extraImages: {}              # name -> image spec

config: {}                   # merged last into devenv.yaml (any field below)
```

SQLite runs a single replica (the old pod stops before the new one starts). With PostgreSQL `replicaCount`
can be raised; the controller runs on the elected leader.

Register the client at the identity provider with the redirect URL
`<externalURL>/api/v1/auth/oidc/<id>/callback`, the authorization code grant and
PKCE (S256).

## devenv.yaml

The server reads `--config` (`DEVENV_CONFIG`). Unknown fields are rejected so typos
fail at start. Secret values use `*File` fields that point at mounted files.

```yaml
server:
  externalURL: https://devenv.example.com
auth:
  disabled: false
  admins: []
  session: {ttl: 168h, secureCookies: true}
  password:
    enabled: true
    username: admin
    passwordFile: /etc/devenv/secrets/admin/password
  oidc:
    - id: sso
      issuer: https://sso.example.com
      clientID: devenv
      clientSecretFile: /etc/devenv/secrets/oidc-sso/client-secret
users:
  defaultLimits: {maxEnvironments: 5}
mcp:
  enabled: true
ssh:
  publicHost: ssh.example.com
  publicPort: 2222
database:
  driver: postgres
  urlFile: /etc/devenv/secrets/database/url   # or url:, or driver: sqlite + path:
classes:
  default:
    storage:
      mode: persistent           # or ephemeral
      storageClassName: longhorn
      defaultSize: 20Gi
      labels: {}                 # added to home volume PVCs
      annotations: {}
    network:
      mode: gateway              # gateway | loadBalancer | multus
    limits: {maxCPU: "8", maxMemory: 16Gi, maxDisk: 100Gi}
    scheduling: {nodeSelector: {}, tolerations: [], runtimeClassName: null}
    access: {groups: [], users: []}   # empty: everyone
    podOverrides: {}             # strategic merge patch for the pod
images:
  base:
    image: ghcr.io/tinyrack-net/devenv-base:0.11.0
    displayName: Ubuntu 24.04
    desktop: false
    prepull: true
    defaultResources: {cpu: "2", memory: 4Gi}
    access: {groups: []}
```

Images from the configuration are read-only in the console. Changing a class changes
existing environments on their next reconcile (pods are recreated when their spec
changes).

## Users and permissions

- Users are stored in the database. OIDC logins create them (unless `autoCreate: false`).
  Admins can create users in advance; the first OIDC login with a verified matching
  e-mail links to that user.
- Administrators: role admin (console), listed in `auth.admins`, in an `adminGroups`
  group, or the local account.
- Environments belong to their owner. Owners share them with users or groups: shared
  users can open terminals and the desktop, connect over SSH and start or stop the
  environment. Only the owner and administrators can change or delete it.
- SSH keys belong to users and work for every environment the user can use, through
  the gateway and on dedicated addresses.
- Shared images come from the configuration or are added by administrators in the
  console, and can be limited to groups or users (`access`). Users can add personal
  images.
- Classes can be limited to groups or users (`access` in the configuration), e.g. a
  `lan` class with dedicated LAN addresses only for some people.
- Limits are checked when environments are created, started or resized.
- Disabling a user rejects their sessions, tokens and SSH keys immediately.

## Upgrades

devenv keeps no backward compatibility: no deprecated settings, no automatic data
migrations. Each release note lists the manual steps for breaking changes.

The database schema is created on an empty database only. When `store.SchemaVersion`
changes, the server refuses to start until the schema change is applied by hand
(including the `schema_migrations` row).

# Configuration

devenv separates settings by who owns them:

| Layer | What | Where | Changed with |
|---|---|---|---|
| Deployment | Login methods, sessions, administrators, MCP, SSH address, default user limits | `devenv.yaml` (the chart renders it from values into a ConfigMap; secrets come from mounted Secrets) | Helm / GitOps. The console shows it read only (Administration → System) |
| Platform | Shared images, classes, users | CRDs `DevImage`, `DevEnvironmentClass`, `DevUser` | Console (admins) or GitOps |
| Personal | SSH keys, access tokens, preferences, personal images | `DevUser`, token Secrets, `DevImage` with `spec.owner` | Console |

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
    - id: sso                # used in the callback URL and DevUser identities
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
  enabled: true
  legacyToken: true          # the single token of earlier versions
  legacyTokenUser: ""        # user it acts as (default: the local admin)
  existingSecret: ""         # key "token"; generated when empty

config: {}                   # merged last into devenv.yaml (any field below)
```

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
  legacyToken: {enabled: true, user: admin, tokenFile: /etc/devenv/secrets/mcp/token}
ssh:
  publicHost: ssh.example.com
  publicPort: 2222
```

Environment variables of earlier versions (`DEVENV_AUTH_MODE`, `DEVENV_ADMIN_USERNAME`,
`DEVENV_ADMIN_PASSWORD[_FILE]`, `DEVENV_SECURE_COOKIES`, `DEVENV_MCP_ENABLED`,
`DEVENV_MCP_TOKEN[_FILE]`, `DEVENV_SSH_PUBLIC_HOST`, `DEVENV_SSH_PUBLIC_PORT`) still work
and override the file.

## Users and permissions

- Users are `DevUser` objects. OIDC logins create them (unless `autoCreate: false`).
  Admins can create users in advance; the first OIDC login with a verified matching
  e-mail links to that user.
- Administrators: `spec.role: admin`, listed in `auth.admins`, in an `adminGroups`
  group, or the local account.
- Environments belong to their owner. Owners share them with users or groups: shared
  users can open terminals and the desktop, connect over SSH and start or stop the
  environment. Only the owner and administrators can change or delete it.
- SSH keys belong to users and work for every environment the user can use, through
  the gateway and on dedicated addresses.
- Shared images are managed by administrators and can be limited to groups or users
  (`spec.access`). Users can add personal images (`spec.owner`).
- Classes can be limited to groups or users (`spec.access`), e.g. a `lan` class with
  dedicated LAN addresses only for some people.
- Limits are checked when environments are created, started or resized.
- Disabling a user rejects their sessions, tokens and SSH keys immediately.

## Upgrading from 0.8

Nothing to change. At start the server creates the local admin `DevUser`, moves SSH
keys from the `devenv-user-*` ConfigMaps to it, and assigns environments created by
MCP (owner `mcp`) to the legacy token user. The single MCP token keeps working as
that user; issue personal tokens in the console and then set `mcp.legacyToken: false`.

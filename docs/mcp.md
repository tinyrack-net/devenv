# MCP server

devenv serves a [Model Context Protocol](https://modelcontextprotocol.io) endpoint so AI agents can
create their own development environments and run commands in them.

- Endpoint: `<console URL>/mcp` (Streamable HTTP, stateless)
- Auth: `Authorization: Bearer <token>`

```sh
kubectl -n devenv-system get secret devenv-keys -o jsonpath='{.data.mcp-token}' | base64 -d; echo
```

The token has full access (same as the console admin). To rotate it, delete the `mcp-token`
key from the Secret and restart the server, or set `mcp.existingSecret` to your own Secret
(key `token`).

## Tools

| Tool | Description |
|---|---|
| `list_images` | Images available for new environments |
| `list_classes` | Classes: storage, network mode and limits |
| `get_capacity` | Largest CPU / memory / disk currently available in a class |
| `list_environments` | Environments (`created_by_mcp_only` filter) |
| `get_environment` | State, resources, address, SSH command |
| `create_environment` | Create; waits until running by default |
| `update_environment` | Change image, CPU, memory, disk, ports, idle timeout |
| `start_environment` / `stop_environment` | Start / stop (keeps `/home/dev`) |
| `delete_environment` | Delete including the home volume |
| `exec` | Run a command: `bash -lc` as `dev` |

### exec

| Argument | Default | |
|---|---|---|
| `name`, `command` | required | |
| `cwd` | `/home/dev` | working directory |
| `env` | | extra variables |
| `stdin` | | up to 1 MB |
| `timeout_seconds` | 120 | max 1800; the whole process group is killed on timeout |
| `auto_start` | true | start a stopped environment first |

Result: `exit_code` (-1 on timeout or signal), `stdout`, `stderr` (256 KB each; the middle is
omitted beyond that, `truncated: true`), `timed_out`, `duration_ms`.

Each call is a new shell. At most 4 concurrent calls per environment.

## Idle auto-stop

Environments created through MCP get `idleTimeout` = `mcp.defaultIdleTimeout` (chart value,
default `1h`). After that long without activity the environment is stopped (data in
`/home/dev` is kept) and `exec` starts it again. Pass `idle_timeout: "off"` to disable it or
a duration such as `"4h"`.

Activity: MCP calls for the environment, web terminal, desktop and SSH gateway sessions.
Direct SSH to a dedicated IP is not observed.

## Clients

Any MCP client supporting Streamable HTTP with headers works, for example:

```json
{
  "mcpServers": {
    "devenv": {
      "type": "http",
      "url": "https://devenv.example.com/mcp",
      "headers": { "Authorization": "Bearer dvm_..." }
    }
  }
}
```

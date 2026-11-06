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
| `update_environment` | Change image, CPU, memory, disk, ports |
| `start_environment` / `stop_environment` | Start / stop (keeps `/home/dev`) |
| `delete_environment` | Delete including the home volume |
| `exec` | Run a command: `bash -lc` as `dev` |
| `read_file` | Read lines with line numbers (`offset`, `limit`) |
| `write_file` | Create or replace a file (parents created, atomic) |
| `edit_file` | Exact text replacement, multiple edits, returns a diff |
| `list_dir` | Directory listing |
| `search` | ripgrep content search (grep fallback), respects `.gitignore` |
| `find_files` | Find files by glob |
| `start_process` | Run a long command in the background (dev servers, builds) |
| `read_process_output` | Output tail or new output since `next_offset`, optional wait |
| `list_processes` / `stop_process` | Background process status / stop the process group |

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

### Files

File tools use SFTP as `dev`. Relative paths (and `~/`) resolve against `/home/dev`. `edit_file`
requires each `old_text` to match exactly once (or `replace_all`), rejects overlapping edits,
keeps the file mode and CRLF line endings, and writes atomically.

### Background processes

`start_process` detaches the command (its own process group) and logs stdout+stderr to
`/tmp/devenv-proc/<id>/output`. Use `wait_seconds` and `wait_for` (e.g. `"listening on"`) to wait
for readiness, and `read_process_output` with `offset: next_offset` to follow new output.
`stop_process` sends SIGTERM then SIGKILL to the whole group. State lives in `/tmp`, so it is
lost when the environment restarts.

Some MCP gateways (e.g. Bifrost code mode) cut a tool call after 60 seconds; use background
processes for anything longer.

## Images

The chart registers an `agent` image (`ghcr.io/tinyrack-net/devenv-agent`): the base image plus
Go, Node.js with corepack (pnpm, yarn), Python with uv and the GitHub CLI. ripgrep and fd are in
every official image.

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

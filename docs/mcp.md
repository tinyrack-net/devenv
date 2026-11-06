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
| `get_environment` | State, address, SSH command, usage (memory, `/home/dev` disk), restarts, last crash/OOM, warnings |
| `create_environment` | Create; waits until running by default |
| `update_environment` | Change image, CPU, memory, disk, ports |
| `start_environment` / `stop_environment` | Start / stop (keeps `/home/dev`) |
| `delete_environment` | Delete including the home volume |
| `restart_environment` | Fresh container, `/home/dev` kept |
| `exec` | Run a command: `bash -lc` as `dev` |
| `read_file` | Read lines with line numbers (`offset`, `limit`); images are returned as images; `encoding: base64` for binary |
| `write_file` | Create or replace a file (parents created, atomic); `encoding: base64` for binary |
| `edit_file` | Exact text replacement, multiple edits, returns a diff |
| `list_dir` | Directory listing |
| `search` | ripgrep content search (grep fallback), respects `.gitignore` |
| `find_files` | Find files by glob |
| `start_process` | Run a long command in the background (dev servers, builds) |
| `read_process_output` | Output tail or new output since `next_offset`, optional wait |
| `list_processes` / `stop_process` | Background process status / stop the process group |
| `desktop_screenshot` | Screenshot of a desktop environment |
| `desktop_click` / `desktop_scroll` | Mouse at screenshot coordinates (double/right click, drag) |
| `desktop_type` / `desktop_key` | Type text (Unicode) / key combinations like `ctrl+s` |

### How tools run

Environment images only need sshd and sftp-server (plus a VNC server on `127.0.0.1:5901` for
desktops). Everything else runs on the server or in `devenv-agent`, a static helper the server
mounts read-only at `/opt/devenv` (image volume of the server image, `helper.enabled`):

- `exec` runs `bash -lc` as `dev` in its own process group: exact exit codes and signals, a
  timeout (default 120s, max 1800s) kills the whole tree, and `oom_killed` is set when the
  kernel killed it for memory (or the whole environment restarted). Background children
  (`app &`) keep running. Output: 256KB per stream, the middle is omitted beyond that.
- Background processes belong to a small daemon of the helper: exact exit status, the last 8MB
  of output, and waits (`wait_seconds`, `wait_for`) happen inside the environment.
- `search` / `find_files` walk the tree in the helper (Go RE2 regexps, `.gitignore`, hidden
  files, `.git`, `node_modules` and binary files skipped by default).
- File tools use SFTP. `edit_file` requires each `old_text` to match exactly once (or
  `replace_all`), rejects overlapping edits, keeps file mode and CRLF, and writes atomically.
- Images from `read_file` and screenshots are scaled to 1568px / 1MB on the server.
- Desktop tools speak VNC to the environment through the SSH tunnel.

The server keeps one SSH connection per environment and reuses it (a call costs ~25ms instead
of ~60ms for a new connection).

Without the helper (custom pod specs, clusters without image volumes) `exec` falls back to a
plain shell; search and background processes report that the helper is unavailable.

## Images

The chart registers an `agent` image (`ghcr.io/tinyrack-net/devenv-agent`): the base image plus
Go, Node.js with corepack (pnpm, yarn), Python with uv and the GitHub CLI. It is a convenience;
MCP tools work with any image.

DevImages with `prepull: true` (chart default for `base` and `agent`) are kept on every node by
a DaemonSet that mounts them as image volumes, so new environments do not wait for a pull.

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

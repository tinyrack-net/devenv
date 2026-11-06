// Package mcpserver exposes devenv to AI agents over the Model Context
// Protocol (Streamable HTTP at /mcp).
//
// Agents can list images and classes, create, update, start, stop and delete
// environments, and run shell commands in them. CRUD goes through the same
// service functions as the REST API, so validation, class limits and
// capacity checks are identical.
package mcpserver

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tinyrack-net/devenv/internal/api"
	"github.com/tinyrack-net/devenv/internal/sshclient"
)

const (
	// Owner recorded on environments created through MCP.
	Owner = "mcp"
	// LabelCreatedBy marks environments created through MCP.
	LabelCreatedBy = "devenv.tinyrack.net/created-by"
)

// Server implements the MCP tools.
type Server struct {
	API    *api.Server
	Dialer *sshclient.Dialer
	// Token is the bearer token accepted on /mcp.
	Token   string
	Version string

	execLimiter *limiter
}

const instructions = `devenv provides isolated Linux development environments (containers on Kubernetes) for coding work.
Workflow:
1. create_environment with a unique name. Prefer the "agent" image when it exists (Go, Node.js/pnpm, Python/uv, gh, ripgrep preinstalled). It waits until running.
2. Work inside it. The user is "dev" (home /home/dev, passwordless sudo). Relative paths are relative to /home/dev.
   - Files: read_file (line numbers + offset/limit), edit_file (exact text replacement, returns a diff), write_file (new files or full rewrites), list_dir.
   - Code search: search (ripgrep, respects .gitignore), find_files (glob).
   - Commands: exec for commands that finish within about a minute (builds, tests, git).
   - Long running work (dev servers, watchers, long builds/tests): start_process, then read_process_output with next_offset / wait_seconds; stop_process when done.
3. Only /home/dev persists across stop/start; packages installed elsewhere and background processes are lost on restart.
4. stop_environment to pause (frees CPU/memory) or delete_environment when finished. exec and the file tools start a stopped environment automatically.`

// Handler returns the authenticated Streamable HTTP handler.
func (s *Server) Handler() http.Handler {
	if s.execLimiter == nil {
		s.execLimiter = newLimiter(4)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "devenv", Title: "devenv", Version: s.Version}, &mcp.ServerOptions{
		Instructions: instructions,
	})
	s.register(srv)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		// Requests come through ingress/Bifrost with arbitrary Host headers.
		DisableLocalhostProtection: true,
	})
	return s.authenticate(h)
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || s.Token == "" || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), []byte(s.Token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="devenv"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

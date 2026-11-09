// Package mcpserver exposes devenv to AI agents over the Model Context
// Protocol (Streamable HTTP at /mcp).
//
// Agents can list images and classes, create, update, start, stop and delete
// environments, and run shell commands in them. CRUD goes through the same
// service functions as the REST API, so validation, class limits and
// capacity checks are identical.
package mcpserver

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tinyrack-net/devenv/internal/activity"
	"github.com/tinyrack-net/devenv/internal/api"
	"github.com/tinyrack-net/devenv/internal/auth"
	"github.com/tinyrack-net/devenv/internal/controller"
	"github.com/tinyrack-net/devenv/internal/sshclient"
	"github.com/tinyrack-net/devenv/internal/tokens"
)


// Server implements the MCP tools.
type Server struct {
	API    *api.Server
	Dialer *sshclient.Dialer
	// Resolver verifies bearer tokens (scope "mcp").
	Resolver auth.Resolver
	Version  string
	// Activity receives tool calls. May be nil.
	Activity *activity.Log

	// Pool reuses SSH connections to environments. Created on demand.
	Pool *sshclient.Pool

	execLimiter *limiter
	poolOnce    sync.Once
	desk        desktops
}

func (s *Server) pool() *sshclient.Pool {
	s.poolOnce.Do(func() {
		if s.Pool == nil {
			s.Pool = &sshclient.Pool{Dialer: s.Dialer}
		}
	})
	return s.Pool
}

const instructions = `devenv provides isolated Linux development environments (containers on Kubernetes) for coding work.
Workflow:
1. create_environment with a unique name. Prefer the "agent" image when it exists (Go, Node.js/pnpm, Python/uv, gh, ripgrep preinstalled). It waits until running.
   Environments belong to the user of your access token; list_environments shows yours and those shared with you. A person may watch or join your work from the devenv console.
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

// authenticate resolves the bearer token to a user. Tools act with that
// user's permissions.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		var p *auth.Principal
		var err error
		if ok {
			p, err = s.Resolver.Token(r.Context(), strings.TrimSpace(token), tokens.ScopeMCP)
		}
		if !ok || err != nil || p == nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="devenv"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := auth.WithPrincipal(r.Context(), p)
		ctx = context.WithValue(ctx, activityKey{}, s.Activity)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// containerStatus returns the environment container status, or nil.
func (s *Server) containerStatus(ctx context.Context, name string) *corev1.ContainerStatus {
	pod := &corev1.Pod{}
	if err := s.API.Reader.Get(ctx, client.ObjectKey{Namespace: s.API.Namespace, Name: name}, pod); err != nil {
		return nil
	}
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == controller.ContainerName {
			return &pod.Status.ContainerStatuses[i]
		}
	}
	return nil
}

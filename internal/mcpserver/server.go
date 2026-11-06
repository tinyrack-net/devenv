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
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tinyrack-net/devenv/internal/api"
	"github.com/tinyrack-net/devenv/internal/sshclient"
)

const (
	// Owner recorded on environments created through MCP.
	Owner = "mcp"
	// LabelCreatedBy marks environments created through MCP.
	LabelCreatedBy = "devenv.tinyrack.net/created-by"
)

// Activity records activity on an environment (for idle stop).
type Activity interface {
	Touch(name string)
}

// Server implements the MCP tools.
type Server struct {
	API    *api.Server
	Dialer *sshclient.Dialer
	// Token is the bearer token accepted on /mcp.
	Token string
	// DefaultIdleTimeout applied to environments created through MCP. 0 disables.
	DefaultIdleTimeout time.Duration
	Activity           Activity
	Version            string

	execLimiter *limiter
}

const instructions = `devenv provides isolated Linux development environments (containers on Kubernetes).
Typical workflow:
1. list_images / list_classes to choose an image and class.
2. create_environment with a unique name (lowercase, digits, '-'); it waits until the environment is running.
3. exec to run shell commands (bash -lc as user "dev", home /home/dev, passwordless sudo). Files in /home/dev persist across stop/start; everything else is reset when the environment restarts.
4. stop_environment when idle work is done (keeps data) or delete_environment when finished (deletes everything).
Environments created through MCP stop automatically after a period of inactivity; exec starts them again automatically.`

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

func (s *Server) idleTimeout(override string) (*metav1.Duration, error) {
	if override != "" {
		if override == "0" || override == "off" || override == "none" {
			return nil, nil
		}
		d, err := time.ParseDuration(override)
		if err != nil || d < time.Minute {
			return nil, errorf("invalid idle_timeout %q: use a duration such as 30m or 2h (minimum 1m), or \"off\"", override)
		}
		return &metav1.Duration{Duration: d}, nil
	}
	if s.DefaultIdleTimeout <= 0 {
		return nil, nil
	}
	return &metav1.Duration{Duration: s.DefaultIdleTimeout}, nil
}

func (s *Server) touch(name string) {
	if s.Activity != nil && name != "" {
		s.Activity.Touch(name)
	}
}

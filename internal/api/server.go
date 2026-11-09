// Package api implements the devenv REST API consumed by the web console.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/tinyrack-net/devenv/internal/activity"
	"github.com/tinyrack-net/devenv/internal/auth"
	"github.com/tinyrack-net/devenv/internal/capacity"
	"github.com/tinyrack-net/devenv/internal/catalog"
	"github.com/tinyrack-net/devenv/internal/config"
	"github.com/tinyrack-net/devenv/internal/sshclient"
	"github.com/tinyrack-net/devenv/internal/store"
	"github.com/tinyrack-net/devenv/internal/tokens"
	"github.com/tinyrack-net/devenv/internal/users"
)

// Server serves the REST API and the SPA.
type Server struct {
	// DB is the source of truth.
	DB      *store.Store
	Catalog *catalog.Catalog
	// Reader is an uncached reader for cluster reads (nodes, pods, events,
	// orphaned objects).
	Reader client.Reader
	// Client deletes orphaned objects (admin). May be nil.
	Client    client.Client
	Namespace string
	Config    *config.Config
	Sessions  *auth.Sessions
	Users     *users.Store
	Tokens    *tokens.Store
	// OIDC providers by id.
	OIDC     map[string]*auth.OIDC
	Capacity *capacity.Checker
	// Static serves the SPA. May be nil.
	Static  fs.FS
	Version string
	// Dialer connects to environments for the web terminal and VNC.
	Dialer *sshclient.Dialer
	// Pool shares SSH connections with MCP (helper calls, terminal sessions).
	Pool *sshclient.Pool
	// SSH describes the public SSH gateway endpoint.
	SSH SSHInfo
	// Activity records agent tool calls. May be nil.
	Activity *activity.Log
	// Events fans out environment changes to /api/v1/stream. May be nil.
	Events *Broker
	// HelperImage is shown in the admin system page.
	HelperImage string
	// Handlers mounted under /api/v1/ by other packages (workspace APIs).
	Extra map[string]http.Handler
}

// Middleware returns the authentication middleware for API requests.
func (s *Server) Middleware() *auth.Middleware {
	m := &auth.Middleware{Sessions: s.Sessions, Resolver: s}
	if s.Config.Auth.Disabled {
		m.Anonymous = &auth.Principal{Name: s.Config.Auth.Password.Username, Admin: true, Method: "none"}
	}
	return m
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/me", s.me)
	api.HandleFunc("PATCH /api/v1/me", s.updateMe)
	api.HandleFunc("GET /api/v1/me/usage", s.myUsage)

	api.HandleFunc("GET /api/v1/images", s.listImages)
	api.HandleFunc("POST /api/v1/images", s.createImage)
	api.HandleFunc("GET /api/v1/images/{name}", s.getImage)
	api.HandleFunc("PUT /api/v1/images/{name}", s.updateImage)
	api.HandleFunc("DELETE /api/v1/images/{name}", s.deleteImage)

	api.HandleFunc("GET /api/v1/classes", s.listClasses)
	api.HandleFunc("GET /api/v1/classes/{name}", s.getClass)
	api.HandleFunc("GET /api/v1/classes/{name}/capacity", s.classCapacity)

	api.HandleFunc("GET /api/v1/environments", s.listEnvironments)
	api.HandleFunc("POST /api/v1/environments", s.createEnvironment)
	api.HandleFunc("GET /api/v1/environments/{name}", s.getEnvironment)
	api.HandleFunc("PATCH /api/v1/environments/{name}", s.updateEnvironment)
	api.HandleFunc("DELETE /api/v1/environments/{name}", s.deleteEnvironment)
	api.HandleFunc("POST /api/v1/environments/{name}/start", s.startEnvironment)
	api.HandleFunc("POST /api/v1/environments/{name}/stop", s.stopEnvironment)
	api.HandleFunc("POST /api/v1/environments/{name}/restart", s.restartEnvironment)
	api.HandleFunc("GET /api/v1/environments/{name}/events", s.environmentEvents)
	api.HandleFunc("GET /api/v1/environments/{name}/activity", s.environmentActivity)
	api.HandleFunc("GET /api/v1/environments/{name}/terminal", s.terminal)
	api.HandleFunc("GET /api/v1/environments/{name}/vnc", s.vnc)

	api.HandleFunc("GET /api/v1/sshkeys", s.listSSHKeys)
	api.HandleFunc("POST /api/v1/sshkeys", s.createSSHKey)
	api.HandleFunc("DELETE /api/v1/sshkeys/{id}", s.deleteSSHKey)

	api.HandleFunc("GET /api/v1/tokens", s.listTokens)
	api.HandleFunc("POST /api/v1/tokens", s.createToken)
	api.HandleFunc("DELETE /api/v1/tokens/{id}", s.deleteToken)

	api.HandleFunc("GET /api/v1/users", s.listUsers)
	api.HandleFunc("GET /api/v1/stream", s.stream)

	// Admin.
	api.HandleFunc("POST /api/v1/admin/users", s.adminCreateUser)
	api.HandleFunc("GET /api/v1/admin/users/{name}", s.adminGetUser)
	api.HandleFunc("PATCH /api/v1/admin/users/{name}", s.adminUpdateUser)
	api.HandleFunc("DELETE /api/v1/admin/users/{name}", s.adminDeleteUser)
	api.HandleFunc("GET /api/v1/admin/tokens", s.adminListTokens)
	api.HandleFunc("DELETE /api/v1/admin/tokens/{id}", s.adminDeleteToken)
	api.HandleFunc("GET /api/v1/admin/activity", s.adminActivity)
	api.HandleFunc("GET /api/v1/admin/system", s.adminSystem)
	api.HandleFunc("GET /api/v1/admin/orphans", s.adminOrphans)
	api.HandleFunc("DELETE /api/v1/admin/orphans/{name}", s.adminDeleteOrphan)
	api.HandleFunc("POST /api/v1/admin/environments/{name}/reset-volume", s.adminResetVolume)

	s.registerWorkspace(api)
	for pattern, h := range s.Extra {
		api.Handle(pattern, h)
	}

	root := http.NewServeMux()
	root.HandleFunc("GET /api/v1/info", s.info)
	root.HandleFunc("POST /api/v1/login", s.login)
	root.HandleFunc("POST /api/v1/logout", s.logout)
	root.HandleFunc("GET /api/v1/auth/oidc/{id}/login", s.oidcLogin)
	root.HandleFunc("GET /api/v1/auth/oidc/{id}/callback", s.oidcCallback)
	root.Handle("/api/", s.Middleware().Wrap(api))
	root.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	if s.Static != nil {
		root.Handle("/", spaHandler(s.Static))
	}
	return securityHeaders(root)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// spaHandler serves static files and falls back to index.html for client routes.
func spaHandler(static fs.FS) http.Handler {
	files := http.FileServerFS(static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if f, err := static.Open(p); err == nil {
				_ = f.Close()
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		files.ServeHTTP(w, r2)
	})
}

// principal returns the authenticated caller (always set behind the middleware).
func principal(r *http.Request) *auth.Principal { return auth.From(r.Context()) }

// requireAdmin writes 403 unless the caller is an admin.
func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if p := principal(r); p == nil || !p.Admin {
		writeError(w, http.StatusForbidden, "administrator role required")
		return false
	}
	return true
}

// --- helpers ---

type httpError struct {
	code int
	msg  string
}

func (e *httpError) Error() string { return e.msg }

func badRequest(msg string) error { return &httpError{http.StatusBadRequest, msg} }
func forbidden(msg string) error  { return &httpError{http.StatusForbidden, msg} }

// errNotFound hides resources the caller can not see.
var errNotFound = &httpError{http.StatusNotFound, "not found"}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// fail maps errors to HTTP responses.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	var he *httpError
	switch {
	case errors.As(err, &he):
		writeError(w, he.code, he.msg)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrExists):
		writeError(w, http.StatusConflict, "already exists")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "resource was modified, retry")
	case errors.Is(err, context.Canceled):
	default:
		logf.FromContext(r.Context()).Error(err, "request failed", "path", r.URL.Path)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return false
	}
	return true
}

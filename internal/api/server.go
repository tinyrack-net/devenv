// Package api implements the devenv REST API consumed by the web console.
package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/tinyrack-net/devenv/internal/auth"
	"github.com/tinyrack-net/devenv/internal/capacity"
	"github.com/tinyrack-net/devenv/internal/sshclient"
)

// +kubebuilder:rbac:groups=devenv.tinyrack.net,resources=devimages,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=list

// Server serves the REST API and the SPA.
type Server struct {
	// Client is used for CRD reads (cached) and writes.
	Client client.Client
	// Reader is an uncached reader for cluster wide reads (nodes, pods, events).
	Reader    client.Reader
	Namespace string
	Auth      auth.Authenticator
	Sessions  *auth.Sessions
	Capacity  *capacity.Checker
	// Static serves the SPA. May be nil.
	Static  fs.FS
	Version string
	// Dialer connects to environments for the web terminal and VNC.
	Dialer *sshclient.Dialer
	// SSH describes the public SSH gateway endpoint.
	SSH SSHInfo
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/me", s.me)

	api.HandleFunc("GET /api/v1/images", s.listImages)
	api.HandleFunc("POST /api/v1/images", s.createImage)
	api.HandleFunc("GET /api/v1/images/{name}", s.getImage)
	api.HandleFunc("PUT /api/v1/images/{name}", s.updateImage)
	api.HandleFunc("DELETE /api/v1/images/{name}", s.deleteImage)

	api.HandleFunc("GET /api/v1/classes", s.listClasses)
	api.HandleFunc("GET /api/v1/classes/{name}/capacity", s.classCapacity)

	api.HandleFunc("GET /api/v1/environments", s.listEnvironments)
	api.HandleFunc("POST /api/v1/environments", s.createEnvironment)
	api.HandleFunc("GET /api/v1/environments/{name}", s.getEnvironment)
	api.HandleFunc("PATCH /api/v1/environments/{name}", s.updateEnvironment)
	api.HandleFunc("DELETE /api/v1/environments/{name}", s.deleteEnvironment)
	api.HandleFunc("POST /api/v1/environments/{name}/start", s.startEnvironment)
	api.HandleFunc("POST /api/v1/environments/{name}/stop", s.stopEnvironment)
	api.HandleFunc("GET /api/v1/environments/{name}/events", s.environmentEvents)
	api.HandleFunc("GET /api/v1/environments/{name}/terminal", s.terminal)
	api.HandleFunc("GET /api/v1/environments/{name}/vnc", s.vnc)

	api.HandleFunc("GET /api/v1/sshkeys", s.listSSHKeys)
	api.HandleFunc("POST /api/v1/sshkeys", s.createSSHKey)
	api.HandleFunc("DELETE /api/v1/sshkeys/{id}", s.deleteSSHKey)

	root := http.NewServeMux()
	root.HandleFunc("GET /api/v1/info", s.info)
	root.HandleFunc("POST /api/v1/login", s.login)
	root.HandleFunc("POST /api/v1/logout", s.logout)
	root.Handle("/api/", auth.Middleware(s.Auth, s.Sessions, api))
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

func (s *Server) info(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, Info{AuthMode: s.Auth.Mode(), Namespace: s.Namespace, Version: s.Version, SSH: s.SSH})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, auth.UserFrom(r.Context()))
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	u, err := s.Auth.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if err := s.Sessions.SetCookie(w, u); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) logout(w http.ResponseWriter, _ *http.Request) {
	s.Sessions.ClearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ---

type httpError struct {
	code int
	msg  string
}

func (e *httpError) Error() string { return e.msg }

func badRequest(format string) error { return &httpError{http.StatusBadRequest, format} }

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
	case apierrors.IsNotFound(err):
		writeError(w, http.StatusNotFound, "not found")
	case apierrors.IsAlreadyExists(err):
		writeError(w, http.StatusConflict, "already exists")
	case apierrors.IsConflict(err):
		writeError(w, http.StatusConflict, "resource was modified, retry")
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case apierrors.IsForbidden(err):
		writeError(w, http.StatusForbidden, err.Error())
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

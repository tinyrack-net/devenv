package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
	"github.com/tinyrack-net/devenv/internal/access"
	"github.com/tinyrack-net/devenv/internal/agent"
	"github.com/tinyrack-net/devenv/internal/controller"
	"github.com/tinyrack-net/devenv/internal/helper"
	"github.com/tinyrack-net/devenv/internal/sshclient"
)

// Workspace APIs: terminal sessions, background processes, listening ports
// and usage of a running environment, all through devenv-agent.

func (s *Server) registerWorkspace(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/environments/{name}/sessions", s.listSessions)
	mux.HandleFunc("PATCH /api/v1/environments/{name}/sessions/{id}", s.renameSession)
	mux.HandleFunc("DELETE /api/v1/environments/{name}/sessions/{id}", s.killSession)
	mux.HandleFunc("GET /api/v1/environments/{name}/processes", s.listProcesses)
	mux.HandleFunc("GET /api/v1/environments/{name}/processes/{id}/output", s.processOutput)
	mux.HandleFunc("POST /api/v1/environments/{name}/processes", s.startProcess)
	mux.HandleFunc("POST /api/v1/environments/{name}/processes/{id}/stop", s.stopProcess)
	mux.HandleFunc("GET /api/v1/environments/{name}/ports", s.listPorts)
	mux.HandleFunc("GET /api/v1/environments/{name}/stats", s.envStats)
}

// errNotRunning is returned when the environment can not be reached.
var errNotRunning = &httpError{http.StatusConflict, "environment is not running"}

// withHelper leases a pooled connection to a running environment the caller
// can use and runs fn.
func (s *Server) withHelper(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, sc *ssh.Client) (any, error)) {
	name := r.PathValue("name")
	env, _, err := s.Authorize(r.Context(), principal(r), name, access.Use)
	if err != nil {
		fail(w, r, err)
		return
	}
	if env.Status.Phase != devenvv1.PhaseRunning {
		fail(w, r, errNotRunning)
		return
	}
	lease, err := s.Pool.Get(r.Context(), name)
	if err != nil {
		if errors.Is(err, sshclient.ErrNotRunning) {
			fail(w, r, errNotRunning)
			return
		}
		fail(w, r, &httpError{http.StatusBadGateway, dialError(err)})
		return
	}
	defer lease.Release()
	out, err := fn(r.Context(), lease.Client)
	if err != nil {
		var he *helper.Error
		switch {
		case errors.Is(err, helper.ErrUnavailable):
			fail(w, r, &httpError{http.StatusNotImplemented, "devenv-agent is not available in this environment (restart it after enabling the helper)"})
		case errors.As(err, &he):
			fail(w, r, &httpError{http.StatusBadRequest, he.Msg})
		default:
			fail(w, r, &httpError{http.StatusBadGateway, err.Error()})
		}
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	s.withHelper(w, r, func(ctx context.Context, sc *ssh.Client) (any, error) {
		l, err := helper.Call[agent.PtyList](ctx, sc, "pty-list", struct{}{}, 15*time.Second)
		return l.Sessions, err
	})
}

func (s *Server) renameSession(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	s.withHelper(w, r, func(ctx context.Context, sc *ssh.Client) (any, error) {
		return helper.Call[agent.PtySession](ctx, sc, "pty-rename", agent.PtyRenameRequest{ID: r.PathValue("id"), Name: in.Name}, 15*time.Second)
	})
}

func (s *Server) killSession(w http.ResponseWriter, r *http.Request) {
	s.withHelper(w, r, func(ctx context.Context, sc *ssh.Client) (any, error) {
		_, err := helper.Call[any](ctx, sc, "pty-kill", agent.PtyKillRequest{ID: r.PathValue("id")}, 15*time.Second)
		return map[string]bool{"ok": err == nil}, err
	})
}

func (s *Server) listProcesses(w http.ResponseWriter, r *http.Request) {
	s.withHelper(w, r, func(ctx context.Context, sc *ssh.Client) (any, error) {
		l, err := helper.Call[agent.ProcessList](ctx, sc, "list", struct{}{}, 15*time.Second)
		return l.Processes, err
	})
}

func (s *Server) processOutput(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset := int64(-1)
	if v := q.Get("offset"); v != "" {
		offset, _ = strconv.ParseInt(v, 10, 64)
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 1<<20 {
		limit = 256 << 10
	}
	wait, _ := strconv.Atoi(q.Get("wait"))
	if wait < 0 || wait > 25 {
		wait = 0
	}
	s.withHelper(w, r, func(ctx context.Context, sc *ssh.Client) (any, error) {
		return helper.Call[agent.Output](ctx, sc, "read", agent.ReadRequest{
			ID: r.PathValue("id"), Offset: offset, Limit: limit, WaitMs: int64(wait) * 1000,
		}, time.Duration(wait+15)*time.Second)
	})
}

func (s *Server) startProcess(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID      string `json:"id"`
		Command string `json:"command"`
		Cwd     string `json:"cwd,omitempty"`
	}
	if !decode(w, r, &in) {
		return
	}
	s.withHelper(w, r, func(ctx context.Context, sc *ssh.Client) (any, error) {
		return helper.Call[agent.Output](ctx, sc, "start", agent.StartRequest{ID: in.ID, Command: in.Command, Cwd: in.Cwd, Limit: 16 << 10}, 30*time.Second)
	})
}

func (s *Server) stopProcess(w http.ResponseWriter, r *http.Request) {
	remove := r.URL.Query().Get("remove") != ""
	s.withHelper(w, r, func(ctx context.Context, sc *ssh.Client) (any, error) {
		return helper.Call[agent.Output](ctx, sc, "stop", agent.StopRequest{ID: r.PathValue("id"), Remove: remove}, 30*time.Second)
	})
}

// Port is a listening port with ways to reach it.
type Port struct {
	agent.ListeningPort `tstype:",extends"`
	// URL on the environment's own address (loadBalancer, multus).
	URL string `json:"url,omitempty"`
	// PreviewURL through the devenv server (preview.domain).
	PreviewURL string `json:"previewURL,omitempty"`
	// Exposed reports whether the port is reachable on the environment IP.
	Exposed bool `json:"exposed"`
}

// internalPorts are served by the environment itself.
var internalPorts = map[int]bool{22: true, controller.VNCPort: true}

func (s *Server) listPorts(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.withHelper(w, r, func(ctx context.Context, sc *ssh.Client) (any, error) {
		l, err := helper.Call[agent.PortList](ctx, sc, "ports", struct{}{}, 15*time.Second)
		if err != nil {
			return nil, err
		}
		env, _, err := s.Authorize(ctx, principal(r), name, access.Use)
		if err != nil {
			return nil, err
		}
		exposed := map[int32]bool{}
		for _, p := range env.Spec.Ports {
			if p.Protocol == "TCP" {
				exposed[p.Port] = true
			}
		}
		seen := map[int]bool{}
		out := []Port{}
		for _, p := range l.Ports {
			if internalPorts[p.Port] || seen[p.Port] || (p.Port >= 6000 && p.Port < 6100) {
				continue
			}
			seen[p.Port] = true
			pp := Port{ListeningPort: p}
			if env.Status.IP != "" && !p.Local {
				switch env.Status.NetworkMode {
				case devenvv1.NetworkModeMultus:
					pp.Exposed = true
				case devenvv1.NetworkModeLoadBalancer:
					pp.Exposed = exposed[int32(p.Port)]
				}
				if pp.Exposed {
					pp.URL = "http://" + env.Status.IP + ":" + strconv.Itoa(p.Port)
				}
			}
			if d := s.Config.Preview.Domain; d != "" {
				pp.PreviewURL = "https://" + strconv.Itoa(p.Port) + "-" + name + "." + d + "/"
			}
			out = append(out, pp)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
		return out, nil
	})
}

func (s *Server) envStats(w http.ResponseWriter, r *http.Request) {
	s.withHelper(w, r, func(ctx context.Context, sc *ssh.Client) (any, error) {
		return helper.Call[agent.StatsResult](ctx, sc, "stats", agent.StatsRequest{Path: "/home/dev"}, 15*time.Second)
	})
}

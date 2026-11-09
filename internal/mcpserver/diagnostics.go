package mcpserver

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tinyrack-net/devenv/internal/model"
	"github.com/tinyrack-net/devenv/internal/agent"
)

// Diagnostics describe the health of a running environment.
type Diagnostics struct {
	Usage           *Usage       `json:"usage,omitempty" jsonschema:"current resource usage (running environments only)"`
	Restarts        int32        `json:"restarts" jsonschema:"times the environment container restarted (crash or out of memory)"`
	LastTermination *Termination `json:"last_termination,omitempty" jsonschema:"why the container last stopped unexpectedly"`
	Warnings        []string     `json:"warnings,omitempty" jsonschema:"recent warning events (scheduling, image pull, probes)"`
}

type Usage struct {
	MemoryUsed  string `json:"memory_used"`
	MemoryLimit string `json:"memory_limit,omitempty"`
	MemoryPct   int    `json:"memory_percent,omitempty"`
	DiskUsed    string `json:"disk_used" jsonschema:"/home/dev volume"`
	DiskTotal   string `json:"disk_total"`
	DiskPct     int    `json:"disk_percent"`
	Processes   int    `json:"processes"`
	OOMKills    uint64 `json:"oom_kills" jsonschema:"processes killed for exceeding memory since the container started"`
}

type Termination struct {
	Reason   string    `json:"reason" jsonschema:"e.g. OOMKilled or Error"`
	ExitCode int32     `json:"exit_code"`
	At       time.Time `json:"at"`
}

type envDetailOut struct {
	Environment Environment  `json:"environment"`
	Diagnostics *Diagnostics `json:"diagnostics,omitempty"`
}

func pct(used, total uint64) int {
	if total == 0 {
		return 0
	}
	return int(used * 100 / total)
}

// diagnostics gathers pod state, events and (when running) helper stats.
func (s *Server) diagnostics(ctx context.Context, env *model.Environment) *Diagnostics {
	d := &Diagnostics{}
	if st := s.containerStatus(ctx, env.Name); st != nil {
		d.Restarts = st.RestartCount
		if t := st.LastTerminationState.Terminated; t != nil {
			d.LastTermination = &Termination{Reason: t.Reason, ExitCode: t.ExitCode, At: t.FinishedAt.Time}
		}
	}
	d.Warnings = s.recentWarnings(ctx, env.Name)

	if env.Status.Phase == model.PhaseRunning {
		sc, release, err := s.connect(ctx, env.Name, false)
		if err == nil {
			st, err := callHelper[agent.StatsResult](ctx, sc, "stats", agent.StatsRequest{Path: homeDir}, 15*time.Second)
			release()
			if err == nil {
				u := &Usage{
					MemoryUsed: fmtBytes(st.MemoryUsed), DiskUsed: fmtBytes(st.DiskUsed), DiskTotal: fmtBytes(st.DiskTotal),
					DiskPct: pct(st.DiskUsed, st.DiskTotal), Processes: st.Processes, OOMKills: st.OOMKills,
				}
				if st.MemoryLimit > 0 {
					u.MemoryLimit, u.MemoryPct = fmtBytes(st.MemoryLimit), pct(st.MemoryUsed, st.MemoryLimit)
				}
				d.Usage = u
			}
		}
	}
	if d.Usage == nil && d.Restarts == 0 && d.LastTermination == nil && len(d.Warnings) == 0 {
		return nil
	}
	return d
}

// recentWarnings returns up to 5 warning events of the environment from the last hour.
func (s *Server) recentWarnings(ctx context.Context, name string) []string {
	list := &corev1.EventList{}
	if err := s.API.Reader.List(ctx, list, client.InNamespace(s.API.Namespace)); err != nil {
		return nil
	}
	related := map[string]bool{name: true, name + "-home": true}
	type ev struct {
		at  time.Time
		msg string
	}
	var evs []ev
	for _, e := range list.Items {
		if e.Type != corev1.EventTypeWarning || !related[e.InvolvedObject.Name] {
			continue
		}
		at := e.LastTimestamp.Time
		if at.IsZero() {
			at = e.EventTime.Time
		}
		if time.Since(at) > time.Hour {
			continue
		}
		evs = append(evs, ev{at, fmt.Sprintf("%s: %s", e.Reason, strings.TrimSpace(e.Message))})
	}
	sort.Slice(evs, func(i, j int) bool { return evs[i].at.After(evs[j].at) })
	var out []string
	for i := 0; i < len(evs) && i < 5; i++ {
		out = append(out, evs[i].msg)
	}
	return out
}

type restartIn struct {
	Name string `json:"name" jsonschema:"environment name"`
	Wait *bool  `json:"wait,omitempty" jsonschema:"wait until running again (default true)"`
}

func (s *Server) restartEnvironment(ctx context.Context, _ *mcp.CallToolRequest, in restartIn) (*mcp.CallToolResult, envOut, error) {
	pod := &corev1.Pod{}
	_ = s.API.Reader.Get(ctx, client.ObjectKey{Namespace: s.API.Namespace, Name: in.Name}, pod)
	s.pool().Forget(in.Name)
	// The controller recreates the pod; the home volume is kept.
	env, err := s.API.Restart(ctx, who(ctx), in.Name)
	if err != nil {
		return nil, envOut{}, userErr(err)
	}
	note := "restarted: processes were stopped; files in /home/dev are kept"
	if boolDefault(in.Wait, true) {
		// Wait for the new pod, then for Running.
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) && pod.UID != "" {
			np := &corev1.Pod{}
			if err := s.API.Reader.Get(ctx, client.ObjectKey{Namespace: s.API.Namespace, Name: in.Name}, np); err == nil && np.UID != pod.UID {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		var wnote string
		if env, wnote, err = s.waitSoft(ctx, in.Name, true); err != nil {
			return nil, envOut{}, err
		}
		if wnote != "" {
			note += "; " + wnote
		}
	}
	return nil, envOut{Environment: s.view(env), Note: note}, nil
}

func (s *Server) registerDiagnostics(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "restart_environment",
		Description: "Restart a running environment: all processes stop and the container starts fresh. Files in /home/dev are kept. Use it when the environment is stuck.",
	}, logged("restart_environment", s.restartEnvironment))
}

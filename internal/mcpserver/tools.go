package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/api/resource"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
	"github.com/tinyrack-net/devenv/internal/activity"
	"github.com/tinyrack-net/devenv/internal/api"
)

// toolError is returned to the agent as a tool error (IsError result).
type toolError struct{ msg string }

func (e *toolError) Error() string { return e.msg }

func errorf(format string, args ...any) error { return &toolError{fmt.Sprintf(format, args...)} }

// userErr converts service errors into readable tool errors.
func userErr(err error) error {
	if err == nil {
		return nil
	}
	var te *toolError
	if errors.As(err, &te) {
		return err
	}
	msg, _ := api.ErrorMessage(err)
	return &toolError{msg}
}

// Environment is the MCP view of an environment.
type Environment struct {
	Name         string             `json:"name"`
	Image        string             `json:"image"`
	Class        string             `json:"class"`
	Phase        string             `json:"phase" jsonschema:"Pending, Starting, Running, Stopping, Stopped or Failed"`
	Message      string             `json:"message,omitempty"`
	Running      bool               `json:"running" jsonschema:"desired state"`
	Resources    api.ResourcesInput `json:"resources"`
	Desktop      bool               `json:"desktop"`
	NetworkMode  string             `json:"network_mode,omitempty"`
	IP           string             `json:"ip,omitempty" jsonschema:"dedicated address (loadBalancer or multus classes)"`
	Ports        []api.PortInput    `json:"ports,omitempty"`
	Owner        string             `json:"owner"`
	CreatedByMCP bool               `json:"created_by_mcp"`
	CreatedAt    time.Time          `json:"created_at"`
	SSH          string             `json:"ssh,omitempty" jsonschema:"command to connect over SSH"`
}

func (s *Server) view(e *devenvv1.DevEnvironment) Environment {
	a := api.ToEnvironment(e)
	out := Environment{
		Name: a.Name, Image: a.Image, Class: a.Class, Phase: a.Phase, Message: a.Message,
		Running: a.Running, Resources: a.Resources, Desktop: a.Desktop, NetworkMode: a.NetworkMode,
		IP: a.IP, Ports: a.Ports, CreatedAt: a.CreatedAt,
		Owner:        e.Spec.Owner,
		CreatedByMCP: e.Labels[LabelCreatedBy] == "mcp",
	}
	if a.IP != "" {
		out.SSH = "ssh dev@" + a.IP
	} else if ssh := s.API.SSH; ssh.Enabled && ssh.Host != "" {
		out.SSH = fmt.Sprintf("ssh -p %d %s@%s", ssh.Port, a.Name, ssh.Host)
	}
	return out
}

type empty struct{}

type listImagesOut struct {
	Images []api.Image `json:"images"`
}

type listClassesOut struct {
	Classes []api.Class `json:"classes"`
}

// capacityOut is the MCP view of class capacity. Quantities are strings:
// resource.Quantity marshals to a string but its generated schema is an
// object, which fails output validation in clients.
type capacityOut struct {
	Class     string         `json:"class"`
	MaxCPU    string         `json:"max_cpu" jsonschema:"largest CPU a new environment can request"`
	MaxMemory string         `json:"max_memory"`
	MaxDisk   string         `json:"max_disk,omitempty"`
	Nodes     []nodeCapacity `json:"nodes"`
}

type nodeCapacity struct {
	Name       string `json:"name"`
	FreeCPU    string `json:"free_cpu"`
	FreeMemory string `json:"free_memory"`
	CPU        string `json:"allocatable_cpu"`
	Memory     string `json:"allocatable_memory"`
}

type capacityIn struct {
	Class string `json:"class,omitempty" jsonschema:"class name; defaults to the default class"`
}

type listEnvsIn struct {
	CreatedByMCPOnly bool `json:"created_by_mcp_only,omitempty" jsonschema:"only environments created through MCP"`
	All              bool `json:"all,omitempty" jsonschema:"administrators: include every user's environments"`
}

type listEnvsOut struct {
	Environments []Environment `json:"environments"`
}

type nameIn struct {
	Name string `json:"name" jsonschema:"environment name"`
}

type waitIn struct {
	Name string `json:"name" jsonschema:"environment name"`
	Wait *bool  `json:"wait,omitempty" jsonschema:"wait until the change is complete (default true, up to 3 minutes)"`
}

type createIn struct {
	Name   string          `json:"name" jsonschema:"unique name: lowercase letters, digits and '-', max 40 characters"`
	Image  string          `json:"image" jsonschema:"image name from list_images"`
	Class  string          `json:"class,omitempty" jsonschema:"class name from list_classes; default class when empty"`
	CPU    string          `json:"cpu,omitempty" jsonschema:"CPU cores, e.g. 2 or 500m; image default when empty"`
	Memory string          `json:"memory,omitempty" jsonschema:"memory, e.g. 4Gi; image default when empty"`
	Disk   string          `json:"disk,omitempty" jsonschema:"home volume size, e.g. 20Gi; can only grow later"`
	Ports  []api.PortInput `json:"ports,omitempty" jsonschema:"ports to expose (loadBalancer classes only)"`
	Wait   *bool           `json:"wait,omitempty" jsonschema:"wait until running (default true, up to 3 minutes)"`
}

type updateIn struct {
	Name   string           `json:"name" jsonschema:"environment name"`
	Image  string           `json:"image,omitempty" jsonschema:"new image (restarts the environment)"`
	Class  string           `json:"class,omitempty" jsonschema:"move to another class from list_classes, e.g. one with a dedicated network address (restarts the environment; only between classes with the same storage)"`
	Wait   *bool            `json:"wait,omitempty" jsonschema:"wait until a restart caused by the change is done (default true, up to 3 minutes)"`
	CPU    string           `json:"cpu,omitempty" jsonschema:"new CPU (restarts the environment)"`
	Memory string           `json:"memory,omitempty" jsonschema:"new memory (restarts the environment)"`
	Disk   string           `json:"disk,omitempty" jsonschema:"new home volume size; can only grow"`
	Ports  *[]api.PortInput `json:"ports,omitempty" jsonschema:"replace exposed ports (loadBalancer classes only)"`
}

type envOut struct {
	Environment Environment `json:"environment"`
	Note        string      `json:"note,omitempty"`
}

type deleteOut struct {
	Deleted string `json:"deleted"`
}

func boolDefault(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

func (s *Server) register(srv *mcp.Server) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcp.AddTool(srv, &mcp.Tool{Name: "list_images", Description: "List the environment images that can be used to create environments.", Annotations: readOnly},
		logged("list_images", func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, listImagesOut, error) {
			imgs, err := s.API.ListImages(ctx, who(ctx), false)
			if err != nil {
				return nil, listImagesOut{}, userErr(err)
			}
			out := []api.Image{}
			for _, i := range imgs {
				if !i.Disabled {
					out = append(out, i)
				}
			}
			return nil, listImagesOut{Images: out}, nil
		}))

	mcp.AddTool(srv, &mcp.Tool{Name: "list_classes", Description: "List environment classes (storage, network mode and resource limits).", Annotations: readOnly},
		logged("list_classes", func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, listClassesOut, error) {
			cls, err := s.API.ListClasses(ctx, who(ctx))
			return nil, listClassesOut{Classes: cls}, userErr(err)
		}))

	mcp.AddTool(srv, &mcp.Tool{Name: "get_capacity", Description: "Show the largest CPU, memory and disk that can currently be requested in a class.", Annotations: readOnly},
		logged("get_capacity", func(ctx context.Context, _ *mcp.CallToolRequest, in capacityIn) (*mcp.CallToolResult, capacityOut, error) {
			class := in.Class
			if class == "" {
				class = devenvv1.DefaultClassName
			}
			c, err := s.API.ClassCapacity(ctx, who(ctx), class, "")
			if err != nil {
				return nil, capacityOut{}, userErr(err)
			}
			out := capacityOut{Class: c.Class, MaxCPU: c.MaxCPU, MaxMemory: c.MaxMem, MaxDisk: c.MaxDisk, Nodes: []nodeCapacity{}}
			for _, n := range c.Nodes {
				out.Nodes = append(out.Nodes, nodeCapacity{
					Name: n.Name, FreeCPU: n.Free.CPU.String(), FreeMemory: n.Free.Memory.String(),
					CPU: n.Allocable.CPU.String(), Memory: n.Allocable.Memory.String(),
				})
			}
			return nil, out, nil
		}))

	mcp.AddTool(srv, &mcp.Tool{Name: "list_environments", Description: "List development environments.", Annotations: readOnly},
		logged("list_environments", func(ctx context.Context, _ *mcp.CallToolRequest, in listEnvsIn) (*mcp.CallToolResult, listEnvsOut, error) {
			p := who(ctx)
			envs, err := s.API.ListEnvironments(ctx, p)
			if err != nil {
				return nil, listEnvsOut{}, userErr(err)
			}
			out := []Environment{}
			for i := range envs {
				if in.CreatedByMCPOnly && envs[i].Labels[LabelCreatedBy] != "mcp" {
					continue
				}
				if !in.All && envs[i].Spec.Owner != p.Name && !sharedWith(p, &envs[i]) {
					continue
				}
				out = append(out, s.view(&envs[i]))
			}
			return nil, listEnvsOut{Environments: out}, nil
		}))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_environment",
		Description: "Get the state of an environment, with resource usage (memory, /home/dev disk), restarts, the last crash or out-of-memory kill and recent warnings.",
		Annotations: readOnly,
	}, logged("get_environment", func(ctx context.Context, _ *mcp.CallToolRequest, in nameIn) (*mcp.CallToolResult, envDetailOut, error) {
		e, err := s.getEnv(ctx, in.Name)
		if err != nil {
			return nil, envDetailOut{}, userErr(err)
		}
		return nil, envDetailOut{Environment: s.view(e), Diagnostics: s.diagnostics(ctx, e)}, nil
	}))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "create_environment",
		Description: "Create a new development environment. By default waits until it is running (up to 3 minutes).",
	}, logged("create_environment", func(ctx context.Context, _ *mcp.CallToolRequest, in createIn) (*mcp.CallToolResult, envOut, error) {
		env, err := s.API.CreateEnvironment(ctx, who(ctx), api.CreateEnvironmentRequest{
			Name:      in.Name,
			Image:     in.Image,
			Class:     in.Class,
			Resources: api.ResourcesInput{CPU: in.CPU, Memory: in.Memory, Disk: in.Disk},
			Ports:     in.Ports,
		}, api.CreateOptions{
			Labels: map[string]string{LabelCreatedBy: "mcp"},
		})
		if err != nil {
			return nil, envOut{}, userErr(err)
		}
		note := ""
		if boolDefault(in.Wait, true) {
			if env, note, err = s.waitSoft(ctx, env.Name, true); err != nil {
				return nil, envOut{}, err
			}
		}
		return nil, envOut{Environment: s.view(env), Note: note}, nil
	}))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "update_environment",
		Description: "Change resources, image, class (network) or ports of an environment. Changing image, class or CPU/memory restarts it; files in /home/dev are kept.",
	}, logged("update_environment", func(ctx context.Context, _ *mcp.CallToolRequest, in updateIn) (*mcp.CallToolResult, envOut, error) {
		req := api.UpdateEnvironmentRequest{Ports: in.Ports}
		if in.Image != "" {
			req.Image = &in.Image
		}
		if in.Class != "" {
			req.Class = &in.Class
		}
		if in.CPU != "" || in.Memory != "" || in.Disk != "" {
			cur, err := s.getEnv(ctx, in.Name)
			if err != nil {
				return nil, envOut{}, userErr(err)
			}
			r := api.ToEnvironment(cur).Resources
			if in.CPU != "" {
				r.CPU = in.CPU
			}
			if in.Memory != "" {
				r.Memory = in.Memory
			}
			if in.Disk != "" {
				r.Disk = in.Disk
			}
			req.Resources = &r
		}
		note := ""
		before, err := s.getEnv(ctx, in.Name)
		if err != nil {
			return nil, envOut{}, userErr(err)
		}
		env, err := s.API.UpdateEnvironment(ctx, who(ctx), in.Name, req)
		if err != nil {
			return nil, envOut{}, userErr(err)
		}
		restarts := env.Spec.ImageRef != before.Spec.ImageRef || env.ClassName() != before.ClassName() ||
			!quantityEqual(env.Spec.Resources.CPU, before.Spec.Resources.CPU) || !quantityEqual(env.Spec.Resources.Memory, before.Spec.Resources.Memory)
		if restarts && env.IsRunning() {
			note = "the environment restarted to apply the change; files in /home/dev are kept"
			s.pool().Forget(in.Name)
			if boolDefault(in.Wait, true) {
				s.waitCacheGeneration(ctx, in.Name, env.Generation)
				var wnote string
				if env, wnote, err = s.waitSoft(ctx, in.Name, true); err != nil {
					return nil, envOut{}, err
				}
				if wnote != "" {
					note = wnote
				}
			}
		}
		return nil, envOut{Environment: s.view(env), Note: note}, nil
	}))

	mcp.AddTool(srv, &mcp.Tool{Name: "start_environment", Description: "Start a stopped environment. Waits until running by default."},
		logged("start_environment", func(ctx context.Context, _ *mcp.CallToolRequest, in waitIn) (*mcp.CallToolResult, envOut, error) {
			env, err := s.API.SetRunning(ctx, who(ctx), in.Name, true)
			if err != nil {
				return nil, envOut{}, userErr(err)
			}
			note := ""
			if boolDefault(in.Wait, true) {
				if env, note, err = s.waitSoft(ctx, in.Name, true); err != nil {
					return nil, envOut{}, err
				}
			}
			return nil, envOut{Environment: s.view(env), Note: note}, nil
		}))

	mcp.AddTool(srv, &mcp.Tool{Name: "stop_environment", Description: "Stop an environment. Files in /home/dev are kept; running processes end.", Annotations: &mcp.ToolAnnotations{IdempotentHint: true}},
		logged("stop_environment", func(ctx context.Context, _ *mcp.CallToolRequest, in waitIn) (*mcp.CallToolResult, envOut, error) {
			env, err := s.API.SetRunning(ctx, who(ctx), in.Name, false)
			if err != nil {
				return nil, envOut{}, userErr(err)
			}
			note := ""
			if boolDefault(in.Wait, true) {
				if env, note, err = s.waitSoft(ctx, in.Name, false); err != nil {
					return nil, envOut{}, err
				}
			}
			return nil, envOut{Environment: s.view(env), Note: note}, nil
		}))

	mcp.AddTool(srv, &mcp.Tool{Name: "delete_environment", Description: "Delete an environment permanently, including its home volume.", Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr.To(true)}},
		logged("delete_environment", func(ctx context.Context, _ *mcp.CallToolRequest, in nameIn) (*mcp.CallToolResult, deleteOut, error) {
			if err := s.API.DeleteEnvironment(ctx, who(ctx), in.Name); err != nil {
				return nil, deleteOut{}, userErr(err)
			}
			return nil, deleteOut{Deleted: in.Name}, nil
		}))

	s.registerExec(srv)
	s.registerFiles(srv)
	s.registerSearch(srv)
	s.registerProcesses(srv)
	s.registerDiagnostics(srv)
	s.registerDesktop(srv)
}

// getEnv reads an environment, tolerating informer cache lag right after
// creation (agents often act on an environment immediately).
func (s *Server) getEnv(ctx context.Context, name string) (*devenvv1.DevEnvironment, error) {
	start := time.Now()
	for {
		env, err := s.API.GetEnvironment(ctx, who(ctx), name)
		if err == nil || !apierrors.IsNotFound(err) || time.Since(start) > 3*time.Second || ctx.Err() != nil {
			return env, err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func quantityEqual(a, b *resource.Quantity) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Cmp(*b) == 0
}

// waitCacheGeneration waits (briefly) until reads see at least generation,
// so a following wait does not act on the state from before an update.
func (s *Server) waitCacheGeneration(ctx context.Context, name string, gen int64) {
	if gen == 0 {
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if e, err := s.API.GetEnvironment(ctx, who(ctx), name); err == nil && e.Generation >= gen && e.Status.ObservedGeneration >= gen {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// WaitLimit bounds how long tools wait for an environment. After it the
// tool returns the current state with a note instead of failing.
var WaitLimit = 3 * time.Minute

// waitSoft waits up to WaitLimit. When the environment is not ready yet it
// returns its current state with a note instead of an error.
func (s *Server) waitSoft(ctx context.Context, name string, running bool) (*devenvv1.DevEnvironment, string, error) {
	env, err := s.waitFor(ctx, name, running, WaitLimit)
	var te *timeoutError
	if errors.As(err, &te) {
		verb := "starting"
		if !running {
			verb = "stopping"
		}
		return te.env, fmt.Sprintf("still %s after %s (phase %s%s); call get_environment again in a little while", verb, WaitLimit.Round(time.Second), te.env.Status.Phase, msgSuffix(te.env.Status.Message)), nil
	}
	return env, "", err
}

func msgSuffix(m string) string {
	if m == "" {
		return ""
	}
	return ": " + m
}

type timeoutError struct{ env *devenvv1.DevEnvironment }

func (e *timeoutError) Error() string {
	return fmt.Sprintf("timed out waiting for environment %s (phase %s%s); check again with get_environment", e.env.Name, e.env.Status.Phase, msgSuffix(e.env.Status.Message))
}

// waitFor polls until the environment is running (or stopped).
func (s *Server) waitFor(ctx context.Context, name string, running bool, limit time.Duration) (*devenvv1.DevEnvironment, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	start := time.Now()
	for {
		env, err := s.API.GetEnvironment(ctx, who(ctx), name)
		if err != nil {
			if ctx.Err() != nil {
				return nil, errorf("cancelled")
			}
			// Reads come from the informer cache, which may not have seen a
			// just created environment yet.
			if apierrors.IsNotFound(err) && time.Since(start) < 10*time.Second {
				time.Sleep(200 * time.Millisecond)
				continue
			}
			return nil, userErr(err)
		}
		switch {
		case running && env.Status.Phase == devenvv1.PhaseRunning && env.Status.ObservedGeneration == env.Generation &&
			(env.Status.NetworkMode == "" || env.Status.NetworkMode == devenvv1.NetworkModeGateway || env.Status.IP != ""):
			return env, nil
		case !running && env.Status.Phase == devenvv1.PhaseStopped:
			return env, nil
		case running && env.Status.Phase == devenvv1.PhaseFailed:
			return nil, errorf("environment %s failed to start: %s", name, env.Status.Message)
		}
		select {
		case <-ctx.Done():
			return nil, &timeoutError{env}
		case <-time.After(2 * time.Second):
		}
	}
}

// logged wraps a handler with an audit log line and the activity log.
func logged[In, Out any](name string, h mcp.ToolHandlerFor[In, Out]) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		start := time.Now()
		p := who(ctx)
		env, summary := describe(in)
		done := activityLog(ctx).Start(activity.Entry{
			Environment: env, User: p.Name, Token: p.TokenName, Tool: name, Summary: summary,
		})
		res, out, err := h(ctx, req, in)
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		done("", detail(out), errMsg)
		log := logf.FromContext(ctx).WithName("mcp")
		kv := []any{"tool", name, "user", p.Name, "durationMs", time.Since(start).Milliseconds()}
		if env != "" {
			kv = append(kv, "environment", env)
		}
		if err != nil {
			kv = append(kv, "error", errMsg)
		}
		log.Info("tool call", kv...)
		return res, out, err
	}
}

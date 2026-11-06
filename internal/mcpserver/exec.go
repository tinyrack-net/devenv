package mcpserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tinyrack-net/devenv/internal/agent"
	"golang.org/x/crypto/ssh"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
	"github.com/tinyrack-net/devenv/internal/sshclient"
)

const (
	defaultExecTimeout = 120 * time.Second
	maxExecTimeout     = 30 * time.Minute
	// maxStreamBytes is kept per stream (stdout, stderr).
	maxStreamBytes = 256 << 10
	maxStdinBytes  = 1 << 20
)

type execIn struct {
	Name           string            `json:"name" jsonschema:"environment name"`
	Command        string            `json:"command" jsonschema:"shell command, run with bash -lc as user dev"`
	Cwd            string            `json:"cwd,omitempty" jsonschema:"working directory (default /home/dev)"`
	Env            map[string]string `json:"env,omitempty" jsonschema:"extra environment variables"`
	Stdin          string            `json:"stdin,omitempty" jsonschema:"data written to the command's standard input"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty" jsonschema:"kill the command after this many seconds (default 120, max 1800)"`
	AutoStart      *bool             `json:"auto_start,omitempty" jsonschema:"start the environment first if it is stopped (default true)"`
}

type execOut struct {
	ExitCode   int    `json:"exit_code" jsonschema:"process exit code; -1 when killed by timeout or signal"`
	Signal     string `json:"signal,omitempty" jsonschema:"signal that ended the process, e.g. KILL or SEGV"`
	OOMKilled  bool   `json:"oom_killed,omitempty" jsonschema:"killed for exceeding the environment's memory limit"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	TimedOut   bool   `json:"timed_out"`
	Truncated  bool   `json:"truncated" jsonschema:"true when output exceeded 256KB per stream and the middle was omitted"`
	DurationMs int64  `json:"duration_ms"`
}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// shellQuote quotes s for POSIX shells.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// buildCommand builds the fallback shell command (no helper). It must not
// use setsid: sshd already starts commands as session leaders, so setsid
// would fork and the exit code would be lost.
func buildCommand(in execIn) (string, error) {
	var b strings.Builder
	keys := make([]string, 0, len(in.Env))
	for k := range in.Env {
		if !envNameRe.MatchString(k) {
			return "", errorf("invalid environment variable name %q", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "export %s=%s; ", k, shellQuote(in.Env[k]))
	}
	cwd := in.Cwd
	if cwd == "" {
		cwd = homeDir
	}
	fmt.Fprintf(&b, "cd %s || exit 126; ", shellQuote(cwd))
	b.WriteString(in.Command)
	// bash -lc: login shell so PATH from profile files applies.
	return "exec bash -lc " + shellQuote(b.String()), nil
}

// capped keeps the head and tail of a stream when it exceeds max bytes.
type capped struct {
	max   int
	head  bytes.Buffer
	tail  []byte
	total int
}

func (c *capped) Write(p []byte) (int, error) {
	n := len(p)
	c.total += n
	half := c.max / 2
	if room := half - c.head.Len(); room > 0 {
		k := min(room, len(p))
		c.head.Write(p[:k])
		p = p[k:]
	}
	if len(p) > 0 {
		c.tail = append(c.tail, p...)
		if len(c.tail) > half {
			c.tail = c.tail[len(c.tail)-half:]
		}
	}
	return n, nil
}

func (c *capped) String() (string, bool) {
	if c.total <= c.max {
		return c.head.String() + string(c.tail), false
	}
	omitted := c.total - c.head.Len() - len(c.tail)
	return fmt.Sprintf("%s\n... [%d bytes omitted] ...\n%s", c.head.String(), omitted, c.tail), true
}

// limiter bounds concurrent exec calls per environment.
type limiter struct {
	mu   sync.Mutex
	max  int
	used map[string]int
}

func newLimiter(max int) *limiter { return &limiter{max: max, used: map[string]int{}} }

func (l *limiter) acquire(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.used[name] >= l.max {
		return false
	}
	l.used[name]++
	return true
}

func (l *limiter) release(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.used[name]--; l.used[name] <= 0 {
		delete(l.used, name)
	}
}

func (s *Server) registerExec(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "exec",
		Description: "Run a shell command in an environment and return its exit code, stdout and stderr. " +
			"Runs `bash -lc` as user dev (passwordless sudo) in /home/dev unless cwd is set. " +
			"Each call is a fresh shell: use cwd/env or chain commands with && instead of relying on cd from earlier calls. " +
			"Not interactive: pass input via stdin and use non-interactive flags (e.g. apt-get -y). " +
			"Output is limited to 256KB per stream. Prefer read_file/edit_file/search for files, and start_process for anything that runs longer than about a minute " +
			"(some MCP gateways cut tool calls after 60 seconds).",
	}, logged("exec", s.exec))
}

func (s *Server) exec(ctx context.Context, _ *mcp.CallToolRequest, in execIn) (*mcp.CallToolResult, execOut, error) {
	if strings.TrimSpace(in.Command) == "" {
		return nil, execOut{}, errorf("command is required")
	}
	if len(in.Stdin) > maxStdinBytes {
		return nil, execOut{}, errorf("stdin is larger than %d bytes", maxStdinBytes)
	}
	for k := range in.Env {
		if !envNameRe.MatchString(k) {
			return nil, execOut{}, errorf("invalid environment variable name %q", k)
		}
	}
	timeout := defaultExecTimeout
	if in.TimeoutSeconds > 0 {
		timeout = min(time.Duration(in.TimeoutSeconds)*time.Second, maxExecTimeout)
	}
	restartsBefore := s.podRestarts(ctx, in.Name)
	sc, release, err := s.connect(ctx, in.Name, boolDefault(in.AutoStart, true))
	if err != nil {
		return nil, execOut{}, err
	}
	defer release()

	cwd := in.Cwd
	if cwd == "" {
		cwd = homeDir
	}
	// The helper runs the command in its own process group: exact exit
	// codes, timeouts that kill the whole tree, and OOM detection.
	res, err := callHelper[agent.ExecResult](ctx, sc, "exec", agent.ExecRequest{
		Command: in.Command, Cwd: cwd, Env: in.Env, Stdin: in.Stdin,
		TimeoutMs: timeout.Milliseconds(), MaxOutput: maxStreamBytes,
	}, timeout+30*time.Second)
	if err == nil {
		return nil, execOut{
			ExitCode: res.ExitCode, Signal: res.Signal, Stdout: res.Stdout, Stderr: res.Stderr,
			TimedOut: res.TimedOut, Truncated: res.Truncated, OOMKilled: res.OOMKilled, DurationMs: res.DurationMs,
		}, nil
	}
	if !errors.Is(err, errNoHelper) {
		// The session can die because the whole container was killed (memory
		// limit of the environment reached). Report that precisely.
		if reason := s.containerRestarted(ctx, in.Name, restartsBefore); reason != "" {
			s.pool().Forget(in.Name)
			return nil, execOut{ExitCode: -1, Signal: "KILL", OOMKilled: reason == "OOMKilled",
				Stderr: fmt.Sprintf("[devenv] the environment was restarted while the command ran (%s); "+
					"processes were stopped, /home/dev is kept. %s", reason, restartHint(reason))}, nil
		}
		return nil, execOut{}, err
	}
	// Fallback without the helper: plain shell. Exit codes are exact, but a
	// timeout only ends the session, not processes the command detached.
	cmd, err := buildCommand(in)
	if err != nil {
		return nil, execOut{}, err
	}
	out, err := runCommand(ctx, sc, cmd, in.Stdin, timeout)
	return nil, out, err
}

// connect makes sure the environment is running (starting it when allowed),
// takes a concurrency slot and opens an SSH connection. release must be
// called when done.
func (s *Server) connect(ctx context.Context, name string, autoStart bool) (*ssh.Client, func(), error) {
	env, err := s.getEnv(ctx, name)
	if err != nil {
		return nil, nil, userErr(err)
	}
	if env.Status.Phase != devenvv1.PhaseRunning || !env.IsRunning() {
		if !autoStart {
			return nil, nil, errorf("environment %s is %s; start it with start_environment or pass auto_start", name, env.Status.Phase)
		}
		if !env.IsRunning() {
			if _, err := s.API.SetRunning(ctx, name, true); err != nil {
				return nil, nil, userErr(err)
			}
		}
		if _, err := s.waitFor(ctx, name, true); err != nil {
			return nil, nil, err
		}
	}
	if !s.execLimiter.acquire(name) {
		return nil, nil, errorf("too many concurrent calls for %s (max %d); wait for one to finish", name, s.execLimiter.max)
	}
	lease, err := s.leaseWithRetry(ctx, name)
	if err != nil {
		s.execLimiter.release(name)
		return nil, nil, errorf("could not connect to environment %s: %v", name, err)
	}
	return lease.Client, func() { lease.Release(); s.execLimiter.release(name) }, nil
}

// runCommand runs cmd in a new session with a timeout that kills the whole
// process group.
func runCommand(ctx context.Context, sc *ssh.Client, cmd, stdin string, timeout time.Duration) (execOut, error) {
	start := time.Now()
	sess, err := sc.NewSession()
	if err != nil {
		return execOut{}, errorf("open session: %v", err)
	}
	defer sess.Close()

	stdout := &capped{max: maxStreamBytes}
	stderr := &capped{max: maxStreamBytes}
	sess.Stdout = stdout
	sess.Stderr = stderr
	sess.Stdin = strings.NewReader(stdin)
	if err := sess.Start(cmd); err != nil {
		return execOut{}, errorf("start command: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	out := execOut{}
	var waitErr error
	select {
	case waitErr = <-done:
	case <-timer.C:
		out.TimedOut = true
	case <-ctx.Done():
		out.TimedOut = true
	}
	if out.TimedOut {
		// Kill the process group: signal, then closing the session hangs
		// up the setsid'd group (SIGHUP).
		_ = sess.Signal(ssh.SIGKILL)
		_ = sess.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		out.ExitCode = -1
	} else {
		var ee *ssh.ExitError
		var missing *ssh.ExitMissingError
		switch {
		case waitErr == nil:
		case errors.As(waitErr, &ee):
			out.ExitCode = ee.ExitStatus()
			if ee.Signal() != "" {
				out.ExitCode = -1
			}
		case errors.As(waitErr, &missing):
			out.ExitCode = -1
		default:
			return execOut{}, errorf("command failed: %v", waitErr)
		}
	}

	var t1, t2 bool
	out.Stdout, t1 = stdout.String()
	out.Stderr, t2 = stderr.String()
	out.Truncated = t1 || t2
	out.DurationMs = time.Since(start).Milliseconds()
	if out.TimedOut {
		out.Stderr += fmt.Sprintf("\n[devenv] command timed out after %s and was killed", timeout)
	}
	return out, nil
}

// leaseWithRetry tolerates sshd still starting right after the pod is ready.
func (s *Server) leaseWithRetry(ctx context.Context, name string) (*sshclient.Lease, error) {
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		c, err := s.pool().Get(ctx, name)
		if err == nil {
			return c, nil
		}
		lastErr = err
		if errors.Is(err, sshclient.ErrNotRunning) || ctx.Err() != nil {
			break
		}
		time.Sleep(time.Second)
	}
	return nil, lastErr
}

// podRestarts returns the environment container's restart count (-1 unknown).
func (s *Server) podRestarts(ctx context.Context, name string) int32 {
	st := s.containerStatus(ctx, name)
	if st == nil {
		return -1
	}
	return st.RestartCount
}

// containerRestarted returns the last termination reason when the container
// restarted since before (e.g. "OOMKilled"), or "".
func (s *Server) containerRestarted(ctx context.Context, name string, before int32) string {
	if before < 0 {
		return ""
	}
	// The kubelet reports the restart shortly after the kill.
	for i := 0; i < 10; i++ {
		if st := s.containerStatus(ctx, name); st != nil && st.RestartCount > before {
			if t := st.LastTerminationState.Terminated; t != nil && t.Reason != "" {
				return t.Reason
			}
			return "restarted"
		}
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(500 * time.Millisecond):
		}
	}
	return ""
}

func restartHint(reason string) string {
	if reason == "OOMKilled" {
		return "It exceeded the environment's memory limit: raise memory with update_environment or use less memory."
	}
	return ""
}

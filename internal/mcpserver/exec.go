package mcpserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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

// buildCommand wraps the user command so it runs in its own process group
// (setsid), letting the timeout kill everything it started.
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
		cwd = "/home/dev"
	}
	fmt.Fprintf(&b, "cd %s || exit 126; ", shellQuote(cwd))
	b.WriteString(in.Command)
	// bash -lc: login shell so PATH from profile files applies.
	return "exec setsid bash -lc " + shellQuote(b.String()), nil
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
			"Output is limited to 256KB per stream. For long running servers, start them in the background with nohup and redirect output to a file.",
	}, logged("exec", s.exec))
}

func (s *Server) exec(ctx context.Context, _ *mcp.CallToolRequest, in execIn) (*mcp.CallToolResult, execOut, error) {
	if strings.TrimSpace(in.Command) == "" {
		return nil, execOut{}, errorf("command is required")
	}
	if len(in.Stdin) > maxStdinBytes {
		return nil, execOut{}, errorf("stdin is larger than %d bytes", maxStdinBytes)
	}
	timeout := defaultExecTimeout
	if in.TimeoutSeconds > 0 {
		timeout = min(time.Duration(in.TimeoutSeconds)*time.Second, maxExecTimeout)
	}
	cmd, err := buildCommand(in)
	if err != nil {
		return nil, execOut{}, err
	}

	env, err := s.getEnv(ctx, in.Name)
	if err != nil {
		return nil, execOut{}, userErr(err)
	}
	if env.Status.Phase != devenvv1.PhaseRunning || !env.IsRunning() {
		if !boolDefault(in.AutoStart, true) {
			return nil, execOut{}, errorf("environment %s is %s; start it with start_environment or pass auto_start", in.Name, env.Status.Phase)
		}
		if !env.IsRunning() {
			if _, err := s.API.SetRunning(ctx, in.Name, true); err != nil {
				return nil, execOut{}, userErr(err)
			}
		}
		if _, err := s.waitFor(ctx, in.Name, true); err != nil {
			return nil, execOut{}, err
		}
	}

	if !s.execLimiter.acquire(in.Name) {
		return nil, execOut{}, errorf("too many concurrent exec calls for %s (max %d); wait for one to finish", in.Name, s.execLimiter.max)
	}
	defer s.execLimiter.release(in.Name)

	start := time.Now()
	sc, err := s.dialWithRetry(ctx, in.Name)
	if err != nil {
		return nil, execOut{}, errorf("could not connect to environment %s: %v", in.Name, err)
	}
	defer sc.Close()
	sess, err := sc.NewSession()
	if err != nil {
		return nil, execOut{}, errorf("open session: %v", err)
	}
	defer sess.Close()

	stdout := &capped{max: maxStreamBytes}
	stderr := &capped{max: maxStreamBytes}
	sess.Stdout = stdout
	sess.Stderr = stderr
	if in.Stdin != "" {
		sess.Stdin = strings.NewReader(in.Stdin)
	} else {
		sess.Stdin = io.LimitReader(strings.NewReader(""), 0)
	}
	if err := sess.Start(cmd); err != nil {
		return nil, execOut{}, errorf("start command: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	out := execOut{}
	var waitErr error
loop:
	for {
		select {
		case waitErr = <-done:
			break loop
		case <-timer.C:
			out.TimedOut = true
			break loop
		case <-ctx.Done():
			out.TimedOut = true
			break loop
		}
	}
	if out.TimedOut {
		// Kill the process group: signal, then closing the session hangs
		// up the setsid'd group (SIGHUP).
		_ = sess.Signal(ssh.SIGKILL)
		_ = sess.Close()
		_ = sc.Close()
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
			return nil, execOut{}, errorf("command failed: %v", waitErr)
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
	return nil, out, nil
}

// dialWithRetry tolerates sshd still starting right after the pod is ready.
func (s *Server) dialWithRetry(ctx context.Context, name string) (*ssh.Client, error) {
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		c, err := s.Dialer.Dial(ctx, name)
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

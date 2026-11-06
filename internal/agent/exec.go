package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Capped keeps the head and tail of a stream beyond Max bytes.
type Capped struct {
	Max   int
	head  bytes.Buffer
	tail  []byte
	total int
}

func (c *Capped) Write(p []byte) (int, error) {
	n := len(p)
	c.total += n
	half := c.Max / 2
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

// String returns the kept output and whether the middle was omitted.
func (c *Capped) String() (string, bool) {
	if c.total <= c.Max {
		return c.head.String() + string(c.tail), false
	}
	omitted := c.total - c.head.Len() - len(c.tail)
	return fmt.Sprintf("%s\n... [%d bytes omitted] ...\n%s", c.head.String(), omitted, c.tail), true
}

// shellCommand builds a login bash running command in cwd with env.
func shellCommand(command, cwd string, env map[string]string) (*exec.Cmd, error) {
	if cwd == "" {
		cwd = homeDir()
	}
	st, err := os.Stat(cwd)
	if err != nil {
		return nil, fmt.Errorf("cwd %s: %w", cwd, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("cwd %s is not a directory", cwd)
	}
	cmd := exec.Command("bash", "-lc", command)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	keys := make([]string, 0, len(env))
	for k := range env {
		if !validEnvName(k) {
			return nil, fmt.Errorf("invalid environment variable name %q", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	// Own process group so the whole tree can be signalled.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd, nil
}

func validEnvName(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		ok := r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return true
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return "/"
}

// killGroup sends sig to the process group of pid.
func killGroup(pid int, sig syscall.Signal) {
	if pid > 0 {
		_ = syscall.Kill(-pid, sig)
	}
}

// exitInfo extracts the exit code and signal name of a finished command.
func exitInfo(state *os.ProcessState) (int, string) {
	if state == nil {
		return -1, ""
	}
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return -1, unixSignalName(ws.Signal())
	}
	return state.ExitCode(), ""
}

func unixSignalName(s syscall.Signal) string {
	switch s {
	case syscall.SIGKILL:
		return "KILL"
	case syscall.SIGTERM:
		return "TERM"
	case syscall.SIGINT:
		return "INT"
	case syscall.SIGSEGV:
		return "SEGV"
	case syscall.SIGABRT:
		return "ABRT"
	case syscall.SIGHUP:
		return "HUP"
	case syscall.SIGPIPE:
		return "PIPE"
	}
	return strings.ToUpper(strings.TrimPrefix(s.String(), "signal "))
}

// Exec runs a command to completion.
func Exec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	if strings.TrimSpace(req.Command) == "" {
		return ExecResult{}, errors.New("command is required")
	}
	cmd, err := shellCommand(req.Command, req.Cwd, req.Env)
	if err != nil {
		return ExecResult{}, err
	}
	maxOut := req.MaxOutput
	if maxOut <= 0 {
		maxOut = 256 << 10
	}
	stdout, stderr := &Capped{Max: maxOut}, &Capped{Max: maxOut}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Stdin = strings.NewReader(req.Stdin)
	// Bound the wait for output pipes held open by background children.
	cmd.WaitDelay = 500 * time.Millisecond

	oomBefore := oomKills()
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return ExecResult{}, err
	}
	pid := cmd.Process.Pid
	// Wait for the shell (waitid with WNOWAIT keeps it reapable), then for
	// its output. Children it started in the background ("app &", nohup)
	// are left running: they may hold stdout/stderr open, and WaitDelay
	// stops waiting for those pipes shortly after the shell exits.
	done := make(chan error, 1)
	go func() {
		var info unixSiginfo
		_ = waitNoReap(pid, &info)
		done <- cmd.Wait()
	}()

	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	res := ExecResult{}
	select {
	case <-done:
	case <-timer.C:
		res.TimedOut = true
	case <-ctx.Done():
		res.TimedOut = true
	}
	if res.TimedOut {
		killGroup(pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			killGroup(pid, syscall.SIGKILL)
			<-done
		}
	}

	res.ExitCode, res.Signal = exitInfo(cmd.ProcessState)
	res.DurationMs = time.Since(start).Milliseconds()
	var t1, t2 bool
	res.Stdout, t1 = stdout.String()
	res.Stderr, t2 = stderr.String()
	res.Truncated = t1 || t2
	if res.Signal == "KILL" && !res.TimedOut && oomKills() > oomBefore {
		res.OOMKilled = true
	}
	if res.TimedOut {
		res.ExitCode = -1
		res.Stderr += fmt.Sprintf("\n[devenv] command timed out after %s and was killed", timeout)
	}
	if res.OOMKilled {
		res.Stderr += "\n[devenv] killed by the kernel for exceeding the environment's memory limit; raise memory with update_environment or use less"
	}
	return res, nil
}

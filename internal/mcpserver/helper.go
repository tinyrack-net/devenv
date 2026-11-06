package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/tinyrack-net/devenv/internal/agent"
)

// errNoHelper means the environment does not have devenv-agent mounted.
var errNoHelper = errors.New("helper unavailable")

// helperPath is where the helper is mounted; a variable for tests.
var helperPath = agent.Path

// callHelper runs one helper operation over a new session on the pooled
// connection and decodes its result.
func callHelper[T any](ctx context.Context, sc *ssh.Client, op string, req any, timeout time.Duration) (T, error) {
	var zero T
	body, err := json.Marshal(req)
	if err != nil {
		return zero, err
	}
	sess, err := sc.NewSession()
	if err != nil {
		return zero, errorf("open session: %v", err)
	}
	defer sess.Close()
	var stdout, stderr bytes.Buffer
	sess.Stdin = bytes.NewReader(body)
	sess.Stdout = &stdout
	sess.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- sess.Run(shellQuote(helperPath) + " " + op) }()
	select {
	case err = <-done:
	case <-time.After(timeout):
		_ = sess.Signal(ssh.SIGKILL)
		return zero, errorf("helper %s timed out after %s", op, timeout)
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		return zero, errorf("cancelled")
	}
	if err != nil {
		var ee *ssh.ExitError
		// 126/127: the mount is missing or not executable.
		if errors.As(err, &ee) && (ee.ExitStatus() == 127 || ee.ExitStatus() == 126) {
			return zero, errNoHelper
		}
		return zero, errorf("helper %s failed: %v %s", op, err, strings.TrimSpace(stderr.String()))
	}
	var resp agent.Response[T]
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return zero, errorf("helper %s: invalid response: %v", op, err)
	}
	if !resp.OK {
		return zero, &toolError{resp.Error}
	}
	return resp.Result, nil
}

// helper runs an operation in a (started) environment.
func helper[T any](s *Server, ctx context.Context, name, op string, req any, timeout time.Duration) (T, error) {
	var zero T
	sc, release, err := s.connect(ctx, name, true)
	if err != nil {
		return zero, err
	}
	defer release()
	res, err := callHelper[T](ctx, sc, op, req, timeout)
	if errors.Is(err, errNoHelper) {
		return zero, errorf("the devenv-agent helper is not available in environment %s (it is mounted at %s when the server has helper.enabled and the cluster supports image volumes); restart the environment after enabling it", name, helperPath)
	}
	return res, err
}

func fmtBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

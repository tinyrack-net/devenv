// Package helper calls devenv-agent inside environments over SSH.
package helper

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

// ErrUnavailable means the environment does not have devenv-agent mounted.
var ErrUnavailable = errors.New("helper unavailable")

// Path is where the helper is mounted; a variable for tests.
var Path = agent.Path

// Error is a failure reported by the helper itself (a user facing message).
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// ShellQuote quotes s for a POSIX shell.
func ShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Call runs one helper operation in a new session on sc and decodes its result.
func Call[T any](ctx context.Context, sc *ssh.Client, op string, req any, timeout time.Duration) (T, error) {
	var zero T
	body, err := json.Marshal(req)
	if err != nil {
		return zero, err
	}
	sess, err := sc.NewSession()
	if err != nil {
		return zero, fmt.Errorf("open session: %w", err)
	}
	defer sess.Close()
	var stdout, stderr bytes.Buffer
	sess.Stdin = bytes.NewReader(body)
	sess.Stdout = &stdout
	sess.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- sess.Run(ShellQuote(Path) + " " + op) }()
	select {
	case err = <-done:
	case <-time.After(timeout):
		_ = sess.Signal(ssh.SIGKILL)
		return zero, fmt.Errorf("helper %s timed out after %s", op, timeout)
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		return zero, ctx.Err()
	}
	if err != nil {
		var ee *ssh.ExitError
		// 126/127: the mount is missing or not executable.
		if errors.As(err, &ee) && (ee.ExitStatus() == 127 || ee.ExitStatus() == 126) {
			return zero, ErrUnavailable
		}
		return zero, fmt.Errorf("helper %s failed: %v %s", op, err, strings.TrimSpace(stderr.String()))
	}
	var resp agent.Response[T]
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return zero, fmt.Errorf("helper %s: invalid response: %v", op, err)
	}
	if !resp.OK {
		return zero, &Error{resp.Error}
	}
	return resp.Result, nil
}

package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/tinyrack-net/devenv/internal/helper"
)

// errNoHelper means the environment does not have devenv-agent mounted.
var errNoHelper = helper.ErrUnavailable

// callHelper runs one helper operation over a new session on the pooled
// connection and decodes its result. Helper errors become tool errors.
func callHelper[T any](ctx context.Context, sc *ssh.Client, op string, req any, timeout time.Duration) (T, error) {
	helper.Path = helperPath
	res, err := helper.Call[T](ctx, sc, op, req, timeout)
	var he *helper.Error
	switch {
	case err == nil, errors.Is(err, helper.ErrUnavailable):
		return res, err
	case errors.As(err, &he):
		return res, &toolError{he.Msg}
	case errors.Is(err, context.Canceled):
		return res, errorf("cancelled")
	}
	return res, errorf("%v", err)
}

// helperPath is where the helper is mounted; a variable for tests.
var helperPath = helper.Path

// helper runs an operation in a (started) environment.
func callEnv[T any](s *Server, ctx context.Context, name, op string, req any, timeout time.Duration) (T, error) {
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

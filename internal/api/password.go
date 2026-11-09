package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/tinyrack-net/devenv/internal/controller"
	"github.com/tinyrack-net/devenv/internal/model"
	"github.com/tinyrack-net/devenv/internal/sshclient"
)

func validatePassword(pw string) error {
	n := utf8.RuneCountInString(pw)
	if n < controller.MinPasswordLength {
		return badRequest(fmt.Sprintf("password must be at least %d characters", controller.MinPasswordLength))
	}
	if len(pw) > 72 { // bcrypt limit
		return badRequest("password must be at most 72 bytes")
	}
	if strings.ContainsAny(pw, "\x00\n\r") {
		return badRequest("password contains invalid characters")
	}
	return nil
}

// pushPassword applies the change inside the running environment right away
// (best effort; the environment also picks it up from its Secret).
func (s *Server) pushPassword(env *model.Environment, hash string) {
	if s.Dialer == nil || !env.IsRunning() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := s.Dialer.PushPassword(ctx, env.Name, hash); err != nil && !errors.Is(err, sshclient.ErrNotRunning) {
			logf.Log.WithName("api").Info("push password to environment failed; it will apply from the Secret", "environment", env.Name, "error", err.Error())
		}
	}()
}

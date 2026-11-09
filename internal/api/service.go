package api

import (
	"errors"

	"github.com/tinyrack-net/devenv/internal/access"
	"github.com/tinyrack-net/devenv/internal/model"
	"github.com/tinyrack-net/devenv/internal/store"
)

// Service functions shared with other frontends (MCP).

// ToEnvironment converts an environment to its API representation.
func ToEnvironment(e *model.Environment) Environment { return toEnvironment(e) }

// View converts an environment for a caller with the given access level.
func (s *Server) View(e *model.Environment, lvl access.Level) Environment {
	return s.view(e, lvl)
}

// ErrorMessage returns a user facing message and whether the error was caused by the request.
func ErrorMessage(err error) (msg string, userError bool) {
	var he *httpError
	switch {
	case errors.As(err, &he):
		return he.msg, he.code < 500
	case errors.Is(err, store.ErrNotFound):
		return "not found", true
	case errors.Is(err, store.ErrExists):
		return "already exists", true
	case errors.Is(err, store.ErrConflict):
		return "resource was modified concurrently, retry", true
	}
	return err.Error(), false
}

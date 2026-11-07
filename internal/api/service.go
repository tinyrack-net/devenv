package api

import (
	"errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
	"github.com/tinyrack-net/devenv/internal/access"
)

// Service functions shared with other frontends (MCP).

// ToEnvironment converts an environment to its API representation.
func ToEnvironment(e *devenvv1.DevEnvironment) Environment { return toEnvironment(e) }

// View converts an environment for a caller with the given access level.
func (s *Server) View(e *devenvv1.DevEnvironment, lvl access.Level) Environment {
	return s.view(e, lvl)
}

// ErrorMessage returns a user facing message and whether the error was caused by the request.
func ErrorMessage(err error) (msg string, userError bool) {
	var he *httpError
	switch {
	case errors.As(err, &he):
		return he.msg, he.code < 500
	case apierrors.IsNotFound(err):
		return "not found", true
	case apierrors.IsAlreadyExists(err):
		return "already exists", true
	case apierrors.IsConflict(err):
		return "resource was modified concurrently, retry", true
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err), apierrors.IsForbidden(err):
		return err.Error(), true
	}
	return err.Error(), false
}

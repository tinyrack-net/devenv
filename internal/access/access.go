// Package access decides what a principal may do with images, classes and
// environments. Every entry point (REST API, MCP, SSH gateway, terminal and
// VNC) uses these functions.
package access

import (
	"slices"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
	"github.com/tinyrack-net/devenv/internal/auth"
)

// Level of access to an environment.
type Level int

const (
	// None: the environment is invisible.
	None Level = iota
	// Use: view, connect (terminal, desktop, SSH, MCP tools), start and stop.
	Use
	// Manage: change the spec, sharing, password; delete.
	Manage
)

func (l Level) String() string {
	switch l {
	case Use:
		return "use"
	case Manage:
		return "manage"
	}
	return "none"
}

func granted(a devenvv1.Access, p *auth.Principal) bool {
	return slices.Contains(a.Users, p.Name) || p.InGroup(a.Groups...)
}

// Environment returns the principal's access level.
func Environment(p *auth.Principal, e *devenvv1.DevEnvironment) Level {
	if p == nil {
		return None
	}
	if p.Admin || e.Spec.Owner == p.Name {
		return Manage
	}
	if granted(e.Spec.Sharing, p) {
		return Use
	}
	return None
}

// CanUseImage reports whether a principal may create environments with an
// image (and see it in the catalog).
func CanUseImage(p *auth.Principal, img *devenvv1.DevImage) bool {
	if p == nil {
		return false
	}
	if p.Admin {
		return true
	}
	if img.Spec.Owner != "" {
		return img.Spec.Owner == p.Name
	}
	return img.Spec.Access.IsZero() || granted(img.Spec.Access, p)
}

// CanManageImage reports whether a principal may edit or delete an image.
func CanManageImage(p *auth.Principal, img *devenvv1.DevImage) bool {
	if p == nil {
		return false
	}
	return p.Admin || (img.Spec.Owner != "" && img.Spec.Owner == p.Name)
}

// CanUseClass reports whether a principal may create environments in a class.
func CanUseClass(p *auth.Principal, c *devenvv1.DevEnvironmentClass) bool {
	if p == nil {
		return false
	}
	return p.Admin || c.Spec.Access.IsZero() || granted(c.Spec.Access, p)
}

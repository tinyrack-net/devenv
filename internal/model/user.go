package model

import (
	"encoding/json"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
)

// Role of a user.
type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

// Identity links a user to an external login.
type Identity struct {
	// Provider is the id of an OIDC provider in the server configuration.
	Provider string `json:"provider"`
	// Subject is the provider's stable user id ("sub" claim).
	Subject string `json:"subject"`
}

// UserSSHKey is a public key of the user.
type UserSSHKey struct {
	// Name shown in the console.
	Name string `json:"name"`
	// PublicKey in authorized_keys format.
	PublicKey string     `json:"publicKey"`
	CreatedAt *time.Time `json:"createdAt,omitempty"`
}

// UserLimits caps what a user can consume. Empty fields fall back to the
// server defaults (users.defaultLimits); admins are unlimited by default.
type UserLimits struct {
	// MaxEnvironments is the number of environments the user may own.
	MaxEnvironments *int32 `json:"maxEnvironments,omitempty"`
	// MaxRunning is the number of environments that may run at once.
	MaxRunning *int32 `json:"maxRunning,omitempty"`
	// CPU is the total CPU of running environments.
	CPU *resource.Quantity `json:"cpu,omitempty"`
	// Memory is the total memory of running environments.
	Memory *resource.Quantity `json:"memory,omitempty"`
	// Disk is the total home volume size of all environments.
	Disk *resource.Quantity `json:"disk,omitempty"`
}

// UserSpec defines a console user.
type UserSpec struct {
	DisplayName string `json:"displayName,omitempty"`
	Email       string `json:"email,omitempty"`
	// Role defaults to user.
	Role Role `json:"role,omitempty"`
	// Disabled users can not sign in and their tokens and keys are rejected.
	Disabled bool `json:"disabled,omitempty"`
	// Identities are the external logins of the user.
	Identities []Identity `json:"identities,omitempty"`
	// SSHKeys are accepted for every environment the user can access.
	SSHKeys []UserSSHKey `json:"sshKeys,omitempty"`
	// Limits overrides the server defaults.
	Limits UserLimits `json:"limits,omitempty"`
	// Preferences are console settings, stored as is.
	Preferences json.RawMessage `json:"preferences,omitempty"`
}

// UserStatus is the observed state of a user.
type UserStatus struct {
	LastLoginAt *time.Time `json:"lastLoginAt,omitempty"`
	// LastLoginProvider is "password" or an OIDC provider id.
	LastLoginProvider string `json:"lastLoginProvider,omitempty"`
	// Groups reported by the identity provider at the last login.
	Groups []string `json:"groups,omitempty"`
}

// AccessKeys returns the SSH keys of the owner and of the users the
// environment is shared with (directly or through a group). Disabled users
// are skipped.
func AccessKeys(users []User, env *Environment) []string {
	want := map[string]bool{env.Spec.Owner: true}
	for _, u := range env.Spec.Sharing.Users {
		want[u] = true
	}
	groups := map[string]bool{}
	for _, g := range env.Spec.Sharing.Groups {
		groups[g] = true
	}
	var keys []string
	for _, u := range users {
		if u.Spec.Disabled {
			continue
		}
		ok := want[u.Name]
		for _, g := range u.Status.Groups {
			ok = ok || groups[g]
		}
		if !ok {
			continue
		}
		for _, k := range u.Spec.SSHKeys {
			keys = append(keys, k.PublicKey)
		}
	}
	return keys
}

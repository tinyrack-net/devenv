package api

import (
	"encoding/json"
	"time"

	"github.com/tinyrack-net/devenv/internal/capacity"
	"github.com/tinyrack-net/devenv/internal/tokens"
)

// AccessInput lists users and groups.
type AccessInput struct {
	Users  []string `json:"users"`
	Groups []string `json:"groups"`
}

// Image is the API representation of a DevImage.
type Image struct {
	Name             string         `json:"name"`
	Image            string         `json:"image"`
	DisplayName      string         `json:"displayName,omitempty"`
	Description      string         `json:"description,omitempty"`
	Desktop          bool           `json:"desktop"`
	Disabled         bool           `json:"disabled"`
	Prepull          bool           `json:"prepull"`
	ImagePullPolicy  string         `json:"imagePullPolicy,omitempty"`
	DefaultResources ResourcesInput `json:"defaultResources"`
	// Owner is set for personal images.
	Owner     string      `json:"owner,omitempty"`
	Access    AccessInput `json:"access"`
	CreatedAt time.Time   `json:"createdAt"`
	InUse     int         `json:"inUse"`
	// CanManage reports whether the caller may edit the image.
	CanManage bool `json:"canManage"`
}

// ResourcesInput uses strings so the frontend can send "2", "4Gi" etc.
type ResourcesInput struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
	Disk   string `json:"disk,omitempty"`
}

// Class is the API representation of a DevEnvironmentClass.
type Class struct {
	Name         string         `json:"name"`
	StorageMode  string         `json:"storageMode"`
	StorageClass string         `json:"storageClass,omitempty"`
	DefaultDisk  string         `json:"defaultDisk,omitempty"`
	NetworkMode  string         `json:"networkMode"`
	Limits       ResourcesInput `json:"limits"`
	Default      bool           `json:"default"`
	Access       AccessInput    `json:"access"`
}

// ClassDetail adds the full spec for admins.
type ClassDetail struct {
	Class
	Spec json.RawMessage `json:"spec"`
}

// Environment is the API representation of a DevEnvironment.
type Environment struct {
	Name        string         `json:"name"`
	Owner       string         `json:"owner,omitempty"`
	Image       string         `json:"image"`
	Class       string         `json:"class"`
	Resources   ResourcesInput `json:"resources"`
	Running     bool           `json:"running"`
	Phase       string         `json:"phase"`
	Message     string         `json:"message,omitempty"`
	Desktop     bool           `json:"desktop"`
	ImageRef    string         `json:"imageRef,omitempty"`
	NodeName    string         `json:"nodeName,omitempty"`
	IP          string         `json:"ip,omitempty"`
	MAC         string         `json:"mac,omitempty"`
	NetworkMode string         `json:"networkMode,omitempty"`
	Ports       []PortInput    `json:"ports"`
	PasswordSet bool           `json:"passwordSet"`
	SSHKeys     []string       `json:"sshAuthorizedKeys"`
	Sharing     AccessInput    `json:"sharing"`
	CreatedAt   time.Time      `json:"createdAt"`
	Deleting    bool           `json:"deleting"`
	ServiceName string         `json:"serviceName,omitempty"`
	// CreatedBy is "mcp" for environments created by agents.
	CreatedBy string `json:"createdBy,omitempty"`
	// Access is the caller's level: "use" or "manage".
	Access string `json:"access"`
}

// CreateEnvironmentRequest creates an environment.
type CreateEnvironmentRequest struct {
	Name      string         `json:"name"`
	Image     string         `json:"image"`
	Class     string         `json:"class,omitempty"`
	Resources ResourcesInput `json:"resources"`
	// SSHKeys are extra keys besides the keys of the owner and the users the
	// environment is shared with (which always work).
	SSHKeys *[]string   `json:"sshAuthorizedKeys,omitempty"`
	Running *bool       `json:"running,omitempty"`
	Ports   []PortInput `json:"ports,omitempty"`
	// Password of the dev user; enables SSH password login. Optional.
	Password string       `json:"password,omitempty"`
	Sharing  *AccessInput `json:"sharing,omitempty"`
	// Owner creates the environment for another user (admins only).
	Owner string `json:"owner,omitempty"`
}

// PortInput is a port exposed in loadBalancer network mode.
type PortInput struct {
	Port     int32  `json:"port"`
	Protocol string `json:"protocol,omitempty"`
	Name     string `json:"name,omitempty"`
}

// UpdateEnvironmentRequest patches an environment. Nil fields are unchanged.
type UpdateEnvironmentRequest struct {
	Image     *string         `json:"image,omitempty"`
	Resources *ResourcesInput `json:"resources,omitempty"`
	SSHKeys   *[]string       `json:"sshAuthorizedKeys,omitempty"`
	Running   *bool           `json:"running,omitempty"`
	Ports     *[]PortInput    `json:"ports,omitempty"`
	// Password sets the password; an empty string removes it.
	Password *string      `json:"password,omitempty"`
	Sharing  *AccessInput `json:"sharing,omitempty"`
	// Owner transfers the environment (admins only).
	Owner *string `json:"owner,omitempty"`
}

// SSHKey is a stored public key of the user.
type SSHKey struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	PublicKey   string    `json:"publicKey"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Capacity is returned for a class.
type Capacity struct {
	Class   string                  `json:"class"`
	MaxCPU  string                  `json:"maxCPU"`
	MaxMem  string                  `json:"maxMemory"`
	MaxDisk string                  `json:"maxDisk,omitempty"`
	Nodes   []capacity.NodeCapacity `json:"nodes"`
}

// Event is a Kubernetes event related to an environment.
type Event struct {
	Type    string    `json:"type"`
	Reason  string    `json:"reason"`
	Message string    `json:"message"`
	Object  string    `json:"object"`
	Count   int32     `json:"count"`
	Time    time.Time `json:"time"`
}

// Info describes the server to unauthenticated clients (login page).
type Info struct {
	Version   string         `json:"version"`
	Namespace string         `json:"namespace"`
	SSH       SSHInfo        `json:"ssh"`
	Auth      AuthInfo       `json:"auth"`
	MCP       bool           `json:"mcp"`
	Providers []ProviderInfo `json:"providers"`
}

// AuthInfo describes the login options.
type AuthInfo struct {
	// Disabled means every request is the local admin (development).
	Disabled bool `json:"disabled"`
	Password bool `json:"password"`
}

// ProviderInfo is an OIDC login button.
type ProviderInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	LoginURL string `json:"loginURL"`
}

// SSHInfo tells clients how to reach the SSH gateway.
type SSHInfo struct {
	Enabled bool `json:"enabled"`
	// Host is the public gateway host. Empty means "same host as the console".
	Host string `json:"host,omitempty"`
	Port int    `json:"port"`
	// HostKey is the gateway's public host key (authorized_keys format).
	HostKey     string `json:"hostKey,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// Me is the current user.
type Me struct {
	Name        string          `json:"name"`
	DisplayName string          `json:"displayName,omitempty"`
	Email       string          `json:"email,omitempty"`
	Admin       bool            `json:"admin"`
	Groups      []string        `json:"groups"`
	Method      string          `json:"method"`
	Provider    string          `json:"provider,omitempty"`
	Identities  []IdentityInfo  `json:"identities"`
	Preferences json.RawMessage `json:"preferences"`
}

// IdentityInfo is a linked login.
type IdentityInfo struct {
	Provider string `json:"provider"`
	Subject  string `json:"subject"`
}

// UpdateMeRequest changes the current user's own settings.
type UpdateMeRequest struct {
	DisplayName *string          `json:"displayName,omitempty"`
	Preferences *json.RawMessage `json:"preferences,omitempty"`
}

// Usage is a user's consumption against their limits. Empty limit fields mean
// unlimited.
type Usage struct {
	Environments int32  `json:"environments"`
	Running      int32  `json:"running"`
	CPU          string `json:"cpu"`
	Memory       string `json:"memory"`
	Disk         string `json:"disk"`
	Limits       struct {
		MaxEnvironments *int32 `json:"maxEnvironments,omitempty"`
		MaxRunning      *int32 `json:"maxRunning,omitempty"`
		CPU             string `json:"cpu,omitempty"`
		Memory          string `json:"memory,omitempty"`
		Disk            string `json:"disk,omitempty"`
	} `json:"limits"`
}

// UserSummary is shown in user pickers (sharing) to everyone.
type UserSummary struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
}

// User is the admin view of a DevUser.
type User struct {
	Name              string         `json:"name"`
	DisplayName       string         `json:"displayName,omitempty"`
	Email             string         `json:"email,omitempty"`
	Role              string         `json:"role"`
	Admin             bool           `json:"admin"`
	Disabled          bool           `json:"disabled"`
	Identities        []IdentityInfo `json:"identities"`
	Groups            []string       `json:"groups"`
	LastLoginAt       *time.Time     `json:"lastLoginAt,omitempty"`
	LastLoginProvider string         `json:"lastLoginProvider,omitempty"`
	SSHKeys           int            `json:"sshKeys"`
	Limits            LimitsInput    `json:"limits"`
	Usage             *Usage         `json:"usage,omitempty"`
	CreatedAt         time.Time      `json:"createdAt"`
	Local             bool           `json:"local"`
}

// LimitsInput sets per-user limits. Empty strings and nil numbers mean "use
// the server default".
type LimitsInput struct {
	MaxEnvironments *int32 `json:"maxEnvironments,omitempty"`
	MaxRunning      *int32 `json:"maxRunning,omitempty"`
	CPU             string `json:"cpu,omitempty"`
	Memory          string `json:"memory,omitempty"`
	Disk            string `json:"disk,omitempty"`
}

// CreateUserRequest creates a user in advance (admin).
type CreateUserRequest struct {
	Name        string       `json:"name"`
	DisplayName string       `json:"displayName,omitempty"`
	Email       string       `json:"email,omitempty"`
	Role        string       `json:"role,omitempty"`
	Limits      *LimitsInput `json:"limits,omitempty"`
}

// UpdateUserRequest changes a user (admin).
type UpdateUserRequest struct {
	DisplayName *string      `json:"displayName,omitempty"`
	Email       *string      `json:"email,omitempty"`
	Role        *string      `json:"role,omitempty"`
	Disabled    *bool        `json:"disabled,omitempty"`
	Limits      *LimitsInput `json:"limits,omitempty"`
	// Identities replaces the linked logins (unlink by removing).
	Identities *[]IdentityInfo `json:"identities,omitempty"`
}

// CreateTokenRequest issues a personal access token.
type CreateTokenRequest struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes,omitempty"`
	// ExpiresInDays, 0 means no expiry.
	ExpiresInDays int `json:"expiresInDays,omitempty"`
}

// CreatedToken includes the plain token, shown once.
type CreatedToken struct {
	Token *tokens.Token `json:"token"`
	Value string        `json:"value"`
}

// System is the admin system overview.
type System struct {
	Version   string                  `json:"version"`
	Namespace string                  `json:"namespace"`
	Config    any                     `json:"config"`
	SSH       SSHInfo                 `json:"ssh"`
	Helper    string                  `json:"helperImage,omitempty"`
	Nodes     []capacity.NodeCapacity `json:"nodes"`
	Counts    map[string]int          `json:"counts"`
}

type capacityNode = capacity.NodeCapacity

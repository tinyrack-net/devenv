package api

import (
	"time"

	"github.com/tinyrack-net/devenv/internal/capacity"
)

// Image is the API representation of a DevImage.
type Image struct {
	Name             string         `json:"name"`
	Image            string         `json:"image"`
	DisplayName      string         `json:"displayName,omitempty"`
	Description      string         `json:"description,omitempty"`
	Desktop          bool           `json:"desktop"`
	Disabled         bool           `json:"disabled"`
	ImagePullPolicy  string         `json:"imagePullPolicy,omitempty"`
	DefaultResources ResourcesInput `json:"defaultResources"`
	CreatedAt        time.Time      `json:"createdAt"`
	InUse            int            `json:"inUse"`
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
	CreatedAt   time.Time      `json:"createdAt"`
	Deleting    bool           `json:"deleting"`
	ServiceName string         `json:"serviceName,omitempty"`
}

// CreateEnvironmentRequest creates an environment.
type CreateEnvironmentRequest struct {
	Name      string         `json:"name"`
	Image     string         `json:"image"`
	Class     string         `json:"class,omitempty"`
	Resources ResourcesInput `json:"resources"`
	// SSHKeys defaults to all stored user keys when nil.
	SSHKeys *[]string   `json:"sshAuthorizedKeys,omitempty"`
	Running *bool       `json:"running,omitempty"`
	Ports   []PortInput `json:"ports,omitempty"`
	// Password of the dev user; enables SSH password login. Optional.
	Password string `json:"password,omitempty"`
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
	Password *string `json:"password,omitempty"`
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

// Info describes the server.
type Info struct {
	AuthMode  string  `json:"authMode"`
	Namespace string  `json:"namespace"`
	Version   string  `json:"version"`
	SSH       SSHInfo `json:"ssh"`
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

type capacityNode = capacity.NodeCapacity

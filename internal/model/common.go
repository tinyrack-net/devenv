package model

import "k8s.io/apimachinery/pkg/api/resource"

// Resources describes the compute and storage size of an environment.
type Resources struct {
	// CPU is the number of CPU cores. Used as both request and limit.
	CPU *resource.Quantity `json:"cpu,omitempty"`

	// Memory is the amount of memory. Used as both request and limit.
	Memory *resource.Quantity `json:"memory,omitempty"`

	// Disk is the size of the persistent home volume.
	// Ignored when the class uses ephemeral storage.
	Disk *resource.Quantity `json:"disk,omitempty"`
}

const (
	// DefaultCPU is applied when neither the environment nor the image specify CPU.
	DefaultCPU = "1"
	// DefaultMemory is applied when neither the environment nor the image specify memory.
	DefaultMemory = "2Gi"
	// DefaultDisk is applied when neither the environment, image nor class specify disk.
	DefaultDisk = "10Gi"
)

// Access restricts who may use a shared resource. Empty means everyone.
type Access struct {
	// Groups whose members may use the resource (identity provider groups).
	Groups []string `json:"groups,omitempty"`
	// Users who may use the resource (user names).
	Users []string `json:"users,omitempty"`
}

// IsZero reports an unrestricted access list.
func (a *Access) IsZero() bool { return a == nil || (len(a.Groups) == 0 && len(a.Users) == 0) }

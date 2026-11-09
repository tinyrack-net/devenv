package model

import (
	"encoding/json"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// StorageMode selects how the home directory is stored.
type StorageMode string

const (
	StorageModePersistent StorageMode = "persistent"
	StorageModeEphemeral  StorageMode = "ephemeral"
)

// NetworkMode selects how an environment is reachable from outside the cluster.
type NetworkMode string

const (
	// NetworkModeGateway exposes SSH through the built-in SSH gateway. Works everywhere.
	NetworkModeGateway NetworkMode = "gateway"
	// NetworkModeLoadBalancer creates a LoadBalancer Service per environment.
	NetworkModeLoadBalancer NetworkMode = "loadBalancer"
	// NetworkModeMultus attaches a secondary network interface via Multus.
	NetworkModeMultus NetworkMode = "multus"
)

// StorageSpec configures the home volume.
type StorageSpec struct {
	// Mode is persistent (PVC) or ephemeral (emptyDir).
	Mode StorageMode `json:"mode,omitempty"`

	// StorageClassName for the PVC. Empty uses the cluster default.
	StorageClassName *string `json:"storageClassName,omitempty"`

	// DefaultSize of the home volume.
	DefaultSize *resource.Quantity `json:"defaultSize,omitempty"`

	// Labels added to home volume PVCs, e.g. to select them for backups
	// (recurring-job-group.longhorn.io/backup: enabled).
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations added to home volume PVCs.
	Annotations map[string]string `json:"annotations,omitempty"`
}

// LoadBalancerSpec configures loadBalancer network mode.
type LoadBalancerSpec struct {
	// LoadBalancerClassName for the Service.
	LoadBalancerClassName *string `json:"loadBalancerClassName,omitempty"`

	// Annotations added to the Service.
	Annotations map[string]string `json:"annotations,omitempty"`

	// Labels added to the Service, e.g. for LB IP pool selection.
	Labels map[string]string `json:"labels,omitempty"`
}

// MultusSpec configures multus network mode.
type MultusSpec struct {
	// NetworkAttachment is the NetworkAttachmentDefinition reference as
	// "namespace/name" or "name" (same namespace as the environment).
	NetworkAttachment string `json:"networkAttachment"`

	// Interface name inside the pod.
	Interface string `json:"interface,omitempty"`

	// StableMAC assigns a deterministic MAC address derived from the environment name,
	// so DHCP based IPAM keeps the same lease across restarts.
	// Requires the CNI plugin to support the "mac" capability.
	StableMAC bool `json:"stableMAC,omitempty"`
}

// NetworkSpec configures external reachability.
type NetworkSpec struct {
	Mode NetworkMode `json:"mode,omitempty"`

	LoadBalancer *LoadBalancerSpec `json:"loadBalancer,omitempty"`

	Multus *MultusSpec `json:"multus,omitempty"`
}

// Limits restricts what an environment of this class may request.
type Limits struct {
	MaxCPU    *resource.Quantity `json:"maxCPU,omitempty"`
	MaxMemory *resource.Quantity `json:"maxMemory,omitempty"`
	MaxDisk   *resource.Quantity `json:"maxDisk,omitempty"`
}

// SchedulingSpec controls pod placement.
type SchedulingSpec struct {
	NodeSelector      map[string]string   `json:"nodeSelector,omitempty"`
	Tolerations       []corev1.Toleration `json:"tolerations,omitempty"`
	Affinity          *corev1.Affinity    `json:"affinity,omitempty"`
	RuntimeClassName  *string             `json:"runtimeClassName,omitempty"`
	PriorityClassName string              `json:"priorityClassName,omitempty"`
}

// ClassSpec adapts environments to the cluster.
type ClassSpec struct {
	Storage StorageSpec `json:"storage,omitempty"`

	Network NetworkSpec `json:"network,omitempty"`

	Limits Limits `json:"limits,omitempty"`

	Scheduling SchedulingSpec `json:"scheduling,omitempty"`

	// Access limits the class to some groups or users. Empty means everyone.
	// Admins can always use every class.
	Access Access `json:"access,omitempty"`

	// PodOverrides is a strategic merge patch applied to the generated Pod.
	// Escape hatch for anything not covered by the fields above.
	PodOverrides json.RawMessage `json:"podOverrides,omitempty"`
}

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// StorageMode selects how the home directory is stored.
// +kubebuilder:validation:Enum=persistent;ephemeral
type StorageMode string

const (
	StorageModePersistent StorageMode = "persistent"
	StorageModeEphemeral  StorageMode = "ephemeral"
)

// NetworkMode selects how an environment is reachable from outside the cluster.
// +kubebuilder:validation:Enum=gateway;loadBalancer;multus
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
	// +kubebuilder:default=persistent
	// +optional
	Mode StorageMode `json:"mode,omitempty"`

	// StorageClassName for the PVC. Empty uses the cluster default.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`

	// DefaultSize of the home volume.
	// +optional
	DefaultSize *resource.Quantity `json:"defaultSize,omitempty"`
}

// LoadBalancerSpec configures loadBalancer network mode.
type LoadBalancerSpec struct {
	// LoadBalancerClassName for the Service.
	// +optional
	LoadBalancerClassName *string `json:"loadBalancerClassName,omitempty"`

	// Annotations added to the Service.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// Labels added to the Service, e.g. for LB IP pool selection.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// MultusSpec configures multus network mode.
type MultusSpec struct {
	// NetworkAttachment is the NetworkAttachmentDefinition reference as
	// "namespace/name" or "name" (same namespace as the environment).
	// +kubebuilder:validation:MinLength=1
	NetworkAttachment string `json:"networkAttachment"`

	// Interface name inside the pod.
	// +kubebuilder:default=net1
	// +optional
	Interface string `json:"interface,omitempty"`

	// StableMAC assigns a deterministic MAC address derived from the environment UID,
	// so DHCP based IPAM keeps the same lease across restarts.
	// Requires the CNI plugin to support the "mac" capability.
	// +optional
	StableMAC bool `json:"stableMAC,omitempty"`
}

// NetworkSpec configures external reachability.
// +kubebuilder:validation:XValidation:rule="self.mode != 'multus' || has(self.multus)",message="multus is required when mode is multus"
type NetworkSpec struct {
	// +kubebuilder:default=gateway
	// +optional
	Mode NetworkMode `json:"mode,omitempty"`

	// +optional
	LoadBalancer *LoadBalancerSpec `json:"loadBalancer,omitempty"`

	// +optional
	Multus *MultusSpec `json:"multus,omitempty"`
}

// Limits restricts what an environment of this class may request.
type Limits struct {
	// +optional
	MaxCPU *resource.Quantity `json:"maxCPU,omitempty"`
	// +optional
	MaxMemory *resource.Quantity `json:"maxMemory,omitempty"`
	// +optional
	MaxDisk *resource.Quantity `json:"maxDisk,omitempty"`
}

// SchedulingSpec controls pod placement.
type SchedulingSpec struct {
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`
	// +optional
	RuntimeClassName *string `json:"runtimeClassName,omitempty"`
	// +optional
	PriorityClassName string `json:"priorityClassName,omitempty"`
}

// DevEnvironmentClassSpec adapts environments to the cluster.
type DevEnvironmentClassSpec struct {
	// +optional
	Storage StorageSpec `json:"storage,omitempty"`

	// +optional
	Network NetworkSpec `json:"network,omitempty"`

	// +optional
	Limits Limits `json:"limits,omitempty"`

	// +optional
	Scheduling SchedulingSpec `json:"scheduling,omitempty"`

	// Access limits the class to some groups or users. Empty means everyone.
	// Admins can always use every class.
	// +optional
	Access Access `json:"access,omitempty"`

	// PodOverrides is a strategic merge patch applied to the generated Pod.
	// Escape hatch for anything not covered by the fields above.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:Type=object
	// +optional
	PodOverrides *runtime.RawExtension `json:"podOverrides,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=declass
// +kubebuilder:printcolumn:name="Storage",type=string,JSONPath=`.spec.storage.mode`
// +kubebuilder:printcolumn:name="Network",type=string,JSONPath=`.spec.network.mode`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DevEnvironmentClass describes how environments are realized on this cluster.
type DevEnvironmentClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec DevEnvironmentClassSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// DevEnvironmentClassList contains a list of DevEnvironmentClass.
type DevEnvironmentClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DevEnvironmentClass `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DevEnvironmentClass{}, &DevEnvironmentClassList{})
}

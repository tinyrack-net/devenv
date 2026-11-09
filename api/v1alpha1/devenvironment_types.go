package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DefaultClassName is used when spec.className is empty.
const DefaultClassName = "default"

// DevEnvironmentSpec defines the desired state of DevEnvironment.
type DevEnvironmentSpec struct {
	// Owner is the DevUser owning this environment.
	// +optional
	Owner string `json:"owner,omitempty"`

	// Sharing grants other users access (connect, start, stop). Only the
	// owner and admins can change the environment.
	// +optional
	Sharing Access `json:"sharing,omitempty"`

	// ImageRef is the name of a DevImage.
	// +kubebuilder:validation:MinLength=1
	ImageRef string `json:"imageRef"`

	// ClassName is the name of a DevEnvironmentClass. Defaults to "default".
	// It can change between classes with the same storage (mode and storage
	// class); the pod is recreated, network and limits follow the new class.
	// +optional
	ClassName string `json:"className,omitempty"`

	// Resources requested for the environment.
	// +kubebuilder:validation:XValidation:rule="!has(oldSelf.disk) || (has(self.disk) && quantity(string(self.disk)).compareTo(quantity(string(oldSelf.disk))) >= 0)",message="disk can not be shrunk"
	// +optional
	Resources Resources `json:"resources,omitempty"`

	// Running controls whether the environment pod exists.
	// Stopping keeps the home volume.
	// +kubebuilder:default=true
	// +optional
	Running *bool `json:"running,omitempty"`

	// SSHAuthorizedKeys are written to the environment's authorized_keys.
	// +optional
	SSHAuthorizedKeys []string `json:"sshAuthorizedKeys,omitempty"`

	// PasswordSecretName references a Secret in the environment namespace
	// holding the password of the dev user, either as a bcrypt hash in key
	// "passwordHash" or in plain text in key "password". The password enables
	// SSH password login (directly and through the gateway). Empty disables
	// password login.
	// +optional
	PasswordSecretName string `json:"passwordSecretName,omitempty"`

	// Ports exposed on the environment's own address in loadBalancer
	// network mode. SSH (22) is always exposed. Ignored in other modes
	// (multus exposes every port).
	// +listType=map
	// +listMapKey=port
	// +listMapKey=protocol
	// +kubebuilder:validation:MaxItems=64
	// +optional
	Ports []Port `json:"ports,omitempty"`
}

// Port is a port exposed in loadBalancer network mode.
type Port struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`
	// +kubebuilder:validation:Enum=TCP;UDP
	// +kubebuilder:default=TCP
	Protocol string `json:"protocol"`
	// Name is an optional label shown in the console.
	// +kubebuilder:validation:MaxLength=63
	// +optional
	Name string `json:"name,omitempty"`
}

// Phase is a high level summary of the environment state.
// +kubebuilder:validation:Enum=Pending;Starting;Running;Stopping;Stopped;Failed
type Phase string

const (
	PhasePending  Phase = "Pending"
	PhaseStarting Phase = "Starting"
	PhaseRunning  Phase = "Running"
	PhaseStopping Phase = "Stopping"
	PhaseStopped  Phase = "Stopped"
	PhaseFailed   Phase = "Failed"
)

// Condition types.
const (
	ConditionReady = "Ready"
)

// DevEnvironmentStatus defines the observed state of DevEnvironment.
type DevEnvironmentStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	Phase Phase `json:"phase,omitempty"`

	// Message is a human readable explanation of the phase.
	// +optional
	Message string `json:"message,omitempty"`

	// Image is the resolved container image.
	// +optional
	Image string `json:"image,omitempty"`

	// Desktop reports whether VNC is available.
	// +optional
	Desktop bool `json:"desktop,omitempty"`

	// Resources are the effective resources after applying image and class
	// defaults to spec.resources.
	// +optional
	Resources Resources `json:"resources,omitempty"`

	// PodName of the running environment.
	// +optional
	PodName string `json:"podName,omitempty"`

	// ServiceName used to reach the environment inside the cluster.
	// +optional
	ServiceName string `json:"serviceName,omitempty"`

	// NodeName where the environment runs.
	// +optional
	NodeName string `json:"nodeName,omitempty"`

	// IP is the externally reachable address (loadBalancer or multus mode).
	// +optional
	IP string `json:"ip,omitempty"`

	// NetworkMode is the effective network mode of the class.
	// +optional
	NetworkMode NetworkMode `json:"networkMode,omitempty"`

	// PasswordSet reports whether password login is enabled.
	// +optional
	PasswordSet bool `json:"passwordSet,omitempty"`

	// MAC is the secondary interface MAC address (multus mode).
	// +optional
	MAC string `json:"mac,omitempty"`

	// StableMAC is the MAC used for multus classes with stableMAC. It is
	// derived from the namespace and name, so a restored environment keeps
	// its address.
	// +optional
	StableMAC string `json:"stableMAC,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=denv
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=`.spec.owner`
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.imageRef`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="IP",type=string,JSONPath=`.status.ip`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DevEnvironment is a single remote development environment.
type DevEnvironment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DevEnvironmentSpec   `json:"spec"`
	Status DevEnvironmentStatus `json:"status,omitempty"`
}

// IsRunning reports the desired running state (default true).
func (e *DevEnvironment) IsRunning() bool {
	return e.Spec.Running == nil || *e.Spec.Running
}

// ClassName returns the effective class name.
func (e *DevEnvironment) ClassName() string {
	if e.Spec.ClassName == "" {
		return DefaultClassName
	}
	return e.Spec.ClassName
}

// +kubebuilder:object:root=true

// DevEnvironmentList contains a list of DevEnvironment.
type DevEnvironmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DevEnvironment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DevEnvironment{}, &DevEnvironmentList{})
}

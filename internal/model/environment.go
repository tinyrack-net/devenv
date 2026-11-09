package model

// DefaultClassName is used when spec.className is empty.
const DefaultClassName = "default"

// EnvironmentSpec defines the desired state of DevEnvironment.
type EnvironmentSpec struct {
	// Owner is the DevUser owning this environment.
	Owner string `json:"owner,omitempty"`

	// Sharing grants other users access (connect, start, stop). Only the
	// owner and admins can change the environment.
	Sharing Access `json:"sharing,omitempty"`

	// ImageRef is the name of a DevImage.
	ImageRef string `json:"imageRef"`

	// ClassName is the name of a DevEnvironmentClass. Defaults to "default".
	// It can change between classes with the same storage (mode and storage
	// class); the pod is recreated, network and limits follow the new class.
	ClassName string `json:"className,omitempty"`

	// Resources requested for the environment.
	Resources Resources `json:"resources,omitempty"`

	// Running controls whether the environment pod exists.
	// Stopping keeps the home volume.
	Running *bool `json:"running,omitempty"`

	// SSHAuthorizedKeys are written to the environment's authorized_keys.
	SSHAuthorizedKeys []string `json:"sshAuthorizedKeys,omitempty"`

	// Ports exposed on the environment's own address in loadBalancer
	// network mode. SSH (22) is always exposed. Ignored in other modes
	// (multus exposes every port).
	Ports []Port `json:"ports,omitempty"`
}

// Port is a port exposed in loadBalancer network mode.
type Port struct {
	Port     int32  `json:"port"`
	Protocol string `json:"protocol"`
	// Name is an optional label shown in the console.
	Name string `json:"name,omitempty"`
}

// Phase is a high level summary of the environment state.
type Phase string

const (
	PhasePending  Phase = "Pending"
	PhaseStarting Phase = "Starting"
	PhaseRunning  Phase = "Running"
	PhaseStopping Phase = "Stopping"
	PhaseStopped  Phase = "Stopped"
	PhaseFailed   Phase = "Failed"
)

// EnvironmentStatus defines the observed state of DevEnvironment.
type EnvironmentStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	Phase Phase `json:"phase,omitempty"`

	// Message is a human readable explanation of the phase.
	Message string `json:"message,omitempty"`

	// Image is the resolved container image.
	Image string `json:"image,omitempty"`

	// Desktop reports whether VNC is available.
	Desktop bool `json:"desktop,omitempty"`

	// Resources are the effective resources after applying image and class
	// defaults to spec.resources.
	Resources Resources `json:"resources,omitempty"`

	// PodName of the running environment.
	PodName string `json:"podName,omitempty"`

	// ServiceName used to reach the environment inside the cluster.
	ServiceName string `json:"serviceName,omitempty"`

	// NodeName where the environment runs.
	NodeName string `json:"nodeName,omitempty"`

	// IP is the externally reachable address (loadBalancer or multus mode).
	IP string `json:"ip,omitempty"`

	// NetworkMode is the effective network mode of the class.
	NetworkMode NetworkMode `json:"networkMode,omitempty"`

	// PasswordSet reports whether password login is enabled.
	PasswordSet bool `json:"passwordSet,omitempty"`

	// MAC is the secondary interface MAC address (multus mode).
	MAC string `json:"mac,omitempty"`

	// StableMAC is the MAC used for multus classes with stableMAC. It is
	// derived from the name and pinned on first use, so a restored environment keeps
	// its address.
	StableMAC string `json:"stableMAC,omitempty"`

	// Reason is a machine readable phase reason.
	Reason string `json:"reason,omitempty"`
}

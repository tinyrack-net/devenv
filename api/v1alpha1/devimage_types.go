package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DevImageSpec defines a container image that can be used for environments.
type DevImageSpec struct {
	// Image is the container image reference.
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// ImagePullPolicy for the image.
	// +optional
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// DisplayName is a human friendly name shown in the console.
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// Description shown in the console.
	// +optional
	Description string `json:"description,omitempty"`

	// Desktop indicates that the image provides a VNC desktop on port 5901.
	// +optional
	Desktop bool `json:"desktop,omitempty"`

	// DefaultResources are used when an environment does not specify resources.
	// +optional
	DefaultResources Resources `json:"defaultResources,omitempty"`

	// Prepull keeps the image on every node that can run environments so new
	// environments start without waiting for the pull.
	// +optional
	Prepull bool `json:"prepull,omitempty"`

	// Disabled hides the image from the console for new environments.
	// Existing environments keep working.
	// +optional
	Disabled bool `json:"disabled,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=dimg
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`
// +kubebuilder:printcolumn:name="Desktop",type=boolean,JSONPath=`.spec.desktop`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DevImage is a catalog entry for a development environment image.
type DevImage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec DevImageSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// DevImageList contains a list of DevImage.
type DevImageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DevImage `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DevImage{}, &DevImageList{})
}

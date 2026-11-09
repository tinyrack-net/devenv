package model

import (
	corev1 "k8s.io/api/core/v1"
)

// ImageSpec defines a container image that can be used for environments.
type ImageSpec struct {
	// Image is the container image reference.
	Image string `json:"image"`

	// ImagePullPolicy for the image.
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// DisplayName is a human friendly name shown in the console.
	DisplayName string `json:"displayName,omitempty"`

	// Description shown in the console.
	Description string `json:"description,omitempty"`

	// Desktop indicates that the image provides a VNC desktop on port 5901.
	Desktop bool `json:"desktop,omitempty"`

	// DefaultResources are used when an environment does not specify resources.
	DefaultResources Resources `json:"defaultResources,omitempty"`

	// Prepull keeps the image on every node that can run environments so new
	// environments start without waiting for the pull.
	Prepull bool `json:"prepull,omitempty"`

	// Owner makes the image personal: only the owner (and admins) can see
	// and use it. Empty means a shared image managed by admins.
	Owner string `json:"owner,omitempty"`

	// Access limits a shared image to some groups or users. Empty means
	// everyone. Ignored for personal images.
	Access Access `json:"access,omitempty"`

	// Disabled hides the image from the console for new environments.
	// Existing environments keep working.
	Disabled bool `json:"disabled,omitempty"`
}

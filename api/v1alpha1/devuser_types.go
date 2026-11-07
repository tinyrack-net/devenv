package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Role of a user.
// +kubebuilder:validation:Enum=admin;user
type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

// Identity links a user to an external login.
type Identity struct {
	// Provider is the id of an OIDC provider in the server configuration.
	// +kubebuilder:validation:MinLength=1
	Provider string `json:"provider"`
	// Subject is the provider's stable user id ("sub" claim).
	// +kubebuilder:validation:MinLength=1
	Subject string `json:"subject"`
}

// UserSSHKey is a public key of the user.
type UserSSHKey struct {
	// Name shown in the console.
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`
	// PublicKey in authorized_keys format.
	// +kubebuilder:validation:MinLength=1
	PublicKey string `json:"publicKey"`
	// +optional
	CreatedAt *metav1.Time `json:"createdAt,omitempty"`
}

// UserLimits caps what a user can consume. Empty fields fall back to the
// server defaults (users.defaultLimits); admins are unlimited by default.
type UserLimits struct {
	// MaxEnvironments is the number of environments the user may own.
	// +optional
	MaxEnvironments *int32 `json:"maxEnvironments,omitempty"`
	// MaxRunning is the number of environments that may run at once.
	// +optional
	MaxRunning *int32 `json:"maxRunning,omitempty"`
	// CPU is the total CPU of running environments.
	// +optional
	CPU *resource.Quantity `json:"cpu,omitempty"`
	// Memory is the total memory of running environments.
	// +optional
	Memory *resource.Quantity `json:"memory,omitempty"`
	// Disk is the total home volume size of all environments.
	// +optional
	Disk *resource.Quantity `json:"disk,omitempty"`
}

// DevUserSpec defines a console user.
type DevUserSpec struct {
	// +optional
	DisplayName string `json:"displayName,omitempty"`
	// +optional
	Email string `json:"email,omitempty"`
	// Role defaults to user.
	// +optional
	Role Role `json:"role,omitempty"`
	// Disabled users can not sign in and their tokens and keys are rejected.
	// +optional
	Disabled bool `json:"disabled,omitempty"`
	// Identities are the external logins of the user.
	// +listType=atomic
	// +optional
	Identities []Identity `json:"identities,omitempty"`
	// SSHKeys are accepted for every environment the user can access.
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=100
	// +optional
	SSHKeys []UserSSHKey `json:"sshKeys,omitempty"`
	// Limits overrides the server defaults.
	// +optional
	Limits UserLimits `json:"limits,omitempty"`
	// Preferences are console settings, stored as is.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:Type=object
	// +optional
	Preferences *runtime.RawExtension `json:"preferences,omitempty"`
}

// DevUserStatus is the observed state of a user.
type DevUserStatus struct {
	// +optional
	LastLoginAt *metav1.Time `json:"lastLoginAt,omitempty"`
	// LastLoginProvider is "password" or an OIDC provider id.
	// +optional
	LastLoginProvider string `json:"lastLoginProvider,omitempty"`
	// Groups reported by the identity provider at the last login.
	// +listType=atomic
	// +optional
	Groups []string `json:"groups,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=duser
// +kubebuilder:printcolumn:name="Email",type=string,JSONPath=`.spec.email`
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=`.spec.role`
// +kubebuilder:printcolumn:name="Disabled",type=boolean,JSONPath=`.spec.disabled`
// +kubebuilder:printcolumn:name="Last login",type=date,JSONPath=`.status.lastLoginAt`

// DevUser is a console user. The name is the user name recorded as
// environment owner.
type DevUser struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DevUserSpec   `json:"spec,omitempty"`
	Status DevUserStatus `json:"status,omitempty"`
}

// IsAdmin reports the admin role.
func (u *DevUser) IsAdmin() bool { return u.Spec.Role == RoleAdmin }

// +kubebuilder:object:root=true

// DevUserList contains a list of DevUser.
type DevUserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DevUser `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DevUser{}, &DevUserList{})
}

// AccessKeys returns the SSH keys of the users who can use env: the owner and
// the users it is shared with (directly or through a group they had at their
// last login). Disabled users are skipped.
func AccessKeys(users []DevUser, env *DevEnvironment) []string {
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

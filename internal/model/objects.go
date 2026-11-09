package model

import (
	"encoding/json"
	"time"
)

// Environment is a remote development environment.
type Environment struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	// CreatedBy is "mcp" for environments created by agents.
	CreatedBy string `json:"createdBy,omitempty"`
	// Generation increases on every spec change.
	Generation int64 `json:"generation"`
	// Deleting is set when deletion was requested; the controller removes
	// the Kubernetes objects and then the record.
	Deleting bool `json:"deleting,omitempty"`

	Spec   EnvironmentSpec   `json:"spec"`
	Status EnvironmentStatus `json:"status"`

	// Secrets. Never sent to clients.
	HostKey      []byte `json:"hostKey,omitempty"`
	HostKeyPub   []byte `json:"hostKeyPub,omitempty"`
	PasswordHash string `json:"passwordHash,omitempty"`
	// VolumeCreated records that the home volume was created once. A missing
	// volume then means it has not been restored yet: the environment waits
	// instead of starting with an empty home directory.
	VolumeCreated bool `json:"volumeCreated,omitempty"`

	// Revision is the store revision used for optimistic concurrency.
	Revision int64 `json:"-"`
}

// IsRunning reports the desired running state (default true).
func (e *Environment) IsRunning() bool { return e.Spec.Running == nil || *e.Spec.Running }

// ClassName returns the effective class name.
func (e *Environment) ClassName() string {
	if e.Spec.ClassName == "" {
		return DefaultClassName
	}
	return e.Spec.ClassName
}

// DeepCopy returns an independent copy.
func (e *Environment) DeepCopy() *Environment {
	out := deepCopy(e)
	out.Revision = e.Revision
	return out
}

// User is a console user.
type User struct {
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"createdAt"`
	Spec      UserSpec   `json:"spec"`
	Status    UserStatus `json:"status"`
	Revision  int64      `json:"-"`
}

func (u *User) IsAdmin() bool { return u.Spec.Role == RoleAdmin }
func (u *User) DeepCopy() *User {
	out := deepCopy(u)
	out.Revision = u.Revision
	return out
}

// Image is a catalog entry. Images from the configuration file are Managed
// and read-only in the console.
type Image struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	Managed   bool      `json:"-"`
	Spec      ImageSpec `json:"spec"`
	Revision  int64     `json:"-"`
}

func (i *Image) DeepCopy() *Image {
	out := deepCopy(i)
	out.Revision, out.Managed = i.Revision, i.Managed
	return out
}

// Class adapts environments to the cluster. Classes come from the
// configuration file.
type Class struct {
	Name string    `json:"name"`
	Spec ClassSpec `json:"spec"`
}

func deepCopy[T any](in *T) *T {
	if in == nil {
		return nil
	}
	b, err := json.Marshal(in)
	if err != nil {
		panic(err)
	}
	out := new(T)
	if err := json.Unmarshal(b, out); err != nil {
		panic(err)
	}
	return out
}

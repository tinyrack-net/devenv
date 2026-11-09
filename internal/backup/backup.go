// Package backup exports and imports devenv state for disaster recovery.
//
// A backup is one JSON document with everything devenv keeps outside home
// volumes: users, images, class access lists, environments and the Secrets
// that must survive (server keys, environment host keys and passwords,
// access tokens). Home volumes are backed up by the storage system (Longhorn,
// CSI snapshots, Velero); restore recreates environments stopped first and
// starts them once their volume exists.
//
// Classes are usually managed declaratively (Helm/GitOps); a backup keeps
// them so a restore into an empty cluster works, but existing classes are
// only updated in their access list.
package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
	"github.com/tinyrack-net/devenv/internal/controller"
)

// FormatVersion is bumped on incompatible changes.
const FormatVersion = 1

// Backup is the exported state.
type Backup struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	// ServerVersion of the devenv that wrote the backup.
	ServerVersion string `json:"serverVersion,omitempty"`

	Users        []devenvv1.DevUser             `json:"users"`
	Images       []devenvv1.DevImage            `json:"images"`
	Classes      []devenvv1.DevEnvironmentClass `json:"classes"`
	Environments []Environment                  `json:"environments"`
	// ServerSecrets are Secrets of the server namespace (keys, tokens).
	ServerSecrets []Secret `json:"serverSecrets"`
}

// Environment is a DevEnvironment with the Secrets it owns.
type Environment struct {
	devenvv1.DevEnvironment
	// WasRunning is the running state at backup time.
	WasRunning bool `json:"wasRunning"`
	// MAC is the multus MAC address at backup time.
	MAC     string   `json:"mac,omitempty"`
	Secrets []Secret `json:"secrets"`
}

// Secret is the restorable part of a Secret.
type Secret struct {
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Type        corev1.SecretType `json:"type,omitempty"`
	Data        map[string][]byte `json:"data"`
}

// Options locate the objects.
type Options struct {
	// EnvironmentNamespace holds environments and their Secrets.
	EnvironmentNamespace string
	// ServerNamespace holds devenv-keys and token Secrets.
	ServerNamespace string
	// KeysSecret is the name of the server key Secret.
	KeysSecret    string
	ServerVersion string
}

// clean strips server populated metadata so objects can be created again.
func clean(m *metav1.ObjectMeta) {
	*m = metav1.ObjectMeta{
		Name:        m.Name,
		Namespace:   m.Namespace,
		Labels:      m.Labels,
		Annotations: dropAnnotations(m.Annotations),
	}
}

func dropAnnotations(a map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		if k == "kubectl.kubernetes.io/last-applied-configuration" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func toSecret(s *corev1.Secret) Secret {
	return Secret{Name: s.Name, Labels: s.Labels, Annotations: dropAnnotations(s.Annotations), Type: s.Type, Data: s.Data}
}

// Export reads the current state. c should be an uncached client.
func Export(ctx context.Context, c client.Client, o Options) (*Backup, error) {
	b := &Backup{Version: FormatVersion, CreatedAt: time.Now().UTC().Truncate(time.Second), ServerVersion: o.ServerVersion}

	users := &devenvv1.DevUserList{}
	if err := c.List(ctx, users); err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	for _, u := range users.Items {
		clean(&u.ObjectMeta)
		b.Users = append(b.Users, u)
	}
	images := &devenvv1.DevImageList{}
	if err := c.List(ctx, images); err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}
	for _, i := range images.Items {
		clean(&i.ObjectMeta)
		b.Images = append(b.Images, i)
	}
	classes := &devenvv1.DevEnvironmentClassList{}
	if err := c.List(ctx, classes); err != nil {
		return nil, fmt.Errorf("list classes: %w", err)
	}
	for _, cl := range classes.Items {
		clean(&cl.ObjectMeta)
		b.Classes = append(b.Classes, cl)
	}

	secrets := &corev1.SecretList{}
	if err := c.List(ctx, secrets, client.InNamespace(o.EnvironmentNamespace), client.MatchingLabels{"app.kubernetes.io/managed-by": "devenv"}); err != nil {
		return nil, fmt.Errorf("list environment secrets: %w", err)
	}
	byEnv := map[string][]Secret{}
	for i := range secrets.Items {
		s := &secrets.Items[i]
		env := s.Labels["devenv.tinyrack.net/environment"]
		if env == "" {
			continue
		}
		byEnv[env] = append(byEnv[env], toSecret(s))
	}
	envs := &devenvv1.DevEnvironmentList{}
	if err := c.List(ctx, envs, client.InNamespace(o.EnvironmentNamespace)); err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	for _, e := range envs.Items {
		if !e.DeletionTimestamp.IsZero() {
			continue
		}
		out := Environment{WasRunning: e.IsRunning(), MAC: e.Status.StableMAC, Secrets: byEnv[e.Name]}
		if out.MAC == "" {
			out.MAC = e.Status.MAC
		}
		if out.Secrets == nil {
			out.Secrets = []Secret{}
		}
		clean(&e.ObjectMeta)
		e.Status = devenvv1.DevEnvironmentStatus{}
		out.DevEnvironment = e
		b.Environments = append(b.Environments, out)
	}

	server := &corev1.SecretList{}
	if err := c.List(ctx, server, client.InNamespace(o.ServerNamespace)); err != nil {
		return nil, fmt.Errorf("list server secrets: %w", err)
	}
	for i := range server.Items {
		s := &server.Items[i]
		if s.Name == o.KeysSecret || s.Labels["devenv.tinyrack.net/component"] == "token" {
			b.ServerSecrets = append(b.ServerSecrets, toSecret(s))
		}
	}
	sort.Slice(b.Environments, func(i, j int) bool { return b.Environments[i].Name < b.Environments[j].Name })
	for _, l := range []*[]Secret{&b.ServerSecrets} {
		if *l == nil {
			*l = []Secret{}
		}
	}
	return b, nil
}

// Marshal encodes a backup.
func (b *Backup) Marshal() ([]byte, error) { return json.MarshalIndent(b, "", "  ") }

// Unmarshal decodes a backup and checks its version.
func Unmarshal(data []byte) (*Backup, error) {
	b := &Backup{}
	if err := json.Unmarshal(data, b); err != nil {
		return nil, err
	}
	if b.Version != FormatVersion {
		return nil, fmt.Errorf("unsupported backup version %d (want %d)", b.Version, FormatVersion)
	}
	return b, nil
}

// RestoreOptions control a restore.
type RestoreOptions struct {
	Options
	// Overwrite replaces objects that already exist. Without it existing
	// objects are kept (a restore into a partially recovered cluster).
	Overwrite bool
	// Start starts environments that were running at backup time once their
	// home volume exists.
	Start bool
	// VolumeTimeout bounds the wait for home volumes (restored by the storage
	// system) before an environment is started; 0 starts immediately.
	VolumeTimeout time.Duration
	// Log receives progress lines.
	Log func(format string, a ...any)
}

// Result summarizes a restore.
type Result struct {
	Created, Updated, Kept int
	Started                []string
	Waiting                []string
}

func (o *RestoreOptions) logf(format string, a ...any) {
	if o.Log != nil {
		o.Log(format, a...)
	}
}

// Restore recreates the state. Order: server Secrets, users, images and
// classes, environment Secrets, environments (stopped), then start the ones
// that were running when their volume is there.
func Restore(ctx context.Context, c client.Client, b *Backup, o RestoreOptions) (*Result, error) {
	r := &Result{}
	apply := func(obj client.Object, mutate func(existing client.Object)) error {
		key := client.ObjectKeyFromObject(obj)
		existing := obj.DeepCopyObject().(client.Object)
		err := c.Get(ctx, key, existing)
		switch {
		case apierrors.IsNotFound(err):
			if err := c.Create(ctx, obj); err != nil {
				return fmt.Errorf("create %s: %w", key, err)
			}
			r.Created++
			return nil
		case err != nil:
			return err
		case !o.Overwrite:
			if mutate != nil {
				before := existing.DeepCopyObject().(client.Object)
				mutate(existing)
				if err := c.Patch(ctx, existing, client.MergeFrom(before)); err != nil {
					return fmt.Errorf("update %s: %w", key, err)
				}
				r.Updated++
				return nil
			}
			r.Kept++
			return nil
		}
		obj.SetResourceVersion(existing.GetResourceVersion())
		obj.SetUID(existing.GetUID())
		if err := c.Update(ctx, obj); err != nil {
			return fmt.Errorf("update %s: %w", key, err)
		}
		r.Updated++
		return nil
	}

	secret := func(ns string, s Secret) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: ns, Labels: s.Labels, Annotations: s.Annotations}, Type: s.Type, Data: s.Data}
	}
	for _, s := range b.ServerSecrets {
		if err := apply(secret(o.ServerNamespace, s), nil); err != nil {
			return r, err
		}
	}
	o.logf("server secrets: %d", len(b.ServerSecrets))
	for i := range b.Users {
		u := b.Users[i].DeepCopy()
		status := u.Status
		if err := apply(u, nil); err != nil {
			return r, err
		}
		// Status (groups, last login) is a subresource.
		cur := &devenvv1.DevUser{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(u), cur); err == nil && cur.Status.LastLoginAt == nil {
			cur.Status = status
			_ = c.Status().Update(ctx, cur)
		}
	}
	o.logf("users: %d", len(b.Users))
	for i := range b.Images {
		if err := apply(b.Images[i].DeepCopy(), nil); err != nil {
			return r, err
		}
	}
	for i := range b.Classes {
		cl := b.Classes[i].DeepCopy()
		// Declaratively managed classes keep their spec; only the access
		// list (edited in the console) comes from the backup.
		if err := apply(cl, func(existing client.Object) {
			existing.(*devenvv1.DevEnvironmentClass).Spec.Access = cl.Spec.Access
		}); err != nil {
			return r, err
		}
	}
	o.logf("images: %d, classes: %d", len(b.Images), len(b.Classes))

	for _, e := range b.Environments {
		for _, s := range e.Secrets {
			if err := apply(secret(o.EnvironmentNamespace, s), nil); err != nil {
				return r, err
			}
		}
		env := e.DevEnvironment.DeepCopy()
		env.Namespace = o.EnvironmentNamespace
		env.Spec.Running = ptr.To(false)
		if env.Annotations == nil {
			env.Annotations = map[string]string{}
		}
		env.Annotations[controller.AnnotationAwaitVolume] = "true"
		if e.MAC != "" {
			if env.Annotations == nil {
				env.Annotations = map[string]string{}
			}
			env.Annotations[controller.AnnotationStableMAC] = e.MAC
		}
		if err := apply(env, nil); err != nil {
			return r, err
		}
	}
	o.logf("environments: %d (created stopped, waiting for their home volumes)", len(b.Environments))

	if !o.Start {
		return r, nil
	}
	deadline := time.Now().Add(o.VolumeTimeout)
	pending := map[string]bool{}
	for _, e := range b.Environments {
		if e.WasRunning {
			pending[e.Name] = true
		}
	}
	for len(pending) > 0 {
		for name := range pending {
			ready, err := volumeReady(ctx, c, o.EnvironmentNamespace, name)
			if err != nil {
				return r, err
			}
			if !ready && time.Now().Before(deadline) {
				continue
			}
			if !ready {
				o.logf("%s: home volume not restored yet; left stopped (it waits for the volume; remove annotation %s to start empty)", name, controller.AnnotationAwaitVolume)
				r.Waiting = append(r.Waiting, name)
				delete(pending, name)
				continue
			}
			env := &devenvv1.DevEnvironment{}
			if err := c.Get(ctx, client.ObjectKey{Namespace: o.EnvironmentNamespace, Name: name}, env); err != nil {
				return r, err
			}
			before := env.DeepCopy()
			env.Spec.Running = ptr.To(true)
			if err := c.Patch(ctx, env, client.MergeFrom(before)); err != nil {
				return r, err
			}
			o.logf("%s: started", name)
			r.Started = append(r.Started, name)
			delete(pending, name)
		}
		if len(pending) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return r, ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	sort.Strings(r.Started)
	sort.Strings(r.Waiting)
	return r, nil
}

// volumeReady reports whether an environment can start: ephemeral classes
// need no volume; persistent ones need their PVC to exist (restored by the
// storage system) and be bound.
func volumeReady(ctx context.Context, c client.Client, ns, name string) (bool, error) {
	env := &devenvv1.DevEnvironment{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, env); err != nil {
		return false, err
	}
	cl := &devenvv1.DevEnvironmentClass{}
	if err := c.Get(ctx, client.ObjectKey{Name: env.ClassName()}, cl); err == nil && cl.Spec.EffectiveStorageMode() == devenvv1.StorageModeEphemeral {
		return true, nil
	}
	pvc := &corev1.PersistentVolumeClaim{}
	err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name + "-home"}, pvc)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return pvc.Status.Phase == corev1.ClaimBound || pvc.Spec.VolumeName != "", nil
}

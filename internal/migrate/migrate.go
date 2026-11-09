// Package migrate imports the state of devenv 0.10 and earlier (CRDs and
// Secrets) into an empty database. It reads the old objects as unstructured
// data, so the CRD types are not needed anymore.
//
// Imported: server keys and the legacy MCP token (keys Secret), access
// tokens (devenv-token-*), DevUsers, DevImages that are not managed by Helm
// (console created, personal), DevEnvironments with their host keys,
// password hashes, pinned MACs. Owner references from the old CRD objects
// are removed from Pods, Services, PVCs and Secrets so deleting the CRDs
// does not garbage collect them.
package migrate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tinyrack-net/devenv/internal/controller"
	"github.com/tinyrack-net/devenv/internal/keys"
	"github.com/tinyrack-net/devenv/internal/model"
	"github.com/tinyrack-net/devenv/internal/store"
)

// +kubebuilder:rbac:groups=devenv.tinyrack.net,resources=devenvironments;devusers;devimages;devenvironmentclasses,verbs=get;list

// Marker in the kv table once the import ran.
const Marker = "migration/crds"

var group = "devenv.tinyrack.net"

func gvk(kind string) schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: group, Version: "v1alpha1", Kind: kind + "List"}
}

// Options of the import.
type Options struct {
	EnvironmentNamespace string
	ServerNamespace      string
	KeysSecret           string
	Log                  logr.Logger
}

// Result counts imported records.
type Result struct {
	Users, Images, Environments, Tokens int
	Keys                                bool
	Skipped                             bool
}

// Run imports the old state once. c must be an uncached client.
func Run(ctx context.Context, c client.Client, db *store.Store, o Options) (*Result, error) {
	res := &Result{}
	if _, err := db.GetValue(ctx, Marker); err == nil {
		res.Skipped = true
		return res, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	log := o.Log

	// Server keys first: they must be in place before keys.Ensure generates
	// new ones.
	sec := &corev1.Secret{}
	err := c.Get(ctx, client.ObjectKey{Namespace: o.ServerNamespace, Name: o.KeysSecret}, sec)
	switch {
	case err == nil:
		for k, key := range map[string]string{
			"session-key": keys.KeySession, "gateway-host-key": keys.KeyHost, "client-key": keys.KeyClient, "mcp-token": keys.KeyMCPToken,
		} {
			v := sec.Data[k]
			if len(v) == 0 {
				continue
			}
			val := string(v)
			if k == "session-key" {
				val = base64.StdEncoding.EncodeToString(v)
			}
			if _, err := db.SetValueIfAbsent(ctx, key, strings.TrimSpace(val)); err != nil {
				return nil, err
			}
		}
		res.Keys = true
	case apierrors.IsNotFound(err):
	default:
		return nil, fmt.Errorf("read %s: %w", o.KeysSecret, err)
	}

	// Access tokens.
	toks := &corev1.SecretList{}
	if err := c.List(ctx, toks, client.InNamespace(o.ServerNamespace), client.MatchingLabels{"devenv.tinyrack.net/component": "token"}); err != nil {
		return nil, err
	}
	for _, s := range toks.Items {
		a := s.Annotations
		rec := &store.TokenRecord{
			ID:        strings.TrimPrefix(s.Name, "devenv-token-"),
			Name:      a["devenv.tinyrack.net/name"],
			User:      a["devenv.tinyrack.net/user"],
			Hash:      string(s.Data["hash"]),
			CreatedAt: s.CreationTimestamp.UTC(),
		}
		if v := a["devenv.tinyrack.net/scopes"]; v != "" {
			rec.Scopes = strings.Split(v, ",")
		}
		if t, err := time.Parse(time.RFC3339, a["devenv.tinyrack.net/expires-at"]); err == nil {
			rec.ExpiresAt = &t
		}
		if t, err := time.Parse(time.RFC3339, a["devenv.tinyrack.net/last-used-at"]); err == nil {
			rec.LastUsedAt = &t
		}
		if err := db.CreateToken(ctx, rec); err != nil && !errors.Is(err, store.ErrExists) {
			return nil, err
		}
		res.Tokens++
	}

	// Users.
	users, err := list(ctx, c, "DevUser", "")
	if err != nil {
		return nil, err
	}
	for _, u := range users {
		m := &model.User{Name: u.GetName(), CreatedAt: u.GetCreationTimestamp().UTC()}
		if err := convert(u.Object["spec"], &m.Spec); err != nil {
			return nil, fmt.Errorf("user %s: %w", u.GetName(), err)
		}
		if err := convert(u.Object["status"], &m.Status); err != nil {
			return nil, fmt.Errorf("user %s: %w", u.GetName(), err)
		}
		if err := db.CreateUser(ctx, m); err != nil && !errors.Is(err, store.ErrExists) {
			return nil, err
		}
		res.Users++
	}

	// Console created images. Helm managed ones come from the
	// configuration now.
	images, err := list(ctx, c, "DevImage", "")
	if err != nil {
		return nil, err
	}
	for _, i := range images {
		if i.GetLabels()["app.kubernetes.io/managed-by"] == "Helm" {
			continue
		}
		m := &model.Image{Name: i.GetName(), CreatedAt: i.GetCreationTimestamp().UTC()}
		if err := convert(i.Object["spec"], &m.Spec); err != nil {
			return nil, fmt.Errorf("image %s: %w", i.GetName(), err)
		}
		if err := db.CreateImage(ctx, m); err != nil && !errors.Is(err, store.ErrExists) {
			return nil, err
		}
		res.Images++
	}

	// Environments.
	envs, err := list(ctx, c, "DevEnvironment", o.EnvironmentNamespace)
	if err != nil {
		return nil, err
	}
	for _, e := range envs {
		if e.GetDeletionTimestamp() != nil {
			continue
		}
		name := e.GetName()
		m := &model.Environment{Name: name, CreatedAt: e.GetCreationTimestamp().UTC(), Generation: 1}
		if e.GetLabels()["devenv.tinyrack.net/created-by"] == "mcp" {
			m.CreatedBy = "mcp"
		}
		var spec struct {
			model.EnvironmentSpec
			PasswordSecretName string `json:"passwordSecretName,omitempty"`
		}
		if err := convert(e.Object["spec"], &spec); err != nil {
			return nil, fmt.Errorf("environment %s: %w", name, err)
		}
		m.Spec = spec.EnvironmentSpec
		var st model.EnvironmentStatus
		_ = convert(e.Object["status"], &st)
		// Keep the address: the pinned MAC, or the MAC the pod has now
		// (derived from the old UID).
		m.Status.StableMAC = st.StableMAC
		if m.Status.StableMAC == "" && st.NetworkMode == model.NetworkModeMultus {
			m.Status.StableMAC = st.MAC
		}
		if mac := e.GetAnnotations()["devenv.tinyrack.net/stable-mac"]; mac != "" && m.Status.StableMAC == "" {
			m.Status.StableMAC = mac
		}
		m.Status.Phase = st.Phase

		ssh := &corev1.Secret{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: o.EnvironmentNamespace, Name: name + "-ssh"}, ssh); err == nil {
			m.HostKey = ssh.Data[controller.HostKeyFile]
			m.HostKeyPub = ssh.Data[controller.HostPublicKeyFile]
			m.PasswordHash = string(ssh.Data[controller.PasswordHashFile])
		} else if !apierrors.IsNotFound(err) {
			return nil, err
		}
		if m.PasswordHash == "" && spec.PasswordSecretName != "" {
			pw := &corev1.Secret{}
			if err := c.Get(ctx, client.ObjectKey{Namespace: o.EnvironmentNamespace, Name: spec.PasswordSecretName}, pw); err == nil {
				if h := strings.TrimSpace(string(pw.Data["passwordHash"])); controller.IsBcrypt(h) {
					m.PasswordHash = h
				} else if p := string(pw.Data["password"]); p != "" {
					if h, err := controller.HashPassword(p); err == nil {
						m.PasswordHash = h
					}
				}
			}
		}
		pvc := &corev1.PersistentVolumeClaim{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: o.EnvironmentNamespace, Name: name + "-home"}, pvc); err == nil {
			m.VolumeCreated = true
		}
		if err := db.CreateEnvironment(ctx, m); err != nil && !errors.Is(err, store.ErrExists) {
			return nil, err
		}
		res.Environments++
		if err := release(ctx, c, o.EnvironmentNamespace, name, spec.PasswordSecretName); err != nil {
			return nil, fmt.Errorf("release objects of %s: %w", name, err)
		}
	}

	if err := db.SetValue(ctx, Marker, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return nil, err
	}
	log.Info("imported state of the previous version", "users", res.Users, "images", res.Images, "environments", res.Environments, "tokens", res.Tokens, "keys", res.Keys)
	return res, nil
}

// list returns all objects of a kind, or nothing when the CRD is not
// installed.
func list(ctx context.Context, c client.Client, kind, ns string) ([]unstructured.Unstructured, error) {
	l := &unstructured.UnstructuredList{}
	l.SetGroupVersionKind(gvk(kind))
	var opts []client.ListOption
	if ns != "" {
		opts = append(opts, client.InNamespace(ns))
	}
	if err := c.List(ctx, l, opts...); err != nil {
		if meta.IsNoMatchError(err) || apierrors.IsNotFound(err) || strings.Contains(err.Error(), "no matches for kind") {
			return nil, nil
		}
		return nil, fmt.Errorf("list %s: %w", kind, err)
	}
	return l.Items, nil
}

func convert(in any, out any) error {
	if in == nil {
		return nil
	}
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// release removes DevEnvironment owner references from an environment's
// objects so deleting the CRD does not delete them.
func release(ctx context.Context, c client.Client, ns, name, passwordSecret string) error {
	objs := []client.Object{
		&corev1.Pod{}, &corev1.Service{}, &corev1.Service{}, &corev1.Secret{}, &corev1.PersistentVolumeClaim{}, &corev1.Secret{},
	}
	names := []string{name, name, name + "-lb", name + "-ssh", name + "-home", passwordSecret}
	for i, o := range objs {
		if names[i] == "" {
			continue
		}
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: names[i]}, o); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return err
		}
		refs := o.GetOwnerReferences()
		var keep []metav1.OwnerReference
		changed := false
		for _, r := range refs {
			if r.Kind == "DevEnvironment" {
				changed = true
				continue
			}
			keep = append(keep, r)
		}
		if !changed {
			continue
		}
		patch := client.MergeFrom(o.DeepCopyObject().(client.Object))
		o.SetOwnerReferences(keep)
		if err := c.Patch(ctx, o, patch); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

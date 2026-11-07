package users

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
)

// LegacyMCPOwner was recorded on environments created through MCP before
// per-user tokens.
const LegacyMCPOwner = "mcp"

// Migrate brings data from single-user versions into the multi-user model.
// It is idempotent and runs at every start. c must be an uncached client.
//
//   - creates the local admin DevUser
//   - moves SSH keys from devenv-user-<hash> ConfigMaps to DevUsers
//   - assigns environments owned by "mcp", nobody or an unknown user to the
//     legacy MCP token user (default: the local admin)
//   - labels environments with their owner
func (s *Store) Migrate(ctx context.Context, c client.Client, namespace string) error {
	log := logf.FromContext(ctx).WithName("migrate")
	admin := s.Config.Auth.Password.Username
	adminUser, err := s.EnsureLocalAdmin(ctx)
	if err != nil {
		return fmt.Errorf("local admin: %w", err)
	}
	_ = adminUser

	// SSH keys.
	cms := &corev1.ConfigMapList{}
	if err := c.List(ctx, cms, client.InNamespace(namespace), client.MatchingLabels{"devenv.tinyrack.net/component": "user"}); err != nil {
		return err
	}
	for i := range cms.Items {
		cm := &cms.Items[i]
		owner := cm.Annotations["devenv.tinyrack.net/user"]
		if owner == "" {
			continue
		}
		var keys []struct {
			Name      string    `json:"name"`
			PublicKey string    `json:"publicKey"`
			CreatedAt time.Time `json:"createdAt"`
		}
		if err := json.Unmarshal([]byte(cm.Data["keys.json"]), &keys); err != nil {
			log.Info("skip unreadable ssh key ConfigMap", "name", cm.Name, "error", err.Error())
			continue
		}
		u := &devenvv1.DevUser{}
		if err := c.Get(ctx, client.ObjectKey{Name: owner}, u); err != nil {
			log.Info("skip ssh keys of unknown user", "user", owner)
			continue
		}
		before := u.DeepCopy()
		have := map[string]bool{}
		for _, k := range u.Spec.SSHKeys {
			have[k.PublicKey] = true
		}
		for _, k := range keys {
			if have[k.PublicKey] {
				continue
			}
			t := metav1.NewTime(k.CreatedAt)
			u.Spec.SSHKeys = append(u.Spec.SSHKeys, devenvv1.UserSSHKey{Name: k.Name, PublicKey: k.PublicKey, CreatedAt: &t})
		}
		if len(u.Spec.SSHKeys) != len(before.Spec.SSHKeys) {
			if err := c.Patch(ctx, u, client.MergeFrom(before)); err != nil {
				return err
			}
			log.Info("moved ssh keys", "user", owner, "keys", len(u.Spec.SSHKeys)-len(before.Spec.SSHKeys))
		}
		if err := c.Delete(ctx, cm); client.IgnoreNotFound(err) != nil {
			return err
		}
	}

	// Environment owners.
	users, err := s.List(ctx)
	if err != nil {
		return err
	}
	known := map[string]bool{admin: true}
	for _, u := range users {
		known[u.Name] = true
	}
	fallback := s.Config.MCP.LegacyToken.User
	if !known[fallback] {
		fallback = admin
	}
	envs := &devenvv1.DevEnvironmentList{}
	if err := c.List(ctx, envs, client.InNamespace(namespace)); err != nil {
		return err
	}
	for i := range envs.Items {
		e := &envs.Items[i]
		before := e.DeepCopy()
		if !known[e.Spec.Owner] {
			if e.Spec.Owner != "" && e.Spec.Owner != LegacyMCPOwner {
				log.Info("environment owner does not exist; assigning", "environment", e.Name, "owner", e.Spec.Owner, "to", fallback)
			}
			e.Spec.Owner = fallback
		}
		if e.Labels[devenvv1.LabelOwner] != e.Spec.Owner {
			if e.Labels == nil {
				e.Labels = map[string]string{}
			}
			e.Labels[devenvv1.LabelOwner] = e.Spec.Owner
		}
		if e.Spec.Owner != before.Spec.Owner || e.Labels[devenvv1.LabelOwner] != before.Labels[devenvv1.LabelOwner] {
			if err := c.Patch(ctx, e, client.MergeFrom(before)); err != nil {
				return fmt.Errorf("environment %s: %w", e.Name, err)
			}
		}
	}
	return nil
}

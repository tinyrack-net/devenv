package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tinyrack-net/devenv/internal/auth"
)

// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete

const sshKeysDataKey = "keys.json"

// userConfigMapName returns the ConfigMap that stores a user's public keys.
// Public keys are not secret, so a ConfigMap is sufficient.
func userConfigMapName(user string) string {
	sum := sha256.Sum256([]byte(user))
	return "devenv-user-" + hex.EncodeToString(sum[:6])
}

func parseKey(raw string) (ssh.PublicKey, string, error) {
	pk, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(raw)))
	if err != nil {
		return nil, "", badRequest("invalid public key: " + err.Error())
	}
	return pk, comment, nil
}

// normalizeKeys validates and canonicalizes authorized_keys lines.
func normalizeKeys(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		pk, comment, err := parseKey(raw)
		if err != nil {
			return nil, err
		}
		line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk)))
		if comment != "" {
			line += " " + comment
		}
		fp := ssh.FingerprintSHA256(pk)
		if !seen[fp] {
			seen[fp] = true
			out = append(out, line)
		}
	}
	return out, nil
}

func (s *Server) loadKeys(ctx context.Context, user string) ([]SSHKey, error) {
	cm := &corev1.ConfigMap{}
	err := s.Client.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: userConfigMapName(user)}, cm)
	if apierrors.IsNotFound(err) {
		return []SSHKey{}, nil
	}
	if err != nil {
		return nil, err
	}
	keys := []SSHKey{}
	if raw := cm.Data[sshKeysDataKey]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &keys); err != nil {
			return nil, fmt.Errorf("decode ssh keys: %w", err)
		}
	}
	return keys, nil
}

func (s *Server) saveKeys(ctx context.Context, user string, keys []SSHKey) error {
	b, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{Namespace: s.Namespace, Name: userConfigMapName(user)}
	err = s.Client.Get(ctx, key, cm)
	if apierrors.IsNotFound(err) {
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: key.Name, Namespace: key.Namespace,
				Labels:      map[string]string{"app.kubernetes.io/managed-by": "devenv", "devenv.tinyrack.net/component": "user"},
				Annotations: map[string]string{"devenv.tinyrack.net/user": user},
			},
			Data: map[string]string{sshKeysDataKey: string(b)},
		}
		return s.Client.Create(ctx, cm)
	}
	if err != nil {
		return err
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[sshKeysDataKey] = string(b)
	return s.Client.Update(ctx, cm)
}

func (s *Server) listSSHKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.loadKeys(r.Context(), auth.UserFrom(r.Context()).Name)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

func (s *Server) createSSHKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name      string `json:"name"`
		PublicKey string `json:"publicKey"`
	}
	if !decode(w, r, &in) {
		return
	}
	pk, comment, err := parseKey(in.PublicKey)
	if err != nil {
		fail(w, r, err)
		return
	}
	user := auth.UserFrom(r.Context()).Name
	keys, err := s.loadKeys(r.Context(), user)
	if err != nil {
		fail(w, r, err)
		return
	}
	fp := ssh.FingerprintSHA256(pk)
	for _, k := range keys {
		if k.Fingerprint == fp {
			fail(w, r, &httpError{http.StatusConflict, "key already exists"})
			return
		}
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = comment
	}
	if name == "" {
		name = pk.Type()
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk)))
	if comment != "" {
		line += " " + comment
	}
	sum := sha256.Sum256([]byte(fp))
	k := SSHKey{
		ID:          hex.EncodeToString(sum[:6]),
		Name:        name,
		PublicKey:   line,
		Fingerprint: fp,
		CreatedAt:   time.Now().UTC().Truncate(time.Second),
	}
	keys = append(keys, k)
	if err := s.saveKeys(r.Context(), user, keys); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, k)
}

func (s *Server) deleteSSHKey(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFrom(r.Context()).Name
	keys, err := s.loadKeys(r.Context(), user)
	if err != nil {
		fail(w, r, err)
		return
	}
	id := r.PathValue("id")
	out := keys[:0]
	found := false
	for _, k := range keys {
		if k.ID == id {
			found = true
			continue
		}
		out = append(out, k)
	}
	if !found {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err := s.saveKeys(r.Context(), user, out); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

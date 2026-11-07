package api

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
)

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

func keyID(fp string) string {
	sum := sha256.Sum256([]byte(fp))
	return hex.EncodeToString(sum[:6])
}

func toSSHKeys(in []devenvv1.UserSSHKey) []SSHKey {
	out := make([]SSHKey, 0, len(in))
	for _, k := range in {
		pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k.PublicKey))
		if err != nil {
			continue
		}
		fp := ssh.FingerprintSHA256(pk)
		sk := SSHKey{ID: keyID(fp), Name: k.Name, PublicKey: k.PublicKey, Fingerprint: fp}
		if k.CreatedAt != nil {
			sk.CreatedAt = k.CreatedAt.Time
		}
		out = append(out, sk)
	}
	return out
}

func (s *Server) listSSHKeys(w http.ResponseWriter, r *http.Request) {
	u, err := s.Users.Get(r.Context(), principal(r).Name)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSSHKeys(u.Spec.SSHKeys))
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
	u, err := s.Users.Get(r.Context(), principal(r).Name)
	if err != nil {
		fail(w, r, err)
		return
	}
	fp := ssh.FingerprintSHA256(pk)
	for _, k := range toSSHKeys(u.Spec.SSHKeys) {
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
	if len(name) > 100 {
		name = name[:100]
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk)))
	if comment != "" {
		line += " " + comment
	}
	before := u.DeepCopy()
	now := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	u.Spec.SSHKeys = append(u.Spec.SSHKeys, devenvv1.UserSSHKey{Name: name, PublicKey: line, CreatedAt: &now})
	if err := s.Client.Patch(r.Context(), u, client.MergeFrom(before)); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, SSHKey{ID: keyID(fp), Name: name, PublicKey: line, Fingerprint: fp, CreatedAt: now.Time})
}

func (s *Server) deleteSSHKey(w http.ResponseWriter, r *http.Request) {
	u, err := s.Users.Get(r.Context(), principal(r).Name)
	if err != nil {
		fail(w, r, err)
		return
	}
	id := r.PathValue("id")
	before := u.DeepCopy()
	out := []devenvv1.UserSSHKey{}
	found := false
	for _, k := range u.Spec.SSHKeys {
		if pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k.PublicKey)); err == nil && keyID(ssh.FingerprintSHA256(pk)) == id {
			found = true
			continue
		}
		out = append(out, k)
	}
	if !found {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	u.Spec.SSHKeys = out
	if err := s.Client.Patch(r.Context(), u, client.MergeFrom(before)); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

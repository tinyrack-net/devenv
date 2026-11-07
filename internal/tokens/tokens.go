// Package tokens implements personal access tokens for MCP and the API.
//
// A token is "dvn_<id>_<secret>". The server stores only a SHA-256 hash in a
// Secret named devenv-token-<id> in the server namespace, together with the
// owner, scopes and expiry. The plain token is shown once at creation.
package tokens

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

const (
	Prefix = "dvn_"

	ScopeMCP = "mcp"
	ScopeAPI = "api"

	LabelComponent = "devenv.tinyrack.net/component"
	LabelUser      = "devenv.tinyrack.net/user"
	componentToken = "token"

	keyHash     = "hash"
	annName     = "devenv.tinyrack.net/name"
	annScopes   = "devenv.tinyrack.net/scopes"
	annExpires  = "devenv.tinyrack.net/expires-at"
	annLastUsed = "devenv.tinyrack.net/last-used-at"
	annUser     = "devenv.tinyrack.net/user"

	lastUsedResolution = 5 * time.Minute
)

// ErrInvalid is returned for unknown, expired or malformed tokens.
var ErrInvalid = errors.New("invalid token")

// Token is the metadata of a token.
type Token struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	User       string     `json:"user"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

// Store manages tokens. Client must be able to list Secrets in Namespace;
// reads go through the cache when it is a cached client.
type Store struct {
	Client    client.Client
	Namespace string

	mu       sync.Mutex
	lastSeen map[string]time.Time
	now      func() time.Time
}

func (s *Store) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func secretName(id string) string { return "devenv-token-" + id }

var enc = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func random(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return enc.EncodeToString(b), nil
}

func hash(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// Create issues a token and returns the metadata and the plain value.
func (s *Store) Create(ctx context.Context, user, name string, scopes []string, ttl time.Duration) (*Token, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return nil, "", fmt.Errorf("name is required (at most 100 characters)")
	}
	if len(scopes) == 0 {
		scopes = []string{ScopeMCP}
	}
	for _, sc := range scopes {
		if sc != ScopeMCP && sc != ScopeAPI {
			return nil, "", fmt.Errorf("unknown scope %q", sc)
		}
	}
	id, err := random(5) // 8 chars
	if err != nil {
		return nil, "", err
	}
	secret, err := random(20) // 32 chars
	if err != nil {
		return nil, "", err
	}
	now := s.clock().UTC().Truncate(time.Second)
	ann := map[string]string{annName: name, annScopes: strings.Join(scopes, ","), annUser: user}
	t := &Token{ID: id, Name: name, User: user, Scopes: scopes, CreatedAt: now}
	if ttl > 0 {
		exp := now.Add(ttl)
		t.ExpiresAt = &exp
		ann[annExpires] = exp.Format(time.RFC3339)
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: secretName(id), Namespace: s.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "devenv",
				LabelComponent:                 componentToken,
				LabelUser:                      user,
			},
			Annotations: ann,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{keyHash: []byte(hash(secret))},
	}
	if err := s.Client.Create(ctx, sec); err != nil {
		return nil, "", err
	}
	return t, Prefix + id + "_" + secret, nil
}

func fromSecret(sec *corev1.Secret) Token {
	a := sec.Annotations
	t := Token{
		ID:        strings.TrimPrefix(sec.Name, "devenv-token-"),
		Name:      a[annName],
		User:      a[annUser],
		CreatedAt: sec.CreationTimestamp.Time,
	}
	if a[annScopes] != "" {
		t.Scopes = strings.Split(a[annScopes], ",")
	}
	if v, err := time.Parse(time.RFC3339, a[annExpires]); err == nil {
		t.ExpiresAt = &v
	}
	if v, err := time.Parse(time.RFC3339, a[annLastUsed]); err == nil {
		t.LastUsedAt = &v
	}
	return t
}

// List returns the tokens of a user, or of everyone when user is empty.
func (s *Store) List(ctx context.Context, user string) ([]Token, error) {
	sel := client.MatchingLabels{LabelComponent: componentToken}
	if user != "" {
		sel[LabelUser] = user
	}
	list := &corev1.SecretList{}
	if err := s.Client.List(ctx, list, client.InNamespace(s.Namespace), sel); err != nil {
		return nil, err
	}
	out := make([]Token, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, fromSecret(&list.Items[i]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// Delete revokes a token. When user is not empty the token must belong to it.
func (s *Store) Delete(ctx context.Context, user, id string) error {
	sec := &corev1.Secret{}
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: secretName(id)}, sec); err != nil {
		return err
	}
	if sec.Labels[LabelComponent] != componentToken || (user != "" && sec.Labels[LabelUser] != user) {
		return apierrors.NewNotFound(corev1.Resource("tokens"), id)
	}
	return s.Client.Delete(ctx, sec)
}

// DeleteUser revokes every token of a user.
func (s *Store) DeleteUser(ctx context.Context, user string) error {
	return s.Client.DeleteAllOf(ctx, &corev1.Secret{}, client.InNamespace(s.Namespace),
		client.MatchingLabels{LabelComponent: componentToken, LabelUser: user})
}

// Verify checks a plain token and returns its metadata.
func (s *Store) Verify(ctx context.Context, plain string) (*Token, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(plain), Prefix)
	if !ok {
		return nil, ErrInvalid
	}
	id, secret, ok := strings.Cut(rest, "_")
	if !ok || len(id) != 8 || secret == "" {
		return nil, ErrInvalid
	}
	sec := &corev1.Secret{}
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: secretName(id)}, sec); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrInvalid
		}
		return nil, err
	}
	if sec.Labels[LabelComponent] != componentToken ||
		subtle.ConstantTimeCompare(sec.Data[keyHash], []byte(hash(secret))) != 1 {
		return nil, ErrInvalid
	}
	t := fromSecret(sec)
	now := s.clock()
	if t.ExpiresAt != nil && now.After(*t.ExpiresAt) {
		return nil, ErrInvalid
	}
	s.touch(ctx, sec, now)
	return &t, nil
}

// touch records the last use at most every few minutes per token.
func (s *Store) touch(ctx context.Context, sec *corev1.Secret, now time.Time) {
	s.mu.Lock()
	if s.lastSeen == nil {
		s.lastSeen = map[string]time.Time{}
	}
	if now.Sub(s.lastSeen[sec.Name]) < lastUsedResolution {
		s.mu.Unlock()
		return
	}
	s.lastSeen[sec.Name] = now
	s.mu.Unlock()
	if v, err := time.Parse(time.RFC3339, sec.Annotations[annLastUsed]); err == nil && now.Sub(v) < lastUsedResolution {
		return
	}
	patch := fmt.Appendf(nil, `{"metadata":{"annotations":{%q:%q}}}`, annLastUsed, now.UTC().Format(time.RFC3339))
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = s.Client.Patch(ctx, sec.DeepCopy(), client.RawPatch("application/merge-patch+json", patch))
	}()
}

// HasScope reports whether a token grants scope.
func (t *Token) HasScope(scope string) bool { return slices.Contains(t.Scopes, scope) }

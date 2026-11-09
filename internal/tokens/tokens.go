// Package tokens implements personal access tokens for MCP and the API.
//
// A token is "dvn_<id>_<secret>". The database stores only a SHA-256 hash
// with the owner, scopes and expiry. The plain token is shown once at
// creation.
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

	"github.com/tinyrack-net/devenv/internal/store"
)

const (
	Prefix = "dvn_"

	ScopeMCP = "mcp"
	ScopeAPI = "api"

	lastUsedResolution = 5 * time.Minute
)

// ErrInvalid is returned for unknown, expired or malformed tokens.
var ErrInvalid = errors.New("invalid token")

// ErrNotFound is returned for unknown token ids.
var ErrNotFound = store.ErrNotFound

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

// Store manages tokens.
type Store struct {
	DB *store.Store

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

var enc = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func random(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return enc.EncodeToString(b), nil
}

// Hash returns the stored form of a token secret.
func Hash(secret string) string {
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
	rec := &store.TokenRecord{ID: id, Name: name, User: user, Scopes: scopes, CreatedAt: now, Hash: Hash(secret)}
	if ttl > 0 {
		exp := now.Add(ttl)
		rec.ExpiresAt = &exp
	}
	if err := s.DB.CreateToken(ctx, rec); err != nil {
		return nil, "", err
	}
	t := fromRecord(rec)
	return &t, Prefix + id + "_" + secret, nil
}

func fromRecord(r *store.TokenRecord) Token {
	return Token{ID: r.ID, Name: r.Name, User: r.User, Scopes: r.Scopes, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, LastUsedAt: r.LastUsedAt}
}

// List returns the tokens of a user, or of everyone when user is empty.
func (s *Store) List(ctx context.Context, user string) ([]Token, error) {
	recs, err := s.DB.ListTokens(ctx, user)
	if err != nil {
		return nil, err
	}
	out := make([]Token, 0, len(recs))
	for i := range recs {
		out = append(out, fromRecord(&recs[i]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// Delete revokes a token. When user is not empty the token must belong to it.
func (s *Store) Delete(ctx context.Context, user, id string) error {
	rec, err := s.DB.GetToken(ctx, id)
	if err != nil {
		return err
	}
	if user != "" && rec.User != user {
		return ErrNotFound
	}
	return s.DB.DeleteToken(ctx, id)
}

// DeleteUser revokes every token of a user.
func (s *Store) DeleteUser(ctx context.Context, user string) error {
	return s.DB.DeleteUserTokens(ctx, user)
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
	rec, err := s.DB.GetToken(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrInvalid
		}
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(rec.Hash), []byte(Hash(secret))) != 1 {
		return nil, ErrInvalid
	}
	now := s.clock()
	if rec.ExpiresAt != nil && now.After(*rec.ExpiresAt) {
		return nil, ErrInvalid
	}
	s.touch(ctx, rec, now)
	t := fromRecord(rec)
	return &t, nil
}

// touch records the last use at most every few minutes per token.
func (s *Store) touch(ctx context.Context, rec *store.TokenRecord, now time.Time) {
	s.mu.Lock()
	if s.lastSeen == nil {
		s.lastSeen = map[string]time.Time{}
	}
	if now.Sub(s.lastSeen[rec.ID]) < lastUsedResolution {
		s.mu.Unlock()
		return
	}
	s.lastSeen[rec.ID] = now
	s.mu.Unlock()
	if rec.LastUsedAt != nil && now.Sub(*rec.LastUsedAt) < lastUsedResolution {
		return
	}
	upd := *rec
	t := now.UTC().Truncate(time.Second)
	upd.LastUsedAt = &t
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = s.DB.UpdateToken(ctx, &upd)
	}()
}

// HasScope reports whether a token grants scope.
func (t *Token) HasScope(scope string) bool { return slices.Contains(t.Scopes, scope) }

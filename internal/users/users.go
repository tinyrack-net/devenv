// Package users manages users: lookup, creation on OIDC login, admin
// resolution and consumption limits.
package users

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"

	"github.com/tinyrack-net/devenv/internal/auth"
	"github.com/tinyrack-net/devenv/internal/config"
	"github.com/tinyrack-net/devenv/internal/model"
	"github.com/tinyrack-net/devenv/internal/store"
)

// ErrDisabled is returned for disabled users.
var ErrDisabled = errors.New("user is disabled")

// ErrNotAllowed is returned when a login does not pass the provider rules.
var ErrNotAllowed = errors.New("not allowed to sign in")

// Store reads and writes users.
type Store struct {
	DB     *store.Store
	Config *config.Config
}

// Get returns a user by name.
func (s *Store) Get(ctx context.Context, name string) (*model.User, error) {
	return s.DB.GetUser(ctx, name)
}

// List returns all users.
func (s *Store) List(ctx context.Context) ([]model.User, error) {
	return s.DB.ListUsers(ctx)
}

// IsAdmin resolves the admin role from the user, the configured admin list
// and the provider's admin groups.
func (s *Store) IsAdmin(u *model.User) bool {
	if u.IsAdmin() || s.Config.IsConfiguredAdmin(u.Name, u.Spec.Email) {
		return true
	}
	if p := s.Config.Provider(u.Status.LastLoginProvider); p != nil {
		for _, g := range p.AdminGroups {
			for _, h := range u.Status.Groups {
				if g == h {
					return true
				}
			}
		}
	}
	return false
}

// Principal builds the request principal of a user. Disabled users are rejected.
func (s *Store) Principal(u *model.User, method string) (*auth.Principal, error) {
	if u.Spec.Disabled {
		return nil, ErrDisabled
	}
	return &auth.Principal{
		Name:        u.Name,
		DisplayName: u.Spec.DisplayName,
		Email:       u.Spec.Email,
		Admin:       s.IsAdmin(u),
		Groups:      append([]string(nil), u.Status.Groups...),
		Method:      method,
		Provider:    u.Status.LastLoginProvider,
	}, nil
}

// Load returns the principal for a user name.
func (s *Store) Load(ctx context.Context, name, method string) (*auth.Principal, error) {
	u, err := s.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	return s.Principal(u, method)
}

// EnsureLocalAdmin creates the local password user with the admin role.
func (s *Store) EnsureLocalAdmin(ctx context.Context) (*model.User, error) {
	name := s.Config.Auth.Password.Username
	u, err := s.Get(ctx, name)
	if err == nil {
		if u.Spec.Role != model.RoleAdmin {
			return s.DB.MutateUser(ctx, name, func(u *model.User) error {
				u.Spec.Role = model.RoleAdmin
				return nil
			})
		}
		return u, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	u = &model.User{Name: name, Spec: model.UserSpec{DisplayName: "Administrator", Role: model.RoleAdmin}}
	if err := s.DB.CreateUser(ctx, u); err != nil {
		if errors.Is(err, store.ErrExists) {
			return s.Get(ctx, name)
		}
		return nil, err
	}
	return u, nil
}

// RecordLogin updates the login status (best effort for the caller).
func (s *Store) RecordLogin(ctx context.Context, u *model.User, provider string, groups []string) error {
	now := time.Now().UTC().Truncate(time.Second)
	got, err := s.DB.MutateUser(ctx, u.Name, func(x *model.User) error {
		x.Status.LastLoginAt = &now
		x.Status.LastLoginProvider = provider
		if groups != nil || provider == "password" {
			x.Status.Groups = groups
		}
		return nil
	})
	if err != nil {
		return err
	}
	*u = *got
	return nil
}

// Claims are the identity attributes of an OIDC login.
type Claims struct {
	Subject       string
	Username      string
	Email         string
	EmailVerified *bool
	Name          string
	Groups        []string
}

// LoginOIDC finds (or creates) the user of an OIDC login and records it.
func (s *Store) LoginOIDC(ctx context.Context, p *config.OIDCProvider, c Claims) (*model.User, error) {
	if err := allowed(p, c); err != nil {
		return nil, err
	}
	users, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	var u *model.User
	for i := range users {
		for _, id := range users[i].Spec.Identities {
			if id.Provider == p.ID && id.Subject == c.Subject {
				u = &users[i]
			}
		}
	}
	if u == nil {
		// An admin may have created the user in advance; link it by e-mail
		// when the provider verified the address.
		if c.Email != "" && (c.EmailVerified == nil || *c.EmailVerified) {
			for i := range users {
				if strings.EqualFold(users[i].Spec.Email, c.Email) && len(users[i].Spec.Identities) == 0 && users[i].Name != s.Config.Auth.Password.Username {
					u = &users[i]
					u.Spec.Identities = append(u.Spec.Identities, model.Identity{Provider: p.ID, Subject: c.Subject})
					if err := s.DB.UpdateUser(ctx, u); err != nil {
						return nil, err
					}
					break
				}
			}
		}
	}
	if u == nil {
		if !p.IsAutoCreate() {
			return nil, fmt.Errorf("%w: no devenv account for this login; ask an administrator", ErrNotAllowed)
		}
		if u, err = s.create(ctx, users, p, c); err != nil {
			return nil, err
		}
	} else {
		// Keep profile fields in sync with the provider.
		before := u.DeepCopy()
		if c.Email != "" {
			u.Spec.Email = c.Email
		}
		if c.Name != "" {
			u.Spec.DisplayName = c.Name
		}
		if u.Spec.Email != before.Spec.Email || u.Spec.DisplayName != before.Spec.DisplayName {
			if err := s.DB.UpdateUser(ctx, u); err != nil {
				return nil, err
			}
		}
	}
	if u.Spec.Disabled {
		return nil, ErrDisabled
	}
	groups := c.Groups
	if groups == nil {
		groups = []string{}
	}
	if err := s.RecordLogin(ctx, u, p.ID, groups); err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Store) create(ctx context.Context, existing []model.User, p *config.OIDCProvider, c Claims) (*model.User, error) {
	taken := map[string]bool{s.Config.Auth.Password.Username: true}
	for _, u := range existing {
		taken[u.Name] = true
	}
	base := UserName(c.Username, c.Email, c.Subject)
	for i := 0; i < 50; i++ {
		name := base
		if i > 0 {
			suffix := fmt.Sprintf("-%d", i+1)
			name = strings.TrimRight(truncate(base, 40-len(suffix)), "-") + suffix
		}
		if taken[name] {
			continue
		}
		u := &model.User{
			Name: name,
			Spec: model.UserSpec{
				DisplayName: c.Name,
				Email:       c.Email,
				Role:        model.RoleUser,
				Identities:  []model.Identity{{Provider: p.ID, Subject: c.Subject}},
			},
		}
		err := s.DB.CreateUser(ctx, u)
		if errors.Is(err, store.ErrExists) {
			taken[name] = true
			continue
		}
		if err != nil {
			return nil, err
		}
		return u, nil
	}
	return nil, errors.New("could not find a free user name")
}

func allowed(p *config.OIDCProvider, c Claims) error {
	if c.Subject == "" {
		return fmt.Errorf("%w: the provider returned no subject", ErrNotAllowed)
	}
	if len(p.AllowedGroups) > 0 {
		ok := false
		for _, g := range p.AllowedGroups {
			for _, h := range c.Groups {
				ok = ok || g == h
			}
		}
		if !ok {
			return fmt.Errorf("%w: not in an allowed group", ErrNotAllowed)
		}
	}
	if len(p.AllowedEmails) > 0 {
		if c.Email == "" || (c.EmailVerified != nil && !*c.EmailVerified) {
			return fmt.Errorf("%w: a verified e-mail address is required", ErrNotAllowed)
		}
		ok := false
		email := strings.ToLower(c.Email)
		for _, pat := range p.AllowedEmails {
			pat = strings.ToLower(pat)
			if domain, wild := strings.CutPrefix(pat, "*@"); wild {
				ok = ok || strings.HasSuffix(email, "@"+domain)
			} else {
				ok = ok || email == pat
			}
		}
		if !ok {
			return fmt.Errorf("%w: e-mail address not allowed", ErrNotAllowed)
		}
	}
	return nil
}

var invalidChars = regexp.MustCompile(`[^a-z0-9-]+`)

// UserName derives a DNS label user name from login attributes.
func UserName(username, email, subject string) string {
	for _, cand := range []string{username, strings.SplitN(email, "@", 2)[0], subject} {
		n := strings.ToLower(strings.TrimSpace(cand))
		n = invalidChars.ReplaceAllString(n, "-")
		n = strings.Trim(truncate(strings.Trim(n, "-"), 40), "-")
		if n != "" && n[0] >= 'a' && n[0] <= 'z' {
			return n
		}
		if n != "" {
			return strings.Trim(truncate("u-"+n, 40), "-")
		}
	}
	return "user"
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Limits are a user's effective consumption limits. Nil fields are unlimited.
type Limits struct {
	MaxEnvironments *int32
	MaxRunning      *int32
	CPU             *resource.Quantity
	Memory          *resource.Quantity
	Disk            *resource.Quantity
}

// EffectiveLimits merges the user's own limits with the server defaults.
func (s *Store) EffectiveLimits(u *model.User, admin bool) Limits {
	var base *config.Limits
	if admin {
		base = s.Config.Users.AdminLimits
	} else {
		base = &s.Config.Users.DefaultLimits
	}
	out := Limits{}
	if base != nil {
		if base.MaxEnvironments > 0 {
			out.MaxEnvironments = ptr.To(base.MaxEnvironments)
		}
		if base.MaxRunning > 0 {
			out.MaxRunning = ptr.To(base.MaxRunning)
		}
		out.CPU = parse(base.CPU)
		out.Memory = parse(base.Memory)
		out.Disk = parse(base.Disk)
	}
	if u == nil {
		return out
	}
	l := u.Spec.Limits
	if l.MaxEnvironments != nil {
		out.MaxEnvironments = l.MaxEnvironments
	}
	if l.MaxRunning != nil {
		out.MaxRunning = l.MaxRunning
	}
	if l.CPU != nil {
		out.CPU = l.CPU
	}
	if l.Memory != nil {
		out.Memory = l.Memory
	}
	if l.Disk != nil {
		out.Disk = l.Disk
	}
	return out
}

func parse(v string) *resource.Quantity {
	if v == "" {
		return nil
	}
	q, err := resource.ParseQuantity(v)
	if err != nil {
		return nil
	}
	return &q
}

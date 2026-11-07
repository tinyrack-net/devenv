// Package config loads the devenv server configuration.
//
// Deployment settings (authentication, sessions, MCP, SSH, user defaults) live
// in one YAML file (--config / DEVENV_CONFIG, rendered by the Helm chart from
// `config:`). Secrets are referenced by *File fields that point at mounted
// Secret keys. Command line flags and the older environment variables still
// work and override the file.
//
// Platform resources (images, classes) and user data stay in CRDs.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"
)

// Config is the server configuration file.
type Config struct {
	Server Server `json:"server"`
	Auth   Auth   `json:"auth"`
	Users  Users  `json:"users"`
	MCP    MCP    `json:"mcp"`
	SSH    SSH    `json:"ssh"`
}

type Server struct {
	// ExternalURL is the public URL of the console, e.g.
	// https://devenv.example.com. Required for OIDC callbacks.
	ExternalURL string `json:"externalURL,omitempty"`
}

type Auth struct {
	Session Session `json:"session"`
	// Admins are user names or e-mail addresses that always get the admin role.
	Admins []string `json:"admins,omitempty"`
	// Password is the local admin account.
	Password Password `json:"password"`
	// OIDC providers. Several can be enabled at once.
	OIDC []OIDCProvider `json:"oidc,omitempty"`
	// Disabled turns authentication off: every request is the local admin.
	// Development only.
	Disabled bool `json:"disabled,omitempty"`
}

type Session struct {
	TTL           Duration `json:"ttl,omitempty"`
	SecureCookies bool     `json:"secureCookies,omitempty"`
}

type Password struct {
	Enabled  *bool  `json:"enabled,omitempty"`
	Username string `json:"username,omitempty"`
	// Password in plain text. Prefer PasswordFile.
	Password     string `json:"password,omitempty"`
	PasswordFile string `json:"passwordFile,omitempty"`
}

// IsEnabled reports whether local password login is on (default true).
func (p Password) IsEnabled() bool { return p.Enabled == nil || *p.Enabled }

type OIDCProvider struct {
	// ID appears in callback URLs and DevUser identities. DNS label.
	ID string `json:"id"`
	// Name is shown on the login button.
	Name             string   `json:"name,omitempty"`
	Issuer           string   `json:"issuer"`
	ClientID         string   `json:"clientID"`
	ClientSecret     string   `json:"clientSecret,omitempty"`
	ClientSecretFile string   `json:"clientSecretFile,omitempty"`
	Scopes           []string `json:"scopes,omitempty"`
	Claims           Claims   `json:"claims"`
	// AutoCreate creates a DevUser on first login (default true). When false
	// only users an admin created (or linked) can sign in.
	AutoCreate *bool `json:"autoCreate,omitempty"`
	// AllowedGroups restricts login to members of these groups. Empty allows all.
	AllowedGroups []string `json:"allowedGroups,omitempty"`
	// AllowedEmails restricts login to these addresses; "*@example.com"
	// matches a domain. Empty allows all. Requires a verified e-mail when the
	// provider reports email_verified.
	AllowedEmails []string `json:"allowedEmails,omitempty"`
	// AdminGroups grant the admin role.
	AdminGroups []string `json:"adminGroups,omitempty"`
}

// IsAutoCreate reports whether unknown users are created on login.
func (p OIDCProvider) IsAutoCreate() bool { return p.AutoCreate == nil || *p.AutoCreate }

type Claims struct {
	Username string `json:"username,omitempty"`
	Email    string `json:"email,omitempty"`
	Name     string `json:"name,omitempty"`
	Groups   string `json:"groups,omitempty"`
}

type Users struct {
	// DefaultLimits apply to users without their own limits. Admins are
	// unlimited unless AdminLimits is set.
	DefaultLimits Limits  `json:"defaultLimits"`
	AdminLimits   *Limits `json:"adminLimits,omitempty"`
}

// Limits caps a user's consumption. Zero values mean unlimited.
type Limits struct {
	MaxEnvironments int32  `json:"maxEnvironments,omitempty"`
	MaxRunning      int32  `json:"maxRunning,omitempty"`
	CPU             string `json:"cpu,omitempty"`
	Memory          string `json:"memory,omitempty"`
	Disk            string `json:"disk,omitempty"`
}

type MCP struct {
	Enabled *bool `json:"enabled,omitempty"`
	// LegacyToken is the single bearer token of older versions (Secret key
	// mcp-token, chart mcp.existingSecret). It acts as User.
	LegacyToken LegacyToken `json:"legacyToken"`
}

// IsEnabled reports whether /mcp is served (default true).
func (m MCP) IsEnabled() bool { return m.Enabled == nil || *m.Enabled }

type LegacyToken struct {
	// Enabled defaults to true.
	Enabled *bool `json:"enabled,omitempty"`
	// User the token acts as. Defaults to the local admin user name.
	User      string `json:"user,omitempty"`
	Token     string `json:"token,omitempty"`
	TokenFile string `json:"tokenFile,omitempty"`
}

// IsEnabled reports whether the legacy token is accepted (default true).
func (l LegacyToken) IsEnabled() bool { return l.Enabled == nil || *l.Enabled }

type SSH struct {
	PublicHost string `json:"publicHost,omitempty"`
	PublicPort int    `json:"publicPort,omitempty"`
}

// Duration is a time.Duration written as "168h".
type Duration struct{ time.Duration }

func (d Duration) MarshalJSON() ([]byte, error) { return []byte(`"` + d.String() + `"`), nil }

func (d *Duration) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		d.Duration = 0
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q", s)
	}
	d.Duration = v
	return nil
}

// Load reads a configuration file. An empty path returns the defaults.
func Load(path string) (*Config, error) {
	c := &Config{}
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if err := yaml.UnmarshalStrict(b, c); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return c, nil
}

// Defaults fills unset values.
func (c *Config) Defaults() {
	if c.Auth.Session.TTL.Duration == 0 {
		c.Auth.Session.TTL.Duration = 7 * 24 * time.Hour
	}
	if c.Auth.Password.Username == "" {
		c.Auth.Password.Username = "admin"
	}
	for i := range c.Auth.OIDC {
		p := &c.Auth.OIDC[i]
		if p.Name == "" {
			p.Name = p.ID
		}
		if len(p.Scopes) == 0 {
			p.Scopes = []string{"openid", "profile", "email"}
		}
		if p.Claims.Username == "" {
			p.Claims.Username = "preferred_username"
		}
		if p.Claims.Email == "" {
			p.Claims.Email = "email"
		}
		if p.Claims.Name == "" {
			p.Claims.Name = "name"
		}
		if p.Claims.Groups == "" {
			p.Claims.Groups = "groups"
		}
	}
	if c.MCP.LegacyToken.User == "" {
		c.MCP.LegacyToken.User = c.Auth.Password.Username
	}
	if c.SSH.PublicPort == 0 {
		c.SSH.PublicPort = 2222
	}
}

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// Validate checks the configuration after Defaults and secret resolution.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if c.Server.ExternalURL != "" {
		if u, err := url.Parse(c.Server.ExternalURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			add("server.externalURL must be an absolute http(s) URL")
		}
	}
	if !dnsLabel.MatchString(c.Auth.Password.Username) {
		add("auth.password.username %q must be a DNS label (lowercase letters, digits, '-')", c.Auth.Password.Username)
	}
	if !c.Auth.Disabled {
		if c.Auth.Password.IsEnabled() && c.Auth.Password.Password == "" {
			add("auth.password: password or passwordFile is required (or set auth.password.enabled: false)")
		}
		if !c.Auth.Password.IsEnabled() && len(c.Auth.OIDC) == 0 {
			add("auth: enable password login or configure at least one OIDC provider")
		}
	}
	seen := map[string]bool{"password": true}
	for i, p := range c.Auth.OIDC {
		f := fmt.Sprintf("auth.oidc[%d]", i)
		if !dnsLabel.MatchString(p.ID) {
			add("%s.id %q must be a DNS label", f, p.ID)
		} else if seen[p.ID] {
			add("%s.id %q is used twice (or reserved)", f, p.ID)
		}
		seen[p.ID] = true
		if u, err := url.Parse(p.Issuer); err != nil || u.Scheme == "" || u.Host == "" {
			add("%s.issuer must be a URL", f)
		}
		if p.ClientID == "" {
			add("%s.clientID is required", f)
		}
		if c.Server.ExternalURL == "" {
			add("%s: server.externalURL is required for OIDC callbacks", f)
		}
	}
	for _, l := range append([]*Limits{&c.Users.DefaultLimits}, c.Users.AdminLimits) {
		if l == nil {
			continue
		}
		for name, v := range map[string]string{"cpu": l.CPU, "memory": l.Memory, "disk": l.Disk} {
			if v == "" {
				continue
			}
			if _, err := resource.ParseQuantity(v); err != nil {
				add("users limits: invalid %s %q", name, v)
			}
		}
	}
	return errors.Join(errs...)
}

// ResolveSecrets reads every *File field into its value field.
func (c *Config) ResolveSecrets() error {
	read := func(field, path string, dst *string) error {
		if path == "" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		*dst = strings.TrimSpace(string(b))
		return nil
	}
	if err := read("auth.password.passwordFile", c.Auth.Password.PasswordFile, &c.Auth.Password.Password); err != nil {
		return err
	}
	for i := range c.Auth.OIDC {
		p := &c.Auth.OIDC[i]
		if err := read(fmt.Sprintf("auth.oidc[%d].clientSecretFile", i), p.ClientSecretFile, &p.ClientSecret); err != nil {
			return err
		}
	}
	return read("mcp.legacyToken.tokenFile", c.MCP.LegacyToken.TokenFile, &c.MCP.LegacyToken.Token)
}

// Redacted returns a copy without secret values, for display.
func (c *Config) Redacted() *Config {
	out := *c
	mask := func(s string) string {
		if s == "" {
			return ""
		}
		return "********"
	}
	out.Auth.Password.Password = mask(c.Auth.Password.Password)
	out.Auth.OIDC = make([]OIDCProvider, len(c.Auth.OIDC))
	for i, p := range c.Auth.OIDC {
		p.ClientSecret = mask(p.ClientSecret)
		out.Auth.OIDC[i] = p
	}
	out.MCP.LegacyToken.Token = mask(c.MCP.LegacyToken.Token)
	return &out
}

// Provider returns an OIDC provider by id.
func (c *Config) Provider(id string) *OIDCProvider {
	for i := range c.Auth.OIDC {
		if c.Auth.OIDC[i].ID == id {
			return &c.Auth.OIDC[i]
		}
	}
	return nil
}

// IsConfiguredAdmin reports whether a user name or e-mail is in auth.admins.
func (c *Config) IsConfiguredAdmin(name, email string) bool {
	for _, a := range c.Auth.Admins {
		if a == name || (email != "" && strings.EqualFold(a, email)) {
			return true
		}
	}
	return false
}

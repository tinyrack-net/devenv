package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFull(t *testing.T) {
	secret := write(t, "oidc", "s3cret\n")
	pw := write(t, "pw", "hunter2")
	p := write(t, "devenv.yaml", `
server:
  externalURL: https://devenv.example.com
auth:
  session: {ttl: 12h, secureCookies: true}
  admins: [alice]
  password: {passwordFile: `+pw+`}
  oidc:
    - id: issuary
      issuer: https://auth.example.com
      clientID: devenv
      clientSecretFile: `+secret+`
      adminGroups: [admins]
users:
  defaultLimits: {maxEnvironments: 3, cpu: "4", memory: 16Gi}
preview: {domain: preview.example.com}
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	c.Defaults()
	if err := c.ResolveSecrets(); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Auth.Session.TTL.Duration != 12*time.Hour || !c.Auth.Session.SecureCookies {
		t.Fatalf("session %+v", c.Auth.Session)
	}
	o := c.Provider("issuary")
	if o == nil || o.ClientSecret != "s3cret" || o.Name != "issuary" || o.Claims.Username != "preferred_username" || !o.IsAutoCreate() {
		t.Fatalf("provider %+v", o)
	}
	if c.Auth.Password.Password != "hunter2" || c.MCP.LegacyToken.User != "admin" {
		t.Fatalf("defaults %+v %+v", c.Auth.Password, c.MCP.LegacyToken)
	}
	r := c.Redacted()
	if r.Auth.OIDC[0].ClientSecret != "********" || c.Auth.OIDC[0].ClientSecret != "s3cret" {
		t.Fatal("redaction must not modify the original")
	}
	if !c.IsConfiguredAdmin("alice", "") || c.IsConfiguredAdmin("bob", "") {
		t.Fatal("admins")
	}
}

func TestUnknownField(t *testing.T) {
	p := write(t, "c.yaml", "auth:\n  pasword: {}\n")
	if _, err := Load(p); err == nil {
		t.Fatal("unknown fields must be rejected")
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]struct {
		cfg  Config
		want string
	}{
		"password required": {Config{}, "password or passwordFile"},
		"no login": {Config{Auth: Auth{Password: Password{Enabled: new(false)}}}, "at least one OIDC"},
		"oidc needs url": {Config{Auth: Auth{Password: Password{Password: "x"}, OIDC: []OIDCProvider{{ID: "a", Issuer: "https://a", ClientID: "c"}}}}, "externalURL is required"},
		"bad id": {Config{Server: Server{ExternalURL: "https://x"}, Auth: Auth{Password: Password{Password: "x"}, OIDC: []OIDCProvider{{ID: "password", Issuer: "https://a", ClientID: "c"}}}}, "reserved"},
		"bad limit": {Config{Auth: Auth{Password: Password{Password: "x"}}, Users: Users{DefaultLimits: Limits{CPU: "lots"}}}, "invalid cpu"},
		"disabled ok": {Config{Auth: Auth{Disabled: true}}, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tc.cfg.Defaults()
			err := tc.cfg.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

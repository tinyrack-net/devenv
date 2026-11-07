package users

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
	"github.com/tinyrack-net/devenv/internal/config"
)

func store(t *testing.T, objs ...client.Object) *Store {
	s := runtime.NewScheme()
	_ = devenvv1.AddToScheme(s)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(&devenvv1.DevUser{}).Build()
	cfg := &config.Config{Auth: config.Auth{Admins: []string{"boss@example.com"}, OIDC: []config.OIDCProvider{{ID: "sso", AdminGroups: []string{"admins"}}}}}
	cfg.Defaults()
	return &Store{Client: c, Config: cfg}
}

func TestUserName(t *testing.T) {
	for _, tc := range [][4]string{
		{"Alice.Smith", "", "", "alice-smith"},
		{"", "bob@example.com", "", "bob"},
		{"", "", "1234", "u-1234"},
		{"한글", "", "x9", "x9"},
		{"", "", "", "user"},
	} {
		if got := UserName(tc[0], tc[1], tc[2]); got != tc[3] {
			t.Errorf("UserName(%q,%q,%q) = %q, want %q", tc[0], tc[1], tc[2], got, tc[3])
		}
	}
}

func TestLoginOIDC(t *testing.T) {
	ctx := context.Background()
	pre := &devenvv1.DevUser{ObjectMeta: metav1.ObjectMeta{Name: "carol"}, Spec: devenvv1.DevUserSpec{Email: "carol@example.com"}}
	taken := &devenvv1.DevUser{ObjectMeta: metav1.ObjectMeta{Name: "alice"}}
	s := store(t, pre, taken)
	p := s.Config.Provider("sso")

	// New user; the name "alice" is taken so a suffix is used.
	u, err := s.LoginOIDC(ctx, p, Claims{Subject: "s1", Username: "alice", Email: "alice@example.com", Groups: []string{"devs"}})
	if err != nil || u.Name != "alice-2" || u.Spec.Role != devenvv1.RoleUser {
		t.Fatalf("create: %+v %v", u, err)
	}
	// Same subject logs into the same user and refreshes groups.
	u, err = s.LoginOIDC(ctx, p, Claims{Subject: "s1", Username: "renamed", Groups: []string{"admins"}})
	if err != nil || u.Name != "alice-2" {
		t.Fatalf("relogin: %+v %v", u, err)
	}
	got, _ := s.Get(ctx, "alice-2")
	if !s.IsAdmin(got) {
		t.Fatal("admin group not applied")
	}
	// Pre-created user is linked by verified e-mail.
	verified := true
	u, err = s.LoginOIDC(ctx, p, Claims{Subject: "s2", Email: "carol@example.com", EmailVerified: &verified})
	if err != nil || u.Name != "carol" || len(u.Spec.Identities) != 1 {
		t.Fatalf("link: %+v %v", u, err)
	}
	// Unverified e-mail does not link.
	notVerified := false
	pre2 := &devenvv1.DevUser{ObjectMeta: metav1.ObjectMeta{Name: "dave"}, Spec: devenvv1.DevUserSpec{Email: "dave@example.com"}}
	_ = s.Client.Create(ctx, pre2)
	u, _ = s.LoginOIDC(ctx, p, Claims{Subject: "s3", Username: "mallory", Email: "dave@example.com", EmailVerified: &notVerified})
	if u.Name == "dave" {
		t.Fatal("linked through an unverified e-mail")
	}
	// Configured admins by e-mail.
	u, _ = s.LoginOIDC(ctx, p, Claims{Subject: "s4", Username: "boss", Email: "boss@example.com"})
	if !s.IsAdmin(u) {
		t.Fatal("auth.admins e-mail not applied")
	}

	// Rules.
	strict := config.OIDCProvider{ID: "strict", AllowedGroups: []string{"devs"}, AllowedEmails: []string{"*@corp.com"}, AutoCreate: new(false)}
	if _, err := s.LoginOIDC(ctx, &strict, Claims{Subject: "x", Email: "a@corp.com"}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("group rule: %v", err)
	}
	if _, err := s.LoginOIDC(ctx, &strict, Claims{Subject: "x", Email: "a@other.com", Groups: []string{"devs"}}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("email rule: %v", err)
	}
	if _, err := s.LoginOIDC(ctx, &strict, Claims{Subject: "x", Email: "a@corp.com", Groups: []string{"devs"}}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("autoCreate false: %v", err)
	}
	// Disabled.
	got, _ = s.Get(ctx, "alice-2")
	got.Spec.Disabled = true
	_ = s.Client.Update(ctx, got)
	if _, err := s.LoginOIDC(ctx, p, Claims{Subject: "s1"}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
}

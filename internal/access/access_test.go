package access

import (
	"testing"

	"github.com/tinyrack-net/devenv/internal/auth"
	"github.com/tinyrack-net/devenv/internal/model"
)

func TestEnvironment(t *testing.T) {
	alice := &auth.Principal{Name: "alice", Groups: []string{"dev"}}
	bob := &auth.Principal{Name: "bob"}
	carol := &auth.Principal{Name: "carol", Groups: []string{"ops"}}
	admin := &auth.Principal{Name: "root", Admin: true}
	env := &model.Environment{Spec: model.EnvironmentSpec{
		Owner:   "alice",
		Sharing: model.Access{Users: []string{"bob"}, Groups: []string{"ops"}},
	}}
	for _, tc := range []struct {
		p    *auth.Principal
		want Level
	}{{alice, Manage}, {bob, Use}, {carol, Use}, {admin, Manage}, {&auth.Principal{Name: "dave"}, None}, {nil, None}} {
		if got := Environment(tc.p, env); got != tc.want {
			t.Errorf("%v: %v, want %v", tc.p, got, tc.want)
		}
	}
}

func TestImagesAndClasses(t *testing.T) {
	alice := &auth.Principal{Name: "alice", Groups: []string{"dev"}}
	bob := &auth.Principal{Name: "bob"}
	admin := &auth.Principal{Name: "root", Admin: true}
	shared := &model.Image{}
	restricted := &model.Image{Spec: model.ImageSpec{Access: model.Access{Groups: []string{"dev"}}}}
	personal := &model.Image{Spec: model.ImageSpec{Owner: "alice"}}

	check := func(name string, got, want bool) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	check("bob shared", CanUseImage(bob, shared), true)
	check("bob restricted", CanUseImage(bob, restricted), false)
	check("alice restricted", CanUseImage(alice, restricted), true)
	check("bob personal", CanUseImage(bob, personal), false)
	check("alice personal", CanUseImage(alice, personal), true)
	check("admin personal", CanUseImage(admin, personal), true)
	check("alice manage shared", CanManageImage(alice, shared), false)
	check("alice manage personal", CanManageImage(alice, personal), true)
	check("admin manage shared", CanManageImage(admin, shared), true)
	check("admin manage configured", CanManageImage(admin, &model.Image{Managed: true}), false)

	lan := &model.Class{Spec: model.ClassSpec{Access: model.Access{Groups: []string{"dev"}}}}
	check("alice lan", CanUseClass(alice, lan), true)
	check("bob lan", CanUseClass(bob, lan), false)
	check("admin lan", CanUseClass(admin, lan), true)
	check("bob default", CanUseClass(bob, &model.Class{}), true)
}

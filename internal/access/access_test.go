package access

import (
	"testing"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
	"github.com/tinyrack-net/devenv/internal/auth"
)

func TestEnvironment(t *testing.T) {
	alice := &auth.Principal{Name: "alice", Groups: []string{"dev"}}
	bob := &auth.Principal{Name: "bob"}
	carol := &auth.Principal{Name: "carol", Groups: []string{"ops"}}
	admin := &auth.Principal{Name: "root", Admin: true}
	env := &devenvv1.DevEnvironment{Spec: devenvv1.DevEnvironmentSpec{
		Owner:   "alice",
		Sharing: devenvv1.Access{Users: []string{"bob"}, Groups: []string{"ops"}},
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
	shared := &devenvv1.DevImage{}
	restricted := &devenvv1.DevImage{Spec: devenvv1.DevImageSpec{Access: devenvv1.Access{Groups: []string{"dev"}}}}
	personal := &devenvv1.DevImage{Spec: devenvv1.DevImageSpec{Owner: "alice"}}

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

	lan := &devenvv1.DevEnvironmentClass{Spec: devenvv1.DevEnvironmentClassSpec{Access: devenvv1.Access{Groups: []string{"dev"}}}}
	check("alice lan", CanUseClass(alice, lan), true)
	check("bob lan", CanUseClass(bob, lan), false)
	check("admin lan", CanUseClass(admin, lan), true)
	check("bob default", CanUseClass(bob, &devenvv1.DevEnvironmentClass{}), true)
}

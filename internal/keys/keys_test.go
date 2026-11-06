package keys

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestEnsureIsStable(t *testing.T) {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	a, err := Ensure(context.Background(), c, "ns", "keys")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Ensure(context.Background(), c, "ns", "keys")
	if err != nil {
		t.Fatal(err)
	}
	if string(a.SessionKey) != string(b.SessionKey) || len(a.SessionKey) != 32 {
		t.Errorf("session key not stable")
	}
	if a.ClientAuthorizedKey() != b.ClientAuthorizedKey() {
		t.Errorf("client key not stable")
	}
	if !strings.HasPrefix(a.ClientAuthorizedKey(), "ssh-ed25519 ") || !strings.HasSuffix(a.ClientAuthorizedKey(), " devenv-server") {
		t.Errorf("authorized key = %q", a.ClientAuthorizedKey())
	}
	if string(a.HostKey.PublicKey().Marshal()) == string(a.ClientKey.PublicKey().Marshal()) {
		t.Errorf("host and client keys must differ")
	}
}

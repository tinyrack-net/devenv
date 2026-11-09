package keys

import (
	"context"
	"strings"
	"sync"
	"testing"
)

type memKV struct {
	mu sync.Mutex
	m  map[string]string
}

func (k *memKV) SetValueIfAbsent(_ context.Context, key, value string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if v, ok := k.m[key]; ok {
		return v, nil
	}
	k.m[key] = value
	return value, nil
}

func TestEnsureIsStable(t *testing.T) {
	c := &memKV{m: map[string]string{}}

	a, err := Ensure(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Ensure(context.Background(), c)
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
	if a.MCPToken == "" || a.MCPToken != b.MCPToken || !strings.HasPrefix(a.MCPToken, "dvm_") {
		t.Errorf("mcp token = %q / %q", a.MCPToken, b.MCPToken)
	}
	if string(a.HostKey.PublicKey().Marshal()) == string(a.ClientKey.PublicKey().Marshal()) {
		t.Errorf("host and client keys must differ")
	}
}

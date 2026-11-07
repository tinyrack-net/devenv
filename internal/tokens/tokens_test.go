package tokens

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestTokens(t *testing.T) {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	now := time.Now()
	st := &Store{Client: c, Namespace: "sys", now: func() time.Time { return now }}
	ctx := context.Background()

	tok, plain, err := st.Create(ctx, "alice", "laptop agent", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, Prefix+tok.ID+"_") || !tok.HasScope(ScopeMCP) || tok.HasScope(ScopeAPI) {
		t.Fatalf("token %+v %q", tok, plain)
	}
	got, err := st.Verify(ctx, plain)
	if err != nil || got.User != "alice" || got.Name != "laptop agent" {
		t.Fatalf("verify %+v %v", got, err)
	}
	for _, bad := range []string{"", "dvn_x", plain + "x", strings.Replace(plain, tok.ID, "aaaaaaaa", 1), strings.TrimPrefix(plain, Prefix)} {
		if _, err := st.Verify(ctx, bad); err != ErrInvalid {
			t.Errorf("verify(%q) = %v, want ErrInvalid", bad, err)
		}
	}
	now = now.Add(2 * time.Hour)
	if _, err := st.Verify(ctx, plain); err != ErrInvalid {
		t.Fatal("expired token accepted")
	}

	_, other, _ := st.Create(ctx, "bob", "b", []string{ScopeAPI}, 0)
	if l, _ := st.List(ctx, "alice"); len(l) != 1 {
		t.Fatalf("alice tokens %v", l)
	}
	if l, _ := st.List(ctx, ""); len(l) != 2 {
		t.Fatalf("all tokens %v", l)
	}
	if err := st.Delete(ctx, "alice", tok.ID); err != nil {
		t.Fatal(err)
	}
	bt, _ := st.Verify(ctx, other)
	if err := st.Delete(ctx, "alice", bt.ID); err == nil {
		t.Fatal("deleted another user's token")
	}
	if _, _, err := st.Create(ctx, "a", "x", []string{"root"}, 0); err == nil {
		t.Fatal("unknown scope accepted")
	}
}

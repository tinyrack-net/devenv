package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// fakeIdP is a minimal OpenID provider: discovery, JWKS, authorize (records
// the request) and token (checks PKCE, returns a signed id_token).
type fakeIdP struct {
	t      *testing.T
	srv    *httptest.Server
	key    *rsa.PrivateKey
	nonce  string
	chall  string
	claims map[string]any
}

func newIdP(t *testing.T) *fakeIdP {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	f := &fakeIdP{t: t, key: key, claims: map[string]any{}}
	mux := http.NewServeMux()
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	iss := f.srv.URL
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": iss, "authorization_endpoint": iss + "/authorize", "token_endpoint": iss + "/token",
			"jwks_uri": iss + "/jwks", "userinfo_endpoint": iss + "/userinfo",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"sub": "user-1", "groups": []string{"devs", "admins"}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if r.Form.Get("code") != "the-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != f.chall {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		sig, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "k1"))
		cl := jwt.Claims{Issuer: iss, Subject: "user-1", Audience: jwt.Audience{"devenv"},
			IssuedAt: jwt.NewNumericDate(time.Now()), Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour))}
		extra := map[string]any{"nonce": f.nonce, "preferred_username": "Alice.Smith", "email": "alice@example.com", "email_verified": true, "name": "Alice"}
		for k, v := range f.claims {
			extra[k] = v
		}
		tok, _ := jwt.Signed(sig).Claims(cl).Claims(extra).Serialize()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": tok, "expires_in": 3600})
	})
	return f
}

func TestOIDCFlow(t *testing.T) {
	idp := newIdP(t)
	sessions, _ := NewSessions([]byte("k"), false)
	o := &OIDC{Config: OIDCConfig{
		ID: "test", Issuer: idp.srv.URL, ClientID: "devenv", ClientSecret: "s",
		Scopes: []string{"openid", "profile", "email"}, RedirectURL: "https://devenv.example.com/api/v1/auth/oidc/test/callback",
		Claims: ClaimNames{Username: "preferred_username", Email: "email", Name: "name", Groups: "groups"},
	}}

	// Start: redirect to the IdP with state, nonce and an S256 challenge.
	rec := httptest.NewRecorder()
	if err := o.Start(rec, httptest.NewRequest("GET", "/api/v1/auth/oidc/test/login", nil), sessions, "/env/x"); err != nil {
		t.Fatal(err)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	q := loc.Query()
	if !strings.HasPrefix(loc.String(), idp.srv.URL+"/authorize") || q.Get("code_challenge_method") != "S256" || q.Get("state") == "" {
		t.Fatalf("redirect %s", loc)
	}
	idp.nonce, idp.chall = q.Get("nonce"), q.Get("code_challenge")
	cookie := rec.Result().Cookies()[0]

	callback := func(query string, c *http.Cookie) (*Identity, string, error) {
		req := httptest.NewRequest("GET", "/api/v1/auth/oidc/test/callback?"+query, nil)
		if c != nil {
			req.AddCookie(c)
		}
		return o.Callback(req, httptest.NewRecorder(), sessions)
	}
	// Forged state, missing cookie.
	if _, _, err := callback("code=the-code&state=forged", cookie); err == nil {
		t.Fatal("forged state accepted")
	}
	if _, _, err := callback("code=the-code&state="+q.Get("state"), nil); err == nil {
		t.Fatal("missing flow cookie accepted")
	}
	id, ret, err := callback("code=the-code&state="+q.Get("state"), cookie)
	if err != nil {
		t.Fatal(err)
	}
	if id.Subject != "user-1" || id.Username != "Alice.Smith" || id.Email != "alice@example.com" || id.EmailVerified == nil || !*id.EmailVerified || ret != "/env/x" {
		t.Fatalf("identity %+v return %q", id, ret)
	}
	if len(id.Groups) != 2 || id.Groups[1] != "admins" {
		t.Fatalf("groups from userinfo: %v", id.Groups)
	}

	// A token for another nonce is rejected (replayed id_token).
	idp.nonce = "other"
	if _, _, err := callback("code=the-code&state="+q.Get("state"), cookie); err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Fatalf("nonce mismatch: %v", err)
	}
}

func TestSafeReturn(t *testing.T) {
	for in, want := range map[string]string{
		"/env/a": "/env/a", "": "/", "https://evil.com": "/", "//evil.com": "/", "/\\evil.com": "/", "env": "/",
	} {
		if got := SafeReturn(in); got != want {
			t.Errorf("SafeReturn(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSessions(t *testing.T) {
	s, _ := NewSessions([]byte("k"), false)
	tok, _ := s.Encode("alice", "password")
	got, err := s.Decode(tok)
	if err != nil || got.User != "alice" || got.Provider != "password" {
		t.Fatalf("%+v %v", got, err)
	}
	other, _ := NewSessions([]byte("other"), false)
	if _, err := other.Decode(tok); err == nil {
		t.Fatal("foreign key accepted")
	}
	// A sealed value for another purpose is not a session.
	st, _ := s.Seal("oidc-x", Session{User: "alice", Exp: time.Now().Add(time.Hour).Unix()})
	if _, err := s.Decode(st); err == nil {
		t.Fatal("cross-purpose token accepted")
	}
	s.now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
	if _, err := s.Decode(tok); err == nil {
		t.Fatal("expired session accepted")
	}
}

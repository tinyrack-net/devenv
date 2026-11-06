package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPasswordAuthenticator(t *testing.T) {
	a := &PasswordAuthenticator{Username: "admin", Password: "secret"}
	if _, err := a.Login(context.Background(), "admin", "secret"); err != nil {
		t.Fatalf("valid login failed: %v", err)
	}
	for _, c := range [][2]string{{"admin", "wrong"}, {"root", "secret"}, {"", ""}} {
		if _, err := a.Login(context.Background(), c[0], c[1]); err == nil {
			t.Errorf("login %v should fail", c)
		}
	}
	empty := &PasswordAuthenticator{Username: "admin"}
	if _, err := empty.Login(context.Background(), "admin", ""); err == nil {
		t.Errorf("empty password must never authenticate")
	}
}

func TestSessionRoundTrip(t *testing.T) {
	s, _ := NewSessions([]byte("k"), false)
	tok, err := s.Encode(&User{Name: "admin", Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.Decode(tok)
	if err != nil || u.Name != "admin" || !u.Admin {
		t.Fatalf("decode = %+v, %v", u, err)
	}

	other, _ := NewSessions([]byte("other"), false)
	if _, err := other.Decode(tok); err == nil {
		t.Errorf("token signed with another key must be rejected")
	}
	if _, err := s.Decode(tok + "x"); err == nil {
		t.Errorf("tampered token must be rejected")
	}

	s.now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
	if _, err := s.Decode(tok); err == nil {
		t.Errorf("expired token must be rejected")
	}
}

func TestMiddleware(t *testing.T) {
	s, _ := NewSessions([]byte("k"), false)
	a := &PasswordAuthenticator{Username: "admin", Password: "p"}
	h := Middleware(a, s, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if UserFrom(r.Context()) == nil {
			t.Error("user missing in context")
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	do := func(method string, cookie bool, csrf bool) int {
		req := httptest.NewRequest(method, "/api/x", nil)
		if cookie {
			tok, _ := s.Encode(&User{Name: "admin"})
			req.AddCookie(&http.Cookie{Name: s.CookieName, Value: tok})
		}
		if csrf {
			req.Header.Set(CSRFHeader, "devenv")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if c := do("GET", false, false); c != 401 {
		t.Errorf("no cookie: %d", c)
	}
	if c := do("GET", true, false); c != 204 {
		t.Errorf("get with cookie: %d", c)
	}
	if c := do("POST", true, false); c != 403 {
		t.Errorf("post without csrf header: %d", c)
	}
	if c := do("POST", true, true); c != 204 {
		t.Errorf("post with csrf header: %d", c)
	}
}

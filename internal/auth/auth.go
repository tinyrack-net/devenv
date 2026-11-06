// Package auth provides pluggable authentication for the devenv API.
//
// The initial implementation is a single admin password with a signed
// session cookie. The Authenticator interface allows replacing it with OIDC
// or a forward-auth proxy later without touching the API handlers.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// User is an authenticated principal.
type User struct {
	Name  string `json:"name"`
	Admin bool   `json:"admin"`
}

// ErrInvalidCredentials is returned when login fails.
var ErrInvalidCredentials = errors.New("invalid credentials")

// Authenticator verifies credentials.
type Authenticator interface {
	// Mode identifies the authenticator for the frontend ("password", "none").
	Mode() string
	// Login verifies a username/password pair.
	Login(ctx context.Context, username, password string) (*User, error)
}

// PasswordAuthenticator accepts a single admin user.
type PasswordAuthenticator struct {
	Username string
	Password string
}

func (a *PasswordAuthenticator) Mode() string { return "password" }

func (a *PasswordAuthenticator) Login(_ context.Context, username, password string) (*User, error) {
	uh := sha256.Sum256([]byte(username))
	ue := sha256.Sum256([]byte(a.Username))
	ph := sha256.Sum256([]byte(password))
	pe := sha256.Sum256([]byte(a.Password))
	okU := subtle.ConstantTimeCompare(uh[:], ue[:])
	okP := subtle.ConstantTimeCompare(ph[:], pe[:])
	if okU&okP != 1 || a.Password == "" {
		return nil, ErrInvalidCredentials
	}
	return &User{Name: a.Username, Admin: true}, nil
}

// NoneAuthenticator treats every request as the admin user. Development only.
type NoneAuthenticator struct{ Username string }

func (a *NoneAuthenticator) Mode() string { return "none" }

func (a *NoneAuthenticator) Login(context.Context, string, string) (*User, error) {
	return &User{Name: a.Username, Admin: true}, nil
}

// Sessions issues and verifies HMAC signed session cookies.
type Sessions struct {
	Key        []byte
	CookieName string
	TTL        time.Duration
	Secure     bool
	now        func() time.Time
}

// NewSessions creates a session manager. A random key is generated when key is empty,
// which invalidates sessions on restart.
func NewSessions(key []byte, secure bool) (*Sessions, error) {
	if len(key) == 0 {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
	}
	return &Sessions{Key: key, CookieName: "devenv_session", TTL: 7 * 24 * time.Hour, Secure: secure, now: time.Now}, nil
}

type claims struct {
	User
	Exp int64 `json:"exp"`
}

func (s *Sessions) sign(payload []byte) string {
	m := hmac.New(sha256.New, s.Key)
	m.Write(payload)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Encode returns a signed token for the user.
func (s *Sessions) Encode(u *User) (string, error) {
	b, err := json.Marshal(claims{User: *u, Exp: s.now().Add(s.TTL).Unix()})
	if err != nil {
		return "", err
	}
	p := base64.RawURLEncoding.EncodeToString(b)
	return p + "." + s.sign([]byte(p)), nil
}

// Decode verifies a token.
func (s *Sessions) Decode(token string) (*User, error) {
	p, sig, ok := strings.Cut(token, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(s.sign([]byte(p)))) {
		return nil, errors.New("invalid session")
	}
	b, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return nil, errors.New("invalid session")
	}
	var c claims
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, errors.New("invalid session")
	}
	if s.now().Unix() > c.Exp {
		return nil, errors.New("session expired")
	}
	u := c.User
	return &u, nil
}

// SetCookie writes the session cookie.
func (s *Sessions) SetCookie(w http.ResponseWriter, u *User) error {
	tok, err := s.Encode(u)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.CookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.Secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(s.TTL.Seconds()),
	})
	return nil
}

// ClearCookie removes the session cookie.
func (s *Sessions) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: s.CookieName, Value: "", Path: "/", HttpOnly: true,
		Secure: s.Secure, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// FromRequest returns the user of a request's session cookie.
func (s *Sessions) FromRequest(r *http.Request) (*User, error) {
	c, err := r.Cookie(s.CookieName)
	if err != nil {
		return nil, err
	}
	return s.Decode(c.Value)
}

type ctxKey struct{}

// WithUser stores the user in the context.
func WithUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

// UserFrom returns the user stored in the context.
func UserFrom(ctx context.Context) *User {
	u, _ := ctx.Value(ctxKey{}).(*User)
	return u
}

// CSRFHeader must be present on state changing requests. Browsers can not
// send custom headers cross-origin without a CORS preflight, which we never
// allow, so its presence proves a same-origin request.
const CSRFHeader = "X-Requested-With"

// Middleware authenticates requests. With the "none" authenticator every
// request is treated as the configured user.
func Middleware(a Authenticator, s *Sessions, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var u *User
		if na, ok := a.(*NoneAuthenticator); ok {
			u = &User{Name: na.Username, Admin: true}
		} else {
			var err error
			if u, err = s.FromRequest(r); err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if r.Header.Get(CSRFHeader) == "" {
				writeError(w, http.StatusForbidden, "missing "+CSRFHeader+" header")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u)))
	})
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

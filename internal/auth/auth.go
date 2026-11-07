// Package auth authenticates console, API and MCP requests.
//
// Login methods (local password, OIDC providers) produce a signed session
// cookie that carries only the user name and expiry. Every request resolves
// the user again through a Resolver, so role changes and disabled users take
// effect immediately. Personal access tokens (Authorization: Bearer dvn_...)
// authenticate API and MCP clients.
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

// ErrInvalidCredentials is returned when login fails.
var ErrInvalidCredentials = errors.New("invalid credentials")

// CheckPassword compares credentials in constant time. An empty expected
// password never matches.
func CheckPassword(wantUser, wantPassword, user, password string) bool {
	uh := sha256.Sum256([]byte(user))
	ue := sha256.Sum256([]byte(wantUser))
	ph := sha256.Sum256([]byte(password))
	pe := sha256.Sum256([]byte(wantPassword))
	okU := subtle.ConstantTimeCompare(uh[:], ue[:])
	okP := subtle.ConstantTimeCompare(ph[:], pe[:])
	return okU&okP == 1 && wantPassword != ""
}

// Sessions issues and verifies HMAC signed cookies.
type Sessions struct {
	Key        []byte
	CookieName string
	TTL        time.Duration
	Secure     bool
	now        func() time.Time
}

// NewSessions creates a session manager. A random key is generated when key
// is empty, which invalidates sessions on restart.
func NewSessions(key []byte, secure bool) (*Sessions, error) {
	if len(key) == 0 {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
	}
	return &Sessions{Key: key, CookieName: "devenv_session", TTL: 7 * 24 * time.Hour, Secure: secure, now: time.Now}, nil
}

func (s *Sessions) clock() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// Session is the content of a session cookie.
type Session struct {
	User     string `json:"u"`
	Provider string `json:"p,omitempty"`
	Exp      int64  `json:"exp"`
}

func (s *Sessions) sign(purpose string, payload []byte) string {
	m := hmac.New(sha256.New, s.Key)
	m.Write([]byte(purpose))
	m.Write([]byte{0})
	m.Write(payload)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Seal signs a JSON value for purpose ("session", "oidc-state", ...).
func (s *Sessions) Seal(purpose string, v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	p := base64.RawURLEncoding.EncodeToString(b)
	return p + "." + s.sign(purpose, []byte(p)), nil
}

// Open verifies a sealed value.
func (s *Sessions) Open(purpose, token string, v any) error {
	p, sig, ok := strings.Cut(token, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(s.sign(purpose, []byte(p)))) {
		return errors.New("invalid signature")
	}
	b, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return errors.New("invalid encoding")
	}
	return json.Unmarshal(b, v)
}

// Encode returns a signed session token.
func (s *Sessions) Encode(user, provider string) (string, error) {
	return s.Seal("session", Session{User: user, Provider: provider, Exp: s.clock().Add(s.TTL).Unix()})
}

// Decode verifies a session token.
func (s *Sessions) Decode(token string) (*Session, error) {
	var c Session
	if err := s.Open("session", token, &c); err != nil || c.User == "" {
		return nil, errors.New("invalid session")
	}
	if s.clock().Unix() > c.Exp {
		return nil, errors.New("session expired")
	}
	return &c, nil
}

// SetCookie writes the session cookie.
func (s *Sessions) SetCookie(w http.ResponseWriter, user, provider string) error {
	tok, err := s.Encode(user, provider)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: s.CookieName, Value: tok, Path: "/", HttpOnly: true,
		Secure: s.Secure, SameSite: http.SameSiteLaxMode, MaxAge: int(s.TTL.Seconds()),
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

// FromRequest returns the session of a request.
func (s *Sessions) FromRequest(r *http.Request) (*Session, error) {
	c, err := r.Cookie(s.CookieName)
	if err != nil {
		return nil, err
	}
	return s.Decode(c.Value)
}

// Resolver turns a session or token into a principal.
type Resolver interface {
	// Session resolves a session user. Errors reject the request.
	Session(ctx context.Context, s *Session) (*Principal, error)
	// Token resolves a bearer token for scope ("api" or "mcp").
	Token(ctx context.Context, token, scope string) (*Principal, error)
}

// CSRFHeader must be present on state changing cookie requests. Browsers can
// not send custom headers cross-origin without a CORS preflight, which we
// never allow, so its presence proves a same-origin request.
const CSRFHeader = "X-Requested-With"

// Middleware authenticates requests with a session cookie or a bearer token
// (scope "api"). A non-nil Anonymous principal disables authentication.
type Middleware struct {
	Sessions  *Sessions
	Resolver  Resolver
	Anonymous *Principal
}

// Wrap returns the authenticating handler.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, cookie, err := m.authenticate(r)
		if err != nil {
			WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if cookie {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
			default:
				if r.Header.Get(CSRFHeader) == "" {
					WriteError(w, http.StatusForbidden, "missing "+CSRFHeader+" header")
					return
				}
			}
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

func (m *Middleware) authenticate(r *http.Request) (*Principal, bool, error) {
	if m.Anonymous != nil {
		p := *m.Anonymous
		return &p, false, nil
	}
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		p, err := m.Resolver.Token(r.Context(), strings.TrimSpace(tok), "api")
		return p, false, err
	}
	s, err := m.Sessions.FromRequest(r)
	if err != nil {
		return nil, true, err
	}
	p, err := m.Resolver.Session(r.Context(), s)
	return p, true, err
}

// WriteError writes a JSON error.
func WriteError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

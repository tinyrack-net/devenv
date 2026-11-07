package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCConfig configures one provider.
type OIDCConfig struct {
	ID           string
	Issuer       string
	ClientID     string
	ClientSecret string
	Scopes       []string
	// RedirectURL is the absolute callback URL.
	RedirectURL string
	Claims      ClaimNames
	// HTTPClient overrides the client used for discovery and token exchange.
	HTTPClient *http.Client
}

// ClaimNames maps profile fields to claim names.
type ClaimNames struct {
	Username, Email, Name, Groups string
}

// Identity is the verified result of an OIDC login.
type Identity struct {
	Provider      string
	Subject       string
	Username      string
	Email         string
	EmailVerified *bool
	Name          string
	Groups        []string
}

// OIDC runs the authorization code flow with PKCE for one provider.
// Discovery happens lazily and is retried until it succeeds, so an
// unreachable provider does not prevent the server from starting.
type OIDC struct {
	Config OIDCConfig

	mu       sync.Mutex
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	lastErr  time.Time
}

func (o *OIDC) ctx(ctx context.Context) context.Context {
	if o.Config.HTTPClient != nil {
		return oidc.ClientContext(ctx, o.Config.HTTPClient)
	}
	return ctx
}

func (o *OIDC) discover(ctx context.Context) (*oidc.Provider, *oidc.IDTokenVerifier, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.provider != nil {
		return o.provider, o.verifier, nil
	}
	if time.Since(o.lastErr) < 5*time.Second {
		return nil, nil, errors.New("identity provider unavailable, retry shortly")
	}
	dctx, cancel := context.WithTimeout(o.ctx(ctx), 10*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(dctx, o.Config.Issuer)
	if err != nil {
		o.lastErr = time.Now()
		return nil, nil, fmt.Errorf("discover %s: %w", o.Config.Issuer, err)
	}
	o.provider = p
	o.verifier = p.Verifier(&oidc.Config{ClientID: o.Config.ClientID})
	return p, o.verifier, nil
}

func (o *OIDC) oauth(p *oidc.Provider) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     o.Config.ClientID,
		ClientSecret: o.Config.ClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  o.Config.RedirectURL,
		Scopes:       o.Config.Scopes,
	}
}

// flowState is kept in a short lived signed cookie between redirect and callback.
type flowState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Return   string `json:"r,omitempty"`
	Exp      int64  `json:"exp"`
}

func randomString() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (o *OIDC) cookieName() string { return "devenv_oidc_" + o.Config.ID }

// SafeReturn keeps only same-origin relative paths.
func SafeReturn(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return "/"
	}
	if u, err := url.Parse(p); err != nil || u.Host != "" || u.Scheme != "" {
		return "/"
	}
	return p
}

// Start redirects the browser to the provider.
func (o *OIDC) Start(w http.ResponseWriter, r *http.Request, s *Sessions, returnTo string) error {
	p, _, err := o.discover(r.Context())
	if err != nil {
		return err
	}
	st := flowState{
		State: randomString(), Nonce: randomString(), Verifier: oauth2.GenerateVerifier(),
		Return: SafeReturn(returnTo), Exp: time.Now().Add(10 * time.Minute).Unix(),
	}
	sealed, err := s.Seal("oidc-"+o.Config.ID, st)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: o.cookieName(), Value: sealed, Path: "/api/v1/auth/oidc/" + o.Config.ID,
		HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteLaxMode, MaxAge: 600,
	})
	u := o.oauth(p).AuthCodeURL(st.State, oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier))
	http.Redirect(w, r, u, http.StatusFound)
	return nil
}

// Callback completes the flow and returns the verified identity and the
// path to return to.
func (o *OIDC) Callback(r *http.Request, w http.ResponseWriter, s *Sessions) (*Identity, string, error) {
	c, err := r.Cookie(o.cookieName())
	if err != nil {
		return nil, "", errors.New("login session expired; start again")
	}
	http.SetCookie(w, &http.Cookie{Name: o.cookieName(), Value: "", Path: "/api/v1/auth/oidc/" + o.Config.ID, MaxAge: -1, HttpOnly: true, Secure: s.Secure})
	var st flowState
	if err := s.Open("oidc-"+o.Config.ID, c.Value, &st); err != nil || time.Now().Unix() > st.Exp {
		return nil, "", errors.New("login session expired; start again")
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		return nil, "", fmt.Errorf("identity provider: %s %s", e, q.Get("error_description"))
	}
	if q.Get("state") == "" || q.Get("state") != st.State {
		return nil, "", errors.New("invalid state")
	}
	p, verifier, err := o.discover(r.Context())
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(o.ctx(r.Context()), 15*time.Second)
	defer cancel()
	cfg := o.oauth(p)
	tok, err := cfg.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		return nil, "", fmt.Errorf("token exchange: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		return nil, "", errors.New("no id_token in token response")
	}
	idt, err := verifier.Verify(ctx, raw)
	if err != nil {
		return nil, "", fmt.Errorf("verify id_token: %w", err)
	}
	if idt.Nonce != st.Nonce {
		return nil, "", errors.New("invalid nonce")
	}
	claims := map[string]any{}
	if err := idt.Claims(&claims); err != nil {
		return nil, "", err
	}
	// Merge userinfo (some providers only put groups or e-mail there).
	if ui, err := p.UserInfo(ctx, oauth2.StaticTokenSource(tok)); err == nil {
		extra := map[string]any{}
		if ui.Claims(&extra) == nil && extra["sub"] == idt.Subject {
			for k, v := range extra {
				if _, exists := claims[k]; !exists {
					claims[k] = v
				}
			}
		}
	}
	id := &Identity{
		Provider: o.Config.ID,
		Subject:  idt.Subject,
		Username: str(claims[o.Config.Claims.Username]),
		Email:    str(claims[o.Config.Claims.Email]),
		Name:     str(claims[o.Config.Claims.Name]),
		Groups:   strs(claims[o.Config.Claims.Groups]),
	}
	if v, ok := claims["email_verified"].(bool); ok {
		id.EmailVerified = &v
	}
	return id, st.Return, nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func strs(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		if t == "" {
			return nil
		}
		return strings.Fields(strings.ReplaceAll(t, ",", " "))
	}
	return nil
}

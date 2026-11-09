package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/tinyrack-net/devenv/internal/model"
	"github.com/tinyrack-net/devenv/internal/auth"
	"github.com/tinyrack-net/devenv/internal/tokens"
	"github.com/tinyrack-net/devenv/internal/users"
)

// Session implements auth.Resolver.
func (s *Server) Session(ctx context.Context, sess *auth.Session) (*auth.Principal, error) {
	return s.Users.Load(ctx, sess.User, "session")
}

// Token implements auth.Resolver: personal access tokens and the legacy
// global MCP token.
func (s *Server) Token(ctx context.Context, token, scope string) (*auth.Principal, error) {
	if strings.HasPrefix(token, tokens.Prefix) {
		t, err := s.Tokens.Verify(ctx, token)
		if err != nil {
			return nil, err
		}
		if !t.HasScope(scope) {
			return nil, errors.New("token lacks scope " + scope)
		}
		p, err := s.Users.Load(ctx, t.User, "token")
		if err != nil {
			return nil, err
		}
		p.TokenID, p.TokenName = t.ID, t.Name
		return p, nil
	}
	lt := s.Config.MCP.LegacyToken
	if scope == tokens.ScopeMCP && lt.IsEnabled() && lt.Token != "" && auth.CheckPassword("", lt.Token, "", token) {
		p, err := s.Users.Load(ctx, lt.User, "token")
		if err != nil {
			return nil, err
		}
		p.TokenName = "legacy MCP token"
		return p, nil
	}
	return nil, tokens.ErrInvalid
}

func (s *Server) info(w http.ResponseWriter, _ *http.Request) {
	out := Info{
		Version:   s.Version,
		Namespace: s.Namespace,
		SSH:       s.SSH,
		Auth:      AuthInfo{Disabled: s.Config.Auth.Disabled, Password: s.Config.Auth.Password.IsEnabled()},
		MCP:       s.Config.MCP.IsEnabled(),
		Providers: []ProviderInfo{},
	}
	for _, p := range s.Config.Auth.OIDC {
		out.Providers = append(out.Providers, ProviderInfo{ID: p.ID, Name: p.Name, LoginURL: "/api/v1/auth/oidc/" + p.ID + "/login"})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	out := Me{
		Name: p.Name, DisplayName: p.DisplayName, Email: p.Email, Admin: p.Admin,
		Groups: p.Groups, Method: p.Method, Provider: p.Provider,
		Identities: []IdentityInfo{}, Preferences: json.RawMessage("{}"),
	}
	if out.Groups == nil {
		out.Groups = []string{}
	}
	if u, err := s.Users.Get(r.Context(), p.Name); err == nil {
		for _, id := range u.Spec.Identities {
			out.Identities = append(out.Identities, IdentityInfo{Provider: id.Provider, Subject: id.Subject})
		}
		if len(u.Spec.Preferences) > 0 {
			out.Preferences = u.Spec.Preferences
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) updateMe(w http.ResponseWriter, r *http.Request) {
	var in UpdateMeRequest
	if !decode(w, r, &in) {
		return
	}
	u, err := s.Users.Get(r.Context(), principal(r).Name)
	if err != nil {
		fail(w, r, err)
		return
	}
	if in.DisplayName != nil {
		if len(*in.DisplayName) > 100 {
			fail(w, r, badRequest("display name too long"))
			return
		}
		u.Spec.DisplayName = strings.TrimSpace(*in.DisplayName)
	}
	if in.Preferences != nil {
		raw := []byte(*in.Preferences)
		var obj map[string]any
		if len(raw) > 16<<10 || json.Unmarshal(raw, &obj) != nil {
			fail(w, r, badRequest("preferences must be a JSON object under 16 KiB"))
			return
		}
		u.Spec.Preferences = json.RawMessage(raw)
	}
	if err := s.DB.UpdateUser(r.Context(), u); err != nil {
		fail(w, r, err)
		return
	}
	s.me(w, r)
}

// loginFailures slows down password guessing per client address.
var loginFailures = newRateLimiter(10, time.Minute)

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.Config.Auth.Password.IsEnabled() {
		writeError(w, http.StatusNotFound, "password login is disabled")
		return
	}
	ip := clientIP(r)
	if loginFailures.exceeded(ip) {
		writeError(w, http.StatusTooManyRequests, "too many failed login attempts, wait a minute")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	cfg := s.Config.Auth.Password
	if !auth.CheckPassword(cfg.Username, cfg.Password, req.Username, req.Password) {
		loginFailures.add(ip)
		time.Sleep(500 * time.Millisecond)
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	u, err := s.Users.EnsureLocalAdmin(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	if u.Spec.Disabled {
		writeError(w, http.StatusForbidden, "user is disabled")
		return
	}
	if err := s.Users.RecordLogin(r.Context(), u, "password", nil); err != nil {
		logf.FromContext(r.Context()).Error(err, "record login")
	}
	if err := s.Sessions.SetCookie(w, u.Name, "password"); err != nil {
		fail(w, r, err)
		return
	}
	p, _ := s.Users.Principal(u, "session")
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) logout(w http.ResponseWriter, _ *http.Request) {
	s.Sessions.ClearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) oidcLogin(w http.ResponseWriter, r *http.Request) {
	o := s.OIDC[r.PathValue("id")]
	if o == nil {
		writeError(w, http.StatusNotFound, "unknown provider")
		return
	}
	if err := o.Start(w, r, s.Sessions, r.URL.Query().Get("return")); err != nil {
		logf.FromContext(r.Context()).Error(err, "oidc start", "provider", o.Config.ID)
		loginErrorRedirect(w, r, err.Error())
	}
}

func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	o, cfg := s.OIDC[id], s.Config.Provider(id)
	if o == nil || cfg == nil {
		writeError(w, http.StatusNotFound, "unknown provider")
		return
	}
	log := logf.FromContext(r.Context()).WithValues("provider", id)
	ident, ret, err := o.Callback(r, w, s.Sessions)
	if err != nil {
		log.Info("oidc callback failed", "error", err.Error())
		loginErrorRedirect(w, r, err.Error())
		return
	}
	u, err := s.Users.LoginOIDC(r.Context(), cfg, users.Claims{
		Subject: ident.Subject, Username: ident.Username, Email: ident.Email,
		EmailVerified: ident.EmailVerified, Name: ident.Name, Groups: ident.Groups,
	})
	if err != nil {
		msg := err.Error()
		if !errors.Is(err, users.ErrNotAllowed) && !errors.Is(err, users.ErrDisabled) {
			log.Error(err, "oidc login")
			msg = "sign in failed"
		}
		loginErrorRedirect(w, r, msg)
		return
	}
	if err := s.Sessions.SetCookie(w, u.Name, id); err != nil {
		fail(w, r, err)
		return
	}
	log.Info("signed in", "user", u.Name)
	http.Redirect(w, r, ret, http.StatusFound)
}

func loginErrorRedirect(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/login?error="+url.QueryEscape(msg), http.StatusFound)
}

// --- helpers for user objects ---

func toIdentities(in []model.Identity) []IdentityInfo {
	out := make([]IdentityInfo, 0, len(in))
	for _, id := range in {
		out = append(out, IdentityInfo{Provider: id.Provider, Subject: id.Subject})
	}
	return out
}

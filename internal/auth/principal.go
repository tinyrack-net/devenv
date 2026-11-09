package auth

import "context"

// Principal is the authenticated caller of a request.
type Principal struct {
	// Name is the user name.
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Email       string `json:"email,omitempty"`
	Admin       bool   `json:"admin"`
	// Groups from the identity provider (last login).
	Groups []string `json:"groups,omitempty"`
	// Method is how the request authenticated: "session", "token" or "none".
	Method string `json:"method"`
	// Provider of the session ("password" or an OIDC provider id).
	Provider string `json:"provider,omitempty"`
	// TokenID and TokenName are set for token requests.
	TokenID   string `json:"tokenId,omitempty"`
	TokenName string `json:"tokenName,omitempty"`
}

// InGroup reports membership in any of groups.
func (p *Principal) InGroup(groups ...string) bool {
	for _, g := range groups {
		for _, h := range p.Groups {
			if g == h {
				return true
			}
		}
	}
	return false
}

type ctxKey struct{}

// WithPrincipal stores the principal in the context.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// From returns the principal stored in the context, or nil.
func From(ctx context.Context) *Principal {
	p, _ := ctx.Value(ctxKey{}).(*Principal)
	return p
}

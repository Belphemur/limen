// Package session owns the per-request identity plumbing shared by
// every SPA-facing Connect service (portal, admin, staff). It hosts:
//
//   - UserSession + Resolver: the cookie-decryption boundary, abstracted
//     so Connect handlers stay free of OIDC RP machinery.
//   - Context helpers: WithUser / UserFromContext / MustUser pin the
//     resolved identity onto ctx so handlers can read it without
//     re-decoding the cookie.
//   - Connect interceptors: TenancyInterceptor, Interceptor, and
//     RoleInterceptor compose into the standard tenancy → session →
//     role stack that PortalService, AdminService, and StaffService all
//     reuse.
//   - SessionService handler: the single shared RPC that every SPA
//     calls on boot.
//
// Crypto + cookie format live in internal/auth; this package only
// consumes the verified ID-token claims.
package session

import (
	"context"
	"net/http"

	"github.com/zitadel/oidc/v3/pkg/oidc"
)

// UserSession is the per-request identity blob the session interceptor
// pins on ctx. Sourced from the verified Zitadel ID token; never from
// Limen-side identity tables.
type UserSession struct {
	Subject   string
	Email     string
	FirstName string
	LastName  string
	Roles     []string
}

// Resolver turns the request's Cookie header into a verified session
// for the given tenant public id. Production wires this to
// auth.OIDC.ResolvePortalSession via OIDCResolver. Tests use a closure
// that returns canned sessions.
type Resolver func(ctx context.Context, header http.Header, tenantPublicID string) (*UserSession, *http.Cookie, error)

// OIDCAdapter narrows *auth.OIDC to the single method the session
// resolver needs so callers can drop in a fake without dragging RP
// discovery into tests.
type OIDCAdapter interface {
	ResolvePortalSession(ctx context.Context, header http.Header, tenant string) (*oidc.IDTokenClaims, *http.Cookie, error)
}

// OIDCResolver adapts an OIDCAdapter (production: *auth.OIDC) into a
// Resolver. The returned function shape keeps callers free of RP
// plumbing.
func OIDCResolver(o OIDCAdapter) Resolver {
	return func(ctx context.Context, header http.Header, tenantPublicID string) (*UserSession, *http.Cookie, error) {
		claims, setCookie, err := o.ResolvePortalSession(ctx, header, tenantPublicID)
		if err != nil {
			return nil, nil, err
		}
		return claimsToSession(claims), setCookie, nil
	}
}

// projectRolesClaim is the Zitadel-side wire constant carrying the
// project's role membership map. Kept here (not in internal/auth) so
// the session package owns the only consumer.
const projectRolesClaim = "urn:zitadel:iam:org:project:roles"

func claimsToSession(c *oidc.IDTokenClaims) *UserSession {
	if c == nil {
		return nil
	}
	first := c.GivenName
	last := c.FamilyName
	// Fallback: providers that only set the combined `name` claim get
	// best-effort split on first whitespace.
	if first == "" && last == "" && c.Name != "" {
		for i := 0; i < len(c.Name); i++ {
			if c.Name[i] == ' ' {
				first = c.Name[:i]
				last = c.Name[i+1:]
				break
			}
		}
		if first == "" {
			first = c.Name
		}
	}
	return &UserSession{
		Subject:   c.GetSubject(),
		Email:     c.Email,
		FirstName: first,
		LastName:  last,
		Roles:     extractRolesFromClaims(c),
	}
}

func extractRolesFromClaims(c *oidc.IDTokenClaims) []string {
	if c == nil || c.Claims == nil {
		return nil
	}
	raw, ok := c.Claims[projectRolesClaim].(map[string]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for role := range raw {
		out = append(out, role)
	}
	return out
}

// ctx keys are package-private; readers go through the typed helpers
// below.
type ctxKey int

const ctxKeyUser ctxKey = 1

// WithUser pins a verified UserSession on ctx. Only the session
// interceptor calls this in production.
func WithUser(ctx context.Context, u *UserSession) context.Context {
	return context.WithValue(ctx, ctxKeyUser, u)
}

// UserFromContext returns the verified user session pinned by the
// session interceptor.
func UserFromContext(ctx context.Context) (*UserSession, bool) {
	u, ok := ctx.Value(ctxKeyUser).(*UserSession)
	return u, ok && u != nil
}

// MustUser is the handler-side accessor: it panics if no session is
// pinned, which means the interceptor stack is misconfigured. Handlers
// served behind Interceptor never see this panic in production.
func MustUser(ctx context.Context) *UserSession {
	u, ok := UserFromContext(ctx)
	if !ok {
		panic("session: no user on ctx — interceptor stack misconfigured")
	}
	return u
}

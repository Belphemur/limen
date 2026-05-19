// Package portal implements the user-scoped portal Connect-RPC service
// mounted at /t/{tenant}/api/portal.v1.PortalService/*. The handler is
// driven by three unary interceptors in this order:
//
//  1. tenancyInterceptor    — pulls *storage.Tenant from ctx (set by
//     tenancy.RequireTenant HTTP middleware). Defense in depth: if it's
//     missing, the mount is misconfigured and we fail loudly with
//     CodeNotFound.
//  2. sessionInterceptor    — decrypts limen_portal, verifies the ID
//     token (with transparent refresh), and pins *User + roles on ctx.
//     Skipped for GetSession, which is the public RPC the SPA polls on
//     boot to discover whether it already has a session.
//  3. roleInterceptor       — looks up the required role for the RPC in
//     the static requiredRole table and enforces it against the roles
//     pulled out of the project-roles claim. Unknown methods default-deny.
//
// Tenant is NEVER read from the request payload. The proto enforces this
// at the IDL level via internal/portal/portalv1guard.
package portal

import (
	"context"
	"net/http"

	"github.com/zitadel/oidc/v3/pkg/oidc"
)

// UserSession is the per-request identity blob the interceptor stack
// pins on ctx. Sourced from the verified Zitadel ID token; never from
// Limen-side identity tables.
type UserSession struct {
	Subject string
	Email   string
	Name    string
	Roles   []string
}

// SessionResolver turns the request's Cookie header into a verified
// session for the given tenant public id. Production wires this to
// auth.OIDC.ResolvePortalSession via OIDCSessionResolver. Tests use a
// closure that returns canned sessions.
type SessionResolver func(ctx context.Context, header http.Header, tenantPublicID string) (*UserSession, *http.Cookie, error)

// OIDCSessionResolver adapts *auth.OIDC into a SessionResolver. The
// returned function shape lets the portal interceptor stay free of
// auth/RP plumbing.
func OIDCSessionResolver(o oidcResolver) SessionResolver {
	return func(ctx context.Context, header http.Header, tenantPublicID string) (*UserSession, *http.Cookie, error) {
		claims, setCookie, err := o.ResolvePortalSession(ctx, header, tenantPublicID)
		if err != nil {
			return nil, nil, err
		}
		return claimsToSession(claims), setCookie, nil
	}
}

// oidcResolver narrows *auth.OIDC to the single method portal needs so
// callers can drop in a fake without dragging RP discovery into tests.
type oidcResolver interface {
	ResolvePortalSession(ctx context.Context, header http.Header, tenant string) (*oidc.IDTokenClaims, *http.Cookie, error)
}

func claimsToSession(c *oidc.IDTokenClaims) *UserSession {
	if c == nil {
		return nil
	}
	name := c.Name
	if name == "" {
		first := c.GivenName
		last := c.FamilyName
		if first != "" || last != "" {
			name = first
			if last != "" {
				if name != "" {
					name += " "
				}
				name += last
			}
		}
	}
	roles := extractRolesFromClaims(c)
	return &UserSession{
		Subject: c.GetSubject(),
		Email:   c.Email,
		Name:    name,
		Roles:   roles,
	}
}

// extractRolesFromClaims mirrors auth.ExtractRoles but keeps the portal
// package free of an import on auth — the projectRolesClaim key is a
// Zitadel-side wire constant, not a Limen concept.
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

const projectRolesClaim = "urn:zitadel:iam:org:project:roles"

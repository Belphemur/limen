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
	"fmt"
	"net/http"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"
)

// UserSession is the per-request identity blob the session interceptor
// pins on ctx. Sourced from the verified Zitadel ID token; never from
// Limen-side identity tables.
type UserSession struct {
	Subject     string
	Email       string
	FirstName   string
	LastName    string
	Roles       []string
	AccessToken string
	// Impersonation fields (set when resolved from impersonation cookie)
	IsImpersonating bool
	ActorUserID     string
	ActorEmail      string
	ActorFirstName  string
	ActorLastName   string
	Reason          string
	TargetUserType  string
	ExpiresAt       time.Time
}

// Resolver turns the request's Cookie header into a verified session
// for the given tenant public id. Production wires this to
// auth.OIDC.ResolvePortalSession via OIDCResolver. Tests use a closure
// that returns canned sessions.
type Resolver func(ctx context.Context, header http.Header, tenantPublicID string) (*UserSession, *http.Cookie, error)

// OIDCAdapter narrows *auth.OIDC to the methods the session
// resolver needs so callers can drop in a fake without dragging RP
// discovery into tests.
type OIDCAdapter interface {
	ResolvePortalSession(ctx context.Context, header http.Header, tenant string) (*oidc.IDTokenClaims, string, *http.Cookie, error)
	ResolveImpersonationSession(ctx context.Context, header http.Header, tenant string) (*oidc.IDTokenClaims, string, *http.Cookie, error)
}

// OIDCResolver adapts an OIDCAdapter (production: *auth.OIDC) into a
// Resolver. The returned function shape keeps callers free of RP
// plumbing.
func OIDCResolver(o OIDCAdapter) Resolver {
	return func(ctx context.Context, header http.Header, tenantPublicID string) (*UserSession, *http.Cookie, error) {
		claims, accessToken, setCookie, err := o.ResolvePortalSession(ctx, header, tenantPublicID)
		if err != nil {
			return nil, nil, err
		}
		sess := claimsToSession(claims)
		sess.AccessToken = accessToken
		return sess, setCookie, nil
	}
}

// OIDCImpersonationResolver adapts an OIDCAdapter into a Resolver
// that reads the impersonation cookie. Callers should try this
// resolver first and fall back to OIDCResolver.
//
// It bypasses claimsToSession and builds UserSession directly from the
// raw claims map — synthetic claims don't need struct-field gymnastics.
func OIDCImpersonationResolver(o OIDCAdapter) Resolver {
	return func(ctx context.Context, header http.Header, tenantPublicID string) (*UserSession, *http.Cookie, error) {
		claims, accessToken, setCookie, err := o.ResolveImpersonationSession(ctx, header, tenantPublicID)
		if err != nil {
			return nil, nil, err
		}
		if claims == nil {
			return nil, nil, fmt.Errorf("impersonation resolver: nil claims")
		}

		first := stringClaim(claims, "given_name")
		last := stringClaim(claims, "family_name")
		if first == "" && last == "" {
			first, last = splitName(stringClaim(claims, "name"))
		}

		sess := &UserSession{
			Subject:     claims.GetSubject(),
			Email:       stringClaim(claims, "email"),
			FirstName:   first,
			LastName:    last,
			Roles:       extractRolesFromClaims(claims),
			AccessToken: accessToken,
		}
		if claims.Claims != nil {
			sess.IsImpersonating = true
			if v, ok := claims.Claims["actor_user_id"].(string); ok {
				sess.ActorUserID = v
			}
			if v, ok := claims.Claims["actor_email"].(string); ok {
				sess.ActorEmail = v
			}
			if v, ok := claims.Claims["actor_first_name"].(string); ok {
				sess.ActorFirstName = v
			}
			if v, ok := claims.Claims["actor_last_name"].(string); ok {
				sess.ActorLastName = v
			}
			if v, ok := claims.Claims["impersonation_reason"].(string); ok {
				sess.Reason = v
			}
			if v, ok := claims.Claims["target_user_type"].(string); ok {
				sess.TargetUserType = v
			}
			if v, ok := claims.Claims["impersonation_expires_at"].(string); ok {
				if t, err := time.Parse(time.RFC3339, v); err == nil {
					sess.ExpiresAt = t
				}
			}
		}
		return sess, setCookie, nil
	}
}

// stringClaim reads a string value from the claims map.
func stringClaim(c *oidc.IDTokenClaims, key string) string {
	if c == nil || c.Claims == nil {
		return ""
	}
	if v, ok := c.Claims[key].(string); ok {
		return v
	}
	return ""
}

// splitName splits a combined full name into first and last on the first
// space. If there is no space, the whole string becomes first.
func splitName(name string) (first, last string) {
	if name == "" {
		return "", ""
	}
	for i := 0; i < len(name); i++ {
		if name[i] == ' ' {
			return name[:i], name[i+1:]
		}
	}
	return name, ""
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

const (
	ctxKeyUser        ctxKey = 1
	ctxKeyAccessToken ctxKey = 2
)

// WithUser pins a verified UserSession on ctx. Only the session
// interceptor calls this in production.
func WithUser(ctx context.Context, u *UserSession) context.Context {
	ctx = context.WithValue(ctx, ctxKeyAccessToken, u.AccessToken)
	return context.WithValue(ctx, ctxKeyUser, u)
}

// AccessTokenFromContext returns the access token bound by WithUser.
func AccessTokenFromContext(ctx context.Context) (string, bool) {
	tok, ok := ctx.Value(ctxKeyAccessToken).(string)
	return tok, ok && tok != ""
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

// Package portal implements the user-scoped PortalService Connect-RPC
// service mounted at /t/{tenant}/api/limen.portal.v1.PortalService/*.
// The handler is driven by three unary interceptors composed from
// internal/session:
//
//  1. session.TenancyInterceptor — pulls *storage.Tenant from ctx (set
//     by tenancy.RequireTenant HTTP middleware).
//  2. session.Interceptor        — decrypts limen_portal, verifies the
//     ID token (with transparent refresh), and pins *UserSession on ctx.
//  3. session.RoleInterceptor    — looks up the required role for the
//     RPC in this package's requiredRole table.
//
// Session bootstrap (i.e. "who am I?") is owned by
// internal/session.SessionService, NOT by PortalService — see
// docs/phases/phase-09d-shared-session-service.md.
//
// Tenant is NEVER read from the request payload. The proto enforces
// this at the IDL level via internal/portal/portalv1guard.
package portal

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/portal/portalv1/portalv1connect"
	"github.com/belphemur/limen/internal/session"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/upstream"
)

// Service is the PortalServiceHandler implementation. Construction is
// deliberately constructor-style so the dependencies (store, resolver,
// logger) flow through the wiring layer (internal/boot/portalmount)
// rather than getting picked up from globals.
type Service struct {
	store                 *storage.Store
	upstream              *upstream.Service
	apps                  AppManager
	resolver              session.Resolver
	impersonationResolver session.Resolver
	bearerIntercept       connect.UnaryInterceptorFunc
	logger                *zap.Logger
}

// AppManager is the narrow ISP slice the portal needs from
// internal/zitadel.Client. Kept here so internal/portal doesn't take a
// hard dep on the Zitadel package — tests can pass a fake without
// pulling in the management SDK.
type AppManager interface {
	DeleteOIDCApp(ctx context.Context, orgID, projectID, appID string) error
}

// NewService builds the portal Connect-RPC service. resolver MUST
// verify the portal cookie against the Zitadel ID-token issuer;
// production wires this to auth.OIDC via session.OIDCResolver.
// impersonationResolver, when non-nil, is tried first so the
// interceptor stack reads the limen_portal_impersonate cookie before
// falling back to the normal portal cookie. apps may be nil in tests
// that don't exercise the MCP-client RPCs.
func NewService(store *storage.Store, upstreamSvc *upstream.Service, apps AppManager, resolver session.Resolver, impersonationResolver session.Resolver, bearerIntercept connect.UnaryInterceptorFunc, logger *zap.Logger) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Service{store: store, upstream: upstreamSvc, apps: apps, resolver: resolver, impersonationResolver: impersonationResolver, bearerIntercept: bearerIntercept, logger: logger}
}

// Handler returns the URL-path-prefix + http.Handler pair to register
// on a chi router behind tenancy.RequireTenant.
func (s *Service) Handler() (string, http.Handler) {
	interceptors := []connect.Interceptor{
		session.TenancyInterceptor(),
	}
	if s.bearerIntercept != nil {
		interceptors = append(interceptors, s.bearerIntercept)
	}
	interceptors = append(interceptors,
		session.Interceptor(s.resolver, s.impersonationResolver, s.logger),
		session.RoleInterceptor(requiredRole, s.logger),
	)
	return portalv1connect.NewPortalServiceHandler(s, connect.WithInterceptors(interceptors...))
}

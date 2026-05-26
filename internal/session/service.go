package session

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	sessionv1 "github.com/belphemur/limen/internal/session/sessionv1"
	"github.com/belphemur/limen/internal/session/sessionv1/sessionv1connect"
	"github.com/belphemur/limen/internal/tenancy"
)

// Service implements SessionServiceHandler. It is a passthrough over
// the values that TenancyInterceptor + Interceptor place in ctx — no
// Zitadel calls happen inside the handler itself; the cookie was
// already verified by the interceptor stack.
type Service struct {
	resolver              Resolver
	impersonationResolver Resolver
	logger                *zap.Logger
}

// NewService builds the shared SessionService handler. resolver MUST
// verify the portal cookie against the Zitadel ID-token issuer;
// production wires this to auth.OIDC via OIDCResolver.
// impersonationResolver, when non-nil, is tried first so the
// interceptor stack reads the limen_portal_impersonate cookie before
// falling back to the normal portal cookie.
func NewService(resolver Resolver, impersonationResolver Resolver, logger *zap.Logger) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Service{resolver: resolver, impersonationResolver: impersonationResolver, logger: logger}
}

// Handler returns the URL-path-prefix + http.Handler pair to register
// on a chi router behind tenancy.RequireTenant. The interceptor stack
// is intentionally only the two infrastructural interceptors — no
// RoleInterceptor — because SessionService is the one place where any
// authenticated user is the correct gate (the caller doesn't yet know
// what role it holds).
func (s *Service) Handler() (string, http.Handler) {
	return sessionv1connect.NewSessionServiceHandler(
		s,
		connect.WithInterceptors(
			TenancyInterceptor(),
			Interceptor(s.resolver, s.impersonationResolver, s.logger),
		),
	)
}

// GetSession returns the tenant + user + highest role pinned on ctx by
// the interceptor stack. Unauthenticated requests never reach this
// method — Interceptor short-circuits with CodeUnauthenticated.
func (s *Service) GetSession(ctx context.Context, _ *connect.Request[sessionv1.GetSessionRequest]) (*connect.Response[sessionv1.GetSessionResponse], error) {
	t := tenancy.MustTenant(ctx)
	u := MustUser(ctx)
	return connect.NewResponse(&sessionv1.GetSessionResponse{
		Tenant: &sessionv1.Tenant{
			PublicId: t.PublicID,
			Name:     t.Name,
		},
		User: &sessionv1.User{
			Id:        u.Subject,
			Email:     u.Email,
			FirstName: u.FirstName,
			LastName:  u.LastName,
		},
		Role: HighestRole(u.Roles),
	}), nil
}

package portal

import (
	"context"
	"net/http"
	"net/url"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/portal/portalv1"
	"github.com/belphemur/limen/internal/portal/portalv1/portalv1connect"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/upstream"
)

// Service is the PortalServiceHandler implementation. Construction is
// deliberately constructor-style so the dependencies (store, resolver,
// logger) flow through the wiring layer (internal/boot/portalmount)
// rather than getting picked up from globals.
type Service struct {
	store    *storage.Store
	upstream *upstream.Service
	apps     AppManager
	resolver SessionResolver
	logger   *zap.Logger
}

// AppManager is the narrow ISP slice the portal needs from
// internal/zitadel.Client. Kept here so internal/portal doesn't take a
// hard dep on the Zitadel package — tests can pass a fake without
// pulling in the management SDK.
type AppManager interface {
	DeleteOIDCApp(ctx context.Context, orgID, projectID, appID string) error
}

// NewService builds the portal Connect-RPC service. resolver MUST verify
// the portal cookie against the Zitadel ID token issuer; production
// wires this to auth.OIDC via OIDCSessionResolver. apps may be nil in
// tests that don't exercise the MCP client RPCs.
func NewService(store *storage.Store, upstreamSvc *upstream.Service, apps AppManager, resolver SessionResolver, logger *zap.Logger) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Service{store: store, upstream: upstreamSvc, apps: apps, resolver: resolver, logger: logger}
}

// Handler returns the URL-path-prefix + http.Handler pair to register
// on a chi router behind tenancy.RequireTenant.
func (s *Service) Handler() (string, http.Handler) {
	return portalv1connect.NewPortalServiceHandler(
		s,
		connect.WithInterceptors(
			tenancyInterceptor(),
			sessionInterceptor(s.resolver, s.logger),
			roleInterceptor(s.logger),
		),
	)
}

// GetSession is the only RPC the SPA may call without an established
// session. It returns either the unauthenticated shape carrying a
// login_url the SPA bounces the browser to, or the authenticated shape
// with the verified user + roles.
//
// The session interceptor skips this method, so we re-run the resolver
// here inside the handler: if the cookie is present and valid we return
// authenticated=true; otherwise we return the login URL.
func (s *Service) GetSession(ctx context.Context, req *connect.Request[portalv1.GetSessionRequest]) (*connect.Response[portalv1.GetSessionResponse], error) {
	t := tenancy.MustTenant(ctx)
	loginURL := "/t/" + t.PublicID + "/auth/login?return_to=" + url.QueryEscape("/portal")

	sess, setCookie, err := s.resolver(ctx, req.Header(), t.PublicID)
	if err != nil || sess == nil {
		return connect.NewResponse(&portalv1.GetSessionResponse{
			Authenticated: false,
			LoginUrl:      loginURL,
		}), nil
	}
	resp := connect.NewResponse(&portalv1.GetSessionResponse{
		Authenticated: true,
		User: &portalv1.User{
			Subject: sess.Subject,
			Email:   sess.Email,
			Name:    sess.Name,
		},
		Roles: sess.Roles,
	})
	if setCookie != nil {
		resp.Header().Add("Set-Cookie", setCookie.String())
	}
	return resp, nil
}

// The remaining RPCs are stubbed in slice 2 and filled in by slices 3-4.
// (Upstream RPCs land in slice 3 / upstreams.go; MCP-client RPCs land
// in slice 4 / mcpclients.go.)

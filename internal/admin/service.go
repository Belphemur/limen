// Package admin implements the admin/owner-scoped AdminService
// Connect-RPC handler mounted at
// /t/{tenant}/api/limen.admin.v1.AdminService/*.
//
// The handler is driven by the standard SPA-facing interceptor stack
// from internal/session:
//
//  1. session.TenancyInterceptor — pulls *storage.Tenant from ctx
//     (set by tenancy.RequireTenant HTTP middleware).
//  2. session.Interceptor        — decrypts limen_portal, verifies the
//     ID token, pins *UserSession on ctx.
//  3. session.RoleInterceptor    — looks up the minimum role for the
//     RPC in this package's requiredRole table; default-deny.
//
// Public, tenant-agnostic signup is NOT a method on AdminService —
// see internal/signup for limen.signup.v1.SignupService. Skip-listing
// would mis-shape this interceptor stack.
//
// Tenant is NEVER read from the request payload. See
// proto/limen/admin/v1/admin.proto.
//
// Slice 1 of phase 9c lands the surface area only — every RPC returns
// CodeUnimplemented. Subsequent slices implement them against real
// product logic.
package admin

import (
	"net/http"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/admin/adminv1/adminv1connect"
	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/session"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenant"
	"github.com/belphemur/limen/internal/upstream"
)

// Service is the AdminServiceHandler implementation.
type Service struct {
	store            *storage.Store
	upstream         *upstream.Service
	tenant           *tenant.Service
	resolver         session.Resolver
	members          MemberDirectory
	serviceAccounts  ServiceAccountDirectory
	zitadelDomain    string
	zitadelProjectID string
	cipher           *crypto.Cipher
	secureCookie     bool
	logger           *zap.Logger
}

// NewService builds the admin Connect-RPC service. resolver MUST
// verify the portal cookie against the Zitadel ID-token issuer.
// members is the Zitadel directory pass-through used by the
// ListMembers/InviteMember/UpdateMemberRole/RemoveMember RPCs; when
// nil those RPCs return CodeUnimplemented.
// serviceAccounts is the Zitadel directory pass-through used by the
// service account RPCs; when nil those RPCs return CodeUnimplemented.
func NewService(store *storage.Store, upstreamSvc *upstream.Service, tenantSvc *tenant.Service, resolver session.Resolver, members MemberDirectory, serviceAccounts ServiceAccountDirectory, zitadelDomain, zitadelProjectID string, cipher *crypto.Cipher, secureCookie bool, logger *zap.Logger) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Service{
		store:            store,
		upstream:         upstreamSvc,
		tenant:           tenantSvc,
		resolver:         resolver,
		members:          members,
		serviceAccounts:  serviceAccounts,
		zitadelDomain:    zitadelDomain,
		zitadelProjectID: zitadelProjectID,
		cipher:           cipher,
		secureCookie:     secureCookie,
		logger:           logger,
	}
}

// Handler returns the URL-path-prefix + http.Handler pair to mount on
// a chi router behind tenancy.RequireTenant.
func (s *Service) Handler() (string, http.Handler) {
	return adminv1connect.NewAdminServiceHandler(
		s,
		connect.WithInterceptors(
			session.TenancyInterceptor(),
			session.Interceptor(s.resolver, s.logger),
			session.RoleInterceptor(requiredRole, s.logger),
		),
	)
}

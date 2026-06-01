package stripe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/config"
	"github.com/belphemur/limen/internal/portal/portalv1"
	"github.com/belphemur/limen/internal/portal/portalv1/portalv1connect"
	"github.com/belphemur/limen/internal/session"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
)

// requiredRole maps the RPC procedure name to the minimum role required.
// All billing RPCs are owner-only.
var requiredRole = map[string]session.Role{
	"GetBillingSummary":     session.RoleOwner,
	"CreateCheckoutSession": session.RoleOwner,
	"OpenCustomerPortal":    session.RoleOwner,
}

// Service is the BillingServiceHandler implementation.
type Service struct {
	store           *storage.Store
	client          *Client
	cfg             config.BillingConfig
	resolver        session.Resolver
	bearerIntercept connect.UnaryInterceptorFunc
	logger          *zap.Logger
}

// NewService builds the billing Connect-RPC service.
func NewService(store *storage.Store, client *Client, cfg config.BillingConfig, resolver session.Resolver, bearerIntercept connect.UnaryInterceptorFunc, logger *zap.Logger) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Service{
		store:           store,
		client:          client,
		cfg:             cfg,
		resolver:        resolver,
		bearerIntercept: bearerIntercept,
		logger:          logger,
	}
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
		session.Interceptor(s.resolver, s.logger),
		session.RoleInterceptor(requiredRole, s.logger),
	)
	return portalv1connect.NewBillingServiceHandler(s, connect.WithInterceptors(interceptors...))
}

// GetBillingSummary returns the current billing state for the tenant.
func (s *Service) GetBillingSummary(ctx context.Context, req *connect.Request[portalv1.GetBillingSummaryRequest]) (*connect.Response[portalv1.GetBillingSummaryResponse], error) {
	tenant, ok := tenancy.TenantFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("tenant not found"))
	}

	db, commit, err := s.store.Session(ctx)
	if err != nil {
		s.logger.Error("GetBillingSummary session failed", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("internal error"))
	}
	defer func() { _ = commit() }()

	var billing storage.TenantBilling
	if err := db.Where("tenant_id = ?", tenant.ID).First(&billing).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.Error("GetBillingSummary query failed", zap.Error(err))
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("internal error"))
		}
		// No row — return developer defaults.
		return connect.NewResponse(&portalv1.GetBillingSummaryResponse{
			Plan:                    "developer",
			Status:                  "none",
			StripePublishableKey:    s.cfg.Stripe.PublishableKey,
			ActiveUserCount:         0,
			ActiveSaConnectionCount: 0,
		}), nil
	}

	resp := &portalv1.GetBillingSummaryResponse{
		Plan:                    billing.Plan,
		Status:                  billing.Status,
		ActiveUserCount:         billing.ActiveUserCount,
		ActiveSaConnectionCount: billing.ActiveSAConnectionCount,
		StripePublishableKey:    s.cfg.Stripe.PublishableKey,
		CancelAtPeriodEnd:       billing.CancelAtPeriodEnd,
	}
	if billing.CurrentPeriodEnd != nil {
		resp.CurrentPeriodEnd = billing.CurrentPeriodEnd.Format(time.RFC3339)
	}
	if billing.GraceUntil != nil {
		resp.GraceUntil = billing.GraceUntil.Format(time.RFC3339)
	}
	return connect.NewResponse(resp), nil
}

// CreateCheckoutSession opens a Stripe Checkout session for upgrading to the Team plan.
func (s *Service) CreateCheckoutSession(ctx context.Context, req *connect.Request[portalv1.CreateCheckoutSessionRequest]) (*connect.Response[portalv1.CreateCheckoutSessionResponse], error) {
	tenant, ok := tenancy.TenantFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("tenant not found"))
	}

	db, commit, err := s.store.Session(ctx)
	if err != nil {
		s.logger.Error("CreateCheckoutSession session failed", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("internal error"))
	}
	defer func() { _ = commit() }()

	// Ensure billing row exists.
	var billing storage.TenantBilling
	if err := db.Where("tenant_id = ?", tenant.ID).First(&billing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			billing = storage.TenantBilling{
				TenantID: tenant.ID,
				Plan:     "developer",
				Status:   "none",
			}
			if err := db.Create(&billing).Error; err != nil {
				s.logger.Error("CreateCheckoutSession create billing row failed", zap.Error(err))
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("internal error"))
			}
		} else {
			s.logger.Error("CreateCheckoutSession query failed", zap.Error(err))
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("internal error"))
		}
	}

	// Ensure Stripe customer exists.
	existingCustomerID := ""
	if billing.StripeCustomerID != nil {
		existingCustomerID = *billing.StripeCustomerID
	}
	customerID, err := s.client.EnsureCustomer(ctx, tenant.PublicID, existingCustomerID)
	if err != nil {
		s.logger.Error("CreateCheckoutSession ensure customer failed", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("stripe error"))
	}

	if billing.StripeCustomerID == nil {
		billing.StripeCustomerID = &customerID
		if err := db.Where("tenant_id = ?", tenant.ID).Save(&billing).Error; err != nil {
			s.logger.Error("CreateCheckoutSession save customer id failed", zap.Error(err))
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("internal error"))
		}
	}

	// Query current month metrics.
	monthStart := time.Now().UTC().Format("2006-01-02")
	var activeUsers int64
	if err := db.Raw("SELECT COUNT(DISTINCT user_id) FROM active_user_months WHERE tenant_id = ? AND month_start = ? AND deleted_at IS NULL", tenant.ID, monthStart).Scan(&activeUsers).Error; err != nil {
		s.logger.Error("CreateCheckoutSession active user count failed", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("internal error"))
	}

	var saConnections int64
	if err := db.Raw("SELECT COUNT(*) FROM sa_connection_snapshots WHERE tenant_id = ? AND connected = true AND deleted_at IS NULL AND (disconnected_at IS NULL OR disconnected_at > NOW())", tenant.ID).Scan(&saConnections).Error; err != nil {
		s.logger.Error("CreateCheckoutSession sa connection count failed", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("internal error"))
	}

	redirectURL, err := s.client.CreateCheckoutSession(ctx, customerID, req.Msg.ReturnTo, int32(activeUsers), int32(saConnections))
	if err != nil {
		s.logger.Error("CreateCheckoutSession stripe call failed", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("stripe error"))
	}

	return connect.NewResponse(&portalv1.CreateCheckoutSessionResponse{RedirectUrl: redirectURL}), nil
}

// OpenCustomerPortal creates a Stripe Customer Portal session.
func (s *Service) OpenCustomerPortal(ctx context.Context, req *connect.Request[portalv1.OpenCustomerPortalRequest]) (*connect.Response[portalv1.OpenCustomerPortalResponse], error) {
	tenant, ok := tenancy.TenantFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("tenant not found"))
	}

	db, commit, err := s.store.Session(ctx)
	if err != nil {
		s.logger.Error("OpenCustomerPortal session failed", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("internal error"))
	}
	defer func() { _ = commit() }()

	var billing storage.TenantBilling
	if err := db.Where("tenant_id = ?", tenant.ID).First(&billing).Error; err != nil {
		s.logger.Error("OpenCustomerPortal query failed", zap.Error(err))
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no stripe customer"))
	}

	if billing.StripeCustomerID == nil || *billing.StripeCustomerID == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no stripe customer"))
	}

	redirectURL, err := s.client.CreatePortalSession(ctx, *billing.StripeCustomerID, req.Msg.ReturnTo)
	if err != nil {
		s.logger.Error("OpenCustomerPortal stripe call failed", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("stripe error"))
	}

	return connect.NewResponse(&portalv1.OpenCustomerPortalResponse{RedirectUrl: redirectURL}), nil
}

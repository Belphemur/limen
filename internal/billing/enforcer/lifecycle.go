package enforcer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/config"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
)

// GraceHeader is the response header set when a Team plan tenant
// is past_due / unpaid but still inside the configured grace window.
// The portal SPA reads this to render a non-blocking warning banner.
const GraceHeader = "X-Limen-Billing"

// GraceHeaderValue is the canonical value paired with GraceHeader.
const GraceHeaderValue = "grace"

// StaffPublicID is the reserved tenant public id that the staff
// backoffice resolves to. The billing lifecycle middleware passes
// requests for this tenant through unconditionally — staff is never
// gated by subscription state.
const StaffPublicID = "_staff"

// lifecycleDecision is the verdict evaluateBillingStatus returns.
// A pure function turning (status, graceUntil, now) into one of four
// outcomes keeps the middleware body flat and trivially testable.
//
// decisionPassUnknown is the fail-open verdict for statuses the state
// machine does not recognise (e.g. a future Stripe enum). The caller
// distinguishes it from decisionPass so it can log a warning — we'd
// rather let a tenant through on a typo than lock them out silently.
type lifecycleDecision int

const (
	decisionPass lifecycleDecision = iota
	decisionPassGrace
	decisionBlock
	decisionPassUnknown
)

// RequireBillingActive returns a chi middleware that gates the request
// path on the tenant's subscription lifecycle state. The middleware is
// a no-op when:
//
//   - billing is disabled in config (self-host path),
//   - the resolved tenant is the staff backoffice,
//   - no tenant_billing row exists yet (e.g. brand-new signup).
//
// All other requests pass / pass-with-grace-header / block per the
// state machine in evaluateBillingStatus. Blocks return 402 Payment
// Required — the portal SPA renders the "your subscription expired"
// page on that response.
//
// Mount AFTER tenancy.RequireTenant (needs the tenant in ctx) and
// AFTER auth (the value-generating path is gated, not the public
// PRM / discovery endpoints). Lives in this package because the
// gateway binary forbids importing internal/billing — see the
// import-graph test in cmd/gateway.
func RequireBillingActive(store *storage.Store, cfg config.BillingConfig, logger *zap.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !cfg.Enabled {
				next.ServeHTTP(w, r)
				return
			}
			t := tenancy.MustTenant(r.Context())
			if t.PublicID == StaffPublicID {
				next.ServeHTTP(w, r)
				return
			}

			billing, err := loadTenantBilling(r.Context(), store, t.ID)
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					next.ServeHTTP(w, r)
					return
				}
				logger.Warn("billing lifecycle: load tenant_billing failed, passing through",
					zap.Int64("tenant_id", t.ID), zap.Error(err))
				next.ServeHTTP(w, r)
				return
			}

			switch evaluateBillingStatus(billing.Status, billing.GraceUntil, time.Now()) {
			case decisionPassGrace:
				w.Header().Set(GraceHeader, GraceHeaderValue)
				next.ServeHTTP(w, r)
			case decisionBlock:
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusPaymentRequired)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "payment required"})
			case decisionPassUnknown:
				logger.Warn("billing lifecycle: unknown status, passing through",
					zap.Int64("tenant_id", t.ID),
					zap.String("status", billing.Status))
				next.ServeHTTP(w, r)
			default:
				next.ServeHTTP(w, r)
			}
		})
	}
}

// loadTenantBilling fetches the tenant_billing row for the current
// tenant via the app pool (the request already carries a tenant pin
// from RequireTenant, so RLS scopes the query for us).
func loadTenantBilling(ctx context.Context, store *storage.Store, tenantID int64) (*storage.TenantBilling, error) {
	db, commit, err := store.Session(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = commit() }()
	var row storage.TenantBilling
	if err := db.Where("tenant_id = ?", tenantID).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// evaluateBillingStatus is the pure function at the heart of the
// lifecycle gate. Kept separate from the middleware body so the
// status / grace-window matrix is exhaustively table-tested without
// standing up a database or a chi router.
//
// Status values follow Stripe's subscription.status enum verbatim
// plus a synthetic "none" for tenants that have never subscribed
// (typically the Developer plan path — those rows simply don't
// exist and the caller short-circuits before reaching this fn).
func evaluateBillingStatus(status string, graceUntil *time.Time, now time.Time) lifecycleDecision {
	switch status {
	case "none", "trialing", "active":
		return decisionPass
	case "past_due", "unpaid":
		if graceUntil != nil && now.Before(*graceUntil) {
			return decisionPassGrace
		}
		return decisionBlock
	case "canceled", "incomplete", "incomplete_expired", "paused":
		return decisionBlock
	default:
		// Unknown status — fail-open but flag it. The caller logs
		// a warning so an unrecognised Stripe enum (typo, new
		// release) never silently locks a tenant out.
		return decisionPassUnknown
	}
}

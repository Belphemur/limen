package enforcer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
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

// DeveloperPlan is the canonical plan name for the free tier. Written
// to tenant_billing.plan by the auto-downgrade path and on the
// initial row for tenants that have never subscribed.
const DeveloperPlan = "developer"

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
// The verdicts for "canceled" and "past_due"/"unpaid" with an expired
// or absent grace window now return decisionPass (they used to block).
// Before forwarding the request the middleware triggers a one-time
// Developer downgrade via enforcer + DowngradeToDeveloper so the
// tenant's subsequent requests resolve to developer limits. The
// downgrade is best-effort: any error is logged and the request still
// proceeds (we'd rather serve one stale request than 402 a paying
// customer on a transient DB blip).
//
// Mount AFTER tenancy.RequireTenant (needs the tenant in ctx) and
// AFTER auth (the value-generating path is gated, not the public
// PRM / discovery endpoints). Lives in this package because the
// gateway binary forbids importing internal/billing — see the
// import-graph test in cmd/gateway.
//
// enforcer may be nil when billing is disabled; the short-circuits at
// the top of the handler never touch it. Passing a real *Enforcer is
// required when cfg.Enabled is true so the downgrade path can
// invalidate the entitlement cache after commit.
func RequireBillingActive(store *storage.Store, enforcer *Enforcer, cfg config.BillingConfig, logger *zap.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			result := checkBillingStatus(r.Context(), store, cfg, logger)
			if !billingCheckSentinel(result) {
				next.ServeHTTP(w, r)
				return
			}

			switch result.decision {
			case decisionPassGrace:
				w.Header().Set(GraceHeader, GraceHeaderValue)
				next.ServeHTTP(w, r)
			case decisionBlock:
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusPaymentRequired)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "payment required"})
			case decisionPassUnknown:
				logger.Warn("billing lifecycle: unknown status, passing through",
					zap.Int64("tenant_id", result.tenant.ID),
					zap.String("status", result.billing.Status))
				next.ServeHTTP(w, r)
			default: // decisionPass — including the auto-downgrade cases.
				if enforcer != nil && isAutoDowngradeStatus(result.billing.Status) && result.billing.Plan != DeveloperPlan {
					autoDowngradeTenant(r.Context(), store, enforcer, result.tenant.ID, logger)
				}
				next.ServeHTTP(w, r)
			}
		})
	}
}

// billingCheck is the structured outcome of a single billing-status
// check. tenant is non-nil whenever the request actually reached a
// tenant-scoped code path (billing enabled, tenancy middleware ran);
// billing is non-nil only when a tenant_billing row was found and the
// state machine produced a verdict. When either is nil the caller
// must pass the request through untouched.
type billingCheck struct {
	decision lifecycleDecision
	tenant   *storage.Tenant
	billing  *storage.TenantBilling
}

// billingCheckSentinel reports whether a billingCheck carries enough
// state to act on. Returns false when billing was disabled, the
// staff tenant was resolved, or no tenant_billing row exists — all
// of which the calling middleware turns into a transparent
// next.ServeHTTP passthrough.
func billingCheckSentinel(c billingCheck) bool {
	return c.tenant != nil && c.billing != nil
}

// checkBillingStatus is the shared core both lifecycle middlewares
// (RequireBillingActive for the portal / Connect-RPC path and
// RequireBillingActiveMCP for the MCP transport) route their state
// machine through. Pulling this out keeps the two callers in lock-step:
// a new verdict, a new short-circuit, or a new load-failure rule
// applies to both surfaces without copy-paste drift.
//
// Short-circuits (tenant == nil or billing == nil) all return
// decisionPass because the caller treats them as "nothing to
// evaluate, pass through". The decisionPassUnknown verdict is the
// only non-passthrough outcome that doesn't require billing != nil —
// we get it from the state machine, so a row must have loaded for
// the verdict to exist, but the helper still works the same way
// for the caller (sentinel == false ⇒ no row, passthrough).
func checkBillingStatus(ctx context.Context, store *storage.Store, cfg config.BillingConfig, logger *zap.Logger) billingCheck {
	if !cfg.Enabled {
		return billingCheck{decision: decisionPass}
	}
	t := tenancy.MustTenant(ctx)
	if t.PublicID == StaffPublicID {
		return billingCheck{decision: decisionPass, tenant: t}
	}

	billing, err := loadTenantBilling(ctx, store, t.ID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return billingCheck{decision: decisionPass, tenant: t}
		}
		logger.Warn("billing lifecycle: load tenant_billing failed, passing through",
			zap.Int64("tenant_id", t.ID), zap.Error(err))
		return billingCheck{decision: decisionPass, tenant: t}
	}

	return billingCheck{
		decision: evaluateBillingStatus(billing.Status, billing.GraceUntil, time.Now()),
		tenant:   t,
		billing:  billing,
	}
}

// RequireBillingActiveMCP is the MCP transport's billing gate. It
// shares checkBillingStatus with RequireBillingActive so both surfaces
// evaluate the same lifecycle state machine, but the wire shape of
// every outcome is JSON-RPC 2.0 instead of HTTP — MCP clients do not
// inspect HTTP status codes, only the in-band error / notification
// payloads:
//
//   - decisionBlock       → HTTP 200 with a JSON-RPC error response
//     (code -32000, message links to {portalOrigin}/billing). The
//     HTTP layer stays 200 because the failure is at the application
//     layer, not the transport layer.
//   - decisionPassGrace   → request is forwarded; a JSON-RPC
//     `notifications/billing_warning` is appended to the response
//     body so the calling LLM can surface a soft hint to the user.
//     Implemented by buffering the handler's response and committing
//     original + notification at the end of the chain. SSE is
//     already excluded by the transport-level route split — this
//     middleware only wraps POST endpoints.
//   - decisionPass / PassUnknown → pure passthrough. No notification,
//     no downgrade side-effect from this middleware (the auto-downgrade
//     still fires for the canceled / expired-grace subcase so the
//     next per-tool-call entitlement check resolves Developer limits).
//
// portalOrigin is embedded in both the block message and the
// notification params so the user knows where to update payment.
// Empty portalOrigin produces a message with no link — the warning
// still surfaces, but without a clickable target.
//
// Mount after RequireMCPAuth (identity must be established) and after
// tenancy.RequireTenant (the helper calls tenancy.MustTenant).
// Must NOT be used for the portal Connect-RPC routes — they expect
// the 402 Payment Required HTTP shape produced by RequireBillingActive.
func RequireBillingActiveMCP(store *storage.Store, enforcer *Enforcer, cfg config.BillingConfig, portalOrigin string, logger *zap.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			result := checkBillingStatus(r.Context(), store, cfg, logger)
			if !billingCheckSentinel(result) {
				next.ServeHTTP(w, r)
				return
			}

			switch result.decision {
			case decisionPassGrace:
				// Auto-downgrade so the next per-tool-call entitlement
				// check resolves Developer limits. Same best-effort
				// contract as the portal middleware.
				if enforcer != nil && isAutoDowngradeStatus(result.billing.Status) && result.billing.Plan != DeveloperPlan {
					autoDowngradeTenant(r.Context(), store, enforcer, result.tenant.ID, logger)
				}
				notification := mcpBillingWarningNotification(portalOrigin)
				bw := newJSONRPCBufferingWriter(w)
				next.ServeHTTP(bw, r)
				if err := bw.commit(notification); err != nil {
					logger.Warn("billing mcp: commit buffered response with warning failed",
						zap.Int64("tenant_id", result.tenant.ID), zap.Error(err))
				}
			case decisionBlock:
				// Same auto-downgrade contract — the request is being
				// rejected with an in-band error, but the tenant's
				// entitlement row should still be flipped to Developer
				// so a recovery flow can take over.
				if enforcer != nil && isAutoDowngradeStatus(result.billing.Status) && result.billing.Plan != DeveloperPlan {
					autoDowngradeTenant(r.Context(), store, enforcer, result.tenant.ID, logger)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(mcpBillingBlockBody(portalOrigin))
			case decisionPassUnknown:
				logger.Warn("billing mcp: unknown status, passing through",
					zap.Int64("tenant_id", result.tenant.ID),
					zap.String("status", result.billing.Status))
				next.ServeHTTP(w, r)
			default: // decisionPass
				if enforcer != nil && isAutoDowngradeStatus(result.billing.Status) && result.billing.Plan != DeveloperPlan {
					autoDowngradeTenant(r.Context(), store, enforcer, result.tenant.ID, logger)
				}
				next.ServeHTTP(w, r)
			}
		})
	}
}

// mcpBillingBlockBody renders the JSON-RPC error response sent on
// decisionBlock. The HTTP layer already wrote 200 + application/json
// headers; this is just the body. The error code -32000 is in the
// JSON-RPC 2.0 server-error range (-32000 to -32099).
func mcpBillingBlockBody(portalOrigin string) []byte {
	body := map[string]any{
		"jsonrpc": "2.0",
		"error": map[string]any{
			"code":    -32000,
			"message": mcpBillingBlockMessage(portalOrigin),
		},
		"id": nil,
	}
	buf, _ := json.Marshal(body)
	return buf
}

// mcpBillingBlockMessage composes the human-readable portion of the
// block error. The URL is omitted (the colon is kept) when
// portalOrigin is empty so the message degrades gracefully.
func mcpBillingBlockMessage(portalOrigin string) string {
	if portalOrigin == "" {
		return "billing: subscription past due"
	}
	return "billing: subscription past due — visit " + portalOrigin + "/billing to update payment"
}

// mcpBillingWarningNotification renders the JSON-RPC notification
// appended on decisionPassGrace. The MCP notification shape is
// {jsonrpc, method, params}; the client LLM is expected to surface
// the params.message to the user.
func mcpBillingWarningNotification(portalOrigin string) []byte {
	body := map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/billing_warning",
		"params": map[string]any{
			"message": mcpBillingWarningMessage(portalOrigin),
		},
	}
	buf, _ := json.Marshal(body)
	return buf
}

// mcpBillingWarningMessage is the soft-hint copy shown to the user.
// Like the block message, the link is omitted when portalOrigin is
// empty.
func mcpBillingWarningMessage(portalOrigin string) string {
	if portalOrigin == "" {
		return "Your subscription payment is past due."
	}
	return "Your subscription payment is past due. Visit " + portalOrigin + "/billing"
}

// jsonRPCBufferingWriter is a minimal http.ResponseWriter wrapper
// that captures the handler's response so the billing middleware
// can append a JSON-RPC notification after the handler returns.
// Go's net/http commits the response on the first Write/WriteHeader
// call, so writing more bytes after ServeHTTP returns is only
// possible if we buffer.
//
// Only the subset of http.ResponseWriter the MCP POST handlers
// actually need is implemented. SSE — which uses http.Flusher — is
// excluded by the route split in internal/transport/mcprs.go so
// the lack of Flush/Flush impls here is intentional: any handler
// that tries to stream a body through this wrapper will simply
// buffer until commit, which is the behaviour the billing
// notification append needs anyway.
type jsonRPCBufferingWriter struct {
	http.ResponseWriter
	header      http.Header
	body        bytes.Buffer
	status      int
	wroteHeader bool
}

func newJSONRPCBufferingWriter(w http.ResponseWriter) *jsonRPCBufferingWriter {
	return &jsonRPCBufferingWriter{
		ResponseWriter: w,
		header:         make(http.Header),
		status:         http.StatusOK,
	}
}

// Header returns the buffered header map. Modifications land in the
// underlying ResponseWriter only on commit.
func (b *jsonRPCBufferingWriter) Header() http.Header { return b.header }

// WriteHeader captures the status code without flushing. Calling
// WriteHeader more than once is a no-op — matches net/http's
// behaviour and protects the buffered response from late status
// mutations.
func (b *jsonRPCBufferingWriter) WriteHeader(statusCode int) {
	if b.wroteHeader {
		return
	}
	b.status = statusCode
	b.wroteHeader = true
}

// Write appends to the buffered body. A first call before
// WriteHeader implicitly writes a 200 status, matching net/http.
func (b *jsonRPCBufferingWriter) Write(p []byte) (int, error) {
	if !b.wroteHeader {
		b.WriteHeader(http.StatusOK)
	}
	return b.body.Write(p)
}

// commit writes the buffered response to the underlying writer and
// then appends extra. extra is the JSON-RPC notification bytes on
// the billing-grace path; nil otherwise. A nil write error from
// the underlying writer is treated as success — there's nothing
// the middleware can do about a closed connection here.
func (b *jsonRPCBufferingWriter) commit(extra []byte) error {
	dst := b.ResponseWriter.Header()
	maps.Copy(dst, b.header)
	b.ResponseWriter.WriteHeader(b.status)
	if _, err := b.ResponseWriter.Write(b.body.Bytes()); err != nil {
		return err
	}
	if len(extra) > 0 {
		_, err := b.ResponseWriter.Write(extra)
		return err
	}
	return nil
}

// isAutoDowngradeStatus reports whether a decisionPass verdict should
// trigger the one-time Developer downgrade. The set is exactly the
// Stripe statuses the state machine used to return decisionBlock for
// before the lifecycle was reworked: "canceled" is always a
// downgrade, and "past_due" / "unpaid" are downgrades when the state
// machine has already established their grace window has expired (the
// within-grace cases go to decisionPassGrace and never reach here).
//
// Happy "active" / "trialing" / "none" tenants do not appear — they
// should never be downgraded by a request-time middleware; Stripe
// webhooks drive their plan transitions.
func isAutoDowngradeStatus(status string) bool {
	switch status {
	case "canceled", "past_due", "unpaid":
		return true
	}
	return false
}

// autoDowngradeTenant resets the tenant to the Developer plan in a
// single transaction and invalidates the entitlement cache so the
// next request resolves DeveloperEntitlements() rather than the stale
// Team snapshot. Best-effort: every error path is logged and the
// caller still serves the request — a transient DB blip should not
// turn into a 402 for a paying customer.
//
// Step ordering matters: DowngradeToDeveloper must run inside the
// session, then commit (rollback on tx.Error), then Invalidate. If
// Invalidate fails the downgrade has still committed and the next
// reload will re-resolve correctly via the DB fallback.
func autoDowngradeTenant(ctx context.Context, store *storage.Store, enf *Enforcer, tenantID int64, logger *zap.Logger) {
	tx, commit, err := store.Session(ctx)
	if err != nil {
		logger.Warn("billing lifecycle: open session for auto-downgrade failed",
			zap.Int64("tenant_id", tenantID), zap.Error(err))
		return
	}
	if err := DowngradeToDeveloper(tx, tenantID); err != nil {
		logger.Warn("billing lifecycle: auto-downgrade failed, passing through",
			zap.Int64("tenant_id", tenantID), zap.Error(err))
		_ = commit() // rolls back if the gorm tx set Error
		return
	}
	if err := commit(); err != nil {
		logger.Warn("billing lifecycle: commit auto-downgrade failed, passing through",
			zap.Int64("tenant_id", tenantID), zap.Error(err))
		return
	}
	if err := enf.Invalidate(ctx, tenantID); err != nil {
		logger.Warn("billing lifecycle: invalidate cache after auto-downgrade failed",
			zap.Int64("tenant_id", tenantID), zap.Error(err))
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
//
// Verdict map (post reworking for the auto-downgrade path):
//
//	canceled            → decisionPass         (was decisionBlock)
//	past_due/unpaid     → decisionPassGrace    (within grace)
//	past_due/unpaid     → decisionPass         (grace expired/nil; was decisionBlock)
//	incomplete/paused   → decisionBlock        (hard block — Stripe mid-flow, not "gone")
//	none/trialing/active→ decisionPass
//	(unknown)           → decisionPassUnknown
func evaluateBillingStatus(status string, graceUntil *time.Time, now time.Time) lifecycleDecision {
	switch status {
	case "none", "trialing", "active":
		return decisionPass
	case "canceled":
		// Subscription cancelled (either at period end or
		// immediately). The state machine used to 402 here; the
		// middleware now auto-downgrades to Developer and lets
		// the request through.
		return decisionPass
	case "past_due", "unpaid":
		if graceUntil != nil && now.Before(*graceUntil) {
			return decisionPassGrace
		}
		// Grace window expired (or never set) — also auto-downgrade
		// and pass through. The grace comparison is strict-less-than,
		// so now == graceUntil is treated as "expired" and returns
		// decisionPass.
		return decisionPass
	case "incomplete", "incomplete_expired", "paused":
		// Stripe mid-flow states we don't auto-recover from — the
		// tenant needs to finish checkout (incomplete), the
		// subscription has lapsed permanently (incomplete_expired),
		// or Stripe has paused it (paused). 402 stays.
		return decisionBlock
	default:
		// Unknown status — fail-open but flag it. The caller logs
		// a warning so an unrecognised Stripe enum (typo, new
		// release) never silently locks a tenant out.
		return decisionPassUnknown
	}
}

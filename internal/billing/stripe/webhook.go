package stripe

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/stripe/stripe-go/v82"
	"go.uber.org/zap"
	"gorm.io/gorm/clause"

	"github.com/belphemur/limen/internal/billing/enforcer"
	"github.com/belphemur/limen/internal/billing/entitlements"
	"github.com/belphemur/limen/internal/config"
	"github.com/belphemur/limen/internal/ids"
	"github.com/belphemur/limen/internal/storage"
)

var teamFeatures = map[string]struct{}{
	"advanced-ai":                   {},
	"audit-logs":                    {},
	"sso":                           {},
	"priority-support":              {},
	"max-user_unlimited":            {},
	"max-service-account-unlimited": {},
	"max-sa-connection-unlimited":   {},
	"max-upstream-link-unlimited":   {},
	"audit-retention_90d":           {},
}

// WebhookHandler handles incoming Stripe webhook events.
// Signature verification via stripe.ConstructEvent.
// Uses async drain: enqueue to in-memory channel, ACK Stripe immediately,
// background goroutine mutates Postgres.
type WebhookHandler struct {
	store        *storage.Store
	secret       string
	logger       *zap.Logger
	cfg          config.BillingConfig
	enforcer     *enforcer.Enforcer
	events       chan stripe.Event
	drainDone    chan struct{}
	stop         chan struct{}
	processedIDs sync.Map // map[string]time.Time — event ID → when processed
}

// NewWebhookHandler constructs a webhook handler. Call StartDrain once
// after construction and StopDrain on shutdown.
func NewWebhookHandler(store *storage.Store, secret string, cfg config.BillingConfig, enf *enforcer.Enforcer, logger *zap.Logger) *WebhookHandler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &WebhookHandler{
		store:     store,
		secret:    secret,
		logger:    logger,
		cfg:       cfg,
		enforcer:  enf,
		events:    make(chan stripe.Event, 100),
		drainDone: make(chan struct{}),
		stop:      make(chan struct{}),
	}
}

// ServeHTTP verifies the webhook signature, enqueues the event, and
// returns 200 immediately. Stripe expects < 5s response.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		h.logger.Warn("stripe webhook: read body failed", zap.Error(err))
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	event, err := stripe.ConstructEvent(body, r.Header.Get("Stripe-Signature"), h.secret)
	if err != nil {
		h.logger.Warn("stripe webhook: signature verification failed", zap.Error(err))
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	select {
	case h.events <- event:
		w.WriteHeader(http.StatusOK)
	default:
		h.logger.Warn("stripe webhook: event channel full, returning 503", zap.String("type", string(event.Type)))
		http.Error(w, "service overloaded", http.StatusServiceUnavailable)
	}
}

// StartDrain begins the background goroutine that processes webhook
// events. Call once after construction.
func (h *WebhookHandler) StartDrain() {
	go func() {
		defer close(h.drainDone)
		for {
			select {
			case event := <-h.events:
				h.handleEvent(context.Background(), event)
			case <-h.stop:
				for {
					select {
					case event := <-h.events:
						h.handleEvent(context.Background(), event)
					default:
						return
					}
				}
			}
		}
	}()
}

// StopDrain signals the drain goroutine to finish and waits for it.
func (h *WebhookHandler) StopDrain() {
	close(h.stop)
	<-h.drainDone
}

func (h *WebhookHandler) isDuplicate(eventID string) bool {
	if _, loaded := h.processedIDs.LoadOrStore(eventID, time.Now()); loaded {
		return true
	}
	return false
}

func (h *WebhookHandler) handleEvent(ctx context.Context, event stripe.Event) {
	if h.isDuplicate(event.ID) {
		h.logger.Debug("stripe webhook: skipping duplicate event", zap.String("event_id", event.ID))
		return
	}

	h.logger.Debug("stripe webhook: handling event", zap.String("type", string(event.Type)))

	switch event.Type {
	case stripe.EventTypeCheckoutSessionCompleted:
		h.handleCheckoutSessionCompleted(ctx, event)
	case stripe.EventTypeCustomerSubscriptionUpdated:
		h.handleSubscriptionUpdated(ctx, event)
	case stripe.EventTypeCustomerSubscriptionDeleted:
		h.handleSubscriptionDeleted(ctx, event)
	case stripe.EventTypeInvoicePaymentFailed:
		h.handleInvoicePaymentFailed(ctx, event)
	case stripe.EventTypeInvoicePaymentSucceeded:
		h.handleInvoicePaymentSucceeded(ctx, event)
	case "entitlements.active_entitlement_summary.updated":
		h.handleEntitlementsUpdated(ctx, event)
	default:
		h.logger.Debug("stripe webhook: unhandled event type", zap.String("type", string(event.Type)))
	}
}

func (h *WebhookHandler) handleCheckoutSessionCompleted(ctx context.Context, event stripe.Event) {
	var sess stripe.CheckoutSession
	if err := json.Unmarshal(event.Data.Raw, &sess); err != nil {
		h.logger.Warn("stripe webhook: failed to unmarshal checkout session", zap.Error(err))
		return
	}

	tenantPublicID := sess.ClientReferenceID
	if tenantPublicID == "" {
		h.logger.Warn("stripe webhook: checkout.session.completed missing client_reference_id")
		return
	}

	if _, err := ids.MustParse(ids.PrefixTenant, tenantPublicID); err != nil {
		h.logger.Warn("stripe webhook: invalid tenant public id", zap.String("public_id", tenantPublicID), zap.Error(err))
		return
	}

	tx, commit, err := h.store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		h.logger.Error("stripe webhook: checkout.session.completed session failed", zap.Error(err))
		return
	}
	defer func() {
		if err := commit(); err != nil {
			h.logger.Error("stripe webhook: commit failed", zap.Error(err), zap.String("event_type", string(event.Type)))
		}
	}()

	var tenant storage.Tenant
	if err := tx.Where("public_id = ?", tenantPublicID).First(&tenant).Error; err != nil {
		h.logger.Error("stripe webhook: tenant lookup failed", zap.String("public_id", tenantPublicID), zap.Error(err))
		return
	}

	billing := storage.TenantBilling{
		TenantID:             tenant.ID,
		StripeCustomerID:     stripe.String(sess.Customer.ID),
		StripeSubscriptionID: stripe.String(sess.Subscription.ID),
		Plan:                 "team",
		Status:               "active",
	}

	// Derive initial status from the subscription object when available.
	if sess.Subscription != nil && sess.Subscription.Status == stripe.SubscriptionStatusTrialing {
		billing.Status = "trialing"
	}

	// Extract price IDs from line items by matching against configured IDs.
	if sess.LineItems != nil {
		for _, item := range sess.LineItems.Data {
			if item.Price == nil {
				continue
			}
			switch item.Price.ID {
			case h.cfg.Products.TeamActiveUserPriceID:
				billing.StripeActiveUserPriceID = stripe.String(item.Price.ID)
			case h.cfg.Products.TeamSAConnectionPriceID:
				billing.StripeSAConnectionPriceID = stripe.String(item.Price.ID)
			}
		}
	}

	err = tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tenant_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"stripe_customer_id", "stripe_subscription_id", "status", "plan",
			"stripe_active_user_price_id", "stripe_sa_connection_price_id",
		}),
	}).Create(&billing).Error
	if err != nil {
		h.logger.Error("stripe webhook: failed to persist billing row", zap.Error(err))
	}
}

func (h *WebhookHandler) handleSubscriptionUpdated(ctx context.Context, event stripe.Event) {
	var sub stripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
		h.logger.Warn("stripe webhook: failed to unmarshal subscription", zap.Error(err))
		return
	}

	tx, commit, err := h.store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		h.logger.Error("stripe webhook: subscription.updated session failed", zap.Error(err))
		return
	}
	var billing storage.TenantBilling
	defer func() {
		if err := commit(); err != nil {
			h.logger.Error("stripe webhook: commit failed", zap.Error(err), zap.String("event_type", string(event.Type)))
			return
		}
		// Skip cache invalidation when the billing row was never resolved
		// (early return on lookup failure) — there's nothing new to cache.
		if billing.TenantID == 0 || h.enforcer == nil {
			return
		}
		if err := h.enforcer.Invalidate(ctx, billing.TenantID); err != nil {
			h.logger.Warn("stripe webhook: failed to invalidate entitlement cache",
				zap.Int64("tenant_id", billing.TenantID), zap.Error(err))
		}
	}()

	if err := tx.Where("stripe_customer_id = ?", sub.Customer.ID).First(&billing).Error; err != nil {
		h.logger.Warn("stripe webhook: billing row not found for customer", zap.String("customer", sub.Customer.ID), zap.Error(err))
		return
	}

	billing.Status = string(sub.Status)
	if sub.Status == stripe.SubscriptionStatusCanceled {
		// Mirror the helper's DB writes onto the in-memory struct so the
		// Save() below doesn't clobber them with the pre-cancelation state.
		billing.Plan = "developer"
		billing.GraceUntil = nil
		if err := enforcer.DowngradeToDeveloper(tx, billing.TenantID); err != nil {
			h.logger.Error("stripe webhook: failed to downgrade to developer", zap.Error(err))
			// Do not Save() the mirrored in-memory state on failure —
			// the entitlement rows are still on Team in the DB, so
			// flipping the plan column here would leave the tenant in
			// an inconsistent state. The deferred commit will roll the
			// transaction back; the next webhook will retry cleanly.
			return
		}
	}
	if sub.Items != nil && len(sub.Items.Data) > 0 && sub.Items.Data[0].CurrentPeriodEnd > 0 {
		t := time.Unix(sub.Items.Data[0].CurrentPeriodEnd, 0).UTC()
		billing.CurrentPeriodEnd = &t
	}
	billing.CancelAtPeriodEnd = sub.CancelAtPeriodEnd

	if sub.Items != nil {
		for _, item := range sub.Items.Data {
			if item.Price == nil {
				continue
			}
			if billing.StripeActiveUserPriceID == nil {
				billing.StripeActiveUserPriceID = stripe.String(item.Price.ID)
			} else if billing.StripeSAConnectionPriceID == nil {
				billing.StripeSAConnectionPriceID = stripe.String(item.Price.ID)
			}
		}
	}

	if err := tx.Where("tenant_id = ?", billing.TenantID).Save(&billing).Error; err != nil {
		h.logger.Error("stripe webhook: failed to update billing row", zap.Error(err))
	}
}

func (h *WebhookHandler) handleSubscriptionDeleted(ctx context.Context, event stripe.Event) {
	var sub stripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
		h.logger.Warn("stripe webhook: failed to unmarshal subscription", zap.Error(err))
		return
	}

	tx, commit, err := h.store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		h.logger.Error("stripe webhook: subscription.deleted session failed", zap.Error(err))
		return
	}
	var billing storage.TenantBilling
	defer func() {
		if err := commit(); err != nil {
			h.logger.Error("stripe webhook: commit failed", zap.Error(err), zap.String("event_type", string(event.Type)))
			return
		}
		// Skip cache invalidation when the billing row was never resolved
		// (early return on lookup failure) — there's nothing new to cache.
		if billing.TenantID == 0 || h.enforcer == nil {
			return
		}
		if err := h.enforcer.Invalidate(ctx, billing.TenantID); err != nil {
			h.logger.Warn("stripe webhook: failed to invalidate entitlement cache",
				zap.Int64("tenant_id", billing.TenantID), zap.Error(err))
		}
	}()

	if err := tx.Where("stripe_customer_id = ?", sub.Customer.ID).First(&billing).Error; err != nil {
		h.logger.Warn("stripe webhook: billing row not found for customer", zap.String("customer", sub.Customer.ID), zap.Error(err))
		return
	}

	billing.Status = "canceled"
	// Mirror the helper's DB writes onto the in-memory struct so the
	// Save() below doesn't clobber them with the pre-cancelation state.
	billing.Plan = "developer"
	billing.GraceUntil = nil
	if err := enforcer.DowngradeToDeveloper(tx, billing.TenantID); err != nil {
		h.logger.Error("stripe webhook: failed to downgrade to developer", zap.Error(err))
		// Do not Save() the mirrored in-memory state on failure —
		// the entitlement rows are still on Team in the DB, so
		// flipping the plan column here would leave the tenant in
		// an inconsistent state. The deferred commit will roll the
		// transaction back; the next webhook will retry cleanly.
		return
	}
	billing.StripeSubscriptionID = nil
	billing.StripeActiveUserPriceID = nil
	billing.StripeSAConnectionPriceID = nil

	if err := tx.Where("tenant_id = ?", billing.TenantID).Save(&billing).Error; err != nil {
		h.logger.Error("stripe webhook: failed to update billing row", zap.Error(err))
	}
}

func (h *WebhookHandler) handleInvoicePaymentFailed(ctx context.Context, event stripe.Event) {
	var inv stripe.Invoice
	if err := json.Unmarshal(event.Data.Raw, &inv); err != nil {
		h.logger.Warn("stripe webhook: failed to unmarshal invoice", zap.Error(err))
		return
	}

	tx, commit, err := h.store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		h.logger.Error("stripe webhook: invoice.payment_failed session failed", zap.Error(err))
		return
	}
	defer func() {
		if err := commit(); err != nil {
			h.logger.Error("stripe webhook: commit failed", zap.Error(err), zap.String("event_type", string(event.Type)))
		}
	}()

	var billing storage.TenantBilling
	if err := tx.Where("stripe_customer_id = ?", inv.Customer.ID).First(&billing).Error; err != nil {
		h.logger.Warn("stripe webhook: billing row not found for customer", zap.String("customer", inv.Customer.ID), zap.Error(err))
		return
	}

	graceUntil := time.Now().UTC().Add(time.Duration(h.cfg.GraceDays) * 24 * time.Hour)
	billing.GraceUntil = &graceUntil
	billing.Status = string(stripe.SubscriptionStatusPastDue)

	if err := tx.Where("tenant_id = ?", billing.TenantID).Save(&billing).Error; err != nil {
		h.logger.Error("stripe webhook: failed to update billing row", zap.Error(err))
	}
}

func (h *WebhookHandler) handleInvoicePaymentSucceeded(ctx context.Context, event stripe.Event) {
	var inv stripe.Invoice
	if err := json.Unmarshal(event.Data.Raw, &inv); err != nil {
		h.logger.Warn("stripe webhook: failed to unmarshal invoice", zap.Error(err))
		return
	}

	tx, commit, err := h.store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		h.logger.Error("stripe webhook: invoice.payment_succeeded session failed", zap.Error(err))
		return
	}
	defer func() {
		if err := commit(); err != nil {
			h.logger.Error("stripe webhook: commit failed", zap.Error(err), zap.String("event_type", string(event.Type)))
		}
	}()

	var billing storage.TenantBilling
	if err := tx.Where("stripe_customer_id = ?", inv.Customer.ID).First(&billing).Error; err != nil {
		h.logger.Warn("stripe webhook: billing row not found for customer", zap.String("customer", inv.Customer.ID), zap.Error(err))
		return
	}

	billing.GraceUntil = nil

	if err := tx.Where("tenant_id = ?", billing.TenantID).Save(&billing).Error; err != nil {
		h.logger.Error("stripe webhook: failed to update billing row", zap.Error(err))
	}
}

type entitlementEvent struct {
	Data struct {
		Object struct {
			Customer     string `json:"customer"`
			Entitlements []struct {
				LookupKey string `json:"lookup_key"`
				IsEnabled bool   `json:"is_enabled"`
			} `json:"entitlements"`
		} `json:"object"`
	} `json:"data"`
}

func (h *WebhookHandler) handleEntitlementsUpdated(ctx context.Context, event stripe.Event) {
	var raw entitlementEvent
	if err := json.Unmarshal(event.Data.Raw, &raw); err != nil {
		h.logger.Warn("stripe webhook: failed to unmarshal entitlement event", zap.Error(err))
		return
	}

	customerID := raw.Data.Object.Customer
	if customerID == "" {
		h.logger.Warn("stripe webhook: entitlement event missing customer")
		return
	}

	tx, commit, err := h.store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		h.logger.Error("stripe webhook: entitlements session failed", zap.Error(err))
		return
	}
	defer func() {
		if err := commit(); err != nil {
			h.logger.Error("stripe webhook: commit failed", zap.Error(err), zap.String("event_type", string(event.Type)))
		}
	}()

	var billing storage.TenantBilling
	if err := tx.Where("stripe_customer_id = ?", customerID).First(&billing).Error; err != nil {
		h.logger.Warn("stripe webhook: billing row not found for customer", zap.String("customer", customerID), zap.Error(err))
		return
	}

	if err := tx.Unscoped().Delete(&storage.TenantEntitlement{}, "tenant_id = ?", billing.TenantID).Error; err != nil {
		h.logger.Error("stripe webhook: failed to delete old entitlements", zap.Error(err))
		return
	}

	hasTeamFeature := false

	for _, ent := range raw.Data.Object.Entitlements {
		limit := entitlements.EntitlementLimitFromLookupKey(ent.LookupKey, ent.IsEnabled)
		if limit == 0 && !ent.IsEnabled {
			continue
		}
		row := storage.TenantEntitlement{
			TenantID:   billing.TenantID,
			Feature:    ent.LookupKey,
			LimitValue: limit,
		}
		if err := tx.Create(&row).Error; err != nil {
			h.logger.Error("stripe webhook: failed to create entitlement", zap.String("feature", ent.LookupKey), zap.Error(err))
			continue
		}
		if _, ok := teamFeatures[ent.LookupKey]; ok {
			hasTeamFeature = true
		}
	}

	if hasTeamFeature {
		billing.Plan = "team"
	} else {
		billing.Plan = "developer"
	}
	if err := tx.Where("tenant_id = ?", billing.TenantID).Save(&billing).Error; err != nil {
		h.logger.Error("stripe webhook: failed to update billing plan", zap.Error(err))
		return
	}

	// Invalidate entitlement cache so the next request picks up new entitlements.
	if h.enforcer != nil {
		if err := h.enforcer.Invalidate(ctx, billing.TenantID); err != nil {
			h.logger.Warn("stripe webhook: failed to invalidate entitlement cache",
				zap.Int64("tenant_id", billing.TenantID), zap.Error(err))
		}
	}
}

//go:build integration

package stripe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v82"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/billing/enforcer"
	"github.com/belphemur/limen/internal/billing/entitlements"
	"github.com/belphemur/limen/internal/config"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/storage/storagetest"
	"github.com/belphemur/limen/internal/valkey"
)

// testCacheKeyPrefix mirrors the prefix in internal/billing/enforcer/cache.go
// (cacheKeyPrefix). The enforcer doesn't expose this constant, but the
// format is stable — changing it would be a deliberate API break.
const testCacheKeyPrefix = "limen:billing:entitlements:"

// newTestHandler builds a WebhookHandler backed by a freshly-migrated
// Postgres (via testcontainers), an in-memory valkey, and a no-op logger.
// Each call gets its own Store so tests are fully isolated.
func newTestHandler(t *testing.T, graceDays int) (*WebhookHandler, *storage.Store, *enforcer.Enforcer, valkey.Client) {
	t.Helper()
	store := storagetest.OpenMigrated(t)
	vk := valkey.NewInMemory()
	enf := enforcer.New(store, vk, zap.NewNop())
	h := NewWebhookHandler(store, "whsec_test", config.BillingConfig{GraceDays: graceDays}, enf, zap.NewNop())
	return h, store, enf, vk
}

// seedBilling creates a Tenant + TenantBilling row with the given fields
// and returns the internal tenant ID. Each test uses a unique customerID
// (and therefore a unique ZitadelOrgID) so the unique index is satisfied.
func seedBilling(t *testing.T, store *storage.Store, customerID, plan, status string, grace *time.Time) int64 {
	t.Helper()
	db := store.RawDB()
	tenant := &storage.Tenant{
		Name:         "test-" + customerID,
		ZitadelOrgID: "zorg_" + customerID,
	}
	if err := db.Create(tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	customer := customerID
	billing := &storage.TenantBilling{
		TenantID:         tenant.ID,
		StripeCustomerID: &customer,
		Status:           status,
		Plan:             plan,
		GraceUntil:       grace,
	}
	if err := db.Create(billing).Error; err != nil {
		t.Fatalf("create tenant_billing: %v", err)
	}
	return tenant.ID
}

// seedEntitlements inserts tenant_entitlements rows so the downgrade path
// has something concrete to hard-delete.
func seedEntitlements(t *testing.T, store *storage.Store, tenantID int64, features ...string) {
	t.Helper()
	db := store.RawDB()
	for _, feat := range features {
		row := &storage.TenantEntitlement{
			TenantID:   tenantID,
			Feature:    feat,
			LimitValue: -1,
		}
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("create entitlement %s: %v", feat, err)
		}
	}
}

// loadBilling reads the tenant_billing row back from the DB.
func loadBilling(t *testing.T, store *storage.Store, tenantID int64) storage.TenantBilling {
	t.Helper()
	var b storage.TenantBilling
	if err := store.RawDB().Where("tenant_id = ?", tenantID).First(&b).Error; err != nil {
		t.Fatalf("load tenant_billing: %v", err)
	}
	return b
}

// entitlementCount returns the number of (unscoped) tenant_entitlements
// rows for the tenant — used to assert the downgrade hard-deletes them.
func entitlementCount(t *testing.T, store *storage.Store, tenantID int64) int64 {
	t.Helper()
	var n int64
	if err := store.RawDB().Unscoped().Model(&storage.TenantEntitlement{}).
		Where("tenant_id = ?", tenantID).Count(&n).Error; err != nil {
		t.Fatalf("count entitlements: %v", err)
	}
	return n
}

// cacheKeyFor returns the valkey key the enforcer uses to cache a tenant's
// entitlements. See internal/billing/enforcer/cache.go.
func cacheKeyFor(tenantID int64) string {
	return fmt.Sprintf("%s%d", testCacheKeyPrefix, tenantID)
}

// seedEnforcerCache writes a known stale value into the enforcer's cache
// for the given tenant, so tests can assert Invalidate wipes it.
func seedEnforcerCache(t *testing.T, vk valkey.Client, tenantID int64, value entitlements.PlanEntitlements) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal cache value: %v", err)
	}
	if err := vk.SetEX(context.Background(), cacheKeyFor(tenantID), raw, time.Minute); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
}

// buildEvent wraps a Stripe resource in a stripe.Event with a unique ID
// and the requested event type.
func buildEvent(t *testing.T, eventType stripe.EventType, id string, payload any) stripe.Event {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal %s payload: %v", eventType, err)
	}
	return stripe.Event{
		ID:   id,
		Type: eventType,
		Data: &stripe.EventData{Raw: raw},
	}
}

// --- 1. handleSubscriptionUpdated with status = canceled ---

// TestHandleSubscriptionUpdated_Canceled verifies that a
// customer.subscription.updated event with status="canceled" runs the
// DowngradeToDeveloper path: Plan flips to "developer", GraceUntil is
// cleared, and the tenant_entitlements rows are hard-deleted so the
// next cache load resolves the developer defaults.
func TestHandleSubscriptionUpdated_Canceled(t *testing.T) {
	h, store, _, _ := newTestHandler(t, 14)
	customerID := "cus_sub_updated_canceled"
	tenantID := seedBilling(t, store, customerID, "team", "active", nil)
	seedEntitlements(t, store, tenantID, "advanced-ai", "sso")

	event := buildEvent(t,
		stripe.EventTypeCustomerSubscriptionUpdated,
		"evt_sub_updated_canceled",
		stripe.Subscription{
			Customer: &stripe.Customer{ID: customerID},
			Status:   stripe.SubscriptionStatusCanceled,
		},
	)

	h.handleSubscriptionUpdated(context.Background(), event)

	b := loadBilling(t, store, tenantID)
	if b.Plan != enforcer.DeveloperPlan {
		t.Errorf("Plan = %q, want %q", b.Plan, enforcer.DeveloperPlan)
	}
	if b.GraceUntil != nil {
		t.Errorf("GraceUntil = %v, want nil", *b.GraceUntil)
	}
	if b.Status != string(stripe.SubscriptionStatusCanceled) {
		t.Errorf("Status = %q, want %q", b.Status, string(stripe.SubscriptionStatusCanceled))
	}
	if n := entitlementCount(t, store, tenantID); n != 0 {
		t.Errorf("entitlements remaining after downgrade: %d, want 0", n)
	}
}

// --- 2. handleSubscriptionDeleted ---

// TestHandleSubscriptionDeleted verifies that a
// customer.subscription.deleted event downgrades the tenant (same as the
// cancel case), clears the subscription/price IDs, and invalidates the
// enforcer's entitlement cache.
func TestHandleSubscriptionDeleted(t *testing.T) {
	h, store, _, vk := newTestHandler(t, 14)
	customerID := "cus_sub_deleted"
	tenantID := seedBilling(t, store, customerID, "team", "active", nil)
	seedEntitlements(t, store, tenantID, "audit-logs")

	// Pre-seed the enforcer's valkey cache with a known stale value so
	// we can directly assert the Invalidate side effect. Using MaxActiveUsers
	// = 999 is unmistakably different from any value the DB would yield.
	ctx := context.Background()
	seedEnforcerCache(t, vk, tenantID, entitlements.PlanEntitlements{MaxActiveUsers: 999})

	event := buildEvent(t,
		stripe.EventTypeCustomerSubscriptionDeleted,
		"evt_sub_deleted",
		stripe.Subscription{
			Customer: &stripe.Customer{ID: customerID},
		},
	)

	h.handleSubscriptionDeleted(ctx, event)

	// Plan downgrade.
	b := loadBilling(t, store, tenantID)
	if b.Plan != enforcer.DeveloperPlan {
		t.Errorf("Plan = %q, want %q", b.Plan, enforcer.DeveloperPlan)
	}
	if b.GraceUntil != nil {
		t.Errorf("GraceUntil = %v, want nil", *b.GraceUntil)
	}
	if b.Status != "canceled" {
		t.Errorf("Status = %q, want %q", b.Status, "canceled")
	}
	if b.StripeSubscriptionID != nil {
		t.Errorf("StripeSubscriptionID = %v, want nil", *b.StripeSubscriptionID)
	}
	if b.StripeActiveUserPriceID != nil {
		t.Errorf("StripeActiveUserPriceID = %v, want nil", *b.StripeActiveUserPriceID)
	}
	if b.StripeSAConnectionPriceID != nil {
		t.Errorf("StripeSAConnectionPriceID = %v, want nil", *b.StripeSAConnectionPriceID)
	}
	if n := entitlementCount(t, store, tenantID); n != 0 {
		t.Errorf("entitlements remaining after downgrade: %d, want 0", n)
	}

	// Invalidate verified: the cache key the enforcer wrote should be
	// gone. Using the valkey client directly (rather than poking the
	// private cache) keeps the test honest about the public contract:
	// the next read must miss and reload from the DB.
	if _, err := vk.Get(ctx, cacheKeyFor(tenantID)); !errors.Is(err, valkey.ErrNotFound) {
		t.Errorf("enforcer cache still present after Invalidate: err=%v", err)
	}
}

// --- 3. handleInvoicePaymentFailed ---

// TestHandleInvoicePaymentFailed verifies that an invoice.payment_failed
// event sets GraceUntil to now + cfg.GraceDays and flips status to
// past_due, giving the tenant a grace window before the middleware
// auto-downgrades them.
func TestHandleInvoicePaymentFailed(t *testing.T) {
	const graceDays = 14
	h, store, _, _ := newTestHandler(t, graceDays)
	customerID := "cus_inv_failed"
	tenantID := seedBilling(t, store, customerID, "team", "active", nil)

	event := buildEvent(t,
		stripe.EventTypeInvoicePaymentFailed,
		"evt_inv_failed",
		stripe.Invoice{
			Customer: &stripe.Customer{ID: customerID},
		},
	)

	before := time.Now().UTC()
	h.handleInvoicePaymentFailed(context.Background(), event)
	after := time.Now().UTC()

	b := loadBilling(t, store, tenantID)
	if b.GraceUntil == nil {
		t.Fatal("GraceUntil = nil, want a value ~now+GraceDays")
	}

	grace := time.Duration(graceDays) * 24 * time.Hour
	wantMin := before.Add(grace)
	wantMax := after.Add(grace)
	if b.GraceUntil.Before(wantMin) || b.GraceUntil.After(wantMax) {
		t.Errorf("GraceUntil = %v, want in [%v, %v] (now+%dd)", *b.GraceUntil, wantMin, wantMax, graceDays)
	}
	if b.Status != string(stripe.SubscriptionStatusPastDue) {
		t.Errorf("Status = %q, want %q", b.Status, string(stripe.SubscriptionStatusPastDue))
	}
}

// --- 4. handleInvoicePaymentSucceeded ---

// TestHandleInvoicePaymentSucceeded verifies that a successful payment
// after a failed one clears the grace window, leaving the tenant in
// their normal billing state.
func TestHandleInvoicePaymentSucceeded(t *testing.T) {
	h, store, _, _ := newTestHandler(t, 14)
	customerID := "cus_inv_succeeded"
	grace := time.Now().UTC().Add(24 * time.Hour)
	tenantID := seedBilling(t, store, customerID, "team", "past_due", &grace)

	event := buildEvent(t,
		stripe.EventTypeInvoicePaymentSucceeded,
		"evt_inv_succeeded",
		stripe.Invoice{
			Customer: &stripe.Customer{ID: customerID},
		},
	)

	h.handleInvoicePaymentSucceeded(context.Background(), event)

	b := loadBilling(t, store, tenantID)
	if b.GraceUntil != nil {
		t.Errorf("GraceUntil = %v, want nil", *b.GraceUntil)
	}
}

// Package billing implements the Limen billing subsystem.
package billing

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/subscription"
	"go.uber.org/zap"

	stripeclient "github.com/belphemur/limen/internal/billing/stripe"
	"github.com/belphemur/limen/internal/config"
	"github.com/belphemur/limen/internal/storage"
)

// Reconciler periodically syncs billing metrics to Stripe subscription
// quantities, and reports on startup to repair missed webhook deliveries.
type Reconciler struct {
	store           *storage.Store
	stripeClient    *stripeclient.Client
	cfg             config.BillingConfig
	logger          *zap.Logger
	interval        time.Duration
	ReactiveTrigger chan struct{}
	wg              sync.WaitGroup
	cancel          context.CancelFunc
	mu              sync.Mutex // guards cancel
}

// NewReconciler creates a reconciler. interval is the jittered period
// between full reconciliation passes (typically 1h). ReactiveTrigger
// may be nil — in that case only the periodic loop runs.
func NewReconciler(store *storage.Store, client *stripeclient.Client, cfg config.BillingConfig, interval time.Duration, logger *zap.Logger) *Reconciler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Reconciler{
		store:           store,
		stripeClient:    client,
		cfg:             cfg,
		logger:          logger,
		interval:        interval,
		ReactiveTrigger: make(chan struct{}, 1),
	}
}

// Start begins the periodic reconciliation loop and the reactive listener.
// Call once after construction. Pass a context that controls the lifetime
// of the loops (e.g., the app's root context).
func (r *Reconciler) Start(ctx context.Context) {
	ctx, r.cancel = context.WithCancel(ctx)
	r.wg.Add(2)
	go r.periodicLoop(ctx)
	go r.reactiveLoop(ctx)
}

// Stop signals the reconciler to stop and waits for goroutines to finish.
func (r *Reconciler) Stop() {
	r.mu.Lock()
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	r.wg.Wait()
}

// ReconcileNow performs a single reconciliation pass over all tenants
// with active subscriptions. Returns the number of tenants reconciled.
// This is the startup reconciliation entry point.
func (r *Reconciler) ReconcileNow(ctx context.Context) (int, error) {
	return r.reconcileActiveSubscriptions(ctx)
}

// reconcileActiveSubscriptions queries all tenants with active subscriptions
// and reconciles each one. Returns the number of tenants processed.
func (r *Reconciler) reconcileActiveSubscriptions(ctx context.Context) (int, error) {
	db, commit, err := r.store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		return 0, fmt.Errorf("reconciler: open superuser session: %w", err)
	}
	defer func() { _ = commit() }()

	var billings []storage.TenantBilling
	if err := db.Where("status IN ? AND stripe_subscription_id IS NOT NULL", []string{"trialing", "active", "past_due"}).Find(&billings).Error; err != nil {
		return 0, fmt.Errorf("reconciler: query active subscriptions: %w", err)
	}

	for i := range billings {
		if err := r.reconcileTenant(ctx, &billings[i]); err != nil {
			r.logger.Warn("reconcile tenant failed",
				zap.Int64("tenant_id", billings[i].TenantID),
				zap.Error(err),
			)
		}
	}

	return len(billings), nil
}

// reconcileTenant syncs a single tenant's billing metrics to Stripe.
func (r *Reconciler) reconcileTenant(ctx context.Context, billing *storage.TenantBilling) error {
	monthStart := currentMonthStart()

	activeUsers, err := r.countActiveUsers(ctx, billing.TenantID, monthStart)
	if err != nil {
		return fmt.Errorf("count active users: %w", err)
	}

	activeSA, err := r.countActiveSAConnections(ctx, billing.TenantID)
	if err != nil {
		return fmt.Errorf("count active sa connections: %w", err)
	}

	// Upward-only: never decrease quantities within a billing period.
	newActiveUsers := max(billing.ActiveUserCount, activeUsers)
	newSA := max(billing.ActiveSAConnectionCount, activeSA)

	// Update local state.
	if newActiveUsers != billing.ActiveUserCount || newSA != billing.ActiveSAConnectionCount {
		db, commit, err := r.store.Session(storage.WithSuperuser(ctx))
		if err != nil {
			return fmt.Errorf("open superuser session for update: %w", err)
		}
		defer func() { _ = commit() }()

		billing.ActiveUserCount = newActiveUsers
		billing.ActiveSAConnectionCount = newSA
		if err := db.Save(billing).Error; err != nil {
			return fmt.Errorf("update tenant billing counts: %w", err)
		}
	}

	// Update Stripe subscription quantities.
	if err := r.updateStripeSubscription(ctx, billing, newActiveUsers, newSA); err != nil {
		return fmt.Errorf("update stripe subscription: %w", err)
	}

	return nil
}

// countActiveUsers returns the count of distinct users in the current month.
func (r *Reconciler) countActiveUsers(ctx context.Context, tenantID int64, monthStart string) (int32, error) {
	db, commit, err := r.store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		return 0, fmt.Errorf("open superuser session: %w", err)
	}
	defer func() { _ = commit() }()

	var count int64
	sql := `SELECT COUNT(DISTINCT user_id) FROM active_user_months WHERE tenant_id = ? AND month_start = ? AND deleted_at IS NULL`
	if err := db.Raw(sql, tenantID, monthStart).Scan(&count).Error; err != nil {
		return 0, err
	}
	return int32(count), nil
}

// countActiveSAConnections returns the count of active service-account connections.
func (r *Reconciler) countActiveSAConnections(ctx context.Context, tenantID int64) (int32, error) {
	db, commit, err := r.store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		return 0, fmt.Errorf("open superuser session: %w", err)
	}
	defer func() { _ = commit() }()

	var count int64
	sql := `SELECT COUNT(*) FROM sa_connection_snapshots WHERE tenant_id = ? AND deleted_at IS NULL AND disconnected_at IS NULL`
	if err := db.Raw(sql, tenantID).Scan(&count).Error; err != nil {
		return 0, err
	}
	return int32(count), nil
}

// updateStripeSubscription fetches the subscription, finds the items matching
// the configured price IDs, and updates their quantities.
func (r *Reconciler) updateStripeSubscription(ctx context.Context, billing *storage.TenantBilling, activeUsers, saConnections int32) error {
	if billing.StripeSubscriptionID == nil || *billing.StripeSubscriptionID == "" {
		return nil
	}
	if billing.StripeActiveUserPriceID == nil || billing.StripeSAConnectionPriceID == nil {
		return nil
	}

	params := &stripe.SubscriptionParams{Params: stripe.Params{Context: ctx}}
	sub, err := subscription.Get(*billing.StripeSubscriptionID, params)
	if err != nil {
		return fmt.Errorf("fetch subscription: %w", err)
	}

	var userItemID, saItemID string
	for _, item := range sub.Items.Data {
		if item.Price == nil {
			continue
		}
		switch item.Price.ID {
		case *billing.StripeActiveUserPriceID:
			userItemID = item.ID
		case *billing.StripeSAConnectionPriceID:
			saItemID = item.ID
		}
	}

	if userItemID == "" && saItemID == "" {
		return nil
	}

	updateParams := &stripe.SubscriptionParams{
		Params: stripe.Params{Context: ctx},
		Items:  []*stripe.SubscriptionItemsParams{},
	}

	if userItemID != "" {
		updateParams.Items = append(updateParams.Items, &stripe.SubscriptionItemsParams{
			ID:       stripe.String(userItemID),
			Quantity: new(int64(activeUsers)),
		})
	}
	if saItemID != "" {
		updateParams.Items = append(updateParams.Items, &stripe.SubscriptionItemsParams{
			ID:       stripe.String(saItemID),
			Quantity: new(int64(saConnections)),
		})
	}

	_, err = subscription.Update(*billing.StripeSubscriptionID, updateParams)
	if err != nil {
		return fmt.Errorf("update subscription quantities: %w", err)
	}

	return nil
}

// periodicLoop runs the reconciliation at jittered intervals.
func (r *Reconciler) periodicLoop(ctx context.Context) {
	defer r.wg.Done()

	for {
		jittered := time.Duration(float64(r.interval) * (0.8 + 0.4*rand.Float64()))
		timer := time.NewTimer(jittered)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		if _, err := r.reconcileActiveSubscriptions(ctx); err != nil {
			r.logger.Error("periodic reconciliation failed", zap.Error(err))
		}
	}
}

// reactiveLoop listens for triggers and runs reconciliation with debouncing.
func (r *Reconciler) reactiveLoop(ctx context.Context) {
	defer r.wg.Done()

	if r.ReactiveTrigger == nil {
		return
	}

	debounce := time.NewTimer(0)
	if !debounce.Stop() {
		<-debounce.C
	}
	pending := false

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.ReactiveTrigger:
			if !pending {
				pending = true
				debounce.Reset(5 * time.Second)
			}
		case <-debounce.C:
			if !pending {
				continue
			}
			pending = false

			timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if _, err := r.reconcileActiveSubscriptions(timeoutCtx); err != nil {
				r.logger.Error("reactive reconciliation failed", zap.Error(err))
			}
			cancel()
		}
	}
}

// currentMonthStart returns the first day of the current month in YYYY-MM-DD format.
func currentMonthStart() string {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
}

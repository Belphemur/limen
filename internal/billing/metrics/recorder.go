package metrics

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/valkey"
	"go.uber.org/zap"
)

// billingEvent is the internal representation of a billing event for the fallback channel.
type billingEvent struct {
	Kind             string
	TenantID         int64
	UserID           int64
	ServiceAccountID int64
	Connected        bool
	TS               time.Time
}

// BillingRecorder emits billing events to Valkey Streams.
// Fire-and-forget: failures increment a dropped counter, never block the caller.
// When Valkey is disabled, events flow through an in-memory fallback channel
// that is drained to Postgres by StartFallbackDrain.
type BillingRecorder struct {
	valkey    valkey.Client
	store     *storage.Store
	logger    *zap.Logger
	enabled   atomic.Bool
	dropped   atomic.Uint64
	started   atomic.Bool
	closeOnce sync.Once
	fallback  chan billingEvent
	wg        sync.WaitGroup
	mu        sync.Mutex
	closed    bool
}

// NewBillingRecorder creates a recorder.
// When vc is nil, the recorder falls back to an in-memory buffered channel
// that must be drained by calling StartFallbackDrain in a goroutine.
func NewBillingRecorder(vc valkey.Client, store *storage.Store, logger *zap.Logger) *BillingRecorder {
	r := &BillingRecorder{
		valkey: vc,
		store:  store,
		logger: logger,
	}
	if vc != nil {
		r.enabled.Store(true)
	} else {
		r.enabled.Store(false)
		r.fallback = make(chan billingEvent, 1024)
	}
	return r
}

// RecordActiveUser emits an active_user event to the billing:active_users stream.
// Called after a tool call completes. tenantID comes from context, userID and
// serviceAccountID from auth context — one or both may be zero (absent).
func (r *BillingRecorder) RecordActiveUser(ctx context.Context, tenantID int64, userID int64, serviceAccountID int64) {
	if !r.enabled.Load() {
		ev := billingEvent{
			Kind:             "active_user",
			TenantID:         tenantID,
			UserID:           userID,
			ServiceAccountID: serviceAccountID,
			TS:               time.Now(),
		}
		if !r.sendFallback(ev) {
			r.dropped.Add(1)
			eventsDroppedTotal.Inc()
		}
		return
	}
	fields := map[string]string{
		"tenant_id": fmt.Sprintf("%d", tenantID),
		"user_id":   fmt.Sprintf("%d", userID),
		"sa_id":     fmt.Sprintf("%d", serviceAccountID),
		"ts":        fmt.Sprintf("%d", time.Now().UnixMilli()),
	}
	if _, err := r.valkey.XAdd(ctx, "billing:active_users", fields, 500_000); err != nil {
		r.dropped.Add(1)
		eventsDroppedTotal.Inc()
	}
}

// RecordSAConnection emits a connection event to the billing:sa_connections stream.
// Called when a service account MCP connection is established or closed.
func (r *BillingRecorder) RecordSAConnection(ctx context.Context, tenantID int64, serviceAccountID int64, connected bool) {
	if !r.enabled.Load() {
		ev := billingEvent{
			Kind:             "sa_connection",
			TenantID:         tenantID,
			ServiceAccountID: serviceAccountID,
			Connected:        connected,
			TS:               time.Now(),
		}
		if !r.sendFallback(ev) {
			r.dropped.Add(1)
			eventsDroppedTotal.Inc()
		}
		return
	}
	connectedStr := "0"
	if connected {
		connectedStr = "1"
	}
	fields := map[string]string{
		"tenant_id": fmt.Sprintf("%d", tenantID),
		"sa_id":     fmt.Sprintf("%d", serviceAccountID),
		"connected": connectedStr,
		"ts":        fmt.Sprintf("%d", time.Now().UnixMilli()),
	}
	if _, err := r.valkey.XAdd(ctx, "billing:sa_connections", fields, 100_000); err != nil {
		r.dropped.Add(1)
		eventsDroppedTotal.Inc()
	}
}

// StartFallbackDrain starts the fallback drain goroutine. It is safe to call
// multiple times; subsequent calls are no-ops.
func (r *BillingRecorder) StartFallbackDrain(ctx context.Context) {
	if r.enabled.Load() || r.fallback == nil {
		return
	}
	if !r.started.CompareAndSwap(false, true) {
		return
	}
	r.wg.Add(1)
	go r.fallbackDrainLoop(ctx)
}

func (r *BillingRecorder) fallbackDrainLoop(ctx context.Context) {
	defer r.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-r.fallback:
			if !ok {
				return
			}
			r.processFallbackEvent(ctx, ev)
		}
	}
}

// Close gracefully shuts down the fallback drain. It closes the fallback
// channel and waits for the drain goroutine to finish processing remaining
// events. Safe to call multiple times.
func (r *BillingRecorder) Close() {
	if r.enabled.Load() || r.fallback == nil {
		return
	}
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		close(r.fallback)
		r.mu.Unlock()
		r.wg.Wait()
	})
}

// sendFallback sends an event to the fallback channel under the mutex.
// The closed flag and the channel send are protected atomically so that Close()
// cannot close the channel between the check and the send.
// Returns true if the event was sent, false if dropped (closed or full channel).
func (r *BillingRecorder) sendFallback(ev billingEvent) bool {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return false
	}
	select {
	case r.fallback <- ev:
		r.mu.Unlock()
		return true
	default:
		r.mu.Unlock()
		return false
	}
}

func (r *BillingRecorder) processFallbackEvent(ctx context.Context, ev billingEvent) {
	tenantCtx := storage.WithTenant(ctx, ev.TenantID)
	db, commit, err := r.store.Session(tenantCtx)
	if err != nil {
		r.logger.Warn("fallback drain: failed to open session", zap.Int64("tenant_id", ev.TenantID), zap.Error(err))
		r.dropped.Add(1)
		eventsDroppedTotal.Inc()
		return
	}

	switch ev.Kind {
	case "active_user":
		monthStart := ev.TS.Format("2006-01") + "-01"
		var userID, saID *int64
		if ev.UserID != 0 {
			v := ev.UserID
			userID = &v
		}
		if ev.ServiceAccountID != 0 {
			v := ev.ServiceAccountID
			saID = &v
		}
		err = db.Exec(UpsertActiveUserMonthSQL, ev.TenantID, monthStart, userID, saID, ev.TS, ev.TS).Error
	case "sa_connection":
		if ev.Connected {
			err = db.Exec(InsertSAConnectionSnapshotSQL, ev.TenantID, ev.ServiceAccountID, ev.TS, ev.TenantID).Error
		} else {
			err = db.Exec(UpdateSAConnectionSnapshotDisconnectSQL, ev.TS, ev.TenantID, ev.ServiceAccountID).Error
		}
	default:
		r.logger.Error("fallback drain: unknown event kind", zap.String("kind", ev.Kind), zap.Int64("tenant_id", ev.TenantID))
		err = errors.New("unknown event kind: " + ev.Kind)
	}

	if err != nil {
		r.logger.Warn("fallback drain: database write failed", zap.String("kind", ev.Kind), zap.Int64("tenant_id", ev.TenantID), zap.Error(err))
		if rbErr := db.Rollback().Error; rbErr != nil {
			r.logger.Warn("fallback drain: rollback failed", zap.Error(rbErr))
		}
		r.dropped.Add(1)
		eventsDroppedTotal.Inc()
		return
	}

	if err := commit(); err != nil {
		r.logger.Warn("fallback drain: commit failed", zap.String("kind", ev.Kind), zap.Int64("tenant_id", ev.TenantID), zap.Error(err))
		r.dropped.Add(1)
		eventsDroppedTotal.Inc()
	}
}

// Dropped returns the count of events that failed to deliver, useful for Prometheus metrics.
func (r *BillingRecorder) Dropped() uint64 {
	return r.dropped.Load()
}

// Enabled returns whether the recorder is active (Valkey mode).
func (r *BillingRecorder) Enabled() bool {
	return r.enabled.Load()
}

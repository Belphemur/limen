package metrics

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/belphemur/limen/internal/valkey"
)

// BillingRecorder emits billing events to Valkey Streams.
// Fire-and-forget: failures increment a dropped counter, never block the caller.
type BillingRecorder struct {
	valkey  valkey.Client
	enabled atomic.Bool
	dropped atomic.Uint64
}

// NewBillingRecorder creates a recorder. When valkey is nil or valkey.enabled is false,
// the recorder is a no-op (events are silently dropped — the reconciler handles gaps).
func NewBillingRecorder(vc valkey.Client) *BillingRecorder {
	r := &BillingRecorder{valkey: vc}
	r.enabled.Store(vc != nil)
	return r
}

// RecordActiveUser emits an active_user event to the billing:active_users stream.
// Called after a tool call completes. tenantID and userID come from context.
// isServiceAccount indicates whether this user is a service account.
func (r *BillingRecorder) RecordActiveUser(ctx context.Context, tenantID int64, userID int64, serviceAccountID int64) {
	if !r.enabled.Load() {
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
	}
}

// RecordSAConnection emits a connection event to the billing:sa_connections stream.
// Called when a service account MCP connection is established or closed.
func (r *BillingRecorder) RecordSAConnection(ctx context.Context, tenantID int64, serviceAccountID int64, connected bool) {
	if !r.enabled.Load() {
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
	}
}

// Dropped returns the count of events that failed to deliver, useful for Prometheus metrics.
func (r *BillingRecorder) Dropped() uint64 {
	return r.dropped.Load()
}

// Enabled returns whether the recorder is active.
func (r *BillingRecorder) Enabled() bool {
	return r.enabled.Load()
}

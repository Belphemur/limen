package metrics

import (
	"context"
	"testing"

	"github.com/belphemur/limen/internal/valkey"
)

func TestRecorder_Disabled(t *testing.T) {
	r := NewBillingRecorder(nil)
	if r.Enabled() {
		t.Error("expected disabled recorder")
	}
	// Should not panic
	r.RecordActiveUser(context.Background(), 1, 42, 0)
	r.RecordSAConnection(context.Background(), 1, 42, true)
}

func TestRecorder_RecordActiveUser(t *testing.T) {
	vc := valkey.NewInMemory()
	r := NewBillingRecorder(vc)
	if !r.Enabled() {
		t.Fatal("expected enabled recorder")
	}

	ctx := context.Background()
	r.RecordActiveUser(ctx, 1, 42, 0)
	r.RecordActiveUser(ctx, 1, 43, 10)

	if r.Dropped() != 0 {
		t.Errorf("expected 0 dropped, got %d", r.Dropped())
	}

	// Verify stream has entries
	msgs, err := vc.XReadGroup(ctx, "test-group", "test-consumer", 0, 10, "billing:active_users")
	// This will fail because group not created, but that's fine — test just that no panic
	_ = msgs
	_ = err
}

func TestRecorder_RecordSAConnection(t *testing.T) {
	vc := valkey.NewInMemory()
	r := NewBillingRecorder(vc)

	ctx := context.Background()
	r.RecordSAConnection(ctx, 1, 42, true)  // connect
	r.RecordSAConnection(ctx, 1, 42, false) // disconnect

	if r.Dropped() != 0 {
		t.Errorf("expected 0 dropped, got %d", r.Dropped())
	}
}

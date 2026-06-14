package metrics

import (
	"context"
	"testing"

	"github.com/belphemur/limen/internal/valkey"
	"go.uber.org/zap"
)

// TestConsumer_XAck_RemovesFromPending verifies XAck clears the PEL.
func TestConsumer_XAck_RemovesFromPending(t *testing.T) {
	vc := valkey.NewInMemory()
	ctx := context.Background()
	NewConsumer(vc, nil, zap.NewNop(), "c").Bootstrap(ctx)

	if _, err := vc.XAdd(ctx, "billing:active_users", map[string]string{
		"tenant_id": "1", "user_id": "42", "sa_id": "0", "ts": "1700000000000",
	}, 0); err != nil {
		t.Fatalf("XAdd: %v", err)
	}
	msgs, err := vc.XReadGroup(ctx, "billing_observer", "c", 0, 10, "billing:active_users")
	if err != nil {
		t.Fatalf("XReadGroup: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 msg, got %d", len(msgs))
	}

	// Ack the message
	n, err := vc.XAck(ctx, "billing:active_users", "billing_observer", msgs[0].ID)
	if err != nil {
		t.Fatalf("XAck: %v", err)
	}
	if n != 1 {
		t.Errorf("XAck returned %d, want 1", n)
	}

	// Pending list should be empty
	pending, err := vc.XPending(ctx, "billing:active_users", "billing_observer", "-", "+", 100)
	if err != nil {
		t.Fatalf("XPending: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("expected empty PEL after ack, got %+v", pending)
	}
}

// TestConsumer_XDel_RemovesFromStream verifies XDel removes the entry.
func TestConsumer_XDel_RemovesFromStream(t *testing.T) {
	vc := valkey.NewInMemory()
	ctx := context.Background()
	NewConsumer(vc, nil, zap.NewNop(), "c").Bootstrap(ctx)

	id1, err := vc.XAdd(ctx, "billing:active_users", map[string]string{"tenant_id": "1"}, 0)
	if err != nil {
		t.Fatalf("XAdd 1: %v", err)
	}
	id2, err := vc.XAdd(ctx, "billing:active_users", map[string]string{"tenant_id": "2"}, 0)
	if err != nil {
		t.Fatalf("XAdd 2: %v", err)
	}

	n, err := vc.XDel(ctx, "billing:active_users", id1, id2)
	if err != nil {
		t.Fatalf("XDel: %v", err)
	}
	if n != 2 {
		t.Errorf("XDel returned %d, want 2", n)
	}

	entries, err := vc.XRange(ctx, "billing:active_users", "-", "+")
	if err != nil {
		t.Fatalf("XRange: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty stream, got %+v", entries)
	}
}

// TestConsumer_XAck_UnknownID_ReturnsZero verifies XAck with a fake ID is a no-op.
func TestConsumer_XAck_UnknownID_ReturnsZero(t *testing.T) {
	vc := valkey.NewInMemory()
	ctx := context.Background()
	NewConsumer(vc, nil, zap.NewNop(), "c").Bootstrap(ctx)

	n, err := vc.XAck(ctx, "billing:active_users", "billing_observer", "9999-0")
	if err != nil {
		t.Errorf("XAck unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("XAck count = %d, want 0", n)
	}
}

// TestConsumer_XDel_UnknownID_ReturnsZero verifies XDel with a fake ID is a no-op.
func TestConsumer_XDel_UnknownID_ReturnsZero(t *testing.T) {
	vc := valkey.NewInMemory()
	ctx := context.Background()
	NewConsumer(vc, nil, zap.NewNop(), "c").Bootstrap(ctx)

	n, err := vc.XDel(ctx, "billing:active_users", "9999-0")
	if err != nil {
		t.Errorf("XDel unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("XDel count = %d, want 0", n)
	}
}

// TestConsumer_XGroupCreate_IdempotentError verifies XGroupCreate returns
// an error on the second call with the same group name.
func TestConsumer_XGroupCreate_IdempotentError(t *testing.T) {
	vc := valkey.NewInMemory()
	ctx := context.Background()

	if err := vc.XGroupCreate(ctx, "billing:active_users", "billing_observer", "$"); err != nil {
		t.Fatalf("first XGroupCreate: %v", err)
	}

	err := vc.XGroupCreate(ctx, "billing:active_users", "billing_observer", "$")
	if err == nil {
		t.Fatal("expected error on duplicate XGroupCreate")
	}
}

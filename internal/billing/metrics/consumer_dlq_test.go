package metrics

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/belphemur/limen/internal/valkey"
	"go.uber.org/zap"
)

// TestConsumer_DLQ_MessageExceedingThreshold_Moves verifies a message with
// delivery_count >= 5 is moved to the dead-letter stream.
func TestConsumer_DLQ_MessageExceedingThreshold_Moves(t *testing.T) {
	vc := valkey.NewInMemory()
	ctx := context.Background()
	NewConsumer(vc, nil, zap.NewNop(), "c").Bootstrap(ctx)

	if _, err := vc.XAdd(ctx, "billing:active_users", map[string]string{
		"tenant_id": "1", "user_id": "42", "sa_id": "0", "ts": "1700000000000",
	}, 0); err != nil {
		t.Fatalf("XAdd: %v", err)
	}
	if _, err := vc.XReadGroup(ctx, "billing_observer", "c", 0, 10, "billing:active_users"); err != nil {
		t.Fatalf("XReadGroup: %v", err)
	}

	// InMemory XReadGroup only delivers new messages — bump the count via
	// XAutoClaim chain to simulate the message being re-delivered 5+ times.
	bumpDeliveryCount(t, vc, "billing:active_users", 6)

	c := NewConsumer(vc, nil, zap.NewNop(), "c")
	c.sweepDLQ(ctx)

	entries, _ := vc.XRange(ctx, "billing:active_users", "-", "+")
	if len(entries) != 0 {
		t.Errorf("expected original stream to be empty after DLQ move, got %+v", entries)
	}

	dlqEntries, _ := vc.XRange(ctx, "billing:dlq", "-", "+")
	if len(dlqEntries) != 1 {
		t.Errorf("expected 1 DLQ entry, got %d", len(dlqEntries))
	}
}

// TestConsumer_DLQ_BelowThreshold_NoMove verifies a message with delivery
// count < 5 stays in the original stream.
func TestConsumer_DLQ_BelowThreshold_NoMove(t *testing.T) {
	vc := valkey.NewInMemory()
	ctx := context.Background()
	NewConsumer(vc, nil, zap.NewNop(), "c").Bootstrap(ctx)

	if _, err := vc.XAdd(ctx, "billing:active_users", map[string]string{
		"tenant_id": "1", "user_id": "42", "sa_id": "0", "ts": "1700000000000",
	}, 0); err != nil {
		t.Fatalf("XAdd: %v", err)
	}

	for i := range 4 {
		if _, err := vc.XReadGroup(ctx, "billing_observer", "c", 0, 10, "billing:active_users"); err != nil {
			t.Fatalf("XReadGroup iter %d: %v", i, err)
		}
	}

	c := NewConsumer(vc, nil, zap.NewNop(), "c")
	c.sweepDLQ(ctx)

	entries, _ := vc.XRange(ctx, "billing:active_users", "-", "+")
	if len(entries) != 1 {
		t.Errorf("expected 1 entry in original stream, got %d", len(entries))
	}

	dlqEntries, _ := vc.XRange(ctx, "billing:dlq", "-", "+")
	if len(dlqEntries) != 0 {
		t.Errorf("expected empty DLQ, got %d entries", len(dlqEntries))
	}
}

// failingXAddClient fails on XAdd only for the DLQ stream.
type failingXAddClient struct {
	*valkey.InMemory
}

func (f *failingXAddClient) XAdd(ctx context.Context, stream string, fields map[string]string, maxLen int64) (string, error) {
	if stream == "billing:dlq" {
		return "", errors.New("simulated dlq failure")
	}
	return f.InMemory.XAdd(ctx, stream, fields, maxLen)
}

// TestConsumer_DLQ_DLQAddFails_NoAckOriginal verifies that if the DLQ XAdd
// fails, the original message is NOT acked (so it can be retried).
func TestConsumer_DLQ_DLQAddFails_NoAckOriginal(t *testing.T) {
	vc := &failingXAddClient{InMemory: valkey.NewInMemory()}
	ctx := context.Background()
	NewConsumer(vc, nil, zap.NewNop(), "c").Bootstrap(ctx)

	if _, err := vc.XAdd(ctx, "billing:active_users", map[string]string{
		"tenant_id": "1", "user_id": "42", "sa_id": "0", "ts": "1700000000000",
	}, 0); err != nil {
		t.Fatalf("XAdd: %v", err)
	}
	if _, err := vc.XReadGroup(ctx, "billing_observer", "c", 0, 10, "billing:active_users"); err != nil {
		t.Fatalf("XReadGroup: %v", err)
	}

	// Bump delivery count past threshold so sweepDLQ actually attempts the DLQ move.
	bumpDeliveryCount(t, vc.InMemory, "billing:active_users", 6)

	c := NewConsumer(vc, nil, zap.NewNop(), "c")
	c.sweepDLQ(ctx)

	entries, _ := vc.XRange(ctx, "billing:active_users", "-", "+")
	if len(entries) != 1 {
		t.Errorf("expected original message to remain in stream after DLQ failure, got %d entries", len(entries))
	}

	dlqEntries, _ := vc.XRange(ctx, "billing:dlq", "-", "+")
	if len(dlqEntries) != 0 {
		t.Errorf("expected empty DLQ after failure, got %d entries", len(dlqEntries))
	}
}

// TestConsumer_DLQ_StreamEvictedTotal_Increments verifies the prom counter
// increments when a message is moved to the DLQ.
func TestConsumer_DLQ_StreamEvictedTotal_Increments(t *testing.T) {
	vc := valkey.NewInMemory()
	ctx := context.Background()
	NewConsumer(vc, nil, zap.NewNop(), "c").Bootstrap(ctx)

	if _, err := vc.XAdd(ctx, "billing:active_users", map[string]string{
		"tenant_id": "1", "user_id": "42", "sa_id": "0", "ts": "1700000000000",
	}, 0); err != nil {
		t.Fatalf("XAdd: %v", err)
	}
	if _, err := vc.XReadGroup(ctx, "billing_observer", "c", 0, 10, "billing:active_users"); err != nil {
		t.Fatalf("XReadGroup: %v", err)
	}

	bumpDeliveryCount(t, vc, "billing:active_users", 6)

	before := testutil.ToFloat64(streamEvictedTotal)
	c := NewConsumer(vc, nil, zap.NewNop(), "c")
	c.sweepDLQ(ctx)
	after := testutil.ToFloat64(streamEvictedTotal)

	if after != before+1 {
		t.Errorf("expected streamEvictedTotal to increment by 1, got %f -> %f", before, after)
	}
}

// bumpDeliveryCount bumps the delivery count of the most recently delivered
// message in the stream to the given target by chaining XAutoClaim calls
// between throwaway consumer names. Each XAutoClaim requires the message to
// have been idle for minIdleMs (60s), so we use a frozen clock and advance
// it past the threshold between calls.
//
// The InMemory XReadGroup only delivers new messages (it tracks
// lastDeliveredID per stream), so calling it 6 times does not bump the
// delivery count past 1. The XAutoClaim chain is the only way to simulate
// repeated redeliveries against this fake.
func bumpDeliveryCount(t *testing.T, vc *valkey.InMemory, stream string, target int64) {
	t.Helper()
	frozen := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	vc.Now = func() time.Time { return frozen }

	for i := int64(1); i < target; i++ {
		nextConsumer := fmt.Sprintf("bump-%d", i)
		advance := time.Duration(i) * 61 * time.Second
		vc.Now = func() time.Time { return frozen.Add(advance) }
		if _, err := vc.XAutoClaim(context.Background(), stream, "billing_observer", nextConsumer, 60000, 100); err != nil {
			t.Fatalf("XAutoClaim iter %d: %v", i, err)
		}
	}
}

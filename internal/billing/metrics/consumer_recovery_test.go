package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/belphemur/limen/internal/valkey"
	"go.uber.org/zap"
)

// TestConsumer_AutoClaim_PendingMessages verifies a crashed consumer's
// pending messages can be claimed by a new consumer via XAutoClaim.
func TestConsumer_AutoClaim_PendingMessages(t *testing.T) {
	vc := valkey.NewInMemory()
	frozen := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	vc.Now = func() time.Time { return frozen }
	c := NewConsumer(vc, nil, zap.NewNop(), "consumer-a")
	ctx := context.Background()
	c.Bootstrap(ctx)

	// Consumer A reads 1 message but never ACKs (simulates crash)
	if _, err := vc.XAdd(ctx, "billing:active_users", map[string]string{
		"tenant_id": "1", "user_id": "42", "sa_id": "0", "ts": "1700000000000",
	}, 0); err != nil {
		t.Fatalf("XAdd: %v", err)
	}
	if _, err := vc.XReadGroup(ctx, "billing_observer", "consumer-a", 0, 10, "billing:active_users"); err != nil {
		t.Fatalf("XReadGroup: %v", err)
	}

	// Advance past the 60s min-idle threshold so the message becomes claimable.
	vc.Now = func() time.Time { return frozen.Add(2 * time.Minute) }

	// Now consumer B comes online and claims
	c2 := NewConsumer(vc, nil, zap.NewNop(), "consumer-b")
	c2.runAutoClaim(ctx)

	// Verify the message is now in consumer-b's pending list
	pending, err := vc.XPending(ctx, "billing:active_users", "billing_observer", "-", "+", 100)
	if err != nil {
		t.Fatalf("XPending: %v", err)
	}
	foundB := false
	for _, p := range pending {
		if p.Consumer == "consumer-b" {
			foundB = true
		}
	}
	if !foundB {
		t.Errorf("expected message to be in consumer-b's pending list, got %+v", pending)
	}
}

// TestConsumer_AutoClaim_NoPending_NoError verifies runAutoClaim succeeds
// when there are no pending messages.
func TestConsumer_AutoClaim_NoPending_NoError(t *testing.T) {
	vc := valkey.NewInMemory()
	c := NewConsumer(vc, nil, zap.NewNop(), "consumer-a")
	ctx := context.Background()
	c.Bootstrap(ctx)

	// No messages, no pending entries — just run
	c.runAutoClaim(ctx) // should not panic or error
}

// TestConsumer_AutoClaim_BothStreams verifies runAutoClaim sweeps both
// billing streams.
func TestConsumer_AutoClaim_BothStreams(t *testing.T) {
	vc := valkey.NewInMemory()
	frozen := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	vc.Now = func() time.Time { return frozen }
	c := NewConsumer(vc, nil, zap.NewNop(), "consumer-a")
	ctx := context.Background()
	c.Bootstrap(ctx)

	// Add 1 message to each stream and XReadGroup as consumer-a
	if _, err := vc.XAdd(ctx, "billing:active_users", map[string]string{
		"tenant_id": "1", "user_id": "1", "sa_id": "0", "ts": "1700000000000",
	}, 0); err != nil {
		t.Fatalf("XAdd: %v", err)
	}
	if _, err := vc.XAdd(ctx, "billing:sa_connections", map[string]string{
		"tenant_id": "1", "sa_id": "10", "connected": "1", "ts": "1700000000000",
	}, 0); err != nil {
		t.Fatalf("XAdd: %v", err)
	}
	if _, err := vc.XReadGroup(ctx, "billing_observer", "consumer-a", 0, 10, "billing:active_users", "billing:sa_connections"); err != nil {
		t.Fatalf("XReadGroup: %v", err)
	}

	// Advance past the 60s min-idle threshold so both messages become claimable.
	vc.Now = func() time.Time { return frozen.Add(2 * time.Minute) }

	// Consumer B claims
	c2 := NewConsumer(vc, nil, zap.NewNop(), "consumer-b")
	c2.runAutoClaim(ctx)

	// Verify both streams now have consumer-b pending
	for _, stream := range []string{"billing:active_users", "billing:sa_connections"} {
		pending, err := vc.XPending(ctx, stream, "billing_observer", "-", "+", 100)
		if err != nil {
			t.Fatalf("XPending %s: %v", stream, err)
		}
		foundB := false
		for _, p := range pending {
			if p.Consumer == "consumer-b" {
				foundB = true
			}
		}
		if !foundB {
			t.Errorf("%s: expected message in consumer-b's pending, got %+v", stream, pending)
		}
	}
}

// TestConsumer_AutoClaim_RespectsMinIdle verifies that recent pending
// messages (not yet idle long enough) are NOT claimed.
func TestConsumer_AutoClaim_RespectsMinIdle(t *testing.T) {
	vc := valkey.NewInMemory()
	// Freeze clock at a specific time
	frozen := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	vc.Now = func() time.Time { return frozen }

	c := NewConsumer(vc, nil, zap.NewNop(), "consumer-a")
	ctx := context.Background()
	c.Bootstrap(ctx)

	if _, err := vc.XAdd(ctx, "billing:active_users", map[string]string{
		"tenant_id": "1", "user_id": "1", "sa_id": "0", "ts": "1700000000000",
	}, 0); err != nil {
		t.Fatalf("XAdd: %v", err)
	}
	if _, err := vc.XReadGroup(ctx, "billing_observer", "consumer-a", 0, 10, "billing:active_users"); err != nil {
		t.Fatalf("XReadGroup: %v", err)
	}

	// 5 seconds later, consumer B tries to autoclaim with minIdleMs=60000
	vc.Now = func() time.Time { return frozen.Add(5 * time.Second) }
	c2 := NewConsumer(vc, nil, zap.NewNop(), "consumer-b")
	c2.runAutoClaim(ctx)

	// The message was delivered 5s ago — not idle for 60s, so should NOT be claimed
	pending, err := vc.XPending(ctx, "billing:active_users", "billing_observer", "-", "+", 100)
	if err != nil {
		t.Fatalf("XPending: %v", err)
	}
	for _, p := range pending {
		if p.Consumer == "consumer-b" {
			t.Errorf("message was claimed too early: %+v", p)
		}
	}
}

// TestConsumer_XPending_EmptyGroup verifies XPending on an empty group.
func TestConsumer_XPending_EmptyGroup(t *testing.T) {
	vc := valkey.NewInMemory()
	c := NewConsumer(vc, nil, zap.NewNop(), "consumer-a")
	ctx := context.Background()
	c.Bootstrap(ctx)

	pending, err := vc.XPending(ctx, "billing:active_users", "billing_observer", "-", "+", 100)
	if err != nil {
		t.Fatalf("XPending: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("expected empty pending, got %+v", pending)
	}
}

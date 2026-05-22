package signup

import (
	"context"
	"testing"
)

func TestPerIPLimiter_BurstThenDeny(t *testing.T) {
	l := NewPerIPLimiter(1, 2) // burst=2, refill=1/hour
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := l.Allow(ctx, "1.2.3.4"); err != nil {
			t.Fatalf("burst slot %d: unexpected error %v", i, err)
		}
	}
	if err := l.Allow(ctx, "1.2.3.4"); err == nil {
		t.Fatal("want rate-limit error after burst exhausted")
	}
}

func TestPerIPLimiter_PerIPIsolation(t *testing.T) {
	l := NewPerIPLimiter(1, 1)
	ctx := context.Background()
	if err := l.Allow(ctx, "1.1.1.1"); err != nil {
		t.Fatal(err)
	}
	// Second IP should still have its own burst slot.
	if err := l.Allow(ctx, "2.2.2.2"); err != nil {
		t.Fatalf("isolated IP rejected: %v", err)
	}
	if err := l.Allow(ctx, "1.1.1.1"); err == nil {
		t.Fatal("first IP must be limited")
	}
}

func TestPerIPLimiter_EmptyIPAdmitted(t *testing.T) {
	l := NewPerIPLimiter(1, 1)
	// Empty IP means we couldn't extract one — fail open rather than
	// 429 every unparseable request.
	if err := l.Allow(context.Background(), ""); err != nil {
		t.Fatalf("empty IP must be admitted, got %v", err)
	}
}

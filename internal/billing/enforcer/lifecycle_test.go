package enforcer

import (
	"testing"
	"time"
)

// TestEvaluateBillingStatus is the exhaustive table-driven suite for
// the lifecycle state machine. Every Stripe subscription.status value
// is covered, plus the grace-window edge cases (nil, before, at,
// after) and an unknown / future status. The function is pure, so
// the only inputs are (status, graceUntil, now).
func TestEvaluateBillingStatus(t *testing.T) {
	// Fixed reference time so the grace-window cases are
	// deterministic. Status names follow Stripe's enum verbatim.
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	grace := now.Add(24 * time.Hour) // grace expires 24h from now
	past := now.Add(-time.Hour)      // grace expired 1h ago
	future := now.Add(time.Hour)     // grace still valid 1h from now

	tests := []struct {
		name       string
		status     string
		graceUntil *time.Time
		now        time.Time
		want       lifecycleDecision
	}{
		// Pass-through statuses.
		{
			name:   "none passes",
			status: "none",
			want:   decisionPass,
		},
		{
			name:   "trialing passes",
			status: "trialing",
			want:   decisionPass,
		},
		{
			name:   "active passes",
			status: "active",
			want:   decisionPass,
		},

		// past_due with grace window.
		{
			name:       "past_due within grace passes with header",
			status:     "past_due",
			graceUntil: &future,
			now:        now,
			want:       decisionPassGrace,
		},
		{
			name:       "unpaid within grace passes with header",
			status:     "unpaid",
			graceUntil: &future,
			now:        now,
			want:       decisionPassGrace,
		},
		{
			name:       "past_due with nil grace blocks",
			status:     "past_due",
			graceUntil: nil,
			now:        now,
			want:       decisionBlock,
		},
		{
			name:       "unpaid with nil grace blocks",
			status:     "unpaid",
			graceUntil: nil,
			now:        now,
			want:       decisionBlock,
		},
		{
			name:       "past_due grace expired blocks",
			status:     "past_due",
			graceUntil: &past,
			now:        now,
			want:       decisionBlock,
		},
		{
			name:       "unpaid grace expired blocks",
			status:     "unpaid",
			graceUntil: &past,
			now:        now,
			want:       decisionBlock,
		},
		{
			name:       "past_due grace exactly now blocks",
			status:     "past_due",
			graceUntil: &now,
			now:        now,
			want:       decisionBlock,
		},

		// Hard-block statuses.
		{
			name:   "canceled blocks",
			status: "canceled",
			want:   decisionBlock,
		},
		{
			name:       "canceled within grace still blocks",
			status:     "canceled",
			graceUntil: &future,
			now:        now,
			want:       decisionBlock,
		},
		{
			name:   "incomplete blocks",
			status: "incomplete",
			want:   decisionBlock,
		},
		{
			name:   "incomplete_expired blocks",
			status: "incomplete_expired",
			want:   decisionBlock,
		},
		{
			name:   "paused blocks",
			status: "paused",
			want:   decisionBlock,
		},

		// Unknown / future statuses — fail-open (pass with a
		// warning in the caller). Better to let a tenant through
		// than to lock them out on a typo.
		{
			name:   "unknown status fails open",
			status: "totally_new_status",
			want:   decisionPass,
		},
		{
			name:       "unknown status with grace still fails open",
			status:     "weird_state",
			graceUntil: &grace,
			now:        now,
			want:       decisionPass,
		},
		{
			name:   "empty status fails open",
			status: "",
			want:   decisionPass,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evaluateBillingStatus(tt.status, tt.graceUntil, tt.now)
			if got != tt.want {
				t.Errorf("evaluateBillingStatus(%q, %v, %v) = %d, want %d",
					tt.status, tt.graceUntil, tt.now, got, tt.want)
			}
		})
	}
}

// TestEvaluateBillingStatus_GraceBoundary pins the exact semantic of
// now.Before(*graceUntil) — the comparison is strict-less-than, so
// now == graceUntil is the boundary and BLOCKS. This is the
// intent: grace is a real-time window, not a whole-day cushion.
func TestEvaluateBillingStatus_GraceBoundary(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	// One nanosecond before grace expires → still grace.
	almostNow := now.Add(-time.Nanosecond)
	grace := now
	if got := evaluateBillingStatus("past_due", &grace, almostNow); got != decisionPassGrace {
		t.Errorf("expected passGrace at now-1ns, got %d", got)
	}

	// Exactly at grace → blocks.
	if got := evaluateBillingStatus("past_due", &grace, now); got != decisionBlock {
		t.Errorf("expected block at now==grace, got %d", got)
	}
}

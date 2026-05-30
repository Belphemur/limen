package valkey

import (
	"context"
	"testing"
)

func TestCompareStreamID_Malformed(t *testing.T) {
	// Malformed IDs should not panic; they should fall back to simple string comparison.
	cases := []struct {
		a, b string
		want int
	}{
		{"foo", "bar", 1},        // both malformed, string compare: "f" > "b"
		{"foo", "foo", 0},        // both malformed, equal
		{"1-0", "foo", -1},       // one malformed: "1" < "f"
		{"foo", "1-0", 1},        // one malformed: "f" > "1"
		{"1", "2", -1},           // both malformed (no dash): "1" < "2"
		{"1-0-extra", "1-0", 0},  // extra dash — both have >=2 parts, ParseInt ignores suffix, so 0 == 0
		{"1-0", "1-1", -1},       // well-formed, seq differs
		{"1-1", "1-0", 1},        // well-formed, seq differs
		{"0-0", "0-0", 0},        // well-formed, equal
	}

	for _, tc := range cases {
		got := compareStreamID(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("compareStreamID(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestInMemory_XRange_MalformedID(t *testing.T) {
	m := NewInMemory()
	ctx := context.Background()

	// Adding a message with a normal ID
	_, _ = m.XAdd(ctx, "s", map[string]string{"k": "v"}, 0)

	// XRange with malformed IDs should not panic
	msgs, err := m.XRange(ctx, "s", "invalid", "also-invalid")
	if err != nil {
		t.Fatalf("XRange with malformed IDs: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("XRange with malformed IDs should return 0, got %d", len(msgs))
	}
}

func TestInMemory_XPending_MalformedID(t *testing.T) {
	m := NewInMemory()
	ctx := context.Background()

	_, _ = m.XAdd(ctx, "s", map[string]string{"k": "v"}, 0)
	_ = m.XGroupCreate(ctx, "s", "g", "0")
	_, _ = m.XReadGroup(ctx, "g", "c", 0, 10, "s")

	// XPending with malformed range IDs should not panic
	pending, err := m.XPending(ctx, "s", "g", "invalid-start", "invalid-end", 10)
	if err != nil {
		t.Fatalf("XPending with malformed IDs: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("XPending with malformed IDs should return 0, got %d", len(pending))
	}
}

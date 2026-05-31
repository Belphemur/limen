package valkey

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestInMemory_SetExGetDelOneShot(t *testing.T) {
	m := NewInMemory()
	ctx := context.Background()

	if err := m.SetEX(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("SetEX: %v", err)
	}
	got, err := m.GetDel(ctx, "k")
	if err != nil {
		t.Fatalf("GetDel: %v", err)
	}
	if string(got) != "v" {
		t.Fatalf("value = %q, want %q", got, "v")
	}
	if _, err := m.GetDel(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second GetDel err = %v, want ErrNotFound", err)
	}
}

func TestInMemory_Expiry(t *testing.T) {
	m := NewInMemory()
	now := time.Unix(1_700_000_000, 0)
	m.Now = func() time.Time { return now }
	ctx := context.Background()

	if err := m.SetEX(ctx, "k", []byte("v"), 5*time.Second); err != nil {
		t.Fatalf("SetEX: %v", err)
	}
	now = now.Add(10 * time.Second)
	if _, err := m.GetDel(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired GetDel err = %v, want ErrNotFound", err)
	}
}

func TestInMemory_Missing(t *testing.T) {
	m := NewInMemory()
	if _, err := m.GetDel(context.Background(), "absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing GetDel err = %v, want ErrNotFound", err)
	}
}

func TestInMemory_ZeroTTLRejected(t *testing.T) {
	m := NewInMemory()
	if err := m.SetEX(context.Background(), "k", []byte("v"), 0); err == nil {
		t.Fatalf("SetEX with zero TTL should error")
	}
}

func TestInMemory_SetNX(t *testing.T) {
	m := NewInMemory()
	ctx := context.Background()

	ok, err := m.SetNX(ctx, "k", []byte("v"), time.Minute)
	if err != nil {
		t.Fatalf("first SetNX: %v", err)
	}
	if !ok {
		t.Fatalf("first SetNX returned false, want true")
	}

	ok, err = m.SetNX(ctx, "k", []byte("v2"), time.Minute)
	if err != nil {
		t.Fatalf("second SetNX: %v", err)
	}
	if ok {
		t.Fatalf("second SetNX returned true, want false")
	}
}

func TestInMemory_Del(t *testing.T) {
	m := NewInMemory()
	ctx := context.Background()

	if err := m.SetEX(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("SetEX: %v", err)
	}
	if err := m.Del(ctx, "k"); err != nil {
		t.Fatalf("Del: %v", err)
	}
	if _, err := m.Get(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Del err = %v, want ErrNotFound", err)
	}
}

func TestInMemory_Get(t *testing.T) {
	m := NewInMemory()
	ctx := context.Background()

	if err := m.SetEX(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("SetEX: %v", err)
	}
	v, err := m.Get(ctx, "k")
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if string(v) != "v" {
		t.Fatalf("value = %q, want %q", v, "v")
	}

	v, err = m.Get(ctx, "k")
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if string(v) != "v" {
		t.Fatalf("value after second Get = %q, want %q (should not be deleted)", v, "v")
	}
}

func TestInMemory_Get_Expired(t *testing.T) {
	m := NewInMemory()
	now := time.Unix(1_700_000_000, 0)
	m.Now = func() time.Time { return now }
	ctx := context.Background()

	if err := m.SetEX(ctx, "k", []byte("v"), 5*time.Second); err != nil {
		t.Fatalf("SetEX: %v", err)
	}
	now = now.Add(10 * time.Second)
	if _, err := m.Get(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired Get err = %v, want ErrNotFound", err)
	}
}

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

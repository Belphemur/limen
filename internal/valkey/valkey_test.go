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
	// One-shot: second read is gone.
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
	// Advance past TTL.
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

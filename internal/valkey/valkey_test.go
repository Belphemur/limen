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

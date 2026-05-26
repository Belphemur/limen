package resilience

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/belphemur/limen/internal/valkey"
)

func TestValkeyStore_LockUnlock(t *testing.T) {
	store := NewValkeyStore(valkey.NewInMemory())
	const name = "test-lock"

	if err := store.Lock(name); err != nil {
		t.Fatalf("first Lock: %v", err)
	}

	if err := store.Lock(name); !errors.Is(err, errLockHeld) {
		t.Fatalf("second Lock: expected errLockHeld, got %v", err)
	}

	if err := store.Unlock(name); err != nil {
		t.Fatalf("Unlock: %v", err)
	}

	if err := store.Lock(name); err != nil {
		t.Fatalf("Lock after Unlock: %v", err)
	}
}

func TestValkeyStore_GetData_NotFound(t *testing.T) {
	store := NewValkeyStore(valkey.NewInMemory())

	data, err := store.GetData("nonexistent")
	if err != nil {
		t.Fatalf("GetData: expected nil error, got %v", err)
	}
	if data != nil {
		t.Fatalf("GetData: expected nil data for missing key, got %v", data)
	}
}

func TestValkeyStore_SetData_GetData(t *testing.T) {
	store := NewValkeyStore(valkey.NewInMemory())
	const name = "test-state"
	want := []byte(`{"state":"open"}`)

	if err := store.SetData(name, want); err != nil {
		t.Fatalf("SetData: %v", err)
	}

	got, err := store.GetData(name)
	if err != nil {
		t.Fatalf("GetData: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("GetData: expected %q, got %q", want, got)
	}
}

func TestValkeyStore_LockTTL(t *testing.T) {
	clock := time.Now()
	client := valkey.NewInMemory()
	client.Now = func() time.Time { return clock }

	store := NewValkeyStore(client)
	const name = "ttl-test"

	if err := store.Lock(name); err != nil {
		t.Fatalf("Lock: %v", err)
	}

	// Key must exist immediately after lock.
	_, err := client.Get(context.Background(), mutexKey(name))
	if err != nil {
		t.Fatalf("lock key should exist: %v", err)
	}

	// Advance past lock TTL (5s).
	clock = clock.Add(lockTTL + time.Second)

	// Key must be expired now.
	_, err = client.Get(context.Background(), mutexKey(name))
	if !errors.Is(err, valkey.ErrNotFound) {
		t.Fatalf("lock key should be expired: expected ErrNotFound, got %v", err)
	}
}

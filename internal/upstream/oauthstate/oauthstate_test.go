package oauthstate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/valkey"
)

func newTestCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	var key crypto.Key
	for i := range key {
		key[i] = byte(i + 1)
	}
	c, err := crypto.NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	return c
}

func TestStore_PutConsumeRoundTrip(t *testing.T) {
	vk := valkey.NewInMemory()
	s := New(vk, newTestCipher(t), time.Minute)

	env := Envelope{
		TenantID:     42,
		UserID:       7,
		UpstreamID:   13,
		ReturnTo:     "/t/acme/portal/upstreams",
		PKCEVerifier: "abcdefghijklmnopqrstuvwxyz0123456789",
		Nonce:        "nonce-xyz",
	}
	state, err := s.Put(context.Background(), env)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if state == "" {
		t.Fatalf("Put returned empty state")
	}

	got, err := s.Consume(context.Background(), state, 42, 7)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if got != env {
		t.Fatalf("Consume got %+v, want %+v", got, env)
	}
}

func TestStore_ConsumeIsOneShot(t *testing.T) {
	s := New(valkey.NewInMemory(), newTestCipher(t), time.Minute)
	env := Envelope{TenantID: 1, UserID: 2, UpstreamID: 3}
	state, _ := s.Put(context.Background(), env)
	if _, err := s.Consume(context.Background(), state, 1, 2); err != nil {
		t.Fatalf("first Consume: %v", err)
	}
	if _, err := s.Consume(context.Background(), state, 1, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Consume err = %v, want ErrNotFound", err)
	}
}

func TestStore_ConsumeWrongTenant(t *testing.T) {
	s := New(valkey.NewInMemory(), newTestCipher(t), time.Minute)
	env := Envelope{TenantID: 1, UserID: 2, UpstreamID: 3}
	state, _ := s.Put(context.Background(), env)
	// Wrong tenant: AAD mismatch → ErrNotFound (timing-channel safe).
	if _, err := s.Consume(context.Background(), state, 99, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong-tenant Consume err = %v, want ErrNotFound", err)
	}
}

func TestStore_ConsumeUnknown(t *testing.T) {
	s := New(valkey.NewInMemory(), newTestCipher(t), time.Minute)
	if _, err := s.Consume(context.Background(), "no-such-state", 1, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown Consume err = %v, want ErrNotFound", err)
	}
}

func TestStore_TTLExpiry(t *testing.T) {
	vk := valkey.NewInMemory()
	now := time.Unix(1_700_000_000, 0)
	vk.Now = func() time.Time { return now }
	s := New(vk, newTestCipher(t), 5*time.Second)

	env := Envelope{TenantID: 1, UserID: 2, UpstreamID: 3}
	state, _ := s.Put(context.Background(), env)
	now = now.Add(10 * time.Second)
	if _, err := s.Consume(context.Background(), state, 1, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired Consume err = %v, want ErrNotFound", err)
	}
}

func TestStore_PutRejectsZeroIDs(t *testing.T) {
	s := New(valkey.NewInMemory(), newTestCipher(t), time.Minute)
	if _, err := s.Put(context.Background(), Envelope{TenantID: 0, UserID: 1, UpstreamID: 1}); err == nil {
		t.Fatalf("Put with zero TenantID should error")
	}
}

// Package valkey provides a narrow Valkey (Redis-protocol) client used by
// Limen for short-lived, TTL-driven server state. Phase 7's one-shot OAuth
// state is the first consumer; future phases (e.g. rate-limit counters,
// sub-second caches) can reuse the same client.
//
// The Client interface deliberately exposes only the verbs we use today so
// tests can swap in an in-memory fake (see InMemory) without dragging the
// full surface of github.com/valkey-io/valkey-go into the type signature.
package valkey

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/belphemur/limen/internal/config"
	vk "github.com/valkey-io/valkey-go"
)

// ErrNotFound is returned by GetDel when the key is absent (or expired).
// Callers treat this as "first-touch wins" — the second consumer of a
// one-shot token sees ErrNotFound and rejects the request.
var ErrNotFound = errors.New("valkey: key not found")

// Client is the narrow surface Limen consumes.
type Client interface {
	// SetEX writes value with a hard server-side TTL. Overwrites are
	// allowed; callers that need atomic create-or-fail should use SetNX
	// (not currently exposed; add when the first consumer needs it).
	SetEX(ctx context.Context, key string, value []byte, ttl time.Duration) error

	// GetDel atomically reads and deletes the key. Returns ErrNotFound
	// when the key is absent. The atomicity is what gives Phase 7 its
	// one-shot OAuth state guarantee.
	GetDel(ctx context.Context, key string) ([]byte, error)

	// Close releases the underlying connection pool.
	Close()
}

// Open dials Valkey using the supplied config. Returns a usable Client or
// a wiring error; the dial itself happens lazily on the first command (the
// valkey-go default), so this only fails on obvious config mistakes.
func Open(cfg config.ValkeyConfig) (Client, error) {
	if cfg.Address == "" {
		return nil, errors.New("valkey: address is empty")
	}
	opt := vk.ClientOption{
		InitAddress: []string{cfg.Address},
		Password:    cfg.Password,
	}
	if cfg.DialTimeout > 0 {
		opt.Dialer.Timeout = cfg.DialTimeout
	}
	c, err := vk.NewClient(opt)
	if err != nil {
		return nil, fmt.Errorf("valkey: dial %s: %w", cfg.Address, err)
	}
	return &realClient{c: c}, nil
}

type realClient struct {
	c vk.Client
}

func (r *realClient) SetEX(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if ttl <= 0 {
		return errors.New("valkey: ttl must be > 0")
	}
	cmd := r.c.B().Set().Key(key).Value(vk.BinaryString(value)).ExSeconds(int64(ttl.Seconds())).Build()
	if err := r.c.Do(ctx, cmd).Error(); err != nil {
		return fmt.Errorf("valkey: SETEX %s: %w", key, err)
	}
	return nil
}

func (r *realClient) GetDel(ctx context.Context, key string) ([]byte, error) {
	cmd := r.c.B().Getdel().Key(key).Build()
	resp := r.c.Do(ctx, cmd)
	if err := resp.Error(); err != nil {
		if vk.IsValkeyNil(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("valkey: GETDEL %s: %w", key, err)
	}
	s, err := resp.ToString()
	if err != nil {
		if vk.IsValkeyNil(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("valkey: GETDEL decode %s: %w", key, err)
	}
	return []byte(s), nil
}

func (r *realClient) Close() { r.c.Close() }

// InMemory is a test fake honoring TTLs via wall-clock comparisons. Safe
// for concurrent use. Do not use in production.
type InMemory struct {
	mu      sync.Mutex
	entries map[string]inMemoryEntry
	// Now lets tests inject a clock; defaults to time.Now.
	Now func() time.Time
}

type inMemoryEntry struct {
	value     []byte
	expiresAt time.Time
}

// NewInMemory returns an empty in-memory client.
func NewInMemory() *InMemory {
	return &InMemory{entries: make(map[string]inMemoryEntry)}
}

func (m *InMemory) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// SetEX implements Client.
func (m *InMemory) SetEX(_ context.Context, key string, value []byte, ttl time.Duration) error {
	if ttl <= 0 {
		return errors.New("valkey: ttl must be > 0")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	buf := make([]byte, len(value))
	copy(buf, value)
	m.entries[key] = inMemoryEntry{value: buf, expiresAt: m.now().Add(ttl)}
	return nil
}

// GetDel implements Client.
func (m *InMemory) GetDel(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok {
		return nil, ErrNotFound
	}
	delete(m.entries, key)
	if !e.expiresAt.IsZero() && !m.now().Before(e.expiresAt) {
		return nil, ErrNotFound
	}
	return e.value, nil
}

// Close is a no-op for the in-memory fake.
func (m *InMemory) Close() {}

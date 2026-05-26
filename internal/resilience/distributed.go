package resilience

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/belphemur/limen/internal/valkey"
)

const (
	mutexKeyPrefix = "limen:gobreaker:mutex:"
	stateKeyPrefix = "limen:gobreaker:state:"

	lockTTL       = 5 * time.Second
	stateTTL      = 5 * time.Minute
	lockHeldValue = "1"
)

var errLockHeld = errors.New("resilience: lock held")

// ValkeyStore implements gobreaker.SharedDataStore using valkey.Client for
// distributed circuit breaker state. Lock is non-blocking — gobreaker polls.
type ValkeyStore struct {
	client    valkey.Client
	ttl       time.Duration
	opTimeout time.Duration
}

// NewValkeyStore returns a ValkeyStore backed by c. State data TTL defaults
// to 5 minutes, matching the longest breaker open duration plus grace period.
// Operation timeout defaults to 3 seconds.
func NewValkeyStore(c valkey.Client) *ValkeyStore {
	return &ValkeyStore{
		client:    c,
		ttl:       stateTTL,
		opTimeout: 3 * time.Second,
	}
}

func mutexKey(name string) string { return mutexKeyPrefix + name }
func stateKey(name string) string { return stateKeyPrefix + name }

// Lock acquires a non-blocking distributed lock. Returns errLockHeld if the
// key already exists.
func (s *ValkeyStore) Lock(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.opTimeout)
	defer cancel()
	ok, err := s.client.SetNX(ctx, mutexKey(name), []byte(lockHeldValue), lockTTL)
	if err != nil {
		return fmt.Errorf("resilience: lock %q: %w", name, err)
	}
	if !ok {
		return fmt.Errorf("%w: %q", errLockHeld, name)
	}
	return nil
}

// Unlock releases the distributed lock.
func (s *ValkeyStore) Unlock(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.opTimeout)
	defer cancel()
	if err := s.client.Del(ctx, mutexKey(name)); err != nil {
		return fmt.Errorf("resilience: unlock %q: %w", name, err)
	}
	return nil
}

// GetData returns the stored state bytes. Returns (nil, nil) when no state
// exists — gobreaker detects this as ErrNoSharedState and initialises fresh.
func (s *ValkeyStore) GetData(name string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.opTimeout)
	defer cancel()
	val, err := s.client.Get(ctx, stateKey(name))
	if errors.Is(err, valkey.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resilience: get data %q: %w", name, err)
	}
	return val, nil
}

// SetData writes state bytes with a TTL.
func (s *ValkeyStore) SetData(name string, data []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.opTimeout)
	defer cancel()
	if err := s.client.SetEX(ctx, stateKey(name), data, s.ttl); err != nil {
		return fmt.Errorf("resilience: set data %q: %w", name, err)
	}
	return nil
}

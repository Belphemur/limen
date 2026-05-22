package signup

import (
	"context"
	"errors"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter denies StartSignup calls from an IP that has exceeded the
// configured budget. Allow returns nil to admit the request; any
// non-nil error rejects it.
type Limiter interface {
	Allow(ctx context.Context, ip string) error
}

// noopLimiter admits every request. Default when the config sets
// signup.rate_limit.per_hour=0 or the IP is empty (server-to-server
// invocation in tests).
type noopLimiter struct{}

func (noopLimiter) Allow(context.Context, string) error { return nil }

// PerIPLimiter is a fixed-window token bucket keyed by client IP.
// Refill rate and burst are derived from SignupConfig:
//
//	rate  = perHour requests / 3600s
//	burst = configured burst
//
// Buckets for IPs that have been quiet for at least 1h are GC'd by a
// background goroutine to bound memory.
type PerIPLimiter struct {
	mu      sync.Mutex
	rate    rate.Limit
	burst   int
	buckets map[string]*ipBucket
	// idleTTL is how long an idle bucket survives the GC sweep.
	idleTTL time.Duration
}

type ipBucket struct {
	l        *rate.Limiter
	lastUsed time.Time
}

// NewPerIPLimiter constructs a limiter sized for perHour requests
// with the given burst. perHour <= 0 yields a no-op limiter.
func NewPerIPLimiter(perHour, burst int) Limiter {
	if perHour <= 0 {
		return noopLimiter{}
	}
	if burst <= 0 {
		burst = 1
	}
	return &PerIPLimiter{
		rate:    rate.Limit(float64(perHour) / 3600.0),
		burst:   burst,
		buckets: make(map[string]*ipBucket),
		idleTTL: time.Hour,
	}
}

// Allow returns a non-nil error when the IP has exhausted its budget.
// Empty IP admits unconditionally (we cannot key the bucket and an
// untrusted caller could spoof "" anyway).
func (l *PerIPLimiter) Allow(_ context.Context, ip string) error {
	if ip == "" {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[ip]
	if !ok {
		b = &ipBucket{l: rate.NewLimiter(l.rate, l.burst)}
		l.buckets[ip] = b
	}
	b.lastUsed = time.Now()
	if !b.l.Allow() {
		return errors.New("rate limit exceeded")
	}
	return nil
}

// Sweep evicts idle buckets. Callers wire a goroutine that invokes
// this on a 5-minute ticker.
func (l *PerIPLimiter) Sweep() {
	cutoff := time.Now().Add(-l.idleTTL)
	l.mu.Lock()
	defer l.mu.Unlock()
	for ip, b := range l.buckets {
		if b.lastUsed.Before(cutoff) {
			delete(l.buckets, ip)
		}
	}
}

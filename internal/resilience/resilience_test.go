package resilience

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/belphemur/limen/internal/config"
	"go.uber.org/zap"
)

func TestRetryOn503(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	cfg := config.ResiliencePolicy{
		MaxRetries:              2,
		BaseBackoff:             10 * time.Millisecond,
		MaxBackoff:              50 * time.Millisecond,
		BreakerConsecutiveFails: 100, // high so breaker never opens during this test
		BreakerOpenDuration:     30 * time.Second,
	}
	client := Client("test.503", cfg, zap.NewNop())

	req, err := http.NewRequest("GET", server.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected error after exhausted retries")
	}
	if count.Load() != 3 {
		t.Fatalf("expected 3 server requests, got %d", count.Load())
	}
}

func TestNoRetryOn401(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	cfg := config.ResiliencePolicy{
		MaxRetries:              2,
		BaseBackoff:             10 * time.Millisecond,
		MaxBackoff:              50 * time.Millisecond,
		BreakerConsecutiveFails: 100,
		BreakerOpenDuration:     30 * time.Second,
	}
	client := Client("test.401", cfg, zap.NewNop())

	req, err := http.NewRequest("GET", server.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
	if count.Load() != 1 {
		t.Fatalf("expected 1 server request, got %d", count.Load())
	}
}

func TestRetryOn429(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	cfg := config.ResiliencePolicy{
		MaxRetries:              1,
		BaseBackoff:             10 * time.Millisecond,
		MaxBackoff:              50 * time.Millisecond,
		BreakerConsecutiveFails: 100,
		BreakerOpenDuration:     30 * time.Second,
	}
	client := Client("test.429", cfg, zap.NewNop())

	req, err := http.NewRequest("GET", server.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	start := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", resp.StatusCode)
	}
	if count.Load() != 2 {
		t.Fatalf("expected 2 server requests, got %d", count.Load())
	}
	if elapsed < 900*time.Millisecond {
		t.Fatalf("expected at least 900ms elapsed for Retry-After=1s, got %v", elapsed)
	}
}

func TestContextCancellation(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		select {
		case <-r.Context().Done():
			return
		case <-time.After(10 * time.Second):
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	cfg := config.ResiliencePolicy{
		MaxRetries:              2,
		BaseBackoff:             100 * time.Millisecond,
		MaxBackoff:              200 * time.Millisecond,
		BreakerConsecutiveFails: 100,
		BreakerOpenDuration:     30 * time.Second,
	}
	client := Client("test.ctx", cfg, zap.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, err = client.Do(req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if count.Load() != 1 {
		t.Fatalf("expected 1 server request, got %d", count.Load())
	}
}

func TestBreakerOpens(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	cfg := config.ResiliencePolicy{
		MaxRetries:              0,
		BaseBackoff:             10 * time.Millisecond,
		MaxBackoff:              50 * time.Millisecond,
		BreakerConsecutiveFails: 3,
		BreakerOpenDuration:     30 * time.Second,
	}
	client := Client("test.breaker.open", cfg, zap.NewNop())

	for i := range 3 {
		req, _ := http.NewRequest("GET", server.URL, nil)
		_, err := client.Do(req)
		if err == nil {
			t.Fatalf("attempt %d: expected error", i)
		}
		var breakerOpen *BreakerOpenError
		if errors.As(err, &breakerOpen) {
			t.Fatalf("attempt %d: breaker opened too early", i)
		}
	}

	// 4th request should hit the open breaker
	req, _ := http.NewRequest("GET", server.URL, nil)
	_, err := client.Do(req)
	if err == nil {
		t.Fatal("expected breaker open error")
	}
	var breakerOpen *BreakerOpenError
	if !errors.As(err, &breakerOpen) {
		t.Fatalf("expected BreakerOpenError, got: %v", err)
	}
	if count.Load() != 3 {
		t.Fatalf("expected 3 server requests, got %d", count.Load())
	}
}

func TestBreakerHalfOpen(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := count.Add(1)
		if c <= 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := config.ResiliencePolicy{
		MaxRetries:              0,
		BaseBackoff:             10 * time.Millisecond,
		MaxBackoff:              50 * time.Millisecond,
		BreakerConsecutiveFails: 3,
		BreakerOpenDuration:     100 * time.Millisecond,
	}
	client := Client("test.breaker.halfopen", cfg, zap.NewNop())

	for i := range 3 {
		req, _ := http.NewRequest("GET", server.URL, nil)
		_, err := client.Do(req)
		if err == nil {
			t.Fatalf("attempt %d: expected error", i)
		}
	}

	time.Sleep(150 * time.Millisecond)

	req, _ := http.NewRequest("GET", server.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("expected success in half-open, got: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if count.Load() != 4 {
		t.Fatalf("expected 4 server requests, got %d", count.Load())
	}
}

func TestBreakerCloses(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := count.Add(1)
		if c <= 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := config.ResiliencePolicy{
		MaxRetries:              0,
		BaseBackoff:             10 * time.Millisecond,
		MaxBackoff:              50 * time.Millisecond,
		BreakerConsecutiveFails: 3,
		BreakerOpenDuration:     100 * time.Millisecond,
	}
	client := Client("test.breaker.close", cfg, zap.NewNop())

	for i := range 3 {
		req, _ := http.NewRequest("GET", server.URL, nil)
		_, err := client.Do(req)
		if err == nil {
			t.Fatalf("attempt %d: expected error", i)
		}
	}

	time.Sleep(150 * time.Millisecond)

	// Request in half-open that succeeds -> closes breaker
	req, _ := http.NewRequest("GET", server.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("expected success in half-open, got: %v", err)
	}
	resp.Body.Close()

	// Subsequent request should also succeed
	req, _ = http.NewRequest("GET", server.URL, nil)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("expected success after close, got: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if count.Load() != 5 {
		t.Fatalf("expected 5 server requests, got %d", count.Load())
	}
}

func TestBreakerOpenError(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	cfg := config.ResiliencePolicy{
		MaxRetries:              0,
		BaseBackoff:             10 * time.Millisecond,
		MaxBackoff:              50 * time.Millisecond,
		BreakerConsecutiveFails: 3,
		BreakerOpenDuration:     30 * time.Second,
	}
	client := Client("test.breaker.error", cfg, zap.NewNop())

	for i := range 3 {
		req, _ := http.NewRequest("GET", server.URL, nil)
		_, err := client.Do(req)
		if err == nil {
			t.Fatalf("attempt %d: expected error", i)
		}
	}

	req, _ := http.NewRequest("GET", server.URL, nil)
	_, err := client.Do(req)
	if err == nil {
		t.Fatal("expected error")
	}
	var breakerOpen *BreakerOpenError
	if !errors.As(err, &breakerOpen) {
		t.Fatalf("expected BreakerOpenError, got: %v", err)
	}
	if breakerOpen.Name != "test.breaker.error" {
		t.Fatalf("expected name 'test.breaker.error', got %q", breakerOpen.Name)
	}
	if count.Load() != 3 {
		t.Fatalf("expected 3 server requests, got %d", count.Load())
	}
}

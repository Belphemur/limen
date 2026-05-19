package codemode

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// sleepyDispatcher pauses each CallTool for d.delay before returning,
// optionally tracking peak concurrent in-flight calls. A second
// flavour gates on a per-key channel so tests can pin calls in flight
// while inspecting state.
type sleepyDispatcher struct {
	tools     []Tool
	delay     time.Duration
	mu        sync.Mutex
	inFlight  int
	peak      int
	calls     int64
	hold      chan struct{} // when non-nil, CallTool blocks on hold
	respondAs func(upstream, name string, args map[string]any) (any, error)
}

func (s *sleepyDispatcher) ToolsForUser(_ context.Context) ([]Tool, error) {
	return s.tools, nil
}

func (s *sleepyDispatcher) CallTool(ctx context.Context, upstream, name string, args map[string]any) (any, error) {
	atomic.AddInt64(&s.calls, 1)
	s.mu.Lock()
	s.inFlight++
	if s.inFlight > s.peak {
		s.peak = s.inFlight
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.inFlight--
		s.mu.Unlock()
	}()
	if s.hold != nil {
		select {
		case <-s.hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.respondAs != nil {
		return s.respondAs(upstream, name, args)
	}
	return fmt.Sprintf("%s/%s", upstream, name), nil
}

// TestCodeMode_PromiseAllParallelizes proves that Promise.all on two
// proxy calls completes in roughly max(latency) rather than the
// pre-phase-8b sum(latency).
func TestCodeMode_PromiseAllParallelizes(t *testing.T) {
	d := &sleepyDispatcher{
		tools: []Tool{
			{Name: "a", Upstream: "u1"},
			{Name: "b", Upstream: "u2"},
		},
		delay: 80 * time.Millisecond,
	}
	h := newTestHandler(t, d, Config{ScriptTimeout: 5 * time.Second, MaxConcurrentToolCalls: 8})
	start := time.Now()
	_, err := h.Execute(context.Background(),
		`(async () => Promise.all([codemode.u1.a({}), codemode.u2.b({})]))()`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 160*time.Millisecond {
		t.Fatalf("expected parallel ~80ms, took %v (sequential would be ~160ms)", elapsed)
	}
	if atomic.LoadInt64(&d.calls) != 2 {
		t.Fatalf("expected 2 dispatch calls, got %d", d.calls)
	}
	if d.peak < 2 {
		t.Fatalf("expected peak concurrency >= 2, got %d", d.peak)
	}
}

// TestCodeMode_ConcurrencyBoundEnforced fixes the cap at N=2 and
// verifies that when the script issues N+1 calls in parallel, only N
// are ever observed in flight at once.
func TestCodeMode_ConcurrencyBoundEnforced(t *testing.T) {
	hold := make(chan struct{})
	defer close(hold)
	d := &sleepyDispatcher{
		tools: []Tool{
			{Name: "a", Upstream: "u1"},
			{Name: "b", Upstream: "u2"},
			{Name: "c", Upstream: "u3"},
		},
		hold: hold,
	}
	cap := 2
	h := newTestHandler(t, d, Config{
		ScriptTimeout:          5 * time.Second,
		MaxConcurrentToolCalls: cap,
	})

	// Run in a goroutine so we can sample d.inFlight while the script
	// is parked waiting on hold.
	done := make(chan error, 1)
	go func() {
		_, err := h.Execute(context.Background(),
			`(async () => Promise.all([codemode.u1.a({}), codemode.u2.b({}), codemode.u3.c({})]))()`)
		done <- err
	}()

	// Wait for in-flight to reach cap; assert it never exceeds cap.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		n := d.inFlight
		d.mu.Unlock()
		if n >= cap {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	// Hold at cap for a beat and verify the (cap+1)-th doesn't sneak in.
	for i := 0; i < 25; i++ {
		d.mu.Lock()
		n := d.inFlight
		d.mu.Unlock()
		if n > cap {
			t.Fatalf("concurrency cap breached: inFlight=%d cap=%d", n, cap)
		}
		time.Sleep(2 * time.Millisecond)
	}
	// Release: first two complete, third runs.
	hold <- struct{}{}
	hold <- struct{}{}
	hold <- struct{}{}
	if err := <-done; err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if d.peak > cap {
		t.Fatalf("peak exceeded cap: %d > %d", d.peak, cap)
	}
}

// TestCodeMode_CtxCancelStopsInFlight verifies that cancelling the
// invocation ctx mid-script propagates into the in-flight CallTool and
// the script terminates with the ctx error.
func TestCodeMode_CtxCancelStopsInFlight(t *testing.T) {
	hold := make(chan struct{})
	d := &sleepyDispatcher{
		tools: []Tool{{Name: "a", Upstream: "u1"}},
		hold:  hold,
	}
	h := newTestHandler(t, d, Config{ScriptTimeout: 5 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := h.Execute(ctx, `(async () => await codemode.u1.a({}))()`)
		done <- err
	}()

	// Wait until the worker is parked.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		n := d.inFlight
		d.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not return after ctx cancel")
	}
	close(hold)
}

// TestCodeMode_ScriptTimeoutAbortsInFlight verifies that a short
// ScriptTimeout firing while a worker is parked unblocks the outer
// select and surfaces a timeout error.
func TestCodeMode_ScriptTimeoutAbortsInFlight(t *testing.T) {
	hold := make(chan struct{})
	d := &sleepyDispatcher{
		tools: []Tool{{Name: "a", Upstream: "u1"}},
		hold:  hold,
	}
	h := newTestHandler(t, d, Config{ScriptTimeout: 100 * time.Millisecond})
	start := time.Now()
	_, err := h.Execute(context.Background(), `(async () => await codemode.u1.a({}))()`)
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
	close(hold)
}

// TestCodeMode_MicrotaskOrdering pins the spec-compliant FIFO order
// of microtasks: two pre-resolved Promises resolved before a tool
// call complete in their resolution order.
func TestCodeMode_MicrotaskOrdering(t *testing.T) {
	d := &sleepyDispatcher{
		tools: []Tool{{Name: "a", Upstream: "u1"}},
		delay: 20 * time.Millisecond,
	}
	h := newTestHandler(t, d, Config{ScriptTimeout: 5 * time.Second})
	got, err := h.Execute(context.Background(), `(async () => {
		const order = [];
		Promise.resolve().then(() => order.push("p1"));
		Promise.resolve().then(() => order.push("p2"));
		await codemode.u1.a({});
		order.push("tool");
		return order;
	})()`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	arr, ok := got.([]any)
	if !ok || len(arr) != 3 {
		t.Fatalf("expected 3-element array, got %#v", got)
	}
	if arr[0] != "p1" || arr[1] != "p2" || arr[2] != "tool" {
		t.Fatalf("unexpected microtask order: %#v", arr)
	}
}

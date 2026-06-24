package metrics

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/valkey"
)

// TestRecorder_ConcurrentEmit_Enabled_NoRace verifies the recorder is safe
// under concurrent emit to an InMemory Valkey.
func TestRecorder_ConcurrentEmit_Enabled_NoRace(t *testing.T) {
	vc := valkey.NewInMemory()
	r := NewBillingRecorder(vc, nil, zap.NewNop())
	if !r.Enabled() {
		t.Fatal("expected enabled recorder")
	}

	const goroutines = 50
	const eventsPerGoroutine = 1000

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := range goroutines {
		go func(tenantID int64) {
			defer wg.Done()
			for i := range eventsPerGoroutine {
				r.RecordActiveUser(context.Background(), tenantID, int64(i), 0)
			}
		}(int64(g))
	}
	wg.Wait()

	if dropped := r.Dropped(); dropped != 0 {
		t.Errorf("expected 0 dropped, got %d", dropped)
	}
}

// TestRecorder_ChannelBackpressure_RecordsDropped verifies the fallback
// channel's non-blocking send returns false and increments Dropped when
// the 1024 buffer is full.
func TestRecorder_ChannelBackpressure_RecordsDropped(t *testing.T) {
	r := NewBillingRecorder(nil, nil, zap.NewNop())

	beforeProm := testutil.ToFloat64(eventsDroppedTotal)

	for i := range 5000 {
		r.RecordActiveUser(context.Background(), 1, int64(i), 0)
	}

	dropped := r.Dropped()
	if dropped == 0 {
		t.Fatal("expected some dropped events when buffer is full")
	}
	if dropped > 5000 {
		t.Fatalf("dropped %d > 5000, impossible", dropped)
	}

	afterProm := testutil.ToFloat64(eventsDroppedTotal)
	if afterProm <= beforeProm {
		t.Errorf("expected eventsDroppedTotal to increment, before=%f after=%f", beforeProm, afterProm)
	}
}

// TestRecorder_ConcurrentEmit_Fallback_NoRace verifies the fallback path
// does not race under concurrent emit. The drain goroutine is not started
// here because it requires a non-nil store; drain behaviour is covered by
// the existing TestRecorder_FallbackDrain_* tests.
func TestRecorder_ConcurrentEmit_Fallback_NoRace(t *testing.T) {
	r := NewBillingRecorder(nil, nil, zap.NewNop())
	if r.Enabled() {
		t.Fatal("expected disabled recorder")
	}

	const goroutines = 50
	const eventsPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for i := range eventsPerGoroutine {
				r.RecordActiveUser(context.Background(), 1, int64(i), 0)
			}
		}()
	}
	wg.Wait()
}

// TestRecorder_EmitUnderLoad_DoesNotBlock verifies the fallback send is
// non-blocking under heavy concurrent load.
func TestRecorder_EmitUnderLoad_DoesNotBlock(t *testing.T) {
	r := NewBillingRecorder(nil, nil, zap.NewNop())

	const goroutines = 100
	const eventsPerGoroutine = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	done := make(chan struct{})

	for range goroutines {
		go func() {
			defer wg.Done()
			for i := range eventsPerGoroutine {
				r.RecordActiveUser(context.Background(), 1, int64(i), 0)
			}
		}()
	}

	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("emit blocked under load — fallback send is not non-blocking")
	}
}

// TestRecorder_StartFallbackDrain_Idempotent verifies StartFallbackDrain
// is safe to call many times.
func TestRecorder_StartFallbackDrain_Idempotent_Unit(t *testing.T) {
	r := NewBillingRecorder(nil, nil, zap.NewNop())

	for range 100 {
		r.StartFallbackDrain(context.Background())
	}

	r.Close()
}

// TestRecorder_Close_ConcurrentSafe verifies Close is safe to call from
// many goroutines simultaneously.
func TestRecorder_Close_ConcurrentSafe(t *testing.T) {
	r := NewBillingRecorder(nil, nil, zap.NewNop())

	var wg sync.WaitGroup
	wg.Add(20)
	for range 20 {
		go func() {
			defer wg.Done()
			r.Close()
		}()
	}
	wg.Wait()
}

// TestRecorder_Close_BeforeStartDrain verifies Close works when called
// before StartFallbackDrain.
func TestRecorder_Close_BeforeStartDrain(t *testing.T) {
	r := NewBillingRecorder(nil, nil, zap.NewNop())
	r.Close()
	r.StartFallbackDrain(context.Background())
	r.Close()
}

// TestRecorder_Disabled_EmitWithNilStore_DropsAndIncrementsPromCounter
// verifies emitting with nil store increments both counters.
func TestRecorder_Disabled_EmitWithNilStore_DropsAndIncrementsPromCounter(t *testing.T) {
	r := NewBillingRecorder(nil, nil, zap.NewNop())
	r.Close()

	beforeProm := testutil.ToFloat64(eventsDroppedTotal)

	r.RecordActiveUser(context.Background(), 1, 42, 0)

	if dropped := r.Dropped(); dropped != 1 {
		t.Errorf("expected Dropped() == 1, got %d", dropped)
	}

	afterProm := testutil.ToFloat64(eventsDroppedTotal)
	if afterProm <= beforeProm {
		t.Errorf("expected eventsDroppedTotal to increment, before=%f after=%f", beforeProm, afterProm)
	}
}

// TestRecorder_StartFallbackDrain_NoOpOnEnabled verifies StartFallbackDrain
// is a no-op when Valkey is enabled.
func TestRecorder_StartFallbackDrain_NoOpOnEnabled(t *testing.T) {
	vc := valkey.NewInMemory()
	r := NewBillingRecorder(vc, nil, zap.NewNop())

	for range 10 {
		r.StartFallbackDrain(context.Background())
	}

	r.Close()
}

// TestRecorder_RecordActiveUser_EmitsCorrectFields verifies the XAdd
// fields match the input parameters.
func TestRecorder_RecordActiveUser_EmitsCorrectFields(t *testing.T) {
	vc := valkey.NewInMemory()
	r := NewBillingRecorder(vc, nil, zap.NewNop())

	r.RecordActiveUser(context.Background(), 7, 42, 0)
	r.RecordActiveUser(context.Background(), 7, 0, 99)

	entries, err := vc.XRange(context.Background(), "billing:active_users", "-", "+")
	if err != nil {
		t.Fatalf("XRange: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	if entries[0].Fields["tenant_id"] != "7" {
		t.Errorf("entry 0 tenant_id = %q, want 7", entries[0].Fields["tenant_id"])
	}
	if entries[0].Fields["user_id"] != "42" {
		t.Errorf("entry 0 user_id = %q, want 42", entries[0].Fields["user_id"])
	}
	if entries[0].Fields["sa_id"] != "0" {
		t.Errorf("entry 0 sa_id = %q, want 0", entries[0].Fields["sa_id"])
	}

	if entries[1].Fields["user_id"] != "0" {
		t.Errorf("entry 1 user_id = %q, want 0", entries[1].Fields["user_id"])
	}
	if entries[1].Fields["sa_id"] != "99" {
		t.Errorf("entry 1 sa_id = %q, want 99", entries[1].Fields["sa_id"])
	}
}

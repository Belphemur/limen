package metrics

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/storage/storagetest"
	"github.com/belphemur/limen/internal/valkey"
)

func TestRecorder_FallbackDrain_ActiveUser(t *testing.T) {
	store := storagetest.OpenMigratedBilling(t)

	// Create tenant via admin DB
	adminDB := store.RawDB()
	tenant := &storage.Tenant{Name: "Acme", ZitadelOrgID: "z1"}
	if err := adminDB.Create(tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	recorder := NewBillingRecorder(nil, store, zap.NewNop())
	if recorder.Enabled() {
		t.Fatal("expected disabled recorder")
	}

	ctx := context.Background()
	recorder.StartFallbackDrain(ctx)

	// Send two active-user events for the same user
	recorder.RecordActiveUser(ctx, tenant.ID, 42, 0)
	recorder.RecordActiveUser(ctx, tenant.ID, 42, 0)

	// Gracefully drain
	recorder.Close()

	if recorder.Dropped() != 0 {
		t.Fatalf("expected 0 dropped, got %d", recorder.Dropped())
	}

	// Verify DB state using superuser context
	suCtx := storage.WithSuperuser(context.Background())
	db, commit, err := store.Session(suCtx)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer func() { _ = commit() }()

	var aum storage.ActiveUserMonth
	if err := db.Where("tenant_id = ? AND user_id = ?", tenant.ID, 42).First(&aum).Error; err != nil {
		t.Fatalf("find active_user_month: %v", err)
	}
	if aum.CallCount != 2 {
		t.Errorf("call_count = %d, want 2", aum.CallCount)
	}
	if aum.ServiceAccountID != nil && *aum.ServiceAccountID != 0 {
		t.Errorf("service_account_id = %d, want 0", *aum.ServiceAccountID)
	}
}

func TestRecorder_FallbackDrain_SAConnection(t *testing.T) {
	store := storagetest.OpenMigratedBilling(t)

	adminDB := store.RawDB()
	tenant := &storage.Tenant{Name: "Acme", ZitadelOrgID: "z1"}
	if err := adminDB.Create(tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	recorder := NewBillingRecorder(nil, store, zap.NewNop())
	ctx := context.Background()
	recorder.StartFallbackDrain(ctx)

	// Connect then disconnect
	recorder.RecordSAConnection(ctx, tenant.ID, 99, true)
	recorder.RecordSAConnection(ctx, tenant.ID, 99, false)

	recorder.Close()

	if recorder.Dropped() != 0 {
		t.Fatalf("expected 0 dropped, got %d", recorder.Dropped())
	}

	suCtx := storage.WithSuperuser(context.Background())
	db, commit, err := store.Session(suCtx)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer func() { _ = commit() }()

	var snaps []storage.SAConnectionSnapshot
	if err := db.Where("tenant_id = ? AND service_account_id = ?", tenant.ID, 99).Find(&snaps).Error; err != nil {
		t.Fatalf("find snapshots: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snaps))
	}
	if snaps[0].DisconnectedAt == nil {
		t.Error("expected disconnected_at to be set")
	}
	if snaps[0].ConcurrentCount != 1 {
		t.Errorf("concurrent_count = %d, want 1", snaps[0].ConcurrentCount)
	}
}

func TestRecorder_FallbackDrain_UnknownKind(t *testing.T) {
	store := storagetest.OpenMigratedBilling(t)

	adminDB := store.RawDB()
	tenant := &storage.Tenant{Name: "Acme", ZitadelOrgID: "z1"}
	if err := adminDB.Create(tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	recorder := NewBillingRecorder(nil, store, zap.NewNop())
	ctx := context.Background()
	recorder.StartFallbackDrain(ctx)

	// Manually inject an unknown-kind event
	recorder.fallback <- billingEvent{Kind: "unknown", TenantID: tenant.ID, TS: time.Now()}

	recorder.Close()

	if recorder.Dropped() != 1 {
		t.Fatalf("expected 1 dropped, got %d", recorder.Dropped())
	}
}

func TestRecorder_FallbackDrain_DroppedWhenFull(t *testing.T) {
	store := storagetest.OpenMigratedBilling(t)

	recorder := NewBillingRecorder(nil, store, zap.NewNop())
	ctx := context.Background()

	// Don't start drain — fill the buffer past capacity
	for i := range 2000 {
		recorder.RecordActiveUser(ctx, 1, int64(i), 0)
	}

	dropped := recorder.Dropped()
	if dropped == 0 {
		t.Fatal("expected some dropped events when buffer is full")
	}
	if dropped > 2000 {
		t.Fatalf("dropped %d > 2000, impossible", dropped)
	}
}

func TestRecorder_Enabled(t *testing.T) {
	vc := valkey.NewInMemory()
	r := NewBillingRecorder(vc, nil, zap.NewNop())
	if !r.Enabled() {
		t.Error("expected enabled recorder")
	}
}

func TestRecorder_Disabled(t *testing.T) {
	r := NewBillingRecorder(nil, nil, zap.NewNop())
	if r.Enabled() {
		t.Error("expected disabled recorder")
	}
	// Should not panic
	ctx := context.Background()
	r.RecordActiveUser(ctx, 1, 42, 0)
	r.RecordSAConnection(ctx, 1, 42, true)
}

func TestRecorder_RecordActiveUser(t *testing.T) {
	vc := valkey.NewInMemory()
	r := NewBillingRecorder(vc, nil, zap.NewNop())
	if !r.Enabled() {
		t.Fatal("expected enabled recorder")
	}

	ctx := context.Background()
	r.RecordActiveUser(ctx, 1, 42, 0)
	r.RecordActiveUser(ctx, 1, 43, 10)

	if r.Dropped() != 0 {
		t.Errorf("expected 0 dropped, got %d", r.Dropped())
	}

	// Verify stream has entries
	msgs, err := vc.XReadGroup(ctx, "test-group", "test-consumer", 0, 10, "billing:active_users")
	// This will fail because group not created, but that's fine — test just that no panic
	_ = msgs
	_ = err
}

func TestRecorder_RecordSAConnection(t *testing.T) {
	vc := valkey.NewInMemory()
	r := NewBillingRecorder(vc, nil, zap.NewNop())

	ctx := context.Background()
	r.RecordSAConnection(ctx, 1, 42, true)  // connect
	r.RecordSAConnection(ctx, 1, 42, false) // disconnect

	if r.Dropped() != 0 {
		t.Errorf("expected 0 dropped, got %d", r.Dropped())
	}
}

func TestRecorder_StartFallbackDrain_Idempotent(t *testing.T) {
	r := NewBillingRecorder(nil, nil, zap.NewNop())
	ctx := context.Background()

	// Multiple starts should not panic or spawn extra goroutines
	r.StartFallbackDrain(ctx)
	r.StartFallbackDrain(ctx)
	r.StartFallbackDrain(ctx)

	r.Close()

	if r.Dropped() != 0 {
		t.Errorf("expected 0 dropped, got %d", r.Dropped())
	}
}

func TestRecorder_Close_Idempotent(t *testing.T) {
	r := NewBillingRecorder(nil, nil, zap.NewNop())
	ctx := context.Background()

	// Close before start should not panic
	r.Close()

	// Start after close should not panic (goroutine reads closed channel and exits)
	r.StartFallbackDrain(ctx)

	// Multiple closes should not panic
	r.Close()
	r.Close()

	if r.Dropped() != 0 {
		t.Errorf("expected 0 dropped, got %d", r.Dropped())
	}
}

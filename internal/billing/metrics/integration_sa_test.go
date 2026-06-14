//go:build integration

package metrics

import (
	"context"
	"strconv"
	"testing"

	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/storage/storagetest"
	"github.com/belphemur/limen/internal/valkey"
)

// TestIntegration_SAConnection_ConnectThenDisconnect verifies that a
// connect event followed by a disconnect event sets disconnected_at on
// the matching snapshot.
func TestIntegration_SAConnection_ConnectThenDisconnect(t *testing.T) {
	store := storagetest.OpenMigratedBilling(t)
	adminDB := store.RawDB()
	tenant := &storage.Tenant{Name: "Acme", ZitadelOrgID: "z1"}
	if err := adminDB.Create(tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	vc := valkey.NewInMemory()
	c := NewConsumer(vc, store, zap.NewNop(), "test-consumer")
	ctx := context.Background()
	c.Bootstrap(ctx)

	// Connect at t=1700000000000.
	if _, err := vc.XAdd(ctx, "billing:sa_connections", map[string]string{
		"tenant_id": strconv.FormatInt(tenant.ID, 10),
		"sa_id":     "10",
		"connected": "1",
		"ts":        "1700000000000",
	}, 0); err != nil {
		t.Fatalf("XAdd connect: %v", err)
	}
	c.processBatch(ctx)

	// Disconnect at t=1700001000000 (1000 seconds later).
	if _, err := vc.XAdd(ctx, "billing:sa_connections", map[string]string{
		"tenant_id": strconv.FormatInt(tenant.ID, 10),
		"sa_id":     "10",
		"connected": "0",
		"ts":        "1700001000000",
	}, 0); err != nil {
		t.Fatalf("XAdd disconnect: %v", err)
	}
	c.processBatch(ctx)

	suCtx := storage.WithSuperuser(context.Background())
	db, commit, err := store.Session(suCtx)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer func() { _ = commit() }()

	var snap storage.SAConnectionSnapshot
	if err := db.Where("tenant_id = ? AND service_account_id = ?", tenant.ID, 10).First(&snap).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if snap.DisconnectedAt == nil {
		t.Error("expected disconnected_at to be set after disconnect event")
	}
	if snap.DisconnectedAt != nil && !snap.DisconnectedAt.After(snap.ConnectedAt) {
		t.Errorf("disconnected_at %v should be after connected_at %v", snap.DisconnectedAt, snap.ConnectedAt)
	}
}

// TestIntegration_SAConnection_ConcurrentCount_ThreeSequentialConnects
// verifies the concurrent_count subquery returns the right values as
// multiple SA connect events arrive in the same batch. Each event uses a
// distinct connected_at so the order returned by ORDER BY is deterministic.
func TestIntegration_SAConnection_ConcurrentCount_ThreeSequentialConnects(t *testing.T) {
	store := storagetest.OpenMigratedBilling(t)
	adminDB := store.RawDB()
	tenant := &storage.Tenant{Name: "Acme", ZitadelOrgID: "z1"}
	if err := adminDB.Create(tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	vc := valkey.NewInMemory()
	c := NewConsumer(vc, store, zap.NewNop(), "test-consumer")
	ctx := context.Background()
	c.Bootstrap(ctx)

	// 3 SAs connect at distinct timestamps so the read-back order is stable.
	timestamps := []string{"1700000000000", "1700000001000", "1700000002000"}
	for i, ts := range timestamps {
		if _, err := vc.XAdd(ctx, "billing:sa_connections", map[string]string{
			"tenant_id": strconv.FormatInt(tenant.ID, 10),
			"sa_id":     "10",
			"connected": "1",
			"ts":        ts,
		}, 0); err != nil {
			t.Fatalf("XAdd sa %d: %v", i+1, err)
		}
	}

	// Process all 3 in one batch — the per-tenant transaction sees its own
	// prior inserts, so the subquery COUNT(*) reflects the rolling count.
	c.processBatch(ctx)

	suCtx := storage.WithSuperuser(context.Background())
	db, commit, err := store.Session(suCtx)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer func() { _ = commit() }()

	var snaps []storage.SAConnectionSnapshot
	if err := db.Where("tenant_id = ?", tenant.ID).Order("connected_at").Find(&snaps).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(snaps) != 3 {
		t.Fatalf("expected 3 snapshots, got %d", len(snaps))
	}
	// All 3 are still connected, so the rolling concurrent_count is 1, 2, 3.
	if snaps[0].ConcurrentCount != 1 {
		t.Errorf("snaps[0].concurrent_count = %d, want 1", snaps[0].ConcurrentCount)
	}
	if snaps[1].ConcurrentCount != 2 {
		t.Errorf("snaps[1].concurrent_count = %d, want 2", snaps[1].ConcurrentCount)
	}
	if snaps[2].ConcurrentCount != 3 {
		t.Errorf("snaps[2].concurrent_count = %d, want 3", snaps[2].ConcurrentCount)
	}
}

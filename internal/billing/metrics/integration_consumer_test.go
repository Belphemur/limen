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

// TestIntegration_Consumer_ProcessActiveUsers_RLSIsolates verifies that
// per-tenant transactions are isolated — when active-user events for two
// tenants are processed in the same batch, each tenant's session is pinned
// via app.current_tenant, and reading back as tenant A never sees tenant B's
// row (and vice versa).
func TestIntegration_Consumer_ProcessActiveUsers_RLSIsolates(t *testing.T) {
	store := storagetest.OpenMigratedBilling(t)
	adminDB := store.RawDB()

	tenantA := &storage.Tenant{Name: "Acme-A", ZitadelOrgID: "za"}
	tenantB := &storage.Tenant{Name: "Acme-B", ZitadelOrgID: "zb"}
	if err := adminDB.Create(tenantA).Error; err != nil {
		t.Fatalf("create tenant A: %v", err)
	}
	if err := adminDB.Create(tenantB).Error; err != nil {
		t.Fatalf("create tenant B: %v", err)
	}

	vc := valkey.NewInMemory()
	c := NewConsumer(vc, store, zap.NewNop(), "test-consumer")
	ctx := context.Background()
	c.Bootstrap(ctx)

	// 3 events for tenant A (user 10), 2 events for tenant B (user 20).
	for i := 0; i < 3; i++ {
		if _, err := vc.XAdd(ctx, "billing:active_users", map[string]string{
			"tenant_id": strconv.FormatInt(tenantA.ID, 10),
			"user_id":   "10",
			"sa_id":     "0",
			"ts":        "1700000000000",
		}, 0); err != nil {
			t.Fatalf("XAdd A: %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := vc.XAdd(ctx, "billing:active_users", map[string]string{
			"tenant_id": strconv.FormatInt(tenantB.ID, 10),
			"user_id":   "20",
			"sa_id":     "0",
			"ts":        "1700000000000",
		}, 0); err != nil {
			t.Fatalf("XAdd B: %v", err)
		}
	}
	c.processBatch(ctx)

	// Sanity check via superuser — both rows exist with the expected counts.
	suCtx := storage.WithSuperuser(context.Background())
	db, commit, err := store.Session(suCtx)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer func() { _ = commit() }()

	var rowA, rowB storage.ActiveUserMonth
	if err := db.Where("tenant_id = ?", tenantA.ID).First(&rowA).Error; err != nil {
		t.Fatalf("find A: %v", err)
	}
	if rowA.CallCount != 3 {
		t.Errorf("tenant A call_count = %d, want 3", rowA.CallCount)
	}
	if err := db.Where("tenant_id = ?", tenantB.ID).First(&rowB).Error; err != nil {
		t.Fatalf("find B: %v", err)
	}
	if rowB.CallCount != 2 {
		t.Errorf("tenant B call_count = %d, want 2", rowB.CallCount)
	}

	// RLS isolation: a session pinned to tenant A must NOT see tenant B's row.
	aCtx := storage.WithTenant(context.Background(), tenantA.ID)
	aDB, aCommit, err := store.Session(aCtx)
	if err != nil {
		t.Fatalf("session as A: %v", err)
	}
	defer func() { _ = aCommit() }()

	var rowsA []storage.ActiveUserMonth
	if err := aDB.Find(&rowsA).Error; err != nil {
		t.Fatalf("find as A: %v", err)
	}
	if len(rowsA) != 1 {
		t.Fatalf("tenant A session saw %d rows, want 1 (RLS leak)", len(rowsA))
	}
	if rowsA[0].CallCount != 3 {
		t.Errorf("tenant A row call_count = %d, want 3", rowsA[0].CallCount)
	}

	// RLS isolation: a session pinned to tenant B must NOT see tenant A's row.
	bCtx := storage.WithTenant(context.Background(), tenantB.ID)
	bDB, bCommit, err := store.Session(bCtx)
	if err != nil {
		t.Fatalf("session as B: %v", err)
	}
	defer func() { _ = bCommit() }()

	var rowsB []storage.ActiveUserMonth
	if err := bDB.Find(&rowsB).Error; err != nil {
		t.Fatalf("find as B: %v", err)
	}
	if len(rowsB) != 1 {
		t.Fatalf("tenant B session saw %d rows, want 1 (RLS leak)", len(rowsB))
	}
	if rowsB[0].CallCount != 2 {
		t.Errorf("tenant B row call_count = %d, want 2", rowsB[0].CallCount)
	}
}

// TestIntegration_Consumer_ProcessSAConnections_BothStreams verifies that
// SA connection events are written to the sa_connection_snapshots table with
// disconnected_at NULL on a fresh connect.
func TestIntegration_Consumer_ProcessSAConnections_BothStreams(t *testing.T) {
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

	if _, err := vc.XAdd(ctx, "billing:sa_connections", map[string]string{
		"tenant_id": strconv.FormatInt(tenant.ID, 10),
		"sa_id":     "50",
		"connected": "1",
		"ts":        "1700000000000",
	}, 0); err != nil {
		t.Fatalf("XAdd: %v", err)
	}

	c.processBatch(ctx)

	suCtx := storage.WithSuperuser(context.Background())
	db, commit, err := store.Session(suCtx)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer func() { _ = commit() }()

	var snaps []storage.SAConnectionSnapshot
	if err := db.Where("tenant_id = ? AND service_account_id = ?", tenant.ID, 50).Find(&snaps).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snaps))
	}
	if snaps[0].DisconnectedAt != nil {
		t.Errorf("expected disconnected_at to be NULL, got %v", snaps[0].DisconnectedAt)
	}
}

// TestIntegration_Consumer_MonthBoundary verifies that events in different
// calendar months create separate rows in active_user_months.
func TestIntegration_Consumer_MonthBoundary(t *testing.T) {
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

	// 2026-01-15 00:00:00 UTC and 2026-02-15 00:00:00 UTC.
	if _, err := vc.XAdd(ctx, "billing:active_users", map[string]string{
		"tenant_id": strconv.FormatInt(tenant.ID, 10),
		"user_id":   "42",
		"sa_id":     "0",
		"ts":        "1768435200000",
	}, 0); err != nil {
		t.Fatalf("XAdd jan: %v", err)
	}
	if _, err := vc.XAdd(ctx, "billing:active_users", map[string]string{
		"tenant_id": strconv.FormatInt(tenant.ID, 10),
		"user_id":   "42",
		"sa_id":     "0",
		"ts":        "1771113600000",
	}, 0); err != nil {
		t.Fatalf("XAdd feb: %v", err)
	}

	c.processBatch(ctx)

	suCtx := storage.WithSuperuser(context.Background())
	db, commit, err := store.Session(suCtx)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer func() { _ = commit() }()

	var rows []storage.ActiveUserMonth
	if err := db.Where("tenant_id = ?", tenant.ID).Order("month_start").Find(&rows).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows (one per month), got %d", len(rows))
	}
	if rows[0].MonthStart != "2026-01-01" {
		t.Errorf("rows[0].month_start = %q, want %q", rows[0].MonthStart, "2026-01-01")
	}
	if rows[1].MonthStart != "2026-02-01" {
		t.Errorf("rows[1].month_start = %q, want %q", rows[1].MonthStart, "2026-02-01")
	}
	if rows[0].CallCount != 1 || rows[1].CallCount != 1 {
		t.Errorf("call_count = (%d, %d), want (1, 1)", rows[0].CallCount, rows[1].CallCount)
	}
}

// TestIntegration_Consumer_UpsertIncrementsCallCount verifies that 5 events
// for the same (tenant, user, month) increment call_count to 5.
func TestIntegration_Consumer_UpsertIncrementsCallCount(t *testing.T) {
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

	for i := 0; i < 5; i++ {
		if _, err := vc.XAdd(ctx, "billing:active_users", map[string]string{
			"tenant_id": strconv.FormatInt(tenant.ID, 10),
			"user_id":   "42",
			"sa_id":     "0",
			"ts":        "1700000000000",
		}, 0); err != nil {
			t.Fatalf("XAdd iter %d: %v", i, err)
		}
	}

	c.processBatch(ctx)

	suCtx := storage.WithSuperuser(context.Background())
	db, commit, err := store.Session(suCtx)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer func() { _ = commit() }()

	var row storage.ActiveUserMonth
	if err := db.Where("tenant_id = ? AND user_id = ?", tenant.ID, 42).First(&row).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if row.CallCount != 5 {
		t.Errorf("call_count = %d, want 5", row.CallCount)
	}
}

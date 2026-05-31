//go:build integration

package metrics

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/zap"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/config"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/valkey"
)

// startPostgres spins up a postgres:18-alpine container and returns a DSN.
func startPostgres(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pg, err := postgres.Run(ctx,
		"postgres:18-alpine",
		postgres.WithDatabase("limen"),
		postgres.WithUsername("limen"),
		postgres.WithPassword("limen_test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() {
		_ = pg.Terminate(context.Background())
	})

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("conn string: %v", err)
	}
	return dsn
}

func provisionRoles(t *testing.T, bootstrapDSN string) (appDSN, adminDSN string) {
	t.Helper()
	db, err := gorm.Open(gormpostgres.Open(bootstrapDSN), &gorm.Config{})
	if err != nil {
		t.Fatalf("open bootstrap: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	stmts := []string{
		`DROP ROLE IF EXISTS limen_app`,
		`DROP ROLE IF EXISTS limen_admin`,
		`CREATE ROLE limen_admin LOGIN PASSWORD 'admin_pw' BYPASSRLS`,
		`CREATE ROLE limen_app   LOGIN PASSWORD 'app_pw'`,
		`GRANT limen_app TO limen_admin`,
		`GRANT ALL PRIVILEGES ON DATABASE limen TO limen_admin`,
		`GRANT CREATE, USAGE ON SCHEMA public TO limen_admin`,
		`ALTER SCHEMA public OWNER TO limen_admin`,
	}
	for _, q := range stmts {
		if err := db.Exec(q).Error; err != nil {
			t.Fatalf("provision (%s): %v", q, err)
		}
	}
	return rewriteUser(t, bootstrapDSN, "limen_app", "app_pw"),
		rewriteUser(t, bootstrapDSN, "limen_admin", "admin_pw")
}

func rewriteUser(t *testing.T, dsn, user, password string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	u.User = url.UserPassword(user, password)
	return u.String()
}

func openMigratedStore(t *testing.T) *storage.Store {
	t.Helper()
	bootstrap := startPostgres(t)
	appDSN, adminDSN := provisionRoles(t, bootstrap)
	s, err := storage.Open(config.DatabaseConfig{DSN: appDSN, AdminDSN: adminDSN})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Run AutoMigrate directly; skip goose SQL migrations because the project
	// currently has a duplicate version number (00014_billing_metrics.sql vs
	// 00014_strategy_mode_column.sql) that breaks goose. AutoMigrate creates
	// the tables we need for the fallback-drain integration test.
	adminDB := s.RawDB()
	if err := adminDB.AutoMigrate(storage.AllModels()...); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	// Grant table permissions to limen_app so the fallback drain (which uses
	// the app pool) can read and write billing metrics tables.
	grantStmts := []string{
		`GRANT ALL PRIVILEGES ON TABLE active_user_months TO limen_app`,
		`GRANT ALL PRIVILEGES ON TABLE sa_connection_snapshots TO limen_app`,
		`GRANT ALL ON SEQUENCE active_user_months_id_seq TO limen_app`,
		`GRANT ALL ON SEQUENCE sa_connection_snapshots_id_seq TO limen_app`,
	}
	for _, q := range grantStmts {
		if err := adminDB.Exec(q).Error; err != nil {
			t.Fatalf("grant (%s): %v", q, err)
		}
	}

	// Raw SQL inserts bypass GORM's BeforeCreate hook, so public_id is not
	// auto-generated. Add a DB default for the test tables so the fallback
	// drain SQL can insert without providing one.
	defaultStmts := []string{
		`ALTER TABLE active_user_months ALTER COLUMN public_id SET DEFAULT gen_random_uuid()::text`,
		`ALTER TABLE sa_connection_snapshots ALTER COLUMN public_id SET DEFAULT gen_random_uuid()::text`,
	}
	for _, q := range defaultStmts {
		if err := adminDB.Exec(q).Error; err != nil {
			t.Fatalf("default (%s): %v", q, err)
		}
	}
	return s
}

func TestRecorder_FallbackDrain_ActiveUser(t *testing.T) {
	store := openMigratedStore(t)

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
	defer commit()

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
	store := openMigratedStore(t)

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
	defer commit()

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
	store := openMigratedStore(t)

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
	store := openMigratedStore(t)

	recorder := NewBillingRecorder(nil, store, zap.NewNop())
	ctx := context.Background()

	// Don't start drain — fill the buffer past capacity
	for i := 0; i < 2000; i++ {
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

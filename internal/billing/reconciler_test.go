//go:build integration

package billing

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/config"
	"github.com/belphemur/limen/internal/ids"
	"github.com/belphemur/limen/internal/storage"
	"go.uber.org/zap"
)

// startPostgres spins up a postgres:18-alpine container and returns a DSN
// pointing at it.
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

// openMigrated opens a Store, runs Migrate, seeds a test tenant, and returns
// the store + tenant ID + cleanup.
func openMigrated(t *testing.T) (*storage.Store, int64, func()) {
	t.Helper()
	bootstrap := startPostgres(t)
	appDSN, adminDSN := provisionRoles(t, bootstrap)

	cfg := config.DatabaseConfig{
		DSN: appDSN, AdminDSN: adminDSN,
		MaxOpenConns: 5, MaxIdleConns: 2,
	}
	store, err := storage.Open(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Create a test tenant
	tenant := &storage.Tenant{
		Name:         "test-tenant",
		ZitadelOrgID: "org-" + ids.New(ids.PrefixTenant)[4:20],
	}
	db := store.RawDB()
	if err := db.Create(tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	cleanup := func() { _ = store.Close() }
	return store, tenant.ID, cleanup
}

func newTestReconciler(t *testing.T, store *storage.Store) *Reconciler {
	t.Helper()
	return NewReconciler(store, nil, config.BillingConfig{}, time.Hour, zap.NewNop())
}

func TestReconciler_CountActiveUsers_Empty(t *testing.T) {
	store, tenantID, cleanup := openMigrated(t)
	defer cleanup()

	r := newTestReconciler(t, store)
	monthStart := currentMonthStart()

	count, err := r.countActiveUsers(context.Background(), tenantID, monthStart)
	if err != nil {
		t.Fatalf("countActiveUsers: %v", err)
	}
	if count != 0 {
		t.Errorf("countActiveUsers = %d, want 0", count)
	}
}

func TestReconciler_CountActiveUsers_WithData(t *testing.T) {
	store, tenantID, cleanup := openMigrated(t)
	defer cleanup()

	db := store.RawDB()
	monthStart := currentMonthStart()

	// Seed 3 distinct users for current month
	for i := 0; i < 3; i++ {
		user := &storage.User{
			TenantID:       tenantID,
			Email:          fmt.Sprintf("user%d@test.com", i),
			Name:           fmt.Sprintf("User %d", i),
			ZitadelSubject: fmt.Sprintf("zsub-%d", i),
		}
		if err := db.Create(user).Error; err != nil {
			t.Fatalf("create user %d: %v", i, err)
		}

		aum := &storage.ActiveUserMonth{
			TenantID:    tenantID,
			MonthStart:  monthStart,
			UserID:      &user.ID,
			FirstSeenAt: time.Now().UTC(),
			LastSeenAt:  time.Now().UTC(),
		}
		if err := db.Create(aum).Error; err != nil {
			t.Fatalf("create active user month %d: %v", i, err)
		}
	}

	// Seed 2 users for a different tenant
	otherTenant := &storage.Tenant{
		Name:         "other-tenant",
		ZitadelOrgID: "org-other-" + ids.New(ids.PrefixTenant)[4:20],
	}
	if err := db.Create(otherTenant).Error; err != nil {
		t.Fatalf("create other tenant: %v", err)
	}
	for i := 0; i < 2; i++ {
		user := &storage.User{
			TenantID:       otherTenant.ID,
			Email:          fmt.Sprintf("other%d@test.com", i),
			Name:           fmt.Sprintf("Other %d", i),
			ZitadelSubject: fmt.Sprintf("zsub-other-%d", i),
		}
		if err := db.Create(user).Error; err != nil {
			t.Fatalf("create other user %d: %v", i, err)
		}

		aum := &storage.ActiveUserMonth{
			TenantID:    otherTenant.ID,
			MonthStart:  monthStart,
			UserID:      &user.ID,
			FirstSeenAt: time.Now().UTC(),
			LastSeenAt:  time.Now().UTC(),
		}
		if err := db.Create(aum).Error; err != nil {
			t.Fatalf("create other active user month %d: %v", i, err)
		}
	}

	r := newTestReconciler(t, store)
	count, err := r.countActiveUsers(context.Background(), tenantID, monthStart)
	if err != nil {
		t.Fatalf("countActiveUsers: %v", err)
	}
	if count != 3 {
		t.Errorf("countActiveUsers = %d, want 3", count)
	}
}

func TestReconciler_CountActiveSAConnections(t *testing.T) {
	store, tenantID, cleanup := openMigrated(t)
	defer cleanup()

	db := store.RawDB()

	// Create a user to satisfy the service_accounts created_by FK.
	creator := &storage.User{
		TenantID:       tenantID,
		Email:          "creator@test.com",
		Name:           "Creator",
		ZitadelSubject: "zsub-creator",
	}
	if err := db.Create(creator).Error; err != nil {
		t.Fatalf("create creator user: %v", err)
	}

	// Seed 2 active connections (disconnected_at IS NULL)
	for i := 0; i < 2; i++ {
		sa := &storage.ServiceAccount{
			TenantID:      tenantID,
			Name:          fmt.Sprintf("sa-%d", i),
			ZitadelUserID: fmt.Sprintf("zuid-sa-%d", i),
			CreatedByID:   creator.ID,
			Role:          "admin",
		}
		if err := db.Create(sa).Error; err != nil {
			t.Fatalf("create service account %d: %v", i, err)
		}

		snapshot := &storage.SAConnectionSnapshot{
			TenantID:         tenantID,
			ServiceAccountID: sa.ID,
			ConnectedAt:      time.Now().UTC(),
		}
		if err := db.Create(snapshot).Error; err != nil {
			t.Fatalf("create snapshot %d: %v", i, err)
		}
	}

	// Seed 1 disconnected connection (disconnected_at IS NOT NULL)
	sa := &storage.ServiceAccount{
		TenantID:      tenantID,
		Name:          "sa-disconnected",
		ZitadelUserID: "zuid-sa-disconnected",
		CreatedByID:   creator.ID,
		Role:          "admin",
	}
	if err := db.Create(sa).Error; err != nil {
		t.Fatalf("create disconnected service account: %v", err)
	}
	now := time.Now().UTC()
	snapshot := &storage.SAConnectionSnapshot{
		TenantID:         tenantID,
		ServiceAccountID: sa.ID,
		ConnectedAt:      now.Add(-time.Hour),
		DisconnectedAt:   &now,
	}
	if err := db.Create(snapshot).Error; err != nil {
		t.Fatalf("create disconnected snapshot: %v", err)
	}

	r := newTestReconciler(t, store)
	count, err := r.countActiveSAConnections(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("countActiveSAConnections: %v", err)
	}
	if count != 2 {
		t.Errorf("countActiveSAConnections = %d, want 2", count)
	}
}

func TestReconciler_ReconcileTenant_UpwardOnly(t *testing.T) {
	store, tenantID, cleanup := openMigrated(t)
	defer cleanup()

	db := store.RawDB()

	// Create a TenantBilling row with ActiveUserCount=5
	billing := &storage.TenantBilling{
		TenantID:                tenantID,
		Status:                  "active",
		ActiveUserCount:         5,
		ActiveSAConnectionCount: 3,
	}
	if err := db.Create(billing).Error; err != nil {
		t.Fatalf("create tenant billing: %v", err)
	}

	// Seed 3 active users in metrics (fewer than stored count of 5)
	monthStart := currentMonthStart()
	for i := 0; i < 3; i++ {
		user := &storage.User{
			TenantID:       tenantID,
			Email:          fmt.Sprintf("user%d@test.com", i),
			Name:           fmt.Sprintf("User %d", i),
			ZitadelSubject: fmt.Sprintf("zsub-%d", i),
		}
		if err := db.Create(user).Error; err != nil {
			t.Fatalf("create user %d: %v", i, err)
		}

		aum := &storage.ActiveUserMonth{
			TenantID:    tenantID,
			MonthStart:  monthStart,
			UserID:      &user.ID,
			FirstSeenAt: time.Now().UTC(),
			LastSeenAt:  time.Now().UTC(),
		}
		if err := db.Create(aum).Error; err != nil {
			t.Fatalf("create active user month %d: %v", i, err)
		}
	}

	r := newTestReconciler(t, store)
	if err := r.reconcileTenant(context.Background(), billing); err != nil {
		t.Fatalf("reconcileTenant: %v", err)
	}

	// Reload billing from DB
	var updated storage.TenantBilling
	if err := db.First(&updated, billing.ID).Error; err != nil {
		t.Fatalf("reload billing: %v", err)
	}

	if updated.ActiveUserCount != 5 {
		t.Errorf("ActiveUserCount = %d, want 5 (upward-only)", updated.ActiveUserCount)
	}
	if updated.ActiveSAConnectionCount != 3 {
		t.Errorf("ActiveSAConnectionCount = %d, want 3", updated.ActiveSAConnectionCount)
	}
}

func TestReconciler_ReconcileNow_NoActiveSubscriptions(t *testing.T) {
	store, _, cleanup := openMigrated(t)
	defer cleanup()

	r := newTestReconciler(t, store)
	count, err := r.ReconcileNow(context.Background())
	if err != nil {
		t.Fatalf("ReconcileNow: %v", err)
	}
	if count != 0 {
		t.Errorf("ReconcileNow = %d, want 0", count)
	}
}

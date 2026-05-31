//go:build integration

package storage_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
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
		// Termination is best-effort; the test process exit will reap anyway.
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
		// Membership lets the admin role SET ROLE limen_app for in-test
		// downgrades that exercise the policy from the app role's perspective.
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

func openMigrated(t *testing.T) *storage.Store {
	t.Helper()
	bootstrap := startPostgres(t)
	appDSN, adminDSN := provisionRoles(t, bootstrap)
	s, err := storage.Open(config.DatabaseConfig{DSN: appDSN, AdminDSN: adminDSN})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

func TestMigrate_CreatesAllTables(t *testing.T) {
	s := openMigrated(t)

	want := []string{
		"tenants",
		"users",
		"upstreams",
		"upstream_strategy_configs",
		"upstream_registrations",
		"upstream_links",
		"zitadel_apps",
	}
	db := s.RawDB()
	for _, table := range want {
		var exists bool
		err := db.Raw(
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables
				WHERE table_schema = current_schema() AND table_name = ?)`,
			table,
		).Scan(&exists).Error
		if err != nil {
			t.Fatalf("query %s: %v", table, err)
		}
		if !exists {
			t.Errorf("table %q was not created by Migrate", table)
		}
	}
}

func TestPublicID_PrefixAssignedOnCreate(t *testing.T) {
	s := openMigrated(t)
	db := s.RawDB()

	tenant := &storage.Tenant{
		Name:         "Acme Inc",
		ZitadelOrgID: "zorg-1",
	}
	if err := db.Create(tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	if tenant.ID == 0 {
		t.Fatal("tenant.ID was not populated")
	}
	if !strings.HasPrefix(tenant.PublicID, string(ids.PrefixTenant)+"_") {
		t.Errorf("tenant.PublicID = %q, want tnt_ prefix", tenant.PublicID)
	}
	if _, err := ids.MustParse(ids.PrefixTenant, tenant.PublicID); err != nil {
		t.Errorf("MustParse(tnt, %q): %v", tenant.PublicID, err)
	}

	user := &storage.User{
		TenantID:       tenant.ID,
		Email:          "alice@acme.test",
		Name:           "Alice",
		ZitadelSubject: "zsub-alice",
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := ids.MustParse(ids.PrefixUser, user.PublicID); err != nil {
		t.Errorf("user prefix: %v", err)
	}

	upstream := &storage.Upstream{
		TenantID:     tenant.ID,
		Identifier:   "github",
		StrategyType: "mcp_spec",
		McpServerURL: "https://example.invalid/mcp",
	}
	if err := db.Create(upstream).Error; err != nil {
		t.Fatalf("create upstream: %v", err)
	}
	if _, err := ids.MustParse(ids.PrefixUpstream, upstream.PublicID); err != nil {
		t.Errorf("upstream prefix: %v", err)
	}
}

func TestSoftDelete_RowInvisibleByDefaultVisibleUnscoped(t *testing.T) {
	s := openMigrated(t)
	db := s.RawDB()

	tenant := &storage.Tenant{Name: "Acme", ZitadelOrgID: "z1"}
	if err := db.Create(tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	user := &storage.User{
		TenantID:       tenant.ID,
		Email:          "bob@acme.test",
		Name:           "Bob",
		ZitadelSubject: "zsub-bob",
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	if err := db.Delete(user).Error; err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	// Default query: should not find it.
	var found storage.User
	err := db.First(&found, user.ID).Error
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("expected ErrRecordNotFound after soft-delete, got %v", err)
	}

	// Unscoped: should find it with deleted_at set.
	var raw storage.User
	if err := db.Unscoped().First(&raw, user.ID).Error; err != nil {
		t.Fatalf("unscoped first: %v", err)
	}
	if !raw.DeletedAt.Valid {
		t.Error("expected DeletedAt to be set after soft-delete")
	}
}

func TestSoftDelete_DoesNotBlockReinsert(t *testing.T) {
	s := openMigrated(t)
	db := s.RawDB()

	tenant := &storage.Tenant{Name: "Acme", ZitadelOrgID: "z1"}
	if err := db.Create(tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	makeUser := func(seq int) *storage.User {
		return &storage.User{
			TenantID:       tenant.ID,
			Email:          "carol@acme.test",
			Name:           fmt.Sprintf("Carol %d", seq),
			ZitadelSubject: fmt.Sprintf("zsub-carol-%d", seq),
		}
	}

	// First insert succeeds.
	u1 := makeUser(1)
	if err := db.Create(u1).Error; err != nil {
		t.Fatalf("create u1: %v", err)
	}

	// Second insert with the same (tenant, email) must fail.
	u2 := makeUser(2)
	if err := db.Create(u2).Error; err == nil {
		t.Fatal("expected unique-constraint violation on (tenant_id, email), got nil")
	}

	// Soft-delete u1 — now the partial index permits re-creation.
	if err := db.Delete(u1).Error; err != nil {
		t.Fatalf("soft delete u1: %v", err)
	}

	u3 := makeUser(3)
	if err := db.Create(u3).Error; err != nil {
		t.Fatalf("re-insert after soft-delete must succeed, got: %v", err)
	}
}

func TestUniqueConstraints_TenantEmailAndUpstreamName(t *testing.T) {
	s := openMigrated(t)
	db := s.RawDB()

	tenant := &storage.Tenant{Name: "Acme", ZitadelOrgID: "z1"}
	if err := db.Create(tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	// (tenant_id, email)
	mk := func() *storage.User {
		return &storage.User{
			TenantID:       tenant.ID,
			Email:          "dup@acme.test",
			Name:           "Dup",
			ZitadelSubject: "zsub-dup-" + ids.New(ids.PrefixUser),
		}
	}
	if err := db.Create(mk()).Error; err != nil {
		t.Fatalf("first user: %v", err)
	}
	if err := db.Create(mk()).Error; err == nil {
		t.Error("expected duplicate (tenant_id, email) to fail")
	}

	// (tenant_id, name) on upstream
	mkUp := func() *storage.Upstream {
		return &storage.Upstream{
			TenantID:     tenant.ID,
			Identifier:   "github",
			StrategyType: "mcp_spec",
			McpServerURL: "https://x.invalid",
		}
	}
	if err := db.Create(mkUp()).Error; err != nil {
		t.Fatalf("first upstream: %v", err)
	}
	if err := db.Create(mkUp()).Error; err == nil {
		t.Error("expected duplicate (tenant_id, name) on upstream to fail")
	}
}

func TestULID_LexicographicOrdering(t *testing.T) {
	s := openMigrated(t)
	db := s.RawDB()

	// ULID timestamps have 1-millisecond resolution and ulid.Make is
	// monotonic within the same millisecond, so IDs minted back-to-back from
	// a single process must sort in insertion order.
	const n = 25
	publicIDs := make([]string, 0, n)
	for i := range n {
		tnt := &storage.Tenant{
			Name:         fmt.Sprintf("T%d", i),
			ZitadelOrgID: fmt.Sprintf("zorg-%d-%d", time.Now().UnixNano(), i),
		}
		if err := db.Create(tnt).Error; err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		publicIDs = append(publicIDs, tnt.PublicID)
	}

	for i := 1; i < len(publicIDs); i++ {
		if publicIDs[i-1] >= publicIDs[i] {
			t.Errorf("ULID ordering violated at %d: %q !< %q",
				i, publicIDs[i-1], publicIDs[i])
		}
	}
}

func TestSession_RequiresTenantOrSuperuser(t *testing.T) {
	s := openMigrated(t)

	// No tenant, no superuser → ErrNoTenant.
	if _, _, err := s.Session(context.Background()); !errors.Is(err, storage.ErrNoTenant) {
		t.Errorf("expected ErrNoTenant, got %v", err)
	}

	// With tenant → sets the GUC and commits cleanly.
	db := s.RawDB()
	tenant := &storage.Tenant{Name: "Acme", ZitadelOrgID: "z1"}
	if err := db.Create(tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	ctx := storage.WithTenant(context.Background(), tenant.ID)
	tx, commit, err := s.Session(ctx)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}

	// The SET LOCAL we ran should be readable back via current_setting.
	var got string
	if err := tx.Raw(`SELECT current_setting('app.current_tenant', true)`).Scan(&got).Error; err != nil {
		t.Fatalf("current_setting: %v", err)
	}
	if got != fmt.Sprintf("%d", tenant.ID) {
		t.Errorf("app.current_tenant = %q, want %d", got, tenant.ID)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// Idempotent.
	if err := commit(); err != nil {
		t.Errorf("second commit should be a no-op, got %v", err)
	}

	// Superuser path: tenant skipped.
	if _, c, err := s.Session(storage.WithSuperuser(context.Background())); err != nil {
		t.Errorf("superuser Session: %v", err)
	} else {
		_ = c()
	}
}

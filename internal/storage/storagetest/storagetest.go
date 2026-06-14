// Package storagetest provides shared helpers for tests that need a real
// Postgres + Limen schema. It spins up a postgres:18-alpine container
// per call, provisions the limen_admin / limen_app roles, opens both
// pools, and runs the full migration chain.
//
// Test-only — never import from non-_test code.
package storagetest

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/config"
	"github.com/belphemur/limen/internal/storage"
)

// StartPostgres spins up a postgres:18-alpine container and returns a
// superuser DSN pointing at it.
func StartPostgres(t *testing.T) string {
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

// ProvisionRoles creates the limen_admin / limen_app roles on the
// bootstrap DSN and returns app + admin DSNs.
func ProvisionRoles(t *testing.T, bootstrapDSN string) (appDSN, adminDSN string) {
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

// OpenMigrated returns a Store opened against a fresh container with the
// full migration chain applied.
func OpenMigrated(t *testing.T) *storage.Store {
	t.Helper()
	bootstrap := StartPostgres(t)
	appDSN, adminDSN := ProvisionRoles(t, bootstrap)
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

// OpenMigratedBilling returns a Store opened against a fresh container with
// the full migration chain applied, plus billing-specific grants and
// gen_random_uuid() defaults on public_id columns for the billing tables.
// This is needed because the fallback drain uses the app pool (limen_app)
// and raw SQL inserts that bypass GORM's BeforeCreate hook.
func OpenMigratedBilling(t *testing.T) *storage.Store {
	t.Helper()
	s := OpenMigrated(t)

	// Grant table permissions to limen_app so the fallback drain (which uses
	// the app pool) can read and write billing metrics tables.
	adminDB := s.RawDB()
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

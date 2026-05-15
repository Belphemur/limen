// Package storage owns the GORM-backed persistence layer for Limen.
//
// Public surface:
//
//   - Open(cfg) opens the app + admin pools and returns a *Store.
//   - Store.Migrate(ctx) runs AutoMigrate and the embedded SQL migrations.
//   - Session(ctx) is the only sanctioned read/write path on the request path
//     (see tenant.go); it pins the tenant GUC that RLS policies key on.
//
// Phase 3 layers in row-level security: a second connection pool authenticated
// as limen_admin runs migrations and serves WithSuperuser sessions, while the
// default pool authenticates as limen_app (no BYPASSRLS) and is gated by the
// tenant_isolation policy on every tenant-scoped table.
package storage

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/belphemur/limen/internal/config"
)

// Store wraps the GORM handles for the app + admin pools.
type Store struct {
	appDB   *gorm.DB
	adminDB *gorm.DB
	cfg     config.DatabaseConfig
}

// Open establishes both connection pools (app + admin) and returns a Store.
// It does not run migrations — call Store.Migrate explicitly.
//
// cfg.DSN authenticates the request-path pool (limen_app in production).
// cfg.AdminDSN, when non-empty, authenticates the migration / WithSuperuser
// pool (limen_admin). When empty it falls back to cfg.DSN — the dev /
// single-role shortcut documented in docs/runbook.md. Production deployments
// must set both.
func Open(cfg config.DatabaseConfig) (*Store, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("storage: empty DSN")
	}

	appMaxOpen := cfg.MaxOpenConns
	if appMaxOpen == 0 {
		appMaxOpen = 25
	}
	appMaxIdle := cfg.MaxIdleConns
	if appMaxIdle == 0 {
		appMaxIdle = 5
	}
	appDB, err := openPool(cfg.DSN, appMaxOpen, appMaxIdle, cfg.ConnMaxLifetime)
	if err != nil {
		return nil, fmt.Errorf("storage: open app pool: %w", err)
	}

	adminDSN := cfg.AdminDSN
	if adminDSN == "" {
		adminDSN = cfg.DSN
	}
	adminDB, err := openPool(adminDSN, 5, 2, cfg.ConnMaxLifetime)
	if err != nil {
		_ = closePool(appDB)
		return nil, fmt.Errorf("storage: open admin pool: %w", err)
	}

	return &Store{appDB: appDB, adminDB: adminDB, cfg: cfg}, nil
}

func openPool(dsn string, maxOpen, maxIdle int, maxLifetime time.Duration) (*gorm.DB, error) {
	gormCfg := &gorm.Config{
		// Sessions/transactions are managed explicitly through Session(ctx);
		// keep GORM's implicit transactions off for clarity and perf.
		SkipDefaultTransaction: true,
		Logger: logger.New(log.New(os.Stderr, "\r\n", log.LstdFlags), logger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  logger.Warn,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		}),
		NowFunc: func() time.Time { return time.Now().UTC() },
	}
	db, err := gorm.Open(postgres.Open(dsn), gormCfg)
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(maxOpen)
	sqlDB.SetMaxIdleConns(maxIdle)
	if maxLifetime > 0 {
		sqlDB.SetConnMaxLifetime(maxLifetime)
	}
	return db, nil
}

func closePool(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Close releases both pools.
func (s *Store) Close() error {
	appErr := closePool(s.appDB)
	adminErr := closePool(s.adminDB)
	if appErr != nil {
		return appErr
	}
	return adminErr
}

// RawDB returns the admin (BYPASSRLS) handle. Use only for migrations and
// admin tooling — never on the request path.
func (s *Store) RawDB() *gorm.DB { return s.adminDB }

// Ping verifies both pools.
func (s *Store) Ping(ctx context.Context) error {
	for _, db := range []*gorm.DB{s.appDB, s.adminDB} {
		sqlDB, err := db.DB()
		if err != nil {
			return err
		}
		if err := sqlDB.PingContext(ctx); err != nil {
			return err
		}
	}
	return nil
}

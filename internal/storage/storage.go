// Package storage owns the GORM-backed persistence layer for Limen.
//
// Public surface:
//
//   - Open(cfg) opens the pool and returns a *Store.
//   - Store.Migrate(ctx) runs AutoMigrate for every model.
//   - Session(ctx) is the only sanctioned read/write path on the request path
//     (see tenant.go); it pins the tenant GUC that Phase 3 RLS policies key on.
//
// Phase 1 produces the schema, the Session contract, and the WithSuperuser
// escape hatch. Phase 3 layers in RLS policies + an additional limen_admin
// pool without touching call sites.
package storage

import (
	"context"
	"fmt"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/belphemur/limen/internal/config"
)

// Store wraps the GORM handle and the configuration it was opened with.
type Store struct {
	db  *gorm.DB
	cfg config.DatabaseConfig
}

// Open establishes the connection pool against cfg.DSN and returns a Store.
// It does not run migrations — call Store.Migrate explicitly.
func Open(cfg config.DatabaseConfig) (*Store, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("storage: empty DSN")
	}

	gormCfg := &gorm.Config{
		// Sessions/transactions are managed explicitly through Session(ctx);
		// keep GORM's implicit transactions off for clarity and perf.
		SkipDefaultTransaction: true,
		Logger:                 logger.Default.LogMode(logger.Warn),
		NowFunc:                func() time.Time { return time.Now().UTC() },
	}

	db, err := gorm.Open(postgres.Open(cfg.DSN), gormCfg)
	if err != nil {
		return nil, fmt.Errorf("storage: open postgres: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("storage: get *sql.DB: %w", err)
	}

	maxOpen := cfg.MaxOpenConns
	if maxOpen == 0 {
		maxOpen = 25
	}
	maxIdle := cfg.MaxIdleConns
	if maxIdle == 0 {
		maxIdle = 5
	}
	sqlDB.SetMaxOpenConns(maxOpen)
	sqlDB.SetMaxIdleConns(maxIdle)
	if cfg.ConnMaxLifetime > 0 {
		sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}

	return &Store{db: db, cfg: cfg}, nil
}

// Close releases the underlying pool.
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// RawDB returns the unscoped *gorm.DB. Use only for migrations and admin
// tooling — never on the request path.
func (s *Store) RawDB() *gorm.DB { return s.db }

// Ping verifies the connection.
func (s *Store) Ping(ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

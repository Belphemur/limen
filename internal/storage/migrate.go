package storage

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/postgres/*.sql
var sqlMigrations embed.FS

// Migrate brings the schema up to date in two phases, both against the admin
// pool (limen_admin / BYPASSRLS) so RLS policies installed by the SQL layer
// don't lock out their own DDL:
//
//  1. AutoMigrate creates / syncs tables for the registered GORM models. We
//     keep AutoMigrate as the source of truth for table DDL — it tracks Go
//     model changes automatically.
//  2. Goose applies every versioned SQL migration in migrations/postgres/.
//     Migrations there cover everything GORM cannot express: RLS policies,
//     triggers, functions, indexes that need partial / expression syntax,
//     and data backfills. Version state lives in goose_db_version.
func (s *Store) Migrate(ctx context.Context) error {
	db := s.adminDB.WithContext(ctx)
	if err := db.AutoMigrate(AllModels()...); err != nil {
		return fmt.Errorf("storage: automigrate: %w", err)
	}
	return s.runSQLMigrations(ctx)
}

func (s *Store) runSQLMigrations(ctx context.Context) error {
	sqlDB, err := s.adminDB.DB()
	if err != nil {
		return fmt.Errorf("storage: resolve admin *sql.DB: %w", err)
	}
	migFS, err := fs.Sub(sqlMigrations, "migrations/postgres")
	if err != nil {
		return fmt.Errorf("storage: sub embedded migrations: %w", err)
	}
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		sqlDB,
		migFS,
		goose.WithVerbose(false),
	)
	if err != nil {
		return fmt.Errorf("storage: build goose provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("storage: apply goose migrations: %w", err)
	}
	return nil
}

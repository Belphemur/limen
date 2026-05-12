package storage

import (
	"context"
	"embed"
	"fmt"
	"sort"
)

//go:embed migrations/postgres/*.sql
var sqlMigrations embed.FS

// Migrate runs AutoMigrate followed by every embedded SQL migration in
// lexical order. All work runs against the admin pool (limen_admin /
// BYPASSRLS) so RLS policies installed by the SQL layer don't lock out their
// own DDL.
//
// SQL migrations are written to be idempotent (DROP IF EXISTS / catalog
// probes), so re-running Migrate is safe.
func (s *Store) Migrate(ctx context.Context) error {
	db := s.adminDB.WithContext(ctx)
	if err := db.AutoMigrate(AllModels()...); err != nil {
		return fmt.Errorf("storage: automigrate: %w", err)
	}
	return s.runSQLMigrations(ctx)
}

func (s *Store) runSQLMigrations(ctx context.Context) error {
	const dir = "migrations/postgres"
	entries, err := sqlMigrations.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("storage: read embedded migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	db := s.adminDB.WithContext(ctx)
	for _, name := range names {
		body, err := sqlMigrations.ReadFile(dir + "/" + name)
		if err != nil {
			return fmt.Errorf("storage: read migration %q: %w", name, err)
		}
		if err := db.Exec(string(body)).Error; err != nil {
			return fmt.Errorf("storage: apply migration %q: %w", name, err)
		}
	}
	return nil
}

package storage

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

// SchemaVersionMismatchError is returned by CheckSchemaVersion when the
// goose head recorded in the database does not match the highest
// migration embedded in the service binary. Service binaries surface
// this with a clear "run `limenctl migrate`" message before exiting.
type SchemaVersionMismatchError struct {
	DBVersion       int64
	EmbeddedVersion int64
}

func (e *SchemaVersionMismatchError) Error() string {
	switch {
	case e.DBVersion < e.EmbeddedVersion:
		return fmt.Sprintf(
			"schema is at version %d but this binary embeds migrations up to %d — run `limenctl migrate`",
			e.DBVersion, e.EmbeddedVersion,
		)
	case e.DBVersion > e.EmbeddedVersion:
		return fmt.Sprintf(
			"schema is at version %d but this binary only embeds migrations up to %d — the database has been migrated by a newer Limen release; upgrade this binary",
			e.DBVersion, e.EmbeddedVersion,
		)
	default:
		return fmt.Sprintf("schema version %d matches embedded version (unexpected mismatch)", e.DBVersion)
	}
}

// CheckSchemaVersion compares the goose head recorded in the database
// against the highest migration embedded in the binary. It returns a
// *SchemaVersionMismatchError when the two diverge.
//
// Every service binary calls this once on boot (after Open, before
// mounting routes) via boot.BootRuntime. Migrations are run out-of-
// band by `limenctl migrate` (or `limen migrate` on the all-in-one);
// no service binary auto-migrates.
func (s *Store) CheckSchemaVersion(ctx context.Context) error {
	sqlDB, err := s.adminDB.DB()
	if err != nil {
		return fmt.Errorf("storage: resolve admin *sql.DB: %w", err)
	}
	migFS, err := fs.Sub(sqlMigrations, "migrations/postgres")
	if err != nil {
		return fmt.Errorf("storage: sub embedded migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migFS, goose.WithVerbose(false))
	if err != nil {
		return fmt.Errorf("storage: build goose provider: %w", err)
	}
	dbVersion, target, err := provider.GetVersions(ctx)
	if err != nil {
		return fmt.Errorf("storage: read schema version: %w", err)
	}
	if dbVersion != target {
		return &SchemaVersionMismatchError{DBVersion: dbVersion, EmbeddedVersion: target}
	}
	return nil
}

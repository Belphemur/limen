//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/belphemur/limen/internal/config"
	"github.com/belphemur/limen/internal/storage"
)

func TestCheckSchemaVersion_Match(t *testing.T) {
	s := openMigrated(t)
	if err := s.CheckSchemaVersion(context.Background()); err != nil {
		t.Fatalf("CheckSchemaVersion after Migrate: %v", err)
	}
}

func TestCheckSchemaVersion_DBBehind(t *testing.T) {
	s := openMigrated(t)

	// Roll the recorded version back to mimic an old DB the current
	// binary doesn't recognise. Direct DELETE against goose_db_version
	// is the simplest way to forge a version-skew scenario without
	// reshuffling embedded migrations.
	rawDB, err := s.RawDB().DB()
	if err != nil {
		t.Fatalf("admin *sql.DB: %v", err)
	}
	var head int64
	if err := rawDB.QueryRow(`SELECT MAX(version_id) FROM goose_db_version`).Scan(&head); err != nil {
		t.Fatalf("read head: %v", err)
	}
	if _, err := rawDB.Exec(`DELETE FROM goose_db_version WHERE version_id = $1`, head); err != nil {
		t.Fatalf("forge version skew: %v", err)
	}

	var mismatch *storage.SchemaVersionMismatchError
	err = s.CheckSchemaVersion(context.Background())
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected SchemaVersionMismatchError, got %v", err)
	}
	if mismatch.DBVersion >= mismatch.EmbeddedVersion {
		t.Errorf("expected DBVersion < EmbeddedVersion, got %d vs %d",
			mismatch.DBVersion, mismatch.EmbeddedVersion)
	}
}

func TestCheckSchemaVersion_FreshDB(t *testing.T) {
	bootstrap := startPostgres(t)
	appDSN, adminDSN := provisionRoles(t, bootstrap)
	s, err := storage.Open(config.DatabaseConfig{DSN: appDSN, AdminDSN: adminDSN})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Unmigrated DB has no goose_db_version table — goose treats this
	// as version 0 and reports mismatch against the embedded head.
	err = s.CheckSchemaVersion(context.Background())
	var mismatch *storage.SchemaVersionMismatchError
	if !errors.As(err, &mismatch) {
		// goose may also surface a "table missing" wrap; either is
		// acceptable as a non-nil error signalling the binary should
		// refuse to start.
		if err == nil {
			t.Fatalf("expected error from CheckSchemaVersion against fresh DB")
		}
	}
}

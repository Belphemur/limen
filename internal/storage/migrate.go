package storage

import (
	"context"
	"fmt"
)

// Migrate runs AutoMigrate for every model. It is the only sanctioned consumer
// of RawDB() — request-path code goes through Session(ctx) instead.
//
// RLS policies and the set_updated_at trigger live in migrations/postgres/*.sql
// (Phase 3) and are not run here.
func (s *Store) Migrate(ctx context.Context) error {
	db := s.db.WithContext(ctx)
	if err := db.AutoMigrate(AllModels()...); err != nil {
		return fmt.Errorf("storage: automigrate: %w", err)
	}
	return nil
}

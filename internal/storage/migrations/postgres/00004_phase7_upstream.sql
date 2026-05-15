-- +goose Up
-- +goose StatementBegin
-- Phase 7 — upstream link health / lifecycle.
--
-- AutoMigrate (run before this migration) has already added the new
-- upstream_links columns: enabled, needs_relink, consecutive_failures,
-- first_failure_at, last_failure_at, last_failure_reason, auto_disabled_at.
-- This migration adds a partial index that the background refresher uses
-- to find candidates without scanning the whole table.
--
-- The refresher's polling query is:
--   SELECT … FROM upstream_links
--   WHERE deleted_at IS NULL
--     AND expires_at IS NOT NULL
--     AND expires_at < $now_plus_window
--     AND needs_relink = false
--     AND auto_disabled_at IS NULL
--   ORDER BY expires_at
--   LIMIT $batch
-- so the index covers the live, refreshable, expiring-soonest rows.
CREATE INDEX IF NOT EXISTS idx_upstream_links_refresh_candidates
ON upstream_links (expires_at)
WHERE deleted_at IS NULL
  AND expires_at IS NOT NULL
  AND needs_relink = false
  AND auto_disabled_at IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_upstream_links_refresh_candidates;
-- +goose StatementEnd

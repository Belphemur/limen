-- +goose Up
-- +goose StatementBegin
-- Phase 9f follow-up. Early dev installs ran 00010 before its DO
-- block learned to drop the legacy (tenant_id, pattern) UNIQUE that
-- GORM's first cut of the model produced. Those DBs still carry
-- `tenant_redirect_uri_allowlist_tenant_id_pattern_key`, which blocks
-- ApplyIDEPreset for IDEs whose patterns overlap an already-applied
-- preset (Kiro/Windsurf vs Claude Code share http://localhost:*/callback).
--
-- Drop the constraint unconditionally if present. Fresh installs are
-- unaffected because 00010 no longer creates it.
ALTER TABLE IF EXISTS tenant_redirect_uri_allowlist
DROP CONSTRAINT IF EXISTS tenant_redirect_uri_allowlist_tenant_id_pattern_key;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Intentionally empty: restoring the legacy constraint would reintroduce
-- the bug it was created to mask.
SELECT 1;
-- +goose StatementEnd
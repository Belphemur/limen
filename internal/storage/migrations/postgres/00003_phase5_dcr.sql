-- +goose Up
-- +goose StatementBegin
-- Phase 5 — DCR proxy schema fix-up.
--
-- AutoMigrate (run before this migration) already added:
--   - tenants.dcr_redirect_uri_allowlist jsonb NOT NULL DEFAULT '[]'
--   - zitadel_apps.registration_access_token_hash bytea NOT NULL
--
-- This migration cleans up the original (encrypted) registration_access_token
-- column on zitadel_apps. The DCR design switched to a SHA-256 hash (Phase 5
-- spec: "Hashing is sufficient on its own; we don't double-wrap with AES-SIV
-- because the row already isn't reversible to a usable credential.") so the
-- old column is dead weight that would otherwise keep leaking AAD-shaped
-- payloads around.
ALTER TABLE zitadel_apps
DROP COLUMN IF EXISTS registration_access_token;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Re-add the column as nullable bytea; previously encrypted contents are not
-- recoverable, so a downgrade only restores the schema shape.
ALTER TABLE zitadel_apps
ADD COLUMN IF NOT EXISTS registration_access_token bytea;
-- +goose StatementEnd
-- +goose Up
-- +goose StatementBegin
-- Phase 9f — per-tenant redirect-URI allowlist as a relation.
--
-- The table itself is created by GORM AutoMigrate from
-- storage.TenantRedirectURIAllowlist. This migration owns RLS + grants
-- + the (tenant_id, pattern) UNIQUE + the column drop from `tenants`
-- that replaces the flat JSONB allowlist.
ALTER TABLE tenant_redirect_uri_allowlist ENABLE ROW LEVEL SECURITY;

ALTER TABLE tenant_redirect_uri_allowlist FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON tenant_redirect_uri_allowlist;

CREATE POLICY tenant_isolation ON tenant_redirect_uri_allowlist USING (
    tenant_id = current_setting('app.current_tenant', true)::bigint
)
WITH
    CHECK (
        tenant_id = current_setting('app.current_tenant', true)::bigint
    );
-- +goose StatementEnd

-- +goose StatementBegin
-- Uniqueness rules:
--   * Preset rows (ide_key IS NOT NULL): unique per (tenant, ide_key,
--     pattern). The same pattern is intentionally allowed under
--     multiple presets — e.g. Claude Code / Windsurf / Kiro all
--     declare http://localhost:*/callback, so each preset owns its
--     own row and removing one preset doesn't drop the others.
--   * Custom rows (ide_key IS NULL): unique per (tenant, pattern).
--     Admins should never get two free-form rows for the same URI.
DO $$
BEGIN
    -- Drop the legacy (tenant_id, pattern) constraint if a previous
    -- run of this migration installed it.
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'tenant_redirect_uri_allowlist_tenant_id_pattern_key'
    ) THEN
        ALTER TABLE tenant_redirect_uri_allowlist
            DROP CONSTRAINT tenant_redirect_uri_allowlist_tenant_id_pattern_key;
    END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS tenant_redirect_uri_allowlist_preset_key
    ON tenant_redirect_uri_allowlist (tenant_id, ide_key, pattern)
    WHERE ide_key IS NOT NULL AND deleted_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS tenant_redirect_uri_allowlist_custom_key
    ON tenant_redirect_uri_allowlist (tenant_id, pattern)
    WHERE ide_key IS NULL AND deleted_at IS NULL;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'limen_app') THEN
        EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON tenant_redirect_uri_allowlist TO limen_app';
        EXECUTE 'GRANT USAGE, SELECT ON SEQUENCE tenant_redirect_uri_allowlist_id_seq TO limen_app';
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
-- Replaces the flat JSONB allowlist on `tenants`. Single-commit cutover
-- per AGENTS.md — no data preservation, dev installs rebuild from
-- `make dev-reset`.
ALTER TABLE tenants DROP COLUMN IF EXISTS dcr_redirect_uri_allowlist;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP POLICY IF EXISTS tenant_isolation ON tenant_redirect_uri_allowlist;

ALTER TABLE IF EXISTS tenant_redirect_uri_allowlist NO FORCE ROW LEVEL SECURITY;

ALTER TABLE IF EXISTS tenant_redirect_uri_allowlist DISABLE ROW LEVEL SECURITY;

ALTER TABLE IF EXISTS tenant_redirect_uri_allowlist
DROP CONSTRAINT IF EXISTS tenant_redirect_uri_allowlist_tenant_id_pattern_key;

DROP INDEX IF EXISTS tenant_redirect_uri_allowlist_preset_key;

DROP INDEX IF EXISTS tenant_redirect_uri_allowlist_custom_key;
-- We do NOT restore tenants.dcr_redirect_uri_allowlist on down — the
-- new relational table is the source of truth from here on out.
-- +goose StatementEnd
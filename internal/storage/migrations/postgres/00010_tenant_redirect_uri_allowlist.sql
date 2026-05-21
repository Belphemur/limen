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
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'tenant_redirect_uri_allowlist_tenant_id_pattern_key'
    ) THEN
        ALTER TABLE tenant_redirect_uri_allowlist
            ADD CONSTRAINT tenant_redirect_uri_allowlist_tenant_id_pattern_key
            UNIQUE (tenant_id, pattern);

END IF;

END $$;
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
-- We do NOT restore tenants.dcr_redirect_uri_allowlist on down — the
-- new relational table is the source of truth from here on out.
-- +goose StatementEnd
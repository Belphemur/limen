-- +goose Up
-- +goose StatementBegin

-- Phase 9l — tenant-level credentials.
-- Table is created by GORM AutoMigrate via UpstreamTenantLink in AllModels().
-- This migration adds what AutoMigrate cannot express:
--   1. Row-Level Security (tenant_isolation) matching 00001_rls.sql.
--   2. updated_at trigger matching 00002_audit_triggers.sql.
--   3. Partial unique index: one link per (tenant, upstream).
--   4. Refresh sweep index for expiring tokens.
--   5. (moved to 00016_dev_wipe_old_links.sql)

-- RLS — ENABLE + FORCE + tenant_isolation policy
ALTER TABLE upstream_tenant_links ENABLE ROW LEVEL SECURITY;

ALTER TABLE upstream_tenant_links FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON upstream_tenant_links;

CREATE POLICY tenant_isolation ON upstream_tenant_links USING (
    tenant_id = current_setting('app.current_tenant', TRUE)::BIGINT
)
WITH
    CHECK (
        tenant_id = current_setting('app.current_tenant', TRUE)::BIGINT
    );

-- Updated-at trigger
DROP TRIGGER IF EXISTS trg_upstream_tenant_links_set_updated_at ON upstream_tenant_links;

CREATE TRIGGER trg_upstream_tenant_links_set_updated_at
    BEFORE UPDATE ON upstream_tenant_links
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

-- Partial unique: one link per (tenant, upstream), ignoring soft-deletes.
CREATE UNIQUE INDEX IF NOT EXISTS idx_tenant_links_unique
    ON upstream_tenant_links (tenant_id, upstream_id)
    WHERE deleted_at IS NULL;

-- Refresh sweep: find tenant links whose tokens expire soon.
CREATE INDEX IF NOT EXISTS idx_tenant_links_refresh
    ON upstream_tenant_links (expires_at, needs_relink, auto_disabled_at, enabled)
    WHERE deleted_at IS NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS trg_upstream_tenant_links_set_updated_at ON upstream_tenant_links;
DROP POLICY IF EXISTS tenant_isolation ON upstream_tenant_links;
ALTER TABLE upstream_tenant_links NO FORCE ROW LEVEL SECURITY;
ALTER TABLE upstream_tenant_links DISABLE ROW LEVEL SECURITY;
DROP INDEX IF EXISTS idx_tenant_links_unique;
DROP INDEX IF EXISTS idx_tenant_links_refresh;
-- +goose StatementEnd

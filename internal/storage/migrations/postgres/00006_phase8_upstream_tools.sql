-- +goose Up
-- +goose StatementBegin
-- Phase 8 — per-upstream tool catalog.
--
-- AutoMigrate created the upstream_tools table with the (tenant_id,
-- upstream_id, name) partial unique index. This migration layers the
-- pieces AutoMigrate can't express: row-level security keyed on the
-- app.current_tenant GUC and the updated_at trigger that matches every
-- other tenant-scoped table (00001_rls, 00002_audit_triggers).
ALTER TABLE upstream_tools ENABLE ROW LEVEL SECURITY;

ALTER TABLE upstream_tools FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON upstream_tools;

CREATE POLICY tenant_isolation ON upstream_tools USING (
    tenant_id = current_setting('app.current_tenant', TRUE)::BIGINT
)
WITH
    CHECK (
        tenant_id = current_setting('app.current_tenant', TRUE)::BIGINT
    );

DROP TRIGGER IF EXISTS trg_upstream_tools_set_updated_at ON upstream_tools;

CREATE TRIGGER trg_upstream_tools_set_updated_at BEFORE
UPDATE ON upstream_tools FOR EACH ROW
EXECUTE FUNCTION set_updated_at ();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS trg_upstream_tools_set_updated_at ON upstream_tools;

DROP POLICY IF EXISTS tenant_isolation ON upstream_tools;

ALTER TABLE upstream_tools NO FORCE ROW LEVEL SECURITY;

ALTER TABLE upstream_tools DISABLE ROW LEVEL SECURITY;
-- +goose StatementEnd
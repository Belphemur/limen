-- +goose Up
-- +goose StatementBegin
-- Phase 9c slice 3 — tenant_settings RLS.
--
-- The table itself is created by GORM AutoMigrate from
-- storage.TenantSettings (see internal/storage/model_tenant_settings.go).
-- This migration only owns the RLS posture + runtime grants, mirroring
-- 00001_rls.sql one-for-one.
ALTER TABLE tenant_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_settings FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON tenant_settings;
CREATE POLICY tenant_isolation ON tenant_settings
    USING (tenant_id = current_setting('app.current_tenant', true)::bigint)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::bigint);
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'limen_app') THEN
        EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON tenant_settings TO limen_app';
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP POLICY IF EXISTS tenant_isolation ON tenant_settings;
ALTER TABLE IF EXISTS tenant_settings NO FORCE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS tenant_settings DISABLE ROW LEVEL SECURITY;
-- +goose StatementEnd

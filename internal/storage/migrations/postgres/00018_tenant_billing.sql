-- +goose Up
-- +goose StatementBegin
-- Phase 13c-b — Tenant billing & entitlements DB layer.
--
-- AutoMigrate created the tenant_billing and tenant_entitlements tables
-- from the TenantBilling and TenantEntitlement models. This migration
-- layers the pieces AutoMigrate can't express: RLS and the updated_at
-- trigger for both tables.

-- tenant_billing
ALTER TABLE tenant_billing ENABLE ROW LEVEL SECURITY;

ALTER TABLE tenant_billing FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON tenant_billing;

CREATE POLICY tenant_isolation ON tenant_billing USING (
    tenant_id = current_setting('app.current_tenant', TRUE)::BIGINT
)
WITH
    CHECK (
        tenant_id = current_setting('app.current_tenant', TRUE)::BIGINT
    );

DROP TRIGGER IF EXISTS trg_tenant_billing_set_updated_at ON tenant_billing;

CREATE TRIGGER trg_tenant_billing_set_updated_at BEFORE
UPDATE ON tenant_billing FOR EACH ROW
EXECUTE FUNCTION set_updated_at ();

-- tenant_entitlements
ALTER TABLE tenant_entitlements ENABLE ROW LEVEL SECURITY;

ALTER TABLE tenant_entitlements FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON tenant_entitlements;

CREATE POLICY tenant_isolation ON tenant_entitlements USING (
    tenant_id = current_setting('app.current_tenant', TRUE)::BIGINT
)
WITH
    CHECK (
        tenant_id = current_setting('app.current_tenant', TRUE)::BIGINT
    );

DROP TRIGGER IF EXISTS trg_tenant_entitlements_set_updated_at ON tenant_entitlements;

CREATE TRIGGER trg_tenant_entitlements_set_updated_at BEFORE
UPDATE ON tenant_entitlements FOR EACH ROW
EXECUTE FUNCTION set_updated_at ();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Revert tenant billing & entitlements.

-- tenant_entitlements
DROP TRIGGER IF EXISTS trg_tenant_entitlements_set_updated_at ON tenant_entitlements;

DROP POLICY IF EXISTS tenant_isolation ON tenant_entitlements;

ALTER TABLE tenant_entitlements NO FORCE ROW LEVEL SECURITY;

ALTER TABLE tenant_entitlements DISABLE ROW LEVEL SECURITY;

-- tenant_billing
DROP TRIGGER IF EXISTS trg_tenant_billing_set_updated_at ON tenant_billing;

DROP POLICY IF EXISTS tenant_isolation ON tenant_billing;

ALTER TABLE tenant_billing NO FORCE ROW LEVEL SECURITY;

ALTER TABLE tenant_billing DISABLE ROW LEVEL SECURITY;
-- +goose StatementEnd

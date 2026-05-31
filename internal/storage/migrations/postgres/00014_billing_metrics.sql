-- +goose Up
-- +goose StatementBegin
-- Phase 13b — Billing metrics pipeline.
--
-- AutoMigrate created the active_user_months and sa_connection_snapshots
-- tables from the ActiveUserMonth and SAConnectionSnapshot models. This
-- migration layers the pieces AutoMigrate can't express: RLS, the
-- updated_at trigger, the partial unique index on active_user_months,
-- and the composite index on sa_connection_snapshots.

-- active_user_months
ALTER TABLE active_user_months ENABLE ROW LEVEL SECURITY;

ALTER TABLE active_user_months FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON active_user_months;

CREATE POLICY tenant_isolation ON active_user_months USING (
    tenant_id = current_setting('app.current_tenant', TRUE)::BIGINT
)
WITH
    CHECK (
        tenant_id = current_setting('app.current_tenant', TRUE)::BIGINT
    );

DROP TRIGGER IF EXISTS trg_active_user_months_set_updated_at ON active_user_months;

CREATE TRIGGER trg_active_user_months_set_updated_at BEFORE
UPDATE ON active_user_months FOR EACH ROW
EXECUTE FUNCTION set_updated_at ();

-- AutoMigrate created a plain unique index; replace it with a partial one.
DROP INDEX IF EXISTS idx_aum_unique;

CREATE UNIQUE INDEX idx_aum_unique
    ON active_user_months (tenant_id, month_start, user_id, service_account_id)
    NULLS NOT DISTINCT
    WHERE deleted_at IS NULL;

-- sa_connection_snapshots
ALTER TABLE sa_connection_snapshots ENABLE ROW LEVEL SECURITY;

ALTER TABLE sa_connection_snapshots FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON sa_connection_snapshots;

CREATE POLICY tenant_isolation ON sa_connection_snapshots USING (
    tenant_id = current_setting('app.current_tenant', TRUE)::BIGINT
)
WITH
    CHECK (
        tenant_id = current_setting('app.current_tenant', TRUE)::BIGINT
    );

DROP TRIGGER IF EXISTS trg_sa_connection_snapshots_set_updated_at ON sa_connection_snapshots;

CREATE TRIGGER trg_sa_connection_snapshots_set_updated_at BEFORE
UPDATE ON sa_connection_snapshots FOR EACH ROW
EXECUTE FUNCTION set_updated_at ();

-- Composite index for tenant-scoped time-range queries.
DROP INDEX IF EXISTS idx_sacs_tenant_month;

CREATE INDEX idx_sacs_tenant_month
    ON sa_connection_snapshots (tenant_id, connected_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Revert billing metrics pipeline.

-- sa_connection_snapshots
DROP INDEX IF EXISTS idx_sacs_tenant_month;

DROP TRIGGER IF EXISTS trg_sa_connection_snapshots_set_updated_at ON sa_connection_snapshots;

DROP POLICY IF EXISTS tenant_isolation ON sa_connection_snapshots;

ALTER TABLE sa_connection_snapshots NO FORCE ROW LEVEL SECURITY;

ALTER TABLE sa_connection_snapshots DISABLE ROW LEVEL SECURITY;

DROP TABLE IF EXISTS sa_connection_snapshots;

-- active_user_months
DROP INDEX IF EXISTS idx_aum_unique;

DROP TRIGGER IF EXISTS trg_active_user_months_set_updated_at ON active_user_months;

DROP POLICY IF EXISTS tenant_isolation ON active_user_months;

ALTER TABLE active_user_months NO FORCE ROW LEVEL SECURITY;

ALTER TABLE active_user_months DISABLE ROW LEVEL SECURITY;

DROP TABLE IF EXISTS active_user_months;
-- +goose StatementEnd

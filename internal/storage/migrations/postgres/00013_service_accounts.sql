-- +goose Up
-- +goose StatementBegin
-- Phase 9i — service accounts and API tokens.
--
-- AutoMigrate created the service_accounts table from the ServiceAccount
-- model. This migration layers the pieces AutoMigrate can't express: RLS,
-- the updated_at trigger, partial unique indexes on service_accounts,
-- and the upstream_links XOR constraint with split partial unique indexes.

ALTER TABLE service_accounts ENABLE ROW LEVEL SECURITY;

ALTER TABLE service_accounts FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON service_accounts;

CREATE POLICY tenant_isolation ON service_accounts USING (
    tenant_id = current_setting('app.current_tenant', TRUE)::BIGINT
)
WITH
    CHECK (
        tenant_id = current_setting('app.current_tenant', TRUE)::BIGINT
    );

DROP TRIGGER IF EXISTS trg_service_accounts_set_updated_at ON service_accounts;

CREATE TRIGGER trg_service_accounts_set_updated_at BEFORE
UPDATE ON service_accounts FOR EACH ROW
EXECUTE FUNCTION set_updated_at ();

-- Partial unique: one active SA per (tenant, zitadel_user_id).
-- AutoMigrate created a plain unique index; replace it with a partial one.
DROP INDEX IF EXISTS idx_sa_tenant_zitadel;

CREATE UNIQUE INDEX idx_sa_tenant_zitadel
    ON service_accounts (tenant_id, zitadel_user_id)
    WHERE deleted_at IS NULL;

-- UpstreamLink: add service_account_id, enforce XOR with user_id.
ALTER TABLE upstream_links
    ADD COLUMN service_account_id BIGINT;

ALTER TABLE upstream_links
    ADD CONSTRAINT fk_link_service_account
        FOREIGN KEY (service_account_id) REFERENCES service_accounts(id) ON DELETE CASCADE;

-- XOR: exactly one of user_id / service_account_id must be set.
-- All existing rows have user_id set and service_account_id NULL, so
-- this CHECK passes for existing data without a data migration.
ALTER TABLE upstream_links
    ADD CONSTRAINT chk_link_owner_xor
        CHECK ((user_id IS NULL) <> (service_account_id IS NULL));

-- Replace the old composite unique index with two partial indexes
-- covering each owner type.
DROP INDEX IF EXISTS idx_link_tenant_user_upstream;

CREATE UNIQUE INDEX idx_link_tenant_user_upstream
    ON upstream_links (tenant_id, user_id, upstream_id)
    WHERE deleted_at IS NULL AND user_id IS NOT NULL;

CREATE UNIQUE INDEX idx_link_tenant_sa_upstream
    ON upstream_links (tenant_id, service_account_id, upstream_id)
    WHERE deleted_at IS NULL AND service_account_id IS NOT NULL;

-- Drop redundant single-column unique indexes created by AutoMigrate.
-- The model tags uniqueIndex:idx_link_tenant_user_id_upstream and
-- uniqueIndex:idx_link_tenant_sa_id_upstream cause AutoMigrate to create
-- single-column indexes; the composite indexes above replace them.
DROP INDEX IF EXISTS idx_link_tenant_user_id_upstream;
DROP INDEX IF EXISTS idx_link_tenant_sa_id_upstream;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Remove service account support and revert to user-only upstream links.

DELETE FROM upstream_links WHERE service_account_id IS NOT NULL;

DROP INDEX IF EXISTS idx_link_tenant_sa_upstream;

DROP INDEX IF EXISTS idx_link_tenant_user_upstream;

ALTER TABLE upstream_links
    DROP CONSTRAINT IF EXISTS chk_link_owner_xor;

ALTER TABLE upstream_links
    DROP CONSTRAINT IF EXISTS fk_link_service_account;

ALTER TABLE upstream_links
    DROP COLUMN IF EXISTS service_account_id;

-- Restore the original composite unique index.
CREATE UNIQUE INDEX idx_link_tenant_user_upstream
    ON upstream_links (tenant_id, user_id, upstream_id)
    WHERE deleted_at IS NULL;

-- Recreate the single-column unique indexes that AutoMigrate would expect.
CREATE UNIQUE INDEX idx_link_tenant_user_id_upstream
    ON upstream_links (user_id)
    WHERE deleted_at IS NULL AND user_id IS NOT NULL;

-- Restore user_id to NOT NULL (all existing rows have it set).
ALTER TABLE upstream_links
    ALTER COLUMN user_id SET NOT NULL;

-- Drop service_accounts RLS, trigger, and table.
DROP TRIGGER IF EXISTS trg_service_accounts_set_updated_at ON service_accounts;

DROP POLICY IF EXISTS tenant_isolation ON service_accounts;

ALTER TABLE service_accounts NO FORCE ROW LEVEL SECURITY;

ALTER TABLE service_accounts DISABLE ROW LEVEL SECURITY;

DROP TABLE IF EXISTS service_accounts;
-- +goose StatementEnd

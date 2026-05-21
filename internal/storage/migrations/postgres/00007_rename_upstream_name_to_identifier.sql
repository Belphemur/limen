-- +goose Up
-- +goose StatementBegin
-- Rename the upstream slug column from `name` to `identifier`.
--
-- `name` was ambiguous (the user-facing label is `display_name`); the slug
-- is really an identifier and we now align Go, proto, and SPA naming on
-- that. AutoMigrate would re-create the column under the new tag rather
-- than rename, so the rename has to live here. The partial unique index
-- gets renamed in lock-step so its name keeps describing its key.
--
-- Idempotent: on fresh databases AutoMigrate already creates the column
-- under its new name (`identifier`), so the rename only fires when an
-- older `name` column is still present.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'upstreams' AND column_name = 'name'
    ) THEN
        EXECUTE 'ALTER TABLE upstreams RENAME COLUMN name TO identifier';
    END IF;
END
$$;

ALTER INDEX IF EXISTS idx_upstream_tenant_name RENAME TO idx_upstream_tenant_identifier;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER INDEX IF EXISTS idx_upstream_tenant_identifier RENAME TO idx_upstream_tenant_name;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'upstreams' AND column_name = 'identifier'
    ) THEN
        EXECUTE 'ALTER TABLE upstreams RENAME COLUMN identifier TO name';
    END IF;
END
$$;
-- +goose StatementEnd

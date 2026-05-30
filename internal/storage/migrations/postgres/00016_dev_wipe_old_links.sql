-- +goose Up
-- +goose StatementBegin

-- Phase 9l — dev-mode cleanup of per-user links/configs/registrations
-- being replaced by the tenant-level model.
--
-- WARNING: This migration is deliberately irreversible. It deletes
-- production data that cannot be reconstructed from other tables.
-- Run only in development / pre-launch environments.

DELETE FROM upstream_links WHERE upstream_id IN (
    SELECT u.id FROM upstreams u WHERE u.strategy_type IN ('mcp_spec', 'static_header')
);
DELETE FROM upstream_strategy_configs WHERE upstream_id IN (
    SELECT u.id FROM upstreams u WHERE u.strategy_type IN ('static_header')
);
DELETE FROM upstream_registrations WHERE upstream_id IN (
    SELECT u.id FROM upstreams u WHERE u.strategy_type = 'mcp_spec'
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- This migration is irreversible. The data deleted by the Up block
-- cannot be reconstructed. Re-seed the database from scratch if you
-- need to undo this migration.
SELECT 1;
-- +goose StatementEnd

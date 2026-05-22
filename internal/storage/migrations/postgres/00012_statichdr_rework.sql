-- +goose Up
-- +goose StatementBegin
-- Phase 9g — static_header rework. The strategy config payload moved from
-- a bimodal {Mode: "tenant"|"user", TenantSecret?: ...} JSON to a single
-- shape {SharedSecret, AllowUserOverride}. The payload lives encrypted in
-- upstream_strategy_configs.config_json (AES-SIV blob, opaque to Postgres),
-- so we cannot rewrite it in SQL — the only correct migration is to drop
-- every static_header upstream and have admins recreate them.
--
-- This is acceptable per AGENTS.md: "Limen has no external users yet.
-- Breaking changes are accepted and expected." No production data exists.
--
-- Order matters even with ON DELETE CASCADE because we keep references
-- explicit for the audit trail; cascades handle upstream_tools,
-- upstream_registrations, and any orphaned upstream_links automatically.
DELETE FROM upstream_strategy_configs WHERE type = 'static_header';

DELETE FROM upstream_links
WHERE
    upstream_id IN (
        SELECT id
        FROM upstreams
        WHERE
            strategy_type = 'static_header'
    );

DELETE FROM upstreams WHERE strategy_type = 'static_header';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Intentionally empty: the deleted rows carried AES-SIV-encrypted payloads
-- whose AAD bound them to the now-removed Mode field. Recreating them via
-- SQL would require the encryption key + the old plaintexts; both are out
-- of reach. Admins recreate the upstream via the SPA — same pattern as 00011.
SELECT 1;
-- +goose StatementEnd
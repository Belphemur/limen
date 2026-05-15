-- +goose Up
-- +goose StatementBegin
-- Phase 7b — DCR per-client Zitadel projects.
--
-- Each DCR-registered MCP client now lives in its own Zitadel project
-- under the tenant organization (instead of Limen's shared gateway
-- project). The mirror row needs to remember which project hosts the
-- app so RFC 7592 management requests can address it. See
-- docs/phases/phase-07b-dcr-per-client-project.md.
ALTER TABLE zitadel_apps
ADD COLUMN IF NOT EXISTS zitadel_project_id text NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE zitadel_apps DROP COLUMN IF EXISTS zitadel_project_id;
-- +goose StatementEnd
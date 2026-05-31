-- +goose Up
-- +goose StatementBegin
-- Phase 9g+ — promote mode from encrypted ConfigJSON to a dedicated DB column.
-- AutoMigrate adds the column with NOT NULL DEFAULT 'shared', so existing rows
-- automatically get the safe default (shared). The previous mode for override
-- rows is lost, but Limen has no external users yet and the admin can recreate
-- with the correct mode.
ALTER TABLE upstream_strategy_configs
    ADD COLUMN IF NOT EXISTS mode TEXT NOT NULL DEFAULT 'shared';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE upstream_strategy_configs DROP COLUMN IF EXISTS mode;
-- +goose StatementEnd

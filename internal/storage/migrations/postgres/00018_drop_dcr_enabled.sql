-- +goose Up
ALTER TABLE tenants DROP COLUMN IF EXISTS dcr_enabled;

-- +goose Down
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS dcr_enabled boolean NOT NULL DEFAULT false;

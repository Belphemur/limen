-- +goose Up
ALTER TABLE tenants DROP COLUMN IF EXISTS dcr_enabled;

-- +goose Down
-- Re-adds dcr_enabled as NOT NULL DEFAULT false. All existing tenants
-- will have DCR disabled after rollback — this is the secure default.
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS dcr_enabled boolean NOT NULL DEFAULT false;

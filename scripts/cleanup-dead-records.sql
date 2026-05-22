-- Cleanup of dead/orphan rows produced by GORM soft-delete.
--
-- Soft-deleting a parent (sets deleted_at) does NOT fire Postgres FK
-- CASCADE on its children — children keep pointing at a row the app
-- can no longer see. This script:
--
--   1. Hard-deletes children whose parent row is soft-deleted or gone.
--   2. Hard-deletes any remaining soft-deleted rows so FK cascade can
--      do its job for newly-added Tenant/Upstream constraints.
--
-- Idempotent. Wrap in one transaction so a failure leaves no partial
-- state behind.
--
-- Usage:
--   docker exec -i limen-dev-limen-postgres-1 \
--     psql -U limen_admin -d limen -v ON_ERROR_STOP=1 \
--     < scripts/cleanup-dead-records.sql
--
-- NB: connects as limen_admin (BYPASSRLS), NOT the app role, so the
-- DELETEs see every tenant's rows.

BEGIN;

-- 1. Children of soft-deleted / missing upstreams.
DELETE FROM upstream_links     l
 USING upstreams u
 WHERE l.upstream_id = u.id AND u.deleted_at IS NOT NULL;
DELETE FROM upstream_links     l
 WHERE NOT EXISTS (SELECT 1 FROM upstreams u WHERE u.id = l.upstream_id);

DELETE FROM upstream_tools     t
 USING upstreams u
 WHERE t.upstream_id = u.id AND u.deleted_at IS NOT NULL;
DELETE FROM upstream_tools     t
 WHERE NOT EXISTS (SELECT 1 FROM upstreams u WHERE u.id = t.upstream_id);

DELETE FROM upstream_registrations r
 USING upstreams u
 WHERE r.upstream_id = u.id AND u.deleted_at IS NOT NULL;
DELETE FROM upstream_registrations r
 WHERE NOT EXISTS (SELECT 1 FROM upstreams u WHERE u.id = r.upstream_id);

DELETE FROM upstream_strategy_configs c
 USING upstreams u
 WHERE c.upstream_id = u.id AND u.deleted_at IS NOT NULL;
DELETE FROM upstream_strategy_configs c
 WHERE NOT EXISTS (SELECT 1 FROM upstreams u WHERE u.id = c.upstream_id);

-- 2. Children of soft-deleted / missing users.
DELETE FROM upstream_links     l
 USING users us
 WHERE l.user_id = us.id AND us.deleted_at IS NOT NULL;
DELETE FROM upstream_links     l
 WHERE NOT EXISTS (SELECT 1 FROM users us WHERE us.id = l.user_id);

-- 3. Children of soft-deleted / missing tenants.
DELETE FROM users                          x USING tenants t WHERE x.tenant_id = t.id AND t.deleted_at IS NOT NULL;
DELETE FROM upstreams                      x USING tenants t WHERE x.tenant_id = t.id AND t.deleted_at IS NOT NULL;
DELETE FROM upstream_strategy_configs      x USING tenants t WHERE x.tenant_id = t.id AND t.deleted_at IS NOT NULL;
DELETE FROM upstream_registrations         x USING tenants t WHERE x.tenant_id = t.id AND t.deleted_at IS NOT NULL;
DELETE FROM upstream_links                 x USING tenants t WHERE x.tenant_id = t.id AND t.deleted_at IS NOT NULL;
DELETE FROM upstream_tools                 x USING tenants t WHERE x.tenant_id = t.id AND t.deleted_at IS NOT NULL;
DELETE FROM zitadel_apps                   x USING tenants t WHERE x.tenant_id = t.id AND t.deleted_at IS NOT NULL;
DELETE FROM tenant_settings                x USING tenants t WHERE x.tenant_id = t.id AND t.deleted_at IS NOT NULL;
DELETE FROM tenant_redirect_uri_allowlist  x USING tenants t WHERE x.tenant_id = t.id AND t.deleted_at IS NOT NULL;

-- 4. Children of missing IDE presets (allowlist FK is SET NULL by design).
-- IDE presets are seed-only and have no soft-delete column.
UPDATE tenant_redirect_uri_allowlist a
   SET ide_key = NULL
 WHERE a.ide_key IS NOT NULL
   AND NOT EXISTS (SELECT 1 FROM ide_presets p WHERE p.key = a.ide_key);

DELETE FROM ide_preset_patterns pat
 WHERE NOT EXISTS (SELECT 1 FROM ide_presets p WHERE p.key = pat.ide_key);

-- 5. Finally, hard-delete the soft-deleted parents themselves.
DELETE FROM upstream_links            WHERE deleted_at IS NOT NULL;
DELETE FROM upstream_tools            WHERE deleted_at IS NOT NULL;
DELETE FROM upstream_registrations    WHERE deleted_at IS NOT NULL;
DELETE FROM upstream_strategy_configs WHERE deleted_at IS NOT NULL;
DELETE FROM upstreams                 WHERE deleted_at IS NOT NULL;
DELETE FROM users                     WHERE deleted_at IS NOT NULL;
DELETE FROM zitadel_apps              WHERE deleted_at IS NOT NULL;
DELETE FROM tenant_settings           WHERE deleted_at IS NOT NULL;
DELETE FROM tenant_redirect_uri_allowlist WHERE deleted_at IS NOT NULL;
DELETE FROM tenants                   WHERE deleted_at IS NOT NULL;

COMMIT;

-- Quick post-cleanup sanity counts.
SELECT 'tenants'                   AS table, COUNT(*) FROM tenants
UNION ALL SELECT 'users',                    COUNT(*) FROM users
UNION ALL SELECT 'upstreams',                COUNT(*) FROM upstreams
UNION ALL SELECT 'upstream_links',           COUNT(*) FROM upstream_links
UNION ALL SELECT 'upstream_tools',           COUNT(*) FROM upstream_tools
UNION ALL SELECT 'upstream_registrations',   COUNT(*) FROM upstream_registrations
UNION ALL SELECT 'upstream_strategy_configs',COUNT(*) FROM upstream_strategy_configs;

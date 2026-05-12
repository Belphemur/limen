-- Phase 3 — Row-Level Security
--
-- Enables RLS + FORCE on every tenant-scoped table and installs a single
-- tenant_isolation policy that keys off the app.current_tenant GUC pinned by
-- storage.Session(ctx). NULL-safe form (current_setting(name, true)) so an
-- unset GUC blocks all rows rather than raising.
--
-- Idempotent: ALTER TABLE … ENABLE/FORCE is a no-op when already on, and the
-- policy is dropped + re-created so signature changes propagate cleanly.

DO $$
DECLARE
  t text;
  tables text[] := ARRAY[
    'users',
    'upstreams',
    'upstream_strategy_configs',
    'upstream_registrations',
    'upstream_links',
    'zitadel_apps'
  ];
BEGIN
  FOREACH t IN ARRAY tables LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
    EXECUTE format(
      'CREATE POLICY tenant_isolation ON %I '
      || 'USING (tenant_id = current_setting(''app.current_tenant'', true)::bigint) '
      || 'WITH CHECK (tenant_id = current_setting(''app.current_tenant'', true)::bigint)',
      t
    );
  END LOOP;
END
$$;

-- Grant runtime DML to limen_app on the tenant-scoped tables (and tenants,
-- which is read-only for the resolver). The role itself is provisioned out of
-- band (see deploy/postgres/limen-init.sql in Phase 11 / docs/runbook.md).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'limen_app') THEN
    EXECUTE 'GRANT USAGE ON SCHEMA public TO limen_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO limen_app';
    EXECUTE 'GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO limen_app';
    EXECUTE 'ALTER DEFAULT PRIVILEGES IN SCHEMA public '
         || 'GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO limen_app';
    EXECUTE 'ALTER DEFAULT PRIVILEGES IN SCHEMA public '
         || 'GRANT USAGE, SELECT ON SEQUENCES TO limen_app';
  END IF;
END
$$;
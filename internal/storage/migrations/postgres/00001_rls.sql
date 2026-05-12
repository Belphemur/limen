-- +goose Up
-- +goose StatementBegin
-- Phase 3 — Row-Level Security.
--
-- Enables RLS + FORCE on every tenant-scoped table and installs a single
-- tenant_isolation policy that keys off the app.current_tenant GUC pinned by
-- storage.Session(ctx). NULL-safe form (current_setting(name, true)) so an
-- unset GUC blocks all rows rather than raising.
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
-- +goose StatementEnd

-- +goose StatementBegin
-- Grant runtime DML to limen_app on the tenant-scoped tables (and tenants,
-- which is read-only for the resolver). The role itself is provisioned out
-- of band (scripts/postgres-init/limen-roles.sql in dev,
-- deploy/postgres/limen-init.sql in prod / Phase 11) because CREATE ROLE is
-- a cluster-level operation that can't live inside a database-scoped
-- migration. We fail loudly here if the role is missing — silently skipping
-- the GRANTs would let the app boot and only blow up on the first request.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'limen_app') THEN
    RAISE EXCEPTION
      'limen_app role missing — provision it before running migrations '
      '(see scripts/postgres-init/limen-roles.sql or deploy/postgres/limen-init.sql)';
  END IF;
  EXECUTE 'GRANT USAGE ON SCHEMA public TO limen_app';
  EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO limen_app';
  EXECUTE 'GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO limen_app';
  EXECUTE 'ALTER DEFAULT PRIVILEGES IN SCHEMA public '
       || 'GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO limen_app';
  EXECUTE 'ALTER DEFAULT PRIVILEGES IN SCHEMA public '
       || 'GRANT USAGE, SELECT ON SEQUENCES TO limen_app';
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
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
    EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
    EXECUTE format('ALTER TABLE %I NO FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I DISABLE ROW LEVEL SECURITY', t);
  END LOOP;
END
$$;
-- +goose StatementEnd
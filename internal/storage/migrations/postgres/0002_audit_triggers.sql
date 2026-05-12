-- Phase 3 — Audit-column trigger
--
-- set_updated_at() bumps NEW.updated_at on every UPDATE, so the column is
-- truthful regardless of which client issues the statement (GORM, psql, an
-- ad-hoc admin script, etc.). Composes with GORM soft-deletes: the
-- "DELETE" issued by GORM is in fact an UPDATE … SET deleted_at = …, which
-- fires the trigger and bumps updated_at on the same statement — exactly
-- the desired behavior.

CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$$;

DO $$
DECLARE
  t text;
  tables text[] := ARRAY[
    'tenants',
    'users',
    'upstreams',
    'upstream_strategy_configs',
    'upstream_registrations',
    'upstream_links',
    'zitadel_apps'
  ];
BEGIN
  FOREACH t IN ARRAY tables LOOP
    EXECUTE format('DROP TRIGGER IF EXISTS trg_%I_set_updated_at ON %I', t, t);
    EXECUTE format(
      'CREATE TRIGGER trg_%I_set_updated_at BEFORE UPDATE ON %I '
      || 'FOR EACH ROW EXECUTE FUNCTION set_updated_at()',
      t, t
    );
  END LOOP;
END
$$;
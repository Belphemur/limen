-- +goose Up
-- +goose StatementBegin
-- set_updated_at() bumps NEW.updated_at on every UPDATE so the column is
-- truthful regardless of the client. Composes with GORM soft-deletes (which
-- are UPDATE … SET deleted_at = …): the same statement bumps updated_at.
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
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
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
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
  END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DROP FUNCTION IF EXISTS set_updated_at ();
-- +goose StatementEnd
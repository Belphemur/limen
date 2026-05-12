-- Dev-only: provisions the runtime roles Phase 3 RLS expects.
-- Mounted by compose.dev.yaml into the postgres init.d directory and runs on
-- first container start. The production equivalent ships in Phase 11
-- (deploy/postgres/limen-init.sql) and is documented in docs/runbook.md.
--
-- Passwords here are dev placeholders. Do not reuse in production.

CREATE ROLE limen_admin LOGIN PASSWORD 'limen_admin_dev' BYPASSRLS;

CREATE ROLE limen_app LOGIN PASSWORD 'limen_app_dev';

GRANT limen_app TO limen_admin;

GRANT ALL PRIVILEGES ON DATABASE limen TO limen_admin;

GRANT CREATE, USAGE ON SCHEMA public TO limen_admin;

ALTER SCHEMA public OWNER TO limen_admin;
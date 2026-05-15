# Phase 3 — PostgreSQL Row-Level Security

**Depends on**: Phase 1 (models + `Session(ctx)` contract)
**Unblocks**: Phase 4 (and everything downstream — RLS must be in place before tenant routing exposes shared endpoints)

## Goal

Make `tenant_id` enforcement a property of the database, not a property of the Go code. Every tenant-scoped table runs under **row-level security** with `FORCE ROW LEVEL SECURITY`, and Limen connects as a non-superuser role that cannot bypass policies. The Go `Session(ctx)` helper from [Phase 1](phase-01-database-foundation.md) already sets `app.current_tenant`; Phase 3 turns that GUC into the actual gate.

## Design

### Roles

Two Postgres roles managed by an out-of-band ops script (documented in the runbook, not auto-created by Limen):

| Role          | Purpose                                                                                                              | Privileges                                                                                              |
| ------------- | -------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| `limen_admin` | Migrations, schema changes, RLS policy installation, the cross-tenant refresher (Phase 7) under `WithSuperuser(ctx)` | `BYPASSRLS`, owns the schema, full `SELECT/INSERT/UPDATE/DELETE` on all tables                          |
| `limen_app`   | Normal runtime queries. Pool used by `Session(ctx)` when ctx has a `TenantID` and no `WithSuperuser` marker.         | **No** `BYPASSRLS`. `SELECT/INSERT/UPDATE/DELETE` granted on tenant-scoped tables; subject to policies. |

Both roles connect through the same pool wiring; `Store` keeps two `*sql.DB` handles (`appDB`, `adminDB`) and `Session` picks the right one.

### Policy template

Applied to every table that has a `tenant_id` column:

```sql
ALTER TABLE <table> ENABLE ROW LEVEL SECURITY;
ALTER TABLE <table> FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON <table>
  USING (tenant_id = current_setting('app.current_tenant', true)::bigint)
  WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::bigint);
```

Notes:

- `FORCE` is critical — without it, the table owner (which is `limen_admin` after the migration) bypasses policies even from a connection without `BYPASSRLS`.
- `current_setting('app.current_tenant', true)` returns `NULL` when unset; `NULL::bigint = …` is `NULL`, which evaluates to `false`, so a missing GUC blocks all rows.
- The same policy enforces `USING` (read) and `WITH CHECK` (write), so a row cannot be inserted/updated into a different tenant even by mistake.

### Tables in scope

Every model from Phase 1 except `Tenant` itself:

`User`, `Upstream`, `UpstreamStrategyConfig`, `UpstreamRegistration`, `UpstreamLink`, `ZitadelApp`.

`Tenant` itself stays without RLS — its rows are looked up by `PublicID` at request entry to _establish_ the tenant context.

### Audit-column trigger (every Limen-owned table)

Independent of RLS, every table (including `Tenant`) gets a trigger that keeps `updated_at` truthful regardless of which client issues the `UPDATE` (GORM, `psql`, an ad-hoc admin script, etc.). This complements the soft-delete + audit-column convention defined in [Phase 1](phase-01-database-foundation.md).

```sql
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$$;
```

For every table in `{Tenant, User, Upstream, UpstreamStrategyConfig, UpstreamRegistration, UpstreamLink, ZitadelApp}`:

```sql
DROP TRIGGER IF EXISTS trg_<table>_set_updated_at ON <table>;
CREATE TRIGGER trg_<table>_set_updated_at
  BEFORE UPDATE ON <table>
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();
```

Lives in `migrations/postgres/0002_audit_triggers.sql`, runs after `AutoMigrate` under `adminDB`. Idempotent (`DROP TRIGGER IF EXISTS` + `CREATE`).

RLS and soft-deletes compose cleanly: the policy filters by `tenant_id`; GORM appends `deleted_at IS NULL`; the trigger only ever fires on `UPDATE` (including the soft-delete `UPDATE ... SET deleted_at = ...`, which is exactly what we want — `updated_at` is bumped on the same statement).

### Migration mechanics

- Add `migrations/postgres/0001_rls.sql` (or equivalent — embed via `embed.FS`).
- Run against `adminDB` after `Migrate(ctx)` finishes. Idempotent: each statement guarded by `IF NOT EXISTS` / catalog probes.

### `internal/storage/rls.go`

GORM session hooks:

- `Session(ctx)` opens a `BEGIN`, then `SET LOCAL app.current_tenant = $1`, then returns the `*gorm.DB`. The `commit` callback `COMMIT`s; on error path, `ROLLBACK`. The transaction is the unit that owns the GUC value (`SET LOCAL` is scoped to the tx).
- `WithSuperuser(ctx)` skips the `SET LOCAL` and routes the query through `adminDB`.
- A defensive check: if ctx has no tenant and no superuser marker, `Session(ctx)` returns an error rather than running an unscoped query.

### Connection-pool sizing

Two pools (`appDB`, `adminDB`) each with their own `max_open_conns`. Default sizes:

| Pool          | Max open | Max idle |
| ------------- | -------- | -------- |
| `limen_app`   | 25       | 5        |
| `limen_admin` | 5        | 2        |

`limen_admin` is small on purpose — it's used by migrations and a single background refresher.

## Deliverables

- New files:
  - `internal/storage/migrations/postgres/0001_rls.sql` (embedded via `embed.FS`)
  - `internal/storage/migrations/postgres/0002_audit_triggers.sql` (embedded)
  - `scripts/postgres-init/limen-roles.sql` (dev-only role provisioning, mounted into the postgres init.d directory by `compose.dev.yaml`)
  - `internal/storage/rls_test.go` (RLS verification suite)
- Modified files:
  - `internal/storage/storage.go` — opens two pools (`appDB`, `adminDB`); `RawDB()` now returns the admin handle.
  - `internal/storage/migrate.go` — embeds and runs the SQL migrations under the admin pool after `AutoMigrate`.
  - `internal/storage/tenant.go` — `Session(ctx)` routes by ctx marker; `WithSuperuser(ctx)` selects `adminDB`.
  - `internal/config/config.go` — `DatabaseConfig.AdminDSN` added (optional, falls back to `DSN`).
- Runbook section (added in Phase 10) describing how to provision the two roles in operator infrastructure.

## Security & operational notes

- **Single most common mistake**: forgetting `FORCE ROW LEVEL SECURITY` and discovering during a postmortem that policies didn't apply to the table owner. The migration test must explicitly catch this.
- **Service-role escape hatches are dangerous**: `WithSuperuser` is the only path to `adminDB`. Code reviews enforce that this marker is only set in `migrate.go` and the upstream refresher (`internal/upstream/refresher.go`). Add a `// nolint:limen.superuser` style comment on every legitimate call site so a grep audit can verify them.
- **`current_setting('app.current_tenant', true)`** — the `true` second arg means "return NULL if missing"; without it, an unset GUC raises an error. Use the safer NULL form.
- The `Tenant` table is intentionally non-RLS — lookup by `PublicID` runs against `adminDB` at request entry. The `PublicID` → tenant resolver (Phase 4) is the only Limen code path that touches `Tenant`.

## Verification

- Spin a `postgres:18-alpine` container in CI (or rely on `testcontainers-go`).
- Apply migrations as `limen_admin`, then run all tests under `limen_app`.
- Test cases:
  - Insert user into tenant A with `app.current_tenant=A` → succeeds.
  - Switch to `app.current_tenant=B` → `SELECT * FROM users` returns 0 rows.
  - Attempt `INSERT INTO users(tenant_id, …) VALUES (A, …)` with `app.current_tenant=B` → fails policy `WITH CHECK`.
  - `db.Unscoped().Find(&users)` from within a tenant-A session still returns only tenant-A rows — Go-level filtering is irrelevant; the DB is the enforcer.
  - Connect as `limen_app` directly (no GUC) → `SELECT * FROM users` returns 0 rows.
  - Connect as `limen_admin` directly → `SELECT * FROM users` returns all rows (sanity).
  - `WithSuperuser(ctx)` returns a handle that reads cross-tenant.
- Negative test: a model is added without the `tenant_id` column — the migration should still apply, but a separate lint/test enumerates tables and asserts the policy is present on tenant-scoped tables (drives a registry list).

## Risks

- **GORM's `Find` may strip the active tx** if a hook returns a fresh `*gorm.DB`. Use `db.WithContext(ctx).Begin()` and pass that around; do not let GORM open a new connection per call.
- **Connection leakage** if `commit` is forgotten — a connection stays in the pool with `app.current_tenant` still set. Mitigation: the next `Session` always issues `SET LOCAL` first thing, and `BEGIN` resets the GUC scope anyway, but `commit` being called is still mandatory. A panic-safe pattern (`defer commit(&err)`) is documented.
- **Migration ordering**: RLS migration must run _after_ `AutoMigrate`. The two are in different code paths; orchestrate them explicitly in `cmd/gateway/main.go` (Phase 10).

## Checklist

- [x] `migrations/postgres/0001_rls.sql` enables and forces RLS on every tenant-scoped table
- [x] Same migration installs `CREATE POLICY tenant_isolation` (`USING` + `WITH CHECK`) on each
- [x] Migration uses `current_setting('app.current_tenant', true)::bigint` (NULL-safe form)
- [x] Migration is idempotent (`IF NOT EXISTS` / catalog probes)
- [x] `migrations/postgres/0002_audit_triggers.sql` defines `set_updated_at()` and installs the trigger on every Limen-owned table (including `Tenant`)
- [x] Integration test asserts `updated_at` advances after a raw `psql` `UPDATE` (i.e. the trigger fires independently of GORM)
- [x] Integration test asserts a soft-delete `UPDATE ... SET deleted_at = ...` also bumps `updated_at`
- [x] `internal/storage/migrate.go` runs the embedded SQL migrations via the admin pool
- [x] `internal/storage/storage.go` opens both `appDB` and `adminDB` pools
- [x] `Session(ctx)` opens a tx and `SET LOCAL app.current_tenant` on Postgres path
- [x] `WithSuperuser(ctx)` routes to `adminDB` and skips the `SET LOCAL`
- [x] `Session(ctx)` rejects calls with no tenant and no superuser marker (defensive error)
- [x] Integration test against Postgres covers all the bullet points in **Verification** above
- [ ] Runbook draft (will be folded into Phase 10) documents role provisioning
- [ ] Audit: every `WithSuperuser` call site is justified and annotated

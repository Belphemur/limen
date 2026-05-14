# Database & Tenancy

Limen's persistence layer is PostgreSQL 18.2 + GORM, with **row-level security
(RLS) as the enforcement boundary** — `tenant_id` is checked by the database,
not by Go code. This doc explains the moving parts and how to add a new
tenant.

For the phase-by-phase design rationale, see
[`phases/phase-01-database-foundation.md`](phases/phase-01-database-foundation.md)
and [`phases/phase-03-postgres-rls.md`](phases/phase-03-postgres-rls.md).

## TL;DR

- One Postgres database, two roles: `limen_admin` (BYPASSRLS, owns the
  schema) and `limen_app` (the request-path runtime).
- Two GORM connection pools opened by `storage.Open(cfg)` — one per role.
- Every tenant-scoped table has `FORCE ROW LEVEL SECURITY` + a
  `tenant_isolation` policy keyed on the `app.current_tenant` GUC.
- Request-path code goes through `store.Session(ctx)`, which opens a tx on
  the app pool and pins the GUC with `set_config('app.current_tenant', …, true)`.
- Out-of-band code (migrations, the cross-tenant refresher) goes through
  `store.Session(storage.WithSuperuser(ctx))`, which routes to the admin pool
  and skips the GUC.

## Roles

Provisioned by `scripts/postgres-init/limen-roles.sql` in dev (mounted into
the postgres container's init.d) and by `deploy/postgres/limen-init.sql` in
production (shipped in Phase 11; documented in the runbook).

| Role          | Privileges                                                                                      | Used by                                                             |
| ------------- | ----------------------------------------------------------------------------------------------- | ------------------------------------------------------------------- |
| `limen_admin` | `BYPASSRLS`, owns `public`, full DML on all tables                                              | `storage.Open` → admin pool; migrations; `WithSuperuser` sessions   |
| `limen_app`   | No `BYPASSRLS`. `SELECT/INSERT/UPDATE/DELETE` on the schema, gated by `tenant_isolation` policy | `storage.Open` → app pool; every `Session(ctx)` on the request path |

Pool defaults: app `25 open / 5 idle`, admin `5 / 2`. Override via
`database.max_open_conns` / `max_idle_conns` in
[`config.yaml`](../config.yaml).

## Configuration

```yaml
database:
  dsn: "${LIMEN_DB_DSN}" # limen_app
  admin_dsn: "${LIMEN_DB_ADMIN_DSN:-}" # limen_admin; falls back to dsn for dev
```

When `admin_dsn` is empty, both pools share the same DSN — acceptable for a
single-role dev setup, **not acceptable in production** (RLS becomes a no-op
if the request-path role is a superuser).

## How RLS is wired

Schema changes ship in two layers, both run on the admin pool by
`Store.Migrate(ctx)` at startup ([`internal/storage/migrate.go`](../internal/storage/migrate.go)):

1. **GORM `AutoMigrate`** creates/syncs the tables for every model in
   `AllModels()`. This is the source of truth for table DDL — columns,
   foreign keys, and the indexes implied by struct tags.
2. **[goose](https://github.com/pressly/goose)** applies versioned SQL
   migrations from [`internal/storage/migrations/postgres/`](../internal/storage/migrations/postgres/)
   for everything GORM can't model. State lives in the `goose_db_version`
   table.

The two RLS-relevant migrations are:

1. [`00001_rls.sql`](../internal/storage/migrations/postgres/00001_rls.sql) —
   for each tenant-scoped table (`users`, `upstreams`,
   `upstream_strategy_configs`, `upstream_registrations`, `upstream_links`,
   `zitadel_apps`):

   ```sql
   ALTER TABLE <t> ENABLE ROW LEVEL SECURITY;
   ALTER TABLE <t> FORCE ROW LEVEL SECURITY;
   DROP POLICY IF EXISTS tenant_isolation ON <t>;
   CREATE POLICY tenant_isolation ON <t>
     USING      (tenant_id = current_setting('app.current_tenant', true)::bigint)
     WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::bigint);
   ```

   The same migration `GRANT`s DML on the schema to `limen_app` and **raises**
   if the role is missing — silently skipping would let the app boot and only
   blow up on the first request.

   Two critical details on the policy itself:
   - `FORCE` — without it the table owner (`limen_admin`, since it ran the
     DDL) would bypass the policy. With it, only roles with the `BYPASSRLS`
     attribute escape.
   - `current_setting(name, true)` — the `true` second arg returns `NULL` if
     the GUC is unset. `NULL = anything` is `NULL`, which evaluates to
     `false`, so a missing GUC blocks every row.

2. [`00002_audit_triggers.sql`](../internal/storage/migrations/postgres/00002_audit_triggers.sql) —
   installs a `BEFORE UPDATE` trigger on every Limen-owned table that bumps
   `updated_at` to `now()`, so the column is truthful regardless of which
   client issued the `UPDATE`. Composes cleanly with GORM soft-deletes (which
   are just `UPDATE … SET deleted_at = …`).

The `tenants` table itself is **not** under RLS — the tenant lookup
(by `PublicID`, a `tnt_<ULID>` string) runs against the admin pool at
request entry to _establish_ the tenant context.

### Where role provisioning lives (and why not in goose)

Goose migrations run as `limen_admin` against an existing database. Roles
live at the **cluster** level — above the database — so `CREATE ROLE` can't
live in a goose migration without dragging a superuser DSN into Limen's
runtime config. We keep that split clean:

| Layer              | Owns                                             | Runs as                   |
| ------------------ | ------------------------------------------------ | ------------------------- |
| Init script / IaC  | `CREATE ROLE`, `CREATE DATABASE`, ownership      | Postgres superuser, once  |
| Goose (`00001`, …) | Tables, RLS, policies, triggers, GRANTs to roles | `limen_admin`, every boot |

Dev: [`scripts/postgres-init/limen-roles.sql`](../scripts/postgres-init/limen-roles.sql)
is mounted into the postgres container's `docker-entrypoint-initdb.d/` and
runs on first boot. Prod: `deploy/postgres/limen-init.sql` (Phase 11) plus a
runbook entry.

### Changing the schema

The full playbook — when to lean on `AutoMigrate`, when to add a goose file,
file naming, transaction caveats, what to do for tenant-scoped tables — is
in [`internal/storage/MIGRATIONS.md`](../internal/storage/MIGRATIONS.md).
Read it before adding or modifying any migration.

## How requests get their tenant

The path is:

```
HTTP request → tenancy resolver (Phase 4) → ctx with tenant ID bound →
  store.Session(ctx) → tx with app.current_tenant pinned → handler queries
```

`Session(ctx)` is the only sanctioned read/write entry point. It opens a
transaction on the right pool and either:

- pins the GUC with `set_config('app.current_tenant', $1, true)` (app pool,
  tenant present), or
- skips the pin and uses the admin pool (`WithSuperuser` marker), or
- returns `storage.ErrNoTenant` (defensive — no unscoped queries leak).

The `true` third arg to `set_config` scopes the setting to the current
transaction, mirroring `SET LOCAL` but accepting bind parameters (raw
`SET LOCAL` does not).

### Calling Session from a handler

```go
func (h *Handler) ListUpstreams(w http.ResponseWriter, r *http.Request) {
    db, commit, err := h.store.Session(r.Context())
    if err != nil {
        // ErrNoTenant or transport error
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    defer func() { _ = commit() }()

    var ups []storage.Upstream
    if err := db.Find(&ups).Error; err != nil {
        // ...
    }
    // Write response, then commit() finalizes the tx.
}
```

Rules of thumb:

- `commit()` is **idempotent**. Calling it from a `defer` is the safe
  default; explicitly returning an error from the handler still triggers a
  rollback because `tx.Error` is non-nil at that point.
- `Unscoped()` only disables GORM's `deleted_at IS NULL` clause — it does
  **not** disable RLS. Cross-tenant reads still require `WithSuperuser`.
- Never call `store.RawDB()` from a request handler. It returns the admin
  (BYPASSRLS) handle and is reserved for migrations / admin tooling.

### Out-of-band code (refreshers, CLI, migrations)

```go
ctx := storage.WithSuperuser(r.Context())
db, commit, err := s.Session(ctx)
// ... db spans all tenants, no GUC pinned, runs on the admin pool
```

Every `WithSuperuser` call site must be justified in code review. Today the
legitimate users are `Migrate(ctx)` and (Phase 7) the upstream token
refresher.

## Adding a new tenant

Tenants are the **only** model that lives outside RLS, so creation happens
against the admin pool. The path is always:

1. Allocate the tenant in Limen's DB.
2. Provision the matching Zitadel organization (Phase 4+).
3. Create the owner user grant in Zitadel.

The first step in isolation looks like:

```go
ctx := storage.WithSuperuser(context.Background())
db, commit, err := store.Session(ctx)
if err != nil {
    return err
}
defer func() { _ = commit() }()

t := &storage.Tenant{
    Name:         "Acme, Inc.",
    ZitadelOrgID: "<id from zitadel>",    // set after Phase 5 lands
    DCREnabled:   false,                  // toggle in the portal later
}
if err := db.Create(t).Error; err != nil {
    return err
}
// t.ID is the internal int64 PK; t.PublicID is the tnt_… ULID
// the portal / APIs see and is the only externally visible
// identifier (used as the {tenant} URL segment everywhere).
```

The end-to-end CLI flow ships in Phase 4 as `./limen create-tenant
--name="Acme, Inc." --owner-email=...`, which wraps the snippet above plus
the Zitadel org + owner-grant calls and mirrors the new `PublicID` into
the Zitadel org metadata under the key `limen_tenant_id`.

### Tenant identifiers

- The tenant has no slug. Its `PublicID` (a `tnt_<ULID>` string from
  [`internal/ids`](../internal/ids/)) is the only externally visible
  identifier and is used as the `{tenant}` URL segment everywhere.
- The `PublicID` is mirrored into the Zitadel organization metadata
  under the key `limen_tenant_id` so the two systems can be
  cross-referenced from either side.
- `ZitadelOrgID` is uniquely indexed; one Limen tenant maps 1:1 to one
  Zitadel organization.

### Public IDs

Every model embeds [`Base`](../internal/storage/models.go) and gets a
`PublicID` of the form `<prefix>_<ULID>` (`tnt_01H…`, `usr_01H…`,
`ups_01H…`). The internal int64 `ID` never leaves the process — APIs, the
SPA, URLs, and operator logs use `PublicID` exclusively. ULIDs are
millisecond-sorted and monotonic within a process, which is what cursor
pagination keys on.

## Testing recipe

Integration tests live alongside the package
([`internal/storage/storage_test.go`](../internal/storage/storage_test.go),
[`rls_test.go`](../internal/storage/rls_test.go)) and follow this shape:

1. `startPostgres(t)` brings up a fresh `postgres:18.2-alpine` testcontainer
   (one per test — ~1–2 s cost buys total isolation).
2. `provisionRoles(t, bootstrapDSN)` connects as the container superuser and
   creates `limen_admin` (BYPASSRLS) and `limen_app`, granting `limen_app`
   to `limen_admin` so tests can `SET ROLE limen_app` for "what does the
   app role see?" assertions.
3. `openMigrated(t)` opens the `Store` with both DSNs and runs `Migrate`.
4. Tests use `store.Session(WithTenant(ctx, id))` for app-role assertions
   and `store.RawDB()` (admin pool) for cross-tenant seeding.

The negative case is the important one: `TestRLS_AppRoleWithoutGUCSeesNothing`
in [`rls_test.go`](../internal/storage/rls_test.go) issues `SET ROLE limen_app`
on a connection without setting the GUC and asserts `SELECT count(*) FROM
users` returns `0`. This is what catches a missing `FORCE ROW LEVEL SECURITY`
during PR review — symptom would be the count returning the full table.

## Troubleshooting

- **`limen_app role missing — provision it before running migrations`** on
  startup → the init script didn't run. In dev, `docker compose down -v`
  the postgres volume and bring it back up so
  [`scripts/postgres-init/limen-roles.sql`](../scripts/postgres-init/limen-roles.sql)
  fires. In prod, run `deploy/postgres/limen-init.sql` manually.
- **"new row violates row-level security policy"** on `INSERT` from a
  request handler → the row's `tenant_id` does not match
  `app.current_tenant`. Check the resolver bound the right tenant to ctx,
  and that the model has `TenantID` populated _before_ `Create`.
- **`SELECT` returns 0 rows when you expect data** → the GUC is unset.
  Almost always because the call path goes around `Session(ctx)`. Search
  for direct uses of `store.RawDB()` on the request path.
- **`updated_at` lagging behind `now()`** after a hand-written `UPDATE` in
  `psql` → the trigger isn't installed. Re-run `Migrate`, then check
  `\d <table>` shows `trg_<table>_set_updated_at`.
- **Tests blow up with `permission denied to set role "limen_app"`** →
  `provisionRoles` did not grant `limen_app` to the admin role. Restart
  the testcontainer; the role-grant SQL only runs on first boot.

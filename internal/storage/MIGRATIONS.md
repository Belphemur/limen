# MIGRATIONS.md — playbook for schema changes

This is the **decision tree + recipe** for every schema change in
`internal/storage/`. Follow it when you add a model, change a column, add a
trigger, or backfill data. It exists because we deliberately split work
between two systems:

- **GORM `AutoMigrate`** handles the GORM-modelable parts of the schema
  (tables, columns, indexes implied by struct tags, foreign keys).
- **[goose](https://github.com/pressly/goose)** (annotated SQL files in
  [`migrations/postgres/`](migrations/postgres/)) handles everything GORM
  can't model: RLS policies, triggers, functions, partial / expression
  indexes, data backfills, custom constraints.

Both run on the **admin pool** (`limen_admin`, `BYPASSRLS`) inside
`Store.Migrate(ctx)`. AutoMigrate first, then goose. Goose tracks state in
the `goose_db_version` table — never edit that table by hand.

## Decision tree — does my change need a goose migration?

```
Change to a GORM model?
│
├── New column / dropped-but-keep-data column / changed type / new index from
│   a struct tag?
│       → AutoMigrate handles it. No goose migration needed for the column
│         itself. Test it in storage_test.go and you're done.
│
├── New tenant-scoped model (embeds TenantID)?
│       → AutoMigrate creates the table. You ALSO need a goose migration that:
│           * adds the table name to the RLS tables[] array (extend or supersede
│             00001_rls.sql), and
│           * adds the table to the audit-trigger tables[] array (extend or
│             supersede 00002_audit_triggers.sql).
│         Idiomatic shape: add 00003_<model>_rls.sql that runs ENABLE/FORCE
│         RLS + tenant_isolation policy + trigger for the new table only.
│         Don't edit 00001/00002 once they've shipped.
│
├── Partial / expression index, CHECK constraint, EXCLUDE constraint,
│   GENERATED column, view, function, trigger?
│       → Goose migration.
│
├── Data backfill or one-time correction (rename a value, split a column,
│   populate a new column from an old one)?
│       → Goose migration. Keep the DML idempotent (WHERE … IS NULL guard) so
│         a re-run is safe.
│
├── Drop / rename a column?
│       → Two-step. AutoMigrate WON'T drop columns (by design, to protect
│         data). Drop or rename in a goose migration. For renames in
│         production, do the classic add-new + dual-write + backfill + cut-
│         over + drop-old dance across multiple migrations.
│
└── Anything touching the `tenants` table, RLS policies, the
    `set_updated_at` function, or the goose machinery itself?
        → Goose migration. Treat with extra care.
```

When in doubt: **prefer a goose migration**. AutoMigrate is convenient but
it's not a record of intent. Goose files are the audit log.

## File naming

`internal/storage/migrations/postgres/NNNNN_<short_snake_case>.sql`

- `NNNNN` is a **5-digit zero-padded sequence** — `00001`, `00002`, `00003`.
  Goose tolerates any monotonically increasing integer; we use 5 digits for
  consistent sort + room to grow.
- The slug is short and describes the change: `00003_add_audit_log`,
  `00004_backfill_user_locale`, `00005_zitadel_app_unique`.
- One concern per migration. Don't fold a backfill into a structural change
  — they fail differently and review differently.

## File template

```sql
-- +goose Up
-- +goose StatementBegin
-- One-paragraph description of what this migration does and why. Include
-- references to the issue / PR / phase doc where appropriate. Mention any
-- non-obvious invariants (e.g. "depends on column X created in 00007").
<your DDL or DML here>
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
<reverse of the Up; if irreversible, see below>
-- +goose StatementEnd
```

### Why `StatementBegin` / `StatementEnd`

Goose splits files on `;` by default. Anything with embedded `;`s
(`DO $$ … $$;`, function bodies, `CREATE TRIGGER`, multi-statement blocks)
must be wrapped in `StatementBegin` / `StatementEnd` so goose sees it as a
single statement. Look at
[`00001_rls.sql`](migrations/postgres/00001_rls.sql) for the canonical
pattern.

### Multiple statements in one file

Each independent statement gets its own `StatementBegin` / `StatementEnd`
block in goose's annotated SQL format. Don't try to share one block across
unrelated DDL — failures become hard to localize.

## Conventions

### Idempotency

Every migration should survive a re-run on a partially-applied DB without
exploding. Goose's version table normally prevents re-execution, but a
botched migration that goose marked applied can leave you re-running it by
hand. Use:

- `CREATE … IF NOT EXISTS`
- `DROP … IF EXISTS` before `CREATE`
- `INSERT … ON CONFLICT DO NOTHING` / `DO UPDATE`
- `WHERE col IS NULL` guards on backfills

The shipped 00001 and 00002 migrations are written this way — copy that
style.

### Down migrations

Always write one. If the change is genuinely irreversible (you just dropped
a column with data), the Down should be a comment explaining why and a
`SELECT 'irreversible: ...'` placeholder so the file parses. Down migrations
are how `make migrate-down` works in CI scratch envs; production never
auto-downs.

### Transactions

Goose wraps each migration in a single transaction by default — what you
want most of the time. Two exceptions need `-- +goose NO TRANSACTION` at the
top of the file:

- `CREATE INDEX CONCURRENTLY`
- `VACUUM`, `REINDEX`, anything Postgres forbids inside a transaction

Without that directive, those statements error with "cannot run inside a
transaction block".

### Naming objects

- Indexes: `idx_<table>_<columns>` for non-unique, `uniq_<table>_<columns>`
  for unique, `partial_<table>_<purpose>` for partial.
- Triggers: `trg_<table>_<purpose>` (see `trg_users_set_updated_at`).
- Functions: snake_case verb_object (`set_updated_at`, `expire_old_sessions`).
- Policies: `<purpose>_isolation` (`tenant_isolation`).

### Tenant scope for new tables

Any new tenant-scoped table needs:

1. `tenant_id` column on the GORM struct (handled by `AutoMigrate`).
2. `ENABLE ROW LEVEL SECURITY` + `FORCE ROW LEVEL SECURITY` (goose).
3. `tenant_isolation` policy with `USING` **and** `WITH CHECK` keyed on
   `current_setting('app.current_tenant', true)::bigint` (goose).
4. `BEFORE UPDATE trg_<table>_set_updated_at` trigger (goose).
5. `GRANT SELECT, INSERT, UPDATE, DELETE` to `limen_app` if the table will
   be touched on the request path (handled by 00001's default-privileges
   block in most cases — verify after the migration runs).

Don't skip 3 or 4. RLS is the security boundary; the trigger keeps
`updated_at` honest.

## Workflow — adding a migration

1. Make the GORM model change (if any) in `models.go`. Add the new model to
   `AllModels()`.
2. Decide using the decision tree above whether goose is needed. If yes,
   create the next-numbered file under
   `internal/storage/migrations/postgres/`.
3. Write `-- +goose Up` and `-- +goose Down` blocks, wrapping anything with
   internal `;`s in `StatementBegin` / `StatementEnd`.
4. Run the tests: `go test ./internal/storage/...`. The testcontainer boots
   a fresh Postgres each time and calls `Store.Migrate` — your migration
   runs in the same path production will use.
5. If you added a new tenant-scoped table, add at least one integration
   test in `rls_test.go` proving cross-tenant `SELECT` returns 0 rows on
   that table (mirror `TestRLS_CrossTenantSelectReturnsZeroRows`).
6. Run `golangci-lint run ./internal/storage/...` and commit.

## Things to never do

- **Never edit a migration after it has shipped to `main`.** Goose tracks
  the file's _version number_, not its content. Editing a shipped file
  silently diverges environments. If you need to fix it, write the fix as
  the next migration.
- **Never use `AutoMigrate` for RLS / triggers / policies.** GORM doesn't
  know about them and will not be able to maintain them. They belong in
  goose.
- **Never call `Store.Migrate` from a request handler or test helper other
  than `openMigrated(t)`.** It's a startup-time operation against the
  admin pool, not a runtime feature.
- **Never bypass goose by running raw SQL through `RawDB()` to "fix"
  schema.** That's a hidden migration. Write it as a file, even if it's a
  one-liner, so the next environment doesn't drift.
- **Never put secrets (DSNs, keys, tokens) in a migration file.** They
  belong in config + `crypto.SecretField`. Migrations are committed and
  embedded in the binary.

## Reference

- Goose annotated SQL: <https://github.com/pressly/goose#sql-migrations>
- Postgres RLS: <https://www.postgresql.org/docs/current/ddl-rowsecurity.html>
- Phase 3 design: [`../../docs/phases/phase-03-postgres-rls.md`](../../docs/phases/phase-03-postgres-rls.md)
- High-level DB doc: [`../../docs/database.md`](../../docs/database.md)

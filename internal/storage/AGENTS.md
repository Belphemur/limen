# AGENTS.md — `internal/storage`

## What this package is

The GORM-backed persistence layer. Owns the schema, the migration entry
point, and — critically — the `Session(ctx)` contract that every request-path
read/write goes through. Postgres 18.2 is the only supported driver.

## File layout

| File              | Purpose                                                                                              |
| ----------------- | ---------------------------------------------------------------------------------------------------- |
| `storage.go`      | `Store` lifecycle: `Open(cfg)`, `Close`, `Ping`, dual-pool wiring.                                   |
| `models.go`       | Every persistent model. All models embed `Base`. Tenant-scoped models additionally embed `TenantID`. |
| `migrate.go`      | `Store.Migrate(ctx)` — runs `AutoMigrate` then goose-managed SQL migrations under the admin pool.    |
| `tenant.go`       | `WithTenant`, `TenantFromCtx`, `WithSuperuser`, `Session`, `RawDB`.                                  |
| `migrations/postgres/*.sql` | Embedded goose migrations (annotated `-- +goose Up` / `Down`): `00001_rls.sql` and `00002_audit_triggers.sql`. Add new ones following [`MIGRATIONS.md`](MIGRATIONS.md). |
| `storage_test.go` / `rls_test.go` | Phase 1 + Phase 3 integration suites — testcontainers `postgres:18.2-alpine`. The `startPostgres(t)` / `provisionRoles(t)` / `openMigrated(t)` helpers are the canonical shape new tests should follow. |

Phase 3 is fully landed: the admin pool, RLS policies, the `set_updated_at`
trigger, and the dual-DSN config (`DSN` / `AdminDSN`) are all in place.

## The `Session(ctx)` contract

```go
db, commit, err := store.Session(ctx)
if err != nil { return err }
defer commit() // idempotent
// db is a *gorm.DB inside a transaction with app.current_tenant set
```

- Opens a transaction on the **app pool** (`limen_app`).
- Runs `set_config('app.current_tenant', <tenant_id>, true)` so the
  `tenant_isolation` RLS policies see the tenant.
- Returns `ErrNoTenant` when ctx has no tenant and is not flagged superuser.
- `commit()` commits on success, rolls back if `tx.Error` is set, and is
  safe to call multiple times.
- `WithSuperuser(ctx)` reroutes to the **admin pool** (`limen_admin`,
  `BYPASSRLS`) and skips the tenant pin.

## Escape hatches (both intentional and conspicuous)

| Hatch           | When                                                                                                |
| --------------- | --------------------------------------------------------------------------------------------------- |
| `RawDB()`       | Migrations and admin tooling only. Returns the **admin** pool. Never on the request path.           |
| `WithSuperuser` | Cross-tenant refreshers and admin migrations. Routes to the admin pool and skips the tenant pin.    |
| `Unscoped()`    | Hard deletes / restoring soft-deleted rows. Always pair with audit logging. Inside a tenant `Session`, RLS still filters — `Unscoped` only disables GORM's soft-delete clause. |

## Models — invariants

- Every model embeds `Base` (`ID`, `PublicID`, `CreatedAt`, `UpdatedAt`,
  `DeletedAt`). No exceptions, including `Tenant`.
- Tenant-scoped models embed `TenantID int64` and declare any composite
  uniques as **partial** indexes filtered by `WHERE deleted_at IS NULL` so
  soft-deletes do not block re-creation under the same logical key.
- `BeforeCreate` assigns `PublicID = ids.New(<prefix>)` if missing.
- Credentials and tokens use `crypto.SecretField`, never `[]byte` or `string`.
- `Base.ID` carries `json:"-"`. Public IDs are the only IDs that cross the
  process boundary.

## Adding a new model

1. Add a `Prefix*` constant to [`internal/ids/prefixes.go`](../ids/prefixes.go).
2. Define the model in `models.go`, embedding `Base`. Add `TenantID` and
   `Tenant *Tenant` if tenant-scoped.
3. Implement `BeforeCreate` calling `ids.New(<prefix>)`.
4. Append the model to `AllModels()` so `AutoMigrate` picks it up.
5. If the model is **tenant-scoped** or needs a trigger / partial index /
   data backfill, add a goose migration — see [`MIGRATIONS.md`](MIGRATIONS.md).
6. Add an integration test asserting round-trip CRUD, prefix correctness, and
   soft-delete behavior (see the Phase 1 checklist).

## When to extend

- **Phase 7** introduces the cross-tenant token refresher — the canonical
  legitimate user of `WithSuperuser`.

## What this package is NOT

- Not a repository / DAO layer. Higher-level packages own their query logic;
  this package gives them a tenant-scoped `*gorm.DB`.
- Not a connection pool _per tenant_. We use one pool; tenancy is enforced
  per-statement via `SET LOCAL app.current_tenant` + RLS.
- Not the place for JSON shapes. API DTOs live with the handlers.

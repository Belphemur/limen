# AGENTS.md — `internal/storage`

## What this package is

The GORM-backed persistence layer. Owns the schema, the migration entry
point, and — critically — the `Session(ctx)` contract that every request-path
read/write goes through. Postgres 18.2 is the only supported driver.

## File layout

| File              | Purpose                                                                                              |
| ----------------- | ---------------------------------------------------------------------------------------------------- |
| `storage.go`      | `Store` lifecycle: `Open(cfg)`, `Close`, `Ping`, dual-pool wiring.                                   |
| `models.go`       | `Base` (embedded by every model) + `AllModels()` (the AutoMigrate manifest). No model definitions live here. |
| `model_*.go`      | One file per persistent model (`model_tenant.go`, `model_user.go`, `model_upstream.go`, `model_upstream_tool.go`, `model_zitadel_app.go`). Tenant-scoped models embed `TenantID`. |
| `migrate.go`      | `Store.Migrate(ctx)` — runs `AutoMigrate` then goose-managed SQL migrations under the admin pool.    |
| `tenant.go`       | `WithTenant`, `TenantFromCtx`, `WithSuperuser`, `Session`, `RawDB`.                                  |
| `migrations/postgres/*.sql` | Embedded goose migrations (annotated `-- +goose Up` / `Down`): `00001_rls.sql` and `00002_audit_triggers.sql`. Add new ones following [`MIGRATIONS.md`](MIGRATIONS.md). |
| `storage_test.go` / `rls_test.go` | Phase 1 + Phase 3 integration suites — testcontainers `postgres:18-alpine`. The `startPostgres(t)` / `provisionRoles(t)` / `openMigrated(t)` helpers are the canonical shape new tests should follow. |

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

## Do not filter by `tenant_id` in `WHERE`

Once `Session(ctx)` has pinned `app.current_tenant`, the RLS policy on
every tenant-scoped table rewrites your query for you. **Writing
`tenant_id = ?` explicitly is redundant and discouraged**:

- It adds no safety — the GUC is the real fence; without it the policy
  matches zero rows (fail-closed).
- It hides the few queries that legitimately bypass RLS via
  `WithSuperuser`, where the explicit filter is **mandatory** (the
  admin pool has `BYPASSRLS`).
- It encourages cargo-culting the pattern into superuser code paths
  where you might forget the filter and silently leak across tenants.

| Context                            | `WHERE tenant_id = ?` …          |
| ---------------------------------- | -------------------------------- |
| `Session(WithTenant(ctx, id))`     | omit — RLS handles it            |
| `Session(WithSuperuser(ctx))`      | **required** on every statement  |
| `Session(ctx)` (no marker)         | n/a — returns `ErrNoTenant`      |

Inserts and upserts on RLS-scoped tables still need `tenant_id`
populated on the row — the policy's `WITH CHECK` rejects mismatches and
`NULL`.

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

## Cascade soft-delete

GORM's `OnDelete:CASCADE` constraint only fires for hard deletes (SQL DELETE).
For soft-deletes (which are UPDATEs that set `deleted_at`), use a `BeforeDelete`
hook on the parent model:

```go
func (u *Upstream) BeforeDelete(tx *gorm.DB) error {
    // Cascade soft-delete all related records in the same transaction.
    return tx.Where("upstream_id = ?", u.ID).Delete(&UpstreamLink{}).Error
}
```

The hook runs inside the same transaction as the parent delete — both succeed
or both roll back. Do NOT implement cascade in the service layer; the model is
the single source of truth for what gets cleaned up when a row is deleted.

## Adding a new model

1. Add a `Prefix*` constant to [`internal/ids/prefixes.go`](../ids/prefixes.go).
2. Define the model in a new `model_<name>.go` file, embedding `Base`. Add `TenantID` and
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

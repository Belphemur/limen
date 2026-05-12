# AGENTS.md — `internal/storage`

## What this package is

The GORM-backed persistence layer. Owns the schema, the migration entry
point, and — critically — the `Session(ctx)` contract that every request-path
read/write goes through. Postgres 18.2 is the only supported driver.

## File layout

| File         | Purpose                                                                                              |
| ------------ | ---------------------------------------------------------------------------------------------------- |
| `storage.go` | `Store` lifecycle: `Open(cfg)`, `Close`, `Ping`, pool tuning.                                        |
| `models.go`  | Every persistent model. All models embed `Base`. Tenant-scoped models additionally embed `TenantID`. |
| `migrate.go` | `Store.Migrate(ctx)` — the only consumer of `RawDB()`.                                               |
| `tenant.go`  | `WithTenant`, `TenantFromCtx`, `WithSuperuser`, `Session`, `RawDB`.                                  |

`rls.go` is added in Phase 3 (Postgres row-level security policies + the
`limen_admin` pool routing for superuser sessions).

## The `Session(ctx)` contract

```go
db, commit, err := store.Session(ctx)
if err != nil { return err }
defer commit() // idempotent
// db is a *gorm.DB inside a transaction with app.current_tenant set
```

- Opens a transaction.
- Runs `SET LOCAL app.current_tenant = <tenant_id>` so RLS policies (Phase 3)
  see the tenant.
- Returns `ErrNoTenant` when ctx has no tenant and is not flagged superuser.
- `commit()` commits on success, rolls back if `tx.Error` is set, and is
  safe to call multiple times.

## Escape hatches (both intentional and conspicuous)

| Hatch           | When                                                                                                               |
| --------------- | ------------------------------------------------------------------------------------------------------------------ |
| `RawDB()`       | Migrations and admin tooling only. Never on the request path.                                                      |
| `WithSuperuser` | Cross-tenant refreshers and admin migrations. Skips the tenant pin and (Phase 3) routes to the `limen_admin` pool. |
| `Unscoped()`    | Hard deletes / restoring soft-deleted rows. Always pair with audit logging.                                        |

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
4. Append the model to `AllModels()` so `Migrate` picks it up.
5. Add an integration test asserting round-trip CRUD, prefix correctness, and
   soft-delete behavior (see the Phase 1 checklist).

## When to extend

- **Phase 3** adds `rls.go` + `migrations/postgres/*.sql` (RLS policies, the
  `set_updated_at` trigger, `limen_app` / `limen_admin` roles). `Session`
  becomes the actual enforcement boundary; no call site changes.
- **Phase 7** introduces the cross-tenant token refresher — the canonical
  legitimate user of `WithSuperuser`.

## What this package is NOT

- Not a repository / DAO layer. Higher-level packages own their query logic;
  this package gives them a tenant-scoped `*gorm.DB`.
- Not a connection pool _per tenant_. We use one pool; tenancy is enforced
  per-statement via `SET LOCAL app.current_tenant` + RLS.
- Not the place for JSON shapes. API DTOs live with the handlers.

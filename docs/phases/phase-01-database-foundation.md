# Phase 1 — Database foundation

**Depends on**: nothing (can start immediately, in parallel with Phase 2)
**Unblocks**: Phases 3, 4, 5, 6, 7, 8, 9, 10

## Goal

Introduce a persistent storage layer powered by GORM on **PostgreSQL 18.2** (the only supported driver — see [Phase 0](phase-00-dev-environment.md) for the dev stack and [Phase 11](phase-11-production-deployment.md) for prod). Define every data model required by later phases (tenants, users, MCP client mirrors, upstreams) so the rest of the work can land without further schema churn. Invitation flow and portal session state are delegated to [Zitadel](https://zitadel.com/) (see [Phase 4](phase-04-tenant-auth-session.md)) and therefore have no Limen-side tables.

This phase deliberately stops short of installing RLS policies — that's Phase 3 — but it lays down the contract `Session(ctx)` that Phase 3 will plug into.

## Design

### Driver

```yaml
database:
  dsn: "<postgres connection string>"
```

`storage.Open(cfg)` opens a single GORM `*gorm.DB` backed by `gorm.io/driver/postgres`. Connection pool sizing via `max_open_conns` / `max_idle_conns` from config (sensible defaults: 25 / 5). The DSN points at the **runtime role** (`limen_app`), not the DB owner — [Phase 3](phase-03-postgres-rls.md) creates these roles.

### IDs

Every persistent entity has two identifiers:

- **`ID` (`int64`, `bigint`)** — internal auto-increment primary key. Used for foreign keys, GORM relations, and indexes. Never exposed in API responses, URLs, logs, or the SPA. Small, fast on B-tree joins, and irreversible from outside.
- **`PublicID` (`string`)** — [ULID](https://github.com/oklog/ulid) with a Stripe-style type prefix. 26-char Crockford base32 body that is **lexicographically sortable by creation time at millisecond resolution**, prefixed with the entity type. `ulid.Make()` is monotonic within the same millisecond, so IDs minted back-to-back in a single process still sort in creation order. This is the only ID that appears in Connect-RPC requests/responses, OAuth redirect URLs, the SPA, and operator-facing logs.

Type prefixes (final list lives in `internal/ids/prefixes.go`; never reuse or rename a prefix once shipped):

| Entity                   | Prefix  |
| ------------------------ | ------- |
| `Tenant`                 | `tnt_`  |
| `User`                   | `usr_`  |
| `Upstream`               | `ups_`  |
| `UpstreamStrategyConfig` | `usc_`  |
| `UpstreamRegistration`   | `ureg_` |
| `UpstreamLink`           | `ulnk_` |
| `ZitadelApp`             | `zapp_` |

`internal/ids/ids.go` exposes:

```go
type Prefix string

const (
    PrefixTenant                 Prefix = "tnt"
    PrefixUser                   Prefix = "usr"
    PrefixUpstream               Prefix = "ups"
    PrefixUpstreamStrategyConfig Prefix = "usc"
    PrefixUpstreamRegistration   Prefix = "ureg"
    PrefixUpstreamLink           Prefix = "ulnk"
    PrefixZitadelApp             Prefix = "zapp"
)

func New(p Prefix) string                       // "tnt_<26-char-ULID>"
func Parse(s string) (Prefix, ulid.ULID, error)
func MustParse(p Prefix, s string) (ulid.ULID, error)  // verifies the prefix matches
```

ULID generation runs in Go (`ulid.Make()`), so no server-side extension is needed. `ulid.Make` uses millisecond timestamps + monotonic entropy, so IDs minted in the same millisecond still sort in creation order. The internal `ID` comes from the database (`BIGSERIAL`).

### Indexes

- `id bigint PRIMARY KEY` on every table (clustered index).
- **Unique** index on `public_id` for every table — API handlers resolve rows by ULID, never by internal ID.
- ULIDs are time-sorted at millisecond resolution, so range scans on `public_id` align with creation order. Cursor pagination is `WHERE public_id > $cursor ORDER BY public_id LIMIT n` — no separate `created_at` index needed.
- Composite indexes (kept from before): `(tenant_id, email)` unique on `User`, `(tenant_id, zitadel_subject)` unique on `User`, `(tenant_id, name)` unique on `Upstream`, `(tenant_id, user_id, upstream_id)` unique on `UpstreamLink`, `(tenant_id, zitadel_app_id)` unique on `ZitadelApp`.
- RLS (Phase 3) keys off `tenant_id` (bigint), so a plain B-tree on `tenant_id` plus the composite uniques are sufficient.

### Audit columns (every table)

Every model embeds the same three audit timestamps — no exceptions, including `Tenant`. They are all `TIMESTAMPTZ NOT NULL DEFAULT now()` (except `DeletedAt`, which is nullable).

| Column      | Type                     | Managed by                                                                                | Notes                                                                                      |
| ----------- | ------------------------ | ----------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| `CreatedAt` | `TIMESTAMPTZ`            | DB default `now()`; reaffirmed by GORM on `Create`                                        | Set once and never modified.                                                               |
| `UpdatedAt` | `TIMESTAMPTZ`            | Postgres trigger `set_updated_at` on every `UPDATE` ([Phase 3](phase-03-postgres-rls.md)) | The trigger is authoritative — never trust application code to touch this column.          |
| `DeletedAt` | `TIMESTAMPTZ` (nullable) | Set by GORM via the `gorm.DeletedAt` soft-delete sentinel                                 | A `NULL` value means "live". Hard-deletes only via `Unscoped().Delete()` in admin tooling. |

All models embed a shared base struct so the columns and tags stay in lockstep:

```go
type Base struct {
    ID        int64          `gorm:"primaryKey;autoIncrement"`
    PublicID  string         `gorm:"type:text;uniqueIndex;not null"`
    CreatedAt time.Time      `gorm:"type:timestamptz;not null;default:now()"`
    UpdatedAt time.Time      `gorm:"type:timestamptz;not null;default:now()"`
    DeletedAt gorm.DeletedAt `gorm:"type:timestamptz;index"`
}
```

**GORM soft-delete semantics**: because `DeletedAt` is `gorm.DeletedAt`, GORM automatically appends `WHERE deleted_at IS NULL` to every `Find` / `First` / `Update` / `Delete`, so soft-deleted rows are invisible by default. `Unscoped()` is the only escape hatch and is reserved for admin / migration code (paired with the `WithSuperuser` ctx marker in Phase 3 when the table is also tenant-scoped).

**Trigger** (defined in [Phase 3](phase-03-postgres-rls.md)) keeps `UpdatedAt` truthful:

```sql
CREATE FUNCTION set_updated_at() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$$;

CREATE TRIGGER trg_<table>_set_updated_at
  BEFORE UPDATE ON <table>
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();
```

The trigger applies to every Limen-owned table; the migration in [Phase 3](phase-03-postgres-rls.md) iterates over the table list and installs it idempotently.

**Soft-delete + unique constraints**: the composite uniques listed below must be **partial** indexes filtered by `deleted_at IS NULL`, otherwise a soft-deleted row would prevent re-creating an entity with the same logical key (e.g. recreating a `User` with the same email after they leave and rejoin). This is encoded in the GORM tag with `where:deleted_at IS NULL` and asserted by an integration test.

### Models (`internal/storage/models.go`)

Every model embeds `Base` (above), which means it carries `ID`, `PublicID`, `CreatedAt`, `UpdatedAt`, `DeletedAt`. Tenant-scoped models additionally carry `TenantID int64`. The columns below list only the **domain-specific** fields:

| Model                    | Domain fields                                                                                                                                             | Notes                                                                                                                                                                                                                                                                                                                    |
| ------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `Tenant`                 | `Name`, `ZitadelOrgID` (unique), `DCREnabled bool`                                                                                       | Root multi-tenancy entity. `PublicID` (prefix `tnt_`) is the only externally visible identifier and is used as the `{tenant}` URL segment everywhere — there is no slug. `ZitadelOrgID` binds the tenant to a Zitadel organization (Phase 4) and the tenant's `PublicID` is mirrored into that org's metadata under the key `limen_tenant_id`.                                                                                                                                                                         |
| `User`                   | `Email`, `Name`, `ZitadelSubject`                                                                                                                         | `(tenant_id, email)` unique; `(tenant_id, zitadel_subject)` unique. No password — Zitadel owns credentials. **No role column** — authorization roles are Zitadel project roles read from the token claim `urn:zitadel:iam:org:project:roles` (see [Phase 4](phase-04-tenant-auth-session.md)). `PublicID` prefix `usr_`. |
| `Upstream`               | `Name`, `StrategyType` (`mcp_spec`/`none` in v1), `McpServerURL`                                                                                          | `(tenant_id, name)` unique. `StrategyType` is the extension point for future strategies. `PublicID` prefix `ups_`.                                                                                                                                                                                                       |
| `UpstreamStrategyConfig` | `UpstreamID` unique, `Type`, `ConfigJSON` (encrypted)                                                                                                     | Opaque-to-storage JSON for strategy-specific parameters. `mcp_spec` and `none` leave it empty in v1. `PublicID` prefix `usc_`.                                                                                                                                                                                           |
| `UpstreamRegistration`   | `UpstreamID`, `Issuer`, `ClientID`, `ClientSecret` (enc), `RegistrationAccessToken` (enc), `RegistrationClientURI`, `ResourceURI`                         | DCR result per `(tenant, upstream)` against _external_ MCP servers. `none` strategies leave this empty. `PublicID` prefix `ureg_`.                                                                                                                                                                                       |
| `UpstreamLink`           | `UserID`, `UpstreamID`, `AccessToken` (enc, nullable), `RefreshToken` (enc, nullable), `ExpiresAt` (nullable), `Scopes`, `ResourceURI`, `ExtraJSON` (enc) | `(tenant_id, user_id, upstream_id)` unique. Only created when `RequiresLink()==true`. `PublicID` prefix `ulnk_`.                                                                                                                                                                                                         |
| `ZitadelApp`             | `ZitadelAppID`, `ClientID`, `ClientSecret` (enc, nullable), `Name`, `RedirectURIs`, `SoftwareID`, `SoftwareVersion`, `RegistrationAccessToken` (enc)      | Mirror of MCP clients that DCR'd through Limen's proxy into Zitadel (Phase 5). Lets the portal list MCP clients and authenticate RFC 7592 management requests without round-tripping Zitadel. `PublicID` prefix `zapp_`.                                                                                                 |

### Encryption fields

Fields marked `(encrypted)` are stored as `[]byte` and wrapped through `internal/crypto` (Phase 2). The natural pattern is a custom GORM type `crypto.SecretField` that implements `Scan`/`Value` so models stay clean:

```go
type UpstreamLink struct {
    // ...
    AccessToken  crypto.SecretField `gorm:"type:bytea"`
    RefreshToken crypto.SecretField `gorm:"type:bytea"`
    // ...
}
```

Phase 1 can stub `SecretField` as a `[]byte` alias and let Phase 2 fill in encryption — the migrations and field types don't change.

### Hard-delete escape hatch

For the rare admin operation that needs a true `DELETE` (GDPR erasure of a tenant, scrubbing test fixtures), `Unscoped().Delete(&model)` does the job. Such call sites must:

- live in admin/migration code (never on the request path);
- run under `WithSuperuser(ctx)` if the table is tenant-scoped;
- be accompanied by an audit-log emission (see [Phase 10](phase-10-wiring-hardening.md)).

### `Session(ctx)` contract

The single sanctioned way to read or write tenant-scoped data:

```go
db, commit, err := storage.Session(ctx)
// db is a *gorm.DB pre-scoped to the tenant in ctx
// commit releases the connection/tx (idempotent)
```

`Session(ctx)` opens a transaction; the first statement is `SET LOCAL app.current_tenant = $1` ([Phase 3](phase-03-postgres-rls.md) turns this GUC into the actual RLS enforcement key). Even before RLS policies are installed, `Session` already sets the GUC so policy rollout doesn't touch any call site.

Two escape hatches, both intentional and conspicuous:

- `RawDB() *gorm.DB` — unfiltered DB handle. Used only by `migrate.go`.
- `WithSuperuser(ctx) context.Context` — marker that `Session(ctx)` honors by switching to the `limen_admin` connection pool (Phase 3) and skipping the tenant `SET LOCAL`. Used only by the cross-tenant refresher (Phase 7) and admin migrations.

### Migrations (`internal/storage/migrate.go`)

- `AutoMigrate(allModels...)` for portable schema (table creation, columns, indexes, FK constraints).
- Index hints declared via GORM struct tags: unique indexes on `(tenant_id, …)` composites as listed above.
- A `Migrate(ctx)` function is the only consumer of `RawDB()`.
- RLS policies (Phase 3) live in `migrations/postgres/*.sql` and are run separately under a `limen_admin` connection — **not** through `AutoMigrate`.

### Package layout

```
internal/storage/
├── storage.go     // Open(cfg) → *Store
├── models.go      // all GORM models
├── migrate.go     // Migrate(ctx) - AutoMigrate, indexes
└── tenant.go      // WithTenant, TenantFromCtx, Session, WithSuperuser, RawDB
```

`internal/storage/rls.go` is added in Phase 3.

## Deliverables

- New files:
  - `internal/storage/storage.go`
  - `internal/storage/models.go`
  - `internal/storage/migrate.go`
  - `internal/storage/tenant.go`
- Modified files:
  - `go.mod` / `go.sum` — add `gorm.io/gorm`, `gorm.io/driver/postgres`, `github.com/oklog/ulid/v2`.
  - `internal/config/config.go` — `DatabaseConfig` struct (Phase 2 finalizes the rest of the config surface; for Phase 1 we add just the database fields).
- New files:
  - `internal/ids/ids.go`, `internal/ids/prefixes.go` (ULID + prefix helpers).

## Security & operational notes

- Do **not** log raw DSNs (they may contain passwords). Use a redacted variant.
- Production and dev both run Postgres 18.2 via Docker Compose ([Phase 0](phase-00-dev-environment.md) / [Phase 11](phase-11-production-deployment.md)) — no "works on my laptop with a different driver" surprises.

## Verification

- `go build ./...` and `go vet ./...` pass.
- Integration tests run against a `postgres:18.2-alpine` container brought up via `testcontainers-go`:
  - Round-trip create/read/update/delete for every model.
  - Unique-constraint violations are reported as such (e.g. duplicate `(tenant_id, email)` on `User`).
  - `Session(ctx)` returns a `*gorm.DB` that yields tenant A's rows only.

## Risks

- **GORM v2 quirks**: `AutoMigrate` won't drop columns or change types in destructive ways — we accept that and revisit with golang-migrate if/when schema changes get destructive.
- **ULID generation**: ULIDs are generated in Go (`ulid.Make()`); no server-side extension needed. `ulid.Make` uses a default monotonic entropy source so two calls in the same millisecond emit strictly increasing values. Worth a smoke test to confirm uniqueness under high concurrency.
- **Encrypted column types**: stored as `bytea`; verify GORM struct tags emit the right DDL.
- **Internal `ID` leakage**: any handler that accidentally serializes the bigint `ID` instead of `PublicID` is a privacy/enumeration footgun. A lint pass (or a `MarshalJSON` that hides `ID`) should catch this; document the convention prominently.

## Checklist

- [x] `gorm.io/gorm`, `gorm.io/driver/postgres`, `github.com/oklog/ulid/v2` added to `go.mod`
- [x] `internal/ids/ids.go` exports `New(prefix)`, `Parse`, `MustParse`; prefix list in `internal/ids/prefixes.go`
- [x] `internal/storage/storage.go` exports `Open(cfg)` with Postgres pool tuning
- [x] `internal/storage/models.go` defines `Base` (`ID int64`, `PublicID string`, `CreatedAt`, `UpdatedAt`, `DeletedAt gorm.DeletedAt`) and embeds it in every model
- [x] All audit columns are `timestamptz`; `CreatedAt`/`UpdatedAt` default to `now()`; `DeletedAt` indexed
- [x] Composite unique indexes are partial (`WHERE deleted_at IS NULL`) so soft-deletes don't block re-creation
- [x] Every new row sets `PublicID = ids.New(<prefix>)` via a `BeforeCreate` GORM hook (or explicit assignment in repository code)
- [x] `crypto.SecretField` stubbed as a `[]byte` alias for Phase 1 (Phase 2 replaces the impl, not the type)
- [x] `internal/storage/migrate.go` exports `Migrate(ctx)` running `AutoMigrate`
- [x] `internal/storage/tenant.go` exports `WithTenant`, `TenantFromCtx`, `Session`, `WithSuperuser`, `RawDB`
- [x] `DatabaseConfig` added to `internal/config/config.go`
- [x] Integration tests cover CRUD for every model against a `postgres:18.2-alpine` testcontainer, asserting `PublicID` round-trips and has the expected prefix
- [x] Test verifies ULID lexicographic ordering matches insertion order
- [x] Test verifies soft-deleted rows are invisible to default queries and visible under `Unscoped()`
- [x] Test verifies a soft-deleted row does not block re-insertion of a new row with the same logical key (partial-unique-index behavior)
- [x] Test verifies `Session(ctx)` scopes queries to the tenant in ctx
- [x] Test verifies `(tenant_id, email)` and `(tenant_id, name)` unique constraints fire as expected
- [x] `go build ./...` and `go vet ./...` clean

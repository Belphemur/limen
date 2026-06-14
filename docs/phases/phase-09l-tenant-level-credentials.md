---
phase: "9l"
title: "Two-Layer MCP Credentials (UpstreamTenantLink for admin, UpstreamLink for users)"
status: planned
progress: 0
depends_on: ["7", "8", "9b", "9c", "9g"]
updated: "2026-05-30"
---

# Phase 9l — Two-Layer MCP Credentials (UpstreamTenantLink for admin, UpstreamLink for users)

> **Depends on**: Phase 7 (upstream strategies, `RequiresLink`), Phase 8 (per-user injection + catalog), Phase 9b (portal SPA), Phase 9c (admin SPA, `McpServers.vue`), Phase 9g (static-header rework — sister phase, splits static_header into TenantOwner/BYOK)
> **Unblocks**: health-tracked admin credentials for all strategies; clean separation of admin and user credential lifecycles; catalog indexing and connection verification without per-user dependency

## Goal

Add a new `upstream_tenant_links` table that holds admin-level credentials (OAuth tokens, setup keys, shared secrets) at the tenant level. These are used exclusively for catalog indexing, connection verification, and relink bootstrapping — never for routing actual tool calls. Per-user credentials remain in `upstream_links` unchanged. mcp_spec stays per-user OAuth (RequiresLink=true); static_header TenantOwner shared secret moves from ConfigJSON to UpstreamTenantLink for health tracking.

## Background

The admin needs credentials to index the tool catalog, verify connections, and bootstrap user relinking. These admin credentials need health tracking (consecutive failures, auto-disable) — the same lifecycle UpstreamLink already provides. UpstreamStrategyConfig.ConfigJSON is stateless config; it doesn't track health. UpstreamTenantLink fills this gap.

Today the upstream strategies have this `RequiresLink` surface:

| Strategy | `RequiresLink` | Who owns credentials |
|----------|---------------|---------------------|
| `none` | `false` | Nobody — no auth |
| `mcp_spec` | `true` | Every user who connects (their own OAuth tokens via DCR) |
| `static_header` TenantOwner | `true` | Shared secret (admin-managed) |
| `static_header` BYOK | `true` | Each user (their own API key in `ExtraJSON`) |

After this phase, the credential model becomes two-layered:

| Strategy | `RequiresLink` | Tenant creds live in | User creds live in |
|----------|---------------|---------------------|-------------------|
| `none` | `false` | (empty) | N/A |
| `mcp_spec` | `true` | `upstream_tenant_links` | `upstream_links` |
| `static_header` TenantOwner | `true` | `upstream_tenant_links` (SharedSecret) | `upstream_links` (Enabled only) |
| `static_header` BYOK | `true` | `upstream_tenant_links` (admin setup key) | `upstream_links` (user BYOK key) |

### Sister phase relationship with 9g

Phase 9g and Phase 9l are complementary. Phase 9g splits `static_header` into TenantOwner and BYOK sub-modes. Phase 9l adds the admin credential layer (`UpstreamTenantLink`) that gives all strategies health-tracked admin credentials for catalog/verification. Together they produce the full strategy landscape shown above.

## Design

### New `upstream_tenant_links` table

```
upstream_tenant_links
├── id            bigint PK (internal)
├── public_id     text UNIQUE (ulnk_tnl_<ULID>)
├── tenant_id     bigint FK → tenants(id)
├── upstream_id   bigint FK → upstreams(id)
├── access_token  bytea  (SecretField, AAD: tenant|"upstream.access_token")
├── refresh_token bytea  (SecretField, AAD: tenant|"upstream.refresh_token")
├── registration_data bytea (SecretField, AAD: tenant|"upstream.registration_data")
├── extra_json    bytea  (SecretField, AAD: tenant|"upstream.extra")
├── expires_at    timestamptz
├── scopes        text[]
└── health columns (mirrored from upstream_links):
    ├── enabled              boolean DEFAULT true
    ├── consecutive_failures int    DEFAULT 0
    ├── first_failure_at     timestamptz
    ├── last_failure_at      timestamptz
    ├── last_failure_reason  text
    └── auto_disabled_at     timestamptz

UNIQUE (tenant_id, upstream_id) WHERE deleted_at IS NULL
INDEX idx_tenant_links_refresh (expires_at, needs_relink, auto_disabled_at, enabled)
```

Key decisions:
- **No `UserID` or `ServiceAccountID`** — this table is purely tenant-scoped.
- **AAD uses only `tenant_id`** (no user component), matching the `upstream_strategy_configs` pattern for tenant-wide secrets.
- **New prefix: `ulnk_tnl_`** (distinct from `ulnk_` for user/SA links) so ULIDs are visually distinguishable in logs.
- **Health columns mirrored 1:1** from `upstream_links`. `RecordSuccess`/`RecordFailure` gain a `table string` parameter (`"upstream_links"` or `"upstream_tenant_links"`) to avoid PK collision between the two auto-increment sequences. `MaybeAutoDisableForRelink` sweeps both tables.

### Two-layer credential model

The core principle of this phase: **UpstreamTenantLink and UpstreamLink serve different purposes and never replace each other.**

**UpstreamTenantLink** is the admin's credential repository. Used by:
- The catalog indexer (tools/list calls to keep the tool registry fresh)
- Connection verification ("Test Connection" in admin SPA)
- Relink bootstrapping (priming new user OAuth flows with upstream metadata)

**UpstreamLink** is the per-user credential. Used by:
- The MCP gateway when routing actual tool calls
- Every user MUST link individually for `mcp_spec` and BYOK — there is no fallback to tenant credentials
- For `static_header` TenantOwner, user links carry only the `Enabled` flag (opt-out toggle)

**No fallback**: `mcp_spec` and BYOK never fall back to tenant credentials for tool calls. If a user hasn't linked, they get an error.

### Header resolution pseudocode (gateway path)

```
if strategy.RequiresLink():
    // Per-user credential always wins for tool calls
    user, ok := userResolver(ctx)
    if !ok → ErrNoUser
    link := loadUserLink(user.ID)
    if !link.Enabled or link.AutoDisabledAt: → ErrNeedsRelink

    if strategy.Type() == "mcp_spec":
        → strategy.Headers(ctx, link)           // user's OAuth tokens
    if strategy.Type() == "static_header":
        if subMode == "tenant_owner":
            → strategy.Headers(ctx, link)       // tenant SharedSecret from TenantLink (with user Enabled flag)
        else: // BYOK
            if link.ExtraJSON has key and !link.NeedsRelink:
                → user's BYOK key
            else:
                → ErrNoCredentials              // NO fallback to admin setup key!
else: // RequiresLink == false (none only)
    → no auth
```

### Catalog / Connection Verification pseudocode

```
tenantLink := loadTenantLink()
if tenantLink == nil → ErrNoTenantLink
→ strategy.Headers(ctx, lctx{TenantLink: tenantLink})
```

### `mcp_spec` — admin OAuth for catalog, per-user OAuth for tools

mcp_spec keeps `RequiresLink() = true` — every user must complete their own OAuth flow to use the upstream's tools. This is unchanged from the current architecture.

What's new: the admin ALSO completes an OAuth flow, and those tokens are stored in `UpstreamTenantLink`. These admin tokens are used by:
- The catalog indexer (tools/list calls to keep the tool registry current)
- Connection verification (admin "Test Connection" button)
- Relink bootstrapping (priming the discovery cache for user OAuth flows)

The strategy gains a `StartTenantLink` / `FinishTenantLink` path that's parallel to the existing per-user `StartLink` / `FinishLink`. Both use the same PKCE + DCR flow, but tenant tokens are encrypted with tenant-only AAD.

### `static_header` TenantOwner — SharedSecret moves to UpstreamTenantLink

The SharedSecret currently lives encrypted inside `UpstreamStrategyConfig.ConfigJSON`. It moves to `UpstreamTenantLink.AccessToken` (encrypted with tenant AAD). This gives it health tracking: if the key is invalid, the catalog indexer records failures and the admin SPA can surface the issue.

`UpstreamStrategyConfig.ConfigJSON` becomes purely non-secret configuration: `header_name`, `header_template`, and the `Mode` column distinguishes TenantOwner from BYOK.

### `static_header` BYOK — admin setup key in UpstreamTenantLink

The admin provides a setup key for catalog indexing and connection verification. This goes in `UpstreamTenantLink`. Per-user BYOK keys stay in `UpstreamLink.ExtraJSON`.

Critical: the gateway NEVER falls back to the admin setup key for tool calls. A user without a BYOK key gets `ErrNoCredentials`.

### `LinkContext` extension

`internal/upstream/strategy.go`:

```go
type LinkContext struct {
    Tenant       *storage.Tenant
    User         *storage.User
    Upstream     *storage.Upstream
    Link         *storage.UpstreamLink         // per-user link
    TenantLink   *storage.UpstreamTenantLink   // tenant link (new)
    ReturnTo     string
    ServiceAccountID *int64
}
```

`TenantLink` is populated when the strategy resolves a tenant-scoped link. Strategies that use it access tokens/health through this field instead of `Link`.

### DBAuthProvider changes

`internal/upstream/authprovider.go`:

- **Construction validation (Fail Fast)**: `NewDBAuthProvider` validates `UserResolver` at build time. If `RequiresLink()==true` AND `resolver==nil` → return error immediately. This prevents runtime nil dereference.
- `linkContext()`: for `RequiresLink == true` strategies:
  - Always resolve the user and load `UpstreamLink`.
  - Populate `lctx.TenantLink` with `UpstreamTenantLink` for strategies that need admin credentials (mcp_spec, static_header TenantOwner).
- **Gateway path (tool calls)**: resolves only `UpstreamLink` for credential injection.
- **Catalog/verification path**: resolves `UpstreamTenantLink` for credential injection.
- `loadTenantLink(ctx)`: new method, mirrors `loadLink` but queries `upstream_tenant_links WHERE tenant_id = ? AND upstream_id = ?`. Decrypts with AAD `(tenantStr, "", kind)` — no user component.
- **`AuthResult` extension**: gains `LinkID int64` (TenantLink.ID or UpstreamLink.ID or 0 for `none`) and `LinkTable string` (`"upstream_tenant_links"`, `"upstream_links"`, or `""`). `LinkTable` tells `AuthInjectingTransport` which table to record health against, resolving the PK collision between the two auto-increment sequences.

### Refresher changes

`internal/upstream/refresher.go`:

**Token refresh sweep** (`sweep` / `dueLinks` / `maintainOne`):

- After iterating `upstream_links`, add a parallel sweep of `upstream_tenant_links`:
  ```go
  tenantLinks, err := r.dueTenantLinks(ctx)
  // ... for each: resolve tenant + upstream, decrypt with tenant-only AAD, strat.Maintain(ctx, lctx{TenantLink: link})
  ```
- `dueTenantLinks()`: mirrors `dueLinks()` but queries `upstream_tenant_links` with the same filter (expires_at < cutoff, enabled, !needs_relink, !auto_disabled_at).
- `autoDisableForRelink` sweep: also sweep `upstream_tenant_links` with the same `NeedsRelink` window logic.

**Catalog sweep** (`sweepCatalog` / `indexOneUpstream`):

- For all strategies with `RequiresLink=true` that have admin credentials: index using the tenant link's tokens/key for the `tools/list` call.
- `indexOneUpstream`: load `UpstreamTenantLink` and pass it as the credential source to `IndexUpstream`.

### Gateway wiring

- `internal/gateway/bundle.go`: `NewBundle` constructs the `AuthProvider` — passes the tenant to `NewDBAuthProvider`.
- `internal/gateway/manager.go`: `Bundle` construction calls `NewDBAuthProvider` with the tenant. Verifies tenant-link and user-link paths work correctly.
- `internal/gateway/authtransport.go`: `AuthInjectingTransport` reads `AuthResult.LinkID` and `AuthResult.LinkTable` to decide health tracking target. On 401 retry, calls `HeadersForceRefresh` via `AuthProvider`. `ErrNoTenantLink` returns a 503-like "admin connection required" signal for catalog paths. `ErrNeedsRelink` for user-facing tool calls. `RecordSuccess`/`RecordFailure` accept `(ctx, store, linkID, table, ...)` where `table` disambiguates `"upstream_links"` vs `"upstream_tenant_links"`.

### Admin SPA action buttons on `McpServers.vue`

The existing `McpServers.vue` table has Connect and Reindex buttons. Phase 9l extends the action column with strategy-specific CTAs:

| Strategy | Sub-mode / state | Action buttons |
|----------|-----------------|----------------|
| `mcp_spec` | No tenant link | **Connect** (opens OAuth popup — admin initiates) |
| `mcp_spec` | Tenant link connected | **Relink** (re-run OAuth flow, replaces existing tokens) |
| `mcp_spec` | Tenant link disabled | **Enable** |
| `static_header` TenantOwner | Always | **Rotate Key** (replaces the SharedSecret in UpstreamTenantLink) |
| `static_header` BYOK | Always | **Rotate Setup Key** (replaces admin setup key; does not clear per-user keys) |
| `none` | Always | _(no actions)_ |

The Connect/Relink buttons use the same `openOAuthPopup` pattern, but call an admin-scoped RPC. The Rotate buttons will be wired after the admin proto gains secret-rotation RPCs.

Note: these buttons are an **additive change** to the existing action column. The current Connect, Edit, Reindex, and Delete buttons remain. The new buttons appear between Connect and Edit.

### `secretClearer` interface — extension for tenant links

The existing `secretClearer` interface in `portal_ops.go` clears a user's BYOK key. For tenant-level credentials, we need a parallel capability to clear/reset a **tenant link**. A new `tenantLinkClearer` interface:

```go
type tenantLinkClearer interface {
    ClearTenantLink(ctx context.Context, lctx LinkContext) error
}
```

Implemented by `mcpspec` — zeroes tokens and registration, resets health counters. Called by the admin SPA's "Relink" action.

### Proto extensions

`proto/limen/portal/v1/portal.proto`:

- `UpstreamSummary` gains:
  - `bool has_tenant_link = 14;` — true when `upstream_tenant_links` has an active row for this upstream.
  - `string tenant_link_state = 15;` — `"none" | "connected" | "needs_relink" | "auto_disabled" | "disabled"` — mirrors `link_state` but for the tenant link.
  - `bool has_user_override = 13;` — already in Phase 9g (BYOK indicator).

`ClearUpstreamOverride` RPC (Phase 9g) stays as-is — it clears per-user overrides, which remain per-user.

### Post-refactor strategy summary

| Strategy | RequiresLink | Tenant creds live in | User creds live in | Tool calls use |
|----------|-------------|---------------------|-------------------|---------------|
| `none` | `false` | (empty) | N/A | Nothing |
| `mcp_spec` | `true` | `upstream_tenant_links` | `upstream_links` | UserLink |
| `static_header` TenantOwner | `true` | `upstream_tenant_links` (SharedSecret) | `upstream_links` (Enabled) | UserLink |
| `static_header` BYOK | `true` | `upstream_tenant_links` (admin setup key) | `upstream_links` (user BYOK key) | UserLink |

## Files

### Backend (Go)

| File | Change |
|------|--------|
| `internal/storage/model_upstream_tenant_link.go` | **New** — `UpstreamTenantLink` model: tenant-scoped, mirrors `UpstreamLink` health columns, no `UserID` |
| `internal/ids/prefixes.go` | Add `PrefixUpstreamTenantLink = "ulnk_tnl"` |
| `internal/storage/models.go` | Register `&UpstreamTenantLink{}` in `AllModels()` |
| `internal/storage/migrations/postgres/00015_tenant_links.sql` | **New** — create table, partial unique index, refresh index, health columns, down is no-op |
| `internal/upstream/strategy.go` | `LinkContext` gains `TenantLink *storage.UpstreamTenantLink`; add `tenantLinkClearer` interface; add `ErrNoTenantLink` sentinel |
| `internal/upstream/mcpspec/strategy.go` | Adds StartTenantLink/FinishTenantLink for admin OAuth; RequiresLink stays true; Headers stays per-user |
| `internal/upstream/authprovider.go` | DBAuthProvider: resolve tenant link for catalog/verification paths, user link for tool-call paths; `loadTenantLink()` method |
| `internal/upstream/refresher.go` | `sweep()`: add `dueTenantLinks()` + `maintainTenantLink()`; `MaybeAutoDisableForRelink` also sweeps tenant links; `sweepCatalog()`: index using tenant link credentials |
| `internal/upstream/service.go` | Add tenant-level `Connect` / `Disconnect` / `Relink` paths that build `LinkContext{TenantLink: nil}` → `StartTenantLink`/`FinishTenantLink` creates tenant link |
| `internal/upstream/portal_ops.go` | `UserUpstreamSummary` gains `HasTenantLink bool`, `TenantLinkState LinkState`; `applyStrategyMeta` sets `HasTenantLink` for strategies with tenant links; `summariseUpstream` populates tenant link state; new `ClearUpstreamOverride` remains per-user |
| `internal/upstream/protoview/protoview.go` | `ToSummaryProto` propagates `HasTenantLink`, `TenantLinkState` |
| `internal/gateway/bundle.go` | Wire tenant-resolution `AuthProvider` — no change to Bundle shape, `NewDBAuthProvider` handles the tenant-link path |
| `internal/gateway/manager.go` | Bundle construction passes tenant to `NewDBAuthProvider` — already wired, verify tenant-link and user-link paths work |
| `internal/gateway/authtransport.go` | `LinkID=0` still means "no link" (for `none`); `LinkID != 0` covers both user and tenant links for health recording; handle `ErrNoTenantLink` and `ErrNeedsRelink` |
| `internal/admin/upstreams.go` | `ReindexUpstreamCatalog`: for all strategies with tenant links, use tenant link credentials for `tools/list` |
| `internal/portal/upstreams.go` | `ClearUpstreamOverride` handler (Phase 9g) — verify still works for BYOK mode; no change to mcp_spec path |

### Frontend

| File | Change |
|------|--------|
| `web/admin/src/pages/McpServers.vue` | Add Connect/Relink/Enable buttons for `mcp_spec`; add Rotate Key button for `static_header` TenantOwner; add Rotate Setup Key button for `static_header` BYOK — decision logic driven by `strategy_type` + `tenant_link_state` |
| `web/portal/src/lib/upstream-cta.ts` | Updated decision table: `mcp_spec` rows still show Connect CTA (per-user OAuth required); BYOK rows unchanged (user still submits own key); TenantOwner rows show Enable/Disable toggle |

### Proto

| File | Change |
|------|--------|
| `proto/limen/portal/v1/portal.proto` | `UpstreamSummary`: add `has_tenant_link`, `tenant_link_state`; retain `has_user_override` (Phase 9g); retain `ClearUpstreamOverride` RPC (per-user, unchanged) |

## Checklist

- [ ] `UpstreamTenantLink` model defined in `model_upstream_tenant_link.go` with `TenantID`, `UpstreamID`, encrypted token fields, mirrored health columns, `BeforeCreate` with `PrefixUpstreamTenantLink`
- [ ] `PrefixUpstreamTenantLink = "ulnk_tnl"` added to `prefixes.go`
- [ ] `&UpstreamTenantLink{}` registered in `AllModels()`
- [ ] Migration `00015_tenant_links.sql` creates `upstream_tenant_links` table, partial unique `(tenant_id, upstream_id) WHERE deleted_at IS NULL`, refresh index, `Down` is no-op
- [ ] `LinkContext` gains `TenantLink *storage.UpstreamTenantLink` field in `strategy.go`
- [ ] `tenantLinkClearer` interface defined in `strategy.go`
- [ ] `ErrNoTenantLink` sentinel defined in `strategy.go`
- [ ] `mcpspec/strategy.go`: adds `StartTenantLink`/`FinishTenantLink` for admin OAuth, `RequiresLink` stays `true`, `Headers` stays per-user
- [ ] `mcpspec` state envelope gains `is_tenant_link` flag to distinguish tenant vs user OAuth flows
- [ ] `DBAuthProvider.linkContext()`: resolve tenant link for catalog/verification paths, resolve user link for tool-call paths
- [ ] `DBAuthProvider.loadTenantLink()` method: queries `upstream_tenant_links`, decrypts with tenant-only AAD
- [ ] `DBAuthProvider.Headers()` returns `AuthResult{LinkID: UpstreamLink.ID, LinkTable: "upstream_links"}` for tool-call paths
- [ ] Refresher `sweep()`: add `dueTenantLinks()` and tenant-link maintain loop
- [ ] Refresher: `MaybeAutoDisableForRelink` also sweeps `upstream_tenant_links`
- [ ] Refresher `sweepCatalog()`: index using tenant link credentials for all strategies that have them
- [ ] `upstream.Service`: add tenant-level `Connect`/`Disconnect`/`Relink` methods
- [ ] `UserUpstreamSummary` gains `HasTenantLink bool` and `TenantLinkState LinkState`
- [ ] `applyStrategyMeta`: set `HasTenantLink = true` for strategies with tenant links
- [ ] `summariseUpstream`: load tenant link state
- [ ] `protoview.ToSummaryProto`: propagate `HasTenantLink` and `TenantLinkState` to proto
- [ ] Proto: add `has_tenant_link` and `tenant_link_state` fields to `UpstreamSummary`; `buf generate`
- [ ] `authtransport.go`: handle `ErrNoTenantLink` and `ErrNeedsRelink`; `LinkID != 0` covers both tables for health recording
- [ ] `admin/upstreams.go`: `ReindexUpstreamCatalog` uses tenant link credentials for catalog indexing
- [ ] `web/admin McpServers.vue`: Connect/Relink/Enable buttons for `mcp_spec`; Rotate Key for static_header TenantOwner; Rotate Setup Key for BYOK
- [ ] `web/portal upstream-cta.ts`: `mcp_spec` rows still show Connect CTA (per-user required); BYOK rows unchanged; TenantOwner shows Enable/Disable toggle
- [ ] `static_header` SharedSecret moved from ConfigJSON to UpstreamTenantLink
- [ ] `static_header` BYOK admin setup key stored in UpstreamTenantLink
- [ ] `static_header` BYOK gateway: no fallback to admin setup key — `ErrNoCredentials` for users without BYOK key
- [ ] `mcp_spec` admin OAuth flow persists to UpstreamTenantLink; per-user OAuth persists to UpstreamLink unchanged
- [ ] `go mod tidy && go fmt ./... && go vet ./... && golangci-lint run ./... && go test -race ./...` all clean
- [ ] `buf generate` — only `portal.proto` diff (new fields on `UpstreamSummary`)
- [ ] `cd web/portal && pnpm test && pnpm build` green
- [ ] `cd web/admin && pnpm test && pnpm build` green

## Deliverables

| File | Change |
|------|--------|
| `internal/storage/model_upstream_tenant_link.go` | New model |
| `internal/ids/prefixes.go` | New prefix |
| `internal/storage/models.go` | AllModels registration |
| `internal/storage/migrations/postgres/00015_tenant_links.sql` | New migration |
| `internal/upstream/strategy.go` | LinkContext.TenantLink, tenantLinkClearer, ErrNoTenantLink |
| `internal/upstream/mcpspec/strategy.go` | StartTenantLink/FinishTenantLink for admin OAuth |
| `internal/upstream/authprovider.go` | Tenant link resolution for catalog/verification; user link for tool calls |
| `internal/upstream/refresher.go` | Tenant link sweep + catalog indexing via tenant link |
| `internal/upstream/service.go` | Tenant-level Connect/Disconnect/Relink |
| `internal/upstream/portal_ops.go` | HasTenantLink, TenantLinkState in summary |
| `internal/upstream/protoview/protoview.go` | Propagate new fields |
| `internal/gateway/authtransport.go` | Handle ErrNoTenantLink, ErrNeedsRelink; LinkID covers both tables |
| `internal/admin/upstreams.go` | Tenant-link aware reindex |
| `proto/limen/portal/v1/portal.proto` | has_tenant_link, tenant_link_state, ClearUpstreamOverride (retain) |
| `web/admin/src/pages/McpServers.vue` | Add action buttons |
| `web/portal/src/lib/upstream-cta.ts` | Updated decision table |
| `docs/phases/phase-09l-tenant-level-credentials.md` | This file |
| `docs/phases/README.md` | Updated index |

## Verification

- `go mod tidy && go fmt ./... && go vet ./... && golangci-lint run ./... && go test -race ./...`
- `buf generate` — the only generated diff is `has_tenant_link` / `tenant_link_state` fields on `UpstreamSummary`.
- `cd web/portal && pnpm test && pnpm build` — portal CTA decision table tests cover: `mcp_spec` = Connect CTA (per-user required), `none` = no CTA, `static_header` TenantOwner = Enable/Disable, `static_header` BYOK = Submit/Rotate/Enable.
- `cd web/admin && pnpm test && pnpm build` — admin SPA: new action buttons render correctly for each strategy/state combination.
- Manual smoke (dev compose): create `mcp_spec` upstream → admin Connects (tenant tokens) → catalog populates → users still need to Connect individually (user tokens) → tool calls use user tokens. Create `static_header` TenantOwner → SharedSecret in UpstreamTenantLink → catalog populates directly → users see tools immediately.

## Out of scope

- An "Edit Upstream" SPA action for `mcp_spec` (changing MCP URL, scopes, or OAuth client overrides in place). v1 still recreates the upstream.
- Admin "Rotate Key" / "Rotate Setup Key" RPCs in proto — the admin SPA buttons are wired to stubs; the actual secret-rotation proto RPCs are a Phase 10 hardening task.
- `upstream_tenant_links` preservation on the existing dev DBs — the migration is additive (new table), no wipe needed.
- Portal SPA changes beyond the CTA decision table — the portal CTA changes reflect the new per-user linking requirement.

## Risks

- **`upstream_strategy_configs.ConfigJSON` SharedSecret migration**: moving the SharedSecret from ConfigJSON to UpstreamTenantLink requires a data migration. The migration must decrypt with the old AAD, re-encrypt with tenant-only AAD, and write to the new table. A failure here leaves the admin without a working secret.
- **Dual AAD schemes**: User links use AAD `(tenant, user, kind)`, tenant links use AAD `(tenant, "", kind)`. A mismatch between encrypt and decrypt AAD makes tokens unreadable. The `UpstreamTenantLink` model must set AAD consistently with no user component, and `loadTenantLink` must decrypt with the matching AAD.
- **Dual credential resolution in DBAuthProvider**: DBAuthProvider now resolves credentials from two different tables depending on the path (catalog vs tool-call). The switching logic must be exact — a mistake could send user tokens to the catalog indexer or admin tokens to the gateway's tool-call path.
- **mcp_spec dual OAuth: admin and user OAuth flows coexist. The state envelope must distinguish them so FinishLink vs FinishTenantLink routes correctly.**
- **Refresher concurrency**: The refresher now sweeps two tables (`upstream_links` + `upstream_tenant_links`). Adding a second sweep doubles the refresh query load. For v1 deployments with a handful of upstreams this is negligible, but the `limit(500)` on both sweeps should be monitored if upstream count grows.
- **Admin SPA button complexity**: The action column now has 7 possible buttons (Connect, Relink, Edit, Reindex, Delete, Rotate Key, Rotate Setup Key) conditionally shown. The `v-if` logic must be well-tested so no button appears for the wrong strategy/state combination.
- **No-fallback guarantee for BYOK**: The BYOK gateway path must never fall back to the admin setup key. Any code path that checks ExtraJSON and finds it empty must return `ErrNoCredentials`, not attempt to load from the tenant link.

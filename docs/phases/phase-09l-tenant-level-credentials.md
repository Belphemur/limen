---
phase: "09l"
title: "Tenant-Level MCP Credentials (upstream_tenant_links + mcp_spec rework)"
status: planned
progress: 0
depends_on: ["7", "8", "9b", "9c", "9g"]
updated: "2026-05-30"
---

# Phase 9l — Tenant-Level MCP Credentials (upstream_tenant_links + mcp_spec rework)

> **Depends on**: Phase 7 (upstream strategies, `RequiresLink`), Phase 8 (per-user injection + catalog), Phase 9b (portal SPA), Phase 9c (admin SPA, `McpServers.vue`), Phase 9g (static-header rework — sister phase, shares the "no per-user link for tenant-wide creds" philosophy)
> **Unblocks**: tenant self-service MCP linking (no admin popup); clean `RequiresLink=false` for `mcp_spec` post-refactor; admin SPA action buttons without portal coupling

## Goal

Move `mcp_spec` (OAuth-via-PRM) from **per-user requiring-link** to **tenant-level**: one set of OAuth credentials per (tenant, upstream), shared by all users. The strategy's `RequiresLink()` flips to `false`. This creates a new `upstream_tenant_links` table to hold tenant-scoped tokens, refresh state, and health tracking — the same shape `UpstreamLink` has today, but keyed by `(tenant_id, upstream_id)` instead of `(tenant_id, user_id, upstream_id)`.

## Background

Today the upstream strategies have this `RequiresLink` surface:

| Strategy | `RequiresLink` | Who owns credentials |
|----------|---------------|---------------------|
| `none` | `false` | Nobody — no auth |
| `mcp_spec` | `true` | Every user who connects (their own OAuth tokens via DCR) |
| `static_header` shared | `false` | The admin (one shared secret in `ConfigJSON`) |
| `static_header` BYOK | `true` | Each user (their own API key in `ExtraJSON`, fallback to shared) |

The problem: `mcp_spec` at `RequiresLink=true` means every tenant member must walk through the full OAuth flow (popup → authorize → callback → tokens → DCR registration) to access upstream tools. In practice, upstreams like Atlassian Rovo, GitHub, and Linear are configured by **the admin**, and all tenant members should see the same tools immediately — no per-user ceremony.

Phase 9g solved this problem for `static_header` shared mode by setting `RequiresLink=false` — the admin's config becomes the tenant-wide secret, and the portal drops the per-user CTA. Phase 9l does the same surgery for `mcp_spec`:

- Post-refactor `mcp_spec` has `RequiresLink=false`.
- OAuth tokens (access/refresh), DCR registration, and health tracking live in a new `upstream_tenant_links` table.
- The admin initiates the OAuth flow from the admin SPA (same popup → callback → callback transport flow, but the link row belongs to the tenant, not the user).
- All users see the upstream's tools without connecting first.
- The gateway's `DBAuthProvider` gains a tenant-link resolution path for `!RequiresLink` strategies that still carry credentials (replacing the "skip auth" shortcut).

### Sister phase relationship with 9g

Phase 9g (static-header-rework) and Phase 9l converge on the same design principle: **when credentials are tenant-wide, there should be no per-user link row**. After both phases land, the upstream landscape looks like this:

| Strategy | Post-refactor `RequiresLink` | Where credentials live |
|----------|-----------------------------|------------------------|
| `none` | `false` | Nowhere |
| `mcp_spec` | `false` | `upstream_tenant_links` (new table) |
| `static_header` shared | `false` | `upstream_strategy_configs.ConfigJSON` (existing) |
| `static_header` BYOK | `true` | `upstream_links.ExtraJSON` (per-user, existing) |

The gateway's `DBAuthProvider` will need to handle two sub-cases of `RequiresLink=false`:
1. **No credentials needed** (`none`) — skip auth entirely (current shortcut).
2. **Tenant credentials needed** (`mcp_spec`, `static_header` shared) — resolve from the tenant-scoped source.

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
- **New prefix: `ulnk_tnl`** (distinct from `ulnk` for user/SA links) so ULIDs are visually distinguishable in logs.
- **Health columns mirrored 1:1** from `upstream_links`. `RecordSuccess`/`RecordFailure` gain a `table string` parameter (`"upstream_links"` or `"upstream_tenant_links"`) to avoid PK collision between the two auto-increment sequences. `MaybeAutoDisableForRelink` sweeps both tables.

### Header resolution order (gateway path)

After this refactor, `DBAuthProvider.Headers()` resolves credentials as follows:

```
if strategy.RequiresLink():
    user, ok := userResolver(ctx)
    if !ok → ErrNoUser
    link := loadUserLink(user.ID)
    if !link.Enabled or link.AutoDisabledAt != nil → ErrNeedsRelink
    → strategy.Headers(ctx, link)
else:
    switch strategy.Type():
        case "none":
            → empty headers (no auth needed)
        case "mcp_spec":
            link := loadTenantLink()
            if link == nil or !link.Enabled or link.AutoDisabledAt != nil → ErrNoTenantLink
            → strategy.Headers(ctx, link)
        case "static_header":
            if subMode == "shared":
                → decrypt SharedSecret from ConfigJSON
            else (BYOK):
                user, ok := userResolver(ctx)
                if !ok → shared secret fallback
                link := loadUserLink(user.ID)
                if link and link.ExtraJSON not zero and !link.NeedsRelink:
                    → decrypt ExtraJSON (user's key wins)
                else:
                    → decrypt SharedSecret from ConfigJSON (fallback)
```

### `mcp_spec` strategy rework (`RequiresLink → false`)

The strategy implementation at `internal/upstream/mcpspec/strategy.go` is rewritten:

- `RequiresLink() → false` — no per-user link required.
- `Provision()` unchanged — creates/validates upstream row.
- `StartLink()` / `FinishLink()` — now operate on `upstream_tenant_links` instead of `upstream_links`. The `LinkContext` gains `TenantLink *storage.UpstreamTenantLink` (see below).
- `Headers()` / `HeadersForceRefresh()` — read/write `TenantLink` instead of `Link`. Token refresh uses tenant-scoped AAD (no user component).
- `Maintain()` — iterates `upstream_tenant_links` instead of `upstream_links`. Refresh logic identical but AAD is tenant-only.

**Critical: `StartLink` / `FinishLink` still exist.** The admin initiates the OAuth flow from the admin SPA (popup → authorize → callback). The callback handler at `transport/upstream.go` resolves the admin user from the OIDC session, but the link row created is a tenant link, not a user link. The OAuth state envelope needs a `TenantLink bool` flag to distinguish.

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

`TenantLink` is populated when the strategy resolves a tenant-scoped link (currently `mcp_spec` post-refactor). Strategies that use it access tokens/health through this field instead of `Link`.

### DBAuthProvider changes

`internal/upstream/authprovider.go`:

- **Construction validation (Fail Fast)**: `NewDBAuthProvider` validates `UserResolver` at build time. If `RequiresLink()==true` AND `resolver==nil` → return error immediately. If `RequiresLink()==false` → `nil` resolver is explicitly accepted. This prevents runtime nil dereference.
- **UserResolver relaxation**: strategies with `!RequiresLink()` may not need a user at all. When `RequiresLink == false && strategy.Type() != "none"`, we still need a tenant-link resolution path but no `UserResolver`.
- `linkContext()`: for `!RequiresLink` strategies:
  - `none`: return empty `LinkContext` (current shortcut, unchanged).
  - `mcp_spec`: load `UpstreamTenantLink` and populate `lctx.TenantLink`. If missing/disabled, return `ErrNoTenantLink`.
  - `static_header` shared: no link load needed — config is in `ConfigJSON` (current behavior).
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

- For `mcp_spec` strategies (now `RequiresLink=false`): index directly without a link — the tenant link provides OAuth tokens but the catalog is shared per-tenant regardless of which user's token was used. The refresher can use the tenant link's tokens for the `tools/list` call during indexing.
- `indexOneUpstream`: for `mcp_spec`, load `UpstreamTenantLink` and pass it as the credential source to `IndexUpstream` (which needs to decrypt tokens to call the upstream).

### Gateway wiring

- `internal/gateway/bundle.go`: `NewBundle` constructs the `AuthProvider` — passes the tenant to `NewDBAuthProvider`. No per-user link wiring needed for `mcp_spec`.
- `internal/gateway/manager.go`: `Bundle` construction calls `NewDBAuthProvider` with the tenant. For `mcp_spec`, the resulting provider resolves tenant links.
- `internal/gateway/authtransport.go`: `AuthInjectingTransport` reads `AuthResult.LinkID` and `AuthResult.LinkTable` to decide health tracking target. On 401 retry, calls `HeadersForceRefresh` via `AuthProvider`. `ErrNoTenantLink` returns a 503-like "admin connection required" signal upstream (parallel to existing `ErrLinkNotFound` handling). `RecordSuccess`/`RecordFailure` accept `(ctx, store, linkID, table, ...)` where `table` disambiguates `"upstream_links"` vs `"upstream_tenant_links"`.

### Admin SPA action buttons on `McpServers.vue`

The existing `McpServers.vue` table (line 301-331) already has a Connect button for `requiresLink && linkState !== CONNECTED` and a Reindex button. Phase 9l extends the action column with strategy-specific CTAs:

| Strategy | Sub-mode / state | Action buttons |
|----------|-----------------|----------------|
| `mcp_spec` | No tenant link | **Connect** (opens OAuth popup — admin initiates) |
| `mcp_spec` | Tenant link connected | **Relink** (re-run OAuth flow, replaces existing tokens) |
| `mcp_spec` | Tenant link disabled | **Enable** |
| `static_header` shared | Always | **Rotate Key** (replaces the shared secret in `ConfigJSON`) |
| `static_header` BYOK | Always | **Rotate Setup Key** (replaces `SharedSecret`; does not clear per-user keys) |
| `none` | Always | _(no actions)_ |

The Connect/Relink buttons use the same `openOAuthPopup` pattern already on line 139 (`connect(u)`), but call an admin-scoped RPC instead of `PortalService.StartConnect`. The Rotate buttons will be wired after the admin proto gains secret-rotation RPCs.

Note: these buttons are an **additive change** to the existing action column. The current Connect, Edit, Reindex, and Delete buttons remain. The new buttons appear between Connect and Edit.

### BYOK: setup key in `upstream_strategy_configs`

For `static_header` BYOK mode (Phase 9g's `AllowUserOverride=true`):

- The `SharedSecret` in `upstream_strategy_configs.ConfigJSON` serves as the **setup key** — used by the admin SPA for "Test Connection" and by the refresher's catalog indexer to verify the URL is reachable before announcing the upstream to the tenant.
- Per-user keys live in `UpstreamLink.ExtraJSON` as before — unchanged from Phase 9g.
- Gateway `Headers()` resolution: user BYOK key (if present and healthy) → setup key fallback.
- The refresher's catalog indexer uses the setup key for `mcp_spec` and `static_header` BYOK mode, and the tenant link's tokens for `mcp_spec`.

### `secretClearer` interface — extension for tenant links

The existing `secretClearer` interface in `portal_ops.go` (line 67) clears a *user's* override. For tenant-level `mcp_spec`, we need a parallel capability to clear/reset a **tenant link**. A new `tenantLinkClearer` interface:

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

`ClearUpstreamOverride` RPC (Phase 9g) stays as-is — it clears per-user overrides, which remain per-user even after `mcp_spec` goes tenant-level.

### Post-refactor strategy summary

| Strategy | RequiresLink | Credential source | Who connects |
|----------|-------------|------------------|-------------|
| `none` | `false` | None | Nobody |
| `mcp_spec` | `false` | `upstream_tenant_links` | Admin (tenant-level OAuth) |
| `static_header` shared | `false` | `upstream_strategy_configs.ConfigJSON.SharedSecret` | Admin |
| `static_header` BYOK | `true` | `upstream_links.ExtraJSON` → fallback `ConfigJSON.SharedSecret` | User (BYOK) / Admin (setup key) |

## Files

### Backend (Go)

| File | Change |
|------|--------|
| `internal/storage/model_upstream_tenant_link.go` | **New** — `UpstreamTenantLink` model: tenant-scoped, mirrors `UpstreamLink` health columns, no `UserID` |
| `internal/ids/prefixes.go` | Add `PrefixUpstreamTenantLink = "ulnk_tnl"` |
| `internal/storage/models.go` | Register `&UpstreamTenantLink{}` in `AllModels()` |
| `internal/storage/migrations/postgres/00015_tenant_links.sql` | **New** — create table, partial unique index, refresh index, health columns, down is no-op |
| `internal/upstream/strategy.go` | `LinkContext` gains `TenantLink *storage.UpstreamTenantLink`; add `tenantLinkClearer` interface; add `ErrNoTenantLink` sentinel |
| `internal/upstream/mcpspec/strategy.go` | **Rewritten** — `RequiresLink() → false`; `StartLink`/`FinishLink` create `UpstreamTenantLink`; `Headers`/`Maintain` use tenant-scoped AAD; `StartLink` populates `TenantLink` flag in state envelope |
| `internal/upstream/authprovider.go` | Tenant link resolution path in `linkContext()`: for `mcp_spec` (!RequiresLink), load `UpstreamTenantLink`; `loadTenantLink()` method; `LinkID` returns `TenantLink.ID` when `Link` is nil |
| `internal/upstream/refresher.go` | `sweep()`: add `dueTenantLinks()` + `maintainTenantLink()`; `MaybeAutoDisableForRelink` also sweeps tenant links; `sweepCatalog()`: for `mcp_spec`, index using tenant link tokens directly |
| `internal/upstream/service.go` | Add tenant-level `Connect` / `Disconnect` / `Relink` paths that build `LinkContext{TenantLink: nil}` → `StartLink`/`FinishLink` creates tenant link |
| `internal/upstream/portal_ops.go` | `UserUpstreamSummary` gains `HasTenantLink bool`, `TenantLinkState LinkState`; `applyStrategyMeta` sets `HasTenantLink` for `mcp_spec`; `summariseUpstream` populates tenant link state; new `ClearUpstreamOverride` remains per-user |
| `internal/upstream/protoview/protoview.go` | `ToSummaryProto` propagates `HasTenantLink`, `TenantLinkState` |
| `internal/gateway/bundle.go` | Wire tenant-resolution `AuthProvider` — no change to Bundle shape, `NewDBAuthProvider` handles the tenant-link path |
| `internal/gateway/manager.go` | Bundle construction passes tenant to `NewDBAuthProvider` — already wired, verify tenant-link path works |
| `internal/gateway/authtransport.go` | `LinkID=0` still means "no link" (for `none`); `LinkID != 0` covers both user and tenant links for health recording; handle `ErrNoTenantLink` parallel to `ErrLinkNotFound` |
| `internal/admin/upstreams.go` | `ReindexUpstreamCatalog`: for `mcp_spec`, use tenant link tokens for `tools/list` instead of "requires a user link" error |
| `internal/portal/upstreams.go` | `ClearUpstreamOverride` handler (Phase 9g) — verify still works for BYOK mode; no change to mcp_spec path |

### Frontend

| File | Change |
|------|--------|
| `web/admin/src/pages/McpServers.vue` | Add Connect/Relink/Enable buttons for `mcp_spec`; add Rotate Key button for `static_header` shared; add Rotate Setup Key button for `static_header` BYOK — decision logic driven by `strategy_type` + `tenant_link_state` |
| `web/portal/src/lib/upstream-cta.ts` | Updated decision table: `mcp_spec` rows now show no CTA in portal (admin handles linking); BYOK rows unchanged (user still submits own key) |

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
- [ ] `tenantLinkClearer` interface defined in `portal_ops.go`
- [ ] `ErrNoTenantLink` sentinel defined in `strategy.go`
- [ ] `mcpspec/strategy.go` rewritten: `RequiresLink() → false`, `StartLink`/`FinishLink` create tenant links, `Headers`/`Maintain` use tenant-scoped AAD
- [ ] `mcpspec` state envelope gains `is_tenant_link` flag to distinguish tenant vs user OAuth flows
- [ ] `DBAuthProvider.linkContext()` adds tenant-link path for `mcp_spec` (!RequiresLink) → loads `UpstreamTenantLink`
- [ ] `DBAuthProvider.loadTenantLink()` method: queries `upstream_tenant_links`, decrypts with tenant-only AAD
- [ ] `DBAuthProvider.Headers()` returns `AuthResult{LinkID: TenantLink.ID}` when `Link` is nil
- [ ] Refresher `sweep()`: add `dueTenantLinks()` and tenant-link maintain loop
- [ ] Refresher: `MaybeAutoDisableForRelink` also sweeps `upstream_tenant_links`
- [ ] Refresher `sweepCatalog()`: for `mcp_spec`, index using tenant link tokens directly (no user link required)
- [ ] `upstream.Service`: add tenant-level `Connect`/`Disconnect`/`Relink` methods
- [ ] `UserUpstreamSummary` gains `HasTenantLink bool` and `TenantLinkState LinkState`
- [ ] `applyStrategyMeta`: set `HasTenantLink = true` for `mcp_spec` upstreams
- [ ] `summariseUpstream`: load tenant link state for `mcp_spec`
- [ ] `protoview.ToSummaryProto`: propagate `HasTenantLink` and `TenantLinkState` to proto
- [ ] Proto: add `has_tenant_link` and `tenant_link_state` fields to `UpstreamSummary`; `buf generate`
- [ ] `authtransport.go`: handle `ErrNoTenantLink` parallel to `ErrLinkNotFound`; `LinkID != 0` covers tenant links for health recording
- [ ] `admin/upstreams.go`: `ReindexUpstreamCatalog` uses tenant link tokens for `mcp_spec`
- [ ] `web/admin McpServers.vue`: Connect/Relink/Enable buttons for `mcp_spec`; Rotate Key for static_header shared; Rotate Setup Key for BYOK
- [ ] `web/portal upstream-cta.ts`: `mcp_spec` rows show no CTA (admin-managed); BYOK rows unchanged
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
| `internal/upstream/mcpspec/strategy.go` | Rewritten for tenant-level OAuth |
| `internal/upstream/authprovider.go` | Tenant link resolution |
| `internal/upstream/refresher.go` | Tenant link sweep + direct catalog |
| `internal/upstream/service.go` | Tenant-level Connect/Disconnect/Relink |
| `internal/upstream/portal_ops.go` | HasTenantLink, TenantLinkState in summary |
| `internal/upstream/protoview/protoview.go` | Propagate new fields |
| `internal/gateway/authtransport.go` | Handle LinkID=0 for tenant links |
| `internal/admin/upstreams.go` | Tenant-link aware reindex |
| `proto/limen/portal/v1/portal.proto` | has_tenant_link, tenant_link_state, ClearUpstreamOverride (retain) |
| `web/admin/src/pages/McpServers.vue` | Add action buttons |
| `web/portal/src/lib/upstream-cta.ts` | Updated decision table |
| `docs/phases/phase-09l-tenant-level-credentials.md` | This file |
| `docs/phases/README.md` | Updated index |

## Verification

- `go mod tidy && go fmt ./... && go vet ./... && golangci-lint run ./... && go test -race ./...`
- `buf generate` — the only generated diff is `has_tenant_link` / `tenant_link_state` fields on `UpstreamSummary`.
- `cd web/portal && pnpm test && pnpm build` — portal CTA decision table tests cover: `mcp_spec` = no CTA (admin-managed), `none` = no CTA, `static_header` shared = Disable/Enable, `static_header` BYOK = Submit/Rotate/Enable.
- `cd web/admin && pnpm test && pnpm build` — admin SPA: new action buttons render correctly for each strategy/state combination.
- Manual smoke (dev compose): create `mcp_spec` upstream → admin Connect via popup → tokens stored in `upstream_tenant_links` → tenant members see tools without connecting → Relink replaces tokens. Create `static_header` BYOK upstream → user submits own key → portal shows Rotate → Rotate Setup Key in admin replaces shared secret.

## Out of scope

- An "Edit Upstream" SPA action for `mcp_spec` (changing MCP URL, scopes, or OAuth client overrides in place). v1 still recreates the upstream.
- Admin "Rotate Key" / "Rotate Setup Key" RPCs in proto — the admin SPA buttons are wired to stubs; the actual secret-rotation proto RPCs are a Phase 10 hardening task.
- Per-user `mcp_spec` links — this phase commits to tenant-only. If a future upstream requires per-user OAuth, a new strategy or sub-mode will be added.
- `upstream_tenant_links` preservation on the existing dev DBs — the migration is additive (new table), no wipe needed.
- Portal SPA changes beyond the CTA decision table — the portal already shows `mcp_spec` tools after the admin connects; this phase only removes the per-user Connect CTA.

## Risks

- **`RequiresLink=false` changes the gateway's LinkContext assumptions**: Currently `!RequiresLink` means "no link, no credentials." After this refactor, `mcp_spec` has `RequiresLink=false` but *does* need credentials (from the tenant link). The `DBAuthProvider.linkContext()` branching must be exact: `none` → skip, `mcp_spec` → load tenant link, `static_header` shared → use config. A mistake here means `none` accidentally tries to load a tenant link (nil → harmless but wasteful) or `mcp_spec` skips auth entirely (silent tool-call failures).
- **Dual AAD schemes**: User links use AAD `(tenant, user, kind)`, tenant links use AAD `(tenant, "", kind)`. A mismatch between encrypt and decrypt AAD makes tokens unreadable. The `UpstreamTenantLink` model must set AAD consistently with no user component, and `loadTenantLink` must decrypt with the matching AAD.
- **Refresher concurrency**: The refresher now sweeps two tables (`upstream_links` + `upstream_tenant_links`). Adding a second sweep doubles the refresh query load. For v1 deployments with a handful of upstreams this is negligible, but the `limit(500)` on both sweeps should be monitored if upstream count grows.
- **Admin SPA button complexity**: The action column now has 7 possible buttons (Connect, Relink, Edit, Reindex, Delete, Rotate Key, Rotate Setup Key) conditionally shown. The `v-if` logic must be well-tested so no button appears for the wrong strategy/state combination.
- **Nil `User` safety in `LinkContext`**: Tenant-level code paths set `User = nil` intentionally (no per-user context needed). Any code that accesses `LinkContext.User` must guard with a nil check first. The `StartConnect` handler validates `User != nil` for admin-initiated OAuth flows. Omitting this guard in shared utility code could cause nil-pointer panics that surface only in production with certain strategy combinations.
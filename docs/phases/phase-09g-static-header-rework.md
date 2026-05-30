---
phase: "9g"
title: "Static-header strategy rework (Tenant provided secret + BYOK)"
status: planned
progress: 0
depends_on: ["7", "8", "9b", "9c"]
updated: "2026-05-28"
---

# Phase 9g — Static-header strategy rework (Tenant provided secret + BYOK)

**Depends on**: Phase 7 (upstream strategy interface, `static_header` v1), Phase 8 (per-user injection + reactive 401 path), Phase 9b (portal SPA), Phase 9c (admin SPA, `McpServerNew.vue`)
**Unblocks**: nothing — sister to 9c/9f; finishes the v1 admin surface for the only currently-bimodal strategy.

## Goal

Collapse `static_header`'s two modes (`tenant` vs `user`) into a single shape:

1. **Tenant provided** — The admin supplies a secret key at setup. All users share it. Users CANNOT override, change, or enter their own key. No per-user API key mechanism exists in this mode.
2. **BYOK** (Bring Your Own Key) — Each user MUST supply their own API key. There is NO shared tenant key to fall back to. The admin key entered at setup is their personal key only.

Exactly one mode applies per upstream. **Tenant provided** uses a single admin-set key shared by everyone; no per-user override exists. **BYOK** requires every user to supply their own key; there is no tenant-level fallback.

## Why

The old model — `Mode: "tenant" | "user"` — forced a binary choice that didn't match real upstreams:

- "Tenant-only" upstreams (a single org-wide API key) never benefited from the per-user opt-out plumbing.
- "User-only" upstreams (everyone brings their own key) had **no working secret** for "Test Connection" or the indexer, so the admin couldn't even verify the URL was reachable before announcing the server to the tenant.
- The user-mode path conflated "I haven't linked yet" (a non-error pre-link state) with "my key broke" (a real failure), which made the portal CTA confusing.

The new model replaces the binary `tenant` / `user` toggle with two clear, independent modes: **Tenant provided** (one admin-managed key for everyone) and **BYOK** (every user brings their own, with no tenant fallback).

## Design

### Strategy config — single shape

`internal/upstream/statichdr.Config` becomes:

```go
type Config struct {
    HeaderName     string // e.g. "Authorization"
    HeaderTemplate string // contains "{value}", e.g. "Bearer {value}"
    SharedSecret   string // mandatory (setup key for catalog indexing; also user key in TenantOwner mode)
}
```

`Mode`, `ModeTenant`, `ModeUser`, `TenantSecret`, `AllowUserOverride` are **deleted**. No alias, no shim. Validation requires non-empty `SharedSecret`.

### Encryption at rest

Both secret fields ride the existing `crypto.SecretField` plumbing — no plaintext on disk, no new columns:

- `SharedSecret` is one field inside `Config`. `EncodeConfig` JSON-marshals the whole `Config` and hands it to `crypto.NewSecret(payload).SetAAD(tenantID, "", "upstream.strategy_config")`. The resulting `SecretField` lands in `UpstreamStrategyConfig.ConfigJSON`, which AES-SIV-encrypts on `Value()` and decrypts lazily via `Decrypt(tenant, "", kindStrategyConfig)`. This is exactly the pattern `mcpspec.Config.ClientSecret` already uses ([internal/upstream/mcpspec/config.go](../../internal/upstream/mcpspec/config.go)).
- Per-user BYOK secrets are wrapped in `userExtra{Secret string}`, JSON-marshalled, and bound to AAD `(tenant, user, "upstream.extra")` before being stored in `UpstreamLink.ExtraJSON` (also a `SecretField`). Decrypt happens inside `Headers()` only when a BYOK override is actually consulted.

No field on `Config` or `userExtra` is loggable in plaintext; the `String()` / `Bytes()` accessors only return data after a successful `Decrypt(...)` under the matching AAD.

### SubMode vocabulary

`(Strategy).SubMode` now reads the `Mode` column of `UpstreamStrategyConfig` and returns `"tenant_owner"` or `"byok"`. The proto field `UpstreamSummary.strategy_sub_mode` keeps its name; its value set changes from `{"tenant","user"}` → `{"tenant_owner","byok"}`. The UI-facing labels come from the `staticHeaderModeLabel()` helper (see [Deliverable note](#helper-staticheadermodelabel) below).

This is a hard cutover — the portal SPA's decision table reads the new values directly. No backfill.

#### Helper: `staticHeaderModeLabel()`

A new frontend helper maps internal `strategy_sub_mode` strings to user-facing labels:

| Internal value | User-facing label |
| --- | --- |
| `"tenant_owner"`    | `Tenant provided` |
| `"byok"`  | `BYOK`            |

Consumed by:
- `UpstreamCard.vue` subtitle rendering
- `upstream-cta.ts` decision table comments
- Any SPA page that displays the active mode to users

This helper centralises the mapping so the internal vocabulary (`"tenant_owner"` / `"byok"`) never leaks to the UI.

### Headers resolution order

`(Strategy).Headers(ctx, lctx)` resolves based on mode:

1. **TenantOwner mode**: Always use `cfg.SharedSecret` — no per-user override path.
2. **BYOK mode**:
   - No user context (catalog/indexer) → use `SharedSecret` (setup key).
   - User with valid BYOK key in `Link.ExtraJSON` and `!Link.NeedsRelink` → decrypt and use user's key.
   - User with `Link.NeedsRelink` → return `ErrNeedsRelink`.
   - User without a key → return `ErrNoCredentials`.

Key property: **In TenantOwner mode, `Headers` never returns `ErrNeedsRelink`.** In BYOK mode, `ErrNeedsRelink` signals that the user's key must be rotated.

### `RequiresLink`

Stays `true` (for opt-out flag carrier in both modes). The link row carries:
- `Enabled` — per-user opt-out, available in both modes.
- `ExtraJSON` — per-user BYOK secret (only consulted in BYOK mode).

In TenantOwner mode, a missing link is fine — `Headers` always uses `SharedSecret`. In BYOK mode, a missing link (or missing key) returns `ErrNoCredentials`.

### New / changed RPCs

- `PortalService.SubmitUpstreamAPIKey` — unchanged shape; now rejects with `FailedPrecondition` when mode is not BYOK (i.e., TenantOwner mode). The SPA hides the button in that case but server-side rejection is mandatory.
- `PortalService.ClearUpstreamOverride` — **new** RPC. Clears `ExtraJSON` and resets the link's health counters. **Note:** in BYOK mode there is no tenant key to fall back to, so clearing the override makes the upstream permanently unavailable for that user until they submit a new key. The RPC remains in the proto for potential future use.
- `AdminService.CreateUpstream` — unchanged proto shape. The `strategy_config` map now carries `{header_name, header_template, value}` (no `mode` or `allow_user_override`). The handler picks the static_header encoding branch (see [Bug fix](#bug-fix-static_header-config-was-being-dropped) below).
- `AdminService.CreateUpstream`'s `strategy_sub_mode` field becomes **used for `static_header`** — it carries `"tenant_owner"` or `"byok"`. This is the canonical store of mode for `static_header`, replacing the old config-internal bool.
- `UpstreamSummary` gets a new field: `bool has_user_override = 13;`. Set true when `link != nil && !link.ExtraJSON.IsZero()`. The SPA uses this to decide between "Rotate" and "Submit" CTAs.

### Bug fix: `static_header` config was being dropped

A latent bug surfaced during exploration: `internal/admin/upstreams.go.CreateUpstream` only encodes the strategy config row for `mcp_spec` (via `OAuthClientOverride`). The `strategy_config` map sent by the admin SPA is assigned to `in.StrategyConfig` but never consumed by `upstream.Service.CreateUpstream` — which means **today, creating a `static_header` upstream silently persists no config row**. This rework wires it up correctly:

```go
// internal/admin/upstreams.go (CreateUpstream handler, sketch)
switch in.StrategyType {
case upstream.StrategyMCPSpec:
    // existing OAuthClientOverride path
case upstream.StrategyStaticHeader:
    cfg := statichdr.Config{
        HeaderName:     msg.GetStrategyConfig()["header_name"],
        HeaderTemplate: msg.GetStrategyConfig()["header_template"],
        SharedSecret:   msg.GetStrategyConfig()["value"],
    }
    in.HeaderMode = msg.GetStrategySubMode()
    if in.HeaderMode == "" { in.HeaderMode = "tenant_owner" }
    sf, err := statichdr.EncodeConfig(tenant.ID, cfg)
    // ... → in.EncodedStrategyConfig = sf
}
```

`upstream.Service.CreateUpstream` stays strategy-agnostic; the dispatch lives in the admin handler next to its mcpspec sibling.

### Admin SPA (`web/admin/src/pages/McpServerNew.vue`)

Replace the segmented "Tenant secret / User-supplied" control with a mode selector:

- A **required** "Header value (secret)" input (always visible).
- A **mode radio/select**: **TenantOwner** (one key for all users) or **BYOK** (each user brings own key).

`buildStrategyConfig()` emits the mode via `strategy_sub_mode` field instead of an internal bool:

```ts
// strategy_config map (no mode or allow_user_override):
{
  header_name: form.headerName,
  header_template: form.headerTemplate,
  value: form.apiKey, // SharedSecret, always required
}
// strategy_sub_mode: form.mode === 'byok' ? 'byok' : 'tenant_owner'
```

### Portal SPA — CTA decision table

`web/portal/src/lib/upstream-cta.ts` is rewritten. The relevant rows for `static_header` become (internal `strategy_sub_mode` values shown; user-facing labels from `staticHeaderModeLabel()` noted in parentheses):

| `strategy_sub_mode` | `link_state`   | `has_user_override` | Primary CTA      | Secondary  |
| ------------------- | -------------- | ------------------- | ---------------- | ---------- |
| `tenant_owner` (Tenant provided) | any            | n/a                 | Disable / Enable | —          |
| `byok` (BYOK)     | `none`         | false               | Submit API key   | Skip       |
| `byok` (BYOK)     | `connected`    | true                | Rotate           | —          |
| `byok` (BYOK)     | `needs_relink` | true                | Rotate           | —          |
| `byok` (BYOK)     | `disabled`     | any                 | Enable           | —          |

In Tenant provided mode, tools always work via the admin's shared key. In BYOK mode, tools become available once the user submits their own key.

`UpstreamCard.vue`'s subtitle (`strategyType.subMode`) uses `staticHeaderModeLabel()` to map the internal `"tenant_owner"` / `"byok"` values to user-facing labels `"Tenant provided"` / `"BYOK"`. The rendering logic itself is unchanged.

### Database migration — `00012_statichdr_rework.sql`

The strategy config payload lives encrypted in `upstream_strategy_configs.config_json` (AES-SIV blob), so we cannot rewrite it in SQL. The migration is a **hard data wipe** for `static_header` rows:

```sql
DELETE FROM upstream_strategy_configs WHERE type = 'static_header';
DELETE FROM upstream_links            WHERE upstream_id IN
    (SELECT id FROM upstreams WHERE strategy_type = 'static_header');
DELETE FROM upstreams                 WHERE strategy_type = 'static_header';
```

`Down` is intentionally `SELECT 1;` — the deleted blobs cannot be reconstructed in SQL. This is acceptable per [AGENTS.md](../../AGENTS.md) ("breaking changes accepted and expected"); no production data exists. Admins recreate the upstream via the SPA — same pattern as `00011`.

## Files

### Backend (Go)

- `internal/upstream/statichdr/statichdr.go` — rewritten. Mode stored as separate `UpstreamStrategyConfig.Mode` column (`"tenant_owner"` / `"byok"`); `Config` struct has no `AllowUserOverride`. New `SubMode` strings, new `Headers` resolution order, new `ClearUserOverride` method, `Mode`/`ModeTenant`/`ModeUser` deleted.
- `internal/upstream/statichdr/statichdr_test.go` — rewritten. Cases: Tenant-provided-only, BYOK-no-user, BYOK-with-secret (user wins), BYOK-with-NeedsRelink (tenant-provided wins), `PersistUserSecret` round-trip, `ClearUserOverride` round-trip, `EncodeConfig` validation.
- `internal/upstream/portal_ops.go` — `UserUpstreamSummary` gains `HasUserOverride bool`; `summariseUpstream` populates it; new `ClearUserStaticHeaderOverride(ctx, tenant, user, identifier) error` wrapper that resolves the strategy and calls `ClearUserOverride`. New `secretClearer` interface mirrors `secretPersister`.
- `internal/upstream/protoview/protoview.go` — `ToSummaryProto` propagates `HasUserOverride → out.HasUserOverride`.
- `internal/admin/upstreams.go` — `CreateUpstream` handler grows the `static_header` encoding branch described above; mapProvisionError comment refreshed.
- `internal/portal/upstreams.go` — new `ClearUpstreamOverride` RPC handler calls `s.upstream.ClearUserStaticHeaderOverride`.
- `internal/storage/migrations/postgres/00012_statichdr_rework.sql` — new.

### Proto

- `proto/limen/portal/v1/portal.proto`:
  - `UpstreamSummary`: add `bool has_user_override = 13;`. Update `strategy_sub_mode` comment to document the new value set.
  - Add `rpc ClearUpstreamOverride(ClearUpstreamOverrideRequest) returns (ClearUpstreamOverrideResponse);` to `PortalService`.
  - Add `ClearUpstreamOverrideRequest{ string upstream_identifier = 1; }` and empty `ClearUpstreamOverrideResponse{}`.
- `proto/limen/admin/v1/admin.proto`: `CreateUpstreamRequest.strategy_sub_mode` comment updated — "used for `static_header` (values `"tenant_owner"` / `"byok"`)"; field number unchanged.

### Frontend

- `web/admin/src/pages/McpServerNew.vue` — segmented sub-mode control → mode selector (radio/select: TenantOwner ↔ BYOK); secret field always visible & required; `buildStrategyConfig` emits config-only map with mode via `strategy_sub_mode`.
- `web/admin/src/pages/McpServerNew.test.ts` (if present) — assertions updated.
- `web/portal/src/lib/static-header-mode-label.ts` — **new**. `staticHeaderModeLabel(mode: string)` helper mapping internal values (`"tenant_owner"`, `"byok"`) to user-facing labels (`Tenant provided`, `BYOK`).
- `web/portal/src/lib/upstream-cta.ts` — rewritten decision table.
- `web/portal/src/lib/upstream-cta.spec.ts` — coverage for the new table rows.
- `web/portal/src/components/UpstreamCard.vue` — uses `staticHeaderModeLabel()` for subtitle rendering; verify labels display as `Tenant provided` / `BYOK`.
- `web/portal/src/pages/Upstreams.vue` — note: the Clear CTA was removed. Only Submit/Rotate/Enable/Disable CTAs remain.

## Verification

- `go mod tidy && go fmt ./... && go vet ./... && golangci-lint run ./... && go test -race ./...`
- `buf generate` — the only generated diff is the new `has_user_override` field, the renamed `strategy_sub_mode` doc comment, and the new `ClearUpstreamOverride` RPC.
- Integration test in `statichdr_test.go` exercises a real Postgres testcontainer through both Tenant provided and BYOK modes.
- `cd web/portal && pnpm test && pnpm build`; `cd web/admin && pnpm test && pnpm build`.
- Manual smoke (dev compose): create a `static_header` upstream in TenantOwner mode → catalog populates immediately, no portal CTA. Switch to BYOK mode → portal shows "Submit API key" CTA, tools unavailable until user submits their key.

## Out of scope

- An admin "Edit Upstream" SPA action for `static_header` (rotating the shared secret or changing the mode in place). v1 still recreates the upstream — the proto already has `AdminService.UpdateUpstream` but it currently only patches `display_name` / `mcp_url`. Lifting that into a strategy-config patch is a Phase 10 hardening task.
- `static_header` row preservation on the existing dev DBs. We wipe and recreate; that's the whole point of `00012`.

## Per-phase checklist

- [ ] `statichdr.Config` rewritten; `AllowUserOverride` removed; `Mode`/`ModeTenant`/`ModeUser`/`TenantSecret` deleted, no shim
- [ ] `Mode` stored as separate `UpstreamStrategyConfig.Mode` column (`"tenant_owner"` / `"byok"`), not in encrypted Config
- [ ] `SubMode` returns `"tenant_owner"` (Tenant provided) / `"byok"` (BYOK)
- [ ] `Headers` in TenantOwner mode always uses `SharedSecret`; no per-user override path
- [ ] `Headers` in BYOK mode: user BYOK key → `ErrNeedsRelink` → `ErrNoCredentials`; no `SharedSecret` fallback for users
- [ ] `Headers` never returns `ErrNeedsRelink` in TenantOwner mode
- [ ] `PersistUserSecret` rejects when mode is not BYOK (returns `ErrUnsupported`)
- [ ] `ClearUserOverride` zeroes `ExtraJSON` and resets health counters (RPC exists; in BYOK mode no tenant fallback)
- [ ] `upstream.Service` wraps `ClearUserStaticHeaderOverride`; `secretClearer` interface added
- [ ] `UserUpstreamSummary.HasUserOverride` surfaced through `protoview`
- [ ] `internal/admin/upstreams.go.CreateUpstream` reads `strategy_sub_mode` for mode, encodes `static_header` config from the form map (fixes the latent dropped-config bug)
- [ ] `internal/portal/upstreams.go` exposes `ClearUpstreamOverride` RPC
- [ ] `00012_statichdr_rework.sql` deletes existing `static_header` rows; `Down` is a no-op
- [ ] `portal.proto`: `has_user_override` field added; `ClearUpstreamOverride` RPC added; codegen regenerated
- [ ] `admin.proto`: `strategy_sub_mode` comment updated (now USED for static_header); codegen regenerated
- [ ] `web/admin McpServerNew.vue` uses mode selector (radio/select) + always-visible secret field; emits mode via `strategy_sub_mode`
- [ ] `web/portal upstream-cta.ts` decision table rewritten; covered by tests
- [ ] `web/portal Upstreams.vue` wires Submit/Rotate/Enable/Disable CTAs (Clear CTA removed — see note above)
- [ ] `go test -race ./...` green; `pnpm test && pnpm build` green for both SPAs
- [ ] Manual smoke (TenantOwner + BYOK) on dev compose

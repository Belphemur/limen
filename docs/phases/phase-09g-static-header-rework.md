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
    HeaderName        string // e.g. "Authorization"
    HeaderTemplate    string // contains "{value}", e.g. "Bearer {value}"
    SharedSecret      string // mandatory
    AllowUserOverride bool   // opt-in
}
```

`Mode`, `ModeTenant`, `ModeUser`, `TenantSecret` are **deleted**. No alias, no shim. Validation requires non-empty `SharedSecret` regardless of `AllowUserOverride`.

### Encryption at rest

Both secret fields ride the existing `crypto.SecretField` plumbing — no plaintext on disk, no new columns:

- `SharedSecret` is one field inside `Config`. `EncodeConfig` JSON-marshals the whole `Config` and hands it to `crypto.NewSecret(payload).SetAAD(tenantID, "", "upstream.strategy_config")`. The resulting `SecretField` lands in `UpstreamStrategyConfig.ConfigJSON`, which AES-SIV-encrypts on `Value()` and decrypts lazily via `Decrypt(tenant, "", kindStrategyConfig)`. This is exactly the pattern `mcpspec.Config.ClientSecret` already uses ([internal/upstream/mcpspec/config.go](../../internal/upstream/mcpspec/config.go)).
- Per-user BYOK secrets are wrapped in `userExtra{Secret string}`, JSON-marshalled, and bound to AAD `(tenant, user, "upstream.extra")` before being stored in `UpstreamLink.ExtraJSON` (also a `SecretField`). Decrypt happens inside `Headers()` only when a BYOK override is actually consulted.

No field on `Config` or `userExtra` is loggable in plaintext; the `String()` / `Bytes()` accessors only return data after a successful `Decrypt(...)` under the matching AAD.

### SubMode vocabulary

`(Strategy).SubMode` now returns `"shared"` (Tenant provided mode) or `"override"` (BYOK mode). The proto field `UpstreamSummary.strategy_sub_mode` keeps its name; its value set changes from `{"tenant","user"}` → `{"shared","override"}`. The UI-facing labels come from the `staticHeaderModeLabel()` helper (see [Deliverable note](#helper-staticheadermodelabel) below).

This is a hard cutover — the portal SPA's decision table reads the new values directly. No backfill.

#### Helper: `staticHeaderModeLabel()`

A new frontend helper maps internal `strategy_sub_mode` strings to user-facing labels:

| Internal value | User-facing label |
| --- | --- |
| `"shared"`    | `Tenant provided` |
| `"override"`  | `BYOK`            |

Consumed by:
- `UpstreamCard.vue` subtitle rendering
- `upstream-cta.ts` decision table comments
- Any SPA page that displays the active mode to users

This helper centralises the mapping so the internal vocabulary (`"shared"` / `"override"`) never leaks to the UI.

### Headers resolution order

`(Strategy).Headers(ctx, lctx)` always returns a populated header map:

1. If `AllowUserOverride && lctx.Link != nil && !lctx.Link.ExtraJSON.IsZero() && !lctx.Link.NeedsRelink` → decrypt `ExtraJSON` with AAD `tenant|user|"upstream.extra"`, substitute the user's secret.
2. Otherwise → substitute `cfg.SharedSecret`.

Critical property: **`Headers` never returns `ErrNeedsRelink`**. A user whose BYOK key starts failing has their `Link.NeedsRelink` flipped by the Phase 8 reactive-401 path, which short-circuits step 1 and falls back to step 2. Their tools keep working on the tenant-provided key while the portal nudges them to fix their BYOK key.

### `RequiresLink`

Stays `true`. The link row carries two pieces of state:

- `Enabled` — per-user opt-out, available in both Tenant provided and BYOK modes ("hide GitHub's tools from my session").
- `ExtraJSON` — per-user BYOK secret (only consulted when `AllowUserOverride`).

A missing link is fine; `Headers` falls back to the tenant-provided key cleanly.

### New / changed RPCs

- `PortalService.SubmitUpstreamAPIKey` — unchanged shape; now rejects with `FailedPrecondition` when `AllowUserOverride=false` (i.e., Tenant provided mode). The SPA hides the button in that case but server-side rejection is mandatory.
- `PortalService.ClearUpstreamOverride` — **new** RPC. Clears `ExtraJSON` and resets the link's health counters so the next request falls back to the tenant-provided key. Replaces the implicit "rotate by submitting empty" path. **Note:** the `clearOverride` CTA was later removed from the portal UI entirely, because neither Tenant provided mode nor BYOK mode supports "reverting to a tenant key" — in BYOK mode there is no tenant key to fall back to, and in Tenant provided mode there is no per-user override to clear. The RPC itself remains in the proto for potential future use.
- `AdminService.CreateUpstream` — unchanged proto shape. The `strategy_config` map now carries `{header_name, header_template, value, allow_user_override}` instead of `{header_name, header_template, mode, value}`. The handler picks the static_header encoding branch (see [Bug fix](#bug-fix-static_header-config-was-being-dropped) below).
- `AdminService.CreateUpstream`'s `strategy_sub_mode` field becomes **unused for `static_header`** — the BYOK flag (`allow_user_override`) travels inside `strategy_config`. Comment is updated; the field stays on the message because `mcp_spec` may grow sub-modes later (DCR vs static client).
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
        HeaderName:        msg.GetStrategyConfig()["header_name"],
        HeaderTemplate:    msg.GetStrategyConfig()["header_template"],
        SharedSecret:      msg.GetStrategyConfig()["value"],
        AllowUserOverride: msg.GetStrategyConfig()["allow_user_override"] == "true",
    }
    sf, err := statichdr.EncodeConfig(tenant.ID, cfg)
    // ... → in.EncodedStrategyConfig = sf
}
```

`upstream.Service.CreateUpstream` stays strategy-agnostic; the dispatch lives in the admin handler next to its mcpspec sibling.

### Admin SPA (`web/admin/src/pages/McpServerNew.vue`)

Replace the segmented "Tenant secret / User-supplied" control with:

- A **required** "Header value (secret)" input (always visible).
- A checkbox: **"Allow users to bring their own key (BYOK)"**.

`buildStrategyConfig()` now emits:

```ts
{
  header_name: form.headerName,
  header_template: form.headerTemplate,
  value: form.apiKey, // shared secret, always required
  allow_user_override: form.allowUserOverride ? 'true' : 'false',
}
```

### Portal SPA — CTA decision table

`web/portal/src/lib/upstream-cta.ts` is rewritten. The relevant rows for `static_header` become (internal `strategy_sub_mode` values shown; user-facing labels from `staticHeaderModeLabel()` noted in parentheses):

| `strategy_sub_mode` | `link_state`   | `has_user_override` | Primary CTA      | Secondary  |
| ------------------- | -------------- | ------------------- | ---------------- | ---------- |
| `shared` (Tenant provided) | any            | n/a                 | Disable / Enable | —          |
| `override` (BYOK)     | `none`         | false               | Submit API key   | Skip       |
| `override` (BYOK)     | `connected`    | true                | Rotate           | —          |
| `override` (BYOK)     | `needs_relink` | true                | Rotate           | —          |
| `override` (BYOK)     | `disabled`     | any                 | Enable           | —          |

In Tenant provided mode, tools always work via the admin's shared key. In BYOK mode, tools become available once the user submits their own key.

`UpstreamCard.vue`'s subtitle (`strategyType.subMode`) uses `staticHeaderModeLabel()` to map the internal `"shared"` / `"override"` values to user-facing labels `"Tenant provided"` / `"BYOK"`. The rendering logic itself is unchanged.

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

- `internal/upstream/statichdr/statichdr.go` — rewritten. New `Config` shape, new `SubMode` strings, new `Headers` resolution order, new `ClearUserOverride` method, `Mode`/`ModeTenant`/`ModeUser` deleted.
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
- `proto/limen/admin/v1/admin.proto`: `CreateUpstreamRequest.strategy_sub_mode` comment updated — "unused for `static_header` (encoded inside `strategy_config['allow_user_override']`)". Field number unchanged.

### Frontend

- `web/admin/src/pages/McpServerNew.vue` — segmented sub-mode control → checkbox; secret field always visible & required; `buildStrategyConfig` emits the new keys.
- `web/admin/src/pages/McpServerNew.test.ts` (if present) — assertions updated.
- `web/portal/src/lib/static-header-mode-label.ts` — **new**. `staticHeaderModeLabel(mode: string)` helper mapping internal values (`"shared"`, `"override"`) to user-facing labels (`Tenant provided`, `BYOK`).
- `web/portal/src/lib/upstream-cta.ts` — rewritten decision table.
- `web/portal/src/lib/upstream-cta.spec.ts` — coverage for the new table rows.
- `web/portal/src/components/UpstreamCard.vue` — uses `staticHeaderModeLabel()` for subtitle rendering; verify labels display as `Tenant provided` / `BYOK`.
- `web/portal/src/pages/Upstreams.vue` — note: the Clear CTA was removed. Only Submit/Rotate/Enable/Disable CTAs remain.

## Verification

- `go mod tidy && go fmt ./... && go vet ./... && golangci-lint run ./... && go test -race ./...`
- `buf generate` — the only generated diff is the new `has_user_override` field, the renamed `strategy_sub_mode` doc comment, and the new `ClearUpstreamOverride` RPC.
- Integration test in `statichdr_test.go` exercises a real Postgres testcontainer through both Tenant provided and BYOK modes.
- `cd web/portal && pnpm test && pnpm build`; `cd web/admin && pnpm test && pnpm build`.
- Manual smoke (dev compose): create a `static_header` upstream in Tenant provided mode → catalog populates immediately, no portal CTA. Switch to BYOK mode → portal shows "Submit API key" CTA, tools unavailable until user submits their key.

## Out of scope

- An admin "Edit Upstream" SPA action for `static_header` (rotating the shared secret or flipping `AllowUserOverride` in place). v1 still recreates the upstream — the proto already has `AdminService.UpdateUpstream` but it currently only patches `display_name` / `mcp_url`. Lifting that into a strategy-config patch is a Phase 10 hardening task.
- `static_header` row preservation on the existing dev DBs. We wipe and recreate; that's the whole point of `00012`.

## Per-phase checklist

- [ ] `statichdr.Config` rewritten; `Mode`/`ModeTenant`/`ModeUser`/`TenantSecret` deleted, no shim
- [ ] `SubMode` returns `"shared"` (Tenant provided) / `"override"` (BYOK)
- [ ] `Headers` falls back to `SharedSecret` when BYOK override absent / link `NeedsRelink`
- [ ] `Headers` never returns `ErrNeedsRelink`
- [ ] `PersistUserSecret` rejects when `AllowUserOverride=false` (returns `ErrUnsupported`)
- [ ] `ClearUserOverride` zeroes `ExtraJSON` and resets health counters (RPC exists; Clear CTA removed from UI since neither mode supports reverting to a tenant key)
- [ ] `upstream.Service` wraps `ClearUserStaticHeaderOverride`; `secretClearer` interface added
- [ ] `UserUpstreamSummary.HasUserOverride` surfaced through `protoview`
- [ ] `internal/admin/upstreams.go.CreateUpstream` encodes `static_header` config from the form map (fixes the latent dropped-config bug)
- [ ] `internal/portal/upstreams.go` exposes `ClearUpstreamOverride` RPC
- [ ] `00012_statichdr_rework.sql` deletes existing `static_header` rows; `Down` is a no-op
- [ ] `portal.proto`: `has_user_override` field added; `ClearUpstreamOverride` RPC added; codegen regenerated
- [ ] `admin.proto`: `strategy_sub_mode` comment updated; codegen regenerated
- [ ] `web/admin McpServerNew.vue` uses checkbox + always-visible secret field; emits new map keys
- [ ] `web/portal upstream-cta.ts` decision table rewritten; covered by tests
- [ ] `web/portal Upstreams.vue` wires Submit/Rotate/Enable/Disable CTAs (Clear CTA removed — see note above)
- [ ] `go test -race ./...` green; `pnpm test && pnpm build` green for both SPAs
- [ ] Manual smoke (Tenant provided + BYOK + BYOK-with-bad-key fallback) on dev compose

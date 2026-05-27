---
phase: "9f"
title: "IDE presets & per-tenant redirect-URI allowlist"
status: planned
progress: 0
depends_on: ["5", "9c"]
updated: "2026-04-25"
---

# Phase 9f — IDE presets & per-tenant redirect-URI allowlist as a relation

**Depends on**: Phase 5 (redirect-URI floor + glob matcher), Phase 9c (tenant admin SPA + Settings + Dashboard onboarding, current `dcr_redirect_uri_allowlist` JSONB on `tenant_settings`)
**Unblocks**: nothing (sister to 9c — together they finish the v1 administrative surface)

## Goal

Turn the flat `string[]` glob list that Phase 9c parked on `tenant_settings.dcr_redirect_uri_allowlist` into a proper relation, and use that relation to drive a **"Choose your IDE"** onboarding step.

Two outcomes:

1. The redirect-URI allowlist is stored as one row per `(tenant, pattern)`, tagged with an **IDE preset key** (e.g. `cursor`, `vscode`, `claude_code`) or `NULL` for free-form admin entries. The matcher's input is still a deduplicated set of compiled globs — the table shape changes, the runtime semantics do not.
2. The set of "well-known IDE presets" is a global, seeded table (`ide_presets`) that the SPA reads to render the **Common IDEs** quick-setup grid on both the Settings page and the new **Choose Your IDE** onboarding card.

The current `dcr_redirect_uri_allowlist` JSONB column on `tenant_settings` is **deleted** in this phase — there is no compatibility shim, no migration of existing rows. We have no external users yet, so a one-commit cutover is the cheaper move ([`AGENTS.md`](../../AGENTS.md) — "No backwards-compatibility tax").

## Why a new table (and not "just" a column rename)

A `JSONB string[]` was fine while every entry was anonymous. With the IDE-preset feature, every entry now has structure:

- **Source**: did this pattern come from an IDE preset (`cursor`), or is it a one-off admin entry (`NULL`)?
- **Label**: what does the admin see in the table — "Cursor Editor" vs. their typed-in "Local Dev Portal"?
- **Lifecycle**: bulk-add-by-preset and bulk-remove-by-preset are first-class operations; that is awkward over `JSONB array_remove`.

Modelling this in JSON forces every read site to re-parse + branch on `null`. A relation is cheaper, lets RLS protect entries directly, and gives us a stable primary key the SPA can address for delete/edit without re-sending the whole array.

It also separates **what the preset declares** (read-only, shipped with Limen) from **what the tenant chose** (mutable, per-tenant). A tenant who later removes a single Cursor pattern shouldn't lose the rest; the relation makes that obvious.

## Design

### `ide_presets` — global, seeded, GORM-owned

```
ide_presets
├── key           text PRIMARY KEY          -- "cursor", "vscode", "claude_code", ...
├── display_name  text NOT NULL             -- "Cursor", "VS Code", "Claude Code", ...
├── icon          text NOT NULL             -- Lucide icon name ("terminal", "code", ...)
├── sort_order    integer NOT NULL DEFAULT 100
├── created_at    timestamptz NOT NULL DEFAULT now()
└── updated_at    timestamptz NOT NULL DEFAULT now()

ide_preset_patterns
├── id            bigserial PRIMARY KEY
├── ide_key       text NOT NULL REFERENCES ide_presets(key) ON DELETE CASCADE
├── pattern       text NOT NULL             -- already passes oauthproxy.CompilePattern
├── sort_order    integer NOT NULL DEFAULT 0
└── UNIQUE (ide_key, pattern)
```

Both tables are **global** (not tenant-scoped), so RLS is intentionally not enabled — every tenant can read every preset. They are seeded by `00009_ide_presets.sql` ([Seed data](#seed-data) below) and **owned at runtime by GORM `AutoMigrate`** for DDL, matching the Phase 1 / 9c convention: SQL migrations do RLS + grants + seed rows; GORM creates and evolves columns.

Presets are versioned by content: a future `00010_ide_presets_add_kiro.sql` does `INSERT ... ON CONFLICT (key) DO UPDATE SET display_name = ...` — never a destructive replace. We never `DELETE FROM ide_presets` from a migration; deprecated IDEs get `sort_order = 999` and a `deprecated_at` timestamp (added column at that point — not in v1).

### `tenant_redirect_uri_allowlist` — relational, tenant-scoped, RLS-forced

```
tenant_redirect_uri_allowlist
├── id            bigserial PRIMARY KEY
├── public_id     text NOT NULL UNIQUE      -- "ral_<ULID>", internal/ids.PrefixAllowlistEntry
├── tenant_id     bigint NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
├── ide_key       text REFERENCES ide_presets(key) ON DELETE SET NULL  -- NULL for custom entries
├── label         text NOT NULL             -- "Cursor Editor" (from preset) or admin-typed "Local Dev Portal"
├── pattern       text NOT NULL             -- raw glob; compiled lazily at request time
├── created_at    timestamptz NOT NULL DEFAULT now()
├── updated_at    timestamptz NOT NULL DEFAULT now()
└── UNIQUE (tenant_id, pattern)             -- dedup at the DB level, not the handler
```

RLS policy `tenant_isolation` keyed on `current_setting('app.current_tenant', true)::bigint`, mirroring [`00008_tenant_settings.sql`](../../internal/storage/migrations/postgres/00008_tenant_settings.sql). Migration `00010_tenant_redirect_uri_allowlist.sql` owns the RLS + grants only; GORM owns the DDL.

`label` is **denormalized on purpose**. When a preset is added, the SPA writes the preset's `display_name` into the row's `label`; if the operator later renames the preset (or the row's `ide_key` is cleared because the preset was removed in some future migration), the row still has a human-readable label without a JOIN. Custom entries set `label` from the admin's input.

`ide_key` is **nullable** (`ON DELETE SET NULL`) so a removed preset doesn't destroy the tenant's custom-labelled rows — it just orphans them into the "Custom Redirect URIs" table on the Settings page. This is the only design point where we trade a JOIN for resilience; the read path is `SELECT … FROM tenant_redirect_uri_allowlist WHERE tenant_id = …` without a JOIN, so it costs nothing.

### Migration plan (single commit, no shim)

`00010_tenant_redirect_uri_allowlist.sql` (RLS + grants for the new table) + a Go migration step in `internal/storage` that:

1. Runs **before** the GORM `AutoMigrate` for the new model so the table exists.
2. Drops the `dcr_redirect_uri_allowlist` column from `tenant_settings` (raw SQL — no data preservation; the JSONB column is being replaced, not migrated).
3. Re-runs the seed via a Go bootstrap that calls a shared `internal/idepresets.Seed(db)` helper — same code path used by `00009_ide_presets.sql` for first install; safe to re-run.

We do **not** copy existing JSONB allowlist values into the new table. Per [`AGENTS.md`](../../AGENTS.md), Limen has no production users — breaking the storage shape in one commit is explicitly the policy. Devs running an older checkout will rebuild from `make dev-reset`.

### `oauthproxy.PatternSet` integration

`internal/oauthproxy/dcr.go` currently reads `tenant.DCRRedirectURIAllowlist` (a `[]string`) and calls `CompilePatternSet(raws)`. After this phase:

- `internal/storage.Tenant` loses the `DCRRedirectURIAllowlist` field.
- A new `internal/tenant.Service.ListAllowlistPatterns(ctx) ([]string, error)` returns the deduplicated raw patterns for the current tenant (RLS scopes it automatically). The DCR handler calls this once per request, exactly as it did the field read.
- `CompilePatternSet`, `CompilePattern`, `Match`, `CheckRedirectURI` are **untouched** — the matcher is the matcher. The shape of the input is what changed.

The shared `oauthproxy.ValidateRedirectURIPattern` is the only validator any caller is allowed to use; the client-side mirror in `web/shared/src/lib/redirectURI.ts` already exists and stays.

### Proto changes — `limen.admin.v1.AdminService`

```proto
// Read-only, available to owner + admin. Returns the full preset catalog
// the SPA needs to render the quick-setup grid.
rpc ListIDEPresets(ListIDEPresetsRequest) returns (ListIDEPresetsResponse);

// Replaces the dcr_redirect_uri_allowlist / dcr_redirect_uri_allowlist_set
// fields on UpdateTenantSettingsRequest, which are deleted.
rpc ListAllowlistEntries(ListAllowlistEntriesRequest) returns (ListAllowlistEntriesResponse);
rpc AddAllowlistEntry(AddAllowlistEntryRequest) returns (AddAllowlistEntryResponse);
rpc UpdateAllowlistEntry(UpdateAllowlistEntryRequest) returns (UpdateAllowlistEntryResponse);
rpc RemoveAllowlistEntry(RemoveAllowlistEntryRequest) returns (RemoveAllowlistEntryResponse);

// Bulk operations keyed on ide_key. ApplyIDEPreset inserts every pattern
// the preset declares that the tenant doesn't already have; rows already
// linked to that ide_key are left alone (idempotent re-add).
rpc ApplyIDEPreset(ApplyIDEPresetRequest) returns (ApplyIDEPresetResponse);
// RemoveIDEPreset deletes every row whose ide_key matches; custom rows
// (ide_key IS NULL) are untouched.
rpc RemoveIDEPreset(RemoveIDEPresetRequest) returns (RemoveIDEPresetResponse);
```

Messages:

```proto
message IDEPreset {
  string key          = 1;   // "cursor"
  string display_name = 2;   // "Cursor"
  string icon         = 3;   // "terminal" (Lucide)
  repeated string patterns = 4;
  int32 sort_order    = 5;
}

message AllowlistEntry {
  string public_id   = 1;    // "ral_<ULID>"
  string ide_key     = 2;    // "" when custom
  string label       = 3;
  string pattern     = 4;
  string created_at  = 5;    // RFC3339
}

message AddAllowlistEntryRequest {
  string ide_key = 1;        // "" for custom
  string label   = 2;
  string pattern = 3;
}
message UpdateAllowlistEntryRequest {
  string public_id = 1;
  string label     = 2;
  string pattern   = 3;
}
message RemoveAllowlistEntryRequest { string public_id = 1; }
message ApplyIDEPresetRequest       { string ide_key = 1; }
message RemoveIDEPresetRequest      { string ide_key = 1; }
```

`UpdateTenantSettingsRequest` loses `dcr_redirect_uri_allowlist` and
`dcr_redirect_uri_allowlist_set`. `GetTenantSettingsResponse` loses
`dcr_redirect_uri_allowlist`. The Settings page composes the allowlist from
`ListAllowlistEntries` instead. **Single-source-of-truth, no overlap.**

### Onboarding step — "Choose Your IDE"

A fourth task tile is added to `Dashboard.vue` (`N=4` in the Setup Progress card):

| Tile                   | Completion rule                                                                                                      |
| ---------------------- | -------------------------------------------------------------------------------------------------------------------- |
| Connect MCP Servers    | unchanged from 9c — at least one `ListUpstreams` row with `status == ready`                                          |
| **Choose Your IDE**    | `tenant_settings.chose_ide_at IS NOT NULL` — set the first time the user applies a preset or saves the "skip" choice |
| Invite Your Team       | unchanged from 9c — `tenant_settings.invited_team_at IS NOT NULL`                                                    |
| Configure Organization | unchanged from 9c — `tenant_settings.configured_at IS NOT NULL`                                                      |

`tenant_settings` gains one new nullable timestamp column: `chose_ide_at timestamptz`. It is set:

- By `ApplyIDEPreset` (any preset), the first time it succeeds for the tenant.
- By a new `MarkIDEChoiceSkipped` RPC the SPA fires from a small "Use custom URIs only" link on the onboarding card. Same semantics as `invited_team_at` — a nudge, not a verification.

Both writes go through the existing one-shot-setter pattern in `internal/tenant/service.go::UpdateSettings` (the `invited_team_at_now` sentinel field — extended to add `chose_ide_at_now`).

The card's UI is the IDE grid from the mockup: 2×2 on `sm`, 1×4 on `md`+, each tile a `<button>` that toggles a checkmark and POSTs `ApplyIDEPreset` on confirm. A "Skip for now" link below the grid fires `MarkIDEChoiceSkipped` and marks the step done.

### Settings page changes

The DCR allowlist section is rebuilt as **two sub-sections**, matching the mockup:

1. **Common IDEs** — grid of buttons, one per `IDEPreset`, sorted by `sort_order`. Each button shows the preset's icon + name. Visual state:
   - **Active** (border-primary + check icon): every pattern in the preset is present in the tenant's allowlist.
   - **Partial** (border-warning + dot icon): some patterns present, some missing. Tooltip lists the missing ones; clicking re-runs `ApplyIDEPreset`.
   - **Inactive** (border-outline-variant + plus icon): no patterns from this preset present; click runs `ApplyIDEPreset`.
   - Long-press / kebab menu offers `RemoveIDEPreset` — clears all rows with this `ide_key`.
2. **Custom Redirect URIs** — table with columns `App/IDE Name` (= label), `Pattern (Glob)`, Actions (edit / delete). Header row offers **Add Custom URI** which opens a modal with two fields (Label, Pattern); both validated client-side via the existing shared validator. The row's `ide_key` stays `NULL`; if the admin manually types a pattern that exactly matches a preset's row they get a hint ("This matches the Cursor preset — apply the whole preset?") but we don't silently fold them together.

The `RedirectURIAllowlistEditor.vue` component in `web/shared/src/components/` is **deleted** in this phase. Its only consumer (Phase 9c `Settings.vue`) switches to a new `IDEAllowlistManager.vue` (also in `web/shared/`) which owns the two-sub-section layout. `validateRedirectURIPattern` is unchanged; only the component shell goes.

### Tenant + admin service plumbing

`internal/tenant/service.go` grows the per-tenant allowlist + IDE-preset operations. Keeping them in `tenant.Service` (not a new package) matches the [`AGENTS.md`](../../AGENTS.md) DRY rule: the allowlist is per-tenant tenancy state, exactly like settings; splitting it would duplicate `Store` plumbing for no payoff.

```go
// internal/tenant/service.go (additions)

func (s *Service) ListAllowlistEntries(ctx context.Context) ([]AllowlistEntry, error)
func (s *Service) ListAllowlistPatterns(ctx context.Context) ([]string, error) // for oauthproxy
func (s *Service) AddAllowlistEntry(ctx context.Context, in AddAllowlistEntryInput) (AllowlistEntry, error)
func (s *Service) UpdateAllowlistEntry(ctx context.Context, publicID string, in UpdateAllowlistEntryInput) (AllowlistEntry, error)
func (s *Service) RemoveAllowlistEntry(ctx context.Context, publicID string) error
func (s *Service) ApplyIDEPreset(ctx context.Context, ideKey string) (ApplyResult, error)   // returns {added: N, alreadyPresent: M}
func (s *Service) RemoveIDEPreset(ctx context.Context, ideKey string) (int, error)          // rows deleted
```

`ApplyIDEPreset` and `RemoveIDEPreset` run inside a single `storage.Session` transaction; they read the preset rows from `ide_presets` / `ide_preset_patterns` (read-allowed for `limen_app`), then INSERT/DELETE under RLS for the current tenant. `ApplyIDEPreset` flips `chose_ide_at` the first time it succeeds for the tenant.

`internal/idepresets` is a new tiny read-only package:

```go
// internal/idepresets/idepresets.go
type Preset struct {
    Key, DisplayName, Icon string
    Patterns               []string
    SortOrder              int
}
func List(ctx context.Context, db *gorm.DB) ([]Preset, error)
func Seed(ctx context.Context, db *gorm.DB) error                 // idempotent UPSERT
```

`internal/admin/{ide_presets,allowlist}.go` are thin Connect-RPC translation layers over `tenant.Service` + `idepresets.List` — identical pattern to the existing `internal/admin/settings.go`. Errors map: `ErrPresetNotFound → NotFound`, `ErrAllowlistEntryInvalid → InvalidArgument` (with `field_path` detail), `ErrAllowlistEntryNotFound → NotFound`.

### Validation rules

- `pattern`: must pass `oauthproxy.ValidateRedirectURIPattern`. Same rule, server and client. Duplicate `(tenant_id, pattern)` returns `AlreadyExists`.
- `label`: 1..120 chars, trimmed, no control chars (`unicode.IsControl`). Empty after trim → `InvalidArgument` with `field_path: "label"`.
- `ide_key` on add: either `""` (custom) or a key that exists in `ide_presets`. Unknown key → `InvalidArgument`.
- `ApplyIDEPreset`: unknown `ide_key` → `NotFound`. Empty preset (no patterns) → `FailedPrecondition` with message "preset has no patterns" (defence in depth — seed always populates patterns).
- `RemoveAllowlistEntry`: row not found in current tenant → `NotFound`; soft-delete via `gorm.DeletedAt` to keep the audit trail.

## Seed data

`00009_ide_presets.sql` (RLS-skipped because global) plus a Go-side `internal/idepresets.Seed` ensure these rows exist on first boot. The IDE list and patterns come from the Phase research summary (Kagi notebook linked in this phase's PR description); icons are Lucide names approved in [`docs/frontend-design.md`](../frontend-design.md).

| `key`         | `display_name`      | `icon`     | Patterns                                                                          |
| ------------- | ------------------- | ---------- | --------------------------------------------------------------------------------- |
| `cursor`      | Cursor              | `terminal` | `cursor://anysphere.cursor-mcp/oauth/callback`, `http://127.0.0.1:54321/callback` |
| `vscode`      | VS Code             | `code`     | `http://127.0.0.1:33418`, `https://vscode.dev/redirect`                           |
| `claude_code` | Claude Code         | `bot`      | `http://localhost:*/callback`, `http://127.0.0.1:*/callback`                      |
| `codex`       | OpenAI Codex        | `brain`    | `http://localhost:1455/auth/callback`                                             |
| `opencode`    | OpenCode            | `package`  | `http://127.0.0.1:19876/mcp/oauth/callback`                                       |
| `gemini_cli`  | Gemini CLI          | `sparkles` | `http://localhost:*/oauth/callback`                                               |
| `windsurf`    | Windsurf            | `wind`     | `http://localhost:*/callback`                                                     |
| `cline`       | Cline (VS Code ext) | `code-2`   | `vscode://saoudrizwan.claude-dev/mcp-auth/callback/*`                             |
| `kiro`        | Kiro                | `monitor`  | `http://localhost:*/callback`                                                     |

`sort_order` matches the table order above; tiles below the fold are still discoverable through the "Show all IDEs" expander on the Settings page (the onboarding card shows only the top 4: cursor, vscode, claude_code, codex). The 4-tile cap matches the mockup; we tune it via `sort_order` without code changes.

Every seeded pattern is round-tripped through `oauthproxy.CompilePattern` by a startup self-check in `internal/idepresets.Seed`; a typo in this table fails boot loudly rather than silently shipping an invalid preset.

## Deliverables

- `proto/limen/admin/v1/admin.proto` — new RPCs + messages above; **deleted** fields on `UpdateTenantSettingsRequest` and `GetTenantSettingsResponse` per the cutover policy.
- `internal/storage/migrations/postgres/00009_ide_presets.sql` — RLS-skipped (global), grants, seed UPSERTs.
- `internal/storage/migrations/postgres/00010_tenant_redirect_uri_allowlist.sql` — RLS + grants for the per-tenant table; drops the JSONB column on `tenant_settings` in the same migration.
- `internal/storage/model_ide_preset.go`, `model_ide_preset_pattern.go`, `model_tenant_redirect_uri_allowlist.go` — GORM models.
- `internal/idepresets/idepresets.go` — read-only catalog + `Seed`.
- `internal/tenant/service.go` — methods listed in [Tenant + admin service plumbing](#tenant--admin-service-plumbing); `ListAllowlistPatterns` is the single read site `oauthproxy` uses.
- `internal/admin/ide_presets.go`, `internal/admin/allowlist.go` — thin Connect handlers + error mapping.
- `internal/oauthproxy/dcr.go` — switch from `tenant.DCRRedirectURIAllowlist` to `tenantSvc.ListAllowlistPatterns(ctx)`; everything else in `oauthproxy` is unchanged.
- `internal/ids/prefixes.go` — `PrefixAllowlistEntry Prefix = "ral"`.
- `web/admin/src/pages/Dashboard.vue` — fourth task tile + N=4 progress; reads chose_ide_at.
- `web/admin/src/pages/Settings.vue` — replaces the flat `RedirectURIAllowlistEditor` with `IDEAllowlistManager`.
- `web/admin/src/components/IDEAllowlistManager.vue` (or `web/shared/src/components/…` if Phase 9b ends up wanting it too — default to admin-only and lift when a second consumer appears).
- **Deleted**: `web/shared/src/components/RedirectURIAllowlistEditor.vue` + its spec.
- Unit + real-Postgres integration tests for `tenant.Service` allowlist + preset methods (cascade behaviour, dedup on insert, RLS isolation, soft-delete).
- Vitest specs for `IDEAllowlistManager.vue` and the updated Dashboard / Settings flows.
- Tripwire greps in the PR checklist:
  - `grep -RIn 'DCRRedirectURIAllowlist' .` → must return zero hits.
  - `grep -RIn 'dcr_redirect_uri_allowlist' .` → zero hits in `proto/`, `internal/`, `web/`.
  - `grep -RIn 'RedirectURIAllowlistEditor' web/` → zero hits.

## Security & operational notes

- The matcher (`oauthproxy.PatternSet.Match`) and the floor (`ValidateRedirectURI`) are **the only** authoritative checks. The UI's per-row validation is a UX accelerator; a hostile admin who bypasses the SPA still hits the floor + matcher on every DCR.
- `ide_presets` and `ide_preset_patterns` are read by `limen_app` but written only by `limen_admin` (the migration / refresher role). Seed-on-boot uses `limen_admin` via the existing migration path; there is no runtime "edit preset" surface, so the app role never needs `INSERT`.
- RLS on `tenant_redirect_uri_allowlist` is `FORCE` + `tenant_isolation`, identical to `tenant_settings`. Verified by an integration test that opens two RLS sessions for different tenants and confirms zero cross-visibility.
- No new secrets, no new keys, no new network egress. The DCR proxy's behaviour is unchanged — the proxy's input is, by construction, the same deduplicated `[]string` it always was.
- Deleting a tenant cascades the allowlist (FK `ON DELETE CASCADE`) via the existing Phase 9c `tenant.Service.Delete` cascade list; the list grows by one row (`UpstreamLink → Upstream → … → TenantRedirectURIAllowlist → TenantSettings → Tenant`). The `Delete` cascade test gains an assertion on this row count.

## Checklist

- [ ] Proto regenerated; old `dcr_redirect_uri_allowlist*` fields removed from messages.
- [ ] `00009_ide_presets.sql` + `00010_tenant_redirect_uri_allowlist.sql` land in one commit with the GORM models and the Go-side `dcr_redirect_uri_allowlist` column drop.
- [ ] `internal/idepresets.Seed` boot self-check rejects an invalid pattern (covered by a deliberately-broken-row unit test).
- [ ] `oauthproxy/dcr.go` reads from `tenant.Service.ListAllowlistPatterns`; the matcher API is untouched.
- [ ] `tenant.Service` real-Postgres tests cover: list (empty + populated), add (dedup, validation), update, remove (soft-delete), apply-preset (idempotent on re-apply), remove-preset (only matching `ide_key`), cross-tenant RLS isolation.
- [ ] `Settings.vue` Vitest specs cover the IDE grid states (active / partial / inactive), apply-preset RPC call, remove-preset confirm, and the custom URI table (add / edit / delete).
- [ ] `Dashboard.vue` Vitest spec covers the new task tile and `chose_ide_at` completion + skip path.
- [ ] All three tripwire greps return zero hits.
- [ ] `make build`, `go vet ./...`, `golangci-lint run ./...`, `go test -race ./...`, `pnpm -r build`, `pnpm -r test` all green locally before merging.

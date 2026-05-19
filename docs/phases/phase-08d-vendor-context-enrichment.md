# Phase 8d — Vendor context enrichment

**Depends on**: Phase 8c (context blob lives on `UpstreamLink.ContextJSON`,
read at `codemode.tools()` time, validated by `ValidateContextBlob`)
**Unblocks**: users no longer paste stable workspace identifiers
(`cloudId`, `organizationSlug`, GitHub installation IDs, Linear
`teamId`, etc.) into the portal — the gateway derives them from each
vendor's own discovery endpoint.

## Goal

Phase 8c shipped the **visibility** half of ambient context:
`codemode.tools()` surfaces a per-upstream blob, the script chooses
whether to spread it into args, and override semantics are clean
because the gateway never injects. What 8c deliberately left out was
**discovery**: where the values in that blob come from. Today they
come from the portal — an admin edits `Upstream.DefaultsJSON`, or a
user edits their own `UpstreamLink.ContextJSON`. That works for
truly free-form values (a preferred project name) but is busywork for
identifiers the vendor will happily tell us if we ask.

8d closes that gap with a new orthogonal abstraction — **the
Enricher** — and ships concrete enrichers for the vendors users
actually reach for first:

| Vendor    | Endpoint                                          | Yields                                                |
| --------- | ------------------------------------------------- | ----------------------------------------------------- |
| Atlassian | `GET https://api.atlassian.com/oauth/token/accessible-resources` | `cloudId`, `siteName`, `siteUrl` (or array if multi-site) |
| GitHub    | `GET https://api.github.com/user/installations`   | `installations: [{ id, account }]`                    |
| Linear    | GraphQL `viewer { organization { id urlKey name } }` | `organizationId`, `organizationUrlKey`, `organizationName` |
| Sentry    | `GET /api/0/organizations/`                       | `organizations: [{ slug, id, name }]`                 |

These four were selected because (a) each solves a real "model burned
a turn rediscovering this" failure mode observed in transcripts, and
(b) each vendor's discovery endpoint returns a stable, JSON-shaped
response under the same OAuth credentials the gateway already holds.
Notion, Slack, Microsoft Graph and similar are easy follow-ups once
the abstraction lands.

## Why this is a different axis from Strategy

8c proposed an `AfterFirstCall` hook on `upstream.Strategy` for this
same purpose and the design was rejected, then ripped out. The
recorded reasons in [docs/ambient-context.md §7](../ambient-context.md#7-open-question-automatic-context-discovery)
are the load-bearing constraints for 8d:

- **`Strategy` is authentication / transport.** `none`,
  `static_header`, `mcp_spec` — they answer "how do I attach
  credentials to outbound HTTP". Atlassian, Linear, Sentry, GitHub
  can all happily share the `mcp_spec` strategy because they all
  speak OAuth 2.1 + DCR; their *response shapes* have nothing in
  common.
- **The motivating data is not on a tool-call response.** Atlassian
  exposes `cloudId` on `/oauth/token/accessible-resources` (an
  OAuth-side endpoint), not on any MCP tool. Hooking
  `AfterFirstCall(toolName, args, response)` was the wrong *timing*
  in addition to the wrong *axis*.
- **No standard.** I looked: RFC 6749, RFC 8414, RFC 7591, RFC 8707,
  RFC 9396, OIDC Core, the MCP authorization draft — none defines a
  "list resources accessible to this token" endpoint. Every vendor
  invents its own URL and response shape. There is nothing for the
  auth strategy to consume generically.

So Enricher is a **separate concept**, indexed by **vendor**, called
on **link lifecycle events** (not per tool call), reading the link's
existing access token but otherwise independent of the strategy
machinery.

## Scope

### In

1. New optional `Upstream.Vendor` field (TEXT, nullable, indexed).
   When set, takes precedence over URL matching.
2. New `internal/enricher/` package: `Enricher` interface, `Registry`,
   URL-pattern + explicit-vendor resolution.
3. Concrete enrichers for **Atlassian, GitHub, Linear, Sentry**.
4. Trigger sites — in priority order:
   - **At link completion** (`Strategy.FinishLink` success). Fires
     fire-and-forget; first `codemode.tools()` read picks up the
     result.
   - **On portal request** ("Refresh context" button, RPC verb on
     the portal service). Synchronous; surfaces errors.
   - **Background maintenance** in the Phase 11 cron pass for
     enrichers that mark themselves as time-varying (e.g. GitHub
     installations can be added/removed).
5. New `UpstreamLink.LastEnrichedAt timestamptz` so the background
   pass can skip recent successes and the portal can show "Last
   refreshed 12 min ago".
6. Per-vendor key allowlist. Each enricher declares which top-level
   keys it is allowed to write; the runtime drops anything else
   before validating. Prevents accidental PII bloat if a vendor adds
   new fields to its response.
7. Same `ValidateContextBlob` gate (object root, ≤ 4 KB,
   JS-identifier keys) all writes go through.

### Out

- **No per-call enrichment.** 8c rejected this; 8d doesn't bring it
  back.
- **No enricher on `Strategy`.** Side-channel registry, keyed by
  vendor, completely independent of the auth driver.
- **No write-back to upstreams.** Enrichers are read-only against
  the vendor's discovery endpoint.
- **No cross-vendor abstraction.** We do NOT normalize
  `atlassian.cloudId` ↔ `linear.organizationId` ↔ `sentry.slug`
  into a generic `workspaceId`. Each vendor's keys ship raw. The
  model knows what `cloudId` means for Atlassian; abstracting it
  loses information.
- **No self-hosted Atlassian / Bitbucket Server / Linear-on-prem.**
  Those work via the explicit `Upstream.Vendor` override but their
  discovery endpoints may differ; we ship cloud-hosted recipes
  first. Adding self-hosted variants is per-vendor follow-up work.

## Vendor identity resolution

```
resolve(upstream) → vendor name or "":
  1. If upstream.vendor != "" → return upstream.vendor
  2. For each (vendor, pattern) in registry.byURL:
       if pattern.Match(upstream.mcp_server_url) → return vendor
  3. Return "" (no enricher; do nothing)
```

URL patterns are **hardcoded in each enricher package** — there is no
config file or admin UI for them. Patterns are intentionally narrow
and listed in the enricher's source:

| Vendor    | URL patterns                                                |
| --------- | ----------------------------------------------------------- |
| atlassian | `*.atlassian.com`, `*.atlassian.net`, `mcp.atlassian.com`   |
| github    | `api.github.com`, `mcp.github.com`                          |
| linear    | `api.linear.app`, `mcp.linear.app`                          |
| sentry    | `*.sentry.io`, `mcp.sentry.dev`                             |

The explicit `Upstream.Vendor` override exists for:
- Self-hosted instances we ship recipes for in later phases.
- Internal proxies / staging envs (the URL hostname is unrelated to
  the upstream vendor).
- Disambiguation when a single host serves multiple products.

## Enricher contract

```go
// internal/enricher/enricher.go

type Enricher interface {
    // Name is the vendor identifier (matches Upstream.Vendor).
    Name() string

    // Match reports whether this enricher claims the given MCP server URL.
    // Pattern matching only — does not perform any HTTP.
    Match(mcpServerURL string) bool

    // AllowedKeys is the per-vendor allowlist of top-level keys that
    // Enrich may produce. Anything not in this list is silently
    // dropped before ValidateContextBlob. Keeps responses bounded and
    // lets reviewers see at a glance what each vendor surfaces.
    AllowedKeys() []string

    // Enrich performs the discovery call(s) using the provided
    // authenticated client and returns a key→value map suitable for
    // shallow-merging into UpstreamLink.ContextJSON.
    //
    // Best-effort: errors are logged by the caller, not surfaced to
    // the user (the link still works; the model just doesn't get the
    // ambient values this round).
    Enrich(ctx context.Context, in EnrichInput) (map[string]any, error)
}

type EnrichInput struct {
    // HTTPClient already wraps the AuthInjectingTransport for the
    // calling link — every request it makes carries the right bearer.
    HTTPClient   *http.Client
    UpstreamURL  string         // for vendors whose discovery URL is derived from their MCP URL
    Link         *storage.UpstreamLink
}
```

Constraints carried by the interface:

- `Enrich` runs synchronously when the caller wants to wait on it
  (portal "refresh" button) and fire-and-forget when it doesn't
  (FinishLink trigger). The enricher itself is unaware which mode it's
  in — that's a caller concern.
- `Enrich` MUST honor `ctx` cancellation. A 30-second hard cap is
  enforced at the registry level.
- `Enrich` MUST NOT panic. Recovered panics log
  `enricher.panic` and degrade to an empty result.
- `AllowedKeys()` is enforced by the registry, not by the enricher
  itself — defense in depth. If an enricher returns a key it didn't
  declare, the key is dropped and the breach is logged.

## Trigger sites

### 1. After link completion

`Strategy.FinishLink` returns a populated `*storage.UpstreamLink`.
A new wrapper in the gateway calls the registry's
`EnrichIfRegistered(ctx, link, upstream)` as a fire-and-forget goroutine
with a 30-second context and its own DB session (so the request-side
session can commit and return immediately). The enricher's output is
merged into `link.context_json` via jsonb `||` exactly the same way
the rejected `AfterFirstCall` path would have — but scoped via
`storage.WithTenant`, never `WithSuperuser`.

### 2. Portal "refresh context" RPC

Phase 9 adds a portal-side RPC `RefreshLinkContext(upstreamID)` that
runs the enricher synchronously, surfaces errors, and returns the new
context blob. Lets users self-heal when something changed upstream
(workspace added, name changed).

### 3. Background maintenance

Phase 11's cron pass iterates links with `LastEnrichedAt < now - 24h`
and re-runs the enricher if its `RefreshInterval()` (optional method,
default = "never") indicates so. Enrichers for stable data
(`atlassian.cloudId`) return zero; enrichers for time-varying data
(`github.installations`) return e.g. 6h.

## Storage changes

```sql
-- internal/storage/migrations/00NN_phase08d_vendor.sql
ALTER TABLE upstreams
    ADD COLUMN vendor text;
CREATE INDEX upstreams_vendor_idx ON upstreams(vendor) WHERE deleted_at IS NULL;

ALTER TABLE upstream_links
    ADD COLUMN last_enriched_at timestamptz;
```

GORM models grow one field each. No RLS changes — both columns inherit
the parent table's policy.

## Concrete enrichers

### Atlassian — `internal/enricher/atlassian/atlassian.go`

```
GET https://api.atlassian.com/oauth/token/accessible-resources
Authorization: Bearer <link.access_token>
```

Response shape (per [Atlassian docs](https://developer.atlassian.com/cloud/oauth/getting-started/making-calls-to-api/)):

```json
[
  { "id": "1324a887-45db-1bf4-1e99-ef0ff456d421",
    "name": "your-domain",
    "url": "https://your-domain.atlassian.net",
    "scopes": ["read:jira-work", "..."],
    "avatarUrl": "..." }
]
```

Allowlist: `cloudId`, `cloudIds`, `siteName`, `siteUrl`.

Reduction:
- 1 resource → `{ cloudId, siteName, siteUrl }`.
- N resources → `{ cloudIds: [{id, name, url}, ...] }` and let the
  user pick a default in the portal (sets `cloudId` explicitly). The
  enricher will not silently pick one.

### GitHub — `internal/enricher/github/github.go`

```
GET https://api.github.com/user/installations
Authorization: Bearer <link.access_token>
Accept: application/vnd.github+json
```

Allowlist: `installations` (array of `{ id, account_login, account_type }`).

`RefreshInterval()` → 6h (users add/remove app installations).

### Linear — `internal/enricher/linear/linear.go`

```
POST https://api.linear.app/graphql
Authorization: Bearer <link.access_token>
Content-Type: application/json

{ "query": "{ viewer { organization { id urlKey name } } }" }
```

Allowlist: `organizationId`, `organizationUrlKey`, `organizationName`.

### Sentry — `internal/enricher/sentry/sentry.go`

```
GET <upstream.url>/api/0/organizations/
Authorization: Bearer <link.access_token>
```

Note: Sentry's endpoint is on the SAAS host, derived from
`upstream.mcp_server_url`, which is why `EnrichInput.UpstreamURL` is
on the interface.

Allowlist: `organizations` (array of `{ slug, id, name }`).

## Slices

Land in this order; each is independently committable + reverible.

1. **Slice 1 — storage**
   - `Upstream.Vendor` column + migration.
   - `UpstreamLink.LastEnrichedAt` column + migration.
   - GORM model updates.
   - Tests: round-trip, RLS-inherits-from-parent assertion.

2. **Slice 2 — enricher infra**
   - `internal/enricher/{enricher.go, registry.go, resolve.go}`
   - `Registry.Resolve(upstream) → Enricher` (explicit override > URL).
   - `Registry.RunIfRegistered(ctx, store, logger, upstream, link)` —
     30s context, allowlist filter, `ValidateContextBlob`, jsonb merge
     under `WithTenant`, sets `LastEnrichedAt`. Panic recovery + log
     `enricher.panic`. Failure log `enricher.failed` with vendor +
     phase.
   - Unit tests with a fake enricher: returns valid / invalid /
     panicking; assert merge / drop / recover.

3. **Slice 3 — first concrete enricher: Atlassian**
   - `internal/enricher/atlassian/atlassian.go`
   - HTTP-fixture tests (1 resource / N resources / 401 / 500).

4. **Slice 4 — wire into FinishLink**
   - Gateway wrapper that fires `RunIfRegistered` as a goroutine on
     successful `FinishLink`. 30s hard cap. Best-effort — errors only
     reach logs.
   - Integration test: simulate FinishLink with an Atlassian-shaped
     upstream; assert link.context_json eventually contains cloudId.

5. **Slice 5 — remaining enrichers: GitHub, Linear, Sentry**
   - One commit per vendor with HTTP-fixture tests.

6. **Slice 6 — portal "Refresh context"**
   - Belongs to Phase 9 (portal). Adds the synchronous RPC + button.
     Calling it from CLI is fine in the meantime.

7. **Slice 7 — background maintenance**
   - Belongs to Phase 11. `RefreshInterval()` optional method,
     iterate links in the cron pass.

Slices 1–5 are 8d's core. 6 and 7 are noted here so the phases that
own them know what to wire.

## Security and quota notes

- **Auth surface is unchanged.** Enrichers use the user's existing
  access token through the same `AuthInjectingTransport`. They don't
  see plaintext credentials; they don't escalate privilege; they don't
  unlock any scope the user hasn't already granted.
- **The 4 KB context budget is shared** with admin defaults and portal
  edits. If a future enricher response is large (e.g. a user with 200
  GitHub installations), the allowlist + `ValidateContextBlob` will
  truncate or reject; the enricher must cap arrays to a sensible upper
  bound (~50 entries) before returning.
- **Vendor rate limits matter.** Fire-and-forget at FinishLink is
  once per link, so cheap. The Phase 11 cron pass MUST batch with
  per-vendor token-bucket throttling (concrete numbers per vendor in
  Phase 11).
- **PII.** OIDC `/userinfo`-style claims (email, name) are intentionally
  NOT pulled by these vendor enrichers; they pull workspace
  identifiers only. A separate generic OIDC-userinfo enricher (different
  follow-up phase) would be where identity claims land, with its own
  consent surface.

## Open questions

1. **Multi-site Atlassian users.** Picking a default `cloudId` requires
   user input. Until the portal lands the picker (Phase 9), the enricher
   will write the full `cloudIds` array and the model will see it but
   not a single `cloudId`. Acceptable interim — better than today's
   "paste it manually" — but worth flagging.
2. **Self-hosted instances.** Enterprise GHE / Bitbucket Server / Sentry
   on-prem need different URLs and sometimes different response shapes.
   The `Upstream.Vendor` override picks the right enricher, but the
   enricher itself currently hardcodes the SaaS endpoint URL. Either:
   - Add `Upstream.VendorEndpointOverride` (text URL) — simple, ugly.
   - Each enricher derives its endpoint from `upstream.mcp_server_url`
     (already on `EnrichInput`) — cleaner, but each vendor's MCP URL ↔
     REST API URL mapping differs.
   - Defer to a per-vendor follow-up phase. Most likely path.
3. **Refresh on token refresh?** When `HeadersForceRefresh` swaps in a
   new token, should we re-enrich? Tokens rotating doesn't change the
   user's workspace membership, so probably not. But scope changes
   between refreshes (rare) could. Default: no re-enrich on refresh;
   rely on the cron pass + manual portal refresh.
4. **Caching the vendor's response.** Some endpoints
   (`/accessible-resources`) are eligible for short-lived caching across
   links if multiple users in the same tenant authorize the same
   Atlassian site. Not worth it for v1 — adds tenancy questions and
   the API quota is per-user-token anyway.

## What this phase deliberately does NOT do

- Add an enricher hook to `Strategy`. That's the rejected design from
  8c; we are not re-litigating.
- Inject context values into outbound tool calls. The visibility rule
  still holds; only the *contents* of the visible blob change.
- Provide a generic OIDC `/userinfo` enricher. That's a different,
  smaller follow-up and lives outside the per-vendor registry.
- Add config-file vendor-pattern customization. Patterns live in code
  next to the enricher that owns them — one source of truth, no
  divergence between cluster instances.
- Promise that the cloudId / installationId / etc. is "always there".
  Enrichment is best-effort; the model must still tolerate a missing
  value (the spread idiom handles this trivially: `{...ctx, ...args}`
  with no `cloudId` in either just doesn't carry one).

## Checklist

- [ ] **Slice 1** — `Upstream.Vendor` + `UpstreamLink.LastEnrichedAt`
      columns; migration; GORM models updated; round-trip + RLS
      inheritance tests.
- [ ] **Slice 2** — `internal/enricher/` package with `Enricher`
      interface, `Registry`, allowlist enforcement, jsonb merge under
      `WithTenant`, panic recovery, structured failure logging
      (`enricher.failed`, `enricher.panic`, with vendor + phase
      labels). Unit tests with a fake enricher.
- [ ] **Slice 3** — Atlassian enricher + HTTP fixture tests for
      1-resource / N-resources / 401 / 500 paths. Validates the
      multi-site `cloudIds` array path.
- [ ] **Slice 4** — gateway wires `RunIfRegistered` into
      `FinishLink` as a fire-and-forget goroutine with 30s cap.
      Integration test through `Manager` with an Atlassian-shaped
      fixture upstream.
- [ ] **Slice 5** — GitHub, Linear, Sentry enrichers + per-vendor
      HTTP fixture tests. One commit per vendor.
- [ ] Documentation: rewrite [docs/ambient-context.md §7](../ambient-context.md#7-open-question-automatic-context-discovery)
      from "open question" to "implemented; see Phase 8d"; add a per-vendor
      key table; cross-link from upstream docs.
- [ ] Phase 11 follow-up captured: cron-side maintenance using
      `RefreshInterval()`.
- [ ] Phase 9 follow-up captured: portal RPC + button for
      "Refresh context".

# Phase 8c — Ambient context, alias autodiscovery, empty-filter hints

**Depends on**: Phase 8 (per-tenant injection — gives upstreams a real
identity and per-user link rows we can hang context off of)
**Unblocks**: shorter, more reliable codemode workflows; fewer
round-trips spent rediscovering org slugs, cloudIds, project defaults,
and brand-name spellings.

## Goal

Three small, related changes to the upstream + codemode surface that
collectively eliminate the "I burned a tool call rediscovering context
the gateway already knew" failure mode observed in real transcripts:

1. **Per-upstream context surfaced to the script**: each upstream
   carries a JSONB blob of stable metadata (cloudId, organizationSlug,
   default project, etc.). The blob is **exposed to the JS sandbox
   inside the `codemode.tools()` listing**, grouped per upstream
   alongside that upstream's tools — it is **not** injected into
   tool calls. The model sees it, decides what to spread into args,
   and stays in full control of the request shape.
2. **Alias autodiscovery from tool names**: an upstream registered as
   `atlassian` becomes reachable as `codemode.jira.*` and
   `codemode.confluence.*` automatically because its tools are named
   `jira_search`, `confluence_get_page`, … No admin config; pure
   derivation.
3. **Empty-filter hints from `codemode.tools()`**: when a filtered
   listing returns no groups and a filter was actually supplied, the
   sandbox surfaces a `hint` field with closest-matching upstreams /
   aliases. Silent failure → actionable failure.

This is a **breaking change** to `codemode.tools()`. The return type
changes from a flat `ToolListing[]` to a small envelope grouping tools
by upstream and carrying any empty-filter hint. The change is
intentional: the existing flat shape forced a separate `upstreams()`
verb to carry per-upstream metadata, and we'd rather unify the
discovery surface than grow it.

Together these turn the model's typical "guess → empty → retry"
pattern into a single correct call.

### Why visibility, not injection

The obvious-looking alternative — merge the context blob into every
tool call's args server-side, transparently — was the original design
and was rejected. Reasons:

- **The script is the program.** Codemode's core property is that the
  JS source you wrote is exactly what runs. Silently mutating tool
  args before dispatch breaks that. Debugging "why did this tool
  receive a `cloudId` I didn't pass?" is much harder than reading the
  script.
- **Logs stay honest.** `codemode.tool.called` records the args the
  script supplied; if we mutated them on the way through, the log
  shape would either lie or balloon to record both views.
- **Precedence rules disappear.** Three-layer merge (model › link
  › upstream) plus a `null`-erases-default escape hatch is a lot of
  prompt text the model has to internalize correctly. Surfacing the
  blob and letting the script spread it (`{...up.context, ...args}`)
  uses plain JavaScript semantics the model already knows.
- **The model can refuse it.** Sometimes the ambient default is wrong
  for one call. With injection the script has to know to override
  with `null`. With visibility it just doesn't spread.

The AfterFirstCall autopopulation hook still exists — it writes the
cloudId / orgSlug to storage on first successful call. The model
picks it up on its next `codemode.tools()` read. "Magic" lives in
the storage layer, not in the dispatch path.

## Why now

Three production transcripts (`docs/research/transcripts/` — Cloudflare
attempt 1 & 2, Sentry triage) all stumbled on the same root
cause: **the model has to discover context the gateway already
knows**. Each stumble cost a codemode invocation, and because there is
no shared state between invocations, the rediscovery work could not
even be reused — the next script paid the same tax again.

Phase 8 made the gateway aware of who the user is; Phase 8b made the
sandbox fast enough that fan-out is cheap. Phase 8c removes the
remaining structural reason the LLM needs more than one round-trip for
a "find issue in Sentry, file it in Jira, assign it" workflow.

## Design

### 1. Per-upstream context (storage + visibility)

#### Storage

Two JSONB columns, both shaped as `{}` JSON objects, both NOT NULL
with a default so reads never see nil.

| Owner            | Column                | Scope                        | Source                                                                                                                                         |
| ---------------- | --------------------- | ---------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| `upstreams`      | `defaults_json` JSONB | per (tenant, upstream)       | Admin sets in portal when registering the upstream.                                                                                            |
| `upstream_links` | `context_json` JSONB  | per (tenant, user, upstream) | User-specific, two ways to populate: (a) user fills in portal on link, (b) autopopulated by a strategy callback on first successful tool call. |

GORM models gain:

```go
type Upstream struct {
    // …
    DefaultsJSON datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'::jsonb"`
}

type UpstreamLink struct {
    // …
    ContextJSON datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'::jsonb"`
}
```

AutoMigrate handles the column creation; no goose migration is needed.
A goose migration **is** added only to backfill existing rows from
`NULL` → `'{}'::jsonb` if GORM's default-on-insert leaves nulls in
already-present rows (verify against a dev DB; if AutoMigrate sets the
default cleanly the goose file is dropped before merge).

Neither column is encrypted: this is metadata the user typed in the
portal or that the upstream's own discovery endpoint returned. If a
strategy needs to stash a _secret_ default it goes in the existing
`ExtraJSON` (already a `crypto.SecretField`), not here.

#### Validation

Both columns must hold a **JSON object** (`{...}`) at the top level.
Validation runs at two layers:

1. **At write time** (portal Connect-RPC handlers — Phase 9c's
   `AdminService.UpdateUpstream` for `defaults_json` and Phase 9b's
   per-link editor for `context_json`):
   - Parse with `encoding/json` into a `map[string]any`. Reject
     non-object roots (`[]`, scalars, `null`) with a 400-equivalent
     Connect error and a structured field path so the SPA can
     highlight the offending value.
   - Reject blobs > 4 KB serialized. 4 KB is ~20× any realistic
     context blob; bigger usually means the user pasted a token they
     shouldn't have.
   - Reject keys whose names are not valid JS identifiers when the
     model would have to spread them. Concretely: each top-level key
     matches `^[A-Za-z_$][\w$]*$`. The model can spread `{...up.context}`
     without bracket-notation gymnastics.
2. **At read time** in `(*Handler).run`, defense-in-depth: if the
   stored bytes fail to unmarshal into a `map[string]any` (corruption,
   manual SQL edit, schema drift), **discard the blob silently and
   surface `context: {}` to the script**. Log
   `gateway.context.invalid_json` with the upstream / link ID. Never
   panic, never block the catalog load.

The write-time path is the canonical guard; the read-time path exists
so a single bad row cannot break the sandbox for the whole tenant.

#### Visibility — context rides on `codemode.tools()` groups

The per-upstream context is exposed as a field on each group in the\n`codemode.tools()` response (see §3 for the full envelope shape). For\neach upstream visible to the calling (tenant, user), the group carries\na merged `context` blob:

```ts
type UpstreamGroup = {
  name: string; // canonical upstream name
  aliases: string[]; // derived (§2)
  context: Record<string, JSONValue>; // shallow merge: link › upstream defaults
  tools: { name: string; description: string }[];
};
```

Merge semantics for the **read-time view** only:

- Shallow merge by top-level key. Link `context_json` wins over
  upstream `defaults_json` per key.
- No nested merging. The whole top-level value from the higher-priority
  source is taken as-is.
- Empty object (`{}`) if both blobs are empty.

The model uses it in JS:

```js
const { upstreams } = codemode.tools({ upstream: "atlassian" });
const up = upstreams[0];
await codemode.atlassian.jira_search({
  ...up.context, // spreads cloudId, defaultProject, …
  jql: "project = OP AND status = Open",
});
```

There is **no server-side merge into tool args**. `(*Manager).CallTool`
ignores the context blob entirely; it just dispatches whatever the
script supplied. This is the property that makes the script the
source of truth and the logs honest.

#### Autopopulation

A strategy may register an `AfterFirstCall(link, response)` hook that
parses the response and writes into `link.context_json`. The Atlassian
strategy, for instance, observes the `/oauth/token/accessible-resources`
response shape and pins `cloudId` after the first call. Result: the
user never pastes a cloudId; after the first Atlassian tool call, the
next `codemode.tools()` view exposes it on the Atlassian group and the
script spreads it in.

Hook semantics:

- Runs **after** a successful tool call returns.
- Writes a delta via JSONB `||` so the hook never clobbers a value
  the user set manually for a different key.
- Failures in the hook are logged (`codemode.context.autopopulate.failed`)
  but do not surface to the script — autopopulation is best-effort.
- Writes go through the admin pool (the runtime pool runs as
  `limen_app` with RLS; the hook needs to touch arbitrary user rows it
  doesn't own when batched). One narrow `UPDATE upstream_links SET
context_json = context_json || $1 WHERE id = $2` per hook call.
- The hook output is itself validated as a JSON object ≤ 4 KB before
  being persisted; invalid deltas are dropped with the same warn log.

### 2. Alias autodiscovery

#### Derivation

Run on the same path that hydrates the upstream tool cache (Phase 8).
For each upstream, group tool names by the part before the first `_`
(or `-`). Any prefix that covers ≥ 50% of the upstream's tools AND has
at least two tools becomes an alias. The upstream's canonical name is
always an alias too.

Examples:

| Upstream canonical | Tool names                                             | Derived aliases                          |
| ------------------ | ------------------------------------------------------ | ---------------------------------------- |
| `atlassian`        | `jira_search`, `jira_get_issue`, `confluence_get_page` | `atlassian`, `jira`, `confluence`        |
| `cloudflare`       | `list_zones`, `get_zone`, `create_ai_gateway`          | `cloudflare` only — no prefix dominates  |
| `github`           | `github_search_issues`, `github_create_pr`             | `github` (canonical + degenerate prefix) |

The 50% / 2-tool floor is a heuristic: tighter and we miss `jira` /
`confluence` on a real Atlassian server (Confluence may be a small
minority of tools); looser and noisy single-letter prefixes leak in.
Adjustable via constant; no config knob.

#### Storage

Denormalized cached column on `upstreams`:

```go
type Upstream struct {
    // …
    Aliases pq.StringArray `gorm:"type:text[];not null;default:'{}'"`
}
```

Recomputed every time the tool cache is refreshed for that upstream
(same trigger that already runs on Phase 8 hydration / invalidation
paths). Stored on the row so codemode's binding setup can read it
without a second query and so `EXPLAIN` plans on alias lookups stay
trivial.

Conflict resolution:

- If two upstreams in the same tenant derive the _same_ alias
  (improbable but possible: two Atlassian instances both claiming
  `jira`), neither gets it. The canonical names still work. Log
  `gateway.alias.collision` once per refresh per pair.
- Aliases matching one of the reserved `codemode.*` keys (`tools`,
  `schemas`, `call`, `json`, `quota`) are dropped at registration time,
  same rule that already applies to canonical names.

#### Sandbox wiring

In `(*Handler).run`, when building the `codemode` object, register the
per-tool proxies under each alias as well as the canonical name:

```go
for _, alias := range append([]string{up.Name}, up.Aliases...) {
    if isReservedCodemodeKey(alias) { continue }
    if _, taken := codemodeObj.Get(alias); taken { continue }
    upObj := buildUpstreamProxyObject(up.Tools, ...)
    codemodeObj.Set(alias, upObj)
}
```

`codemode.tools({upstream: "jira"})` and `codemode.jira.<tool>()` both
resolve to the same set; the filter helper checks aliases as well as
canonical names. `codemode.call("jira", "search", args)` resolves
through the alias map too.

#### Visibility in catalog

`codemode.tools()` reports each upstream's **canonical** name plus its
derived `aliases` array on the group. The `aliases` field is the
single source of truth the model can read to know what names map
where. No separate `upstreams()` helper exists; everything the model
needs to discover is on the same response.

### 3. New `codemode.tools()` envelope shape

#### Return type

`codemode.tools()` now returns an envelope grouping tools by upstream
and carrying an optional `hint` for the empty case:

```ts
type ToolEntry = {
  name:        string;
  description: string;
};

type UpstreamGroup = {
  name:    string;                    // canonical upstream name
  aliases: string[];                  // derived (§2)
  context: Record<string, JSONValue>; // merged ambient context (§1); `{}` if empty
  tools:   ToolEntry[];               // tools surviving the filter
};

type EmptyHint = {
  tried:     string[];  // the filter values that yielded 0 groups
  available: string[];  // canonical upstream names visible to the user
  suggested: string[];  // ≤ 3 closest matches (canonical or alias)
};

type ToolsResult = {
  upstreams: UpstreamGroup[];
  hint?:     EmptyHint;
};

codemode.tools(filter?: ToolsFilter): ToolsResult
```

Notes:

- The per-tool `upstream` field is gone; it's implicit in the
  enclosing group. Saves bytes; eliminates the "tool says X, group
  says Y" inconsistency vector.
- Filters operate on tools (`name`, `description`, `match`, `allOf`,
  `regex`, `limit`) and on upstreams (`upstream`, which now also
  matches against aliases). Groups whose `tools` array filters down
  to zero are **dropped** from `upstreams`. The result always
  contains either ≥ 1 non-empty group OR an empty `upstreams` plus a
  populated `hint`.
- `limit` applies to the **total tool count across groups**, scanned
  in iteration order; partial groups are kept (i.e. a group may
  contain fewer tools than its full catalog if `limit` cut off
  mid-group).
- The envelope is the same shape whether the filter matched or not.
  Callers always do `result.upstreams.forEach(...)`; they don't
  branch on shape.

#### Filtering and aliases

`codemode.tools({upstream: "jira"})` against a tenant linked to
`atlassian` (with derived alias `jira`) returns the Atlassian group
\u2014 the `upstream` filter is matched against `name` ∪ `aliases`. The
group's `name` field is always the canonical name, regardless of
which alias the filter matched.

`codemode.jira.<tool>()` continues to resolve through the alias map
on the sandbox object; this is independent of the listing shape.

#### Empty-filter hint

When all of:

1. a filter was supplied (`filter != {}`),
2. the resulting `upstreams` array is empty,
3. ≥ 1 candidate upstream / alias is found within Levenshtein
   distance ≤ 2 OR substring of any `tried` value,

then `hint` is populated:

```ts
{
  tried:     ["jira"],
  available: ["atlassian", "sentry"],
  suggested: ["atlassian"],  // matched via alias "jira" → canonical "atlassian"
}
```

`suggested` is capped at 3 entries, deduplicated. Candidate set is
the tenant's `(canonical ∪ aliases)` list \u2014 single-digit in practice.

`hint` is omitted (not `null`, not `{}`) when any of (1)/(2)/(3) is
false. A genuinely empty tenant returns
`{ upstreams: [], hint: undefined }` \u2014 same as today's silent `[]`,
just in the new shape.

#### Cheap-discovery case

The "I don't even know what's linked, just show me the upstreams"
exploration case is served by calling `codemode.tools()` with no
filter. The model gets one envelope containing every group's name,
aliases, and context, plus the full tool list. The full list is the
same data the prompt was already about to ask for once the model
picked an upstream; pulling it eagerly costs nothing extra in the
realistic flow.

If the response size matters (very large tenant), the model uses a
narrow filter to scope. `codemode.tools({upstream: "atlassian"})`
returns one group with full tool list; `codemode.tools({match:
"search"})` returns every group whose tools survive the substring
filter.

### Prompt updates

`internal/gateway/codemodeaction/shared.go`:

- Replace the `codemode.tools()` return-type TS signature with the
  new envelope (`{ upstreams: UpstreamGroup[], hint?: EmptyHint }`).
- Document the `context` field on `UpstreamGroup` and the spread
  recipe.
- Add a one-line note that aliases are autoderived from tool name
  prefixes and that the canonical name always works.
- Add an explicit note: "context is **not** injected into tool calls
  — spread it yourself when you need it."

`internal/gateway/codemodeaction/execute.go`:

- Update every recipe and bad-example that destructures
  `codemode.tools()` as a flat array. New canonical pattern:
  `const { upstreams, hint } = codemode.tools({...});` then iterate
  `upstreams` or fall through to `hint`.
- New recipe: "upstream-aware call" — pick a group, spread
  `group.context` into the tool args alongside model-supplied keys.
- New recipe: "empty-filter recovery" — when `upstreams` is empty,
  read `hint.suggested` and retry against the first suggestion.

`internal/gateway/codemodeaction/search.go`:

- Same updates; search exposes the discovery helpers verbatim.

## File-level deliverables

- [internal/storage/model_upstream.go](../../internal/storage/model_upstream.go)
  — add `DefaultsJSON datatypes.JSON` on `Upstream`, `Aliases pq.StringArray`
  on `Upstream`, `ContextJSON datatypes.JSON` on `UpstreamLink`. All
  `not null default '{}' / '{}'::jsonb / '{}'::text[]`.
- [internal/storage/migrations/postgres/00007_phase8c_context_aliases.sql](../../internal/storage/migrations/postgres/00007_phase8c_context_aliases.sql)
  — goose migration **only if** AutoMigrate leaves existing rows with
  nulls. Otherwise omit.
- [internal/gateway/manager.go](../../internal/gateway/manager.go) —
  **no change to `CallTool` dispatch path**. The context blob is read
  on the codemode side; the manager remains a thin dispatcher.
- [internal/gateway/context.go](../../internal/gateway/context.go) —
  new file: `mergeContext(upstreamDefaults, linkContext) map[string]any`
  - JSON validation helpers (`validateContextBlob(b []byte) (map[string]any, error)`)
    used by both the portal write path and the read-time defense.
    Pure functions for easy testing.
- [internal/admin/upstreams_admin.go](../../internal/admin/upstreams_admin.go)
  (Phase 9c) — call `validateContextBlob` on `defaults_json` writes;
  return Connect `invalid_argument` with field path on rejection.
- [internal/portal/links.go](../../internal/portal/links.go) (Phase 9b)
  — same validation on `context_json` writes from the per-user link
  editor.
- [internal/gateway/bundle.go](../../internal/gateway/bundle.go) (or
  wherever upstream tool cache lives) — call
  `deriveAliases(tools) []string` on hydrate / invalidate, persist the
  result onto `upstream.Aliases`.
- [internal/gateway/aliases.go](../../internal/gateway/aliases.go) —
  new file: `deriveAliases([]ToolEntry) []string`, plus the
  collision-resolution pass that takes the tenant-wide alias set and
  drops duplicates. Pure function for easy testing.
- [internal/upstream/strategy.go](../../internal/upstream/strategy.go)
  (or equivalent strategy interface) — new optional
  `AfterFirstCall(ctx, link, args, response) (contextDelta map[string]any, err error)`
  method. Strategies that don't implement it get a no-op default via
  an interface adapter.
- [internal/upstream/atlassian/](../../internal/upstream) — concrete
  hook for cloudId pinning. If the Atlassian strategy doesn't exist as
  a separate package yet, add the hook inline in the existing
  `mcp_spec` strategy guarded by an upstream-URL prefix match.
- [internal/gateway/codemode/filter.go](../../internal/gateway/codemode/filter.go)
  — **breaking change**: replace `ToolListing` with `ToolEntry`
  (drop the per-tool `upstream` field). Replace `filterListings([]ToolListing,
filter) ([]ToolListing, error)` with `filterTools([]Tool, filter, ctxByUpstream)
(ToolsResult, error)` returning the new envelope. Filters apply to
  tools; empty groups are dropped; `hint` is computed when applicable.
- [internal/gateway/codemode/handler.go](../../internal/gateway/codemode/handler.go)
  — `codemode.tools()` binding returns the envelope directly. Thread
  the merged context view (built via `mergeContext` from
  `context.go`) per upstream into `filterTools`. Thread `Aliases`
  through to the per-upstream proxy registration loop so each alias
  gets its own `codemodeObj.Set(alias, upObj)`. Invalid stored JSON
  is dropped to `{}` with the `gateway.context.invalid_json` warn log.
  No new reserved keys (no `upstreams` verb).
- [internal/gateway/codemodeaction/shared.go](../../internal/gateway/codemodeaction/shared.go),
  [search.go](../../internal/gateway/codemodeaction/search.go),
  [execute.go](../../internal/gateway/codemodeaction/execute.go) —
  prompt updates per the section above.
- Tests (see below).

## Risks

| Risk                                                                                                               | Mitigation                                                                                                                                                                                                                                                                            |
| ------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Ambient context leaks across users (per-link `context_json` from user A's link returned in user B's script).       | The context view is built from the same per-(tenant, user) `bundleFor` lookup that already gates tool access; no cross-link reads. RLS on `upstream_links` is the load-bearing guarantee — covered by `internal/storage/rls_test.go`; extend it.                                      |
| Model forgets to read the `context` field on groups and re-stumbles on cloudId / orgSlug class errors.             | Prompt update teaches the read-then-spread recipe explicitly; the empty-filter `hint` on `codemode.tools()` and the per-tool error from the upstream both point back to inspecting `result.upstreams[i].context`. Cost of a missed read is one extra call, not a silent wrong answer. |
| Stored JSON corrupts (manual SQL edit, schema drift, partial write) and breaks the catalog load.                   | Read-time defense: invalid JSON is discarded silently, surfaced to the script as `context: {}`, logged once per load as `gateway.context.invalid_json`. Write-time validation prevents this in the normal path.                                                                       |
| Autopopulation hook writes wrong values, then sticky.                                                              | Hook writes a _delta_ via JSONB `\|\|`; the portal exposes a "reset" action that nulls `context_json` (writes `'{}'::jsonb`). Hook is best-effort: never panics, never blocks the call. Hook output passes the same `validateContextBlob` gate.                                       |
| Alias collision masks a tool unintentionally — e.g. tenant has two Atlassian upstreams, both want `jira`.          | Tenant-wide collision dropper: any alias claimed by ≥ 2 upstreams is dropped from all of them. Canonical names always remain. Logged as `gateway.alias.collision` for operator visibility.                                                                                            |
| Derived alias collides with a reserved sandbox key (`tools`, `schemas`, …).                                        | Same `isReservedCodemodeKey` filter that already applies to canonical names; dropped with a Warn log.                                                                                                                                                                                 |
| Empty-filter hint is _itself_ misleading (suggests `atlassian` when the user genuinely wants a fictitious `jira`). | Hint includes both `tried` and `available` so the LLM can audit the suggestion. Levenshtein distance ≤ 2 keeps suggestions tight. Worst case the model burns one extra call disambiguating — strictly better than the current silent `[]`.                                            |
| Context blob grows unbounded if a strategy autopopulates aggressively.                                             | Hard 4 KB cap enforced by `validateContextBlob` at both write paths and the autopopulate hook; oversize values rejected with `gateway.context.oversize` warn. 4 KB is ~20× any realistic context blob.                                                                                |
| `codemode.tools()` becomes a backdoor for catalog enumeration of disabled / needs-relink links.                    | Groups are built from the same set `ToolsForUser` already returns: enabled, not auto-disabled, not needs-relink. Names of unhealthy upstreams are not exposed.                                                                                                                        |
| **Breaking change** to existing codemode scripts that destructure `codemode.tools()` as a flat array.              | Pre-1.0 surface; the codebase is the only caller. Migrate prompts and any internal helpers in the same PR. No external SDK to coordinate with.                                                                                                                                        |
| Phase 8b parallel fan-out + alias proxies share underlying upstream — cap interaction.                             | Aliases are pure proxy registration; dispatch ultimately goes through the same `dispatchAsync` and `MaxConcurrentToolCalls` semaphore. No change to concurrency accounting.                                                                                                           |

## Verification

1. `go test ./internal/gateway/... -race -count=1` — new tests:
   - `TestMergeContext_LinkOverridesDefaults` — two-layer precedence
     (link › upstream defaults), shallow merge only.
   - `TestMergeContext_EmptyBothSides` — returns `{}`, never nil.
   - `TestValidateContextBlob_RejectsNonObjectRoot` — `[]`, scalars,
     `null` all rejected with a field-path error.
   - `TestValidateContextBlob_RejectsOversize` — > 4 KB rejected.
   - `TestValidateContextBlob_RejectsBadKeyShape` — keys with
     hyphens / spaces / leading digits rejected.
   - `TestCodemodeTools_GroupedByUpstreamWithContext` — a tenant with
     `atlassian` (defaults `{cloudId:"abc",defaultProject:"FALLBACK"}`)
     and a link override `{defaultProject:"OP"}` yields a group
     `{name:"atlassian", context:{cloudId:"abc", defaultProject:"OP"}, tools:[...]}`.
   - `TestCodemodeTools_InvalidStoredJSONDiscarded` — a row with
     bytes that fail to unmarshal yields `context: {}` for that
     group and emits `gateway.context.invalid_json` once.
   - `TestCallTool_DoesNotInjectContext` — a tool call made with
     `args = {jql: "…"}` reaches the upstream with **exactly**
     `{jql: "…"}`; the upstream's `defaults_json` is **not** merged
     in by the manager.
   - `TestDeriveAliases_AtlassianTwoBrands` — `[jira_search,
jira_get_issue, confluence_get_page]` → `[jira, confluence]`.
   - `TestDeriveAliases_NoPrefixDominates` — `[list_zones, get_zone,
create_ai_gateway]` → `[]`.
   - `TestDeriveAliases_CollisionsDroppedTenantWide` — two upstreams
     both claiming `jira` lose the alias.
   - `TestCodemodeTools_AliasFilterMatchesCanonical` —
     `codemode.tools({upstream: "jira"})` against `atlassian` (alias
     `jira`) returns the Atlassian group with `name: "atlassian"`.
   - `TestCodemodeTools_FilterDropsEmptyGroups` — a `match` filter
     that excludes every tool of an upstream removes the group
     entirely from `result.upstreams`.
   - `TestCodemodeTools_EmptyFilterEmitsHint` — `codemode.tools({upstream:
"jira"})` against a tenant with `[sentry]` returns
     `{upstreams: [], hint: {tried: ["jira"], available: ["sentry"], suggested: []}}`.
   - `TestCodemodeTools_GenuinelyEmptyTenantNoHint` — empty tenant
     gets `{upstreams: [], hint: undefined}`.
   - `TestCodemodeTools_NoFilterReturnsAllGroups` — baseline.
   - `TestCodemodeTools_LimitAppliedAcrossGroups` — `limit: 3` on a
     tenant with two groups of 5 tools each yields a total of 3 tools
     split across groups in iteration order.
   - `TestAliasProxy_CallsResolveSameAsCanonical` — `codemode.jira.search(x)`
     and `codemode.atlassian.jira_search(x)` hit the same dispatch
     path; cap/quota accounting unchanged.
2. `go test ./internal/storage/... -race` — extend RLS tests to
   confirm `context_json` and `defaults_json` are tenant-scoped and
   never readable across tenants.
3. Manual: link an Atlassian upstream via the portal **without** a
   cloudId. Run a codemode script that calls
   `await codemode.atlassian.jira_search({jql: "…"})` _without_
   spreading context. First call may fail with a clear MCP
   error if cloudId is required; the AfterFirstCall hook still
   pins `cloudId` on success. Re-run a script that does
   `const { upstreams } = codemode.tools({upstream:"atlassian"}); const up = upstreams[0]; await codemode.atlassian.jira_search({...up.context, jql:"…"})`
   and confirm the call lands with the autopopulated cloudId.
4. Manual: register an upstream whose canonical name is `atlassian`.
   In codemode call `await codemode.jira.list_projects({})`. Confirm
   it resolves. Call `codemode.tools({upstream: "atlassian"})` and
   `codemode.tools({upstream: "jira"})`; both return a single group
   with `name: "atlassian"` and the same tool list.
5. Manual: call `codemode.tools({upstream: "jira"})` on a tenant with
   only Sentry linked. Confirm the result is
   `{upstreams: [], hint: {tried: ["jira"], available: ["sentry"], suggested: []}}`
   and no crash.
6. Manual (JSON validation): in the admin SPA paste `[1,2,3]` into
   the `defaults_json` editor; confirm Connect-RPC returns
   `invalid_argument` with a field path; SPA highlights the
   offending value. Same for an oversize blob.
7. `golangci-lint run ./...` — no new warnings; the
   `datatypes.JSON` import is already in the module via Phase 8.

## Out of scope

- **Server-side injection of context into tool calls.** Explicitly
  rejected (see "Why visibility, not injection" above). The script
  spreads what it wants; the manager dispatches what the script sent.
- **Admin-configured aliases.** Decision: aliases are derived only.
  Adds a knob users would forget to set; if derivation is wrong the
  fix is to fix the heuristic, not paper over it.
- **Per-tool context overrides** (different cloudId per call without
  passing it as an arg). The model already controls the args; pick a
  different field of `group.context` or just don't spread that key.
- **Deep / recursive merge between `defaults_json` and `context_json`.**
  Shallow only. Nested objects are taken whole from the higher-priority
  source. Simpler rule for the model and for the operator.
- **Cross-upstream context sharing** (e.g. one Atlassian's cloudId
  used by another). Each link has its own blob; sharing is a portal
  UX concern, not a runtime one.
- **Auditing every context read.** `codemode.tool.called` already
  records `args_sha256` and `args_bytes` of the args the script
  actually dispatched — which now equals what the model passed,
  including any spread context values. Sufficient for incident
  response without a separate log line.
- **Encrypting `defaults_json` / `context_json`.** This holds metadata
  the user typed in or that an upstream's public discovery endpoint
  returned. Secrets remain in the existing `ExtraJSON`
  (`crypto.SecretField`) path.

## Checklist

- [x] Add `DefaultsJSON`, `AliasesJSON` to `Upstream`; `ContextJSON` to
      `UpstreamLink`. AutoMigrate verified to apply defaults on
      existing rows; goose backfill added only if needed. _(Note:
      `Aliases` landed as `AliasesJSON []byte` jsonb-array rather
      than `pq.StringArray` to avoid pulling in `lib/pq`; runtime
      shape and semantics are identical.)_
- [x] `MergeContext(upstreamDefaults, linkContext)` +
      `ValidateContextBlob` + `SafeLoadContextBlob` implemented in
      [internal/gateway/context.go](../../internal/gateway/context.go);
      4 KB cap, object-root, JS-ident key shape; pure functions,
      table-driven tests.
- [x] `(*Manager).CallTool` confirmed to **not** merge the context
      blob into args (regression test `TestCallTool_DoesNotInjectContext`
      in
      [internal/gateway/manager_context_test.go](../../internal/gateway/manager_context_test.go)
      stands up a real mcp-go server, captures the outbound args,
      and asserts they equal the script-supplied map exactly).
- [x] Read-time defense in `(*Manager).UpstreamsForUser`: invalid
      stored JSON → `context: {}` + `gateway.context.invalid_json`
      warn (covered by `TestUpstreamsForUser_InvalidStoredJSONDiscarded`).
- [ ] Write-time validation wired into the admin upstream service
      (Phase 9c) and the per-link portal handler (Phase 9b). Connect
      `invalid_argument` errors carry the field path. **Deferred:
      `internal/admin/` and `internal/portal/` packages do not exist
      yet; lands with Phase 9b / 9c. `ValidateContextBlob` is ready
      for them to call.**
- [x] `DeriveAliases` + tenant-wide collision pass
      (`ResolveAliasCollisions`) in
      [internal/upstream/aliases.go](../../internal/upstream/aliases.go);
      wired into `reconcileCatalog` so aliases are recomputed every
      time the tool cache refreshes.
- [~] `AfterFirstCall(link, args, response)` hook on the strategy
      interface; Atlassian concrete hook stashes `cloudId`.
      **Rejected — wrong abstraction.** A `Strategy` is the
      authentication / transport driver (none / static_header /
      mcp_spec); response parsing is a per-vendor concern orthogonal
      to auth (Atlassian, Linear, Sentry can all share `mcp_spec`
      yet need different extractors). The motivating case
      (`cloudId`) is also carried on the OAuth-side
      `/oauth/token/accessible-resources` response, not on any MCP
      tool-call response — so even the hook *timing* is wrong. A
      proper design needs a separate `ContextEnricher` registry
      keyed by an explicit `vendor` field on `Upstream` (or by
      upstream URL pattern), independent of the auth strategy. Until
      a concrete need surfaces, context stays admin-defaulted +
      portal-edited only. See
      [docs/ambient-context.md §7](../ambient-context.md#7-open-question-automatic-context-discovery).
- [x] `codemode.tools()` returns the new envelope
      (`{ upstreams: UpstreamGroup[], hint?: EmptyHint }`); per-tool
      `upstream` field removed; groups carry `name`, `aliases`,
      `context`, `tools`.
- [x] No separate `codemode.upstreams()` verb; no new reserved
      sandbox keys introduced by this phase.
- [x] Aliases registered on the `codemode` sandbox object alongside
      canonical names; canonical name always wins on collision
      (covered by `TestAliasProxy_CallsResolveSameAsCanonical`).
- [x] `codemode.tools()` populates `hint` when the filter was
      non-empty, `upstreams` is empty, and ≥ 1 candidate is found.
- [x] `commonDiscoveryAPI` + execute / search recipes updated to
      teach the new envelope shape, the spread-`group.context`
      pattern, alias filtering, and the `hint` field. Explicit note
      that context is **not** injected.
- [x] Unit tests for merge precedence, validation rejection paths,
      alias derivation, collision drop, grouped catalog shape with
      context, alias filter matching canonical, empty-group drop,
      empty-filter hint, limit-across-groups, no-injection
      regression.
- [ ] RLS test extended to cover `context_json` and `defaults_json`
      cross-tenant isolation. **Columns live on already-RLS-scoped
      tables (`upstreams`, `upstream_links`); cross-tenant isolation
      is structurally inherited. Targeted assertion left for the
      next time `internal/storage/rls_test.go` is touched.**
- [x] Update [docs/codemode.md](../codemode.md) — context visibility
      model, aliases, `codemode.tools()` envelope shape, `hint`
      shape.
- [x] Update [docs/phases/README.md](README.md) index with phase 8c
      and its dependency on phase 8.

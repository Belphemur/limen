# Phase 14 — Upstream tool description normalization (speculative)

**Depends on**: Phase 8 (per-tenant injection — gives the gateway a stable, cached, per-link view of upstream tool catalogs to normalize against).
**Unblocks**: nothing on the critical path. This phase is purely about LLM tool-selection accuracy as catalog size grows.
**Status**: SPECULATIVE / NOT SCHEDULED. Captured for future reference. Do not start without telemetry from Phase 10 confirming the problem this phase solves.

## Goal

Stop passing upstream tool descriptions through to `codemode.tools()`
verbatim. Run a deterministic, configurable normalization pass over
each upstream's tool catalog at link/sync time, store the cleaned
form, and serve THAT to scripts. The original description stays
reachable via `codemode.schemas(name)` for callers that want it.

The thesis (and the only reason this phase exists): once a tenant
links 100+ tools across several MCP upstreams, the LLM's
tool-selection accuracy degrades because upstream-authored
descriptions are written for humans — ambiguous, marketing-flavoured,
inconsistently structured, and often embedding REST metadata in prose
that the LLM has to parse on every selection. Cleaning the
descriptions once at sync time is dramatically cheaper than asking
the model to compensate on every script.

## Why this is "speculative" and not "ready"

We do not currently have telemetry to **measure** tool-selection
accuracy, so we cannot demonstrate that this phase moves the needle.
Shipping it on faith is fine for the cheapest tier (truncation +
boilerplate strip) but risky for anything more ambitious. Do not
start this phase before:

- A `codemode.execute.tool_selection_outcome{accurate=bool}` metric
  exists (portal feedback loop, LLM-as-judge eval, or both).
- That metric, segmented by catalog size, shows degradation worth
  fixing (e.g. accuracy drops below threshold for users with > 50
  linked tools).

If the metric stays flat as catalogs grow, this phase is dead and
should be deleted from the roadmap.

## Background

The current pipeline:

1. Upstream MCP server publishes `tools/list` → mcp-go client receives
   `[]mcp.Tool` with `Name`, `Description`, `InputSchema`.
2. Gateway caches this list per link (`internal/storage` —
   `upstream_tools` table, Phase 8).
3. `Manager.ToolsForUser(ctx)` materialises the per-user union of all
   linked-upstream tools.
4. `internal/gateway/codemode/handler.go` injects these into the Goja
   VM as `codemode.tools()` listings (lean shape, no schema) and
   `codemode.schemas()` (full schema, on demand).

This phase inserts a **normalization step between (2) and (3)**:
either as a one-shot pass when the upstream tool cache is written, or
as a lazy derivation cached on first read. Step (4) is unchanged
except that `ToolListing` gains a few optional structured fields.

## Why upstream descriptions are the bottleneck

Three concrete failure modes observed in real transcripts:

- **Marketing prose** wastes tokens at selection time. A tool whose
  description is 600 chars of API documentation and a one-line
  "what it does" forces the LLM to skim 600 chars to find the
  one useful sentence. Multiplied across hundreds of tools, this
  dominates the catalog token budget.
- **REST metadata in prose.** OpenAPI-generated MCP tools commonly
  embed `GET /accounts/{account_id}/tokens` in the description.
  The LLM has to parse this out of free text every time. It would
  scan structured `httpMethod` / `httpPath` fields much better.
- **Collisions.** Two upstreams (e.g. `github` and `gitlab`) expose
  `search_issues` with near-identical descriptions. The LLM picks
  the wrong one and the user gets results from the wrong system. The
  `upstream` field disambiguates technically but visually it is one
  level removed from the description prose the LLM scans first.

Supporting reading: ["Learning to Rewrite Tool Descriptions for
Reliable LLM-Agent Tool Use" (Guo et al., 2026,
arXiv:2602.20426)](https://arxiv.org/abs/2602.20426) reports
~29% reduction in accuracy degradation and ~61% improvement in
query-level success when descriptions are rewritten at scale. Their
method is LLM-driven (Trace-Free+); this phase deliberately scopes
the v1 to **deterministic rules** so we don't add a new LLM
dependency to the data path.

## Design

### Where the cleaner lives

New package `internal/normalize/` (single-purpose, no dependencies on
the rest of the gateway). Exposes:

```go
type Config struct {
    Enabled               bool
    MaxChars              int
    StripBoilerplate      bool
    ExtractRESTMetadata   bool
    AnnotateVerb          bool   // tier 1 opt-in
    AnnotateCollisions    bool   // tier 2
    AnnotateRequiredArgs  bool   // tier 2
}

type Input struct {
    Name        string
    Description string
    InputSchema map[string]any
}

type Output struct {
    Description   string   // cleaned, capped, annotated
    HTTPMethod    string   // "" if not REST-shaped
    HTTPPath      string
    RequiredArgs  []string // promoted from InputSchema.required
}

func Clean(in Input, cfg Config) Output
```

Pure function. Deterministic. Zero IO. Unit-testable against a
corpus of real upstream descriptions (Atlassian, GitHub, Cloudflare —
we have access via the linked upstreams in dev).

### Caching strategy

Cleaned `Output` is cached alongside the raw cache row in
`upstream_tools`. Cache key includes a hash of the active normalize
`Config` so flipping a feature flag triggers a recompute on next
sync, not on every read. A new column `upstream_tools.cleaned_jsonb`
plus `cleaned_config_hash` is sufficient; no separate table needed.

### Tier 1 — pure rules (ship behind a flag, default off)

The minimum useful version. All rules independently config-gated.

1. **Length cap + sentence-boundary truncation.**
   - Cap at `MaxChars` (default 280).
   - Cut at the nearest `.`/`!`/`?` followed by whitespace.
   - If no boundary found within the cap, hard-cut and append `…`.

2. **Boilerplate strip.** A small, audited regex set:
   - Leading: `"This (?:tool|endpoint|API|method) (?:allows you to |is used to |can be used to |lets you )?"`
   - Marketing tails: `"Powered by\b.*$"`, `"Get started.*$"`, `"For more (info|details|information).*$"`.
   - Markdown noise once the result is a single sentence: stray `**`, `__`, escaped newlines, double spaces.
   - Embedded URLs (`https?://\S+`) — irrelevant at selection time, fetchable via the full description on demand.

3. **REST-metadata extraction.** Regex-match `(GET|POST|PUT|PATCH|DELETE)\s+(/\S+)` anywhere in the description, capture into `HTTPMethod` + `HTTPPath`, remove from the description. Heuristics:
   - Only match if the path looks like an API route (starts with `/`, contains at least one `{...}` placeholder OR matches `/[a-z_-]+` segment pattern).
   - First match wins; ignore subsequent occurrences (they're typically examples).

4. **Verb-prefix inference** (`AnnotateVerb`, off by default).
   - Map common name patterns → canonical verb tag:
     - `^get_|^fetch_|^retrieve_` → `[GET]`
     - `^list_|^search_|^find_|^query_` → `[LIST]`
     - `^create_|^new_|^add_|^post_` → `[CREATE]`
     - `^update_|^patch_|^edit_|^set_` → `[UPDATE]`
     - `^delete_|^remove_|^destroy_` → `[DELETE]`
   - Prepend to the cleaned description. Lets the LLM cluster verbs
     visually when scanning a large catalog.
   - Off by default because some upstream tool names lie (a tool
     called `get_X` that actually creates an X is rare but exists);
     opt-in per tenant.

### Tier 2 — catalog-wide annotation (requires a second pass)

Tier 1 is per-tool. Tier 2 needs the cleaned catalog of one tenant in
hand to detect collisions and add disambiguators.

5. **Collision annotation** (`AnnotateCollisions`).
   - Group tools by `firstNStemmedTokens(cleanedDescription, 4)`.
     Cheap stem (lowercase + strip stopwords + Porter or just suffix
     trim — Porter is overkill for v1).
   - For any group with > 1 tool, append `"(<upstream>)"` to each
     member's cleaned description. The `upstream` field is already
     there separately, but inlining it for collision cases reduces
     mis-selection materially in observed transcripts.

6. **Required-arg highlight** (`AnnotateRequiredArgs`).
   - If `InputSchema.required` has 1–3 entries, append
     `"[requires: arg1, arg2]"` to the cleaned description.
   - Free disambiguator: two `create_token` tools across upstreams
     usually require different IDs, and that fact is in the schema
     we'd otherwise force the LLM to fetch separately.

### Tier 3 — LLM rewrite (defer indefinitely)

The paper's actual method. Per-tool, per-upstream-sync LLM call,
result cached. Expensive, nondeterministic, adds a new failure mode
to the data path. Do not ship this without:

- Strong telemetry showing tier 1 + 2 isn't enough.
- A clear story for which model runs it, who pays for the tokens,
  and how reprocessing happens on prompt changes.
- A fallback: if the rewrite fails or the rewriter is unavailable,
  the gateway must fall back to tier 1+2 output (never the raw
  description, since users with this flag on are explicitly
  rejecting that).

This tier exists in the doc for completeness only. Default
recommendation: don't build it.

### Wire shape

`ToolListing` (currently in
`internal/gateway/codemode/filter.go`) gains optional fields:

```go
type ToolListing struct {
    Name        string `json:"name"`
    Description string `json:"description"` // cleaned when normalize is on
    Upstream    string `json:"upstream"`

    // New, optional, omitempty:
    HTTPMethod   string   `json:"httpMethod,omitempty"`
    HTTPPath     string   `json:"httpPath,omitempty"`
    RequiredArgs []string `json:"requiredArgs,omitempty"`
}
```

`codemode.tools(filter?)` gains matching filter keys so scripts can
do `codemode.tools({httpMethod: "GET", upstream: "cloudflare"})`. This
is the most ergonomic-facing piece of the whole phase; the LLM-side
prompts get updated to advertise it.

`codemode.schemas(name)` continues to return the **raw** description
under a `descriptionFull` field, so anything we strip is recoverable
on demand.

### Config

Top-level `codemode.normalize_upstream_descriptions`:

```yaml
codemode:
  normalize_upstream_descriptions:
    enabled: false # tenant opt-in; flip via admin portal per-tenant
    max_chars: 280
    strip_boilerplate: true
    extract_rest_metadata: true
    annotate_verb: false # opt-in within opt-in
    annotate_collisions: true
    annotate_required_args: true
```

`enabled: false` is identical to today's behaviour. Each sub-flag
gates one rule so a bad rule can be disabled without redeploying.

### Rollout

1. Land telemetry first (`codemode.execute.tool_selection_outcome`
   counter + `codemode.tools.catalog_size` distribution). Without
   this, we can't tell if any of this helped.
2. Implement `internal/normalize` and the `upstream_tools` schema
   migration. Default `enabled: false`. Unit tests against a
   captured corpus of real upstream descriptions.
3. Enable tier 1 for a canary tenant. Measure for two weeks.
4. If accuracy moves: enable tier 1 for all tenants, then start
   tier 2 development. Repeat measurement.
5. If accuracy doesn't move: leave the code in, default-off, and
   delete this phase from the roadmap.

## Non-goals

- **Renaming upstream tools.** The wire-level tool name is part of
  the upstream's API contract — we cannot change it. Only the
  description and the structured-field projection in `ToolListing`
  are in scope.
- **Modifying `inputSchema`.** Same reason. We may _read_ it to
  promote `required` to a `RequiredArgs` annotation, but we never
  alter the schema served back from `codemode.schemas()`.
- **Pre-filtering the catalog.** Reducing the number of tools a
  user sees based on inferred relevance is a different feature
  (catalog gating) and a different threat model (we'd be hiding
  capability from the user). Out of scope.
- **Per-script normalization.** Cleaning runs at upstream sync time,
  not per script invocation. No hot-path cost.

## Risks

- **Over-aggressive cleaning drops useful info.** Mitigation:
  `codemode.schemas(name)` always exposes the raw description, and
  every rule is config-gated. Bad rule → disable it, no redeploy.
- **Regex rules will misbehave on edge-case descriptions.**
  Mitigation: corpus-based unit tests; each rule must keep the
  output strictly shorter and never invent text. We never _add_
  prose, only strip or truncate (verb prefix is a fixed token).
- **Catalog-wide collision pass needs the whole catalog in hand.**
  Mitigation: run it during the per-link sync that already
  materialises the catalog; cache the result. Cost is amortised
  over every subsequent `codemode.tools()` call.
- **Schema migration on the `upstream_tools` cache table.** Standard
  GORM auto-migrate adds nullable columns; backfill is unnecessary
  because the cleaned form is rederivable from the raw form. On
  first read after deploy, recompute and store.

## Checklist

This is intentionally light because the phase is speculative. Fill
in once a real implementation starts.

- [ ] Telemetry pre-req shipped (Phase 10 or earlier extension):
      `tool_selection_outcome` metric exists and is queryable.
- [ ] Baseline measurement collected: accuracy vs. catalog size
      curve, recorded somewhere durable so post-rollout numbers
      can be compared.
- [ ] `internal/normalize` package implemented, `Clean()` pure
      function unit-tested against a corpus of ≥ 50 real upstream
      descriptions across ≥ 3 different MCP servers.
- [ ] `upstream_tools.cleaned_jsonb` + `cleaned_config_hash`
      columns added via the storage migration pipeline.
- [ ] `ToolListing` gains `HTTPMethod`, `HTTPPath`,
      `RequiredArgs` (all `omitempty`); existing JSON consumers
      tolerate the new fields.
- [ ] `codemode.tools(filter)` gains `httpMethod` and
      `requiredArgs` filter keys; `codemode_search` /
      `codemode_execute` prompts updated to advertise them.
- [ ] `codemode.schemas(name)` exposes `descriptionFull` (raw
      upstream description) alongside the cleaned variant.
- [ ] Per-tenant config in admin portal; default off for all
      existing tenants.
- [ ] Canary tenant runs tier 1 for two weeks; tool-selection
      accuracy metric reviewed; decision documented in PR.
- [ ] If shipped tenant-wide: PR description records the before/after
      accuracy numbers so a future maintainer can re-evaluate.

# Ambient Context & Tool Aliases

This document is the single reference for the "magic" Limen does around
Code Mode tool dispatch — the per-upstream **ambient context** blob and
the derived **sub-brand aliases**. It explains the contracts, the
failure modes, and — critically — exactly what the gateway will and
will not silently do to a tool call.

Design history: [phase-08c](phases/phase-08c-ambient-context-and-alias-discovery.md). User-facing API surface: [codemode.md](codemode.md).

---

## TL;DR

| Concern                     | Behavior                                                                 |
| --------------------------- | ------------------------------------------------------------------------ |
| Context blob on a tool call | **NEVER injected.** Args travel verbatim from script to upstream.        |
| Context blob in `tools()`   | **Visible, read-only.** Script chooses whether to spread it.             |
| Model wants to override     | Pass the desired value in args — script args always win.                 |
| Aliases                     | Auto-derived from tool-name prefixes; only when prefix dominates.        |
| Invalid JSON at rest        | Read-time defense returns `{}` and emits `gateway.context.invalid_json`. |

---

## 1. Why this exists

Many MCP upstreams require the agent to repeat the same opaque
identifier on every tool call: Atlassian wants a `cloudId`, Sentry an
`organizationSlug`, Linear a `teamId`, etc. Without help the agent has
to either rediscover the value via a `list_*` call (round-trip cost,
context bloat) or have the user paste it manually somewhere.

Limen surfaces these values on the same `codemode.tools()` envelope the
agent already inspects:

```ts
type UpstreamGroup = {
  name: string;
  aliases: string[];
  context: Record<string, unknown>; // <-- the ambient blob
  tools: { name: string; description: string }[];
};
```

The agent reads `context`, decides what to forward, and writes the
forward explicitly. No hidden state.

---

## 2. The visibility-not-injection rule

> **The gateway never merges `context` into outbound tool arguments.**

This is load-bearing. If we silently spread context into args:

- **Provenance breaks.** The audit log and the script say different
  things; debugging "why did this call carry `defaultProject: FOO`?"
  becomes impossible.
- **Override breaks.** The script cannot reliably override a value it
  cannot see being added.
- **Schemas break.** Many upstreams reject unknown keys; injection
  would silently fail half the tools.

The single regression test that guards this rule lives at
[internal/gateway/manager_context_test.go](../internal/gateway/manager_context_test.go) under
`TestCallTool_DoesNotInjectContext`. It stands up a real mcp-go server,
seeds a fat `DefaultsJSON`, calls `Manager.CallTool` with a sparse args
map, and asserts the captured upstream request equals the sparse map
exactly. **Do not remove this test.**

### The spread idiom

The script chooses to forward context when it wants to:

```js
const { upstreams } = codemode.tools({ upstream: "atlassian" });
const ctx = upstreams[0].context;
await codemode.atlassian.jira_search({ ...ctx, jql: "project = OP" });
```

Spread is left-to-right, so **later keys win**. If the agent needs a
different `cloudId` than the stored one:

```js
await codemode.atlassian.jira_search({
  ...ctx,
  cloudId: "different-cloud-id", // explicit override wins
  jql: "project = OP",
});
```

Or skip the spread entirely. The context blob is a hint, not a contract.

---

## 3. Where context comes from

`context` is a shallow per-key merge of two sources:

```
┌──────────────────────────┐
│ Upstream.DefaultsJSON    │  admin-set; same for every user of the upstream
│ (tenant-wide defaults)   │
└────────────┬─────────────┘
             │ shallow merge, link wins per key
┌────────────▼─────────────┐
│ UpstreamLink.ContextJSON │  per-(user, upstream); user-editable in the portal
│ (per-user overrides)     │
└────────────┬─────────────┘
             │
             ▼
  context: { ...defaults, ...linkOverrides }
```

Implementation: `gateway.MergeContext` in [internal/gateway/context.go](../internal/gateway/context.go).

Both blobs must validate against `gateway.ValidateContextBlob`:

- Top-level JSON object (`{}` allowed; arrays, strings, numbers
  rejected).
- ≤ 4 KB serialized.
- Keys match `^[A-Za-z_$][\w$]*$` so they can survive a JS `...spread`
  without lookup gymnastics.
- Nested values may be any JSON; only the **top level** is shape-checked.

Validation runs on every write path. The read path (`SafeLoadContextBlob`)
is more forgiving — see §5 below.

---

## 4. Aliases (sub-brand proxies)

Real-world upstreams bundle multiple products: Atlassian = Jira +
Confluence + Bitbucket. The catalog reflects this through tool-name
prefixes (`jira_*`, `confluence_*`). Aliases promote those prefixes to
first-class proxy names on the `codemode` object.

### Derivation rule

For each tool name we tokenize on `_`, `-`, and camelCase boundaries
(so `getJiraIssue` → `[get, Jira, Issue]`), then drop a leading
CRUD/lookup verb (`get`, `create`, `update`, `find`, `search`,
`delete`, `add`, `edit`, `list`, `lookup`, `remove`, `set`, `fetch`,
`transition`, `analyze`). The next token (lowercased) is the
candidate prefix. A candidate is promoted when it satisfies **both**:

| Threshold                              | Why                                                                                                  |
| -------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| ≥ 2 tools share it                     | One tool with `Jira` in its name isn't a brand; two is.                                              |
| ≥ 20% of the upstream's tools share it | Rejects verb-heavy CRUD catalogs (e.g. sentry, whose top noun appears in only 17% of tools).         |

This keeps multi-brand upstreams like Atlassian (jira ≈ 23%,
confluence ≈ 28%) producing useful aliases while suppressing the
verb-grouping noise a naive prefix split would emit for `<verb>_<noun>`
catalogs.

Implementation: `upstream.DeriveAliases` in [internal/upstream/aliases.go](../internal/upstream/aliases.go).
The canonical upstream name **always** survives in `aliases` lists for
free; you never have to remember which form to use.

### Reserved keys

Aliases never override the built-in sandbox surface:

```
tools  schemas  call  json  quota
```

Plus any canonical upstream name owned by a different upstream in the
same tenant. A clash drops the alias from **all** claimants — neither
upstream silently steals the other's brand. The canonical names always
win.

### Lookup

Both canonical and alias names resolve through the same dispatcher.
`codemode.atlassian.jira_search(...)` and `codemode.jira.jira_search(...)`
land on the same `(upstream, tool)` pair — verified by
`TestAliasProxy_CallsResolveSameAsCanonical` in
[internal/gateway/codemode/handler_test.go](../internal/gateway/codemode/handler_test.go).

---

## 5. Read-time defense

`Upstream.DefaultsJSON` and `UpstreamLink.ContextJSON` are jsonb columns
— Postgres itself rejects syntactically invalid JSON, so we can't land
unparseable bytes at rest through normal writes. But:

- Schema drift, manual SQL fixes, and partial rollouts can produce
  rows whose contents pass jsonb's syntax check but fail our shape
  check (e.g. a JSON array where we expect an object).
- The portal write path validates, but legacy data may predate the
  validator.

`SafeLoadContextBlob` in [internal/gateway/context.go](../internal/gateway/context.go) treats
**every** non-object payload as `{}` and emits
`gateway.context.invalid_json` with `source` = `defaults_json` /
`context_json`, the row ID, and the byte length. `codemode.tools()`
keeps working; the affected upstream's `context` is just empty until
someone fixes the row.

Regression test: `TestUpstreamsForUser_InvalidStoredJSONDiscarded` in
[internal/gateway/manager_context_test.go](../internal/gateway/manager_context_test.go).

---

## 6. Empty-filter hints

When `codemode.tools(filter)` is called with a non-empty filter that
matches zero tools, the envelope carries a `hint` instead of an empty
`upstreams` array:

```ts
type EmptyHint = {
  tried: string[]; // what the filter asked for
  available: string[]; // every upstream + alias the user can see
  suggested: string[]; // edit-distance-closest entries in available
};
```

This collapses the typical typo-recovery dialog ("did you mean
`atlassian`?") to a single round-trip. The script can render the hint
or just retry with a corrected filter.

Implementation: `gateway.BuildEmptyHint` in [internal/gateway/aliases.go](../internal/gateway/aliases.go).

---

## 7. Open question: automatic context discovery

There is no gateway-side hook today that **derives** context values
from upstream responses. An earlier design proposed an
`AfterFirstCall` hook on the `Strategy` interface that would extract
e.g. an Atlassian `cloudId` from a tool-call response and stash it on
the link. That design was rejected because:

- `Strategy` is the authentication / transport abstraction
  (`none` / `static_header` / `mcp_spec`). Response parsing is a
  per-vendor concern that's orthogonal to auth — Atlassian, Linear and
  Sentry could all share the `mcp_spec` strategy but each need
  different extractors.
- The motivating case (`cloudId`) is actually carried on the OAuth-side
  `/oauth/token/accessible-resources` response, not on any MCP
  tool-call response. The right hook for that data is something like
  `AfterLink` / `AfterTokenRefresh`, not `AfterFirstCall`.

When a concrete need surfaces, the right home is a separate
`ContextEnricher` registry keyed by an explicit `vendor` field on the
`Upstream` row (or by upstream URL pattern), entirely independent of
the auth strategy. Until then, context is admin-defaulted +
portal-edited only.

---

## 8. What this system deliberately does NOT do

- **No call-time merging.** Documented; tested; load-bearing.
- **No global context.** Context is per-(tenant, upstream, user). There is no
  cross-upstream blob.
- **No response-driven autopopulation.** See §7.
- **No alias renaming.** Aliases are derived from tool names, not
  configurable. If you don't want them, name your tools without shared
  prefixes (or accept the alias — it never replaces the canonical
  name).

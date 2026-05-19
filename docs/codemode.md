# Code Mode

Code Mode is Limen's JavaScript sandbox feature that collapses all upstream tool definitions into two meta-tools, achieving a **94-99% reduction** in context window consumption.

## The Problem

In a traditional MCP gateway, every tool schema from every upstream server is proxied to the LLM client upfront. Consider a setup with 4 upstream servers exposing 52 tools total:

| Scenario     |               Tools Exposed |    Token Cost |
| ------------ | --------------------------: | ------------: |
| No Code Mode | 52 tools across 4 upstreams | ~9,400 tokens |
| Code Mode    |                2 meta-tools |   ~600 tokens |

That's roughly **180 tokens per tool definition**, all consumed before the agent has even decided what to do. Code Mode eliminates this waste by deferring tool discovery to runtime.

## How It Works

Code Mode uses a two-phase approach:

### Phase 1: Discovery

The LLM agent calls `codemode_search` with a JavaScript filter function. Inside the Goja sandbox, the script calls `codemode.tools(filter)` to retrieve the per-user catalog grouped by upstream, then narrows it down:

```js
async () => {
  const tools = await codemode.tools();
  return tools.filter((t) => t.name.includes("jira"));
};
```

This returns only the matching tool names and descriptions -- no full input schemas. The agent learns what's available without the token cost of dumping everything.

### Phase 2: Execution

The agent calls `codemode_execute` with JavaScript that invokes the discovered tools directly:

```js
async () => {
  const ticket = await codemode.jira_get_ticket({ id: "PROJ-123" });
  const issues = await codemode.github_search_issues({ q: "bug label:P1" });
  return { ticket, issues };
};
```

Each upstream tool is injected as a native JavaScript function on the `codemode` object. The agent calls them with natural syntax -- no string-based tool name matching, no argument schema parsing.

## Goja Sandbox

Code Mode runs JavaScript inside [Goja](https://github.com/dop251/goja), a pure Go ECMAScript 5.1+ runtime. This choice means:

- **No CGO dependency** -- single binary deployment
- **Complete isolation** -- the sandbox has no access to the host system
- **Deterministic execution** -- no race conditions with the Go runtime

### What IS Available

Only the `codemode` object is injected into the sandbox:

| Method                                       | Description                                                                                              |
| -------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| `codemode.tools(filter?)`                    | Returns `{ upstreams: UpstreamGroup[], hint? }` — catalog grouped by upstream, with aliases and context. |
| `codemode.schemas(names)`                    | Batch-fetch `inputSchema` for the named tools; returns `{ found, missing }`.                             |
| `codemode.call(upstream, name, args)`        | Calls a tool by `(upstream, name)`. Both args required; use for runtime / non-identifier names.          |
| `codemode.<upstream>.<tool>(args)`           | Per-upstream proxy. Sub-brand **aliases** (derived from tool-name prefixes) also work as proxy names.    |
| `codemode.json(result)`                      | Unwraps the first text block of an MCP CallToolResult and JSON-parses it.                                |
| `codemode.quota()`                           | Returns `{ used, max, remaining, deadline_ms }` for the current invocation.                              |

### What Is NOT Available

The sandbox explicitly blocks:

- **Filesystem** -- no `fs`, `os`, or path operations
- **Network** -- no `fetch`, `XMLHttpRequest`, `http`, or sockets
- **Process** -- no `child_process` or `exec`
- **Module loading** -- no `require()`, `import`, or module resolution
- **Timers** -- no `setTimeout`, `setInterval`, `setImmediate`,
  `clearTimeout`, `clearInterval`, `clearImmediate`, or
  `queueMicrotask`. Async control flow is expressed through
  Promises/`await` only.
- **Go runtime** -- no access to host memory or Go objects beyond the injected API

The sandbox starts fresh on every invocation. Nothing persists between calls.

## JS API Reference

### `codemode.tools(filter?)`

Returns an envelope grouping the tools visible to the calling user by upstream. **Schemas are not included** — fetch them on demand with `codemode.schemas`.

```ts
type UpstreamGroup = {
  name:    string;                    // canonical upstream name
  aliases: string[];                  // derived sub-brand names (e.g. ["jira","confluence"] for "atlassian")
  context: Record<string, unknown>;   // merged per-user ambient context — informational only
  tools:   { name: string; description: string }[];
};

type EmptyHint = { tried: string[]; available: string[]; suggested: string[] };

type ToolsResult = { upstreams: UpstreamGroup[]; hint?: EmptyHint };
```

Filter shape:

```ts
type ToolFilter = {
  upstream?:    string | string[];   // matches canonical name OR any alias
  name?:        string | string[];
  description?: string | string[];
  match?:       string | string[];   // → name + " " + description
  allOf?:       string[];
  regex?:       boolean;             // strings become RE2 (ci)
  limit?:       number;              // caps TOTAL tools across all groups
};
```

Fields AND-combine; array values within a field OR-combine. Groups whose tools filter to zero are dropped from `upstreams`. When the filter was non-empty and `upstreams` ends up empty, the envelope carries a `hint` with the closest-matching upstream / alias names — actionable typo recovery in one round-trip.

#### Aliases (sub-brand proxies)

For any upstream whose tool names share a `_` / `-` prefix, the prefix is promoted to an alias automatically. An upstream registered as `atlassian` with tools `jira_search`, `jira_create_issue`, `confluence_get_page` becomes reachable as `codemode.jira.*`, `codemode.confluence.*`, **and** `codemode.atlassian.*`. The catalog reports the canonical name on every group and the derived names in `aliases`.

Aliases never override the canonical name and never collide with reserved sandbox keys (`tools`, `schemas`, `call`, `json`, `quota`). Tenant-wide collisions (two upstreams both claiming `jira`) drop the alias from all claimants; canonical names always survive.

#### Per-upstream context (visibility, not injection)

Each group's `context` is a shallow merge of the upstream's admin-set defaults with the calling user's link-specific overrides (link wins per key). It carries stable metadata such as a Jira `cloudId`, a Sentry `organizationSlug`, or a default project — values the gateway already knows so the model does not have to rediscover them.

**The gateway does NOT inject this blob into tool calls.** The script is the source of truth for what gets sent; logs match the call exactly. Spread the values explicitly when you want them:

```js
const { upstreams } = codemode.tools({ upstream: "atlassian" });
const up = upstreams[0];
await codemode.atlassian.jira_search({ ...up.context, jql: "project = OP" });
```

> Full reference — derivation rules, override semantics, alias collision handling, failure modes — lives in [ambient-context.md](ambient-context.md). Read that doc before changing anything about `context` or aliases.

### `codemode.schemas(names)`

Batch-fetch `inputSchema` for the tools you actually intend to call. Single string or array; prefer the array form.

```js
const { found, missing } = codemode.schemas(["jira_search", "github_get_pr"]);
// found:  [{ name, upstream, inputSchema }]
// missing: ["typo_tool"]
```

### `codemode.call(upstream, name, args)`

String-keyed escape hatch — both arguments are required. Use it when an upstream or tool name is not a valid JS identifier (`-`, `.`, leading digit) or when you choose the target at runtime:

```js
const result = await codemode.call("github", "search_issues", {
  q: "is:open",
  repo: "owner/repo",
});
```

### `codemode.<upstream>.<tool>(args)` — Per-Upstream Proxy

Every upstream is registered on `codemode` under its canonical name AND every derived alias. Bracket notation works for non-identifier upstream / tool names:

```js
await codemode.github.search_issues({ q: "is:open", repo: "owner/repo" });
await codemode.jira.jira_search({ jql: "project = OP" });          // alias of atlassian
await codemode["my-upstream"]["weird.tool"]({ ... });               // bracket notation
```

There is no flat `codemode.<tool>` namespace — two upstreams can expose the same tool name (e.g. `github` and `gitlab` both expose `search_issues`), so dispatch is always `(upstream, tool)`.

## Async semantics & concurrency

Tool calls inside the sandbox are **genuinely asynchronous**. Each
proxy returns a real JavaScript `Promise` that settles when the
upstream call completes, so the following patterns all behave the
way they would in Node:

- `await codemode.foo({...})` — single sequential call.
- `Promise.all([codemode.a({...}), codemode.b({...})])` — fan-out;
  both upstream calls run **in parallel** rather than serially.
- `try { await ... } catch (e) { ... }` — upstream errors reject
  the returned Promise as a normal `Error` (`tool "foo" failed:
  …`) and can be caught with standard JS error handling.

The runtime uses a Node-style event loop, so microtasks
(`Promise.resolve().then(...)`) run in FIFO order between
synchronous turns of the script.

### Quotas

Two independent limits protect the gateway:

| Limit                      | Config key                          | Default | Meaning                                                                                                                                                                       |
| -------------------------- | ----------------------------------- | ------: | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Total tool calls           | `codemode.max_tool_calls`           |      50 | Hard cap on the number of tool invocations a single script may issue. Tripping this aborts the script with an **uncatchable** error — JS `try/catch` cannot swallow it.       |
| Concurrent tool calls      | `codemode.max_concurrent_tool_calls`|       8 | Maximum number of in-flight upstream calls at any one time. Excess `Promise.all` fan-out is queued; the script still sees Promises, but the upstream dispatch is rate-bounded. |
| Wall-clock script timeout  | `codemode.script_timeout`           |     30s | If the script (including all awaited tool calls) does not finish in time, in-flight workers are cancelled and the script returns a timeout error.                             |

The two count quotas interact predictably: `max_tool_calls` is the
**budget** for the whole script, `max_concurrent_tool_calls` is the
**width** of parallelism allowed at any instant.

## Code Examples

### Filter Tools by Name

```js
async () => {
  const { upstreams } = codemode.tools({ name: "search" });
  return upstreams.flatMap((g) => g.tools.map((t) => ({ ...t, upstream: g.name })));
};
```

### Filter Tools by Upstream (canonical or alias)

The `upstream` filter resolves aliases automatically, so either name works:

```js
async () => {
  const { upstreams, hint } = codemode.tools({ upstream: "jira" });
  if (hint) return { empty: true, hint };           // typo recovery in one round-trip
  return upstreams;
};
```

### Inspect Input Schemas

Fetch schemas on demand for tools you're about to invoke:

```js
async () => {
  const { upstreams } = codemode.tools({ upstream: "github" });
  const names = upstreams.flatMap((g) => g.tools.map((t) => t.name));
  const { found, missing } = codemode.schemas(names);
  return { found, missing };
};
```

### Chaining Tool Calls

Compose multiple tool calls in a single execution:

```js
async () => {
  // Step 1: Find open issues
  const issues = codemode.json(await codemode.github.search_issues({
    q: "is:open label:bug",
  }));

  // Step 2: For each issue, fetch the linked Jira ticket
  const results = await Promise.all(
    issues.map(async (issue) => {
      const jiraId = extractJiraId(issue.body);
      if (jiraId) {
        return codemode.json(await codemode.jira.get_ticket({ id: jiraId }));
      }
      return null;
    }),
  );

  return results.filter(Boolean);
};
```

### Error Handling

Use standard JavaScript `try/catch` to handle tool failures gracefully:

```js
async () => {
  try {
    const ticket = await codemode.jira.get_ticket({ id: "PROJ-999" });
    return { status: "ok", ticket };
  } catch (err) {
    return { status: "error", message: err.message };
  }
};
```

### Composing Results from Multiple Upstreams

Aggregate data from different systems in a single execution:

```js
async () => {
  const [githubPRs, jiraTickets] = await Promise.all([
    codemode.github.list_pull_requests({ state: "open" }),
    codemode.jira.search({ jql: "status = 'In Progress'" }),
  ]);

  return {
    github: githubPRs.length,
    jira: jiraTickets.length,
    combined: mergeByLinkId(githubPRs, jiraTickets),
  };
};
```

## Limitations

| Limitation              | Impact                                                                           |
| ----------------------- | -------------------------------------------------------------------------------- |
| Fresh VM per invocation | No state persistence between `codemode_search` and `codemode_execute` calls      |
| Execution timeout       | JS execution is interrupted after the configured timeout (default 30s)           |
| Memory cap              | JS heap is limited to a configurable size (default 64 MB)                        |
| No shared context       | Each call starts with zero state; pass data between phases via the LLM           |
| ECMAScript 5.1+         | Modern JS features (async/await is supported, but newer ES proposals may not be) |

The sandbox is intentionally narrow. It exists to orchestrate tool calls, not to run general-purpose code. If you need stateful computation, perform it in the LLM's context between discovery and execution phases.

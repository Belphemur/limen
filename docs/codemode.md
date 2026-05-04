# Code Mode

Code Mode is Limen's JavaScript sandbox feature that collapses all upstream tool definitions into two meta-tools, achieving a **94-99% reduction** in context window consumption.

## The Problem

In a traditional MCP gateway, every tool schema from every upstream server is proxied to the LLM client upfront. Consider a setup with 4 upstream servers exposing 52 tools total:

| Scenario | Tools Exposed | Token Cost |
|----------|--------------:|-----------:|
| No Code Mode | 52 tools across 4 upstreams | ~9,400 tokens |
| Code Mode | 2 meta-tools | ~600 tokens |

That's roughly **180 tokens per tool definition**, all consumed before the agent has even decided what to do. Code Mode eliminates this waste by deferring tool discovery to runtime.

## How It Works

Code Mode uses a two-phase approach:

### Phase 1: Discovery

The LLM agent calls `codemode_search` with a JavaScript filter function. Inside the Goja sandbox, the script calls `codemode.tools()` to get all available tools, then filters them down to a relevant subset:

```js
async () => {
  const tools = await codemode.tools();
  return tools.filter(t => t.name.includes("jira"));
}
```

This returns only the matching tool names and descriptions -- no full input schemas. The agent learns what's available without the token cost of dumping everything.

### Phase 2: Execution

The agent calls `codemode_execute` with JavaScript that invokes the discovered tools directly:

```js
async () => {
  const ticket = await codemode.jira_get_ticket({ id: "PROJ-123" });
  const issues = await codemode.github_search_issues({ q: "bug label:P1" });
  return { ticket, issues };
}
```

Each upstream tool is injected as a native JavaScript function on the `codemode` object. The agent calls them with natural syntax -- no string-based tool name matching, no argument schema parsing.

## Goja Sandbox

Code Mode runs JavaScript inside [Goja](https://github.com/dop251/goja), a pure Go ECMAScript 5.1+ runtime. This choice means:

- **No CGO dependency** -- single binary deployment
- **Complete isolation** -- the sandbox has no access to the host system
- **Deterministic execution** -- no race conditions with the Go runtime

### What IS Available

Only the `codemode` object is injected into the sandbox:

| Method | Description |
|--------|-------------|
| `codemode.tools()` | Returns `[{name, description, inputSchema}]` for all upstream tools |
| `codemode.call(toolName, args)` | Calls a tool by name string with the given arguments |
| `codemode.toolName(args)` | Direct proxy -- each tool is a method on `codemode` |

### What Is NOT Available

The sandbox explicitly blocks:

- **Filesystem** -- no `fs`, `os`, or path operations
- **Network** -- no `fetch`, `XMLHttpRequest`, `http`, or sockets
- **Process** -- no `child_process` or `exec`
- **Module loading** -- no `require()`, `import`, or module resolution
- **Go runtime** -- no access to host memory or Go objects beyond the injected API

The sandbox starts fresh on every invocation. Nothing persists between calls.

## JS API Reference

### `codemode.tools()`

Returns an array of all available tools across all upstreams:

```js
const tools = await codemode.tools();
// [
//   { name: "github_search_issues", description: "...", inputSchema: { ... } },
//   { name: "jira_get_ticket", description: "...", inputSchema: { ... } },
//   ...
// ]
```

### `codemode.call(toolName, args)`

Invokes a tool by its name string. Useful for dynamic tool selection:

```js
const result = await codemode.call("github_search_issues", { q: "is:open", repo: "owner/repo" });
```

### `codemode.toolName(args)` -- Per-Tool Proxy

Every discovered tool is available as a direct method on the `codemode` object:

```js
const result = await codemode.github_search_issues({ q: "is:open", repo: "owner/repo" });
```

This is equivalent to `codemode.call("github_search_issues", args)` but with cleaner syntax.

## Code Examples

### Filter Tools by Name

```js
async () => {
  const tools = await codemode.tools();
  return tools.filter(t => t.name.startsWith("github_"));
}
```

### Filter Tools by Upstream

Tool names typically include an upstream prefix. Filter by that prefix:

```js
async () => {
  const tools = await codemode.tools();
  return tools.filter(t => t.name.startsWith("jira_"));
}
```

### Filter Tools by Parameter

Inspect input schemas to find tools that accept specific parameters:

```js
async () => {
  const tools = await codemode.tools();
  return tools.filter(t =>
    t.inputSchema.properties && "repository" in t.inputSchema.properties
  );
}
```

### Chaining Tool Calls

Compose multiple tool calls in a single execution:

```js
async () => {
  // Step 1: Find open issues
  const issues = await codemode.github_search_issues({ q: "is:open label:bug" });

  // Step 2: For each issue, fetch the linked Jira ticket
  const results = await Promise.all(
    issues.map(async (issue) => {
      const jiraId = extractJiraId(issue.body);
      if (jiraId) {
        return await codemode.jira_get_ticket({ id: jiraId });
      }
      return null;
    })
  );

  return results.filter(Boolean);
}
```

### Error Handling

Use standard JavaScript `try/catch` to handle tool failures gracefully:

```js
async () => {
  try {
    const ticket = await codemode.jira_get_ticket({ id: "PROJ-999" });
    return { status: "ok", ticket };
  } catch (err) {
    return { status: "error", message: err.message };
  }
}
```

### Composing Results from Multiple Upstreams

Aggregate data from different systems in a single execution:

```js
async () => {
  const [githubPRs, jiraTickets] = await Promise.all([
    codemode.github_list_pull_requests({ state: "open" }),
    codemode.jira_search({ jql: "status = 'In Progress'" }),
  ]);

  return {
    github: githubPRs.length,
    jira: jiraTickets.length,
    combined: mergeByLinkId(githubPRs, jiraTickets),
  };
}
```

## Limitations

| Limitation | Impact |
|------------|--------|
| Fresh VM per invocation | No state persistence between `codemode_search` and `codemode_execute` calls |
| Execution timeout | JS execution is interrupted after the configured timeout (default 30s) |
| Memory cap | JS heap is limited to a configurable size (default 64 MB) |
| No shared context | Each call starts with zero state; pass data between phases via the LLM |
| ECMAScript 5.1+ | Modern JS features (async/await is supported, but newer ES proposals may not be) |

The sandbox is intentionally narrow. It exists to orchestrate tool calls, not to run general-purpose code. If you need stateful computation, perform it in the LLM's context between discovery and execution phases.

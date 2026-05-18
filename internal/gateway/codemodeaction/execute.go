package codemodeaction

// Execute is the dispatch tool: exposes the full sandbox API including
// per-(upstream, tool) proxies and codemode.call(). Subject to a
// per-invocation tool-call quota and a wall-clock script timeout.
var Execute = Definition{
	Name:               "codemode_execute",
	Description:        executeDescription,
	CodeArgDescription: executeCodeArgDescription,
}

const executeOpener = `Compose an ENTIRE multi-step workflow in ONE call.

codemode_execute runs JavaScript that invokes upstream tools, composes,
branches, loops, handles errors, and returns a single JSON-serializable
value. Prefer one rich script that chains discovery → filtering →
tool calls → result composition over multiple round-trips. The same
codemode.tools() and codemode.schemas() bindings that codemode_search
exposes are available here — most workflows can skip codemode_search
entirely.

Tools you invoke are dispatched with YOUR identity. The gateway injects
per-user auth headers, records link health, and applies the upstream's
resilience policy (timeout, retry, circuit breaker). You never see or
handle credentials.

=============================================================================
WHEN TO USE EACH TOOL
=============================================================================

  You already know the tool name and arg shape
      → call codemode_execute directly.
  You know the upstream / domain but not the exact tool
      → codemode_execute, with codemode.tools({upstream: "..."}) inline.
  You don't know which upstreams are even linked
      → codemode_search ONCE with a broad filter, then codemode_execute.
  Never: codemode_execute, then codemode_execute, then codemode_execute …
         for what is logically ONE workflow.

=============================================================================
ANTI-PATTERNS
=============================================================================

  BAD:  codemode.tools()                          // dumps the whole catalog
  GOOD: codemode.tools({upstream: "jira"})        // filter when you know it

  BAD:  Three codemode_execute calls chained externally by the agent.
  GOOD: ONE codemode_execute whose script chains all the calls inside.

  BAD:  await codemode.x.a(); await codemode.x.b(); await codemode.x.c();
        for (each thing in long list) { await codemode.x.get(thing); }
  GOOD: Bound the loop with codemode.quota().remaining; bail before
        you hit the cap; design for ~50 calls per invocation.

  BAD:  Parsing r?.content?.[0]?.text + JSON.parse by hand.
  GOOD: codemode.json(await codemode.x.y(args))   // free helper

`

const executeInputErrorSuffix = `
  5. Throwing or rejecting yields IsError=true with text
     "execute failed: <message>".

`

const executeRuntimeSuffix = `

`

const executeQuotasBlock = `=============================================================================
QUOTAS — think of these as a BUDGET, not a wall
=============================================================================

You get roughly:
  • ~30s of wall-clock time (the SCRIPT TIMEOUT)
  • ~50 tool calls per invocation (the TOOL-CALL QUOTA)

Design the script to fit comfortably:
  • Bound your loops up front.
  • Avoid huge Promise.all fan-outs (and remember: no parallelism yet
    even with Promise.all — see CONCURRENCY note below).
  • Prefer a small number of richer calls over many tiny ones.
  • Call codemode.quota() inside a loop to bail before you run out:
        if (codemode.quota().remaining <= 1) break;

Hard failures:
  • Timeout: VM is interrupted, returns IsError=true with text
    containing "script timeout".
  • Tool-call quota exceeded: uncatchable interrupt — try/catch will
    NOT swallow it; the script aborts with "max_tool_calls exceeded".

`

const executeAPIHeader = `=============================================================================
SANDBOX API — full surface
=============================================================================

`

const executeDispatchAPI = `

  await codemode.<upstream>.<toolName>(args)
    Direct, namespaced call. '<upstream>' is the EXACT 'upstream' from
    the catalog; '<toolName>' is the EXACT 'name'. 'args' is a plain
    object matching the tool's inputSchema (fetch it via codemode.schemas
    if unsure). Counts as 1 tool call.

    Namespacing is mandatory: two upstreams may legitimately expose the
    same tool name (e.g. both a 'github' and a 'gitlab' upstream expose
    'search_issues'), so there is NO flat 'codemode.<toolName>' shortcut.

    Names not valid as JS identifiers (contain '-', '.', start with a
    digit, ...) CANNOT be reached as a property chain — use
    codemode.call() or bracket notation
    (codemode["my-upstream"]["some-tool"](args)).

    Returns the upstream's MCP CallToolResult; codemode.json(...) unwraps
    the standard text-block shape in one step.

  await codemode.call(upstream, name, args)
    String-keyed equivalent for runtime-computed or non-identifier
    names. Both arguments REQUIRED. Counts as 1 tool call.

  NOTE ON CONCURRENCY: tool dispatches currently execute sequentially
  even inside Promise.all([...]) — there is no JS event loop. Real
  parallelism is a planned follow-up; until then, do not expect N
  concurrent calls to finish in max(latency).

  RESERVED KEYS: 'codemode.tools', 'codemode.schemas', 'codemode.call',
  'codemode.json', and 'codemode.quota' are the sandbox API. Upstreams
  literally named "tools", "schemas", "call", "json", or "quota" cannot
  be reached as a property — use codemode.call("tools", "<toolName>",
  args).

`

const executeErrorShape = `=============================================================================
ERROR SHAPE FROM TOOL CALLS
=============================================================================

A failed tool call REJECTS with an Error whose 'message' contains a kind
tag (case-insensitive substring match):

  "needs_relink"          — user must re-link the upstream in the portal.
  "no_link"               — user has no link to this upstream.
  "auto_disabled"         — link auto-disabled by the gateway.
  "upstream_unavailable" /
  "circuit ... open"      — resilience layer shedding load; may retry later.
  "401" / "403"           — auth rejected even after forced refresh.
  "5xx" / "network"       — upstream broken or unreachable.

Wrap individual calls in try/catch to recover; otherwise the rejection
bubbles up and the whole script fails with IsError=true.

`

const executeRecipes = `=============================================================================
RECIPES — copy and adapt (composed pipelines FIRST; trivial passthroughs
last). Lead with one that DOES MORE than a single call — that is the
whole point of codemode_execute.
=============================================================================

(1) Discover + call in ONE round-trip (skip codemode_search entirely):

    async () => {
      const tools = codemode.tools({ upstream: "jira", match: "ticket" });
      const { found } = codemode.schemas(tools.map(t => t.name));
      const get = found.find(s => s.name.includes("get"));
      return codemode.json(await codemode.call(get.upstream, get.name, { id: "PROJ-123" }));
    }

(2) Compose two tools across different upstreams:

    async () => {
      const ticket = codemode.json(await codemode.jira.get_ticket({ id: "PROJ-123" }));
      const pr     = codemode.json(await codemode.github.get_pr({ number: 456 }));
      await codemode.jira.update_ticket({
        id: "PROJ-123",
        description: "Linked PR title: " + (pr.title ?? "unknown"),
      });
      return { ticket, pr, updated: "PROJ-123" };
    }

(3) Bounded fan-out tolerating per-call failure, self-bounded by quota:

    async () => {
      const ids = ["PROJ-1", "PROJ-2", "PROJ-3"];   // keep small — quota!
      const out = [];
      for (const id of ids) {
        if (codemode.quota().remaining <= 1) break;   // leave headroom
        try {
          out.push({ id, ok: true, ticket: codemode.json(await codemode.jira.get_ticket({ id })) });
        } catch (e) {
          out.push({ id, ok: false, error: String(e?.message || e) });
        }
      }
      return out;
    }

(4) Dedupe before creating — search upstream for an existing record first:

    async () => {
      const existing = codemode.json(await codemode.jira.search({ jql: 'summary ~ "lock timeout"' }));
      if (existing?.issues?.length) {
        return { skipped: true, existing: existing.issues[0].key };
      }
      const created = codemode.json(await codemode.jira.create_issue({
        project: "LM", summary: "Lock timeout", type: "Bug",
      }));
      return { skipped: false, created: created.key };
    }

(5) Surface needs-relink cleanly to the user:

    async () => {
      try {
        return codemode.json(await codemode.jira.get_ticket({ id: "PROJ-123" }));
      } catch (e) {
        const msg = String(e?.message || e).toLowerCase();
        if (msg.includes("needs_relink") || msg.includes("no_link")) {
          return { error: "please re-link Jira in the portal", detail: String(e) };
        }
        throw e;
      }
    }

(6) Dynamic / non-identifier names:

    async () => codemode.call("github-enterprise", "list-repos", { org: "myorg" })

(7) Parallel-looking fan-out (currently serialized — see CONCURRENCY note):

    async () => {
      const [ticket, pr] = await Promise.all([
        codemode.jira.get_ticket({ id: "PROJ-123" }),
        codemode.github.get_pr({ number: 456 }),
      ]);
      return { ticket: codemode.json(ticket), pr: codemode.json(pr) };
    }

(8) Trivial single-tool passthrough — last because if this is all you
    needed, you probably should have called the upstream tool directly:

    async () => codemode.json(await codemode.jira.get_ticket({ id: "PROJ-123" }))`

const executeDescription = executeOpener +
	commonInputContract + executeInputErrorSuffix +
	commonSandboxRuntime + executeRuntimeSuffix +
	executeQuotasBlock +
	executeAPIHeader +
	commonDiscoveryAPI + "\n\n" +
	commonHelpers +
	executeDispatchAPI +
	executeErrorShape +
	executeRecipes

const executeCodeArgDescription = `Compose ONE pipeline; don't split a workflow across multiple calls. ` +
	commonArgContractShort +
	` Invoke upstream tools via 'await codemode.<upstream>.<toolName>(args)' (when both are valid JS identifiers) or 'await codemode.call(upstream, name, args)' (for runtime-computed or non-identifier names). ` +
	`Filter codemode.tools(filter) by upstream or keyword — never dump the catalog. Batch codemode.schemas([...]). ` +
	`codemode.json(result) unwraps the MCP CallToolResult text block (returns { raw } on parse failure). codemode.quota() returns { used, max, remaining, deadline_ms } — bound loops with it. ` +
	`Tools are upstream-namespaced: there is NO flat 'codemode.<toolName>' shortcut. Tool dispatches currently execute sequentially even inside Promise.all (no event loop yet). ` +
	`Subject to a ~30s wall-clock timeout AND a ~50 tool-call quota; quota exhaustion raises an uncatchable interrupt — treat both as a budget and bail early via codemode.quota().remaining.`

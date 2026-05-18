package codemodeaction

// Execute is the dispatch tool: exposes the full sandbox API including
// per-(upstream, tool) proxies and codemode.call(). Subject to a
// per-invocation tool-call quota and a wall-clock script timeout.
var Execute = Definition{
	Name:               "codemode_execute",
	Description:        executeDescription,
	CodeArgDescription: executeCodeArgDescription,
}

const executeDescription = `Run JavaScript that CALLS one or more MCP tools and returns a composed result.

You write an async arrow function that invokes upstream tools via
'codemode.<upstream>.<toolName>(args)' (or 'codemode.call(upstream, name, args)'),
composes / branches / loops / handles errors, and returns a single
JSON-serializable value. The runtime evaluates your function, awaits the
promise, JSON-encodes the resolved value, and returns that JSON string as
the tool's text result. There is no streaming, no incremental output.

Tools you invoke are dispatched with YOUR identity. The gateway injects
per-user auth headers, records link health, and applies the upstream's
resilience policy (timeout, retry, circuit breaker). You never see or
handle credentials.

=============================================================================
INPUT CONTRACT — the 'code' argument MUST be ALL of the following:
=============================================================================

  1. A SINGLE JavaScript expression that evaluates to a zero-argument async
     arrow function: async () => { ... }
  2. NOT a function declaration, NOT a script of top-level statements,
     NOT a class, NOT an IIFE wrapper.
  3. The function takes NO parameters; hard-code any inputs in the body.
  4. The function MUST eventually return a JSON-serializable value
     (object, array, string, finite number, boolean, null). 'undefined'
     becomes null.
  5. Throwing or rejecting yields IsError=true with text
     "execute failed: <message>".

=============================================================================
SANDBOX RUNTIME
=============================================================================

Modern JavaScript (ES2015+). NO filesystem, NO network (fetch/XHR/WebSocket
— reach the outside world ONLY via codemode.*), NO process/env, NO eval/
Function-constructor, NO timers, NO console, NO DOM, NO Node-isms. The ONLY
non-standard global is 'codemode'. No shared state across invocations.

=============================================================================
QUOTAS — both abort the script
=============================================================================

  1. SCRIPT TIMEOUT (wall-clock, ~30s). On timeout the VM is interrupted
     and the tool returns IsError=true with text containing
     "script timeout".

  2. TOOL-CALL QUOTA (~50 calls per invocation). Every codemode.<upstream>.<tool>(...)
     or codemode.call(...) counts as ONE. Exceeding raises an uncatchable
     interrupt — try/catch will NOT swallow it, the script aborts with
     "max_tool_calls exceeded".

  Plan accordingly: bound your loops, avoid huge Promise.all fan-outs,
  prefer a small number of richer calls over many tiny ones. Call
  codemode.quota() if you need to know your remaining budget at runtime.

=============================================================================
SANDBOX API — full surface
=============================================================================

  codemode.tools(filter?)         // SEE codemode_search for shape + filter.
  codemode.schemas(name|names[])  // Returns { found: ToolSchema[], missing: string[] }.
  codemode.json(result)           // Extract + JSON.parse the first text block
                                  // of an MCP tool result (the standard
                                  // [{type:"text", text:"..."}] shape). On
                                  // parse failure returns { raw: "<text>" }.
                                  // Passes non-array values through unchanged.
  codemode.quota()                // Returns { used, max, remaining, deadline_ms }.
                                  // Free; use to self-bound long loops.
    None of the four count against the tool-call quota. Use them at the
    top of your script when you need to confirm a tool's argument shape
    or to size a paginated walk.

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

    Returns the upstream's MCP "content" array:
        [ { type: "text", text: "..." }, ... ]
    Most tools return a single text block whose text is either a plain
    string or JSON; the codemode.json() helper above unpacks both
    cases in one step:
        const data = codemode.json(await codemode.github.search_issues({...}));

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

=============================================================================
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

=============================================================================
RECIPES — copy and adapt
=============================================================================

(1) Single tool call, raw passthrough:

    async () => codemode.jira.get_ticket({ id: "PROJ-123" })

(2) Single call, parse the JSON text result with the codemode.json helper:

    async () => codemode.json(await codemode.jira.get_ticket({ id: "PROJ-123" }))

(3) Discover + call in one round-trip (skip codemode_search entirely):

    async () => {
      const [t] = codemode.tools({ match: "get_ticket" });
      if (!t) return { error: "no get_ticket tool available" };
      const { found } = codemode.schemas(t.name);   // confirm arg shape
      return codemode.json(await codemode.call(t.upstream, t.name, { id: "PROJ-123" }));
    }

(4) Compose two tools across different upstreams:

    async () => {
      const ticket = await codemode.jira.get_ticket({ id: "PROJ-123" });
      const pr     = await codemode.github.get_pr({ number: 456 });
      await codemode.jira.update_ticket({
        id: "PROJ-123",
        description: "Linked PR: " + (pr?.[0]?.text ?? "unknown"),
      });
      return { updated: "PROJ-123" };
    }

(5) Bounded fan-out tolerating per-call failure:

    async () => {
      const ids = ["PROJ-1", "PROJ-2", "PROJ-3"];   // keep small — quota!
      const out = [];
      for (const id of ids) {
        if (codemode.quota().remaining <= 1) break;   // leave headroom
        try {
          out.push({ id, ok: true, ticket: await codemode.jira.get_ticket({ id }) });
        } catch (e) {
          out.push({ id, ok: false, error: String(e?.message || e) });
        }
      }
      return out;
    }

(6) Parallel-looking fan-out (currently serialized — see CONCURRENCY note):

    async () => {
      const [ticket, pr] = await Promise.all([
        codemode.jira.get_ticket({ id: "PROJ-123" }),
        codemode.github.get_pr({ number: 456 }),
      ]);
      return { ticket, pr };
    }

(7) Dynamic / non-identifier names:

    async () => codemode.call("github-enterprise", "list-repos", { org: "myorg" })

(8) Surface needs-relink cleanly to the user:

    async () => {
      try {
        return await codemode.jira.get_ticket({ id: "PROJ-123" });
      } catch (e) {
        const msg = String(e?.message || e).toLowerCase();
        if (msg.includes("needs_relink") || msg.includes("no_link")) {
          return { error: "please re-link Jira in the portal", detail: String(e) };
        }
        throw e;
      }
    }

=============================================================================
WORKFLOW
=============================================================================

If you already know the tool name and arg shape, call codemode_execute
directly. Otherwise either run codemode_search first or inline
codemode.tools(filter) + codemode.schemas(names) at the top of your script.
Either way, return ONE JSON-serializable value at the end.`

const executeCodeArgDescription = `A single JavaScript expression that evaluates to a zero-argument async arrow function: async () => { ... }. Invoked once, its promise awaited, its resolved value JSON-encoded and returned to you. Invoke upstream tools via 'await codemode.<upstream>.<toolName>(args)' (when both are valid JS identifiers) or 'await codemode.call(upstream, name, args)' (for runtime-computed or non-identifier names). Discover with codemode.tools(filter?) and fetch input schemas with codemode.schemas(name|names[]) which returns { found, missing } — both are free (no quota). codemode.json(result) extracts and JSON.parses the first text block of an MCP content array, returning { raw: "..." } on parse failure. codemode.quota() returns { used, max, remaining, deadline_ms } so loops can self-bound. Tools are upstream-namespaced: there is NO flat 'codemode.<toolName>' shortcut because the same tool name may exist on multiple upstreams. Tool dispatches currently execute sequentially even inside Promise.all (no event loop yet). Must return a JSON-serializable value. Subject to a ~30s wall-clock timeout AND a ~50 tool-call quota; quota exhaustion raises an uncatchable interrupt. Sandbox has no filesystem, no network (except codemode.*), no eval, no require, no timers, no console — only standard ES2015+ JavaScript plus the codemode global.`

package codemodeaction

// Search is the discovery tool: read-only, exposes codemode.tools() and
// codemode.schemas() only. Use it to surface the per-user catalog and
// fetch input schemas for tools the LLM is about to invoke.
var Search = Definition{
	Name:               "codemode_search",
	Description:        searchDescription,
	CodeArgDescription: searchCodeArgDescription,
}

const searchDescription = `Discover MCP tools available to you. Two-step: scan the LEAN catalog with codemode.tools(filter?), then pull schemas for the few tools you actually need with codemode.schemas(names). Schemas are NOT in the catalog — fetching them lazily keeps your context small even when hundreds of tools are linked.

You provide a JavaScript async arrow function. The runtime evaluates it,
awaits the returned promise, JSON-encodes the resolved value, and returns
that JSON string as the tool result.

codemode_search is READ-ONLY: it cannot invoke upstream tools. Use
'codemode_execute' for that. The same codemode.tools() and codemode.schemas()
bindings are also available inside codemode_execute, so you can discover and
call in a single round-trip when convenient.

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
     becomes null. Functions/symbols/BigInt/Map/Set/Date/circular refs
     are NOT serializable — convert them yourself.
  5. Throwing or rejecting yields IsError=true with text
     "search failed: <message>".

=============================================================================
SANDBOX RUNTIME
=============================================================================

Modern JavaScript (ES2015+: let/const, arrow functions, async/await,
destructuring, spread, Promises, JSON, Math, Array, String, Number, RegExp,
Object, Map, Set, ...).

NO filesystem, NO network (fetch/XHR/WebSocket), NO process/env, NO eval/
Function-constructor, NO timers, NO console, NO DOM, NO Node-isms (Buffer/
require/__dirname). The ONLY non-standard global is 'codemode'.

A wall-clock script timeout (typically ~30s) aborts the VM and returns
IsError=true with text containing "script timeout".

=============================================================================
SANDBOX API — codemode_search exposes ONLY the discovery surface
=============================================================================

  codemode.tools(filter?)
    Returns Array<ToolListing> of tools visible to YOU (your tenant,
    your linked upstreams, link health enforced). LEAN shape — no
    schema, cheap to scan even with hundreds of tools.

    ToolListing = {
      "name":        string,   // pass to codemode.schemas / codemode_execute
      "description": string,   // free-form, may be empty
      "upstream":    string    // which upstream MCP server this tool belongs to
    }

    Optional filter (object — every field optional, fields AND-combine;
    string fields accept a single string OR an array; arrays OR within
    a single field):

      {
        upstream?:    string | string[],   // exact upstream name(s), any-of
        name?:        string | string[],   // case-insensitive substring(s) on name
        description?: string | string[],   // case-insensitive substring(s) on description
        match?:       string | string[],   // case-insensitive substring(s) on name + " " + description
        allOf?:       string[],            // ALL substrings must appear in name + " " + description
        regex?:       boolean,             // treat name/description/match/allOf entries as RE2 regex
                                           //  (always case-insensitive). Invalid regex => JS error.
        limit?:       number,              // cap result count (post-filter)
      }

    The array forms are the point: pull every related tool across
    multiple keywords or upstreams in ONE call.

    Returns [] if you have no linked upstreams — that is not an error.

  codemode.schemas(names)
    Returns { found: ToolSchema[], missing: string[] } for the named
    tools. 'names' accepts a single string OR an array of strings; the
    array form is the whole point — fetch every schema you need in ONE
    call to minimise round-trips.

    ToolSchema = {
      "name":        string,
      "upstream":    string,
      "inputSchema": object   // JSON Schema (Draft 2020-12). Typically
                              // { type: "object", properties: {...},
                              //   required: [...], additionalProperties: bool }
                              // May be {} for arg-less tools.
    }

    Unknown names appear in 'missing' (not 'found') so a typo never
    silently drops a tool — check 'missing' if you care.

  codemode.json(result)
    Helper: extracts and JSON.parses the first text block of an MCP
    content array (the standard [{type:"text", text:"..."}] shape).
    Returns { raw: "<text>" } when the text isn't valid JSON, and
    passes non-array inputs through unchanged. Free (no quota). Use
    it to shrink scripts and avoid repeating the r?.[0]?.text /
    JSON.parse dance in every recipe.

  codemode.quota()
    Helper: returns { used, max, remaining, deadline_ms } so loops can
    self-bound before they hit the tool-call cap or wall-clock
    timeout. Available in both codemode_search and codemode_execute
    even though Search cannot itself invoke tools.

  codemode_search does NOT expose codemode.call or codemode.<upstream>.*.
  Trying to invoke a tool throws TypeError. Use codemode_execute.

=============================================================================
RECIPES — copy and adapt
=============================================================================

(1) Full catalog dump (lean — no schemas):

    async () => codemode.tools()

(2) Filter to one upstream, then pull schemas for the matches:

    async () => {
      const tools = codemode.tools({ upstream: "github" });
      return codemode.schemas(tools.map(t => t.name));
    }

(3) Pull every tool across SEVERAL upstreams + keywords in ONE call,
    with schemas inline:

    async () => {
      const tools = codemode.tools({
        upstream: ["jira", "atlassian", "confluence"],
        match:    ["task", "issue", "ticket"],
        limit:    25,
      });
      return {
        candidates: tools,
        schemas: codemode.schemas(tools.map(t => t.name)),
      };
    }

(4) Regex matching (RE2, case-insensitive):

    async () => codemode.tools({
      regex: true,
      name:  "^(get|list)_",
    })

(5) Require multiple keywords to ALL appear (AND across patterns):

    async () => codemode.tools({ allOf: ["pull", "request"] })

(6) Inspect a specific tool you already know the name of:

    async () => codemode.schemas("jira_get_ticket").found

(7) Inspect several tools across upstreams in ONE call, surfacing typos:

    async () => {
      const { found, missing } = codemode.schemas([
        "jira_get_ticket",
        "github_get_pr",
        "slack_post_message",
      ]);
      return { found, missing };   // 'missing' catches typos at a glance
    }

(8) Survey which upstreams are linked and how many tools each exposes:

    async () => {
      const counts = {};
      for (const t of codemode.tools()) {
        counts[t.upstream] = (counts[t.upstream] || 0) + 1;
      }
      return counts;
    }

=============================================================================
WORKFLOW
=============================================================================

Typical pattern: codemode_search ONCE to find names (filter narrowly!) and
fetch the schemas you need, then codemode_execute with a script that calls
those tools. You can also call codemode.tools() / codemode.schemas() inside— returns { found, missing } so typos surface explicitly. codemode.json(result) extracts + JSON.parses the first text block of an MCP content array (free, no quota). codemode.quota() returns { used, max, remaining, deadline_ms } for self-bounded loops
codemode_execute when you want to discover and call in a single round-trip.`

const searchCodeArgDescription = `A single JavaScript expression that evaluates to a zero-argument async arrow function: async () => { ... }. Invoked once, its promise awaited, its resolved value JSON-encoded and returned to you. Use codemode.tools(filter?) to scan the LEAN catalog (name/description/upstream — no schemas) and codemode.schemas(name|names[]) to fetch JSON schemas for the tools you actually need (batched in ONE call). Filter shape: { upstream?: string|string[], name?: string|string[], description?: string|string[], match?: string|string[], allOf?: string[], regex?: boolean, limit?: number } — fields AND-combine, arrays within a field OR-combine, match targets name+description, allOf requires every substring to appear in name+description, regex flips all string fields to RE2 (case-insensitive). Read-only — cannot invoke upstream tools (use codemode_execute). Must return a JSON-serializable value. No filesystem, no network, no eval, no require, no timers, no console.`

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

A wall-clock script timeout (typically ~10s) aborts the VM and returns
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

    Optional filter (object, both fields optional, AND-combined):
      { upstream?: string,   // exact match
        match?:    string }  // case-insensitive substring matched
                             // against (name + " " + description)

    Returns [] if you have no linked upstreams — that is not an error.

  codemode.schemas(names)
    Returns Array<ToolSchema> for the named tools. Accepts a single
    string OR an array of strings; the array form is the whole point —
    fetch every schema you need in ONE call to minimise round-trips.

    ToolSchema = {
      "name":        string,
      "upstream":    string,
      "inputSchema": object   // JSON Schema (Draft 2020-12). Typically
                              // { type: "object", properties: {...},
                              //   required: [...], additionalProperties: bool }
                              // May be {} for arg-less tools.
    }

    Unknown names are silently OMITTED (no error). Check the returned
    array's length / names if you need to detect typos.

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

(3) Find candidates by keyword, then inspect their schemas in one shot:

    async () => {
      const tools = codemode.tools({ match: "ticket" });
      return {
        candidates: tools,
        schemas: codemode.schemas(tools.map(t => t.name)),
      };
    }

(4) Inspect a specific tool you already know the name of:

    async () => codemode.schemas("jira_get_ticket")

(5) Inspect several tools across upstreams in ONE call:

    async () => codemode.schemas([
      "jira_get_ticket",
      "github_get_pr",
      "slack_post_message",
    ])

(6) Survey which upstreams are linked and how many tools each exposes:

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
those tools. You can also call codemode.tools() / codemode.schemas() inside
codemode_execute when you want to discover and call in a single round-trip.`

const searchCodeArgDescription = `A single JavaScript expression that evaluates to a zero-argument async arrow function: async () => { ... }. Invoked once, its promise awaited, its resolved value JSON-encoded and returned to you. Use codemode.tools(filter?) to scan the LEAN catalog (name/description/upstream — no schemas) and codemode.schemas(name|names[]) to fetch JSON schemas for the tools you actually need (batched in ONE call). Filter shape: { upstream?: string, match?: string } where match is a case-insensitive substring on name+description. Read-only — cannot invoke upstream tools (use codemode_execute). Must return a JSON-serializable value. No filesystem, no network, no eval, no require, no timers, no console.`

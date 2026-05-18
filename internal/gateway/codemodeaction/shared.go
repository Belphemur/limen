package codemodeaction

// Shared prompt fragments. The search and execute tool descriptions
// reuse the runtime / sandbox / discovery surface sections verbatim so
// the two docs cannot drift on the parts that are genuinely the same.
// Tool-specific framing (opener, recipes, dispatch surface) lives in
// each tool's own file.

// commonInputContract is the calling convention shared by both tools.
// The error-message prefix differs (search vs. execute) so callers
// concatenate their own first sentence; everything below the divider
// is identical.
const commonInputContract = `=============================================================================
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
     are NOT serializable — convert them yourself.`

// commonSandboxRuntime is the JS runtime / denial list shared by both
// tools. Worded as a single block so any future hardening lands in
// one place.
const commonSandboxRuntime = `=============================================================================
SANDBOX RUNTIME
=============================================================================

Modern JavaScript (ES2015+: let/const, arrow functions, async/await,
destructuring, spread, Promises, JSON, Math, Array, String, Number, RegExp,
Object, Map, Set, ...).

NO filesystem, NO network (fetch/XHR/WebSocket — reach the outside world
ONLY via codemode.*), NO process/env, NO eval/Function-constructor, NO
timers, NO console, NO DOM, NO Node-isms (Buffer/require/__dirname). The
ONLY non-standard global is 'codemode'. No shared state across invocations.`

// commonDiscoveryAPI documents codemode.tools(filter?) and
// codemode.schemas(names) — the read-only catalog surface available in
// BOTH codemode_search and codemode_execute. Both bindings are free
// (do not count against the tool-call quota).
const commonDiscoveryAPI = `  codemode.tools(filter?)
    Returns Array<ToolListing> of tools visible to YOU (your tenant,
    your linked upstreams, link health enforced). LEAN shape — no
    schema, cheap to scan even with hundreds of tools.

    ToolListing = {
      "name":        string,   // pass to codemode.schemas / codemode.call
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
    silently drops a tool — check 'missing' if you care.`

// commonHelpers documents the two free helpers (json + quota) that
// exist in both Search and Execute. Neither counts against the
// tool-call quota.
const commonHelpers = `  codemode.json(result)
    Helper: extracts and JSON.parses the first text block of an MCP
    tool result. Accepts the full CallToolResult (the value tool
    proxies return), a {content:[...]} map, or the bare content array
    — all three shapes are handled. Returns { raw: "<text>" } when the
    text isn't valid JSON, and passes non-recognized inputs through
    unchanged. Free (no quota). Use it to shrink scripts and avoid
    repeating the r?.content?.[0]?.text / JSON.parse dance.

  codemode.quota()
    Helper: returns { used, max, remaining, deadline_ms } so loops can
    self-bound before they hit the tool-call cap or wall-clock
    timeout. Free (no quota).`

// commonArgContractShort is the calling-convention reminder embedded
// in BOTH *CodeArgDescription fields. Kept short on purpose — the
// long-form contract lives in the tool Description.
const commonArgContractShort = `A single JavaScript expression that evaluates to a zero-argument async arrow function: async () => { ... }. Invoked once, its promise awaited, its resolved value JSON-encoded and returned to you. Must return a JSON-serializable value. No filesystem, no network (only codemode.*), no eval, no require, no timers, no console.`

// commonFilterShapeShort is the one-line filter recap embedded in both
// *CodeArgDescription fields.
const commonFilterShapeShort = `Filter shape: { upstream?: string|string[], name?: string|string[], description?: string|string[], match?: string|string[], allOf?: string[], regex?: boolean, limit?: number } — fields AND-combine, arrays within a field OR-combine, match targets name+description, allOf requires every substring to appear in name+description, regex flips all string fields to RE2 (case-insensitive).`

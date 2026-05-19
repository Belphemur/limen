package codemodeaction

// Shared prompt fragments. The search and execute tool descriptions
// reuse the runtime / sandbox / discovery surface sections verbatim so
// the two docs cannot drift on the parts that are genuinely the same.
// Tool-specific framing (opener, recipes, dispatch surface) lives in
// each tool's own file.

// commonInputContract opens the <input> section. The matching </input>
// is emitted by the tool-specific *InputErrorSuffix so each tool can
// stamp its own error-message prefix (search vs. execute) before the
// closing tag.
const commonInputContract = `<input>
code = a single expression evaluating to: async () => { ... }
Not a function declaration, not a class, not top-level statements, not an IIFE.
Must return a JSON-serializable value (object | array | string | finite number | boolean | null).
'undefined' → null. Functions, symbols, BigInt, Map, Set, Date, circular refs are NOT serializable — convert them yourself.
`

// commonSandboxRuntime emits the <runtime> section plus the
// no-shared-state <critical> rider. Worded as one block so any future
// hardening lands in one place.
const commonSandboxRuntime = `<runtime>
Modern JS (ES2015+: let/const, arrow fns, async/await, destructuring, spread, Promises, JSON, Math, Array, String, Number, RegExp, Object, Map, Set).
NO: filesystem, network (fetch/XHR/WebSocket), process/env, eval/Function ctor, timers, console, DOM, Buffer, require, __dirname.
Only non-standard global is 'codemode'.
</runtime>

<critical>
NO SHARED STATE between invocations. Each call gets a fresh VM — no variables, no caches, no module-level objects survive. Inline every ID, name, token, or intermediate result as a literal inside the script. Never plan workflows that depend on "the X I fetched last time" — fetch it again, or fold both steps into the same script.
</critical>`

// commonDiscoveryAPI documents codemode.tools(filter?) and
// codemode.schemas(names) inside the surrounding <api> block. TS
// signatures + a compact substring-semantics legend replace the
// previous prose paragraphs. Both bindings are free (no quota).
const commonDiscoveryAPI = `codemode.tools(filter?) → { upstreams: UpstreamGroup[]; hint?: EmptyHint }
  Catalog visible to YOU (tenant + linked upstreams, link health enforced). Lean shape, no schemas.
  type UpstreamGroup = {
    name:    string                              // canonical upstream name
    aliases: string[]                            // sub-brand names (e.g. ["jira","confluence"] for "atlassian")
    context: Record<string, unknown>             // upstream defaults shallow-merged with your link overrides
    tools:   { name: string; description: string }[]
  }
  type EmptyHint = { tried: string[]; available: string[]; suggested: string[] }
  type ToolFilter = {
    upstream?:    string | string[]              // matches canonical name OR any alias
    name?:        string | string[]
    description?: string | string[]
    match?:       string | string[]              // → name + " " + description
    allOf?:       string[]
    regex?:       boolean                        // strings become RE2 (ci)
    limit?:       number                         // caps TOTAL tools across all groups
  }
  // Fields AND-combine. Array values within a field OR-combine.
  // 'upstream' resolves aliases: codemode.tools({upstream:"jira"}) returns the
  // "atlassian" group when "jira" is one of its aliases.
  // 'hint' is present only when a non-empty filter yielded zero tools; use
  // hint.suggested to recover from a typo. Returns { upstreams: [] } if you
  // have no linked upstreams.
  // 'context' is INFORMATIONAL — the gateway does NOT inject it into outbound
  // calls. Spread it explicitly when you need it: {...g.context, ...args}.
  // Flatten idiom (mimics the old flat shape):
  //   const flat = r.upstreams.flatMap(g => g.tools.map(t => ({...t, upstream: g.name})));

codemode.schemas(names: string | string[]) → { found: ToolSchema[]; missing: string[] }
  type ToolSchema = { name: string; upstream: string; inputSchema: object /* JSON Schema 2020-12 */ }
  Use the array form to batch. 'missing' surfaces typos.`

// commonHelpers documents the two free helpers (json + quota) inside
// the surrounding <api> block. "Free (no quota)" is stated once in
// the <quotas> block; not repeated here.
const commonHelpers = `codemode.json(result) → unwraps the first text block of an MCP CallToolResult and JSON.parses it.
  Accepts: full CallToolResult | { content: [...] } map | bare content array.
  Returns { raw: "<text>" } when not valid JSON.

codemode.quota() → { used: number; max: number; remaining: number; deadline_ms: number }. Bound loops with .remaining.`

// commonArgContractShort is the calling-convention reminder embedded
// in BOTH *CodeArgDescription fields. Long-form contract lives in the
// tool Description.
const commonArgContractShort = `Single JS expression: async () => { ... }. Awaited once, resolved value JSON-encoded and returned. Must be JSON-serializable. No filesystem, no network (only codemode.*), no eval, no require, no timers, no console.`

// commonFilterShapeShort is the one-line filter recap embedded in both
// *CodeArgDescription fields. TS-signature form.
const commonFilterShapeShort = `codemode.tools(filter?) → { upstreams: { name, aliases, context, tools: {name, description}[] }[]; hint? }. Filter: { upstream?: string|string[]; name?: string|string[]; description?: string|string[]; match?: string|string[]; allOf?: string[]; regex?: boolean; limit?: number }. 'upstream' resolves aliases. Fields AND-combine; arrays within a field OR-combine; match targets name+description; allOf requires every entry to appear; regex flips strings to RE2 (always ci); limit caps total tools. 'hint' appears only on empty-filtered results — use hint.suggested. 'context' is informational; spread it into args explicitly — gateway does NOT auto-inject it. Flatten: r.upstreams.flatMap(g => g.tools.map(t => ({...t, upstream: g.name}))).`

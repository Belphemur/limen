package codemodeaction

// Execute is the dispatch tool: exposes the full sandbox API including
// per-(upstream, tool) proxies and codemode.call(). Subject to a
// per-invocation tool-call quota and a wall-clock script timeout.
var Execute = Definition{
	Name:               "codemode_execute",
	Description:        executeDescription,
	CodeArgDescription: executeCodeArgDescription,
}

const executeOpener = `<role>codemode_execute — compose an ENTIRE multi-step workflow in ONE call.</role>

Runs JavaScript that invokes upstream tools, composes, branches, loops, handles errors, returns a single JSON-serializable value. Tools dispatch with YOUR identity; the gateway injects per-user auth, records link health, applies the upstream's resilience policy. You never see or handle credentials. The same codemode.tools() and codemode.schemas() bindings codemode_search exposes are available here — most workflows skip codemode_search entirely.

<critical>
EVIDENCE DISCIPLINE. When the workflow is an audit ("is X implemented/configured/correct?"), return CONCRETE evidence — cite the exact field, value, resource ID, or count supporting each claim. If available tools cannot prove the claim, say "cannot verify from available tools"; do NOT fall back to weasel words ("appears", "indirectly validated", "looks consistent"). Prefer { claim, evidence, confidence: "verified" | "partial" | "none" } over prose. Surface your own search shape (filter used, total results, sample size) so the caller can audit the bucket.
</critical>

<when_to_use>
- know tool name + args              → codemode_execute directly
- know upstream/domain, not the tool → codemode_execute with codemode.tools({upstream}) inline
- don't know which upstreams linked  → codemode_search ONCE, then codemode_execute
- NEVER: codemode_execute → codemode_execute → codemode_execute for what is logically ONE workflow.
  There is no shared state between invocations — fold every step into one script.
</when_to_use>

<anti_patterns>
BAD:  codemode.tools()                     // dumps everything
GOOD: codemode.tools({upstream: "jira"})   // filter

BAD:  3 codemode_execute calls chained externally by the agent.
GOOD: ONE codemode_execute whose script chains all the calls inside.

BAD:  Plan a second codemode_execute that uses "the account_id from last time" — there IS no last time.
GOOD: Fetch the account_id and use it in the SAME script.

BAD:  "appears to exist", "indirectly validated", "looks consistent" — speculation as evidence.
GOOD: { claim, evidence: "<exact field/value/ID>", confidence: "verified"|"partial"|"none" }.
</anti_patterns>

`

const executeInputErrorSuffix = `Throwing/rejecting → IsError=true, text: "execute failed: <message>".
</input>

`

const executeRuntimeSuffix = `

`

const executeQuotasBlock = `<quotas>
Budget per call: ~30s wall-clock AND ~50 tool calls.
All codemode.<upstream>.* and codemode.call() invocations cost 1 quota; codemode.tools / schemas / json / quota are FREE.
- Bound loops up front; prefer few rich calls over many tiny ones.
- Self-bound: if (codemode.quota().remaining <= 1) break;

<parallelism>
Tool proxies return real Promises on an event loop. Promise.all / Promise.allSettled actually parallelize, bounded by a per-invocation in-flight cap (default 8). Independent calls SHOULD fan out; sequential 'await ...; await ...;' wastes wall-clock when the calls don't depend on each other.
- Use Promise.allSettled when one failure shouldn't kill the batch.
- Excess parallel calls queue on the cap, they don't error; total count still counts against the 50-call quota.
- For large fan-outs, slice into chunks of ~cap size so quota self-bounding stays meaningful.
</parallelism>

Timeout → IsError=true with "script timeout"; in-flight tool calls are cancelled and reject.
Quota exceeded → uncatchable interrupt (try/catch will NOT swallow); script aborts with "max_tool_calls exceeded".
</quotas>

`

const executeAPIHeader = `<api>
`

const executeDispatchAPI = `
await codemode.<upstream>.<toolName>(args) → upstream MCP CallToolResult (use codemode.json to unwrap).
  '<upstream>' = exact 'upstream' from the catalog. '<toolName>' = exact 'name'. 'args' = plain object matching inputSchema.
  Two upstreams may expose the same name (e.g. 'github' and 'gitlab' both expose 'search_issues') — there is NO flat codemode.<toolName>.
  For names not valid as JS identifiers ('-', '.', leading digit): use codemode.call() or bracket notation, e.g. codemode["my-upstream"]["some-tool"](args).

await codemode.call(upstream, name, args) → string-keyed escape hatch. Both args REQUIRED.

Reserved keys: 'codemode.tools', '.schemas', '.call', '.json', '.quota'. Upstreams literally named "tools", "schemas", "call", "json", or "quota" must be reached via codemode.call().
</api>

`

const executeErrorShape = `<errors>
A failed tool call REJECTS with an Error whose 'message' contains a kind tag (case-insensitive substring):
  needs_relink         — user must re-link the upstream in the portal.
  no_link              — user has no link to this upstream.
  auto_disabled        — link auto-disabled by the gateway.
  upstream_unavailable | "circuit ... open" — resilience layer shedding load; may retry later.
  401 | 403            — auth rejected even after forced refresh.
  5xx | network        — upstream broken or unreachable.
Wrap individual calls in try/catch to recover; otherwise rejection bubbles up → script fails with IsError=true.
</errors>

`

const executeRecipes = `<examples>

(1) Discover + call in ONE round-trip (skip codemode_search):
  async () => {
    const tools = codemode.tools({ upstream: "jira", match: "ticket" });
    const { found } = codemode.schemas(tools.map(t => t.name));
    const get = found.find(s => s.name.includes("get"));
    return codemode.json(await codemode.call(get.upstream, get.name, { id: "PROJ-123" }));
  }

(2) Parallel fan-out with Promise.allSettled (independent calls — in-flight cap bounds concurrency, you don't have to):
  async () => {
    const ids = ["PROJ-1", "PROJ-2", "PROJ-3", "PROJ-4"];
    if (codemode.quota().remaining < ids.length) ids.length = codemode.quota().remaining;
    const settled = await Promise.allSettled(ids.map(id => codemode.jira.get_ticket({ id })));
    return ids.map((id, i) => settled[i].status === "fulfilled"
      ? { id, ok: true,  ticket: codemode.json(settled[i].value) }
      : { id, ok: false, error: String(settled[i].reason?.message || settled[i].reason) });
  }

(3) Cross-system audit — independent fetches in parallel, return evidence not impressions:
  async () => {
    const jql = 'text ~ "cloudflare" AND status = Done';
    const [issuesRaw, zonesRaw] = await Promise.all([
      codemode.jira.search({ jql, limit: 50 }),
      codemode.cloudflare.list_zones({}),
    ]);
    const issues = codemode.json(issuesRaw);
    const zones  = codemode.json(zonesRaw);
    const zoneNames = new Set((zones?.result || []).map(z => z.name));
    const checks = (issues?.issues || []).slice(0, 20).map(it => {
      const hint = (it.fields?.summary || "").match(/[a-z0-9-]+\.[a-z]{2,}/i)?.[0];
      if (!hint) return { key: it.key, confidence: "none", evidence: "no hostname in summary" };
      const matched = [...zoneNames].find(z => hint.endsWith(z));
      return matched
        ? { key: it.key, claim: hint, evidence: "zone " + matched, confidence: "verified" }
        : { key: it.key, claim: hint, evidence: "no matching CF zone", confidence: "none" };
    });
    return { filter: { jql, limit: 50 }, total: issues?.total ?? 0, sampled: checks.length, checks };
  }

</examples>`

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

const executeCodeArgDescription = `Compose ONE pipeline; don't split a workflow across multiple calls (no shared state between invocations). ` +
	commonArgContractShort +
	` Invoke tools via 'await codemode.<upstream>.<toolName>(args)' or 'await codemode.call(upstream, name, args)' (runtime/non-identifier names). NO flat codemode.<toolName>. ` +
	`Filter codemode.tools(filter) by upstream/keyword — never dump. Batch codemode.schemas([...]). codemode.json(result) unwraps the MCP CallToolResult text block. codemode.quota() → { used, max, remaining, deadline_ms }. ` +
	`Budget: ~30s wall-clock AND ~50 tool calls; tools/schemas/json/quota are FREE, dispatch costs 1 each. Quota exhaustion is uncatchable — self-bound with codemode.quota().remaining. Promise.all / allSettled parallelize independent calls (in-flight cap default 8); fan out instead of awaiting sequentially when calls are independent.`

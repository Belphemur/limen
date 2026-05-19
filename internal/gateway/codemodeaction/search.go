package codemodeaction

// Search is the discovery tool: read-only, exposes codemode.tools() and
// codemode.schemas() only. Use it to surface the per-user catalog and
// fetch input schemas for tools the LLM is about to invoke.
var Search = Definition{
	Name:               "codemode_search",
	Description:        searchDescription,
	CodeArgDescription: searchCodeArgDescription,
}

const searchOpener = `<role>codemode_search — filter the catalog, don't dump it. READ-ONLY: cannot invoke upstream tools (use codemode_execute).</role>

<critical>
In MOST cases you do NOT need codemode_search. The same codemode.tools() and codemode.schemas() bindings exist inside codemode_execute, so you can discover, fetch schemas, AND call tools in ONE round-trip. Use codemode_search only when you must explore before committing to a plan.
</critical>

<when_to_use>
- know tool name + args              → codemode_execute directly
- know upstream/domain, not the tool → codemode_execute with codemode.tools({upstream}) inline
- don't know which upstreams linked  → codemode_search ONCE, then codemode_execute
- NEVER: codemode_search → codemode_search → codemode_search
</when_to_use>

<anti_patterns>
BAD:  codemode.tools()                     // dumps everything
GOOD: codemode.tools({upstream: "jira"})   // filter

BAD:  codemode.schemas("a"); codemode.schemas("b"); codemode.schemas("c");
GOOD: codemode.schemas(["a","b","c"])      // batched
</anti_patterns>

`

const searchInputErrorSuffix = `Throwing/rejecting → IsError=true, text: "search failed: <message>".
</input>

`

const searchRuntimeSuffix = `

Wall-clock timeout (~30s) → IsError=true with "script timeout".

`

const searchAPIHeader = `<api>
`

const searchAPIDenyNote = `
codemode_search does NOT expose codemode.call or codemode.<upstream>.*. Calling one throws TypeError. Use codemode_execute.
</api>

`

const searchRecipes = `<examples>

(1) Narrow to one upstream + pull schemas in ONE batch:
  async () => {
    const r = codemode.tools({ upstream: "github" });
    const names = r.upstreams.flatMap(g => g.tools.map(t => t.name));
    return { upstreams: r.upstreams, schemas: codemode.schemas(names) };
  }

(2) Sub-brand alias (e.g. "jira" → "atlassian") + many keywords + schemas, ONE call:
  async () => {
    const r = codemode.tools({
      upstream: ["jira", "atlassian", "confluence"],   // aliases resolve to canonical groups
      match:    ["task", "issue", "ticket"],
      limit:    25,
    });
    if (r.hint) return { empty: true, hint: r.hint };  // recover from a typo / unlinked upstream
    const names = r.upstreams.flatMap(g => g.tools.map(t => t.name));
    return { groups: r.upstreams, schemas: codemode.schemas(names) };
  }

(3) Inspect known tools, surface typos:
  async () => {
    const { found, missing } = codemode.schemas(["jira_get_ticket", "github_get_pr", "slack_post_message"]);
    return { found, missing };
  }

(4) Surface per-upstream context the agent must pass back explicitly (gateway does NOT auto-inject):
  async () => {
    const r = codemode.tools({ upstream: "jira" });
    return r.upstreams.map(g => ({ name: g.name, context: g.context, toolCount: g.tools.length }));
  }

</examples>`

const searchDescription = searchOpener +
	commonInputContract + searchInputErrorSuffix +
	commonSandboxRuntime + searchRuntimeSuffix +
	searchAPIHeader +
	commonDiscoveryAPI + "\n\n" +
	commonHelpers +
	searchAPIDenyNote +
	searchRecipes

const searchCodeArgDescription = `Filter the catalog — never dump it. ` +
	commonArgContractShort +
	` Call codemode.tools(filter) with at least one of {upstream, match, allOf, name}, then batch codemode.schemas([...]). ` +
	`Most workflows don't need codemode_search at all — the same bindings exist inside codemode_execute. ` +
	commonFilterShapeShort +
	` codemode.schemas returns { found, missing }. READ-ONLY — cannot invoke tools.`

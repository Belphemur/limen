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
    const tools = codemode.tools({ upstream: "github" });
    return { tools, schemas: codemode.schemas(tools.map(t => t.name)) };
  }

(2) Many upstreams + many keywords + schemas, ONE call:
  async () => {
    const tools = codemode.tools({
      upstream: ["jira", "atlassian", "confluence"],
      match:    ["task", "issue", "ticket"],
      limit:    25,
    });
    return { candidates: tools, schemas: codemode.schemas(tools.map(t => t.name)) };
  }

(3) Inspect known tools, surface typos:
  async () => {
    const { found, missing } = codemode.schemas(["jira_get_ticket", "github_get_pr", "slack_post_message"]);
    return { found, missing };
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

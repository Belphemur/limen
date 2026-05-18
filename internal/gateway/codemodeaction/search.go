package codemodeaction

// Search is the discovery tool: read-only, exposes codemode.tools() and
// codemode.schemas() only. Use it to surface the per-user catalog and
// fetch input schemas for tools the LLM is about to invoke.
var Search = Definition{
	Name:               "codemode_search",
	Description:        searchDescription,
	CodeArgDescription: searchCodeArgDescription,
}

const searchOpener = `Filter the catalog. Don't dump it.

codemode_search is for narrowing a (potentially large) tool catalog down to the
handful you actually need, and pulling their schemas in ONE batch. The
catalog can contain hundreds of tools across many upstreams; an unfiltered
dump wastes your context budget and slows every subsequent step.

In MOST cases you do NOT need to call codemode_search at all — the same
codemode.tools() and codemode.schemas() bindings exist inside codemode_execute,
so you can discover, fetch schemas, and call tools in a SINGLE round-trip.
Reach for codemode_search only when you genuinely need to explore before
committing to a plan.

=============================================================================
WHEN TO USE EACH TOOL
=============================================================================

  You already know the tool name and arg shape
      → call codemode_execute directly.
  You know the upstream / domain but not the exact tool
      → codemode_execute, with codemode.tools({upstream: "..."}) inline.
  You don't know which upstreams are even linked
      → codemode_search ONCE with a broad filter, then codemode_execute.
  Never: codemode_search → codemode_search → codemode_search.

=============================================================================
ANTI-PATTERNS
=============================================================================

  BAD:  codemode.tools()                          // dumps the whole catalog
  GOOD: codemode.tools({upstream: "jira"})        // filter when you know it
  GOOD: codemode.tools({match: ["issue","bug"]})  // filter by keyword

  BAD:  codemode_search, then codemode_execute, then codemode_execute, ...
        (one workflow split across many round-trips)
  GOOD: codemode_execute ONCE, composing every step inside the script.

  BAD:  codemode.schemas("a"); codemode.schemas("b"); codemode.schemas("c");
  GOOD: codemode.schemas(["a","b","c"])           // batched, one call

You provide a JavaScript async arrow function. The runtime evaluates it,
awaits the returned promise, JSON-encodes the resolved value, and returns
that JSON string as the tool result.

codemode_search is READ-ONLY: it cannot invoke upstream tools — use
codemode_execute for that.

`

const searchInputErrorSuffix = `
  5. Throwing or rejecting yields IsError=true with text
     "search failed: <message>".

`

const searchRuntimeSuffix = `

A wall-clock script timeout (typically ~30s) aborts the VM and returns
IsError=true with text containing "script timeout".

`

const searchAPIHeader = `=============================================================================
SANDBOX API — codemode_search exposes ONLY the discovery surface
=============================================================================

`

const searchAPIDenyNote = `

  codemode_search does NOT expose codemode.call or codemode.<upstream>.*.
  Trying to invoke a tool throws TypeError. Use codemode_execute.

`

const searchRecipes = `=============================================================================
RECIPES — copy and adapt (filtered patterns FIRST; reach for the
unfiltered dump only when you truly need it)
=============================================================================

(1) Narrow to one upstream and pull its schemas in ONE batch:

    async () => {
      const tools = codemode.tools({ upstream: "github" });
      return {
        tools,
        schemas: codemode.schemas(tools.map(t => t.name)),
      };
    }

(2) Pull every tool across SEVERAL upstreams + keywords in ONE call,
    schemas inline:

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

(3) Require multiple keywords to ALL appear (AND across patterns):

    async () => codemode.tools({ allOf: ["pull", "request"] })

(4) Regex matching (RE2, case-insensitive) — e.g. all read-only verbs:

    async () => codemode.tools({
      regex: true,
      name:  "^(get|list|search|find)_",
    })

(5) Inspect a specific tool you already know the name of:

    async () => codemode.schemas("jira_get_ticket").found

(6) Inspect several tools across upstreams in ONE call, surfacing typos:

    async () => {
      const { found, missing } = codemode.schemas([
        "jira_get_ticket",
        "github_get_pr",
        "slack_post_message",
      ]);
      return { found, missing };   // 'missing' catches typos at a glance
    }

(7) Survey which upstreams are linked and how many tools each exposes
    (the one legitimate reason to call codemode.tools() unfiltered):

    async () => {
      const counts = {};
      for (const t of codemode.tools()) {
        counts[t.upstream] = (counts[t.upstream] || 0) + 1;
      }
      return counts;
    }

(8) Full catalog dump — LAST RESORT. Only use when you genuinely have no
    idea which upstreams or keywords apply. Prefer (1)–(7) above.

    async () => codemode.tools()`

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
	` Call codemode.tools(filter) with at least one of {upstream, match, allOf, name}, then batch codemode.schemas([...]) for the matches. ` +
	`Most workflows don't need codemode_search at all — the same bindings exist inside codemode_execute, so prefer discovering AND calling in a single round-trip there. ` +
	commonFilterShapeShort +
	` codemode.schemas returns { found, missing } so typos surface explicitly. Read-only — cannot invoke upstream tools (use codemode_execute).`

// Package transport — code-mode MCP server.
//
// This file is the downstream-facing MCP server that Limen exposes to
// clients. It is a thin shell: it does not aggregate or proxy upstream
// tools directly. Instead it advertises exactly two tools —
// codemode_search and codemode_execute — and delegates their execution
// to gateway.CodeModeHandler, which runs tenant-supplied JavaScript in
// an isolated sandbox with the per-user upstream tool catalog injected.
//
// All real fan-out (per-tenant upstream lookup, per-user auth header
// injection, link-health bookkeeping, resilience) lives behind the
// handler, on gateway.Manager. From this file's perspective there is one
// MCP server per process; tenant scoping is recovered at request time
// from the chi route via tenancy.TenantFromContext, which the dynamic
// base-path callback uses to advertise the correct per-tenant message
// endpoint.
package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/gateway"
	"github.com/belphemur/limen/internal/tenancy"
)

// MCPServer is the code-mode MCP server. It wraps a single mcp-go SSE
// server configured with a dynamic base path so one instance can serve
// every tenant: the base path is derived from the request's resolved
// tenant and the server advertises per-tenant message endpoints under
// /t/{tenant}/mcp/message.
//
// The advertised tool surface is fixed: codemode_search (discover) and
// codemode_execute (call). Upstream tools are never exposed directly —
// the client reaches them by writing JavaScript that calls codemode.*
// inside the handler's sandbox.
type MCPServer struct {
	handler    *gateway.CodeModeHandler
	logger     *zap.Logger
	core       *server.MCPServer
	sse        *server.SSEServer
	streamable *server.StreamableHTTPServer
}

// NewMCPServer constructs the code-mode MCP server. All tool execution
// flows through handler; the Manager passed to NewCodeModeHandler is
// what actually fans out to per-(tenant, upstream) Bundles — this
// transport layer only sees the handler facade.
func NewMCPServer(_ *gateway.Manager, handler *gateway.CodeModeHandler, logger *zap.Logger) *MCPServer {
	s := &MCPServer{
		handler: handler,
		logger:  logger,
	}
	s.core = server.NewMCPServer(
		"limen",
		"0.1.0",
		server.WithToolCapabilities(true),
	)
	s.registerCodeModeTools()
	s.sse = server.NewSSEServer(
		s.core,
		server.WithDynamicBasePath(func(r *http.Request, _ string) string {
			if t, ok := tenancy.TenantFromContext(r.Context()); ok {
				return "/t/" + t.PublicID + "/mcp"
			}
			return "/mcp"
		}),
	)
	s.streamable = server.NewStreamableHTTPServer(
		s.core,
		server.WithStateLess(true),
	)
	return s
}

// SSEHandler returns the long-lived event-stream handler clients GET to
// open an MCP session. Mount at the tenant subroute path "/sse"; the
// dynamic base-path callback rewrites the advertised message endpoint
// to /t/{tenant}/mcp/message before sending it to the client.
func (s *MCPServer) SSEHandler() http.Handler { return s.sse.SSEHandler() }

// MessageHandler returns the JSON-RPC POST handler that ingests client
// requests for an existing SSE session. Mount at the tenant subroute
// path "/message".
func (s *MCPServer) MessageHandler() http.Handler { return s.sse.MessageHandler() }

// StreamableHandler returns the Streamable HTTP transport handler (MCP
// 2025-03-26 spec). It accepts POST for JSON-RPC requests and GET for
// optional server-initiated streaming. Mount at the tenant subroute
// root so clients can POST to /t/{tenant}/mcp directly — most modern
// MCP clients (Cursor, Claude Desktop) probe streamable HTTP before
// falling back to legacy SSE.
func (s *MCPServer) StreamableHandler() http.Handler { return s.streamable }

// Core exposes the underlying mcp-go server for callers that need to
// register additional tools after construction. Intended for tests and
// future extension points; production wiring should not bypass the
// code-mode surface.
func (s *MCPServer) Core() *server.MCPServer { return s.core }

// registerCodeModeTools advertises the fixed downstream tool surface:
// codemode_search (read-only discovery of the per-user tool catalog)
// and codemode_execute (sandboxed JS that calls upstream tools via
// codemode.<upstream>.<name>). The verbose descriptions are the prompt
// the client LLM sees when picking a tool, so they double as user-facing
// docs.
func (s *MCPServer) registerCodeModeTools() {
	searchTool := mcp.NewTool("codemode_search",
		mcp.WithDescription(`Discover which MCP tools are available to you and inspect their input schemas, without loading the full catalog into your context.

You provide a JavaScript async arrow function. The function inspects the
catalog via 'codemode.tools()' and returns the subset (or projection, or
summary) that you care about. The runtime evaluates your function, awaits
its promise, JSON-encodes the resolved value, and returns that JSON string
to you as the tool's text result.

codemode_search is READ-ONLY: it cannot invoke upstream tools. If you need
to call a tool after discovering it, use 'codemode_execute' instead.

=============================================================================
INPUT CONTRACT — the 'code' argument MUST be ALL of the following:
=============================================================================

  1. A SINGLE JavaScript EXPRESSION that evaluates to an async arrow function.
     The exact required shape is:

         async () => { /* your code */ }

         or equivalently:

         async () => ( /* expression */ )

  2. NOT a function declaration ('async function foo() {...}' is REJECTED).
  3. NOT a script of top-level statements ('const x = 1; return x;' is REJECTED).
  4. NOT a class, NOT an IIFE wrapper, NOT multiple statements at top level.
  5. The function takes NO parameters. Any inputs must be hard-coded in the body.
  6. The function MUST eventually return (or 'await' something that returns).
     If it returns 'undefined', you will receive the JSON literal 'null'.

The runtime will: parse 'code' as an expression, evaluate it to obtain the
async function, immediately invoke it with zero arguments, await the
returned promise, then JSON.stringify the resolved value.

=============================================================================
RETURN VALUE CONTRACT
=============================================================================

Whatever your function resolves to MUST be JSON-serializable:
  - Allowed: null, boolean, number (finite), string, array, plain object.
  - NOT allowed: undefined (becomes null), functions, symbols, BigInt,
    circular references, Date objects (use d.toISOString() yourself),
    Map/Set (convert to array/object yourself).
  - Throwing or rejecting causes the tool call to return an error result
    (IsError=true) whose text is "search failed: <error message>".

=============================================================================
SANDBOX RUNTIME
=============================================================================

The script runs inside an isolated JavaScript sandbox. It supports
modern JavaScript (ES2015+: let/const, arrow functions, async/await,
destructuring, spread, template literals, Promises, JSON, Math, Array,
String, Number, RegExp, Object, Map, Set, etc.).

It does NOT have access to ANY of the following — referencing them throws
ReferenceError or returns undefined:

  - Filesystem:        no 'fs', no 'path', no 'require', no 'import'.
  - Network:           no 'fetch', no 'XMLHttpRequest', no 'WebSocket'.
  - Process / env:     no 'process', no 'globalThis.process', no env vars.
  - Code evaluation:   no 'eval', no 'Function' constructor for code execution.
  - Timers:            no 'setTimeout', no 'setInterval', no 'requestAnimationFrame'.
  - I/O:               no 'console.log' (write to your return value instead).
  - DOM / browser:     no 'window', no 'document', no 'localStorage'.
  - Node-isms:         no 'Buffer', no '__dirname', no '__filename'.

The ONLY non-standard global is 'codemode'. Everything else is plain JS.

The script is killed if it exceeds the server-configured wall-clock script
timeout (typically ~10s). When that happens the tool call returns an error
result whose message contains "script timeout".

=============================================================================
SANDBOX API — codemode_search exposes ONLY the discovery surface
=============================================================================

  await codemode.tools()
    Resolves to an Array<ToolDefinition> of tools visible to YOU
    (the calling authenticated user, scoped to YOUR tenant). Per-user link
    health is enforced: tools whose upstream you have not linked, or whose
    link is currently auto-disabled / needs-relink, are EXCLUDED.

    Each ToolDefinition has exactly these properties:

      {
        "name":        string,   // exact identifier; pass this to codemode_execute
        "description": string,   // free-form, may be empty
        "inputSchema": object,   // JSON Schema (Draft 2020-12 compatible).
                                 // Typically { type: "object", properties: {...},
                                 //   required: [...], additionalProperties: bool }
                                 // May be {} (empty schema) for arg-less tools.
        "upstream":    string    // the upstream MCP server this tool belongs to
      }

    The array may be EMPTY if you have no linked upstreams. That is not an
    error; return an empty array (or a message) from your function.

  codemode_search does NOT expose codemode.call, codemode.<toolName>, or any
  way to invoke an upstream. Trying to invoke a tool will throw
  TypeError: codemode.<name> is not a function (or similar). Use
  codemode_execute when you need to call something.

=============================================================================
COMMON RECIPES — copy and adapt
=============================================================================

(1) List EVERY tool you can see (full catalog dump):

    async () => {
      return await codemode.tools();
    }

(2) Find tools whose name contains a substring (case-insensitive):

    async () => {
      const tools = await codemode.tools();
      const q = "jira";
      return tools.filter(t => t.name.toLowerCase().includes(q));
    }

(3) Find tools whose description mentions a keyword:

    async () => {
      const tools = await codemode.tools();
      return tools.filter(t =>
        (t.description || "").toLowerCase().includes("ticket")
      );
    }

(4) Find tools that accept a specific argument name:

    async () => {
      const tools = await codemode.tools();
      return tools.filter(t =>
        t.inputSchema && t.inputSchema.properties &&
        Object.prototype.hasOwnProperty.call(t.inputSchema.properties, "projectId")
      );
    }

(5) List every upstream you currently have access to:

    async () => {
      const tools = await codemode.tools();
      return [...new Set(tools.map(t => t.upstream))].sort();
    }

(6) Count tools per upstream:

    async () => {
      const tools = await codemode.tools();
      const counts = {};
      for (const t of tools) counts[t.upstream] = (counts[t.upstream] || 0) + 1;
      return counts;
    }

(7) Inspect ONE specific tool's input schema before calling it:

    async () => {
      const tools = await codemode.tools();
      const t = tools.find(x => x.name === "jira_get_ticket");
      if (!t) return { error: "tool not found", available: tools.map(x => x.name) };
      return { name: t.name, schema: t.inputSchema, required: t.inputSchema.required || [] };
    }

(8) Produce a compact summary you can re-use in a later codemode_execute call:

    async () => {
      const tools = await codemode.tools();
      return tools.map(t => ({
        name: t.name,
        upstream: t.upstream,
        params: Object.keys(t.inputSchema?.properties || {}),
        required: t.inputSchema?.required || [],
      }));
    }

=============================================================================
WORKFLOW
=============================================================================

Typical pattern: call codemode_search FIRST to discover the tool name(s) and
argument schema(s) you need, then call codemode_execute with a script that
invokes them. You can also call 'codemode.tools()' inside a codemode_execute
script — the two endpoints expose the same catalog.`),
		mcp.WithString("code",
			mcp.Required(),
			mcp.Description(`A single JavaScript expression that evaluates to a zero-argument async arrow function. Exact required shape: async () => { ... }. The function will be invoked once, its returned promise awaited, and its resolved value JSON-encoded and returned to you. The function MUST use 'await codemode.tools()' to read the per-user tool catalog and return a JSON-serializable value (object, array, string, number, boolean, or null). codemode_search is read-only — it cannot invoke upstream tools; use codemode_execute for that. The sandbox has no filesystem, network, eval, require, timers, or console.`),
		),
	)

	executeTool := mcp.NewTool("codemode_execute",
		mcp.WithDescription(`Run JavaScript that CALLS one or more MCP tools and returns a composed result.

This is the workhorse. You write an async arrow function that invokes upstream
tools via 'codemode.<upstream>.<toolName>(args)' (or 'codemode.call(upstream,
name, args)'), composes / branches / loops / handles errors as needed, and
returns a single
JSON-serializable value. The runtime evaluates your function, awaits its
promise, JSON-encodes the resolved value, and returns that JSON string to you
as the tool's text result. There is no streaming, no incremental output, and
no way to send intermediate values back — everything is returned at the end.

Tools you invoke are dispatched with YOUR identity. The gateway automatically
injects the per-user auth headers for each upstream, records link health on
every call (success / failure / 401 retry), and (in the near future) applies
the resilience policy (timeout, retry, circuit breaker) configured for that
upstream. You never see or handle credentials.

=============================================================================
INPUT CONTRACT — the 'code' argument MUST be ALL of the following:
=============================================================================

  1. A SINGLE JavaScript EXPRESSION that evaluates to an async arrow function.
     The exact required shape is:

         async () => { /* your code */ }

         or equivalently:

         async () => ( /* expression */ )

  2. NOT a function declaration ('async function foo() {...}' is REJECTED).
  3. NOT a script of top-level statements ('const x = 1; return x;' is REJECTED).
  4. NOT a class, NOT an IIFE wrapper, NOT multiple statements at top level.
  5. The function takes NO parameters. Any inputs must be hard-coded in the body.
  6. The function MUST eventually return (or 'await' something that returns).
     If it returns 'undefined', you will receive the JSON literal 'null'.

The runtime will: parse 'code' as an expression, evaluate it to obtain the
async function, immediately invoke it with zero arguments, await the
returned promise, then JSON.stringify the resolved value.

=============================================================================
RETURN VALUE CONTRACT
=============================================================================

Whatever your function resolves to MUST be JSON-serializable:
  - Allowed: null, boolean, number (finite), string, array, plain object.
  - NOT allowed: undefined (becomes null), functions, symbols, BigInt,
    circular references, Date objects (use d.toISOString() yourself),
    Map/Set (convert to array/object yourself).
  - Throwing or rejecting causes the tool call to return an error result
    (IsError=true) whose text is "execute failed: <error message>".

=============================================================================
SANDBOX RUNTIME
=============================================================================

The script runs inside an isolated JavaScript sandbox. It supports
modern JavaScript (ES2015+: let/const, arrow functions, async/await,
destructuring, spread, template literals, Promises, JSON, Math, Array,
String, Number, RegExp, Object, Map, Set, etc.).

It does NOT have access to ANY of the following — referencing them throws
ReferenceError or returns undefined:

  - Filesystem:        no 'fs', no 'path', no 'require', no 'import'.
  - Network:           no 'fetch', no 'XMLHttpRequest', no 'WebSocket'.
                       The ONLY way to reach the outside world is via codemode.*.
  - Process / env:     no 'process', no 'globalThis.process', no env vars.
  - Code evaluation:   no 'eval', no 'Function' constructor for code execution.
  - Timers:            no 'setTimeout', no 'setInterval', no 'requestAnimationFrame'.
  - I/O:               no 'console.log' (write to your return value instead).
  - DOM / browser:     no 'window', no 'document', no 'localStorage'.
  - Node-isms:         no 'Buffer', no '__dirname', no '__filename'.

There is NO shared state across invocations. Each call gets a fresh, isolated execution environment.

=============================================================================
QUOTAS AND LIMITS — both abort the script
=============================================================================

  1. SCRIPT TIMEOUT (wall-clock, typically ~10s). If your function does not
     resolve in time, the VM is interrupted and the tool call returns
     IsError=true with a message containing "script timeout".

  2. TOOL-CALL QUOTA (typically 50 calls per invocation). Each call to
     codemode.<upstream>.<toolName>(...) or codemode.call(...) counts as ONE. Exceeding
     the quota throws inside the script with a message containing
     "max_tool_calls exceeded". This is NOT catchable — the script terminates.

Plan accordingly:
  - Avoid unbounded loops over upstream results. Always 'slice' or paginate.
  - Avoid 'Promise.all' fan-outs of more than ~10 tool calls; they all count.
  - Prefer composing a small number of richer calls over many tiny calls.

=============================================================================
SANDBOX API — full surface
=============================================================================

  await codemode.tools()
    Returns Array<ToolDefinition> visible to you. Same shape and same
    per-user filtering as codemode_search. Does NOT count against the
    tool-call quota.

    ToolDefinition = {
      "name":        string,
      "description": string,
      "inputSchema": object,   // JSON Schema; may be {}
      "upstream":    string
    }

  await codemode.<upstream>.<toolName>(args)
    Direct, namespaced call. '<upstream>' must be the EXACT 'upstream'
    value from the catalog. '<toolName>' must be the EXACT 'name' from
    the catalog. 'args' is a plain object whose keys/values match the
    tool's inputSchema. Counts as 1 tool call against the quota.

    Namespacing is mandatory: two upstreams may legitimately expose the
    same tool name (e.g. both a 'github' and a 'gitlab' upstream expose
    'search_issues'), so there is NO flat 'codemode.<toolName>' shortcut.
    The catalog 'upstream' field tells you which prefix to use.

    Upstream OR tool names that are not valid JS identifiers (contain
    '-', '.', start with a digit, etc.) CANNOT be reached as a property
    chain — use codemode.call() instead, or bracket notation
    (codemode["my-upstream"]["some-tool"](args)).

    Returns: the upstream tool's MCP "content" array, of the form
        [ { type: "text", text: "..." }, ... ]
    Most upstream tools return a single text block whose text is either a
    plain string OR JSON. If you need the parsed value, do:
        const r = await codemode.github.search_issues({...});
        const text = r?.[0]?.text ?? "";
        const data = JSON.parse(text);   // wrap in try/catch if uncertain

  await codemode.call(upstream, name, args)
    String-keyed call. Use this when the upstream or tool name is
    computed at runtime or contains characters not valid as JS
    identifiers. Behaves identically to the property-chain form.
    Counts as 1 tool call. Both 'upstream' and 'name' are REQUIRED —
    there is no single-argument form.

  RESERVED KEYS: 'codemode.tools' and 'codemode.call' are the sandbox
  API and ALWAYS resolve to the built-ins above. An upstream registered
  under the literal name "tools" or "call" is NOT reachable as a
  property; reach it via codemode.call("tools", "<toolName>", args).

=============================================================================
ERROR SHAPE FROM TOOL CALLS
=============================================================================

When a tool call cannot complete, the returned promise REJECTS with an Error
whose 'message' contains a kind tag you can match on (case-insensitive):

  "needs_relink"            — the user's link to this upstream needs a fresh
                              OAuth dance; this tool will keep failing until
                              the user re-links.
  "no_link"                 — the user has no link to this upstream at all.
  "auto_disabled"           — the link has been auto-disabled by the gateway
                              due to repeated failures.
  "upstream_unavailable" /
  "circuit ... open"        — the resilience layer is shedding load; retry
                              later may succeed.
  "401" / "403"             — auth was rejected by the upstream even after a
                              forced token refresh.
  "5xx" / "network"         — the upstream is broken or unreachable.

Wrap individual calls in try/catch if you want to recover. Otherwise the
rejection bubbles up and the WHOLE script fails with IsError=true.

=============================================================================
COMMON RECIPES — copy and adapt
=============================================================================

(1) Single tool call, raw passthrough:

    async () => {
      return await codemode.jira.get_ticket({ id: "PROJ-123" });
    }

(2) Single call, parse the JSON text result:

    async () => {
      const r = await codemode.jira.get_ticket({ id: "PROJ-123" });
      const text = r?.[0]?.text ?? "";
      try { return JSON.parse(text); }
      catch { return { raw: text }; }
    }

(3) Compose two tools across different upstreams (the upstream prefix
    disambiguates even when both upstreams expose the same tool name):

    async () => {
      const ticket = await codemode.jira.get_ticket({ id: "PROJ-123" });
      const pr     = await codemode.github.get_pr({ number: 456 });
      await codemode.jira.update_ticket({
        id: "PROJ-123",
        description: "Linked PR: " + (pr?.[0]?.text ?? "unknown"),
      });
      return { updated: "PROJ-123" };
    }

(4) Discover then call (single round-trip — no need for codemode_search first):

    async () => {
      const tools = await codemode.tools();
      const t = tools.find(x => x.name.includes("get_ticket"));
      if (!t) return { error: "no get_ticket tool available" };
      return await codemode.call(t.upstream, t.name, { id: "PROJ-123" });
    }

(5) Fan out, BOUNDED — remember the tool-call quota:

    async () => {
      const ids = ["PROJ-1", "PROJ-2", "PROJ-3"];   // keep this list small
      const out = [];
      for (const id of ids) {
        out.push({ id, ticket: await codemode.jira.get_ticket({ id }) });
      }
      return out;
    }

(6) Fan out tolerating per-call failure:

    async () => {
      const ids = ["PROJ-1", "PROJ-2", "PROJ-3"];
      const out = [];
      for (const id of ids) {
        try {
          out.push({ id, ok: true, ticket: await codemode.jira.get_ticket({ id }) });
        } catch (e) {
          out.push({ id, ok: false, error: String(e?.message || e) });
        }
      }
      return out;
    }

(7) Parallel calls (Promise.all) — each counts against the quota:

    async () => {
      const [ticket, pr] = await Promise.all([
        codemode.jira.get_ticket({ id: "PROJ-123" }),
        codemode.github.get_pr({ number: 456 }),
      ]);
      return { ticket, pr };
    }

(8) Dynamic / non-identifier names (upstream OR tool contains '-', '.', etc.):

    async () => {
      // Either of these works; pick whichever reads better:
      return await codemode.call("github-enterprise", "list-repos", { org: "myorg" });
      // return await codemode["github-enterprise"]["list-repos"]({ org: "myorg" });
    }

(9) Detect a needs-relink failure and report it cleanly:

    async () => {
      try {
        return await codemode.jira.get_ticket({ id: "PROJ-123" });
      } catch (e) {
        const msg = String(e?.message || e).toLowerCase();
        if (msg.includes("needs_relink") || msg.includes("no_link")) {
          return { error: "please re-link Jira in the portal", detail: String(e) };
        }
        throw e;   // unknown failure — let it bubble
      }
    }

=============================================================================
WORKFLOW
=============================================================================

If you already know the tool name and its argument schema, call
codemode_execute directly. Otherwise call codemode_search first (or use
'await codemode.tools()' at the top of your script) to discover what is
available, then invoke the tools you need. Either way, return ONE
JSON-serializable value at the end.`),
		mcp.WithString("code",
			mcp.Required(),
			mcp.Description(`A single JavaScript expression that evaluates to a zero-argument async arrow function. Exact required shape: async () => { ... }. The function will be invoked once, its returned promise awaited, and its resolved value JSON-encoded and returned to you. The function may invoke upstream tools via 'await codemode.<upstream>.<toolName>(args)' (where <upstream> and <toolName> are the exact 'upstream' and 'name' from codemode.tools() and both are valid JS identifiers) or via 'await codemode.call(upstream, name, args)' for any upstream/tool name. Tools are upstream-namespaced because the same tool name may legitimately exist on multiple upstreams (e.g. both a github and a gitlab upstream may expose 'search_issues') — there is NO flat 'codemode.<toolName>' shortcut. It may also inspect the per-user catalog via 'await codemode.tools()'. Must return a JSON-serializable value (object, array, string, number, boolean, or null). Subject to a server-configured script timeout (~10s wall-clock) AND a per-invocation tool-call quota (~50 calls); exceeding either aborts the script with an error result. Sandbox has no filesystem, network (other than codemode.*), eval, require, timers, or console — only standard ES2015+ JavaScript plus the codemode global.`),
		),
	)

	s.core.AddTool(searchTool, s.handleSearch)
	s.core.AddTool(executeTool, s.handleExecute)
}

// handleSearch runs the supplied JS through CodeModeHandler.Search,
// which exposes only codemode.tools() — no upstream dispatch. Argument
// validation and handler errors are surfaced as MCP error results
// (IsError=true) rather than transport-level errors so the client LLM
// can react to them programmatically.
func (s *MCPServer) handleSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	code, ok := req.GetArguments()["code"].(string)
	if !ok {
		return errorResult("code argument must be a string"), nil
	}

	s.logger.Debug("codemode_search: received script",
		zap.Int("script_bytes", len(code)),
		zap.String("script", code))

	result, err := s.handler.Search(ctx, code)
	if err != nil {
		s.logger.Debug("codemode_search: handler error", zap.Error(err))
		return errorResult(fmt.Sprintf("search failed: %v", err)), nil
	}

	s.logger.Debug("codemode_search: handler result",
		zap.String("result_json", marshalForDebug(result)))
	return successResult(result), nil
}

// handleExecute runs the supplied JS through CodeModeHandler.Execute,
// which exposes codemode.tools() plus per-tool proxies bound to the
// caller's tenant + user context. Same error-shaping rule as Search.
func (s *MCPServer) handleExecute(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	code, ok := req.GetArguments()["code"].(string)
	if !ok {
		return errorResult("code argument must be a string"), nil
	}

	s.logger.Debug("codemode_execute: received script",
		zap.Int("script_bytes", len(code)),
		zap.String("script", code))

	result, err := s.handler.Execute(ctx, code)
	if err != nil {
		s.logger.Debug("codemode_execute: handler error", zap.Error(err))
		return errorResult(fmt.Sprintf("execute failed: %v", err)), nil
	}

	s.logger.Debug("codemode_execute: handler result",
		zap.String("result_json", marshalForDebug(result)))
	return successResult(result), nil
}

// marshalForDebug JSON-encodes v for debug-only logging. Encoding errors
// fall back to a Go fmt %#v dump so the log line is never empty.
func marshalForDebug(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("<marshal error: %v> %#v", err, v)
	}
	return string(b)
}

// errorResult wraps a human-readable message as an MCP tool error. The
// MCP protocol distinguishes transport errors (returned as the second
// return value) from in-band tool errors (IsError=true on a successful
// result); we use the latter so the calling LLM stays in control.
func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{Type: "text", Text: msg},
		},
		IsError: true,
	}
}

// successResult JSON-encodes the handler's return value as a single
// text content block. Encoding errors are swallowed because the handler
// already constrains data to JSON-serializable shapes.
func successResult(data any) *mcp.CallToolResult {
	jsonBytes, _ := json.Marshal(data)
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{Type: "text", Text: string(jsonBytes)},
		},
	}
}

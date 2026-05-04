# Upstreams

Upstream servers are the MCP-compatible services that Limen aggregates behind a single endpoint. This guide covers upstream configuration, connection behavior, and troubleshooting.

## Definition

An upstream is any MCP-compatible server that supports the **Streamable HTTP** transport. Limen connects to each configured upstream at startup, initializes the MCP session, and fetches the full tool list. These tools then become available to clients through Code Mode or direct proxying.

## Configuration

Each upstream is defined in the YAML configuration under the `upstreams` section:

```yaml
upstreams:
  - name: github
    url: https://api.example.com/mcp
    headers:
      Authorization: "Bearer ${GITHUB_TOKEN}"
      X-Api-Version: "2024-01-01"
    timeout: 15s

  - name: jira
    url: https://jira.example.com/mcp
    headers:
      Authorization: "Bearer ${JIRA_API_KEY}"
    timeout: 30s
```

### Config Fields

| Field | Required | Description |
|-------|----------|-------------|
| `name` | Yes | Unique identifier. Used as a prefix for tool names (e.g., `github_search_issues`). |
| `url` | Yes | Full URL of the upstream MCP server endpoint. Must use Streamable HTTP transport. |
| `headers` | No | HTTP headers sent with every request. Supports `${ENV_VAR}` substitution. |
| `timeout` | No | Per-upstream request timeout. Defaults to gateway-level timeout if omitted. |

## Authentication

Authentication is handled through HTTP headers on the upstream configuration. The most common patterns:

### Bearer Tokens

```yaml
headers:
  Authorization: "Bearer ${UPSTREAM_TOKEN}"
```

### API Keys

```yaml
headers:
  X-API-Key: "${API_KEY}"
```

### Multiple Headers

Headers are applied to every request to that upstream, including initialization, tool listing, and tool calls:

```yaml
headers:
  Authorization: "Bearer ${TOKEN}"
  X-Team-Id: "${TEAM_ID}"
  Content-Type: "application/json"
```

## Environment Variables

Limen substitutes `${VAR_NAME}` patterns in config values with values from the process environment at startup.

### Setting Variables

```bash
# Via shell export
export GITHUB_TOKEN="ghp_xxxx"
export JIRA_API_KEY="jira_xxxx"

# Or via .env file (not committed)
cat .env
GITHUB_TOKEN=ghp_xxxx
JIRA_API_KEY=jira_xxxx

# Load before starting
set -a && source .env && set +a
./limen -config config.yaml
```

### Syntax Rules

- Pattern: `${VAR_NAME}` -- curly braces are required
- If the variable is unset, the literal string `${VAR_NAME}` remains in the config value
- Variable names are case-sensitive
- Only string values are supported (no arrays or objects)

## Timeouts

Each upstream can have its own request timeout:

```yaml
upstreams:
  - name: github
    url: https://api.example.com/mcp
    timeout: 10s    # Fast upstream -- strict timeout

  - name: jira
    url: https://jira.example.com/mcp
    timeout: 30s    # Slower upstream -- more lenient
```

Timeouts apply to individual HTTP requests to the upstream server. If an upstream does not respond within the configured duration, the request fails with a timeout error.

### Choosing Timeout Values

| Upstream Type | Suggested Timeout |
|---------------|------------------:|
| Local / same-region | 5-10s |
| Cross-region API | 15-30s |
| Slow / batch-processing | 30-60s |

Set timeouts based on the upstream's expected response latency. Too tight and you'll get false failures; too loose and slow upstreams will block agent workflows.

## Connection Flow

Limen manages the MCP protocol lifecycle automatically. The sequence for each upstream:

```
1. Initialize     POST to upstream URL with MCP initialization request
   │                 → Negotiates protocol version and capabilities
   ▼
2. ListTools      Requests full tool catalog from upstream
   │                 → Merges tool names with upstream prefix (e.g., "github_")
   ▼
3. Ready          Upstream tools are registered and available for client calls
```

When a client calls a tool through Code Mode:

```
4. CallTool       Limen forwards the tool call to the correct upstream
   │                 → Maps tool name back to upstream, sends args
   ▼
5. Response       Limen returns the upstream's response to the client
```

All of this is handled internally. Clients interact only with Limen's endpoint.

## Protocol

Limen communicates with upstreams using the MCP specification:

- **Protocol version**: `LATEST_PROTOCOL_VERSION` as defined by the MCP spec
- **Transport**: Streamable HTTP -- bidirectional HTTP-based message exchange
- **Message format**: JSON-RPC 2.0 with MCP-specific method names

### Streamable HTTP

Streamable HTTP is the MCP specification's standard transport for remote server communication. It provides:

- Request/response over standard HTTP POST
- Server-sent events (SSE) for server-initiated messages
- Session management via HTTP cookies/headers

Limen uses this exclusively -- **stdio transport (local process execution) is not supported**. This is an intentional security decision to eliminate local supply chain risk.

## Troubleshooting

### Connection Timeout

```
ERROR upstream "github" failed to initialize: context deadline exceeded
```

**Causes:**
- Upstream server is down or unreachable
- Network firewall blocking the connection
- Timeout value is too low for the upstream's response time

**Fixes:**
- Verify the `url` field is correct and the server is running
- Test connectivity with `curl -v <url>`
- Increase the upstream `timeout` value

### Authentication Failure

```
ERROR upstream "jira" failed to initialize: 401 Unauthorized
```

**Causes:**
- Token in `${ENV_VAR}` is unset or expired
- Header key is misspelled (e.g., `Autorization` instead of `Authorization`)
- Token format is wrong (missing `Bearer ` prefix)

**Fixes:**
- Check the environment variable is set: `echo $JIRA_API_KEY`
- Verify header names are correct
- Confirm the token is valid and not expired

### Incompatible Protocol Version

```
ERROR upstream "internal" rejected initialization: unsupported protocol version
```

**Causes:**
- Upstream server is running an older MCP version
- Upstream does not support Streamable HTTP transport (only stdio)

**Fixes:**
- Update the upstream server to a version supporting Streamable HTTP
- Verify the upstream implements the MCP specification correctly
- Confirm the upstream accepts HTTP-based MCP connections (not stdio)

### Tool Not Found

```
ERROR tool "github_search_issues" not found
```

**Causes:**
- Tool name prefix doesn't match any configured upstream
- Upstream failed to initialize, so its tools were never registered
- Upstream doesn't expose that tool

**Fixes:**
- Check `name` field in upstream config matches the tool name prefix
- Look for upstream initialization errors in the gateway logs
- Verify the upstream server actually lists that tool (check upstream docs)

### Environment Variable Not Substituted

```
headers:
  Authorization: "Bearer ${MISSING_TOKEN}"   # literal string, not substituted
```

**Cause:**
- The environment variable is not set in the process environment.

**Fix:**
- Export the variable before starting Limen: `export MISSING_TOKEN=value`
- Confirm with `echo $MISSING_TOKEN` -- it should print the value, not be empty

# Configuration Reference

Limen is configured entirely via a YAML file. Pass the path at startup:

```bash
./limen -config config.yaml
```

## Quick Examples

### Single Upstream

```yaml
server:
  host: "0.0.0.0"
  port: 8080

upstreams:
  - name: "github"
    url: "https://api.github.com/mcp"
    headers:
      Authorization: "Bearer ${GITHUB_TOKEN}"
    timeout: "30s"

codemode:
  execution_timeout: "30s"
  max_memory_mb: 64
```

### Multiple Upstreams

```yaml
server:
  host: "0.0.0.0"
  port: 8080

upstreams:
  - name: "github"
    url: "https://api.github.com/mcp"
    headers:
      Authorization: "Bearer ${GITHUB_TOKEN}"
    timeout: "30s"

  - name: "jira"
    url: "https://jira.example.com/mcp"
    headers:
      Authorization: "Bearer ${JIRA_TOKEN}"
    timeout: "15s"

  - name: "internal-docs"
    url: "http://docs.internal:9000/mcp"
    timeout: "10s"

codemode:
  execution_timeout: "30s"
  max_memory_mb: 64
```

### Auth-Enabled

```yaml
server:
  host: "127.0.0.1"
  port: 8443

upstreams:
  - name: "github"
    url: "https://api.github.com/mcp"
    headers:
      Authorization: "Bearer ${GITHUB_TOKEN}"
    timeout: "30s"

codemode:
  execution_timeout: "60s"
  max_memory_mb: 128

zitadel:
  domain: "https://auth.example.com"
  auth_mode: "pat"
  pat: "${LIMEN_ZITADEL_PAT}"
  project_id: "my-zitadel-project-id"
  mcp_resource_audience: "my-zitadel-project-id"
```

---

## Full Reference

### `server`

Bindings for the HTTP/SSE server that LLM clients connect to.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `host` | `string` | `"0.0.0.0"` | Network interface to bind to. Use `127.0.0.1` for local-only access. |
| `port` | `int` | `8080` | HTTP port to listen on. |

### `upstreams`

A list of remote MCP servers to aggregate. Each upstream is connected at startup; if a connection fails, that upstream is skipped but the gateway continues with the remaining ones. The gateway will not start if **zero** upstreams connect successfully.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | `string` | Yes | -- | Unique identifier for this upstream. Used to namespace tools and in logs. Must not contain duplicate values across the list. |
| `url` | `string` | Yes | -- | MCP Streamable HTTP endpoint URL. Limen uses the `mcp-go` Streamable HTTP client to communicate. |
| `headers` | `map[string]string` | No | `{}` | Custom HTTP headers sent with every request to this upstream. Supports environment variable substitution with `${VAR_NAME}` syntax. Commonly used for `Authorization: "Bearer ${TOKEN}"`. |
| `timeout` | `duration` | No | `30s` | Per-request timeout for all operations with this upstream (connect, list tools, call tool). Parsed as Go duration string (e.g., `30s`, `1m`, `500ms`). |

### `codemode`

Settings for the JavaScript sandbox (Code Mode) that executes LLM-generated code.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `execution_timeout` | `duration` | `30s` | Maximum time allowed for a single JS execution (both `search` and `execute`). Prevents infinite loops. Parsed as Go duration string. If the timeout fires, the Goja VM is interrupted and an error is returned. |
| `max_tool_calls` | `int` | `50` | Maximum number of upstream tool invocations a single Code Mode script may issue. Exceeding this aborts the script with an uncatchable quota error. |
| `max_concurrent_tool_calls` | `int` | `8` | Maximum number of upstream tool calls allowed to be in flight at once. `Promise.all` fan-out beyond this cap is queued on a semaphore; total invocations are still bounded by `max_tool_calls`. |
| `max_memory_mb` | `int` | `64` | Intended cap on JS heap size. **Note:** This value is configured but not yet enforced in the runtime. Reserved for future implementation. |

### `zitadel`

Zitadel-specific settings. Drives both the OAuth proxy (Phase 5) and the
MCP Resource Server (Phase 6) — Limen validates inbound MCP access tokens
against Zitadel's JWKS (discovered from `oidc.issuer`) and rejects any
token whose `aud` claim does not contain `mcp_resource_audience`.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `domain` | `string` | Yes | -- | Zitadel domain (e.g. `https://auth.example.com`). |
| `auth_mode` | `string` | Yes | -- | Admin auth mode for the Management API — `pat` or `jwt`. |
| `pat` | `string` | Conditional | -- | Personal access token. Required when `auth_mode: pat`. |
| `jwt_key_path` | `string` | Conditional | -- | Path to service-user JWT key file. Required when `auth_mode: jwt`. |
| `project_id` | `string` | Yes | -- | Zitadel project id holding the MCP application. |
| `mcp_resource_audience` | `string` | Yes | -- | Expected `aud` claim on inbound MCP access tokens. Typically equals `project_id` or a configured Zitadel audience. |
| `http_timeout` | `duration` | No | `15s` | Timeout for outbound Zitadel Management API calls. |

## Environment Variable Substitution

The `headers` map in upstream config supports `${VAR_NAME}` syntax. The gateway reads these variables from the process environment at config load time. For example:

```yaml
upstreams:
  - name: "github"
    url: "https://api.github.com/mcp"
    headers:
      Authorization: "Bearer ${GITHUB_TOKEN}"
```

Before connecting, Limen resolves `${GITHUB_TOKEN}` to the value of the `GITHUB_TOKEN` environment variable. If the variable is unset, the literal string `""` (empty) is used.

Set environment variables before starting Limen:

```bash
export GITHUB_TOKEN=ghp_xxxx
export JIRA_TOKEN=atlassian_token_here
./limen -config config.yaml
```

## Duration Format

All `duration` fields use [Go's time.ParseDuration format](https://pkg.go.dev/time#ParseDuration). Common values:

| Value | Meaning |
|-------|---------|
| `500ms` | 500 milliseconds |
| `30s` | 30 seconds |
| `1m` | 1 minute |
| `2m30s` | 2 minutes 30 seconds |

## Defaults Summary

When omitted, these defaults are applied:

| Field | Default |
|-------|---------|
| `server.host` | `"0.0.0.0"` |
| `server.port` | `8080` |
| `upstreams[].timeout` | `30s` (Go zero-duration, so must be specified to override) |
| `codemode.execution_timeout` | `30s` |
| `codemode.max_tool_calls` | `50` |
| `codemode.max_concurrent_tool_calls` | `8` |
| `codemode.max_memory_mb` | `64` |
| `auth.enabled` | `false` |

## Startup Behavior

1. Load and parse YAML; apply defaults for missing fields.
2. For each upstream: create client, connect via Streamable HTTP, send MCP initialize handshake, list tools.
3. If an upstream fails to connect, log the error and skip it.
4. If **zero** upstreams connected, exit with a fatal error.
5. Start the SSE server on `host:port`.

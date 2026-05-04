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

auth:
  enabled: true
  jwks_url: "https://auth.example.com/.well-known/jwks.json"
  audience: "limen"
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
| `max_memory_mb` | `int` | `64` | Intended cap on JS heap size. **Note:** This value is configured but not yet enforced in the runtime. Reserved for future implementation. |

### `auth`

JWT/JWKS authentication for the SSE endpoint. When enabled, all requests to `/mcp` must include a valid Bearer token.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | `bool` | `false` | Whether to enforce auth middleware. When `false`, the `/mcp` endpoint is open. When `true`, a valid JWT is required (see note below). |
| `jwks_url` | `string` | `""` | URL of the JWKS (JSON Web Key Set) endpoint used to fetch public keys for JWT signature validation. Required when `enabled: true`. **Note:** JWKS validation is stubbed but not yet implemented (see [Roadmap](../README.md#roadmap) in README). |
| `audience` | `string` | `""` | Expected JWT `aud` (audience) claim. Tokens with a non-matching audience will be rejected. Required when `enabled: true`. |

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
| `codemode.max_memory_mb` | `64` |
| `auth.enabled` | `false` |

## Startup Behavior

1. Load and parse YAML; apply defaults for missing fields.
2. For each upstream: create client, connect via Streamable HTTP, send MCP initialize handshake, list tools.
3. If an upstream fails to connect, log the error and skip it.
4. If **zero** upstreams connected, exit with a fatal error.
5. Start the SSE server on `host:port`.

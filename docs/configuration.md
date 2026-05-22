# Configuration Reference

Limen is configured entirely via a YAML file. Pass the path at startup:

```bash
./limen -config config.yaml
```

## Quick Examples

### Minimal

```yaml
server:
  host: "0.0.0.0"
  port: 8080
  base_url: "https://limen.example.com"

database:
  dsn: "${LIMEN_DB_DSN}"

security:
  token_encryption_key: "${LIMEN_TOKEN_ENCRYPTION_KEY}"

codemode:
  execution_timeout: "30s"
  max_memory_mb: 64
```

### Auth-Enabled (Zitadel)

```yaml
server:
  host: "0.0.0.0"
  port: 8080
  base_url: "https://limen.example.com"

database:
  dsn: "${LIMEN_DB_DSN}"
  admin_dsn: "${LIMEN_DB_ADMIN_DSN}"

security:
  token_encryption_key: "${LIMEN_TOKEN_ENCRYPTION_KEY}"

codemode:
  execution_timeout: "60s"
  max_memory_mb: 128

oidc:
  issuer: "${LIMEN_OIDC_ISSUER}"
  client_id: "${LIMEN_OIDC_CLIENT_ID}"
  redirect_uri: "${LIMEN_OIDC_REDIRECT_URI}"
  scopes: [openid, profile, email, offline_access]

zitadel:
  domain: "${LIMEN_ZITADEL_DOMAIN}"
  auth_mode: "pat"
  pat: "${LIMEN_ZITADEL_PAT}"
  project_id: "${LIMEN_ZITADEL_PROJECT_ID}"
  mcp_resource_audience: "${LIMEN_ZITADEL_MCP_RESOURCE_AUDIENCE}"
```

> **Note:** Upstream MCP servers are no longer configured in YAML. They are stored
> per-tenant in the database and managed via `limen create-upstream` or the portal UI.
> See [upstreams.md](upstreams.md) for details.

---

## Full Reference

### `server`

Bindings for the HTTP/SSE server that LLM clients connect to.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `host` | `string` | `"0.0.0.0"` | Network interface to bind to. Use `127.0.0.1` for local-only access. |
| `port` | `int` | `8080` | HTTP port to listen on. |

### `upstreams`

Upstream MCP servers are **not** configured in YAML. They are stored per-tenant
in the database and managed through the `limen create-upstream` CLI command or
the portal UI. Each upstream is owned by a tenant and uses a
strategy-driven credential model (`none`, `static_header`, `mcp_spec`).

See [upstreams.md](upstreams.md) for a full guide to upstream configuration and
authentication strategies.

### `codemode`

Settings for the JavaScript sandbox (Code Mode) that executes LLM-generated code.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `execution_timeout` | `duration` | `30s` | Maximum time allowed for a single JS execution. Prevents infinite loops. Parsed as Go duration string. If the timeout fires, the Goja VM is interrupted and an error is returned. |
| `script_timeout` | `duration` | falls back to `execution_timeout` | Per-invocation wall-clock cap. Defaults to `10s` when zero; inherits `execution_timeout` if that is set and `script_timeout` is not, so existing configs keep working. |
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

### `signup`

Controls the self-serve tenant signup wizard exposed at `/signup` and the
`SignupService` Connect-RPC endpoints. Set `enabled: false` for closed
deployments — `StartSignup` then returns `Unavailable` and the SPA route
404s.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | `bool` | `true` | Master switch for the wizard + Connect handlers. |
| `rate_limit.per_hour` | `int` | `5` | Per-IP token-bucket refill rate for `StartSignup`. |
| `rate_limit.burst` | `int` | `3` | Per-IP token-bucket burst size. |
| `verify_token_ttl` | `duration` | `24h` | Lifetime of the single-use email-verification token. |

### `captcha`

Captcha provider used by `StartSignup`. The server-side `secret_key`
verifies tokens with the provider; the SPA reads `provider` + `site_key`
from `/auth/discovery` to lazy-load the matching widget.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `provider` | `string` | `none` | One of `none` (dev sentinel `dev-captcha-bypass`), `hcaptcha`, `turnstile`. |
| `site_key` | `string` | `""` | Public site key surfaced to the SPA. |
| `secret_key` | `string` | `""` | Provider secret. Required when `provider` is not `none`. |

### `mailer`

SMTP settings used to send the signup verification email. Required when
`signup.enabled: true`.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `smtp.host` | `string` | `localhost` | SMTP host. |
| `smtp.port` | `int` | `1025` | SMTP port (MailHog default in dev). |
| `smtp.from` | `string` | `Limen <noreply@limen.local>` | RFC 5322 `From:` header. |
| `smtp.username` | `string` | `""` | PLAIN auth username (empty disables auth). |
| `smtp.password` | `string` | `""` | PLAIN auth password. |
| `smtp.tls` | `string` | `starttls` | One of `none`, `starttls`, `tls`. |

## Environment Variable Substitution

All string scalars in the YAML config support `${VAR_NAME}` expansion. The
gateway substitutes values from the process environment before parsing YAML.
Two forms are supported:

| Syntax | Behaviour |
|--------|----------|
| `${VAR}` | Required — load error if `VAR` is unset |
| `${VAR:-fallback}` | Optional — uses `fallback` if `VAR` is unset or empty |

Example:

```yaml
database:
  dsn: "${LIMEN_DB_DSN}"
  admin_dsn: "${LIMEN_DB_ADMIN_DSN:-}"

zitadel:
  pat: "${LIMEN_ZITADEL_PAT}"
```

Set variables before starting Limen:

```bash
export LIMEN_DB_DSN="postgres://limen_app:pass@localhost/limen"
export LIMEN_ZITADEL_PAT="pat_xxxx"
./limen serve --config config.yaml
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
| `server.upstream_callback_path` | `"/mcp-servers"` |
| `codemode.execution_timeout` | `30s` |
| `codemode.script_timeout` | `10s` (or inherits `execution_timeout`) |
| `codemode.max_tool_calls` | `50` |
| `codemode.max_concurrent_tool_calls` | `8` |
| `codemode.max_memory_mb` | `64` |
| `upstream_refresh.interval` | `2m` |
| `upstream_refresh.refresh_window` | `5m` |
| `upstream_refresh.proactive_window` | `60s` |
| `upstream_refresh.fail_threshold` | `5` |
| `upstream_refresh.fail_window` | `15m` |
| `upstream_refresh.needs_relink_window` | `24h` |
| `upstream_refresh.catalog_interval` | `6h` |

## Startup Behavior

1. Load and parse YAML; apply environment-variable substitution; validate required fields.
2. Open Postgres connection pools (app + admin DSNs).
3. Load all active, provisioned upstreams for all tenants from the database.
4. Start the background upstream-refresh loop (`upstream_refresh` settings).
5. Start the HTTP/SSE server on `server.host:server.port`.

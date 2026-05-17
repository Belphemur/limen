# Phase 10 — Wiring, verification, hardening

**Depends on**: every previous phase
**Unblocks**: shipping

## Goal

Tie everything together in [cmd/gateway/main.go](../../cmd/gateway/main.go), refresh [config.yaml](../../config.yaml) and [AGENTS.md](../../AGENTS.md), run the full integration matrix, and apply the round of hardening that's only realistic after the parts are in place: timeouts, structured errors at boundaries, audit logging hooks, and a runbook section for operators.

This phase ships no new feature surface. Anything missed earlier gets caught here.

## Design

### `cmd/gateway/main.go` startup order

```
1. parse flags + load config (substitute ${ENV}, validate)
2. dispatch CLI subcommands (-create-tenant, -create-upstream, -migrate) → exit
3. open storage.Store (appDB + adminDB pools, Phase 1/3)
4. run AutoMigrate (Phase 1) using adminDB
5. run RLS migration (Phase 3) using adminDB
6. initialize crypto.Cipher and call crypto.SetCipher (Phase 2)
7. construct the shared `*zitadel.Client` (Phase 4) — reused by tenant CLI, DCR proxy (Phase 5), and portal admin RPCs (Phase 9)
8. construct OIDC RelyingParty (Phase 4) for portal login
9. construct upstream.Registry, register strategies (mcp_spec, none) (Phase 7)
10. start upstream.Refresher goroutine (Phase 7)
11. construct DBAuthProvider (Phase 8)
12. construct UpstreamManager + Gateway (Phase 8)
13. construct oauthproxy handlers (metadata + DCR proxy + redirector) (Phase 5)
14. construct JWKSResolver + auth.Middleware against the Zitadel issuer (Phase 6)
15. construct PortalService (Phase 9)
16. assemble chi router with the full route tree (below)
17. start HTTP server with read/write/idle timeouts and graceful shutdown
```

### Final route tree

```
/                                                    (operator landing or 404)
/healthz                                              health check
/auth/login                                          OIDC start (state + redirect to Zitadel)
/auth/callback                                       OIDC callback → set portal session
/auth/logout                                         portal logout → Zitadel end_session
/t/{tenant}/                                          RequireTenant
  /portal/                                           SPA static + fallback
    /api/portal.v1.PortalService/*                   Connect-RPC (interceptors)
  /oauth/
    /.well-known/openid-configuration                public (rewritten Zitadel metadata)
    /.well-known/oauth-authorization-server          public (rewritten Zitadel metadata)
    /authorize, /userinfo, /jwks, /end_session       302 redirect to Zitadel
    /token, /revoke, /introspect                     307 redirect (or proxy) to Zitadel
    /register, /register/{id}                        DCR proxy → Zitadel Management API
  /mcp/.well-known/oauth-protected-resource          public
  /mcp, /mcp/                                         RequireMCPAuth → Gateway
  /upstream/{name}/{connect,callback,disconnect}     RequirePortalSession
```

### Config example (`config.yaml`)

The shipped example is full but commented:

```yaml
server:
  bind: ":8080"
  base_url: "${LIMEN_BASE_URL}"
  read_timeout: 30s
  write_timeout: 30s
  idle_timeout: 120s
  shutdown_timeout: 15s

database:
  driver: postgres
  dsn: "${LIMEN_DB_DSN}"
  owner_dsn: "${LIMEN_DB_OWNER_DSN}"
  max_open_conns: 25
  max_idle_conns: 5

security:
  token_encryption_key: "${LIMEN_TOKEN_ENCRYPTION_KEY}"
  portal_session_cookie_name: "limen_portal"
  portal_session_cookie_secure: true
  portal_session_cache_ttl: 60s # local positive cache for Zitadel SessionService.GetSession

oidc:
  issuer: "${LIMEN_OIDC_ISSUER}" # e.g. https://auth.limen.example.com
  portal_client_id: "${LIMEN_OIDC_PORTAL_CLIENT_ID}"
  portal_client_secret: "${LIMEN_OIDC_PORTAL_CLIENT_SECRET}" # empty for PKCE public client
  redirect_uri: "${LIMEN_BASE_URL}/auth/callback"
  scopes: ["openid", "profile", "email", "offline_access"]
  mcp_rs_audience: "${LIMEN_OIDC_MCP_RS_AUDIENCE}" # Zitadel project audience id

oauth_proxy:
  dcr_enabled: true
  dcr_initial_access_token: "" # if set, /register requires it
  rate_limit:
    rps: 10
    burst: 20
  # Zitadel project / management credentials are reused from the
  # top-level `zitadel:` block (single shared client) — not duplicated here.

upstream_refresher:
  interval: 2m
  refresh_window: 5m

codemode:
  enabled: true
  max_execution_ms: 5000

logging:
  level: info
  encoding: json
```

The old `upstreams:` block and the old `auth:` block are **removed**. Upstreams now live in the DB (created via CLI or portal); inbound auth is JWT-validated per request against Zitadel's JWKS.

### `AGENTS.md` updates

The current `AGENTS.md` is rewritten in the following sections (not the whole file):

- **Architecture** — extend with `internal/storage/`, `internal/crypto/`, `internal/tenancy/`, `internal/auth/`, `internal/oauthproxy/`, `internal/mcprs/`, `internal/upstream/`, `internal/portal/`, `web/`, `proto/`.
- **Build & Test Commands** — add `buf generate`, `pnpm install`, `pnpm build`, `docker compose -f compose.dev.yaml up -d` (Phase 0), integration-test commands.
- **Setup** — Postgres role provisioning snippet (`limen_admin` + `limen_app`); Zitadel bootstrap script (Phase 0).
- **Testing** — describe `testcontainers-go` setup, the integration test scenarios listed below.
- **Security** — production posture (RLS, Zitadel JWKS, AAD-bound encryption, delegated identity).

### CLI subcommands (recap)

Implemented in Phase 4, finalized here:

```
limen create-tenant name="Acme Corp" owner-email=admin@acme.com
limen -invite-user tenant=acme email=alice@acme.com role=member
limen -create-upstream tenant=acme name=atlassian strategy=mcp_spec url=https://mcp.atlassian.com/v1/mcp/authv2
limen -migrate                                       # run schema + RLS migrations and exit
limen                                                # normal server start
```

`-create-tenant` and `-invite-user` go through the Zitadel Management API (Phase 4 / 5). Password resets are performed via Zitadel's hosted self-service UI — not via a Limen CLI. `-create-upstream` runs `Strategy.Provision` (which performs DCR against external MCP servers like Atlassian for `mcp_spec`) and writes the upstream rows.

### Hardening pass

- **HTTP timeouts** on the server (read/write/idle) — defaults above.
- **Outbound HTTP timeouts** on every `http.Client` (upstreams, JWKS, DCR). Defaults: 30 s for upstream calls, 3 s for JWKS, 10 s for DCR.
- **Resilience stack** — every outbound HTTP client is built through `internal/resilience.Client(name, cfg)`, which layers (innermost → outermost):
  1. **Per-request timeout** (context deadline propagated through ctx).
  2. **Retry with exponential backoff + jitter** via `github.com/cenkalti/backoff/v4`. Retries only on transport errors, `502/503/504`, and `429` (honoring `Retry-After` when present). `4xx` other than `429` and `408` is terminal. Idempotent verbs (`GET`, `HEAD`, `OPTIONS`, `PUT`, `DELETE`) and explicitly-marked POSTs (token refresh, DCR — idempotent by replay) are retryable; arbitrary POSTs are not.
  3. **Circuit breaker** via `github.com/sony/gobreaker/v2`. One breaker per named dependency (e.g. `upstream.atlassian`, `zitadel.session`, `zitadel.jwks`, `dcr.atlassian`). When open, requests fail fast with a typed `*resilience.BreakerOpenError`, which callers map to the right user-facing surface (MCP "re-link / try again" error, portal `unavailable` Connect-RPC error, etc.). Half-open trial count and open duration are configurable.
  4. **Structured logging** on every retry, breaker state transition, and final failure, with the dependency name + request ID.

  Defaults per dependency family:

  | Family                       | Max retries | Base / max backoff | Breaker: consecutive fails → open | Open duration |
  | ---------------------------- | ----------- | ------------------ | --------------------------------- | ------------- |
  | Upstream MCP tool calls      | 2           | 250 ms / 2 s       | 5                                 | 30 s          |
  | Upstream OAuth token refresh | 3           | 500 ms / 5 s       | 5                                 | 60 s          |
  | Upstream DCR / discovery     | 2           | 500 ms / 5 s       | 3                                 | 5 min         |
  | Zitadel `SessionService`     | 2           | 100 ms / 1 s       | 10                                | 15 s          |
  | Zitadel `UserService` (mgmt) | 2           | 250 ms / 2 s       | 5                                 | 60 s          |
  | Zitadel JWKS fetch           | 3           | 100 ms / 1 s       | 5                                 | 30 s          |

  Values are config-driven; the table is the shipped default. All policies respect the request `context.Context` — cancellation aborts retries immediately.

  **Per-upstream MCP tool calls (Phase 8 integration point).** The auth-injecting `http.RoundTripper` defined in [internal/gateway/upstream.go](../../internal/gateway/upstream.go) (see [Phase 8](phase-08-per-tenant-injection.md)) wraps a `base` `http.RoundTripper` that **must** be the one inside the `*http.Client` returned by `resilience.Client("upstream.<name>.calls", cfg)`. Layering matters: the auth wrapper is the **outer** transport (it sees the response status to decide on the single 401-refresh retry), while resilience is the **inner** transport (it owns timeout / backoff / breaker for every physical attempt the auth wrapper makes). Both inner attempts of the auth wrapper's 401 retry therefore inherit the same resilience policy independently — there is no "shared retry budget" between the bearer-refresh retry and the resilience retry, by design.

  Concretely:

  - One `resilience.Client("upstream.<name>.calls", cfg)` per upstream is constructed when the `UpstreamManager` builds its per-(tenant, upstream) `Bundle`. The breaker name carries the upstream's logical name (not the URL) so a per-tenant override of `mcp_server_url` does not silently create a new breaker.
  - A `*resilience.BreakerOpenError` returned from `RoundTrip` is mapped to a structured MCP `upstream_unavailable` error (not a 500), counts as a failure for the Phase 7 / 8 auto-disable bookkeeping (`LastFailureReason = breaker_open`), and **never** triggers the bearer-refresh retry — a 401 requires an HTTP response, and the breaker short-circuits before one exists.
  - A 401 from the upstream goes through the bearer-refresh retry exactly once. If the second attempt also returns 401, the round-tripper returns the structured re-link error and records `LastFailureReason = tool_call_401`; resilience does not retry 401s.
  - The same `resilience.Client` is reused by the catalog indexer in [internal/upstream/catalog.go](../../internal/upstream/catalog.go) so periodic `tools/list` sweeps inherit the same breaker as live tool calls. A wedged upstream therefore stops generating both real traffic and indexer noise the instant the breaker opens.

- **Graceful shutdown**: catch `SIGTERM`/`SIGINT`, stop accepting new connections, wait `shutdown_timeout`, then force-close.
- **Goroutine accounting**: the refresher, janitor (session cleanup, expired auth-code/refresh-token cleanup) all run via a `*errgroup.Group` with a top-level ctx; shutdown cancels them.
- **Structured logging**: every handler logs `tenant_id`, `user_id`, `request_id` (via `chi/middleware.RequestID` + a custom field injector). Errors logged at `Error`; informational events at `Info`; tracing detail at `Debug`.
- **Health endpoint**: `GET /healthz` returns 200 if `Store.Ping()` succeeds, 503 otherwise.
- **No panics**: chi's `Recoverer` middleware is mounted globally; panic logs include the request ID.
- **Audit hooks**: a small event emitter in `internal/audit/` writes records for sensitive events (`tenant_created`, `user_invited`, `dcr_succeeded`, `upstream_linked`, `upstream_disconnected`, `mcp_client_revoked`, `portal_login`, `portal_logout`). v1 writes them to the structured logger; a DB table or external sink is future work. Zitadel's own audit log covers identity-side events (logins, MFA, password resets).

### Integration test matrix

All under `tests/integration/` using `testcontainers-go` for Postgres. A `make test-integration` target spins them.

| #   | Scenario                                                                                                                               |
| --- | -------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | Inbound discovery chain: 401 → PRM → AS metadata → DCR proxy → authorize on Zitadel → token → /mcp 200 (against dev Zitadel container) |
| 2   | Cross-tenant rejection: valid Zitadel token whose `org_id` is tenant A used at `/t/B/mcp` → 403                                        |
| 3   | Cross-tenant DB isolation under Postgres RLS                                                                                           |
| 4   | `mcp_spec` connect: DCR against the upstream happens once per (tenant, upstream); user connect lands an `UpstreamLink`                 |
| 5   | `none` upstream: tools visible without a link; calls succeed without bearer to upstream                                                |
| 6   | Tool visibility: `mcp_spec` upstream hidden until link exists; reappears after connect; disappears after disconnect                    |
| 7   | Refresher: a near-expiring `UpstreamLink` is refreshed in-place                                                                        |
| 8   | Portal role enforcement: matrix of (role × RPC) returns expected `permission_denied`/`ok`                                              |
| 9   | Cross-tenant portal cookie: cookie for tenant A on tenant B path → unauthenticated                                                     |
| 10  | OIDC login flow: tenant `PublicID` in state → callback validated → portal cookie set with correct Path                                       |
| 11  | DCR proxy: registering creates a Zitadel app in the tenant's org and a `ZitadelApp` mirror row; deleting via RFC 7592 removes both     |
| 12  | Full Atlassian Rovo manual smoke (documented runbook, not automated)                                                                   |

All scenarios run against the `postgres:18-alpine` testcontainer ([Phase 0](phase-00-dev-environment.md)'s dev stack mirrors the same image).

### Operator runbook (new section in `docs/`)

Out of scope for the docs Phase 10 ships, but the checklist below tracks "create runbook" so it's not forgotten. Topics:

- Generating the encryption key.
- Provisioning Postgres roles + database with the right ownership (matches the [Phase 11](phase-11-production-deployment.md) compose).
- Bootstrapping Zitadel: instance setup, project + apps creation, service account PAT issuance.
- Creating the first tenant + owner.
- Rotating the encryption key and the Zitadel PAT.
- Backup/restore expectations (encrypted columns are useless without the master key; Zitadel data is separately backed up).
- Monitoring: which log fields to alert on (DCR proxy 5xx rate, JWKS fetch failures, upstream refresh failure rate).

## Deliverables

- Updated `cmd/gateway/main.go` orchestrating all components.
- Updated `config.yaml` example.
- Updated `AGENTS.md`.
- New `internal/audit/` skeleton (just an emitter interface + log sink in v1).
- Optional new `internal/cli/create_upstream.go`.
- `tests/integration/` package with the scenarios above.
- `docs/runbook.md` skeleton.

## Verification

- All integration scenarios pass on the CI Postgres container.
- Manual smoke against the real Atlassian Rovo MCP server end-to-end: portal connect → VS Code MCP client → tool listing → tool call.
- `go vet ./...` clean. `golangci-lint` (if configured) clean.
- `pnpm lint` and `pnpm typecheck` clean.

## Risks

- **Integration test runtime**: 12 scenarios with testcontainers can grow slow. Parallelize where independent; keep DB fixtures minimal.
- **Atlassian-specific drift**: a future change in their MCP/OAuth spec compliance might require small adjustments — keep the `mcp_spec` strategy lenient on optional fields.
- **`AGENTS.md` divergence**: easy to forget to update. Add a CI lint that fails if `AGENTS.md`'s "Architecture" section enumerates a directory that doesn't exist (or vice versa) — optional, but cheap insurance.

## Checklist

- [ ] `cmd/gateway/main.go` rewritten to wire all components in the documented order
- [ ] CLI dispatch (`-create-tenant`, `-reset-password`, optional `-create-upstream`) implemented and tested
- [ ] HTTP server has read/write/idle timeouts from config
- [ ] `internal/resilience/` package exposes `Client(name, cfg) *http.Client` wrapping timeout + backoff (`cenkalti/backoff/v4`) + circuit breaker (`sony/gobreaker/v2`)
- [ ] Every outbound HTTP client in the codebase (upstream MCP, upstream OAuth refresh, upstream DCR, Zitadel Session/User/JWKS) is constructed through `resilience.Client` with a named breaker
- [ ] The Phase 8 upstream MCP transport — both the per-request tool-call client and the [`internal/upstream/catalog.go`](../../internal/upstream/catalog.go) indexer client — is wired so the auth-injecting `http.RoundTripper` wraps the `resilience.Client("upstream.<name>.calls", cfg)` transport as its `base`, sharing a single named breaker per upstream
- [ ] Retry policy: transport errors + `502/503/504/429` only; honors `Retry-After`; respects ctx cancellation; non-idempotent POSTs are not retried by default
- [ ] `BreakerOpenError` is a typed sentinel; callers map it to MCP "upstream unavailable" and Connect-RPC `unavailable` codes
- [ ] Per-dependency resilience config in `config.yaml`; shipped defaults match the table in Phase 10
- [ ] Unit tests assert: retry on 503, no retry on 401, ctx cancellation aborts retries, breaker opens after N consecutive failures and half-opens after the configured duration
- [ ] Graceful shutdown via SIGTERM/SIGINT; background goroutines cancel via errgroup
- [ ] `/healthz` endpoint added
- [ ] Chi `Recoverer` and `RequestID` middleware mounted globally
- [ ] Structured logger injects `tenant_id`, `user_id`, `request_id` per request
- [ ] `internal/audit/` skeleton with sensitive-event emitter (log sink in v1)
- [ ] `config.yaml` example updated with all sections; old `upstreams:` / `auth:` blocks removed
- [ ] `AGENTS.md` architecture, build, setup, testing, security sections updated
- [ ] `docs/runbook.md` drafted (encryption key, Postgres roles, first-tenant, signing-key rotation, backup, monitoring)
- [ ] Integration test scenarios 1–12 implemented and passing on Postgres
- [ ] Manual Atlassian Rovo smoke documented in the runbook
- [ ] `go vet ./...` clean
- [ ] `pnpm typecheck && pnpm lint` clean in `web/`
- [ ] CI workflow runs: lint + Go tests + integration tests + SPA build

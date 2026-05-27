---
phase: "10"
title: "Wiring, hardening, resilience"
status: in_progress
progress: 60
depends_on: ["0", "1", "2", "3", "4", "5", "6", "7", "8", "9a", "9b", "9c", "9d"]
updated: "2026-05-15"
---

# Phase 10 — Wiring, Hardening, Resilience

**Status: Core complete. Distributed circuit breaker (10b) planned. Integration tests deferred.**

**Depends on**: every previous phase
**Unblocks**: shipping

## Phase 10 (Core) — COMPLETED

### Dependencies & Config

- Added `github.com/sony/gobreaker/v2` (v2.4.0) and `github.com/cenkalti/backoff/v4` (v4.3.0) as direct dependencies in `go.mod`.
- Added `ResilienceConfig` (`Policies map` + `Defaults`) and `ResiliencePolicy` struct (`MaxRetries`, `BaseBackoff`, `MaxBackoff`, `BreakerConsecutiveFails`, `BreakerOpenDuration`) to `internal/config/config.go:44-57`.
- Added 6 built-in named policies (see [Resilience Policy Defaults](#resilience-policy-defaults)).
- Added server timeout fields to `ServerConfig`: `ReadTimeout` (30s), `WriteTimeout` (30s), `IdleTimeout` (120s), `ShutdownTimeout` (15s) — `internal/config/config.go:139-142`.
- Updated `config.yaml` with `server` timeouts and a `resilience` section.

### Resilience Package (`internal/resilience/`)

The `Client(name, cfg, logger)` factory at `internal/resilience/resilience.go:16` builds an `*http.Client` whose transport layers (inner → outer):

```
[retry with exponential backoff + jitter] → [circuit breaker] → http.DefaultTransport
```

**Files:**

| File | Purpose |
|------|---------|
| `errors.go` | `BreakerOpenError` typed sentinel — callers can `errors.As` to map to MCP/Connect-RPC errors |
| `breaker.go` | gobreaker v2 circuit breaker using `*gobreaker.CircuitBreaker[struct{}]`. Trips on transport errors AND HTTP 5xx. Logs state transitions. |
| `retry.go` | Exponential backoff + jitter via cenkalti/backoff v4. Retries on transport errors, 502, 503, 504, and 429 only. Honors `Retry-After` header. Passes through 401/408 without retry. Respects context cancellation. Breaker-open errors short-circuit retry. |
| `resilience.go` | `Client(name, cfg, logger)` factory — assembles the transport stack. |
| `resilience_test.go` | 7 tests: retry on 503, no retry on 401, retry on 429 with Retry-After, context cancellation aborts retry, breaker opens, breaker half-open + close, breaker error string. **All PASS.** |

### Server Hardening

**`internal/boot/http.go`:**
- `RunHTTPServer` sets `ReadTimeout`, `ReadHeaderTimeout` (min 10s), `WriteTimeout`, `IdleTimeout` from config. Graceful shutdown uses configurable `ShutdownTimeout`.
- Added `RequestLogger` middleware — injects `request_id`, `method`, `path` into a per-request logger stored on context.
- Added `LoggerFromContext` helper for downstream handlers.

**All 4 serve binaries** (`serveall`, `servegateway`, `serveportal`, `servestaff`) mount: `middleware.Recoverer`, `middleware.RequestID`, `boot.RequestLogger` — all after `PermissiveCORS`.

### Audit Skeleton (`internal/audit/`)

| File | Purpose |
|------|---------|
| `audit.go` | `Event` struct (Action, Actor, Tenant, Target, Outcome, ClientIP, Metadata, Error, Timestamp), `Emitter` interface, `LogEmitter` (zap sink) |
| `audit_test.go` | 4 tests: Emit, auto-timestamp, all fields, interface compliance. **All PASS.** |

v1 writes audit events to the structured logger. A DB table or external sink is deferred to Phase 12 (per `docs/audit.md`).

### Resilience Wiring

**Gateway manager** (`internal/gateway/manager.go:33-38`):
`ManagerOptions` gained a `ResiliencePolicy` field. In `buildBundle` (`manager.go:381`):
```go
resilienceClient := resilience.Client("upstream."+up.Identifier+".calls", m.opts.ResiliencePolicy, m.opts.Logger)
```
The resilience client's `Transport` is used as the `Base` of `AuthInjectingTransport`. This means auth is the outer layer (sees response status for 401-refresh), resilience is the inner layer (owns timeout/backoff/breaker for every physical attempt).

**Boot wiring** (`internal/boot/mcpmount/mcpmount.go:34`):
```go
ResiliencePolicy: rt.Cfg.Resilience.Resolve("upstream.tool_calls"),
```

**Catalog indexer** (`internal/upstream/catalog.go:52-60`):
`IndexUpstream` and `listUpstreamTools` accept an optional `httpClient *http.Client` parameter. All 4 callers currently pass `nil` (using default transport). When wired to the resilience client, the indexer shares the same breaker as live tool calls — a wedged upstream stops generating both real traffic and indexer noise.

### Documentation

- `docs/runbook.md`: encryption key generation, Postgres roles, Zitadel bootstrap, first tenant, secret rotation, backup/restore, monitoring, health endpoints, troubleshooting.
- `AGENTS.md` (now 327 lines): full architecture section, integration tests section, security section.

### Verification Results

```
go build ./...  — PASS
go vet ./...    — PASS
go test ./...   — PASS (34 packages, 0 failures)
```

---

## What Remains (Deferred)

These items from the original Phase 10 plan are **not yet done**:

| Item | Status |
|------|--------|
| Integration tests (`tests/integration/`) | Deferred — directory does not exist yet |
| CLI subcommand consolidation | Not needed — implemented in Phase 4, works as-is |
| `cmd/gateway/main.go` rewrite | Not needed — was already minimal (`servegateway.Run()`) |
| Audit DB table | Deferred to Phase 12 (per `docs/audit.md`) |
| Full Atlassian Rovo manual smoke | Deferred |

---

## Phase 10b — Distributed Circuit Breaker [PLANNED]

### Goal

gobreaker v2 ships a `DistributedCircuitBreaker[T]` type backed by a `SharedDataStore` interface. Implement a Valkey-backed store so all gateway instances share the same breaker state — an instance doesn't need to trip the breaker on its own; collective failure across instances opens it.

### Design

**`SharedDataStore` interface** (4 methods):

```go
type SharedDataStore interface {
    Lock(name string) error    // non-blocking; error if lock held
    Unlock(name string) error
    GetData(name string) ([]byte, error)
    SetData(name string, data []byte) error
}
```

**ValkeyStore**: implements `SharedDataStore` using Valkey `SETNX` (lock), `DEL` (unlock), `GET`/`SET` (state).

**Key format:**

```
limen:gobreaker:mutex:{name}   — lock key
limen:gobreaker:state:{name}   — state blob
```

### Implementation Phases

1. **Extend Valkey interface**: Add `SetNX`, `Del`, `Get` operations to `internal/valkey/` plus `InMemory` implementations for testing.
2. **Create `ValkeyStore`**: `internal/resilience/distributed.go` implementing `gobreaker.SharedDataStore`.
3. **Abstract breaker construction**: `breakerExecutor` interface so `breakerTransport` supports both local and distributed modes.
4. **Update `Client()` factory**: Accept optional `valkey.Client`; when non-nil → construct `DistributedCircuitBreaker`.
5. **Boot-time wiring**: Pass `rt.Valkey` through `ManagerOptions` to `resilience.Client()`.
6. **Global auto-detect**: When Valkey is configured → **all** breakers become distributed. When absent → local fallback with a logged warning.

### Key Decisions

| Decision | Choice |
|----------|--------|
| Distributed mode | Global auto-detect: Valkey present → all breakers distributed; absent → local |
| Key prefix | `limen:gobreaker:` |
| Fallback | Local breaker with warning log (not a fatal error) |
| Lock TTL | 5 seconds |

---

## Resilience Policy Defaults

Built-in policies defined in `internal/config/config.go:489-532`. Users can override any policy in `config.yaml` under `resilience.policies.<name>`. The `Defaults` policy is used when a name is not found.

| Name | Max Retries | Base / Max Backoff | Consecutive Fails → Open | Open Duration |
|------|-------------|--------------------|--------------------------|---------------|
| `upstream.tool_calls` | 2 | 250ms / 2s | 5 | 30s |
| `upstream.token_refresh` | 3 | 500ms / 5s | 5 | 60s |
| `upstream.dcr` | 2 | 500ms / 5s | 3 | 5min |
| `zitadel.session` | 2 | 100ms / 1s | 10 | 15s |
| `zitadel.user` | 2 | 250ms / 2s | 5 | 60s |
| `zitadel.jwks` | 3 | 100ms / 1s | 5 | 30s |

All policies respect the request `context.Context` — cancellation aborts retries immediately.

---

## Route Tree (Aspirational)

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

---

## Integration Test Matrix (Planned)

All under `tests/integration/` using `testcontainers-go` for Postgres. A `make test-integration` target spins them.

| #   | Scenario |
| --- | -------- |
| 1   | Inbound discovery chain: 401 → PRM → AS metadata → DCR proxy → authorize on Zitadel → token → /mcp 200 (against dev Zitadel container) |
| 2   | Cross-tenant rejection: valid Zitadel token whose `org_id` is tenant A used at `/t/B/mcp` → 403 |
| 3   | Cross-tenant DB isolation under Postgres RLS |
| 4   | `mcp_spec` connect: DCR against the upstream happens once per (tenant, upstream); user connect lands an `UpstreamLink` |
| 5   | `none` upstream: tools visible without a link; calls succeed without bearer to upstream |
| 6   | Tool visibility: `mcp_spec` upstream hidden until link exists; reappears after connect; disappears after disconnect |
| 7   | Refresher: a near-expiring `UpstreamLink` is refreshed in-place |
| 8   | Portal role enforcement: matrix of (role × RPC) returns expected `permission_denied`/`ok` |
| 9   | Cross-tenant portal cookie: cookie for tenant A on tenant B path → unauthenticated |
| 10  | OIDC login flow: tenant `PublicID` in state → callback validated → portal cookie set with correct Path |
| 11  | DCR proxy: registering creates a Zitadel app in the tenant's org and a `ZitadelApp` mirror row; deleting via RFC 7592 removes both |
| 12  | Full Atlassian Rovo manual smoke (documented runbook, not automated) |

All scenarios run against the `postgres:18-alpine` testcontainer.

---

## Checklist

### Phase 10 (Core)

- [x] `sony/gobreaker/v2` and `cenkalti/backoff/v4` added to `go.mod`
- [x] `ResilienceConfig` / `ResiliencePolicy` structs in `internal/config/config.go`
- [x] 6 built-in named policies with shipped defaults
- [x] Server timeout fields (Read/Write/Idle/Shutdown) in config
- [x] `internal/resilience/` package: `Client()` factory, breaker, retry, typed `BreakerOpenError`
- [x] Retry policy: transport errors + 502/503/504/429 only; honors `Retry-After`; respects ctx cancellation
- [x] Circuit breaker trips on transport errors AND HTTP 5xx
- [x] Unit tests: retry on 503, no retry on 401, 429 with Retry-After, ctx cancellation, breaker open, half-open, close, error string (7 tests, all PASS)
- [x] `RunHTTPServer`: read/write/idle timeouts + graceful shutdown with configurable timeout
- [x] `RequestLogger` middleware with `LoggerFromContext` helper
- [x] All 4 serve binaries mount `Recoverer` + `RequestID` + `RequestLogger`
- [x] `internal/audit/` skeleton: `Event`, `Emitter` interface, `LogEmitter` (4 tests PASS)
- [x] `ManagerOptions.ResiliencePolicy` wired to `resilience.Client()` in `buildBundle`
- [x] `mcpmount` passes `rt.Cfg.Resilience.Resolve("upstream.tool_calls")` to Manager
- [x] `IndexUpstream` / `listUpstreamTools` accept optional `httpClient` parameter (callers pass nil)
- [x] `config.yaml` updated with server timeouts + resilience section
- [x] `docs/runbook.md` drafted
- [x] `AGENTS.md` updated (327 lines)
- [x] `go build ./...` PASS
- [x] `go vet ./...` PASS
- [x] `go test ./...` PASS (34 packages)
- [ ] Integration tests (12 scenarios) — not yet implemented
- [ ] `cmd/gateway/main.go` rewired — not needed (already minimal)
- [ ] CLI subcommand consolidation — not needed (Phase 4 implementation works)

### Phase 10b (Distributed Circuit Breaker)

- [ ] Extend Valkey interface with SetNX, Del, Get operations
- [ ] Create `ValkeyStore` in `internal/resilience/distributed.go`
- [ ] Abstract `breakerExecutor` interface for local/distributed modes
- [ ] Update `Client()` factory to accept optional `valkey.Client`
- [ ] Boot-time wiring: pass `rt.Valkey` through `ManagerOptions`
- [ ] Global auto-detect: Valkey present → all breakers distributed; absent → local fallback
- [ ] Tests: distributed breaker with two instances sharing InMemory store
- [ ] Documentation: update runbook with Valkey-backed breaker notes

### Other Deferred

- [ ] `pnpm typecheck && pnpm lint` clean in `web/` (CI task)
- [ ] CI workflow: lint + Go tests + integration tests + SPA build
- [ ] Full Atlassian Rovo manual smoke test

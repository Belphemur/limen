# AGENTS.md — `internal/transport`

## What this package is

The HTTP/SSE transport layer. Wires the `Gateway` and `internal/auth`
middleware into a `chi` router, exposes the MCP endpoints, and shuts down
cleanly on signal.

Keep this layer **thin**: parse the request, run middleware, call into
`internal/gateway` or other domain packages, render the response. Anything
that could be unit-tested without an HTTP server probably belongs elsewhere.

## File layout

| File      | Purpose                                                            |
| --------- | ------------------------------------------------------------------ |
| `http.go` | Router setup, route registration, graceful shutdown, SSE plumbing. |

## Conventions

- **Routing**: `chi`-style. Per-tenant routes live under `/t/{slug}/...`.
- **Tenant resolution**: the auth middleware extracts the slug and pins the
  tenant in ctx via `storage.WithTenant`. Handlers downstream call
  `storage.Session(ctx)` — they should never re-parse the path.
- **Errors**: return JSON with a stable `{"error":"..."}` shape; do not leak
  internal error strings. Log the full error with `zap` and a request ID.
- **SSE**: use `http.Flusher` and send heartbeats on idle to keep proxies
  happy. Always honor the request context for cancellation.
- **Logging**: structured fields only — `zap.String("tenant", slug)`,
  `zap.String("request_id", rid)`. Never log Authorization headers or
  request bodies.

## Future surfaces (per phase)

- **Phase 4**: `/auth/login`, `/auth/callback`, `/auth/logout` for the portal
  OIDC flow.
- **Phase 5**: `/t/{slug}/register` (RFC 7591 DCR proxy onto Zitadel),
  `/t/{slug}/.well-known/oauth-protected-resource` (PRM).
- **Phase 6**: `/t/{slug}/mcp` (MCP RS — JWT-authenticated, SSE).
- **Phase 9**: SPA static asset serving under `/t/{slug}/portal/...`.

Each phase adds routes, not new transport patterns — stick to chi + middleware
chains.

## What this package is NOT

- Not a place for business logic. If a handler reads more than ~30 lines,
  move the body into a package method and let the handler stay a one-liner
  wrapper.
- Not the auth layer. Token validation lives in `internal/auth`.
- Not the persistence layer. Handlers acquire DB sessions through
  `internal/storage`.

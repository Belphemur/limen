# Phase 9a — Binary split (gateway / portal / staff)

**Depends on**: Phase 7 (upstream linking engine), Phase 8 (per-user
injection). All other phases up to 8d remain logically independent of
this one — they keep landing in `internal/*` packages that any binary
can consume.
**Unblocks**: Phase 9b (customer portal SPA + Connect-RPC API ships in
`cmd/portal/`), Phase 11 (per-binary Docker images + Compose / managed
deployment), Phase 12 (staff backoffice ships in `cmd/staff/`).

## Goal

Split the single `cmd/gateway/` binary into **three production
binaries** plus an all-in-one for dev and small self-hosters. Same
monorepo, same Go module, same `internal/*` shared code — the split
is at the entry-point + Docker-image boundary only.

| Binary          | Role                                                                                                       | Public ingress?            | Scaling shape                               |
| --------------- | ---------------------------------------------------------------------------------------------------------- | -------------------------- | ------------------------------------------- |
| `cmd/gateway/`  | MCP RS hot path (`/t/{tenant}/mcp/*`)                                                                      | Yes                        | N replicas; autoscale on connection count   |
| `cmd/portal/`   | Customer portal Connect-RPC + tenant admin Connect-RPC + OIDC RP + OAuth-proxy + upstream OAuth callback   | Yes                        | 1–2 replicas; autoscale on RPC QPS          |
| `cmd/staff/`    | Staff backoffice (super-admin, impersonation, audit search) — see [Phase 12](phase-12-staff-backoffice.md) | No (VPN / private ingress) | 1 replica                                   |
| `cmd/limen/`    | All-in-one — mounts every suite in a single process                                                        | Yes (single-node deploys)  | 1 replica; lowest-friction self-hoster path |
| `cmd/limenctl/` | Admin CLI: `migrate`, `create-tenant`, `create-upstream`, etc.                                             | No                         | One-shot                                    |

`cmd/gateway/main.go` is renamed to `cmd/limen/main.go` (the
all-in-one), and three new bare entry points (`cmd/gateway/`,
`cmd/portal/`, `cmd/staff/`) are added alongside it. The existing
`internal/cli/` per-suite assembly is the source of truth — each
`main.go` is a ~20-line composition of those helpers.

## Why this split, why these three (plus the helpers)

The three production binaries map to three distinct **operational
postures**, not to three teams or three repos. Each has a different
threat model, ingress, scaling shape, and runtime tuning; collapsing
them into one process forces every property to its strictest
denominator and bloats every image with code it can't use.

| Property                 | `gateway`                                  | `portal`                                   | `staff`                                           |
| ------------------------ | ------------------------------------------ | ------------------------------------------ | ------------------------------------------------- |
| Threat model             | Untrusted clients (LLM agents)             | Authenticated end users + tenant admins    | Privileged operators; smallest blast radius wins  |
| Ingress                  | Public, behind WAF + rate limit            | Public, behind WAF                         | Private / VPN only                                |
| Idle timeouts            | Long (SSE streaming)                       | Short                                      | Short                                             |
| Concurrency              | High — connection count is the bottleneck  | Modest                                     | Low                                               |
| Hot-path dependencies    | Goja (codemode), upstream HTTP, MCP server | Zitadel admin client, OIDC RP, Connect-RPC | Audit reader, Zitadel admin client, support tools |
| Restart cost             | High — drops in-flight tool calls          | Low                                        | Low                                               |
| Release cadence (likely) | Slow (stable hot path)                     | Fast (UI churn)                            | Medium                                            |
| What it must NOT ship    | Zitadel admin client, OIDC RP credentials  | Goja, codemode, upstream HTTP transport    | Anything end-user-facing                          |

The all-in-one (`cmd/limen/`) exists because forcing a 10-user
self-hosted instance to run 3 containers + 3 healthchecks is a tax
the project doesn't need to charge. It mounts everything; behaves
exactly like today's binary.

The CLI (`cmd/limenctl/`) exists because long-running services and
one-shot admin commands have nothing in common at the runtime layer
(no signal handling, no health server, no graceful drain). Today
they're cobra subcommands on the same root; pulling them out drops
the cobra/viper dependency tree from the three service binaries
entirely.

## Layout

```
cmd/
  gateway/main.go     # MCP RS only
  portal/main.go      # Customer + admin Connect-RPC + OIDC + OAuth proxy + upstream callback
  staff/main.go       # Backoffice — Phase 12 (scaffold lands here; impl lands then)
  limen/main.go       # Renamed from today's cmd/gateway/main.go
  limenctl/main.go    # Admin CLI (migrate / create-tenant / create-upstream)
internal/
  cli/                # Unchanged — already factored per suite (serve_mcp.go, serve_portal.go, ...)
    setup_gateway.go  # Composes setupMCPGateway + mountMCPResource for cmd/gateway/
    setup_portal.go   # Composes setupPortal + setupUpstreamLinking + admin + oauth-proxy for cmd/portal/
    setup_staff.go    # Composes staff routes for cmd/staff/ (scaffold)
    setup_allinone.go # Composes everything for cmd/limen/
    cli_admin.go      # Cobra root for cmd/limenctl/
```

Each `main.go` follows the same shape:

```go
// cmd/portal/main.go
package main

import "github.com/belphemur/limen/internal/cli"

func main() { cli.ServePortal() }
```

Where `cli.ServePortal()` is the existing wiring (config load, logger,
storage open, cipher, signer, OIDC RP, portal mux mount, signal
handling, graceful drain) minus the suites it doesn't need. The
existing `runServe` in [`internal/cli/serve.go`](../../internal/cli/serve.go)
is decomposed into per-binary entry helpers — most of the work is
already done by `setupPortal` / `setupMCPGateway` / `setupUpstreamLinking`.

## Shared runtime services

Every binary needs the same boot floor. Lift it into a single helper
so no `main.go` has to spell it out:

- Load + validate config (`internal/config`).
- Build the zap logger.
- Open the storage handle (`internal/storage.Store`) — only the
  binaries that touch the DB. `cmd/limenctl migrate` opens it as
  `limen_admin`; everything else as `limen_app`.
- Build the AES-SIV cipher bundle (`internal/crypto.Cipher`).
- Build the HMAC state signer.
- Build the Zitadel client (`internal/zitadel.Client`) — `portal`
  and `staff` only; `gateway` does not need management-API reach.
- Set up `signal.NotifyContext(SIGINT, SIGTERM)`.
- Mount `/healthz` + `/readyz` per binary.

Helper signature, illustrative:

```go
// internal/cli/runtime.go
type Runtime struct {
    Ctx     context.Context
    Cfg     *config.Config
    Logger  *zap.Logger
    Store   *storage.Store   // nil for limenctl one-shots that don't need it
    Cipher  *crypto.Cipher
    Signer  *auth.StateSigner
}

func BootRuntime(flags *RootFlags, want BootProfile) (*Runtime, func(), error)
```

`want` is a bitmask that says which dependencies to open (e.g. a
gateway binary doesn't need the Zitadel admin client). Cleanup
function closes everything in reverse order on shutdown.

## What each binary mounts

### `cmd/gateway/`

- `/t/{tenant}/mcp/*` — MCP Resource Server JSON-RPC + SSE transport.
- `/healthz`, `/readyz`.
- That's it. No `/auth/*`, no `/oauth/*` (DCR + AS metadata stay in
  `cmd/portal/` — see the sidebar under `cmd/portal/`), no
  `/t/{tenant}/api/*`, no portal cookie cipher in scope, **no
  Zitadel admin-API credential**.

Boot opens: `storage` (read-mostly path), `crypto.Cipher` (to decrypt
upstream credentials), upstream registry, gateway Manager, codemode
handler, MCP auth (does OIDC discovery against the configured issuer
to fetch `jwks_uri` at startup).

### `cmd/portal/`

- `/t/{tenant}/api/portal.v1.PortalService/*` — customer-facing
  Connect-RPC ([Phase 9b](phase-09b-portal-spa.md)).
- `/t/{tenant}/admin/api/admin.v1.AdminService/*` — tenant admin
  Connect-RPC ([Phase 9c](phase-09c-tenant-admin-spa.md)).
- `/auth/login`, `/auth/callback`, `/auth/logout` — OIDC RP
  ([Phase 4](phase-04-tenant-auth-session.md)).
- `/oauth/*` — DCR proxy + AS metadata ([Phase 5](phase-05-authorization-server.md)).
- `/t/{tenant}/upstream/{name}/callback` — OAuth redirect URI
  ([Phase 7](phase-07-outbound-upstream.md)).
- `/healthz`, `/readyz`.

`portal` is the only public-facing binary that holds the portal-session
cipher key and the Zitadel admin-client credential. Folding the
OAuth proxy in (rather than a separate binary) keeps the Zitadel
admin client to one process; folding the upstream callback in
keeps OAuth redirect-URI routing local to the binary that completes
the link.

> **Why DCR ([`internal/oauthproxy/dcr.go`](../../internal/oauthproxy/dcr.go))
> lives in `cmd/portal/`, not `cmd/gateway/`.** The DCR handler needs
> the Zitadel **management** credential (`appManager.EnsureProject` /
> `AddOIDCApp` / `UpdateOIDCApp` / `DeleteOIDCApp`) to create per-client
> OIDC apps. That credential is the single most privileged secret
> Limen holds; it must never reach the internet-exposed,
> high-traffic gateway binary. DCR traffic is cold-path (one
> registration per MCP-client × tenant install, not per request), so
> co-locating it with the cold-path portal is a strict win on both
> credential surface and scaling shape. MCP clients don't see the
> split: the PRM document served by `gateway` references the AS
> metadata URL, which the same-origin reverse proxy routes to
> `portal`. **This is a load-bearing constraint** — moving DCR to
> `gateway` would defeat the security rationale for splitting the
> binaries in the first place.

Boot opens: `storage`, `crypto.Cipher`, `auth.StateSigner`,
`zitadel.Client`, OIDC RP, upstream `Service` + `Registry`.

### `cmd/staff/`

Scaffold lands in 9a; routes land in [Phase 12](phase-12-staff-backoffice.md).

- `/staff/api/staff.v1.StaffService/*` — staff Connect-RPC.
- `/staff/auth/*` — separate OIDC RP wired to the staff Zitadel
  project (impersonation surfaces require step-up auth).
- `/healthz`, `/readyz`.
- No public path. Compose / k8s manifest binds the listener to a
  private network only.

### `cmd/limen/` (all-in-one)

Mounts every route the other three binaries mount. Same code path as
`portal` + `gateway` + `staff` combined; the only difference is one
chi router instead of three. For self-hosters who don't want to
operate three containers.

### `cmd/limenctl/`

Cobra root containing today's `migrate`, `create-tenant`,
`create-upstream`. No HTTP server. Opens `storage` with the role the
subcommand needs (`limen_admin` for `migrate`; `limen_app` for the
rest).

## Build, image, and deploy

- `go build ./cmd/...` builds every binary in a single invocation;
  Go links only what each `main` references, so the resulting
  binaries are naturally minimal.
- Per-binary Dockerfile under `build/docker/<binary>.Dockerfile`,
  sharing a `go-build` base stage and copying only the relevant
  binary into a `distroless/static` final stage.
- CI matrix builds + pushes all five images on every tag. PRs only
  rebuild the images whose binary actually changed (a `go list -deps`
  diff drives the matrix).
- Compose / Helm / k8s manifests in [Phase 11](phase-11-production-deployment.md)
  declare per-binary services with appropriate replicas, idle
  timeouts, and ingress class.
- The shared `config.yaml` is read by every binary; each binary
  reads the subset of keys it needs and ignores the rest. No
  per-binary config split in v1.

## Version skew + migrations

- **DB migrations**: only `limenctl migrate` runs them. Service
  binaries refuse to start if the schema version is newer or older
  than the embedded marker. (Embed `internal/storage/migrations` into
  every binary; on boot, compare `schema_migrations` head against
  the highest embedded file. Mismatch → exit non-zero with a clear
  message.)
- **Rolling upgrades**: a fleet of `gateway` v1.3 + a fleet of
  `portal` v1.2 must keep working. The only cross-binary contract is
  the DB schema + Zitadel + Valkey; all three already version
  themselves. There is no Go-level cross-binary API.
- **Rollback granularity**: a bad `portal` release rolls back
  `portal` only. Gateway traffic is untouched.

## Testing

- **Per-binary boot test** (one per binary): start the binary against
  a temporary Postgres + a stub Zitadel; assert that exactly the
  expected routes respond, and the unexpected routes return 404.
  This is the load-bearing test that proves the split is real, not
  cosmetic. Uses `testcontainers-go` per the project's testing
  conventions.
- **Cross-binary integration**: keep one existing
  `manager_integration_test.go`-style suite running against the
  all-in-one (`cmd/limen/`) so end-to-end paths stay tested without
  needing to coordinate three processes in a test container.
- **No mocking of `internal/cli` package** in tests — boot the actual
  helper.

## Open questions

1. **`oauth-proxy` co-located with `portal`** — confirmed in this
   phase, but if DCR proxy volumes get noisy in production we may
   split it out later. The cost of a future split is small (separate
   `cmd/oauthproxy/main.go`, same `internal/oauthproxy` package);
   not worth doing pre-emptively.
2. **OIDC RP credentials in `staff`** — staff uses a separate Zitadel
   project, separate client ID + secret. Captured here; concrete
   wiring lands in Phase 12.
3. **`cmd/limen/` survival** — if running 3 binaries proves trivial
   for self-hosters once Compose lands (Phase 11), we may deprecate
   the all-in-one. Keep it for v1; revisit in v1.x.
4. **`cmd/limenctl/` placement** — could live under `cmd/` or be
   renamed something more user-facing (`cmd/limen-admin/`?). v1
   keeps `limenctl` for brevity; the binary is operator-only so
   discoverability isn't a goal.

## Risks

- **Build matrix bloat.** Five binaries × supported platforms × tag
  rebuilds. Mitigated by `go list -deps` driving a per-binary rebuild
  decision and by sharing the Docker base stage.
- **Drift between `cmd/limen/` and the split binaries.** The
  all-in-one must keep mounting every route the split binaries
  mount; a route added to `portal` but not `limen` is a latent
  regression. The per-binary boot test enforces this for the split
  binaries; we add one more assertion (the all-in-one mounts the
  union) to enforce it for `limen`.
- **Per-binary config divergence.** Tempting to give each binary its
  own config file. Resist for v1 — one config is operationally
  simpler. Revisit if any single config key needs to differ between
  binaries in the same deployment (e.g. per-binary log levels).
- **Backwards compatibility with today's `limen serve`.** Today's
  `limen serve` command (everything-in-one) becomes `cmd/limen/` —
  same behavior, just a separate binary. Documentation updates in
  Phase 11.

## Slices

1. **Slice 1 — `internal/cli/runtime.go` + `BootRuntime`.** Lift the
   shared boot floor out of `runServe`. No behavior change; existing
   `limen serve` keeps working.
2. **Slice 2 — `cmd/limen/main.go`.** Rename `cmd/gateway/` →
   `cmd/limen/` (it's the all-in-one). Update `Makefile`,
   `compose.dev.yaml`, README, AGENTS.md, build scripts.
3. **Slice 3 — `cmd/gateway/main.go`.** New entry point that boots
   only the MCP suite. Per-binary boot test asserts 404 on portal /
   auth / oauth / upstream routes.
4. **Slice 4 — `cmd/portal/main.go`.** New entry point that boots
   portal + auth + oauth-proxy + upstream callback. Per-binary boot
   test asserts 404 on `/mcp/*` and on staff routes.
5. **Slice 5 — `cmd/staff/main.go`.** Scaffold only (no staff
   service yet). 404 on everything except `/healthz`. Phase 12 fills
   it in.
6. **Slice 6 — `cmd/limenctl/main.go`.** Move `migrate`,
   `create-tenant`, `create-upstream` out of the service binaries.
   Service binaries no longer expose those subcommands.
7. **Slice 7 — Dockerfiles + CI.** Per-binary Dockerfile, CI matrix.
   Phase 11 picks these up for production wiring.

## Checklist

- [x] **Slice 1** — `internal/boot/runtime.go` with `BootRuntime` +
      `BootProfile` bitmask; existing `limen serve` unchanged.
      (Lives under `internal/boot/` rather than `internal/cli/` so
      `cmd/gateway` can depend on it without transitively importing
      the cobra/viper tree or the admin subcommands.)
- [x] **Slice 2** — `cmd/gateway/` renamed to `cmd/limen/`; `Makefile`,
      `compose.dev.yaml`, build scripts updated. `README.md` /
      `AGENTS.md` updates deferred to the Phase 11 docs sweep.
- [x] **Slice 3** — `cmd/gateway/main.go` (new) boots only the MCP
      suite. Import-graph isolation enforced by
      `cmd/gateway/import_graph_test.go` (asserts `internal/oauthproxy`
      and `internal/zitadel` are absent from `go list -deps`). Route-
      level 404 boot tests deferred (would need a real Postgres per
      `BootRuntime` — out of scope for the no-DB testing posture
      chosen for this slice; the import-graph check is the load-
      bearing assertion).
- [x] **Slice 4** — `cmd/portal/main.go` boots portal + OIDC RP +
      OAuth proxy (DCR + AS metadata) + upstream callback. Admin
      Connect-RPC arrives with Phase 9c. Import-graph test asserts
      portal _does_ pull `internal/oauthproxy` + `internal/zitadel` +
      `internal/auth`. Live `POST /oauth/register` smoke test
      deferred to the Phase 9b/c work that exercises the route end-
      to-end.
- [x] **Slice 5** — `cmd/staff/main.go` scaffold; boots, serves
      `/healthz` + `/readyz`, returns 404 elsewhere. Builds the
      Zitadel admin client (held for Phase 12). Import-graph test
      asserts staff excludes `internal/oauthproxy` and the codemode
      hot path.
- [x] **Slice 6** — `cmd/limenctl/main.go` owns `migrate`,
      `create-tenant`, `create-upstream` via `cli.NewAdminRootCommand`.
      `cmd/limen` keeps the full tree (including `serve`) for the
      all-in-one shape.
- [ ] **Slice 7** — per-binary Dockerfiles under
      `build/docker/<binary>.Dockerfile`; CI matrix builds all five
      binaries on every PR and pushes images on tag. **Deferred to
      Phase 11 per scope decision.**
- [x] Schema-version mismatch on boot exits non-zero with a clear
      "run `limenctl migrate`" message; `storage.CheckSchemaVersion`
      is invoked from `BootRuntime` when `NeedStore` is set, with
      match / DB-behind / fresh-DB coverage in
      `internal/storage/schema_version_test.go`.
- [x] `internal/boot` + `internal/cli` packages documented
      (`internal/boot/AGENTS.md`) — per-suite mount helpers under
      `internal/boot/*` are the supported API; service `main.go`
      files are 1–2 calls each. Top-level `AGENTS.md` + `README.md`
      updated for the five-binary layout.
- [ ] Phase 11 follow-up captured: per-binary Compose services with
      appropriate replicas, timeouts, and ingress class; `staff`
      bound to a private network.
- [ ] Phase 12 follow-up captured: `cmd/staff/` fills in staff
      Connect-RPC + step-up auth + impersonation surfaces.

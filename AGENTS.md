# AGENTS.md — Limen (MCP Gateway)

## Engineering posture (read first)

- **DRY, SOLID, KISS — always.** If the same logic appears in two places, lift it. If a service has two unrelated responsibilities, split it. If a clever abstraction is shorter than the obvious one, keep the obvious one. These rules win against personal taste, file-locality convenience, and "I'll refactor it later."
- **Full development mode.** Limen has no external users yet. **Breaking changes are accepted and expected.** When DRYing or refactoring, do the cutover in one commit — do **not** add migration shims, compatibility aliases, deprecated re-exports, or transitional code paths. Delete the old name in the same change that introduces the new one.
- **Cross-cutting concerns get their own home.** A capability used by ≥2 callers belongs in a shared package (`internal/<concern>/`, `web/shared/`, etc.), not duplicated. The shared `SessionService` ([Phase 9d](docs/phases/phase-09d-shared-session-service.md)) is the canonical example.
- **No backwards-compatibility tax.** No feature flags for behaviour we'll always want. No `v1alpha → v1` aliases. Bump the proto, regenerate, delete the old call sites — all in one PR.

## Project Overview

Limen is a Model Context Protocol (MCP) gateway written in Go. It aggregates upstream MCP servers into a unified endpoint, exposing their tools over HTTP/SSE transport. Clients connect to Limen as a single MCP server, while Limen routes requests to the configured upstream servers.

Limen also includes a **Code Mode** feature — a JavaScript sandbox powered by Goja that lets you compose tools and define custom logic server-side. The sandbox has no filesystem or network access; only explicitly injected tool functions are available.

Limen ships as **six binaries** built from a single Go module (see Phase 9a). All six share `internal/boot` and `internal/*` packages; the split is at the entry-point + Docker-image boundary only.

## Architecture

```
cmd/limen/main.go            # All-in-one binary (dev + small self-hosted)
cmd/gateway/main.go          # MCP RS hot path (Phase 9a)
cmd/portal/main.go           # Portal/admin Connect-RPC + OIDC RP + OAuth proxy (Phase 9a)
cmd/staff/main.go            # Staff backoffice scaffold (Phase 9a, routes land in Phase 12)
cmd/observer/main.go         # Billing telemetry consumer / observer process (decoupled, Phase 11/19/20)
cmd/limenctl/main.go         # Admin CLI: migrate, create-tenant, create-upstream (Phase 9a)
internal/
  admin/                      # AdminService Connect-RPC handler (Phase 9c)
  auth/                       # OIDC relying party, JWT middleware, state signer (Phase 4, 6)
  boot/                       # BootRuntime + BootProfile bitmask shared by every binary
  boot/{suite}mount/          # Per-suite mount helpers (mcpmount, portalmount, oauthproxymount, ...)
  boot/serve{name}/           # Per-binary Run(configPath) entry points
  boot/serveobserver/         # Standalone metrics consumer entrypoint
  config/                     # YAML config loading with env var substitution
  contextblob/                # Context merge utilities for ambient context
  crypto/                     # AES-SIV encryption for SecretField (Phase 2)
  gateway/                    # Core gateway: MCP upstream aggregation + Code Mode
  gateway/authtransport.go    # Auth-injecting outbound HTTP RoundTripper (Phase 8)
  gateway/codemode/           # Goja JS sandbox for server-side tool composition
  gateway/codemodeaction/     # Code Mode MCP tool definitions
  idepresets/                 # IDE preset configuration (Phase 9f)
  ids/                        # ULID / public ID generation
  mailer/                     # SMTP mailer for signup verification (Phase 9h)
  mcprs/                      # MCP Resource Server: PRM handler, challenge flow (Phase 6)
  oauthproxy/                 # Thin OAuth proxy: DCR, AS metadata, redirector (Phase 5)
  portal/                     # PortalService Connect-RPC handler (Phase 9b)
  resilience/                 # HTTP client resilience: retry + circuit breaker (Phase 10)
  audit/                      # Audit event emitter (Phase 10 log sink; DB table in Phase 12)
  session/                    # Shared session service (Phase 9d)
  signup/                     # Self-serve signup wizard (Phase 9h)
  storage/                    # GORM models, Postgres connection pools, RLS (Phase 1, 3)
  tenancy/                    # Tenant resolution, per-tenant middleware (Phase 3)
  tenant/                     # Tenant-scoped services (allowlist, etc.)
  transport/                  # HTTP route mounting (chi): MCP, portal, OAuth, upstream
  upstream/                   # Upstream registry, strategies (mcp_spec, none), refresher (Phase 7)
  valkey/                     # Valkey (Redis) client for OAuth state (Phase 7)
  zitadel/                    # Zitadel Management API client (Phase 4)
web/
  portal/                     # Vue 3 Portal SPA (Phase 9b)
  admin/                      # Vue 3 Admin SPA (Phase 9c)
  shared/                     # Shared TS types + SessionService bindings
proto/
  portal/v1/                  # PortalService protobuf definitions
  admin/v1/                   # AdminService protobuf definitions
  session/v1/                 # SessionService protobuf definitions
  signup/v1/                  # SignupService protobuf definitions
tests/
  integration/                # Integration tests with testcontainers-go (Phase 10)
```

**Key dependencies:** `go-chi/chi/v5`, `mark3labs/mcp-go`, `dop251/goja`, `go.uber.org/zap`, `gopkg.in/yaml.v3`

**Module:** `github.com/belphemur/limen`

## Setup

> **Shell:** the local dev shell is **fish**. When invoking terminal tooling
> as an agent, send fish-syntax commands (no `&&` chaining tricks that
> assume bash semantics; `set -lx VAR …` for env vars; `string` builtins
> over GNU coreutils where natural). Examples in this doc use POSIX/bash
> for portability, but adapt them when sending into the live terminal.

```bash
# Build all five binaries (limen, limenctl, limen-gateway, limen-portal, limen-staff)
make build

# Or build just the all-in-one
go build -o limen ./cmd/limen

# Run with config
./limen serve --config config.yaml
```

Production deployments use the split binaries (`cmd/gateway`, `cmd/portal`,
`cmd/staff`) with `cmd/limenctl migrate` as a one-shot init container. See
[docs/phases/phase-09a-binary-split.md](docs/phases/phase-09a-binary-split.md).

## Database Migrations

Limen uses two complementary migration mechanisms:

- **GORM AutoMigrate** handles DDL column changes on registered models. Adding a
  field to a Go model struct (e.g. `TokenGeneratedAt`, `LastUsedAt`) and appending
  the model to `AllModels()` is sufficient — AutoMigrate creates new tables and
  columns on the next `limenctl migrate` run. No manual SQL migration is needed
  for simple column additions.
- **Goose SQL migrations** (`internal/storage/migrations/postgres/*.sql`) handle
  structural database concerns that GORM cannot: RLS policies, triggers, partial
  indexes, CHECK constraints, and data backfills. See
  [internal/storage/AGENTS.md](internal/storage/AGENTS.md) and
  [MIGRATIONS.md](internal/storage/MIGRATIONS.md) for details.

Run migrations via `limenctl migrate` — this is a one-shot init step, **not** part
of server startup. The server checks the schema version on boot and refuses to
start if the database is un-migrated.

## Build & Test Commands

```bash
# Build every binary
make build           # or: go build ./cmd/...

# Regenerate Go + TypeScript bindings from proto/
buf generate        # writes internal/portal/portalv1{,connect}/* and web/*/src/gen/*

# Build the Vue SPA (web/)
cd web && pnpm install --frozen-lockfile && pnpm build
# Outputs to web/dist/ — the artefact Cloudflare Pages / Caddy file_server ships.

# Run tests (requires Docker for the testcontainers-go Postgres suites)
go test ./...

# Vet & format
go vet ./...
go fmt ./...
```

### Portal SPA (`web/`) toolchain

The Vue 3 + Connect-RPC SPA lives in `web/` and is **not** embedded in any
Go binary. Limen ships the Connect-RPC API at
`/t/{tenant}/api/limen.portal.v1.PortalService/*`; the static host serves
`web/dist/`. See [docs/phases/phase-09b-portal-spa.md](docs/phases/phase-09b-portal-spa.md).

Phase 9c (tenant admin SPA, slice 1) adds two further service mounts plus a
discovery endpoint on the root router; all are wired by
`internal/transport.MountPortal` via `internal/boot/portalmount`:

- `/t/{tenant}/api/limen.admin.v1.AdminService/*` — admin/owner-scoped
  RPCs (same per-tenant ServeMux as PortalService + SessionService).
- `/api/limen.signup.v1.SignupService/*` — public, tenant-agnostic
  signup wizard backend (no tenancy / session / role interceptors).
- `GET /auth/discovery` — returns the configured Zitadel issuer URL as
  `{"zitadelIssuer": "…"}` so SPAs can build Console deep-links without
  hard-coding the IdP.

- Package manager: **pnpm v11** via Corepack (`"packageManager": "pnpm@11.x.x"`
  in `web/package.json`). `npm install` / `yarn install` are not supported.
- Codegen: `pnpm run gen` (a `pnpm build` prebuild hook) shells out to
  `buf generate`. The generated TS lives under `web/*/src/gen/` (e.g., `web/portal/src/gen/`, `web/admin/src/gen/`) and is
  `.gitignore`d; the Go bindings under `internal/portal/portalv1/` are
  checked in.
- Connect-ES v2: the `bufbuild/es:v2.x` plugin emits both messages and
  service descriptors into `*_pb.ts`. We do **not** use the deprecated
  `connectrpc/es` plugin.
- Unit tests: `pnpm test` (Vitest + jsdom).
- E2E smoke: `pnpm e2e` (Playwright against `vite preview`, Connect-RPC
  stubbed via `page.route` or `window.fetch` overrides). First run
  requires `pnpm e2e:install` to fetch the Chromium binary. Shared
  test utilities live in `web/shared/src/test-utils/e2e-mocks.ts`.
  See [docs/frontend-e2e.md](docs/frontend-e2e.md) for the full guide.

## Required pre-commit checks

Every change **must** pass the full toolchain locally before being committed
or pushed. CI runs the same chain — failing it locally only wastes a round
trip.

```bash
go mod tidy
go fmt ./...
go vet ./...
go build ./...
golangci-lint run ./...
go test ./...
```

Conventions:

- Use the `Makefile` targets where they exist (`make build`, `make vet`,
  `make fmt`, `make test`). Add a target rather than a bespoke script.
- `golangci-lint` config lives in `.golangci.yml`. Do not disable lints
  ad-hoc — either fix the issue, add a targeted `//nolint:<linter> //
<reason>` directive, or amend the shared config with a brief justification
  in the PR description.
- Never commit code that does not compile, fails `go vet`, or has unstaged
  `gofmt` diffs.

## Code Style

### Go Naming Conventions

| Item                        | Convention  | Example                              |
| --------------------------- | ----------- | ------------------------------------ |
| Exported types/functions    | PascalCase  | `Gateway`, `NewMCPServer`            |
| Unexported functions/fields | camelCase   | `extractBearerToken`, `handleSearch` |
| Constructors                | `New<Type>` | `NewGateway`, `NewCodeModeHandler`   |
| Config keys / tool names    | snake_case  | `github_token`, `search_issues`      |

### Error Handling

- Use `fmt.Errorf` with `%q` for quoted values and `%w` to wrap errors
- Return errors, don't log and swallow (let the caller decide)
- Log errors at the boundary (HTTP handlers, main) with structured fields

### Logging

- Use `go.uber.org/zap` for structured logging
- Use typed fields: `zap.String()`, `zap.Error()`, `zap.Int()`, etc.
- Log at the right level: `Debug` for flow, `Info` for operations, `Error` for failures

### General

- Prefer small, focused functions over large ones
- Keep HTTP handlers thin — delegate logic to gateway/internal packages
- Add comments for non-obvious behavior; don't restate the code

## Testing

We test against real Postgres (no mocks for the storage layer) and mock
upstreams for the gateway. The Phase 1 integration suite lives in
[internal/storage/storage_test.go](internal/storage/storage_test.go) and is
the reference shape for any new DB-backed test.

### Layout

- Test files live alongside the source they cover (`foo.go` ↔ `foo_test.go`),
  in the same package by default. Use the `_test` package suffix when you
  want to exercise only the public API (`storage_test` vs `storage`).
- Helpers shared across tests in a single package go in `*_test.go` files —
  do **not** export test helpers from production packages.

### Integration tests

Integration tests live **per-package** as `*_integration_test.go` files,
gated by the `//go:build integration` build tag, and use
`testcontainers-go` with `postgres:18-alpine`. They sit next to the code
they cover rather than in a shared `tests/integration/` tree.

```bash
# Run integration tests (requires Docker)
go test -tags integration ./...
```

All scenarios run against an ephemeral Postgres container with the full
schema migration + RLS applied.

### Integration tests with real Postgres

- Use [`testcontainers-go`](https://github.com/testcontainers/testcontainers-go)
  with the **`postgres:18-alpine`** image — the same version Phase 0 / 11
  run. Do not pin a different minor version per-test.
- Each test starts a fresh container via the shared `startPostgres(t)` helper
  in `internal/storage/storage_test.go` (or an analogous helper in other
  packages). One container per test keeps tests independent at the cost of
  ~1–2 seconds each — acceptable for the safety it buys.
- Requires Docker on the host. CI must expose `/var/run/docker.sock` to the
  test runner.
- Long-running suites: don't disable them; mark them with `testing.Short()`
  guards if they grow beyond a few seconds each and exclude them under
  `go test -short`.

### Unit tests

- Use table-driven tests for functions with multiple input cases
  (`tests := []struct{...}{...}` + `for _, tt := range tests { t.Run(... }`).
- Mock upstream MCP clients rather than hitting real servers.
- For the Goja sandbox, write assertions about what is _not_ reachable —
  `os`, `process`, `fetch`, `eval`, filesystem helpers — as well as what is.

### Assertions

- Prefer the standard library (`t.Errorf`, `t.Fatalf`, `errors.Is`) — no
  third-party assertion frameworks.
- Test names: `TestSubject_Behavior` (e.g. `TestSoftDelete_DoesNotBlockReinsert`).
  The underscore separator makes `go test -run` patterns easy.

### Watch out for

- **`SET LOCAL` with placeholders** does not work in Postgres — use
  `set_config(name, value, true)` instead. (Bit us in Phase 1.)
- **ULID ordering invariant**: `ulid.Make` is monotonic _within a single
  process_. Two processes minting IDs in the same millisecond can produce
  IDs that interleave on the global timeline — fine for cursor pagination,
  but don't assert strict cross-process ordering in tests.
- **Container start cost** dominates fast tests — keep the per-test logic
  small once you've paid for the container.

### Running

```bash
go test ./...                       # everything
go test ./internal/storage/...      # one package
go test -run TestMigrate ./...      # one test
go test -race ./...                 # race detector — run before pushing
```

## Security

- **Row-Level Security (RLS):** Every request is scoped to a single tenant
  via `app.current_tenant` set by the tenancy middleware. The `limen_app`
  Postgres role cannot BYPASSRLS.
- **Zitadel JWKS:** Inbound MCP access tokens are validated against
  Zitadel's JWKS endpoint per request. Audience checking ensures tokens
  are bound to the Limen MCP resource server.
- **AES-SIV encryption:** Sensitive fields (tokens, secrets) are encrypted
  at rest using AES-128-SIV. The master key is never stored in the database.
- **Delegated identity:** Limen never sees user passwords. Authentication
  is delegated to Zitadel via OIDC. Limen only holds an encrypted session
  cookie containing the id_token and refresh_token.
- **Circuit breakers:** All outbound HTTP clients (upstream MCP, Zitadel
  API, JWKS) are protected by circuit breakers that fail fast when
  dependencies are unhealthy.

## Pull Requests & Commits

### Commit Messages

Use [Conventional Commits](https://www.conventionalcommits.org/):

```
type(scope): description

[optional body — explain WHY, not what]

[optional footer — breaking changes, issue refs]
```

Types: `feat`, `fix`, `docs`, `style`, `refactor`, `test`, `chore`

Examples:

- `feat(gateway): aggregate upstream tool list on startup`
- `fix(auth): validate JWT issuer claim`
- `docs: add AGENTS.md with project conventions`
- `refactor(codemode): extract Goja runtime initialization`

### PR Guidelines

- Keep PRs focused on one logical change
- Update this file when build steps, commands, or conventions change
- Add tests for new functionality or bug fixes

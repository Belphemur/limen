# AGENTS.md — Limen (MCP Gateway)

## Engineering posture (read first)

- **DRY, SOLID, KISS — always.** If the same logic appears in two places, lift it. If a service has two unrelated responsibilities, split it. If a clever abstraction is shorter than the obvious one, keep the obvious one. These rules win against personal taste, file-locality convenience, and "I'll refactor it later."
- **Full development mode.** Limen has no external users yet. **Breaking changes are accepted and expected.** When DRYing or refactoring, do the cutover in one commit — do **not** add migration shims, compatibility aliases, deprecated re-exports, or transitional code paths. Delete the old name in the same change that introduces the new one.
- **Cross-cutting concerns get their own home.** A capability used by ≥2 callers belongs in a shared package (`internal/<concern>/`, `web/shared/`, etc.), not duplicated. The shared `SessionService` ([Phase 9d](docs/phases/phase-09d-shared-session-service.md)) is the canonical example.
- **No backwards-compatibility tax.** No feature flags for behaviour we'll always want. No `v1alpha → v1` aliases. Bump the proto, regenerate, delete the old call sites — all in one PR.

## Project Overview

Limen is a Model Context Protocol (MCP) gateway written in Go. It aggregates upstream MCP servers into a unified endpoint, exposing their tools over HTTP/SSE transport. Clients connect to Limen as a single MCP server, while Limen routes requests to the configured upstream servers.

Limen also includes a **Code Mode** feature — a JavaScript sandbox powered by Goja that lets you compose tools and define custom logic server-side. The sandbox has no filesystem or network access; only explicitly injected tool functions are available.

Limen ships as **five binaries** built from a single Go module (see Phase 9a). All five share `internal/boot` and `internal/*` packages; the split is at the entry-point + Docker-image boundary only.

## Architecture

```
cmd/limen/main.go            # All-in-one binary (dev + small self-hosted)
cmd/gateway/main.go          # MCP RS hot path (Phase 9a)
cmd/portal/main.go           # Portal/admin Connect-RPC + OIDC RP + OAuth proxy + upstream callback (Phase 9a)
cmd/staff/main.go            # Staff backoffice scaffold (Phase 9a, routes land in Phase 12)
cmd/limenctl/main.go         # Admin CLI: migrate, create-tenant, create-upstream (Phase 9a)
internal/
  auth/middleware.go          # JWT/JWKS auth middleware (stub — needs validation)
  boot/runtime.go             # BootRuntime + BootProfile bitmask shared by every binary
  boot/<suite>mount/          # Per-suite mount helpers (mcpmount, portalmount, oauthproxymount, ...)
  boot/serve<binary>/         # Per-binary Run(configPath) entry points (servegateway, serveportal, ...)
  config/config.go            # YAML config loading with env var substitution
  gateway/gateway.go          # Core gateway: aggregates upstream MCP tools
  gateway/codemode.go         # Goja JS sandbox for server-side tool composition
  gateway/upstream.go         # MCP upstream HTTP/SSE client
  transport/http.go           # HTTP/SSE transport layer (chi router)
```

**Key dependencies:** `go-chi/chi/v5`, `mark3labs/mcp-go`, `dop251/goja`, `go.uber.org/zap`, `gopkg.in/yaml.v3`

**Module:** `github.com/belphemur/limen`

## Setup

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

## Build & Test Commands

```bash
# Build every binary
make build           # or: go build ./cmd/...

# Regenerate Go + TypeScript bindings from proto/
buf generate        # writes internal/portal/portalv1{,connect}/* and web/src/gen/*

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
  `buf generate`. The generated TS lives under `web/src/gen/` and is
  `.gitignore`d; the Go bindings under `internal/portal/portalv1/` are
  checked in.
- Connect-ES v2: the `bufbuild/es:v2.x` plugin emits both messages and
  service descriptors into `*_pb.ts`. We do **not** use the deprecated
  `connectrpc/es` plugin.
- Unit tests: `pnpm test` (Vitest + jsdom).
- E2E smoke: `pnpm e2e` (Playwright against `vite preview`, Connect-RPC
  stubbed via `page.route`). First run requires `pnpm e2e:install` to
  fetch the Chromium binary.

## Required pre-commit checks

Every change **must** pass the full toolchain locally before being committed
or pushed. CI runs the same chain — failing it locally only wastes a round
trip.

```bash
go mod tidy
go fmt ./...
go fix ./...
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

- **Goja sandbox:** No filesystem or network access. Only injected tool functions are callable.
- **Secrets:** Config uses `${ENV_VAR}` substitution for secrets. Never commit real tokens to the repo.
- **Auth middleware:** JWT/JWKS validation is currently stubbed — implement before production use.
- **Config files:** Treat config as sensitive. Use `.gitignore` for any config with embedded secrets.

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

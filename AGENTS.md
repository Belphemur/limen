# AGENTS.md — Limen (MCP Gateway)

## Project Overview

Limen is a Model Context Protocol (MCP) gateway written in Go. It aggregates upstream MCP servers into a unified endpoint, exposing their tools over HTTP/SSE transport. Clients connect to Limen as a single MCP server, while Limen routes requests to the configured upstream servers.

Limen also includes a **Code Mode** feature — a JavaScript sandbox powered by Goja that lets you compose tools and define custom logic server-side. The sandbox has no filesystem or network access; only explicitly injected tool functions are available.

The project is a single binary with minimal dependencies. There is no Dockerfile yet, no test suite, and the JWT auth middleware is a stub that needs real validation logic.

## Architecture

```
cmd/limen/main.go            # All-in-one binary (dev + small self-hosted)
cmd/gateway/main.go          # MCP RS hot path (Phase 9a)
cmd/portal/main.go           # Portal/admin Connect-RPC + OIDC RP + OAuth proxy + upstream callback (Phase 9a)
cmd/staff/main.go            # Staff backoffice scaffold (Phase 9a, routes land in Phase 12)
cmd/limenctl/main.go         # Admin CLI: migrate, create-tenant, create-upstream (Phase 9a)
internal/
  auth/middleware.go          # JWT/JWKS auth middleware (stub — needs validation)
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
# Build the all-in-one binary
go build -o limen ./cmd/limen

# Run with config
./limen -config config.yaml
```

## Build & Test Commands

```bash
# Build
go build -o limen ./cmd/limen

# Run tests (none exist yet — feel free to add them)
go test ./...

# Vet & format
go vet ./...
go fmt ./...
```

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
- For the Goja sandbox, write assertions about what is *not* reachable —
  `os`, `process`, `fetch`, `eval`, filesystem helpers — as well as what is.

### Assertions

- Prefer the standard library (`t.Errorf`, `t.Fatalf`, `errors.Is`) — no
  third-party assertion frameworks.
- Test names: `TestSubject_Behavior` (e.g. `TestSoftDelete_DoesNotBlockReinsert`).
  The underscore separator makes `go test -run` patterns easy.

### Watch out for

- **`SET LOCAL` with placeholders** does not work in Postgres — use
  `set_config(name, value, true)` instead. (Bit us in Phase 1.)
- **ULID ordering invariant**: `ulid.Make` is monotonic *within a single
  process*. Two processes minting IDs in the same millisecond can produce
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

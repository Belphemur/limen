# AGENTS.md — Limen (MCP Gateway)

## Project Overview

Limen is a Model Context Protocol (MCP) gateway written in Go. It aggregates upstream MCP servers into a unified endpoint, exposing their tools over HTTP/SSE transport. Clients connect to Limen as a single MCP server, while Limen routes requests to the configured upstream servers.

Limen also includes a **Code Mode** feature — a JavaScript sandbox powered by Goja that lets you compose tools and define custom logic server-side. The sandbox has no filesystem or network access; only explicitly injected tool functions are available.

The project is a single binary with minimal dependencies. There is no Dockerfile yet, no test suite, and the JWT auth middleware is a stub that needs real validation logic.

## Architecture

```
cmd/gateway/main.go          # Entry point — config load, server init
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
# Build the binary
go build -o limen ./cmd/gateway

# Run with config
./limen -config config.yaml
```

## Build & Test Commands

```bash
# Build
go build -o limen ./cmd/gateway

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

| Item | Convention | Example |
|------|-----------|---------|
| Exported types/functions | PascalCase | `Gateway`, `NewMCPServer` |
| Unexported functions/fields | camelCase | `extractBearerToken`, `handleSearch` |
| Constructors | `New<Type>` | `NewGateway`, `NewCodeModeHandler` |
| Config keys / tool names | snake_case | `github_token`, `search_issues` |

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

No tests exist yet. When adding tests:

- Place test files alongside source (`*_test.go` in the same package)
- Use table-driven tests for functions with multiple input cases
- Mock upstream MCP clients rather than hitting real servers
- Test the Goja sandbox boundary (what's accessible, what's blocked)
- Run `go test ./...` before committing

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

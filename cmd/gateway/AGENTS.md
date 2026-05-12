# AGENTS.md — `cmd/gateway`

## What this is

The Limen binary entry point. Everything that happens before a request lands
on the router lives here:

1. Parse flags (`-config`).
2. Initialize structured logging (`zap`).
3. Load and validate configuration via `internal/config.Load`.
4. Open the Postgres pool via `internal/storage.Open` and run `Store.Migrate`
   (idempotent `AutoMigrate`).
5. Construct the `Gateway` (`internal/gateway.NewGateway`) and dispatch
   upstream discovery.
6. Wire the HTTP transport (`internal/transport`) and start the server.
7. Wait for SIGINT/SIGTERM and shut everything down in reverse order.

## Conventions

- **Thin main.** No business logic here — orchestration only. Each step is
  one or two lines plus error handling.
- **Fail loud, fail fast.** Config/migration failures call `logger.Fatal`.
  Once the server is up, recoverable errors are logged and surfaced; only
  catastrophic conditions take the process down.
- **No globals.** Pass `*zap.Logger`, `*config.Config`, `*storage.Store`
  explicitly to the constructors that need them.
- **Graceful shutdown.** All long-lived components (HTTP server, gateway
  background loops, DB pool) must accept a context and stop when it cancels.
  Add `defer store.Close()` etc. in `main`.

## Boundaries

- `cmd/gateway` is the only package allowed to call `zap.NewProduction()`,
  `flag.Parse`, or read `os.Args`.
- It is the only package that ties together `config`, `storage`, `gateway`,
  `transport`, and `auth`. Sub-packages do not import each other freely —
  they meet here.

## What this is NOT

- Not a place for CLI subcommands. If we add an admin CLI it lives under
  `cmd/limenctl/` (or similar) — not here.
- Not a config file. `config.yaml` is the runtime config; this binary just
  reads it.

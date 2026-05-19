# AGENTS.md — `cmd/limen`

## What this is

The all-in-one Limen binary. Mounts the **union** of every route the
split binaries (`cmd/gateway`, `cmd/portal`, `cmd/staff`) collectively
serve. Intended for dev, local smoke tests, and the lowest-friction
self-hosted deployment.

`main.go` is intentionally one line — all wiring lives in
`internal/boot/serveall` (composition) and `internal/boot/runtime.go`
(`BootRuntime` + `BootProfile`). Admin subcommands (`migrate`,
`create-tenant`, `create-upstream`) are exposed through
`internal/cli`'s cobra tree.

## Conventions

- **Thin main.** No business logic here — composition only.
- **Boot via `boot.BootRuntime(cfg, boot.AllProfiles)`** — never
  construct shared services (cipher, store, signer, Zitadel client,
  OIDC RP) directly in `main`.
- **Graceful shutdown.** `BootRuntime` registers cleanups and wires a
  signal-cancellable context; `boot.RunHTTPServer` drains the HTTP
  server on cancel.

## Boundaries

- The all-in-one **must** mount every route the split binaries mount.
  Adding a route to `cmd/portal` without folding it into
  `internal/boot/serveall` is a regression — the per-binary
  import-graph tests catch the split-binary side; reviewers catch the
  all-in-one side.
- Admin one-shots (`migrate`, `create-tenant`, `create-upstream`) ship
  via this binary's cobra tree AND via `cmd/limenctl/`. Keep both in
  sync via `internal/cli.NewRootCommand` / `NewAdminRootCommand`.

## What this is NOT

- Not the production posture. Production runs `cmd/gateway` + `cmd/portal`
  + `cmd/staff` as separate binaries with different scaling and
  credential surfaces — see [docs/phases/phase-09a-binary-split.md](../../docs/phases/phase-09a-binary-split.md).
- Not a config file. `config.yaml` is the runtime config.

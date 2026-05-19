# AGENTS.md — `cmd/limen`

## What this is

The all-in-one Limen binary. Mounts the **union** of every route the
split binaries (`cmd/gateway`, `cmd/portal`, `cmd/staff`) collectively
serve. Intended for dev, local smoke tests, and the lowest-friction
self-hosted deployment.

`main.go` is intentionally one line — all wiring lives in
`internal/cli` (see `internal/cli/runtime.go` for `BootRuntime` and
`internal/cli/serve.go` for `runServe`).

## Conventions

- **Thin main.** No business logic here — composition only.
- **Boot via `BootRuntime(AllProfiles)`** — never construct shared
  services (cipher, store, signer, Zitadel client, OIDC RP) directly
  in `main`.
- **Graceful shutdown.** `BootRuntime` registers cleanups and wires a
  signal-cancellable context; `runServe` drains the HTTP server on
  cancel.

## Boundaries

- The all-in-one **must** mount every route the split binaries mount.
  Adding a route to `cmd/portal` without folding it into `runServe` is
  a regression — the union assertion in the boot tests catches this.
- Admin one-shots (`migrate`, `create-tenant`, `create-upstream`) live
  in `cmd/limenctl/`, not here.

## What this is NOT

- Not the production posture. Production runs `cmd/gateway` + `cmd/portal`
  + `cmd/staff` as separate binaries with different scaling and
  credential surfaces — see [docs/phases/phase-09a-binary-split.md](../../docs/phases/phase-09a-binary-split.md).
- Not a config file. `config.yaml` is the runtime config.

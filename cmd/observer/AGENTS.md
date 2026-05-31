# AGENTS.md — `cmd/observer`

## What this is

The standalone `limen-observer` process. It decouples the billing metrics telemetry consumption from the hot path gateway (`limen-gateway`) to preserve gateway performance, separate operational roles, restrict database credential access, and scale ingestion and processing tiers independently.

`main.go` is thin and delegates initialization and stream draining to `internal/boot/serveobserver`.

## Conventions

- **Thin main.** Business logic is avoided here; composition and bootstrap only.
- **Boot via `boot.BootRuntime(cfg, boot.NeedStore|boot.NeedCipher|boot.NeedUpstream)`** — coordinates safe and secure database connectivity and secret field decryption (needed for fetching upstream config details).
- **Decoupled consumption.** Only processes streams from Valkey and writes to Postgres; never runs HTTP listeners (aside from `/healthz`), and never imports portal, signup, admin, Zitadel, or OAuth proxy packages.

## Boundaries

- Strictly enforces import boundaries via compile-time tests (`cmd/observer/import_graph_test.go`).

# AGENTS.md — `internal/boot`

## What this is

The shared **boot floor** every Limen binary stands up before mounting
routes. Owns:

- `BootRuntime(configPath, BootProfile)` — loads config, builds the zap
  logger + signal-cancellable context, opens the storage pools, parses
  the AES-SIV key + builds the cipher / state signer, opens Valkey +
  wires the upstream registry / service / refresher. Each dependency
  is gated by a `BootProfile` bitmask bit (`NeedStore`, `NeedCipher`,
  `NeedSigner`, `NeedUpstream`).
- `RunHTTPServer(rt, h)` — graceful-shutdown HTTP-server wrapper used
  by every binary.
- `MountHealth(r)` — `/healthz` + `/readyz`.
- `PermissiveCORS`, `LandingPage` — small primitives shared across
  binaries.

The schema-version guard runs unconditionally when `NeedStore` is set:
binaries refuse to start with a clear "run `limenctl migrate`" message
if the goose head in the DB doesn't match the highest embedded
migration.

## Subpackages

Each is the construction surface for ONE suite. Per-binary `serve*`
packages compose only the helpers they need.

| Package                          | What it builds / mounts                                                                                  | Imports outside `internal/`?                |
| -------------------------------- | -------------------------------------------------------------------------------------------------------- | ------------------------------------------- |
| `internal/boot/zitadelboot`      | The `*zitadel.Client` admin client                                                                       | `internal/zitadel`                          |
| `internal/boot/oidcboot`         | The OIDC RP (`*auth.OIDC`)                                                                               | `internal/auth`                             |
| `internal/boot/mcpmount`         | Gateway `Manager`, codemode handler, `transport.MCPServer`; mounts `/t/{tenant}/mcp/*` + the PRM handler | `internal/gateway`, `internal/transport`    |
| `internal/boot/portalmount`      | Mounts portal SPA + OIDC routes                                                                          | `internal/transport` (+ `internal/auth`)    |
| `internal/boot/oauthproxymount`  | Mounts DCR + AS metadata + redirector under `/t/{tenant}/oauth/*`                                        | `internal/oauthproxy`, `internal/zitadel`   |
| `internal/boot/upstreammount`    | Mounts `/t/{tenant}/upstream/{name}/callback`                                                            | `internal/transport`                        |
| `internal/boot/servegateway`     | `Run(configPath)` for `cmd/gateway` (MCP only)                                                           | `mcpmount`                                  |
| `internal/boot/serveportal`      | `Run(configPath)` for `cmd/portal` (portal + OIDC + OAuth proxy + upstream callback)                     | `oidcboot`, `zitadelboot`, all portal mounts |
| `internal/boot/servestaff`       | `Run(configPath)` for `cmd/staff` (scaffold)                                                             | `zitadelboot`                               |
| `internal/boot/serveobserver`    | `Run(configPath)` for `cmd/observer` (bootstraps background metrics consumer, serves healthz)          | `internal/billing/metrics`                  |
| `internal/boot/serveall`         | `Run(configPath)` for `cmd/limen` (union of every suite)                                                 | every mount package                         |

## Phase 9a load-bearing constraint

`internal/boot` itself must **never** import `internal/oauthproxy` or
`internal/zitadel`. That isolation is what lets `cmd/gateway` depend on
the boot floor without pulling the DCR + Zitadel management credential
surface into the hot-path binary. New shared code that needs those
packages goes into a sibling subpackage (alongside `zitadelboot` /
`oauthproxymount`), not into `internal/boot` itself.

The cross-binary import graph is locked in by
[`cmd/gateway/import_graph_test.go`](../../cmd/gateway/import_graph_test.go)
(forbids `internal/oauthproxy` + `internal/zitadel`),
[`cmd/portal/import_graph_test.go`](../../cmd/portal/import_graph_test.go)
(requires the portal-owned suites), and
[`cmd/staff/import_graph_test.go`](../../cmd/staff/import_graph_test.go)
(forbids the MCP hot path).

## Conventions

- **Profile-gated construction.** Each `BootProfile` bit guards exactly
  one dependency. Don't fold two unrelated dependencies behind a single
  flag — binaries should be able to opt in to the minimum they need.
- **Cleanups in reverse.** Use `rt.AddCleanup(fn)`; everything tears
  down in LIFO order on shutdown.
- **No globals.** The only intentional global is the AES-SIV cipher
  registered via `crypto.SetCipher` for `SecretField` (Phase 2). New
  shared state belongs on the `Runtime` struct.
- **No HTTP wiring here.** `internal/boot` provides primitives
  (`MountHealth`, `RunHTTPServer`, `PermissiveCORS`) but every route
  attaches inside a `*mount` subpackage. Adding `r.Get(...)` directly
  in `internal/boot` is a layering violation.

## Tests

`runtime_test.go` covers the `BootProfile` bit composition (`Has` +
`AllProfiles` union). The real boot tests live alongside each binary
as import-graph assertions; route-level coverage of the mount
helpers is exercised by the existing `internal/transport/*_test.go`
suites against the same handlers.

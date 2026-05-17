# AGENTS.md — `scripts/zitadel-bootstrap`

## What this is

A small standalone Go program that prepares a Zitadel instance for Limen.
It runs from `compose.dev.yaml` (`docker compose run --rm zitadel-bootstrap`)
and is **idempotent** — re-running it is always safe.

It is a separate Go module on purpose: it does not depend on any Limen
package, and it can be rebuilt without touching the main module.

## What it does

Against a fresh Zitadel, the bootstrap ensures the following exist:

| Resource                         | Detail                                                          |
| -------------------------------- | --------------------------------------------------------------- |
| `limen` organization             | Dedicated org for gateway resources; keeps the Zitadel default org clean. Name overridable via `LIMEN_GATEWAY_ORG_NAME`. |
| `Limen Gateway` project          | In the `limen` org; granted to tenant orgs and the staff org.   |
| `Limen Portal` app (OIDC / PKCE) | Web app, redirect = `LIMEN_PORTAL_REDIRECT`, role assertion on. |
| `Limen MCP RS` app (API)         | Audience for RFC 8707 `resource=` requests from MCP clients.    |
| Project roles                    | `member`, `admin`, `owner`, `super_admin`.                      |
| Sample tenant org                | `LIMEN_SAMPLE_TENANT_SLUG` (default `acme`).                    |

On success it prints, and writes to `LIMEN_BOOTSTRAP_OUT`, the values that
need to land in `.env`:

```
LIMEN_OIDC_PORTAL_CLIENT_ID=...
LIMEN_OIDC_MCP_RS_CLIENT_ID=...
LIMEN_OIDC_PROJECT_ID=...
LIMEN_GATEWAY_ORG_ID=...
LIMEN_SAMPLE_TENANT_ORG_ID=...
```

## How it authenticates

Zitadel writes a one-time PAT to `/pat/zitadel-admin-sa.pat` on first
container start (mounted to `scripts/zitadel-bootstrap/.pat/...` on the host).
The bootstrap reads it from `ZITADEL_PAT_FILE`.

The PAT belongs to the IAM service-account user `zitadel-admin-sa` created
by `ZITADEL_FIRSTINSTANCE_ORG_MACHINE_*` in `compose.dev.yaml`. Do not commit
the `.pat` directory — it is in `.gitignore`.

## API style

The Zitadel HTTP/JSON gateway (`/management/v1/*`, `/admin/v1/*`) is used —
no gRPC code generation. Each helper:

- searches for the resource first (when search is supported), returning the
  existing ID;
- otherwise creates it and tolerates `409 Conflict` / "already exists" as a
  successful no-op via `alreadyExists`.

Org-scoped requests set the `x-zitadel-orgid` header. Instance-scoped
requests omit it.

## Adding a new bootstrap step

1. Add a `ensureFoo` method on `*client` following the search-then-create
   pattern. Always treat "already exists" as success.
2. Call it from `main` after the resources it depends on.
3. If the resulting ID is needed at runtime, append it to the `out` map so
   `.bootstrap-out.env` carries it.
4. Update `docs/development.md` if the developer needs to set a new env var.

## What this is NOT

- Not part of the Limen production deploy. Production Zitadel state is
  provisioned with Terraform (Phase 11), not this script.
- Not a Zitadel SDK. We hand-roll the few endpoints we need; do not grow
  this into a general-purpose client.
- Not on the import path of the Limen binary. Keep it that way.

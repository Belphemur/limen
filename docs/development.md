# Local development

This guide gets you from a fresh clone to a Limen instance signing users in
through Zitadel, provisioning tenants, and linking outbound MCP upstreams —
without leaving your laptop.

## Prerequisites

- Docker (with `docker compose`)
- Go 1.26+
- `curl`, `openssl` (used by `scripts/wait-for-zitadel.sh` and the Makefile)

## Quickstart

```bash
cp .env.example .env   # optional — most overrides come from the Makefile
make dev
```

That single command brings the whole environment up. It:

1. Starts the merged docker stack (Zitadel + login UI + Traefik + Zitadel
   Postgres + Limen Postgres + MailHog + Valkey) via three layered compose
   files — see the `COMPOSE :=` block in [Makefile](../Makefile).
2. Waits for Zitadel to be ready (`scripts/wait-for-zitadel.sh`).
3. Runs the idempotent Zitadel bootstrap (`make dev-bootstrap`), which
   creates a dedicated **`limen`** organization (so the Zitadel instance
   default org stays clean), the **Limen Gateway** project inside it, the
   Portal (OIDC) and MCP RS (API) apps, the project roles, a sample
   tenant org (`acme`), and the staff org. Tenant and staff orgs are
   wired up via project grants. Output lands in
   `scripts/zitadel-bootstrap/.bootstrap-out.env` (including
   `LIMEN_GATEWAY_ORG_ID`). Override the org name with
   `LIMEN_GATEWAY_ORG_NAME` if you need a different label.
4. Generates `.env.dev` (pinned `LIMEN_TOKEN_ENCRYPTION_KEY`) if missing.
5. Runs `make dev-run`, which sources the bootstrap output + `.env.dev`,
   reads `admin-sa.pat` live from the docker volume, inlines all dev
   defaults (DB DSNs, OIDC URLs, Zitadel host, Valkey address), and runs
   `go run ./cmd/gateway migrate && go run ./cmd/gateway serve`.

Limen listens on `http://localhost:8080`. Stop with `Ctrl-C`; the stack
keeps running. Re-launch Limen alone with `make dev-run`.

> **Cookie note** — on plain `http://localhost`, set
> `security.portal_session_cookie_secure: false` in [config.yaml](../config.yaml).
> Otherwise the browser drops the session cookie and the callback loops
> back to login.
>
> **Encryption-key note** — `.env.dev` is generated only when missing.
> `make dev-reset` deletes it (along with the docker volumes) so a fresh
> run starts with a new key.

## Make targets

| Target                                | What it does                                                            |
| ------------------------------------- | ----------------------------------------------------------------------- |
| `make dev`                            | Full bring-up: stack → bootstrap → migrate → serve.                     |
| `make dev-run`                        | Migrate + serve, auto-loading the env. Assumes the stack is already up. |
| `make dev-bootstrap`                  | Re-run the Zitadel bootstrap. Idempotent.                               |
| `make dev-cmd ARGS="…"`               | Run any `cmd/gateway` subcommand with the dev env auto-loaded.          |
| `make dev-migrate`                    | Run `migrate` with the dev env auto-loaded.                             |
| `make dev-create-tenant ARGS="…"`     | Run `create-tenant` with the dev env auto-loaded.                       |
| `make dev-create-upstream ARGS="…"`   | Run `create-upstream` with the dev env auto-loaded.                     |
| `make dev-down`                       | Stop services (keeps volumes).                                          |
| `make dev-reset`                      | Stop services, wipe volumes, drop `.env.dev` and `.bootstrap-out.env`.  |
| `make build`                          | `go build -o limen ./cmd/gateway`.                                      |
| `make test` / `make vet` / `make fmt` | Standard Go toolchain wrappers.                                         |

## CLI commands

`./cmd/gateway` (a.k.a. `limen`) exposes these subcommands:

| Command           | Purpose                                                                      |
| ----------------- | ---------------------------------------------------------------------------- |
| `serve`           | Run the HTTP gateway. Reads `config.yaml` + env overrides.                   |
| `migrate`         | Apply Goose migrations under `internal/storage/migrations/`.                 |
| `create-tenant`   | Provision a Limen tenant. Optionally creates the matching Zitadel org.       |
| `create-upstream` | Insert / update an MCP upstream registration for a tenant (PoC, `mcp_spec`). |

All of them need the dev env (DB DSN, OIDC client, Zitadel PAT, …). The
easiest way to run them is via the `make dev-*` wrappers above —
`dev-cmd ARGS="…"` covers anything not pre-wired. Example:

```bash
make dev-create-upstream ARGS='--tenant tnt_01KRH7… --name github --url https://api.githubcopilot.com/mcp/'
make dev-cmd ARGS='create-tenant --help'
```

To invoke them from a plain shell (e.g. to attach a debugger), see
[§ Running commands manually](#running-commands-manually).

## Walking the Phase 4 OIDC flow

After `make dev` is up:

1. Create a tenant. Two options:

   ```bash
   # Bind to the bootstrap-created `acme` org (no Zitadel writes, no PAT
   # required, manage users via the Zitadel Console).
   make dev-create-tenant ARGS='--name Acme --zitadel-org-id '"$LIMEN_SAMPLE_TENANT_ORG_ID"

   # Or: full flow — create the Zitadel org + seed owner. Owner gets an
   # "initial password" email at http://localhost:8025 (MailHog).
   make dev-create-tenant ARGS='--name "Demo Tenant" --owner-email you@example.com --owner-given-name You --owner-family-name Example'
   ```

   Each invocation prints a `PublicID` (`tnt_<ULID>`). Use that for every
   `/t/<tenant>/...` URL below — there is no slug.

2. Open `http://localhost:8080/t/<tenant>/auth/login` — Limen redirects to
   Zitadel.
3. Log in as the tenant owner.
4. Zitadel redirects to `/auth/callback`; Limen exchanges the code, sets
   the `limen_portal` cookie, and lands you on `/t/<tenant>/portal/`.
5. `GET /t/<tenant>/portal/me` returns JSON with `sub`, `email`, `name`,
   and the tenant id — confirms the cookie, tenant resolver, and user
   upsert all work.
6. `/t/<tenant>/auth/logout` clears the cookie and redirects through
   Zitadel's `end_session_endpoint`.

Negative checks:

- `/t/<tenant>/portal/me` with no cookie → 401 + login redirect.
- `/t/tnt_0000000000000000000000000Z/auth/login` → 404 (unknown tenant).

## Walking the Phase 7 outbound-upstream PoC

Once you have a tenant and a logged-in user, the portal page drives the
full connect / refresh / disconnect flow. The portal HTML is a developer
test surface — Phase 9b replaces it with a real SPA.

1. **Register an upstream.** v1 only supports the `mcp_spec` strategy
   (OAuth-via-PRM discovery — what Atlassian Rovo, GitHub MCP, etc.
   speak):

   ```bash
   make dev-create-upstream ARGS="--tenant $TENANT_PUBLIC_ID --name rovo --url https://mcp.atlassian.com/v1/rovo"
   ```

   Idempotent on `(tenant, name)` — re-running updates the MCP URL in
   place. Prints the new `ups_<ULID>` and the portal URL to visit.

2. **Connect from the portal.** Reload `http://localhost:8080/t/<tenant>/portal/`
   — the **MCP Upstreams** table shows the row as `disconnected`. Click
   **Connect**; the portal POSTs `/portal/upstreams/<name>/connect`, Limen
   runs MCP-spec discovery, performs DCR if needed, mints a state token,
   and returns the upstream AS's `authorize` URL. The browser follows the
   redirect.

3. **Approve at the upstream AS.** It redirects back to
   `/t/<tenant>/upstream/<name>/callback`; Limen finishes PKCE + token
   exchange, seals the access + refresh token into an `UpstreamLink`, and
   lands you back on the portal. Status flips to `connected`.

4. **Refresh.** The background refresher (`upstream_refresh:` in
   [config.yaml](../config.yaml)) rotates the access token whenever its
   expiry falls inside `refresh_window`.

5. **Disconnect.** The **Disconnect** button soft-deletes the
   `UpstreamLink`. The row flips back to `disconnected`; a subsequent
   **Connect** starts a fresh flow. Phase 8 will use the link's
   `Enabled` / `NeedsRelink` / `AutoDisabledAt` columns to gate tool
   calls; Phase 7 only persists state.

PoC notes:

- The four endpoints (`GET /portal/upstreams`, `POST .../connect`,
  `POST .../disconnect`) live in
  [internal/transport/portal_upstreams.go](../internal/transport/portal_upstreams.go),
  behind the same `RequireSession` middleware as `/portal/me`. Phase 9b
  replaces them with a typed Connect-RPC service — do not build external
  integrations against this shape.
- The `none` and `static_header` strategies work end-to-end at the
  `upstream.Service` layer but have no admin surface yet. Add them via
  SQL or extend `create-upstream` if you need them before Phase 9b.
- If you don't want Valkey at all, set `LIMEN_VALKEY_ADDRESS=` (empty) —
  linking is disabled and the gateway logs
  `valkey.address empty: upstream linking disabled` at boot.

## Running commands manually

`make dev-run` is the easy path. If you need to run `serve`,
`create-tenant`, `create-upstream` etc. from a plain shell — e.g. to
attach a debugger — replicate the env block from the `dev-run` recipe:

```bash
set -a
source scripts/zitadel-bootstrap/.bootstrap-out.env
source .env.dev
export LIMEN_BASE_URL=http://localhost:8080
export LIMEN_DB_DSN='postgres://limen_app:limen_app_dev@localhost:5432/limen?sslmode=disable'
export LIMEN_DB_ADMIN_DSN='postgres://limen_admin:limen_admin_dev@localhost:5432/limen?sslmode=disable'
export LIMEN_OIDC_ISSUER=http://localhost:8081
export LIMEN_OIDC_CLIENT_ID=$LIMEN_OIDC_PORTAL_CLIENT_ID
export LIMEN_OIDC_REDIRECT_URI=http://localhost:8080/auth/callback
export LIMEN_ZITADEL_DOMAIN=http://localhost:8081
export LIMEN_ZITADEL_AUTH_MODE=pat
export LIMEN_ZITADEL_PAT=$(docker run --rm -v limen-dev_zitadel-bootstrap:/p:ro alpine cat /p/admin-sa.pat)
export LIMEN_ZITADEL_PROJECT_ID=$LIMEN_OIDC_PROJECT_ID
export LIMEN_ZITADEL_MCP_RESOURCE_AUDIENCE=$LIMEN_OIDC_MCP_RS_CLIENT_ID
export LIMEN_VALKEY_ADDRESS=localhost:6380
set +a
```

Fish users: run `bash -l`, paste, then invoke `go run ./cmd/gateway …`
from there. The Makefile recipes pin `SHELL := /bin/bash` and work from
any interactive shell.

## Useful URLs

| URL                                                    | What                                                |
| ------------------------------------------------------ | --------------------------------------------------- |
| http://localhost:8080                                  | Limen (this gateway)                                |
| http://localhost:8081                                  | Zitadel console (`root` / `RootPassword1!`)         |
| http://localhost:8081/.well-known/openid-configuration | OIDC discovery (Limen validates `iss` against this) |
| http://localhost:8025                                  | MailHog inbox                                       |
| localhost:5432                                         | Limen Postgres (user `limen`, db `limen`)           |
| localhost:6380                                         | Limen Valkey                                        |

## Resetting

```bash
make dev-reset   # nukes Postgres volumes, the PAT, .env.dev, .bootstrap-out.env
make dev         # fresh stack + new bootstrap + new encryption key
```

`compose.dev.yaml` is optimized for iteration (TLS off, weak secrets,
ports exposed). It is **not** production-ready — see
[Phase 11](phases/phase-11-production-deployment.md) for the hardened stack.

## Troubleshooting

| Symptom                                               | Cause / fix                                                                                                                                                                                                                                 |
| ----------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `refers to undefined network zitadel`                 | You ran `docker compose -f compose.dev.yaml …` directly. The `zitadel` network lives in the upstream Zitadel compose file — use `make dev` (or copy the full `COMPOSE :=` chain from the Makefile).                                         |
| `Project Grant not found` during bootstrap            | Stale Zitadel state. `make dev-reset && make dev`.                                                                                                                                                                                          |
| SDK error `ExternalDomain mismatch`                   | `ZITADEL_HOST` must equal Zitadel's `ExternalDomain` (default `localhost`). The bootstrap uses it as the gRPC `:authority`.                                                                                                                 |
| `invalid_client` at the token endpoint                | `LIMEN_OIDC_CLIENT_ID` must equal `LIMEN_OIDC_PORTAL_CLIENT_ID`, not `LIMEN_OIDC_MCP_RS_CLIENT_ID`.                                                                                                                                         |
| Callback loops back to login                          | Browser dropped the cookie. Set `portal_session_cookie_secure: false` for `http://localhost`.                                                                                                                                               |
| `password authentication failed for user "limen_app"` | Postgres volume predates `scripts/postgres-init/limen-roles.sql`. Either run it manually (`docker exec -i limen-dev-limen-postgres-1 psql -U limen -d limen < scripts/postgres-init/limen-roles.sql`) or `make dev-reset`.                  |
| `dial limen-valkey: no such host`                     | Limen on the host can't resolve docker DNS. `make dev-run` exports `LIMEN_VALKEY_ADDRESS=localhost:6380` — make sure that's set if you run `serve` manually.                                                                                |
| `create-tenant` fails with auth error                 | PAT changed (every `dev-reset` mints a new one). `make dev-run` re-reads it live; manual shells need to re-export.                                                                                                                          |
| `invalid state` after restarting `serve`              | `LIMEN_TOKEN_ENCRYPTION_KEY` rotated — it seeds the state-cookie HMAC. Keep `.env.dev` pinned and clear the stale `limen_state` cookie.                                                                                                     |
| `org mismatch want=<id> got=""`                       | Portal app isn't requesting `urn:zitadel:iam:user:resourceowner`. Check that scope is present in `oidc.scopes` of [config.yaml](../config.yaml). See [security.md — Tenant ↔ Zitadel org binding](security.md#tenant--zitadel-org-binding). |
| `org mismatch want=<acme-id> got=<other-id>`          | Logged-in user's home org isn't the tenant's bound org. Create / use a user inside the right org (Zitadel Console → switch org → Users → New).                                                                                              |

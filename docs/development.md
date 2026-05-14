# Local development

This guide gets you from a fresh clone to a Limen instance signing users in
through Zitadel — without leaving your laptop.

## Prerequisites

- Docker (with `docker compose`)
- Go 1.26+
- `curl` (for `scripts/wait-for-zitadel.sh`)

## First run

```bash
cp .env.example .env

# Brings up postgres, postgres-zitadel, zitadel, mailhog. Zitadel writes its
# initial admin PAT to scripts/zitadel-bootstrap/.pat on first start.
docker compose -f compose.dev.yaml up -d postgres postgres-zitadel zitadel mailhog
./scripts/wait-for-zitadel.sh

# Creates the Limen project, Portal + MCP RS apps, project roles, and a
# sample tenant org. Idempotent — safe to re-run.
make dev-bootstrap
```

The bootstrap script prints (and writes to
`scripts/zitadel-bootstrap/.bootstrap-out.env`):

- `LIMEN_OIDC_PORTAL_CLIENT_ID`
- `LIMEN_OIDC_MCP_RS_CLIENT_ID`
- `LIMEN_OIDC_PROJECT_ID`
- `LIMEN_SAMPLE_TENANT_ORG_ID`
- `LIMEN_SAMPLE_TENANT_NAME`
- `LIMEN_STAFF_ZITADEL_ORG_ID`
- `LIMEN_STAFF_BOOTSTRAP_EMAIL`

Copy those values into your `.env`, then start Limen on the host:

```bash
go run ./cmd/gateway serve
```

Or do the whole flow in one shot:

```bash
make dev
```

## Testing the Phase 4 OIDC POC

After `make dev-bootstrap` succeeds and the stack is up, you can walk the
end-to-end portal login flow in your browser.

### 1. Wire the environment

```bash
# Pull the bootstrap output into the current shell.
set -a
source scripts/zitadel-bootstrap/.bootstrap-out.env
set +a

# Limen runtime config.
export LIMEN_BASE_URL=http://localhost:8080
export LIMEN_DB_DSN='postgres://limen_app:limen_app_dev@localhost:5432/limen?sslmode=disable'
export LIMEN_DB_ADMIN_DSN='postgres://limen_admin:limen_admin_dev@localhost:5432/limen?sslmode=disable'

# IMPORTANT: pin the encryption key once, in a file you re-source in every
# shell, rather than regenerating with `openssl rand` each time. It seeds
# both the portal cookie cipher AND the state-cookie HMAC; rotating it
# invalidates every in-flight login (yields "invalid state" on callback)
# and every existing portal session.
[ -f .env.dev ] || echo "export LIMEN_TOKEN_ENCRYPTION_KEY=$(openssl rand -hex 32)" > .env.dev
source .env.dev

# OIDC RP (Phase 4 — Portal app created by bootstrap).
export LIMEN_OIDC_ISSUER=http://localhost:8081
export LIMEN_OIDC_CLIENT_ID=$LIMEN_OIDC_PORTAL_CLIENT_ID
export LIMEN_OIDC_REDIRECT_URI=http://localhost:8080/auth/callback

# Zitadel Management client (used by `create-tenant`).
export LIMEN_ZITADEL_DOMAIN=http://localhost:8081
export LIMEN_ZITADEL_PROJECT_ID=$LIMEN_OIDC_PROJECT_ID
export LIMEN_ZITADEL_AUTH_MODE=pat
export LIMEN_ZITADEL_PAT=$(docker run --rm -v limen-dev_zitadel-bootstrap:/p:ro alpine cat /p/admin-sa.pat)
```

> **Cookie note** — on plain `http://localhost`, set
> `security.portal_session_cookie_secure: false` in `config.yaml` (or the
> equivalent env override). Otherwise the browser drops the session cookie
> and the callback loops back to login.

### 2. Migrate Limen's database and create a test tenant

`limen create-tenant` either provisions a fresh Zitadel org + Limen row in
one shot, or binds the Limen row to an existing Zitadel org (e.g. the
`acme` org the bootstrap created):

```bash
go run ./cmd/gateway migrate

# Option A — bind to the bootstrap-created sample org (no Zitadel writes,
# no PAT required, manage users via the Zitadel Console).
go run ./cmd/gateway create-tenant \
  --name "Acme" \
  --zitadel-org-id "$LIMEN_SAMPLE_TENANT_ORG_ID"

# Option B — full flow: also create the Zitadel org + seed owner.
go run ./cmd/gateway create-tenant \
  --name "Demo Tenant" \
  --owner-email you@example.com \
  --owner-given-name You \
  --owner-family-name Example
```

Each `create-tenant` invocation prints the new tenant's `PublicID` (a
`tnt_<ULID>` string). Use that value wherever the docs below say
`/t/<tenant>/...` — there is no slug.

Option B sends a Zitadel "set initial password" mail to the owner — pick
it up at http://localhost:8025 (MailHog) and complete the init flow.

> If you get `password authentication failed for user "limen_app"`, your
> Postgres volume predates `scripts/postgres-init/limen-roles.sql`. Apply
> it manually with
> `docker exec -i limen-dev-limen-postgres-1 psql -U limen -d limen < scripts/postgres-init/limen-roles.sql`,
> or wipe the volume with `make dev-reset`.

### 3. Run the gateway

```bash
go run ./cmd/gateway serve
```

You should see it bind to `:8080` and log a successful OIDC discovery
against `http://localhost:8081`.

### 4. Walk the browser flow

1. Open http://localhost:8080/t/<tenant>/auth/login (substitute the
   `PublicID` printed by `create-tenant`) — Limen redirects to Zitadel.
2. Log in as the owner you just provisioned.
3. Zitadel redirects to `http://localhost:8080/auth/callback?state=…&code=…`.
   Limen exchanges the code, sets the `limen_portal` cookie, and lands you
   on `/t/<tenant>/portal/`.
4. Hit http://localhost:8080/t/<tenant>/portal/me — you should get JSON
   with `sub`, `email`, `name`, and the tenant id. This confirms the
   cookie, tenant resolver, and user upsert all work.
5. http://localhost:8080/t/<tenant>/auth/logout clears the cookie and
   redirects through Zitadel's `end_session_endpoint`.

### 5. Negative checks

- `/t/<tenant>/portal/me` with no cookie → 401 + login redirect.
- `/t/tnt_0000000000000000000000000Z/auth/login` → 404 (unknown tenant).

### Troubleshooting

| Symptom                                    | Cause / fix                                                                                                                 |
| ------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------- |
| `Project Grant not found` during bootstrap | Stale state from before the v2 rewrite. `make dev-reset && make dev`.                                                       |
| SDK error `ExternalDomain mismatch`        | `ZITADEL_HOST` must equal Zitadel's `ExternalDomain` (default `localhost`). The bootstrap uses it as the gRPC `:authority`. |
| `invalid_client` at the token endpoint     | `LIMEN_OIDC_CLIENT_ID` must equal `LIMEN_OIDC_PORTAL_CLIENT_ID`, not `LIMEN_OIDC_MCP_RS_CLIENT_ID`.                         |
| Callback loops back to login               | Browser dropped the cookie. Set `portal_session_cookie_secure: false` for `http://localhost`.                               |
| `create-tenant` fails with auth error      | Re-read the PAT — it changes on every `make dev-reset`.                                                                     |
| `invalid state` after restarting `serve`   | `LIMEN_TOKEN_ENCRYPTION_KEY` changed between runs — it seeds the state-cookie HMAC. Pin it (see step 1) and clear the stale `limen_state` cookie in the browser. |
| `org mismatch want=<id> got=""` in logs, browser shows `access denied` | The Portal app isn't requesting `urn:zitadel:iam:user:resourceowner`. Check that scope is present in `oidc.scopes` of [config.yaml](../config.yaml). See [security.md — Tenant ↔ Zitadel org binding](security.md#tenant--zitadel-org-binding) for the why. |
| `org mismatch want=<acme-id> got=<other-id>` | You logged in as a user whose home org is *not* the one bound to that tenant. Create / use a user inside the right org (Zitadel Console → switch to that org → Users → New). |

## Useful URLs

| URL                                                    | What                                                |
| ------------------------------------------------------ | --------------------------------------------------- |
| http://localhost:8080                                  | Limen (this gateway)                                |
| http://localhost:8081                                  | Zitadel console (root / RootPassword1!)             |
| http://localhost:8081/.well-known/openid-configuration | OIDC discovery (Limen validates `iss` against this) |
| http://localhost:8025                                  | MailHog inbox (Zitadel sends mail here)             |
| localhost:5432                                         | Limen Postgres (user `limen`, db `limen`)           |

## Resetting

```bash
make dev-reset   # nukes both Postgres volumes and the .pat file
```

After a reset, the next `up` re-initializes Zitadel and produces a new PAT.
Re-run `make dev-bootstrap` to recreate the project/apps/org.

## Configuration

`compose.dev.yaml` is optimized for iteration (TLS off, weak secrets, ports
exposed). It is **not** suitable for production — see
[Phase 11](phases/phase-11-production-deployment.md) for the hardened stack.

# Phase 0 — Development environment (Docker Compose)

**Depends on**: nothing — this is the very first step.
**Unblocks**: every other phase (developers, CI, and integration tests rely on this stack).

## Goal

Stand up the full Limen dependency stack on a developer laptop with a single command:

```
make dev
```

Under the hood that runs `docker compose --env-file scripts/zitadel/.env -f scripts/zitadel/docker-compose.yml -f scripts/zitadel/docker-compose.limen.yaml -f compose.dev.yaml up -d --wait`, merging three files into a single `limen-dev` project. The stack runs **PostgreSQL 18.2** as Limen's relational backend and **Zitadel v4.15.0** as the OAuth 2.1 / OIDC authorization server, fronted by Traefik with a separate Next.js Login UI container (mandatory in Zitadel v4). Limen itself is built and run on the host (live-reload) — only the dependencies are containerized. A production-grade compose lives in [Phase 11](phase-11-production-deployment.md); this one trades hardening for fast iteration.

## Architectural context (why Zitadel)

Limen does **not** implement an OAuth Authorization Server itself. Zitadel does. The split:

- **Zitadel** — AS for portal users and MCP clients. Issues JWTs. Owns user accounts, password / MFA flows, organization (= tenant) membership, sessions, DCR.
- **Limen** — OIDC Relying Party (portal login), MCP Resource Server (validates Zitadel JWTs on `/t/{tenant}/mcp`), and MCP-spec adapter (exposes PRM + a thin `/register` proxy onto Zitadel's Management API so MCP clients can DCR).

Tenant ↔ Zitadel **organization** (1:1). One Zitadel project shared across orgs holds the Portal SPA app and the MCP RS app definition.

The dev compose makes that wiring real on first `up`.

## Services

The stack is composed from three files merged into one project:

| File                                          | Owns                                                                                       |
| --------------------------------------------- | ------------------------------------------------------------------------------------------ |
| `scripts/zitadel/docker-compose.yml`          | Vendored verbatim from the upstream Zitadel repo. Defines `proxy` (Traefik v3.6.8), `zitadel-api`, `zitadel-login`, `postgres` (Zitadel's DB), and optional `redis` / `otel-collector` services. |
| `scripts/zitadel/docker-compose.limen.yaml`   | Overlay that seeds a Limen admin service-account PAT via `ZITADEL_FIRSTINSTANCE_*` on first init. |
| `compose.dev.yaml`                            | Limen-side services: `limen-postgres` (Postgres 18.2), `mailhog`, and the `zitadel-bootstrap` runner. |

Environment defaults (port, masterkey, image tags) live in `scripts/zitadel/.env`. The dev-friendly values bind Zitadel's Traefik on host port **8081** so host port 8080 stays free for the Limen Go binary.

Key version pins (kept in `scripts/zitadel/.env`):

- `ZITADEL_VERSION=v4.15.0`
- `TRAEFIK_IMAGE=traefik:v3.6.8`
- `POSTGRES_IMAGE=postgres:18.2-alpine` (Zitadel's bundled DB; matches the Limen DB pin)
- `REDIS_IMAGE=valkey/valkey:latest` (Valkey replaces Redis everywhere in the Limen stack)
- `ZITADEL_TLS_ENABLED=false` (env var, not a CLI flag — the v2-era `--tls-mode disabled` flag was removed in v4)

### Notes

- **Two Postgres instances** is deliberate: keeps Limen's data lifecycle independent from Zitadel's. Production (Phase 11) does the same. Both pin Postgres **18.2-alpine**.
- **Zitadel v4 Login UI** runs in a separate Next.js container (`zitadel-login`) reachable at `http://localhost:8081/ui/v2/login/`. Traefik routes `/ui/v2/login/*` and `/` to the login UI and everything else to `zitadel-api`. `ZITADEL_DEFAULTINSTANCE_FEATURES_LOGINV2_REQUIRED=true` is set so the API knows to redirect there.
- **TLS disabled in dev**: controlled via `ZITADEL_TLS_ENABLED=false` env; the issuer is `http://localhost:8081`.
- **MailHog** captures emails Zitadel sends for password resets, email verification, invitations. Web UI at `http://localhost:8025`.
- **Login Client PAT**: Zitadel writes a PAT for the bundled Login UI to `/zitadel/bootstrap/login-client.pat` inside a named volume (`zitadel-bootstrap`).
- **Limen admin PAT**: the `docker-compose.limen.yaml` overlay sets `ZITADEL_FIRSTINSTANCE_PATPATH=/zitadel/bootstrap/admin-sa.pat` and creates an additional `limen-admin-sa` machine user with `openid` scope. The `zitadel-bootstrap` container mounts the same volume read-only and authenticates against the Management API with that PAT.

## `scripts/zitadel-bootstrap/`

A small Go program (kept in the repo) that uses Zitadel's Management API to ensure the following exist:

| Resource                                 | Detail                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| ---------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `Limen Gateway` project                  | Hosted in the instance default org; shared across tenant orgs                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `Limen Portal` app (Web / PKCE)          | `redirect_uris=[http://localhost:8080/auth/callback]`, `response_types=[code]`, scope `openid profile email org.id`                                                                                                                                                                                                                                                                                                                                                                                                                            |
| `Limen MCP RS` app (API)                 | The audience MCP clients request via RFC 8707 `resource` parameter                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| Project role `member`                    | Default role for tenant users                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| Project role `admin`                     | Tenant admins                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| Project role `owner`                     | Tenant owners                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| Project role `super_admin`               | SaaS-operator role; honored only inside the staff tenant (see [Phase 12](phase-12-staff-backoffice.md))                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| Sample org `acme` + owner user           | For first-run testing; the script also calls `UserService.AddUserGrant(user, project, acme-org, ["owner"])`                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| Staff org `limen-staff` + staff user     | SaaS-operator org. The script creates one human user from `LIMEN_STAFF_BOOTSTRAP_EMAIL` (default `staff@limen.dev`), grants it `super_admin` against the Limen project, and emits `LIMEN_STAFF_ZITADEL_ORG_ID` in the bootstrap output — consumed by `limen-migrate` to ensure the `_staff` tenant row exists (see [Phase 12](phase-12-staff-backoffice.md) and [Phase 11](phase-11-production-deployment.md)). Zitadel sends an initialization email through SMTP (MailHog in dev) so the operator can set their own password and enroll MFA. |
| Allow `org.id` claim in token + userinfo | So Limen can extract `urn:zitadel:iam:user:resourceowner:id`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| Enable project-roles claim               | Project setting `assertRolesOnAuthentication=true` so the `urn:zitadel:iam:org:project:roles` claim is present in ID/access tokens — Limen reads roles from this claim                                                                                                                                                                                                                                                                                                                                                                         |

Bootstrap is **idempotent**: re-running it is safe. It exits early if the project already exists with the same configuration. The script's output prints the Portal app's `client_id` and the master env values the developer needs to put into `.env` for Limen.

## Developer ergonomics

- `.env.example` at the repo root captures the variables Limen reads:

  ```dotenv
  LIMEN_DB_DSN=postgres://limen:limen_dev@localhost:5432/limen?sslmode=disable
  LIMEN_TOKEN_ENCRYPTION_KEY=DLUtu+...32 bytes base64...
  LIMEN_OIDC_ISSUER=http://localhost:8081
  LIMEN_OIDC_PORTAL_CLIENT_ID=<from bootstrap output>
  LIMEN_OIDC_PORTAL_SCOPES=openid profile email urn:zitadel:iam:org:project:id:<project-id>:aud
  LIMEN_OIDC_MGMT_PAT=<service account PAT — only needed for DCR proxy>
  LIMEN_BASE_URL=http://localhost:8080
  # Phase 12 — staff (operator) tenant. The bootstrap script emits both values;
  # LIMEN_STAFF_ZITADEL_ORG_ID is consumed by limen-migrate to ensure the
  # _staff tenant row exists. Override LIMEN_STAFF_BOOTSTRAP_EMAIL before the
  # first `make dev` if you want a personal address (Zitadel will mail an
  # init link to it via MailHog).
  LIMEN_STAFF_BOOTSTRAP_EMAIL=staff@limen.dev
  LIMEN_STAFF_ZITADEL_ORG_ID=<from bootstrap output>
  ```

- `make dev` (or a Justfile) does:
  ```
  docker compose --env-file scripts/zitadel/.env \
    -f scripts/zitadel/docker-compose.yml \
    -f scripts/zitadel/docker-compose.limen.yaml \
    -f compose.dev.yaml \
    up -d --wait
  ./scripts/wait-for-zitadel.sh
  make dev-bootstrap
  go run ./cmd/gateway
  ```
- `make dev-reset` blows away the volumes and re-runs bootstrap.
- Frontend dev (Phase 9) runs `pnpm dev` in `web/` against the live Go server with Vite proxying.

## Deliverables

- New files:
  - `compose.dev.yaml`
  - `scripts/zitadel/docker-compose.yml` (vendored upstream — do not edit)
  - `scripts/zitadel/docker-compose.limen.yaml` (overlay seeding the Limen admin PAT)
  - `scripts/zitadel/.env` and `scripts/zitadel/.env.example`
  - `.env.example` (Limen-side env vars)
  - `scripts/zitadel-bootstrap/main.go` (and `go.mod`)
  - `scripts/wait-for-zitadel.sh`
  - `scripts/postgres-init/limen-roles.sql`
  - `docs/development.md` — quickstart referencing this phase
- `Makefile` with the `dev`, `dev-bootstrap`, `dev-reset`, `dev-down` targets.

## Verification

- `docker compose --env-file scripts/zitadel/.env -f scripts/zitadel/docker-compose.yml -f scripts/zitadel/docker-compose.limen.yaml -f compose.dev.yaml up -d --wait` reaches healthy state for `postgres` (Zitadel), `limen-postgres`, `proxy` (Traefik), `zitadel-api`, and `zitadel-login`.
- `make dev-bootstrap` exits 0 and writes `scripts/zitadel-bootstrap/.bootstrap-out.env`.
- `curl http://localhost:8081/.well-known/openid-configuration` returns valid metadata.
- `curl http://localhost:8081/ui/v2/login/healthy` returns 200 from the Login UI container.
- The bootstrap output prints `client_id`, project id, sample org id, and **staff org id**.
- The `limen-staff` org exists in the Zitadel console with one human user (`LIMEN_STAFF_BOOTSTRAP_EMAIL`) carrying the `super_admin` user grant against the `Limen Gateway` project. MailHog (`http://localhost:8025`) shows the Zitadel init-mail for that user.
- After populating `.env` and running `go run ./cmd/gateway`, visiting `http://localhost:8080/t/acme/portal/` redirects to Zitadel login, authenticating returns to Limen with a portal session.

## Risks

- **Zitadel image version**: pinned to `v4.15.0` in `scripts/zitadel/.env`. Track upstream's `.env.example` when bumping — v4 introduces breaking changes from v2 (TLS env, mandatory Login UI container, Traefik routing).
- **Bootstrap PAT lifecycle**: the upstream Login UI PAT is at `/zitadel/bootstrap/login-client.pat` and the Limen admin PAT (seeded by our overlay) is at `/zitadel/bootstrap/admin-sa.pat`. Both live in the `zitadel-bootstrap` named volume; `make dev-reset` wipes them along with the rest of the state.
- **Port collisions**: 5432 (Limen pg), 8080 (Limen gateway), 8081 (Traefik in front of Zitadel), 1025/8025 (MailHog) are all standard — document overrides via `scripts/zitadel/.env`.

## Checklist

- [x] Three-file layered compose (`scripts/zitadel/docker-compose.yml`, `scripts/zitadel/docker-compose.limen.yaml`, `compose.dev.yaml`) merges into the `limen-dev` project
- [x] Upstream Zitadel compose vendored verbatim and refreshable via `curl` from `zitadel/zitadel@main/deploy/compose/`
- [x] Zitadel pinned to `v4.15.0`; Traefik to `v3.6.8`; both Postgres instances to `postgres:18.2-alpine`
- [x] Limen Postgres pinned to `postgres:18.2-alpine` with `scripts/postgres-init/limen-roles.sql` provisioning `limen_admin` (BYPASSRLS) + `limen_app` roles
- [x] `ZITADEL_TLS_ENABLED=false` env (no `--tls-mode` flag — removed in v4)
- [x] `zitadel-login` Next.js container reachable at `http://localhost:8081/ui/v2/login/`
- [x] `LOGINV2_REQUIRED=true` and `OIDC_DEFAULTLOGINURLV2`/`DEFAULTLOGOUTURLV2` set so the API redirects to the v2 Login UI
- [x] Named volumes for both Postgres instances and the `zitadel-bootstrap` PAT volume; data persists across `up`/`down`
- [x] `scripts/zitadel-bootstrap/` ensures Limen project + Portal app + MCP RS app + sample org, authenticating with the admin PAT at `/zitadel/bootstrap/admin-sa.pat`
- [x] Bootstrap also ensures the `super_admin` project role, the `limen-staff` operator org, and a bootstrap staff user (`LIMEN_STAFF_BOOTSTRAP_EMAIL`) with `super_admin` granted
- [x] Bootstrap output emits `LIMEN_STAFF_ZITADEL_ORG_ID` (consumed by `limen-migrate` in [Phase 11](phase-11-production-deployment.md))
- [x] Bootstrap is idempotent
- [x] `.env.example` documents every Limen env var the dev workflow needs
- [x] `make dev` brings the stack up and runs Limen against it
- [x] `make dev-reset` cleanly wipes volumes
- [x] `docs/development.md` explains the first-run flow (incl. MailHog UI URL)
- [x] CI smoke job runs `make dev` (without the `go run`) and a basic OIDC discovery probe against `http://localhost:8081/.well-known/openid-configuration`

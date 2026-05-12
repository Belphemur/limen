# Phase 0 — Development environment (Docker Compose)

**Depends on**: nothing — this is the very first step.
**Unblocks**: every other phase (developers, CI, and integration tests rely on this stack).

## Goal

Stand up the full Limen dependency stack on a developer laptop with a single command:

```
docker compose -f compose.dev.yaml up -d
```

The stack runs **PostgreSQL 18.2** as the relational backend and **Zitadel** as the OAuth 2.1 / OIDC authorization server. Limen itself is built and run on the host (live-reload) — only the dependencies are containerized. A production-grade compose lives in [Phase 11](phase-11-production-deployment.md); this one trades hardening for fast iteration.

## Architectural context (why Zitadel)

Limen does **not** implement an OAuth Authorization Server itself. Zitadel does. The split:

- **Zitadel** — AS for portal users and MCP clients. Issues JWTs. Owns user accounts, password / MFA flows, organization (= tenant) membership, sessions, DCR.
- **Limen** — OIDC Relying Party (portal login), MCP Resource Server (validates Zitadel JWTs on `/t/{slug}/mcp`), and MCP-spec adapter (exposes PRM + a thin `/register` proxy onto Zitadel's Management API so MCP clients can DCR).

Tenant ↔ Zitadel **organization** (1:1). One Zitadel project shared across orgs holds the Portal SPA app and the MCP RS app definition.

The dev compose makes that wiring real on first `up`.

## Services

```yaml
# compose.dev.yaml (sketch — Phase 0 produces the actual file)
services:
  postgres:
    image: postgres:18.2-alpine
    environment:
      POSTGRES_USER: limen
      POSTGRES_PASSWORD: limen_dev
      POSTGRES_DB: limen
    ports: ["5432:5432"]
    volumes: [pg-data:/var/lib/postgresql/data]
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "limen"]
      interval: 5s
    command: >
      postgres
      -c shared_preload_libraries=pg_stat_statements
      -c max_connections=100

  postgres-zitadel:
    image: postgres:18.2-alpine
    environment:
      POSTGRES_USER: zitadel
      POSTGRES_PASSWORD: zitadel_dev
      POSTGRES_DB: zitadel
    volumes: [pg-zitadel-data:/var/lib/postgresql/data]
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "zitadel"]
      interval: 5s

  zitadel:
    image: ghcr.io/zitadel/zitadel:latest # pin in compose.dev.yaml (e.g. v2.x.y)
    command: >
      start-from-init
      --masterkey "MasterkeyNeedsToHave32Characters"
      --tls-mode disabled
    environment:
      ZITADEL_DATABASE_POSTGRES_HOST: postgres-zitadel
      ZITADEL_DATABASE_POSTGRES_PORT: 5432
      ZITADEL_DATABASE_POSTGRES_DATABASE: zitadel
      ZITADEL_DATABASE_POSTGRES_USER_USERNAME: zitadel
      ZITADEL_DATABASE_POSTGRES_USER_PASSWORD: zitadel_dev
      ZITADEL_DATABASE_POSTGRES_USER_SSL_MODE: disable
      ZITADEL_DATABASE_POSTGRES_ADMIN_USERNAME: zitadel
      ZITADEL_DATABASE_POSTGRES_ADMIN_PASSWORD: zitadel_dev
      ZITADEL_DATABASE_POSTGRES_ADMIN_SSL_MODE: disable
      ZITADEL_EXTERNALSECURE: "false"
      ZITADEL_EXTERNALDOMAIN: "localhost"
      ZITADEL_EXTERNALPORT: "8081"
      ZITADEL_FIRSTINSTANCE_ORG_HUMAN_USERNAME: "root"
      ZITADEL_FIRSTINSTANCE_ORG_HUMAN_PASSWORD: "RootPassword1!"
    ports: ["8081:8080"]
    depends_on:
      postgres-zitadel:
        condition: service_healthy

  zitadel-bootstrap: # runs once: creates Limen project & apps
    image: golang:1.26-alpine
    depends_on:
      zitadel:
        condition: service_started
    volumes:
      - ./scripts/zitadel-bootstrap:/work
    working_dir: /work
    command: ["go", "run", "./bootstrap.go"]
    environment:
      ZITADEL_API: "http://zitadel:8080"
      ZITADEL_PAT: "${ZITADEL_BOOTSTRAP_PAT}" # printed by zitadel on first init
      LIMEN_PORTAL_REDIRECT: "http://localhost:8080/auth/callback"
      LIMEN_MCP_RESOURCE_URI: "http://localhost:8080/t/{tenant}/mcp"

  mailhog:
    image: mailhog/mailhog:latest
    ports: ["1025:1025", "8025:8025"] # SMTP + web UI

volumes:
  pg-data:
  pg-zitadel-data:
```

### Notes

- **Two Postgres instances** is deliberate: keeps Limen's data lifecycle independent from Zitadel's. Production (Phase 11) does the same.
- **Postgres 18.2** chosen as the floor for the project — recent enough for modern partitioning and security defaults, well within Zitadel's supported range.
- **Zitadel TLS disabled** in dev. The Limen RS validates `iss` strictly so `http://localhost:8081` is the canonical issuer in dev configs.
- **MailHog** captures emails Zitadel sends for password resets, email verification, invitations. Web UI at `http://localhost:8025`.
- **First-instance bootstrap**: `ZITADEL_FIRSTINSTANCE_*` env vars create the IAM_OWNER on first start. The bootstrap container then creates the Limen project + Portal app + MCP RS app + a sample tenant.

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
  docker compose -f compose.dev.yaml up -d
  ./scripts/wait-for-zitadel.sh
  go run ./cmd/gateway
  ```
- `make dev-reset` blows away the volumes and re-runs bootstrap.
- Frontend dev (Phase 9) runs `pnpm dev` in `web/` against the live Go server with Vite proxying.

## Deliverables

- New files:
  - `compose.dev.yaml`
  - `.env.example`
  - `scripts/zitadel-bootstrap/main.go` (and `go.mod` if standalone, or part of the main module under `internal/devtools/zitadelbootstrap/`)
  - `scripts/wait-for-zitadel.sh`
  - `docs/development.md` — quickstart referencing this phase
- Optional: `Justfile` or `Makefile` with the targets above.

## Verification

- `docker compose -f compose.dev.yaml up -d` reaches healthy state for `postgres`, `postgres-zitadel`, `zitadel`, and the bootstrap container exits 0.
- `curl http://localhost:8081/.well-known/openid-configuration` returns valid metadata.
- The bootstrap output prints `client_id`, project id, sample org id, and **staff org id**.
- The `limen-staff` org exists in the Zitadel console with one human user (`LIMEN_STAFF_BOOTSTRAP_EMAIL`) carrying the `super_admin` user grant against the `Limen Gateway` project. MailHog (`http://localhost:8025`) shows the Zitadel init-mail for that user.
- After populating `.env` and running `go run ./cmd/gateway`, visiting `http://localhost:8080/t/acme/portal/` redirects to Zitadel login, authenticating returns to Limen with a portal session.

## Risks

- **Zitadel image version**: pin to a specific tag once the team standardizes. `latest` is fine for an initial spike but breaks reproducibility.
- **Bootstrap PAT lifecycle**: Zitadel prints a PAT once on first init. The bootstrap script handles this by writing it to `./scripts/zitadel-bootstrap/.pat` (gitignored) on first run.
- **Port collisions**: 5432, 8080, 8081, 1025, 8025 are all standard — document overrides via `.env`.

## Checklist

- [x] `compose.dev.yaml` defines `postgres`, `postgres-zitadel`, `zitadel`, `zitadel-bootstrap`, `mailhog` with healthchecks
- [x] Postgres images pinned to `postgres:18.2-alpine`
- [x] Zitadel image pinned to a specific tag (not `latest`) once chosen
- [x] Named volumes for both Postgres instances; data persists across `up`/`down`
- [x] `scripts/zitadel-bootstrap/` ensures Limen project + Portal app + MCP RS app + sample org
- [x] Bootstrap also ensures the `super_admin` project role, the `limen-staff` operator org, and a bootstrap staff user (`LIMEN_STAFF_BOOTSTRAP_EMAIL`) with `super_admin` granted — prerequisite for [Phase 12](phase-12-staff-backoffice.md)
- [x] Bootstrap output emits `LIMEN_STAFF_ZITADEL_ORG_ID` (consumed by `limen-migrate` in [Phase 11](phase-11-production-deployment.md) to ensure the `_staff` tenant row)
- [x] Bootstrap is idempotent
- [x] `.env.example` documents every Limen env var the dev workflow needs
- [x] `make dev` brings the stack up and runs Limen against it
- [x] `make dev-reset` cleanly wipes volumes
- [x] `docs/development.md` explains the first-run flow (incl. MailHog UI URL)
- [x] CI smoke job runs `docker compose up` + a basic OIDC discovery probe against Zitadel

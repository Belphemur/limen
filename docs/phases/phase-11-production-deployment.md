# Phase 11 — Production deployment (Docker Compose)

**Depends on**: every other phase delivered and verified.
**Unblocks**: shipping.

## Goal

Provide a production-shaped Docker Compose stack that an operator can run on a single VM (or adapt to Kubernetes / Nomad) and serve real traffic over TLS. The stack mirrors the dev one in [Phase 0](phase-00-dev-environment.md) but adds: TLS termination, secret management, persistent backups, log shipping, restart policies, resource limits, and explicit separation of the migration / bootstrap one-shots from the long-running services.

The dev compose is for fast iteration; the prod compose is the **reference reproducible deployment**. Operators who run Kubernetes can read it as documentation and translate the manifests.

## Architectural recap

Same as dev:

- **Zitadel** — OIDC AS for portal users and MCP clients. Authoritative for identity, sessions, MFA, password policy.
- **Limen** — OIDC RP for the portal; MCP Resource Server for `/t/{tenant}/mcp`; DCR proxy onto Zitadel's Management API.
- **Two Postgres 18.2 instances** — one for Limen, one for Zitadel.
- **Reverse proxy** — terminates TLS, routes by hostname, proxies to Limen and Zitadel.

## Topology

```
                              ┌─────────────────────────────────────────┐
                              │  Caddy (or Traefik)                     │
        Internet  ─────TLS───▶│  - limen.example.com   → limen:8080     │
                              │  - auth.limen.example.com → zitadel:8080│
                              └─────────────────────────────────────────┘
                                                │
                ┌───────────────────────────────┼────────────────────────────┐
                ▼                               ▼                            ▼
         ┌─────────────┐                ┌──────────────┐             ┌──────────────┐
         │   limen     │                │   zitadel    │             │   mail relay │
         │ (Go binary) │                │              │             │   (postfix)  │
         └─────────────┘                └──────────────┘             └──────────────┘
                │                               │
                ▼                               ▼
         ┌─────────────┐                ┌──────────────┐
         │  postgres   │                │  postgres    │
         │   (limen)   │                │  (zitadel)   │
         └─────────────┘                └──────────────┘
```

## Services (`compose.prod.yaml` sketch)

```yaml
services:
  caddy:
    image: caddy:2-alpine
    restart: unless-stopped
    ports: ["80:80", "443:443"]
    volumes:
      - ./deploy/Caddyfile:/etc/caddy/Caddyfile:ro
      - ./web/dist:/srv/portal:ro          # SPA static build (self-hosted mode)
      - caddy-data:/data
      - caddy-config:/config
    depends_on: [limen, zitadel]

  postgres:
    image: postgres:18.2-alpine
    restart: unless-stopped
    environment:
      POSTGRES_USER_FILE: /run/secrets/limen_pg_owner_user
      POSTGRES_PASSWORD_FILE: /run/secrets/limen_pg_owner_password
      POSTGRES_DB: limen
    secrets: [limen_pg_owner_user, limen_pg_owner_password]
    volumes:
      - pg-data:/var/lib/postgresql/data
      - ./deploy/postgres/limen-init.sql:/docker-entrypoint-initdb.d/00-init.sql:ro
    healthcheck:
      test:
        ["CMD-SHELL", "pg_isready -U $$(cat /run/secrets/limen_pg_owner_user)"]
      interval: 10s
      timeout: 5s
      retries: 5
    deploy:
      resources:
        limits: { cpus: "2.0", memory: "2g" }

  postgres-zitadel:
    image: postgres:18.2-alpine
    restart: unless-stopped
    environment:
      POSTGRES_USER_FILE: /run/secrets/zitadel_pg_user
      POSTGRES_PASSWORD_FILE: /run/secrets/zitadel_pg_password
      POSTGRES_DB: zitadel
    secrets: [zitadel_pg_user, zitadel_pg_password]
    volumes: [pg-zitadel-data:/var/lib/postgresql/data]
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $$(cat /run/secrets/zitadel_pg_user)"]
      interval: 10s

  limen-migrate:
    image: ghcr.io/belphemur/limen:${LIMEN_VERSION}
    command: ["limen", "-migrate"]
    depends_on:
      postgres:
        condition: service_healthy
    environment:
      LIMEN_DB_OWNER_DSN_FILE: /run/secrets/limen_db_owner_dsn
      LIMEN_TOKEN_ENCRYPTION_KEY_FILE: /run/secrets/limen_token_encryption_key
    secrets: [limen_db_owner_dsn, limen_token_encryption_key]
    restart: "no"

  zitadel-api:
    image: ghcr.io/zitadel/zitadel:${ZITADEL_VERSION}
    restart: unless-stopped
    command: start-from-init --masterkey "${ZITADEL_MASTERKEY}"
    environment:
      ZITADEL_PORT: 8080
      ZITADEL_EXTERNALSECURE: "true"
      ZITADEL_EXTERNALDOMAIN: "auth.limen.example.com"
      ZITADEL_EXTERNALPORT: "443"
      ZITADEL_TLS_ENABLED: "false" # Caddy terminates TLS
      ZITADEL_PUBLIC_SCHEME: "https"
      ZITADEL_DATABASE_POSTGRES_DSN_FILE: /run/secrets/zitadel_postgres_dsn
      ZITADEL_MASTERKEY_FILE: /run/secrets/zitadel_masterkey
      # v4 Login UI wiring (mandatory).
      ZITADEL_FIRSTINSTANCE_LOGINCLIENTPATPATH: /zitadel/bootstrap/login-client.pat
      ZITADEL_FIRSTINSTANCE_ORG_LOGINCLIENT_MACHINE_USERNAME: login-client
      ZITADEL_FIRSTINSTANCE_ORG_LOGINCLIENT_MACHINE_NAME: "Automatically Initialized IAM_LOGIN_CLIENT"
      ZITADEL_FIRSTINSTANCE_ORG_LOGINCLIENT_PAT_EXPIRATIONDATE: "2099-01-01T00:00:00Z"
      ZITADEL_DEFAULTINSTANCE_FEATURES_LOGINV2_REQUIRED: "true"
      ZITADEL_DEFAULTINSTANCE_FEATURES_LOGINV2_BASEURI: "https://auth.limen.example.com/ui/v2/login/"
      ZITADEL_OIDC_DEFAULTLOGINURLV2: "https://auth.limen.example.com/ui/v2/login/login?authRequest="
      ZITADEL_OIDC_DEFAULTLOGOUTURLV2: "https://auth.limen.example.com/ui/v2/login/logout?post_logout_redirect="
      ZITADEL_S3DEFAULTINSTANCE_SMTPCONFIGURATION_SMTP_HOST: "smtp.example.com:587"
      # ... full prod settings (see Zitadel docs)
    secrets:
      - zitadel_postgres_dsn
      - zitadel_masterkey
    volumes:
      - zitadel-bootstrap:/zitadel/bootstrap:rw
    depends_on:
      postgres-zitadel:
        condition: service_healthy

  zitadel-login:
    image: ghcr.io/zitadel/zitadel-login:${ZITADEL_VERSION}
    restart: unless-stopped
    environment:
      ZITADEL_API_URL: http://zitadel-api:8080
      NEXT_PUBLIC_BASE_PATH: /ui/v2/login
      ZITADEL_SERVICE_USER_TOKEN_FILE: /zitadel/bootstrap/login-client.pat
      CUSTOM_REQUEST_HEADERS: "Host:auth.limen.example.com,X-Forwarded-Proto:https"
    volumes:
      - zitadel-bootstrap:/zitadel/bootstrap:ro
    depends_on:
      zitadel-api:
        condition: service_healthy

  limen:
    image: ghcr.io/belphemur/limen:${LIMEN_VERSION}
    restart: unless-stopped
    environment:
      LIMEN_CONFIG: /etc/limen/config.yaml
      LIMEN_DB_DSN_FILE: /run/secrets/limen_db_app_dsn
      LIMEN_DB_OWNER_DSN_FILE: /run/secrets/limen_db_owner_dsn
      LIMEN_TOKEN_ENCRYPTION_KEY_FILE: /run/secrets/limen_token_encryption_key
      LIMEN_OIDC_ISSUER: "https://auth.limen.example.com"
      LIMEN_OIDC_PORTAL_CLIENT_ID_FILE: /run/secrets/limen_oidc_portal_client_id
      LIMEN_OIDC_MGMT_PAT_FILE: /run/secrets/limen_oidc_mgmt_pat
      LIMEN_BASE_URL: "https://limen.example.com"
    secrets:
      - limen_db_app_dsn
      - limen_db_owner_dsn
      - limen_token_encryption_key
      - limen_oidc_portal_client_id
      - limen_oidc_mgmt_pat
    volumes:
      - ./deploy/limen/config.yaml:/etc/limen/config.yaml:ro
    depends_on:
      limen-migrate:
        condition: service_completed_successfully
      zitadel:
        condition: service_started
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:8080/healthz"]
      interval: 30s

  backup:
    image: prodrigestivill/postgres-backup-local:17
    restart: unless-stopped
    environment:
      POSTGRES_HOST: postgres
      POSTGRES_DB: limen
      POSTGRES_USER_FILE: /run/secrets/limen_pg_owner_user
      POSTGRES_PASSWORD_FILE: /run/secrets/limen_pg_owner_password
      SCHEDULE: "@daily"
      BACKUP_KEEP_DAYS: 14
      BACKUP_KEEP_WEEKS: 8
      BACKUP_KEEP_MONTHS: 6
    secrets: [limen_pg_owner_user, limen_pg_owner_password]
    volumes: [./backups/limen:/backups]

  backup-zitadel:
    image: prodrigestivill/postgres-backup-local:17
    # ... same shape, points at postgres-zitadel

volumes:
  pg-data:
  pg-zitadel-data:
  caddy-data:
  caddy-config:
  zitadel-bootstrap:

secrets:
  limen_pg_owner_user: { file: ./secrets/limen_pg_owner_user }
  limen_pg_owner_password: { file: ./secrets/limen_pg_owner_password }
  limen_db_app_dsn: { file: ./secrets/limen_db_app_dsn }
  limen_db_owner_dsn: { file: ./secrets/limen_db_owner_dsn }
  limen_token_encryption_key: { file: ./secrets/limen_token_encryption_key }
  limen_oidc_portal_client_id:{ file: ./secrets/limen_oidc_portal_client_id }
  limen_oidc_mgmt_pat: { file: ./secrets/limen_oidc_mgmt_pat }
  zitadel_pg_user: { file: ./secrets/zitadel_pg_user }
  zitadel_pg_password: { file: ./secrets/zitadel_pg_password }
  zitadel_masterkey: { file: ./secrets/zitadel_masterkey }
```

### `deploy/Caddyfile` outline

The Limen API and the SPA share an origin (`limen.example.com`). API-shaped paths are reverse-proxied to the Go service; everything else is served from the static SPA build with an SPA-history fallback.

```caddy
limen.example.com {
    encode zstd gzip
    header Strict-Transport-Security "max-age=31536000; includeSubDomains; preload"
    header Content-Security-Policy "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self' https://auth.limen.example.com; img-src 'self' data:; frame-ancestors 'none'"

    # Routes owned by Limen — OAuth proxy, MCP RS, portal API, upstream connect,
    # OIDC login callbacks. Anything tenant-scoped under /t/{tenant}/ that isn't
    # the portal SPA itself.
    @api {
        path /t/*/api/*
        path /t/*/oauth/*
        path /t/*/mcp
        path /t/*/mcp/*
        path /t/*/upstream/*
        path /auth/*
        path /.well-known/*
        path /register*
        path /healthz
    }
    reverse_proxy @api limen:8080

    # SPA: everything else. The Vite build lives in /srv/portal, hashed asset
    # filenames get long-cache headers, and unknown deep links fall back to
    # index.html so Vue Router can take over.
    root * /srv/portal
    @assets path /assets/*
    header @assets Cache-Control "public, max-age=31536000, immutable"
    header /index.html Cache-Control "no-store"
    try_files {path} /index.html
    file_server
}

auth.limen.example.com {
    encode zstd gzip
    header Strict-Transport-Security "max-age=31536000; includeSubDomains; preload"

    # Zitadel v4 splits the Login UI into a separate Next.js container.
    # Route /ui/v2/login/* and the bare root (which the API redirects to the
    # login screen) to zitadel-login; everything else hits the API.
    @login path /ui/v2/login*
    handle @login { reverse_proxy zitadel-login:3000 }
    handle / { reverse_proxy zitadel-login:3000 }
    handle { reverse_proxy h2c://zitadel-api:8080 }
}
```

### Cloudflare Pages alternative

For managed deployments, skip the `./web/dist:/srv/portal` mount and replace the SPA block in the Caddyfile with a reverse proxy to a Pages project:

```caddy
    handle @api { reverse_proxy limen:8080 }
    handle { reverse_proxy https://limen-portal.pages.dev { header_up Host {upstream_hostport} } }
```

The SPA is published with `wrangler pages deploy web/dist --project-name=limen-portal` in CI. A `web/public/_headers` file in the Pages project carries the same CSP + cache directives shown above so behavior is identical regardless of which host serves the static files. Because Caddy still terminates TLS at `limen.example.com` and routes API traffic to Limen, the browser sees a single origin and the Phase 4 cookie scoping continues to work unchanged.

### `deploy/postgres/limen-init.sql`

Runs on first start of the Limen Postgres container to create the two app roles described in [Phase 3](phase-03-postgres-rls.md):

```sql
CREATE ROLE limen_admin LOGIN PASSWORD :'admin_pw' BYPASSRLS;
CREATE ROLE limen_app   LOGIN PASSWORD :'app_pw'   NOBYPASSRLS;
ALTER DATABASE limen OWNER TO limen_admin;
GRANT CONNECT ON DATABASE limen TO limen_app;
```

The init script reads the passwords from secrets (mounted via env / `docker secret`). The DSN secrets pre-bake the right username so application code stays the same as in dev.

## Secrets policy

- **Files-in-`./secrets/`** for the bare compose case; mode `0600`, owned by root, never committed.
- **Docker Swarm / Kubernetes** target: replace `file:` sources with native `external: true` secret references. The compose stays unchanged structurally.
- **Encryption key (`limen_token_encryption_key`)** is the most sensitive item — a leak invalidates all encrypted-at-rest columns. Document rotation procedure in the runbook (Phase 10).
- **Zitadel masterkey** is similarly sensitive — losing it locks operators out of recovering Zitadel-encrypted data.

## Migration strategy

- Limen schema migration runs as a **one-shot service** (`limen-migrate`) with `restart: "no"` and `condition: service_completed_successfully` gating the long-running `limen` service. This ensures schema is up-to-date before traffic flows.
- The same `limen-migrate` one-shot **also ensures the `_staff` tenant row exists** (see [Phase 12](phase-12-staff-backoffice.md)) by `INSERT ... ON CONFLICT DO NOTHING` against `tenants` with kind=`staff` and URL segment `_staff`, linked to the Zitadel org id passed in via `LIMEN_STAFF_ZITADEL_ORG_ID`. In prod the migrate container refuses to start if this env var is missing — the deploy script sources it from `secrets/`. The Zitadel side (org + `super_admin` role + bootstrap user) is provisioned out-of-band by the Phase 0 bootstrap script run against the prod Zitadel instance.
- Zitadel migrations run automatically inside the Zitadel container; no separate service needed.
- Rolling deploys: `limen-migrate` is run with the new image version _before_ the `limen` service is updated, manually or via the deploy script.

## Observability

- Limen logs to stdout in JSON. Compose default logging driver suffices for single-host; production should forward to a central sink (Loki, ELK, CloudWatch).
- Zitadel logs similarly to stdout.
- Postgres slow-query logging enabled in `postgresql.conf` overrides shipped via `deploy/postgres/postgresql.conf`.
- `/healthz` endpoint (Phase 10) is wired into compose healthchecks.

## Backup & restore

- Daily `pg_dump` per database via the `backup` services.
- Backup volumes (`./backups/limen`, `./backups/zitadel`) are bind-mounted to a directory that the operator snapshots out-of-band (rsync to object storage, etc.).
- Restore procedure documented in `docs/runbook.md`:
  1. Stop `limen` (Zitadel continues running).
  2. `dropdb && createdb` from a fresh backup file.
  3. Start `limen-migrate` to bring schema to current.
  4. Start `limen`.
- Encrypted columns survive backup/restore as opaque bytes — the encryption key must move with the data, or the rows are useless.

## Deliverables

- `compose.prod.yaml`
- `deploy/Caddyfile`
- `deploy/postgres/limen-init.sql`
- `deploy/postgres/postgresql.conf` (optional but recommended for tuning)
- `deploy/limen/config.yaml` — production config with `${VAR}` placeholders resolved via env
- `secrets/` directory layout + gitignore
- `docs/runbook.md` updates: deployment, rotation, backup/restore, on-call (Phase 10 starts this; Phase 11 fills it in).

## Verification

- `docker compose -f compose.prod.yaml up -d` brings the stack to healthy with a real TLS cert (Caddy auto-issues via Let's Encrypt when DNS resolves).
- `curl https://auth.limen.example.com/.well-known/openid-configuration` returns Zitadel metadata.
- `curl https://limen.example.com/healthz` returns 200.
- VS Code MCP client configured against `https://limen.example.com/t/<tenant>/mcp` walks the discovery chain (PRM → AS metadata → token → success).
- Stopping `postgres` and starting it again recovers — Limen waits via the healthcheck-driven `depends_on`.
- A backup file taken yesterday can be restored on a fresh stack and the portal works end-to-end.

## Risks

- **Single-host SPOF**: this compose is a single-VM reference. HA needs a multi-host orchestrator. Document that fact loudly in the README of the deploy folder.
- **Cert renewal**: Caddy handles it but requires port 80 reachable for HTTP-01. Document alternatives (DNS-01 with provider modules) for restrictive environments.
- **Zitadel upgrades**: pin to a tag; major upgrades may require their own migration step — link to Zitadel's release notes in the runbook.
- **Two Postgres** doubles ops surface; some teams will prefer a single instance with two databases. The compose can be adapted, but separate instances keep crash blast radius small.

## Checklist

- [ ] `compose.prod.yaml` defines `caddy`, `postgres`, `postgres-zitadel`, `limen-migrate`, `limen`, `zitadel`, `backup`, `backup-zitadel`
- [ ] All images pinned to specific versions (no `latest`)
- [ ] Postgres images are `postgres:18.2-alpine`
- [ ] All secrets sourced from `docker secret` files (never inline env values)
- [ ] `limen-migrate` runs as a one-shot, gates `limen` via `condition: service_completed_successfully`
- [ ] `limen-migrate` ensures the `_staff` tenant row exists (Phase 12) and refuses to start in prod without `LIMEN_STAFF_ZITADEL_ORG_ID`
- [ ] Healthchecks on every long-running service; `restart: unless-stopped`
- [ ] Caddyfile configured for both `limen.example.com` and `auth.limen.example.com` with HSTS
- [ ] `deploy/postgres/limen-init.sql` provisions `limen_admin` and `limen_app` roles with passwords from secrets
- [ ] Volumes named explicitly; backups mounted to a known host path
- [ ] Daily backup services configured with retention policy
- [ ] `docs/runbook.md` covers: first deploy, upgrade, rotate encryption key, backup, restore, incident response
- [ ] `.gitignore` blocks `secrets/` and `backups/`
- [ ] CI smoke job stands the compose up against ephemeral DNS + Caddy in internal-only mode and runs an end-to-end OIDC probe

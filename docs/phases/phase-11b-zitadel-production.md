# Phase 11b — Zitadel production hardening

**Depends on**: [Phase 11](phase-11-production-deployment.md).
**Unblocks**: Phase 11's production compose being safe to run in prod.

## Goal

Document the production-shaped Zitadel deployment so an operator can run a hardened Zitadel instance that Limen depends on. Phase 11 covers the multi-binary Limen stack and glues Zitadel in with `start-from-init`; this file fills the gaps that make Zitadel *production-ready*: lifecycle split, credential security, SMTP, token lifetimes, connection pooling, backup/restore, upgrades, and the Caddy workarounds that Zitadel requires.

## Architectural recap

Zitadel is the OIDC Authorization Server for the entire Limen stack. It is authoritative for:

- **Portal users** — authentication, sessions, MFA, password policy.
- **MCP clients** — dynamic client registration (DCR), token issuance.
- **Staff backoffice** — staff org, super_admin roles, Console deep links.

Zitadel ships as **two components** in v4:

| Component | What it does |
|-----------|--------------|
| `zitadel-api` | Go binary — serves Console, Management API, gRPC-gateway, OIDC endpoints, event store. Embedded web server for Console UI. |
| `zitadel-login` | Next.js — the v2 Login UI. Served as a separate container at `/ui/v2/login/`. Dials `zitadel-api` over HTTP/JSON. |

Both components share the same Postgres database (event-sourced via `eventstore.events`). The Zitadel API container embeds the Console UI; the login container is a separate Next.js app that calls back to the API for auth flows.

## 1. Init / setup / start split (one-shot compose pattern)

### The problem

Phase 11 uses `start-from-init` which combines database initialization, schema migrations, and serving traffic into a single command. This means:

- **Slow startup**: every restart re-runs all migration checks, taking 1-3 minutes before the first request is served.
- **Risky upgrades**: a failed upgrade can leave the database partially migrated with no clean rollback path.
- **No separation of concerns**: the init phase (run once ever) and setup phase (run every upgrade) are coupled to the long-running serve process.

### The fix

Split Zitadel into three services modeled after Zitadel's official `docker-compose.prodlike.yml` overlay:

```yaml
# compose.zitadel-prod.yaml — overlay on top of compose.prod.yaml
services:
  zitadel-init:
    image: ghcr.io/zitadel/zitadel:${ZITADEL_VERSION}
    command: 'init --config /etc/zitadel/config.yaml --masterkeyFile /run/secrets/zitadel_masterkey'
    restart: "no"
    depends_on:
      postgres-zitadel:
        condition: service_healthy
    volumes:
      - ./deploy/zitadel/config.yaml:/etc/zitadel/config.yaml:ro
    secrets: [zitadel_masterkey]

  zitadel-setup:
    image: ghcr.io/zitadel/zitadel:${ZITADEL_VERSION}
    command: 'setup --config /etc/zitadel/config.yaml --masterkeyFile /run/secrets/zitadel_masterkey'
    restart: "no"
    depends_on:
      zitadel-init:
        condition: service_completed_successfully
    volumes:
      - ./deploy/zitadel/config.yaml:/etc/zitadel/config.yaml:ro
    secrets: [zitadel_masterkey]

  zitadel-api:
    image: ghcr.io/zitadel/zitadel:${ZITADEL_VERSION}
    command: 'start --config /etc/zitadel/config.yaml'
    restart: unless-stopped
    depends_on:
      zitadel-setup:
        condition: service_completed_successfully
      zitadel-bootstrap:
        condition: service_completed_successfully
    environment:
      # External routing (Caddy terminates TLS)
      ZITADEL_PORT: "8080"
      ZITADEL_EXTERNALSECURE: "true"
      ZITADEL_EXTERNALDOMAIN: "auth.limen.example.com"
      ZITADEL_EXTERNALPORT: "443"
      ZITADEL_TLS_ENABLED: "false"
      ZITADEL_PUBLIC_SCHEME: "https"
      # Database
      ZITADEL_DATABASE_POSTGRES_DSN_FILE: /run/secrets/zitadel_postgres_dsn
      # v4 Login UI wiring
      ZITADEL_FIRSTINSTANCE_LOGINCLIENTPATPATH: /zitadel/bootstrap/login-client.pat
      ZITADEL_FIRSTINSTANCE_ORG_LOGINCLIENT_MACHINE_USERNAME: login-client
      ZITADEL_FIRSTINSTANCE_ORG_LOGINCLIENT_MACHINE_NAME: "Automatically Initialized IAM_LOGIN_CLIENT"
      ZITADEL_FIRSTINSTANCE_ORG_LOGINCLIENT_PAT_EXPIRATIONDATE: "2099-01-01T00:00:00Z"
      ZITADEL_FIRSTINSTANCE_ORG_HUMAN_PASSWORDCHANGEREQUIRED: "true"
      # Login v2 feature flags
      ZITADEL_DEFAULTINSTANCE_FEATURES_LOGINV2_REQUIRED: "true"
      ZITADEL_DEFAULTINSTANCE_FEATURES_LOGINV2_BASEURI: "https://auth.limen.example.com/ui/v2/login/"
      ZITADEL_OIDC_DEFAULTLOGINURLV2: "https://auth.limen.example.com/ui/v2/login/login?authRequest="
      ZITADEL_OIDC_DEFAULTLOGOUTURLV2: "https://auth.limen.example.com/ui/v2/login/logout?post_logout_redirect="
    volumes:
      - ./deploy/zitadel/config.yaml:/etc/zitadel/config.yaml:ro
      - ./data/zitadel-bootstrap:/zitadel/bootstrap:rw
    secrets: [zitadel_postgres_dsn]
    healthcheck:
      test: ["CMD", "/app/zitadel", "ready"]
      interval: 10s
      timeout: 30s
      retries: 12
      start_period: 20s
```

**Lifecycle semantics**:

- **`init`** — runs once ever. Creates the database user, schema, and initial instance-level data. Fails silently if already initialized (idempotent).
- **`setup`** — runs on each upgrade. Applies schema migrations, creates projections, updates instance defaults. Fast on subsequent runs since it detects current state.
- **`start`** — serves traffic. Starts in milliseconds since the schema is already up-to-date. **Does not receive `--masterkeyFile`** — only `init` and `setup` need the masterkey. The serve phase decrypts at rest via the database (masterkey was used during init/setup to encrypt).

**Parallel with Limen**: this mirrors Limen's own `limenctl-migrate` one-shot pattern, where `limenctl-migrate` gates the long-running `limen-gateway` and `limen-portal` services via `condition: service_completed_successfully`.

**Dependency chain**: the `zitadel-api` service now depends on both `zitadel-setup` *and* `zitadel-bootstrap`. This is required because bootstrap creates organizations, applications, roles, and project grants that the API serves. The full chain:

```
postgres-zitadel ──healthy──▶ zitadel-init ──completed──▶ zitadel-setup ──completed──▶ zitadel-api ──healthy──▶ zitadel-bootstrap ──completed──▶ limenctl-migrate
                                                                       └──▶ zitadel-login (also depends on zitadel-api healthy)
```

## 2. SMTP configuration

### The problem

Phase 11 has a typo in the SMTP environment variable: `ZITADEL_S3DEFAULTINSTANCE_SMTPCONFIGURATION_SMTP_HOST` — the `S3` is wrong, it should be `DE`. Additionally, five required SMTP fields are missing entirely, leaving Zitadel unable to send verification emails, password resets, or invitation links.

### The fix

Full SMTP configuration block in the environment section:

```yaml
environment:
  ZITADEL_DEFAULTINSTANCE_SMTPCONFIGURATION_SMTP_HOST: "smtp.sendgrid.net:587"
  ZITADEL_DEFAULTINSTANCE_SMTPCONFIGURATION_SMTP_USER_FILE: /run/secrets/zitadel_smtp_user
  ZITADEL_DEFAULTINSTANCE_SMTPCONFIGURATION_SMTP_PASSWORD_FILE: /run/secrets/zitadel_smtp_password
  ZITADEL_DEFAULTINSTANCE_SMTPCONFIGURATION_TLS_DISABLED: "false"
  ZITADEL_DEFAULTINSTANCE_SMTPCONFIGURATION_FROM: "noreply@limen.example.com"
  ZITADEL_DEFAULTINSTANCE_SMTPCONFIGURATION_FROMNAME: "Limen"
  ZITADEL_DEFAULTINSTANCE_SMTPCONFIGURATION_REPLYTOADDRESS: ""
```

Corresponding secrets:

```yaml
secrets:
  zitadel_smtp_user: { file: ./secrets/zitadel_smtp_user }
  zitadel_smtp_password: { file: ./secrets/zitadel_smtp_password }
```

**Important**: SMTP configuration is only applied during `zitadel setup`. If the SMTP env vars change after the initial setup has already run, the new values are **ignored**. To change SMTP settings post-setup, operators must use the Console UI (Settings → SMTP) or call the Admin API directly.

The `TLS_DISABLED: "false"` is explicit — omitting it may default to true on some Zitadel versions, causing plaintext credentials on the wire when connecting to the relay.

## 3. Masterkey security

### The problem

Phase 11 passes `--masterkey "${ZITADEL_MASTERKEY}"` on the command line. On Linux, command-line arguments are visible via `ps aux` to any process running as the same user. The masterkey encrypts **all secrets at rest** in Zitadel's database — it is the most sensitive credential in the entire stack.

### The fix

Use `--masterkeyFile` exclusively for all Zitadel commands that accept it:

```yaml
# WRONG (Phase 11) — masterkey visible via ps aux
command: start-from-init --masterkey "${ZITADEL_MASTERKEY}"

# RIGHT — masterkey read from a Docker secret, never appears in process list
command: init --masterkeyFile /run/secrets/zitadel_masterkey
```

The `--masterkeyFile` flag is required for both `init` and `setup`. The **`start` command does not accept `--masterkeyFile`** — it reads the masterkey from the database during initialization (it was already encrypted at setup time).

**Generating a masterkey**: must be 32 bytes exactly.

```bash
openssl rand -hex 16 | xxd -r -p > secrets/zitadel_masterkey
```

Or equivalently:

```bash
openssl rand 32 > secrets/zitadel_masterkey
```

**Critical warning**: losing the masterkey means losing **all** Zitadel-encrypted data irrecoverably. This includes:

- SMTP passwords
- OIDC client secrets
- TOTP seeds
- Signing keys (used for JWT / access tokens)
- Machine user credentials

There is no recovery path. The masterkey must be backed up alongside the database (see [Section 11](#11-backuprestore-with-masterkey)).

## 4. Production bootstrap mode

### The problem

The dev bootstrap at `scripts/zitadel-bootstrap/` configures the Limen Portal OIDC app with `DevelopmentMode: true` (line 207 of `main.go`), which:

- **Disables PKCE enforcement** — any redirect URI is accepted, enabling authorization code interception attacks.
- **Allows HTTP redirects** — production should reject any non-HTTPS redirect URI.

Additionally, the bootstrap seeds a sample tenant org (`acme`) with a hardcoded password (`Password1!`) for the owner user. This is useful for development and e2e testing but dangerous if provisioned in production.

### The fix

The bootstrap script gains a `--production` flag (or equivalent `LIMEN_PRODUCTION_MODE=true` environment variable) that:

1. **Sets `DevelopmentMode: false`** on the Limen Portal OIDC app — enforces PKCE and rejects HTTP redirect URIs.
2. **Skips creating the sample tenant org** (`acme`) and its seed user entirely.
3. **Does NOT output `LIMEN_SAMPLE_*` environment variables** in the bootstrap output summary or `.bootstrap-out.env` file.
4. **Keeps creating the `limen` gateway org, `Limen Gateway` project, `Limen Portal` app, `Limen MCP RS` app, project roles, staff org, and staff user** — these are required in all modes.

**Note**: the `--production` flag does **not** exist in the current code. It is a Phase 11b deliverable to add it to `scripts/zitadel-bootstrap/main.go`. The expected behavior is documented here so the implementation matches the design.

The flag should be parsed early in `main()` and stored as a boolean that gates the sample tenant and `DevelopmentMode` sections:

```
--production mode:
  ❌ no sample tenant org (acme)
  ❌ no sample seed user
  ❌ no LIMEN_SAMPLE_* output vars
  ✅ DevelopmentMode = false on Portal OIDC app
  ✅ still creates gateway org, project, apps, roles, staff org, staff user
```

When running in dev (the current default), `DevelopmentMode` stays `true` for convenience and the sample tenant is created as before.

## 5. Token lifetimes

Zitadel's default token lifetimes are designed for interactive web sessions. Limen's architecture — portal sessions backed by ID tokens and MCP clients holding long-lived SSE connections — requires longer-lived tokens:

```yaml
environment:
  ZITADEL_DEFAULTINSTANCE_OIDCSETTINGS_ACCESSTOKENLIFETIME: "12h"
  ZITADEL_DEFAULTINSTANCE_OIDCSETTINGS_IDTOKENLIFETIME: "12h"
  ZITADEL_DEFAULTINSTANCE_OIDCSETTINGS_REFRESHTOKENIDLEEXPIRATION: "720h"    # 30 days
  ZITADEL_DEFAULTINSTANCE_OIDCSETTINGS_REFRESHTOKENEXPIRATION: "2160h"       # 90 days
```

**Rationale**:

- **Access tokens (12h)**: portal sessions use ID tokens for authentication. A 12-hour window covers a full work day without requiring mid-day re-authentication. MCP clients refresh access tokens as needed.
- **ID tokens (12h)**: aligned with access tokens. The portal validates the ID token at session start.
- **Refresh token idle expiration (30 days)**: MCP clients may hold long-running SSE connections to the gateway. If a client's refresh token expires mid-session, the MCP stream breaks. 30 days of idle tolerance ensures that active sessions don't expire their refresh tokens.
- **Refresh token absolute expiration (90 days)**: provides an upper bound for credential rotation while being generous enough for production use. After 90 days, re-authentication is required.

Like SMTP, OIDC lifetime settings are only applied during `zitadel setup`. Post-setup changes require the Console UI (Project Settings → OIDC Token Settings) or the Admin API.

## 6. Database pool settings

Production Postgres instances — especially managed services with strict connection limits or a shared instance serving multiple applications — need conservative connection pool tuning:

```yaml
environment:
  ZITADEL_DATABASE_POSTGRES_MAXOPENCONNS: "15"
  ZITADEL_DATABASE_POSTGRES_MAXIDLECONNS: "10"
  ZITADEL_DATABASE_POSTGRES_MAXCONNLIFETIME: "1h"
  ZITADEL_DATABASE_POSTGRES_MAXCONNIDLETIME: "5m"
```

**Rationale**:

- **`MaxOpenConns: 15`** — Zitadel is efficient; it does not need hundreds of connections. This leaves headroom for managed Postgres connection limits (e.g., RDS small instances cap at ~100 connections).
- **`MaxIdleConns: 10`** — keeps warm connections ready for burst traffic without over-allocating.
- **`MaxConnLifetime: 1h`** — prevents connection reuse stalemates with Postgres session-level state. Managed services (RDS, Cloud SQL) may rotate backends; short lifetimes ensure connections are refreshed.
- **`MaxConnIdleTime: 5m`** — idle connections are recycled within 5 minutes, keeping the active pool clean.

If Zitadel shares a Postgres instance with Limen, these pool settings ensure Zitadel does not consume all available connections under load.

## 7. Login UI production config

The `zitadel-login` Next.js container needs production-specific configuration:

```yaml
  zitadel-login:
    image: ghcr.io/zitadel/zitadel-login:${ZITADEL_VERSION}
    restart: unless-stopped
    environment:
      ZITADEL_API_URL: http://zitadel-api:8080
      NEXT_PUBLIC_BASE_PATH: /ui/v2/login
      ZITADEL_SERVICE_USER_TOKEN_FILE: /zitadel/bootstrap/login-client.pat
      CUSTOM_REQUEST_HEADERS: "Host:auth.limen.example.com,X-Forwarded-Proto:https"
      NODE_ENV: "production"
    volumes:
      - ./data/zitadel-bootstrap:/zitadel/bootstrap:ro
    depends_on:
      zitadel-api:
        condition: service_healthy
    deploy:
      resources:
        limits: { cpus: "0.5", memory: "256m" }
```

**Configuration notes**:

- `ZITADEL_API_URL` is the **internal** URL to the API container — never the public hostname. The login UI calls the API over the Docker network.
- `NEXT_PUBLIC_BASE_PATH` must match the public URL path. Caddy routes `/ui/v2/login/*` to this container.
- `ZITADEL_SERVICE_USER_TOKEN_FILE` points to the login client PAT provisioned by Zitadel's init phase into the bootstrap volume. The volume mount is **read-only** — the login UI never writes to it.
- `CUSTOM_REQUEST_HEADERS` must match the **public-facing** host and protocol. These headers are forwarded to the Zitadel API so that Zitadel's `ExternalDomain` validation passes. `Host` must match `ZITADEL_EXTERNALDOMAIN` in the API config; `X-Forwarded-Proto` must be `https` because Caddy terminates TLS.
- `NODE_ENV=production` enables Next.js optimizations and disables development warnings. Without this, the login UI logs verbose debug output and uses slower rendering pipelines.

## 8. Console access and first-login walkthrough

The Zitadel Console is at `https://auth.limen.example.com/ui/console`. It is served by the `zitadel-api` container — the Console is embedded in the Go binary — **not** by the `zitadel-login` container. Caddy must route `/ui/console*` to `zitadel-api:8080` (not to the login UI).

### First login

Zitadel creates a default administrator user during the initial `setup` phase:

| Field | Value |
|-------|-------|
| Username | `zitadel-admin@auth.limen.example.com` |
| Password | `Password1!` (Zitadel default) |

The email domain matches the `ZITADEL_EXTERNALDOMAIN` set in the config.

### Production first-login procedure

1. Visit `https://auth.limen.example.com/ui/console` in a browser.
2. Log in as `zitadel-admin@auth.limen.example.com` with initial password `Password1!`.
3. **Change the password immediately**. Zitadel forces this when `ZITADEL_FIRSTINSTANCE_ORG_HUMAN_PASSWORDCHANGEREQUIRED: true` is set in the init environment (included in the overlay above).
4. Create a named admin user for day-to-day operations (e.g., `ops@limen.example.com`). Assign it `ORG_OWNER` in the default organization.
5. Configure SMTP via the Console if the SMTP env vars were not set at init time: **Settings → SMTP**. This is the post-setup path for SMTP configuration.
6. Bookmark the Console URL in the runbook.

### What operators do in the Console

The Console is the primary UI for Zitadel administration. Operators use it to:

- **Manage organizations** — create tenant orgs, configure branding, IdP federation.
- **Manage users** — create/disable users, reset passwords, assign roles.
- **Manage projects and applications** — configure OIDC settings, redirect URIs, token lifetimes.
- **Configure SMTP** — update relay host, credentials, from address.
- **Audit logs** — trace authentication events, login attempts, and configuration changes.
- **Password policy** — set complexity requirements, MFA rules.
- **Manage IdPs** — configure social login, enterprise SAML/OIDC federation.

For production, the `zitadel-admin` service account should be treated as a break-glass credential. Day-to-day operations use the named admin account created in step 4.

## 9. Caddy workarounds

### `TE: trailers` workaround for h2c

Zitadel's gRPC-gateway has a known conflict with the `TE: trailers` HTTP header. When Caddy forwards this header (which it does by default for HTTP/2 backends), requests that result in error responses **hang indefinitely**. This is a known issue in Zitadel's gRPC-Web / gRPC-Gateway implementation.

**Fix**: strip the `TE` header on the Zitadel API reverse proxy:

```caddy
auth.limen.example.com {
    encode zstd gzip
    header Strict-Transport-Security "max-age=31536000; includeSubDomains; preload"

    # Login UI — Next.js on port 3000
    @login path /ui/v2/login*
    handle @login { reverse_proxy zitadel-login:3000 }
    handle / { reverse_proxy zitadel-login:3000 }

    # Zitadel API — h2c required for gRPC
    # IMPORTANT: -TE strips the TE: trailers header that causes hangs on error responses.
    handle {
        reverse_proxy h2c://zitadel-api:8080 {
            header_up -TE
        }
    }
}
```

**Two things matter here**:

1. The Caddyfile **must** specify `h2c://` for the Zitadel API upstream — gRPC requires HTTP/2 cleartext. This is already correct in the Phase 11 Caddyfile outline.
2. The `header_up -TE` directive **must** be present on the `reverse_proxy` block — this strips the `TE: trailers` header from forwarded requests.

Without the `-TE` removal, operators will see intermittent hangs on error responses (4xx, 5xx) that are extremely difficult to debug. The hang manifests as a request that never returns, consuming the connection indefinitely.

## 10. External Postgres (RDS / Cloud SQL)

Some operators prefer a managed Postgres service (AWS RDS, GCP Cloud SQL) instead of running Postgres in a container. This section documents the path for external Postgres.

### Step 1: Provision the database externally

Create the database and role using your cloud provider's console or CLI:

```sql
CREATE ROLE zitadel LOGIN PASSWORD '<strong-password>';
CREATE DATABASE zitadel WITH OWNER zitadel;
GRANT CONNECT, CREATE ON DATABASE zitadel TO zitadel;
```

### Step 2: Run `zitadel init schema`

Because the DBA provisioned the user and database in Step 1, only the schema needs to be initialized:

```bash
ZITADEL_DATABASE_POSTGRES_DSN="postgresql://zitadel:<pw>@<rds-endpoint>:5432/zitadel?sslmode=require" \
  zitadel init schema
```

Run this from a workstation or CI/CD pipeline that has the Zitadel binary installed. The `init schema` command creates the tables, indexes, and event store structure without creating a database user or database.

### Step 3: Wire into production compose

- Remove `postgres-zitadel` from `compose.prod.yaml`.
- Point the `zitadel_postgres_dsn` secret at the managed instance:

```
# secrets/zitadel_postgres_dsn
postgresql://zitadel:<pw>@<rds-endpoint>:5432/zitadel?sslmode=require
```

- Apply the [Database pool settings](#6-database-pool-settings) — these are especially important for managed services with connection limits.

### Key notes

- **SSL is mandatory** — always use `sslmode=require` at minimum. For production, prefer `sslmode=verify-full` with the CA certificate mounted as a Docker secret.
- **No superuser needed** — the `zitadel` role only needs `CREATE` on the database for schema migrations. It never needs `SUPERUSER` privileges.
- **CockroachDB is NOT supported** — Zitadel only supports PostgreSQL. Do not attempt CockroachDB or other PostgreSQL-compatible databases.
- **Backup responsibility shifts** — the cloud provider handles database backups; the operator must still back up the masterkey (see [Section 11](#11-backuprestore-with-masterkey)).

## 11. Backup / restore with masterkey

Zitadel is event-sourced — all state is reconstructed from the `eventstore.events` table. A `pg_dump` of the Zitadel database captures the complete event stream.

**Critical**: the masterkey **must** be backed up alongside the database. Without it, all encrypted columns in the restored database are irrecoverable. The masterkey is the backup key.

### Backup

The `backup-zitadel` service in `compose.prod.yaml` runs daily `pg_dump` on the Zitadel database:

```yaml
  backup-zitadel:
    image: prodrigestivill/postgres-backup-local:17
    environment:
      POSTGRES_HOST: postgres-zitadel
      POSTGRES_DB: zitadel
      POSTGRES_USER_FILE: /run/secrets/zitadel_pg_user
      POSTGRES_PASSWORD_FILE: /run/secrets/zitadel_pg_password
      SCHEDULE: "@daily"
      BACKUP_KEEP_DAYS: 14
      BACKUP_KEEP_WEEKS: 8
      BACKUP_KEEP_MONTHS: 6
    secrets: [zitadel_pg_user, zitadel_pg_password]
    volumes: [./backups/zitadel:/backups]
```

Additionally, the `secrets/zitadel_masterkey` file must be included in whatever off-host backup strategy the operator uses (rsync to object storage, SOPS-encrypted vault, etc.).

### Restore procedure

1. **Stop all Limen and Zitadel services**:
   ```bash
   docker compose -f compose.prod.yaml -f compose.zitadel-prod.yaml down
   ```

2. **Restore the Zitadel database from pg_dump**:
   ```bash
   docker compose run --rm postgres-zitadel
   cat ./backups/zitadel/latest/zitadel.gz | gunzip | docker compose exec -T postgres-zitadel psql -U $$(cat /run/secrets/zitadel_pg_user) -d zitadel
   ```
   Or restore to a fresh Postgres container with the same credentials.

3. **Verify the masterkey**:
   ```bash
   # The masterkey in secrets/zitadel_masterkey must match the one used
   # when the backup was taken. If rotated, use the rotated key file.
   wc -c ./secrets/zitadel_masterkey   # should be 32 bytes
   ```

4. **Start Zitadel** — it replays projections from the event store automatically:
   ```bash
   docker compose -f compose.prod.yaml -f compose.zitadel-prod.yaml up -d zitadel-api zitadel-login
   ```

5. **Verify Console login works**:
   - Visit `https://auth.limen.example.com/ui/console`
   - Log in with an admin account
   - Check that organizations, users, and projects are visible

6. **Start Limen services**:
   ```bash
   docker compose -f compose.prod.yaml -f compose.zitadel-prod.yaml up -d
   ```

7. **Verify OIDC discovery**:
   ```bash
   curl https://auth.limen.example.com/.well-known/openid-configuration
   ```

## 12. Resource limits

Set resource limits to prevent Zitadel from consuming all host resources and to provide baseline guarantees:

```yaml
  zitadel-api:
    deploy:
      resources:
        limits:
          cpus: "2.0"
          memory: "2g"
        reservations:
          cpus: "0.5"
          memory: "512m"

  zitadel-login:
    deploy:
      resources:
        limits:
          cpus: "0.5"
          memory: "256m"
```

**Rationale**:

- `zitadel-api` is the heavier component — it processes OIDC flows, serves the Console, handles projection rebuilds, and serves gRPC-gateway endpoints. 2 CPU / 2 GB is sufficient for moderate traffic. Scale up if Zitadel serves thousands of OIDC requests per second. `reservations` guarantee enough memory for the Go runtime to start cleanly.
- `zitadel-login` is a Next.js app — relatively lightweight. 0.5 CPU / 256 MB is sufficient. The login page is rendered per-request and does not hold state.

## 13. Zitadel upgrade procedure

Zitadel upgrades should follow a disciplined procedure to minimize risk:

1. **Read the release notes** for the target Zitadel version. Major version upgrades may require manual migration steps or config changes.
2. **Pin `ZITADEL_VERSION` in `.env`**:
   ```
   ZITADEL_VERSION=v2.68.0
   ```
3. **Run `zitadel-setup` one-shot** — this handles all migrations:
   ```bash
   docker compose -f compose.prod.yaml -f compose.zitadel-prod.yaml up zitadel-setup --abort-on-container-exit
   ```
4. **Verify Console login** — log in as admin at `https://auth.limen.example.com/ui/console`.
5. **Verify Limen auth flows** — test the portal login, MCP token exchange, and staff Console deep links.
6. **If downtime is needed** (e.g., API-breaking changes):
   - Run `zitadel-setup` first (migrates the schema).
   - Restart `zitadel-api` and `zitadel-login` against the new image.
   - Restart Limen services to pick up any API changes.
7. **Major version upgrades** — always test in staging first. Zitadel's release notes will document any breaking changes or required config updates.

With the init/setup/start split, upgrades are fast: `zitadel-setup` runs migrations (typically seconds), then `zitadel-api` starts in milliseconds. The `start-from-init` approach required 1-3 minutes of migration checks on every restart.

## Deliverables

- `docs/phases/phase-11b-zitadel-production.md` (this file)
- `deploy/zitadel/config.yaml` — Zitadel configuration file with env var placeholders
- `compose.zitadel-prod.yaml` — overlay adding init/setup/start split to compose.prod.yaml
- Updated `compose.prod.yaml` with corrected SMTP env vars and masterkeyFile reference
- Updated `deploy/caddy/Caddyfile` (or the production Caddyfile variant) with `header_up -TE` on the Zitadel API reverse_proxy
- `scripts/zitadel-bootstrap/` — `--production` flag support in main.go (code change, deferred to Phase 11b)

## Verification

- [ ] `docker compose -f compose.prod.yaml -f compose.zitadel-prod.yaml up -d` brings the stack to healthy
- [ ] `docker compose ps` shows `zitadel-api` as `healthy`
- [ ] `curl https://auth.limen.example.com/.well-known/openid-configuration` returns 200 with valid OIDC metadata
- [ ] `curl https://auth.limen.example.com/ui/console` returns 200 (Console is accessible)
- [ ] `docker compose exec zitadel-api /app/zitadel ready` returns OK
- [ ] Bootstrap completes and outputs `PROJECT_ID`, `PORTAL_CLIENT_ID`, `STAFF_ZITADEL_ORG_ID`
- [ ] Bootstrap does NOT output `SAMPLE_TENANT_*` vars in production mode
- [ ] `ps aux | grep masterkey` shows nothing (masterkey is file-based, never in process args)
- [ ] SMTP is configured — Zitadel can send test emails from Console (Settings → SMTP)
- [ ] Token lifetimes match expected values (12h access/ID, 30d refresh idle, 90d refresh absolute)
- [ ] DB pool settings applied (verify via Zitadel logs or `pg_stat_activity`)
- [ ] Login UI serves at `/ui/v2/login/` with `NODE_ENV=production`
- [ ] Caddy `header_up -TE` on Zitadel API — error responses return immediately, no hangs
- [ ] Restore from backup with masterkey — Console login works

## Risks

- **Masterkey is the backup key** — lose it and all Zitadel-encrypted data (SMTP passwords, OIDC secrets, TOTP seeds, signing keys) is gone forever. Include it in every backup.
- **SMTP config applied only at setup time** — post-setup changes require the Console UI or Admin API. If the initial setup runs without SMTP config, configure it manually.
- **Zitadel upgrades may require manual migration steps** — always check release notes before upgrading, especially across major versions.
- **External Postgres means managing two databases** — if using managed Postgres, operators manage both Limen and Zitadel databases externally, doubling the ops surface.
- **Console access needs port 443 to Zitadel API** — ensure Caddy routes `/ui/console*` to `zitadel-api:8080` and not to the login UI.
- **Token lifetime changes require setup rerun** — like SMTP, OIDC token lifetimes are only applied during `setup`. Changing them post-deploy requires Console or API updates.
- **`TE: trailers` hang is subtle** — if the `header_up -TE` directive is missing, error responses hang indefinitely. This is extremely difficult to debug because the hang only occurs on errors, not on success paths.

## Checklist

- [ ] Zitadel uses init/setup/start split, not `start-from-init`
- [ ] Masterkey passed via `--masterkeyFile`, never on CLI as env var expansion
- [ ] Masterkey is 32 bytes, generated with `openssl rand 32`
- [ ] SMTP config uses correct `DEFAULTINSTANCE` prefix (not `S3DEFAULTINSTANCE`) with all 7 fields
- [ ] SMTP credentials come from secret files, not inline env values
- [ ] Token lifetimes configured for Limen's session/MCP patterns (12h/12h/720h/2160h)
- [ ] DB pool settings configured for production load (15/10/1h/5m)
- [ ] Login UI has `NODE_ENV=production` and correct `CUSTOM_REQUEST_HEADERS`
- [ ] Login UI volume mounts bootstrap PAT as read-only (`:ro`)
- [ ] Caddyfile has `header_up -TE` on Zitadel API reverse_proxy
- [ ] Caddyfile uses `h2c://` for Zitadel API upstream
- [ ] Bootstrap supports `--production` flag (skips sample tenant, sets `DevelopmentMode=false`)
- [ ] Console URL (`https://auth.limen.example.com/ui/console`) documented in runbook
- [ ] First-login procedure documented (default admin login, password change, named admin creation)
- [ ] `ZITADEL_FIRSTINSTANCE_ORG_HUMAN_PASSWORDCHANGEREQUIRED: true` set in init environment
- [ ] External Postgres path documented (init schema, sslmode=require, no superuser)
- [ ] Backup procedure includes masterkey backup requirement
- [ ] Restore procedure tested: stop services, restore DB, verify masterkey, start Zitadel, verify Console
- [ ] Resource limits set on `zitadel-api` (2 CPU / 2 GB) and `zitadel-login` (0.5 CPU / 256 MB)
- [ ] Zitadel upgrade procedure documented (release notes, setup one-shot, verify, restart)
- [ ] Zitadel API `depends_on` both `zitadel-setup` AND `zitadel-bootstrap`
- [ ] Zitadel `start` command does NOT include `--masterkeyFile`
- [ ] `ps aux | grep masterkey` confirms masterkey is not visible in process arguments

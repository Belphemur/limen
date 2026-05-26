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
- **Valkey** — short-lived OAuth state for the gateway (`NeedUpstream` profile) and portal OAuth proxy state.
- **Reverse proxy** — terminates TLS, routes by hostname, proxies to Limen binaries and Zitadel.

### Multi-binary architecture

Limen ships as **five separate Go binaries**, each with a distinct role, threat model, and secret footprint:

| Binary | Role | Public? | Key Secrets |
|--------|------|---------|-------------|
| `limen-gateway` | MCP RS hot path (only `/t/{tenant}/mcp/*`) | Yes (behind Caddy) | DB app DSN, token encryption key, Valkey password. **MUST NOT** hold Zitadel admin credential or portal session cipher key |
| `limen-portal` | Portal + OIDC RP + OAuth proxy (DCR) + upstream callback + Connect-RPC (Portal/Admin/Session/Signup services) + healthz | Yes (behind Caddy) | **ALL** secrets (Zitadel PAT, OIDC client, signer key, cipher key, SMTP, captcha, Valkey password) |
| `limen-staff` | Staff backoffice (scaffold in Phase 12) | No (private/VPN only) | DB app DSN, Zitadel PAT only. **NO** cipher, **NO** signer, **NO** Valkey |
| `limen` | All-in-one (dev + self-hosted alternative) | Yes | **ALL** secrets |
| `limenctl` | Admin CLI (migrate, create-tenant, create-upstream) | No (one-shot) | DB admin DSN for migrate, DB app DSN for other commands |

The production binaries (`limen-gateway`, `limen-portal`, `limen-staff`) accept `--config` or `LIMEN_CONFIG` env var and parse flags via the stdlib `flag` package. `limenctl` uses Cobra for its subcommands and also accepts `--config` / `LIMEN_CONFIG`.

The split exists for three reasons:

1. **Credential isolation** — the gateway handles the highest-traffic path (MCP streamable HTTP with SSE). If compromised, the attacker gains only the app-level DB read role and the token encryption key — *not* the Zitadel management PAT, portal session cipher, or SMTP credentials.
2. **Independent scaling** — gateway replicas can be scaled horizontally without also spinning up the heavier portal processes; portal and staff can scale separately based on their own traffic profiles.
3. **Distinct threat models** — gateway is internet-facing and stateless-heavy; staff is internal-only and should never be reachable from the public internet.

For small self-hosted deployments that do not need this separation, the `limen` all-in-one binary provides the same routes and functionality in a single process.

## Topology

```
                               ┌──────────────────────────────────────────────────────────┐
                               │  Caddy (or Traefik)                                      │
          Internet  ──TLS─────▶│  - limen.example.com                                     │
                               │      /t/*/mcp*        → limen-gateway:8080              │
                               │      /t/*/api/*       → limen-portal:8080                │
                               │      /t/*/auth/*      → limen-portal:8080                │
                               │      /t/*/oauth/*     → limen-portal:8080                │
                               │      /auth/*          → limen-portal:8080                │
                               │      /.well-known/*   → limen-portal:8080                │
                               │      /healthz         → limen-portal:8080                │
                               │      + SPA handlers   → limen-portal:8080                │
                               │  - auth.limen.example.com → zitadel:8080                 │
                               └──────────────────────────────────────────────────────────┘
                      │                             │
                      │                ┌────────────┼────────────┐
                      ▼                ▼            ▼            ▼
               ┌─────────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐
               │limen-gateway│  │limen-portal│  │ zitadel  │  │mail relay│
               │  (:8080)    │  │  (:8080)  │  │  api+    │  │(postfix) │
               └──────┬──────┘  └─────┬────┘  │  login   │  └──────────┘
                      │              │        └──────────┘
                      ├──────┐       ├──────┐
                      ▼      ▼       ▼      ▼
               ┌──────────┐ ┌─────────────┐
               │ postgres │ │   valkey    │
               │ (limen)  │ │  (:6379)   │
               └──────────┘ └─────────────┘

               ┌─────────────────────────────────────────┐
               │  Private network / VPN (no public ingress)│
               │                                         │
               │  ┌──────────┐                           │
               │  │limen-staff│                          │
               │  │  (:8080) │                          │
               │  └──────────┘                           │
               │      │                                  │
               │      ▼                                  │
               │  postgres (limen)                       │
               └─────────────────────────────────────────┘
```

## Services (`compose.prod.yaml` sketch)

```yaml
services:
  caddy:
    image: caddy:2-alpine
    restart: unless-stopped
    ports: ["80:80", "443:443"]
    volumes:
      - ./deploy/caddy/Caddyfile:/etc/caddy/Caddyfile:ro
      - ./web/portal/dist:/srv/portal:ro   # portal SPA static build
      - ./web/admin/dist:/srv/admin:ro     # tenant-admin SPA static build
      - ./data/caddy-data:/data
      - ./data/caddy-config:/config
    depends_on: [limen-gateway, limen-portal]

  postgres:
    image: postgres:18-alpine
    restart: unless-stopped
    environment:
      POSTGRES_USER_FILE: /run/secrets/limen_pg_owner_user
      POSTGRES_PASSWORD_FILE: /run/secrets/limen_pg_owner_password
      POSTGRES_DB: limen
    secrets: [limen_pg_owner_user, limen_pg_owner_password]
    volumes:
      - ./data/postgres-limen:/var/lib/postgresql/data
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
    image: postgres:18-alpine
    restart: unless-stopped
    environment:
      POSTGRES_USER_FILE: /run/secrets/zitadel_pg_user
      POSTGRES_PASSWORD_FILE: /run/secrets/zitadel_pg_password
      POSTGRES_DB: zitadel
    secrets: [zitadel_pg_user, zitadel_pg_password]
    volumes: [./data/postgres-zitadel:/var/lib/postgresql/data]
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $$(cat /run/secrets/zitadel_pg_user)"]
      interval: 10s

  valkey:
    image: valkey/valkey:8-alpine
    restart: unless-stopped
    command: ["--save", "", "--appendonly", "no"]
    volumes:
      - ./data/valkey:/data
    healthcheck:
      test: ["CMD", "valkey-cli", "ping"]
      interval: 10s
      timeout: 5s
      retries: 5
    deploy:
      resources:
        limits: { cpus: "0.5", memory: "256m" }
    networks:
      - default

  limenctl-migrate:
    image: ghcr.io/belphemur/limenctl:${LIMEN_VERSION}
    command: ["limenctl", "migrate", "--config", "/etc/limen/config.yaml"]
    depends_on:
      postgres:
        condition: service_healthy
      zitadel-bootstrap:
        condition: service_completed_successfully
    environment:
      LIMEN_CONFIG: /etc/limen/config.yaml
      LIMEN_DB_OWNER_DSN_FILE: /run/secrets/limen_db_owner_dsn
      LIMEN_TOKEN_ENCRYPTION_KEY_FILE: /run/secrets/limen_token_encryption_key
      LIMEN_STAFF_ZITADEL_ORG_ID_FILE: /run/secrets/limen_staff_zitadel_org_id
    secrets:
      - limen_db_owner_dsn
      - limen_token_encryption_key
      - limen_staff_zitadel_org_id
    volumes:
      - ./deploy/limen/config.yaml:/etc/limen/config.yaml:ro
    restart: "no"

  zitadel-bootstrap:
    image: golang:1-alpine
    working_dir: /work
    entrypoint: ["go", "run", "./main.go"]
    environment:
      ZITADEL_API_HOST: http://zitadel-api:8080
      ZITADEL_PAT_FILE: /run/secrets/zitadel_admin_pat
      LIMEN_GATEWAY_ORG_NAME: limen
      LIMEN_STAFF_ORG_NAME: limen-staff
      LIMEN_PORTAL_REDIRECT: https://limen.example.com/auth/callback
      LIMEN_PORTAL_POST_LOGOUT: https://limen.example.com/
      LIMEN_MCP_RESOURCE_URI: https://limen.example.com/t/{tenant}/mcp
    secrets: [zitadel_admin_pat]
    volumes:
      - ./data/bootstrap:/bootstrap
      - ./scripts/zitadel-bootstrap:/work:ro
    restart: "no"
    depends_on:
      zitadel-api:
        condition: service_healthy

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
      - ./data/zitadel-bootstrap:/zitadel/bootstrap:rw
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
      - ./data/zitadel-bootstrap:/zitadel/bootstrap:ro
    depends_on:
      zitadel-api:
        condition: service_healthy

  limen-gateway:
    image: ghcr.io/belphemur/limen-gateway:${LIMEN_VERSION}
    restart: unless-stopped
    command: ["limen-gateway", "--config", "/etc/limen/config.yaml"]
    environment:
      LIMEN_CONFIG: /etc/limen/config.yaml
      LIMEN_DB_DSN_FILE: /run/secrets/limen_db_app_dsn
      LIMEN_TOKEN_ENCRYPTION_KEY_FILE: /run/secrets/limen_token_encryption_key
      LIMEN_VALKEY_ADDR: "valkey:6379"
      LIMEN_VALKEY_PASSWORD_FILE: /run/secrets/limen_valkey_password
    secrets:
      - limen_db_app_dsn
      - limen_token_encryption_key
      - limen_valkey_password
    volumes:
      - ./deploy/limen/config.yaml:/etc/limen/config.yaml:ro
    depends_on:
      limenctl-migrate:
        condition: service_completed_successfully
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:8080/healthz"]
      interval: 30s
    deploy:
      replicas: 2
      resources:
        limits: { cpus: "2.0", memory: "1g" }

  limen-portal:
    image: ghcr.io/belphemur/limen-portal:${LIMEN_VERSION}
    restart: unless-stopped
    command: ["limen-portal", "--config", "/etc/limen/config.yaml"]
    environment:
      LIMEN_CONFIG: /etc/limen/config.yaml
      LIMEN_DB_DSN_FILE: /run/secrets/limen_db_app_dsn
      LIMEN_DB_OWNER_DSN_FILE: /run/secrets/limen_db_owner_dsn
      LIMEN_TOKEN_ENCRYPTION_KEY_FILE: /run/secrets/limen_token_encryption_key
      LIMEN_OIDC_ISSUER: "https://auth.limen.example.com"
      LIMEN_OIDC_PORTAL_CLIENT_ID_FILE: /run/secrets/limen_oidc_portal_client_id
      LIMEN_OIDC_MGMT_PAT_FILE: /run/secrets/limen_oidc_mgmt_pat
      LIMEN_BASE_URL: "https://limen.example.com"
      LIMEN_VALKEY_ADDR: "valkey:6379"
      LIMEN_VALKEY_PASSWORD_FILE: /run/secrets/limen_valkey_password
    secrets:
      - limen_db_app_dsn
      - limen_db_owner_dsn
      - limen_token_encryption_key
      - limen_oidc_portal_client_id
      - limen_oidc_mgmt_pat
      - limen_valkey_password
    volumes:
      - ./deploy/limen/config.yaml:/etc/limen/config.yaml:ro
    depends_on:
      limenctl-migrate:
        condition: service_completed_successfully
      zitadel-api:
        condition: service_started
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:8080/healthz"]
      interval: 30s
    deploy:
      replicas: 2
      resources:
        limits: { cpus: "1.0", memory: "512m" }

  limen-staff:
    image: ghcr.io/belphemur/limen-staff:${LIMEN_VERSION}
    restart: unless-stopped
    command: ["limen-staff", "--config", "/etc/limen/config.yaml"]
    profiles: ["staff"]
    environment:
      LIMEN_CONFIG: /etc/limen/config.yaml
      LIMEN_DB_DSN_FILE: /run/secrets/limen_db_app_dsn
      LIMEN_OIDC_MGMT_PAT_FILE: /run/secrets/limen_oidc_mgmt_pat
    secrets:
      - limen_db_app_dsn
      - limen_oidc_mgmt_pat
    volumes:
      - ./deploy/limen/config.yaml:/etc/limen/config.yaml:ro
    depends_on:
      limenctl-migrate:
        condition: service_completed_successfully
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:8080/healthz"]
      interval: 30s
    networks:
      - default
      - staff-private

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

networks:
  staff-private:
    internal: true

secrets:
  limen_pg_owner_user: { file: ./secrets/limen_pg_owner_user }
  limen_pg_owner_password: { file: ./secrets/limen_pg_owner_password }
  limen_db_app_dsn: { file: ./secrets/limen_db_app_dsn }
  limen_db_owner_dsn: { file: ./secrets/limen_db_owner_dsn }
  limen_token_encryption_key: { file: ./secrets/limen_token_encryption_key }
  limen_oidc_portal_client_id:{ file: ./secrets/limen_oidc_portal_client_id }
  limen_oidc_mgmt_pat: { file: ./secrets/limen_oidc_mgmt_pat }
  limen_staff_zitadel_org_id: { file: ./secrets/limen_staff_zitadel_org_id }
  limen_valkey_password: { file: ./secrets/limen_valkey_password }
  zitadel_pg_user: { file: ./secrets/zitadel_pg_user }
  zitadel_pg_password: { file: ./secrets/zitadel_pg_password }
  zitadel_masterkey: { file: ./secrets/zitadel_masterkey }
  zitadel_admin_pat: { file: ./secrets/zitadel_admin_pat }
```

### `deploy/caddy/Caddyfile` outline

Limen ships **two SPA bundles** plus the Go binaries behind one origin (`limen.example.com`). Caddy terminates TLS, serves the bundles, and forwards traffic to the appropriate binary based on path. Same-origin is what lets the `Path=/t/<tenant>` portal-session cookie cover both the customer portal and the admin SPA without `SameSite=None` or CORS preflights.

The route splitter is the key difference from dev. In production, `/t/*/mcp*` goes to `limen-gateway`; everything else goes to `limen-portal`. The dev Caddyfile ([deploy/caddy/Caddyfile.dev](../../deploy/caddy/Caddyfile.dev)) mirrors the same matchers against a single `:8000` origin so behaviour observed under `make dev` matches production. **Keep dev/prod Caddyfiles in lockstep** — if you change a matcher in one, change it in the other.

```caddy
limen.example.com {
    encode zstd gzip
    header Strict-Transport-Security "max-age=31536000; includeSubDomains; preload"
    header Content-Security-Policy "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self' https://auth.limen.example.com; img-src 'self' data:; frame-ancestors 'none'"

    # MCP Resource Server — the hot path. Routes to limen-gateway which
    # holds no Zitadel admin credential or portal session cipher key.
    @mcp {
        path /t/*/mcp
        path /t/*/mcp/*
    }
    reverse_proxy @mcp limen-gateway:8080

    # Portal + OIDC + OAuth proxy + upstream callback + health + discovery.
    # Routes to limen-portal which holds the Zitadel management credential
    # and portal session cipher key.
    @portalapi {
        path /t/*/api/*
        path /t/*/auth/*
        path /t/*/oauth/*
        path /t/*/mcp-servers/*/callback
        path /auth/login
        path /auth/callback
        path /auth/discovery
        path /auth/signup*
        path /api/limen.signup*
        path /.well-known/*
        path /healthz
    }
    reverse_proxy @portalapi limen-portal:8080

    # Self-serve signup wizard — lives in the admin SPA bundle but is
    # reached at the root (no /t/<tenant>/ prefix; the wizard *creates*
    # the tenant). The regex captures the suffix without its leading
    # slash, and the rewrite always re-prepends one, so /signup and
    # /signup/verify both resolve cleanly inside /srv/admin and Vue
    # Router takes over from there.
    @signup path_regexp signuproute ^/signup/?(.*)$
    handle @signup {
        rewrite * /{re.signuproute.1}
        root * /srv/admin
        @signupAssets path /assets/*
        header @signupAssets Cache-Control "public, max-age=31536000, immutable"
        header /index.html Cache-Control "no-store"
        try_files {path} /index.html
        file_server
    }

    # Tenant-admin SPA — owner/admin surface. Strip /t/<tenant>/admin/
    # before serving so the bundle's relative asset paths
    # (Vite `base: "./"`) resolve against /srv/admin. The regex
    # captures the suffix without its leading slash; the rewrite
    # re-prepends one so the empty case (/t/<tenant>/admin) still
    # resolves to /index.html via try_files instead of emitting a
    # bare "/" that some Caddy versions normalize away. SPA-history
    # fallback hands unknown deep links to Vue Router.
    @admin path_regexp adminroute ^/t/[^/]+/admin/?(.*)$
    handle @admin {
        rewrite * /{re.adminroute.1}
        root * /srv/admin
        @adminAssets path /assets/*
        header @adminAssets Cache-Control "public, max-age=31536000, immutable"
        header /index.html Cache-Control "no-store"
        try_files {path} /index.html
        file_server
    }

    # Customer portal SPA — same shape.
    @portal path_regexp portalroute ^/t/[^/]+/portal/?(.*)$
    handle @portal {
        rewrite * /{re.portalroute.1}
        root * /srv/portal
        @portalAssets path /assets/*
        header @portalAssets Cache-Control "public, max-age=31536000, immutable"
        header /index.html Cache-Control "no-store"
        try_files {path} /index.html
        file_server
    }

    # Anything else (bare /, signed-out shell) is owned by the
    # portal backend — it picks the right page for the request.
    reverse_proxy limen-portal:8080
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

For managed deployments, skip the `./web/portal/dist` and `./web/admin/dist` mounts and replace the two SPA handlers in the Caddyfile with reverse proxies to Pages projects (one per SPA):

```caddy
    reverse_proxy @mcp limen-gateway:8080

    reverse_proxy @portalapi limen-portal:8080

    @admin path_regexp adminroute ^/t/[^/]+/admin(/.*)?$
    handle @admin {
        rewrite * /{re.adminroute.1}
        reverse_proxy https://limen-admin.pages.dev { header_up Host {upstream_hostport} }
    }

    @portal path_regexp portalroute ^/t/[^/]+/portal(/.*)?$
    handle @portal {
        rewrite * /{re.portalroute.1}
        reverse_proxy https://limen-portal.pages.dev { header_up Host {upstream_hostport} }
    }

    reverse_proxy limen-portal:8080
```

Each SPA is published with its own `wrangler pages deploy` in CI (`web/portal/dist` to `limen-portal`, `web/admin/dist` to `limen-admin`). A `public/_headers` file in each Pages project carries the same CSP + cache directives shown above. Because Caddy still terminates TLS at `limen.example.com` and routes API traffic to the appropriate Limen binary, the browser sees a single origin and the `Path=/t/<tenant>` cookie scoping continues to work unchanged.

### `deploy/postgres/limen-init.sql`

Runs on first start of the Limen Postgres container to create the two app roles described in [Phase 3](phase-03-postgres-rls.md):

```sql
CREATE ROLE limen_admin LOGIN PASSWORD :'admin_pw' BYPASSRLS;
CREATE ROLE limen_app   LOGIN PASSWORD :'app_pw'   NOBYPASSRLS;
ALTER DATABASE limen OWNER TO limen_admin;
GRANT CONNECT ON DATABASE limen TO limen_app;
```

The init script reads the passwords from secrets (mounted via env / `docker secret`). The DSN secrets pre-bake the right username so application code stays the same as in dev.

## Secrets policy — per-binary isolation

All production binaries read the same `deploy/limen/config.yaml` file; each extracts only the keys it needs at startup. Docker Compose enforces the secret boundary at runtime — a binary that does not list a secret in its `secrets:` stanza cannot access it, even though the config file may reference other keys.

| Secret | Gateway | Portal | Staff | limenctl (migrate) |
|--------|---------|--------|-------|---------------------|
| `limen_db_app_dsn` | Yes | Yes | Yes | No |
| `limen_db_owner_dsn` | No | Yes (read) | No | Yes |
| `limen_token_encryption_key` | Yes | Yes | No | Yes |
| `limen_oidc_portal_client_id` | No | Yes | No | No |
| `limen_oidc_mgmt_pat` | No | Yes | Yes | No |
| `limen_staff_zitadel_org_id` | No | No | No | Yes |
| `limen_valkey_password` | Yes | Yes | No | No |

Additional policies:

- **Files-in-`./secrets/`** for the bare compose case; mode `0600`, owned by root, never committed.
- **Docker Swarm / Kubernetes** target: replace `file:` sources with native `external: true` secret references. The compose stays unchanged structurally.
- **Encryption key (`limen_token_encryption_key`)** is the most sensitive item — a leak invalidates all encrypted-at-rest columns. Document rotation procedure in the runbook (Phase 10).
- **Zitadel masterkey** is similarly sensitive — losing it locks operators out of recovering Zitadel-encrypted data.
- **Staff has no cipher key, no signer key** — it cannot mint portal session tokens or decrypt encrypted columns. Its Zitadel PAT grants only the minimum scopes needed for staff operations.

## Dockerfiles

Each production binary gets its own Dockerfile under `build/docker/`:

| Dockerfile | Output image |
|------------|-------------|
| `gateway.Dockerfile` | `ghcr.io/belphemur/limen-gateway` |
| `portal.Dockerfile` | `ghcr.io/belphemur/limen-portal` |
| `staff.Dockerfile` | `ghcr.io/belphemur/limen-staff` |
| `limenctl.Dockerfile` | `ghcr.io/belphemur/limenctl` |
| `limen.Dockerfile` | `ghcr.io/belphemur/limen` (all-in-one) |

All per-binary Dockerfiles share a common `go-build` base stage that installs Go toolchain, downloads module dependencies, and compiles the relevant `cmd/` package with `-trimpath -ldflags="-s -w"`. The final stage is `distroless/static` (or `gcr.io/distroless/static:nonroot`), copying only the compiled binary. The all-in-one `limen.Dockerfile` follows the same pattern but for `cmd/limen/main.go`.

CI runs a matrix build across all five Dockerfiles on every PR. On tag push, all five images are built and pushed to GHCR.

## Migration strategy

- Schema migration runs as a **one-shot service** (`limenctl-migrate`) with `restart: "no"` and `condition: service_completed_successfully` gating the long-running `limen-gateway`, `limen-portal`, and `limen-staff` services. This ensures the schema is up-to-date before traffic flows.
- `limenctl migrate --config /etc/limen/config.yaml` opens the database as `limen_admin` (via `LIMEN_DB_OWNER_DSN_FILE`) for DDL operations.
- The same migrate step **ensures the `_staff` tenant row exists** (see [Phase 12](phase-12-staff-backoffice.md)) by `INSERT ... ON CONFLICT DO NOTHING` against `tenants` with kind=`staff` and URL segment `_staff`, linked to the Zitadel org id passed in via `LIMEN_STAFF_ZITADEL_ORG_ID`. In prod the migrate container refuses to start if this env var is missing — the deploy script sources it from `secrets/`. The Zitadel side (gateway org, project, apps, roles, staff org, project grants) is provisioned via the one-shot `zitadel-bootstrap` compose service — see [First-time Zitadel provisioning](#first-time-zitadel-provisioning-bootstrap) below. The instance default org is intentionally left empty.
- Zitadel migrations run automatically inside the Zitadel container; no separate service needed.
- Gateway and portal both run a schema-version guard in `BootRuntime` — if the database schema version does not match the compiled version, the binary refuses to start. This prevents serving traffic against a stale or future schema.
- Rolling deploys: `limenctl-migrate` is run with the new image version _before_ the `limen-gateway` and `limen-portal` services are updated, manually or via the deploy script.

## First-time Zitadel provisioning (bootstrap)

Limen cannot boot without a Zitadel control plane already in place — the gateway org, project, app definitions, roles, staff org, and project grants. The `zitadel-bootstrap` one-shot service creates these resources on first deployment. It follows a search-then-create pattern (idempotent on repeated runs) and speaks directly to Zitadel's gRPC Management API.

### Approach

For the single-VM reference deployment, the dev bootstrap at `scripts/zitadel-bootstrap/` is reused for production. It is a standalone Go module with its own `go.mod`, runs as a one-shot container, and writes its outputs to a shared volume. A future `limenctl bootstrap` subcommand (using the production `internal/zitadel` client wrapper, supporting both `pat` and `jwt_key` auth modes) is the longer-term cleaner path.

### One-shot compose service

The `zitadel-bootstrap` service mounts the bootstrap source at `/work`, reads the Zitadel admin PAT from a secret, and writes its outputs to the `./data/bootstrap` bind mount. The operator must create `secrets/zitadel_admin_pat` before the first boot — this is the Zitadel management PAT with sufficient scopes to create orgs, projects, apps, and roles.

```yaml
  zitadel-bootstrap:
    image: golang:1-alpine
    working_dir: /work
    entrypoint: ["go", "run", "./main.go"]
    environment:
      ZITADEL_API_HOST: http://zitadel-api:8080
      ZITADEL_PAT_FILE: /run/secrets/zitadel_admin_pat
      LIMEN_GATEWAY_ORG_NAME: limen
      LIMEN_STAFF_ORG_NAME: limen-staff
      LIMEN_PORTAL_REDIRECT: https://limen.example.com/auth/callback
      LIMEN_PORTAL_POST_LOGOUT: https://limen.example.com/
      LIMEN_MCP_RESOURCE_URI: https://limen.example.com/t/{tenant}/mcp
    secrets: [zitadel_admin_pat]
    volumes:
      - ./data/bootstrap:/bootstrap
      - ./scripts/zitadel-bootstrap:/work:ro
    restart: "no"
    depends_on:
      zitadel-api:
        condition: service_healthy
```

### Bootstrap output → Limen config

The script creates the Zitadel control plane and exits with a summary of resource IDs. The operator captures the following values and places them into `secrets/` for the main services:

| Bootstrap output | Limen secret | Used by |
|-----------------|--------------|---------|
| `PROJECT_ID` | `secrets/limen_zitadel_project_id` (or config file) | `cfg.Zitadel.ProjectID` — validation at boot |
| `PORTAL_CLIENT_ID` | `secrets/limen_oidc_portal_client_id` | `limen-portal` OIDC RP flow |
| `STAFF_ZITADEL_ORG_ID` | `secrets/limen_staff_zitadel_org_id` | `limenctl migrate` — seeds the `_staff` tenant row |
| `MCP_RS_CLIENT_ID` (optional) | `secrets/limen_mcp_rs_client_id` | MCP token audience validation |

The `limenctl-migrate` service depends on `zitadel-bootstrap` completing successfully, which in turn depends on `zitadel-api` being healthy:

```
postgres ──healthy──▶ zitadel-api ──healthy──▶ zitadel-bootstrap ──completed──▶ limenctl-migrate ──completed──▶ limen-gateway / limen-portal / limen-staff
```

### Why not Terraform yet

The AGENTS.md references Terraform as the production path for Zitadel provisioning, but no Terraform code currently exists. This is tracked as a future deliverable (Phase 11+). For the initial Phase 11 deliverable, the compose one-shot is sufficient for the single-VM reference deployment.

### Alternative: `limenctl bootstrap` (future)

A `limenctl bootstrap` subcommand would be the production-grade path — it would use the same `internal/zitadel` client wrapper that Limen binaries already use, support both `pat` and `jwt_key` auth modes, and integrate cleanly with the config system. The existing standalone binary works for the current deliverable; `limenctl bootstrap` is tracked as a Phase 11 enhancement.

## All-in-one alternative

For small self-hosted deployments that do not need credential isolation or independent scaling, Limen provides the `limen` all-in-one binary as a single-container alternative. This is available via the `allinone` compose profile:

```yaml
  limen:
    image: ghcr.io/belphemur/limen:${LIMEN_VERSION}
    profiles: ["allinone"]
    restart: unless-stopped
    command: ["limen", "serve", "--config", "/etc/limen/config.yaml"]
    environment:
      LIMEN_CONFIG: /etc/limen/config.yaml
      # ... all secrets (gateway + portal + staff combined)
    # ... same depends_on, healthcheck, volumes shape as above
```

In this mode, a single `limen` process handles all routes (MCP, Portal, Admin, Staff, Signup, healthz). All secrets are present in one container. The trade-off is lower operational complexity in exchange for a wider blast radius — if the all-in-one process is compromised, the attacker has access to every credential.

Operators using the all-in-one profile should update their Caddyfile to point `@mcp`, `@portalapi`, and the fallback `reverse_proxy` at `limen:8080` instead of the split binaries.

## Observability

- Each binary logs to stdout in JSON format. Compose default logging driver suffices for single-host; production should forward to a central sink (Loki, ELK, CloudWatch).
- Zitadel logs similarly to stdout.
- Postgres slow-query logging enabled in `postgresql.conf` overrides shipped via `deploy/postgres/postgresql.conf`.
- `/healthz` and `/readyz` endpoints (Phase 10) are wired into compose healthchecks on each binary.
- Logs are tagged with the binary name via the container name in compose (`limen-gateway`, `limen-portal`, `limen-staff`), making log filtering straightforward.
- **Gateway idle timeout should be long** to support SSE streaming on the MCP hot path. Portal and staff can use shorter idle timeouts since they handle request-response traffic.

## Backup & restore

- Daily `pg_dump` per database via the `backup` and `backup-zitadel` services.
- Backup volumes (`./backups/limen`, `./backups/zitadel`) are bind-mounted to a directory that the operator snapshots out-of-band (rsync to object storage, etc.).
- Database data volumes (`./data/postgres-limen` and `./data/postgres-zitadel`) are bind-mounted to the host and must be backed up alongside the `pg_dump` exports.
- Restore procedure documented in `docs/runbook.md`:
  1. Stop `limen-gateway`, `limen-portal`, and `limen-staff` (Zitadel continues running).
  2. `dropdb && createdb` from a fresh backup file.
  3. Run `limenctl-migrate` (or `docker compose run limenctl-migrate`) to bring schema to current version.
  4. Start `limen-gateway`, `limen-portal`, and `limen-staff`.
- For the all-in-one profile: stop `limen`, restore, run `limenctl migrate`, then start `limen`.
- Encrypted columns survive backup/restore as opaque bytes — the encryption key must move with the data, or the rows are useless.

## Deliverables

- `compose.prod.yaml`
- `deploy/caddy/Caddyfile`
- `deploy/postgres/limen-init.sql`
- `deploy/postgres/postgresql.conf` (optional but recommended for tuning)
- `deploy/limen/config.yaml` — production config with `${VAR}` placeholders resolved via env
- `deploy/limen/valkey.conf` — Valkey config (optional, defaults from image)
- `build/docker/gateway.Dockerfile`, `build/docker/portal.Dockerfile`, `build/docker/staff.Dockerfile`, `build/docker/limenctl.Dockerfile`, `build/docker/limen.Dockerfile`
- `secrets/` directory layout + gitignore
- `scripts/zitadel-bootstrap/` — reused for production first-time provisioning (or a containerized version)
- `docs/runbook.md` updates: deployment, rotation, backup/restore, on-call (Phase 10 starts this; Phase 11 fills it in).

## Verification

- `docker compose -f compose.prod.yaml up -d` brings the stack to healthy with a real TLS cert (Caddy auto-issues via Let's Encrypt when DNS resolves).
- Each binary independently healthy via compose healthchecks (`docker compose ps` shows all `healthy`).
- `docker compose exec valkey valkey-cli PING` returns `PONG`.
- Gateway and portal can reach Valkey — check logs for "connected to valkey" or similar after startup.
- `curl https://auth.limen.example.com/.well-known/openid-configuration` returns Zitadel metadata.
- `curl https://limen.example.com/healthz` returns 200 (served by `limen-portal`).
- Gateway returns **404 on portal routes** (proof of credential isolation): `curl https://limen.example.com/t/{tenant}/api/...` routed to gateway should 404.
- Portal returns **404 on MCP routes** (proof of route isolation): `curl https://limen.example.com/t/{tenant}/mcp/.well-known/oauth-protected-resource` routed to portal should 404.
- `curl https://limen.example.com/t/{tenant}/mcp/.well-known/oauth-protected-resource` → gateway serves PRM metadata (200).
- VS Code MCP client configured against `https://limen.example.com/t/{tenant}/mcp` walks the discovery chain (PRM → AS metadata → token → success) end-to-end.
- Stopping `postgres` and starting it again recovers — Limen binaries wait via the healthcheck-driven `depends_on` gates on `limenctl-migrate`.
- A backup file taken yesterday can be restored on a fresh stack and the portal works end-to-end.
- Zitadel bootstrap completes successfully and outputs expected resource IDs (`PROJECT_ID`, `PORTAL_CLIENT_ID`, `STAFF_ZITADEL_ORG_ID`).

## Risks

- **Single-host SPOF**: this compose is a single-VM reference. HA needs a multi-host orchestrator. Document that fact loudly in the README of the deploy folder.
- **Cert renewal**: Caddy handles it but requires port 80 reachable for HTTP-01. Document alternatives (DNS-01 with provider modules) for restrictive environments.
- **Zitadel upgrades**: pin to a tag; major upgrades may require their own migration step — link to Zitadel's release notes in the runbook.
- **Two Postgres** doubles ops surface; some teams will prefer a single instance with two databases. The compose can be adapted, but separate instances keep crash blast radius small.
- **Route drift between dev and prod Caddyfiles**: the `@mcp`/`@portalapi` split must mirror the dev Caddyfile matchers. CI should diff them as part of the PR check.
- **Secret sprawl**: six secrets across four binaries means more files to manage than a single-container deploy. Automation (e.g., SOPS, Vault) is recommended for teams running this at scale.
- **Bootstrap PAT rotation**: `zitadel_admin_pat` is powerful — it can create and modify any Zitadel resource. After first-time provisioning, consider rotating or revoking it.
- **Bind mount persistence**: all persistent state uses bind mounts under `./data/` — operators must ensure this host directory is backed up regularly and is on a resilient filesystem. If the Docker daemon gets corrupted, bind-mounted data survives and can be recovered with standard filesystem tools (rsync, tar, borg).

## Checklist

- [ ] `compose.prod.yaml` defines `caddy`, `postgres`, `postgres-zitadel`, `valkey`, `limenctl-migrate`, `zitadel-bootstrap`, `limen-gateway`, `limen-portal`, `limen-staff`, `zitadel-api`, `zitadel-login`, `backup`, `backup-zitadel`
- [ ] All images pinned to specific versions (no `latest`)
- [ ] Postgres images are `postgres:18-alpine`
- [ ] All secrets sourced from `docker secret` files (never inline env values)
- [ ] `zitadel-bootstrap` one-shot compose service provisions the gateway org, project, apps, roles, staff org, and project grants
- [ ] Bootstrap output (`PROJECT_ID`, `PORTAL_CLIENT_ID`, `STAFF_ZITADEL_ORG_ID`) captured and stored in `secrets/`
- [ ] Bootstrap service depends on Zitadel being healthy; migrate service runs after bootstrap
- [ ] `limenctl-migrate` runs as a one-shot, gates `limen-gateway` + `limen-portal` + `limen-staff` via `condition: service_completed_successfully`
- [ ] `limenctl-migrate` ensures the `_staff` tenant row exists (Phase 12) and refuses to start in prod without `LIMEN_STAFF_ZITADEL_ORG_ID`
- [ ] Healthchecks on every long-running service; `restart: unless-stopped`
- [ ] Caddy routes `/t/*/mcp*` to `limen-gateway`, all other API routes to `limen-portal`
- [ ] Gateway has minimal secrets (no Zitadel admin cred, no portal signer key)
- [ ] Portal has full secrets (Zitadel PAT, OIDC client, cipher key, signer key, SMTP)
- [ ] Staff on private network (`staff-private`, `internal: true`), no public ingress
- [ ] Caddyfile configured for both `limen.example.com` and `auth.limen.example.com` with HSTS
- [ ] Dev Caddyfile (`deploy/caddy/Caddyfile.dev`) kept in lockstep with prod Caddyfile matchers
- [ ] `deploy/postgres/limen-init.sql` provisions `limen_admin` and `limen_app` roles with passwords from secrets
- [ ] All persistent state uses bind mounts under `./data/`, no named Docker volumes
- [ ] Valkey service configured, gateway and portal have `LIMEN_VALKEY_ADDR` and `LIMEN_VALKEY_PASSWORD_FILE`
- [ ] `./data/` directory created by deploy script with correct permissions for the postgres and valkey users
- [ ] Daily backup services configured with retention policy
- [ ] Per-binary Dockerfiles under `build/docker/` for gateway, portal, staff, limenctl, all-in-one
- [ ] CI matrix builds all five images on every PR, pushes on tag
- [ ] `docs/runbook.md` covers: first deploy, upgrade, rotate encryption key, backup, restore, incident response
- [ ] `.gitignore` blocks `secrets/` and `backups/`
- [ ] CI smoke job stands the compose up against ephemeral DNS + Caddy in internal-only mode and runs an end-to-end OIDC probe

# Implementation Phases

This folder breaks the multi-tenant MCP gateway work into twelve phases (0 through 11). Each phase has its own file with detailed design notes, file-level deliverables, verification steps, and a per-phase checklist. This README is the **global index + checklist** — use it as the source of truth for overall progress.

## TL;DR of the work

Limen becomes a multi-tenant B2B MCP gateway:

- **Identity is delegated to [Zitadel](https://zitadel.com/)**. Limen is an OIDC Relying Party for portal users and an MCP Resource Server for `/t/{slug}/mcp`. Tenant ↔ Zitadel **organization** (1:1). One Zitadel project, shared across orgs, hosts the Portal SPA app and the MCP RS app. MCP clients DCR via Limen's `/register` proxy onto Zitadel's Management API.
- **Authorization roles are Zitadel project roles**, not a Limen DB column. The shared project defines `owner`, `admin`, `member`; per-tenant grants live as Zitadel user grants in each tenant's org and arrive as the `urn:zitadel:iam:org:project:roles` claim. Portal admin RPCs mutate roles via `UserService.{AddUserGrant, UpdateUserGrant, RemoveUserGrant}` — Zitadel stays the single source of truth for both authn and authz.
- **Storage is PostgreSQL 18.2 with mandatory row-level security** — the only supported database. Runtime connects as `limen_app` (no `BYPASSRLS`); migrations + the cross-tenant refresher use `limen_admin`. Tenant-scoped tables have `FORCE ROW LEVEL SECURITY`.
- **Outbound** — Limen is an MCP client of upstream servers. Two strategies in v1: **`mcp_spec`** (auto-discovery + DCR + PKCE for OAuth-protected upstreams like Atlassian Rovo) and **`none`** (unauthenticated upstreams, for self-hosted dev / trusted-network internal MCPs). The strategy interface is designed so future modes (`static_header`, `oauth2_app`, `api_token`, `mtls`) plug in without re-architecting.
- **Tenancy** is path-prefix (`/t/{slug}/...`); portal cookies are path-scoped.
- **Frontend** is a Vue 3 SPA over Connect-RPC, embedded via `embed.FS`. Login is a redirect into Zitadel's hosted UI — Limen never sees a password.
- **Deployment** is reproducible via Docker Compose: a dev stack ([Phase 0](phase-00-dev-environment.md)) and a production stack with TLS, secrets, and backups ([Phase 11](phase-11-production-deployment.md)).

## Phase index & dependencies

| #   | Phase                                                                               | Depends on | Status |
| --- | ----------------------------------------------------------------------------------- | ---------- | ------ |
| 0   | [Development environment (Docker Compose)](phase-00-dev-environment.md)             | —          | ✅     |
| 1   | [Database foundation](phase-01-database-foundation.md)                              | —          | ✅     |
| 2   | [Crypto + config](phase-02-crypto-config.md)                                        | —          | ☐      |
| 3   | [Postgres Row-Level Security](phase-03-postgres-rls.md)                             | 1          | ☐      |
| 4   | [Tenant resolution, OIDC login, portal session](phase-04-tenant-auth-session.md)    | 0, 1, 2, 3 | ☐      |
| 5   | [Zitadel integration (AS delegation + DCR proxy)](phase-05-authorization-server.md) | 4          | ☐      |
| 6   | [Limen as MCP Resource Server](phase-06-resource-server.md)                         | 5          | ☐      |
| 7   | [Outbound upstream linking (strategies)](phase-07-outbound-upstream.md)             | 4          | ☐      |
| 8   | [Per-tenant, per-user upstream injection](phase-08-per-tenant-injection.md)         | 6, 7       | ☐      |
| 9   | [Portal backend (Connect-RPC) + Vue 3 SPA](phase-09-portal-spa.md)                  | 4, 7       | ☐      |
| 10  | [Wiring, verification, hardening](phase-10-wiring-hardening.md)                     | 0–9        | ☐      |
| 11  | [Production deployment (Docker Compose)](phase-11-production-deployment.md)         | 0–10       | ☐      |

Phases 1 + 2 can be done in parallel; Phase 0 is independent and should be stood up first since every other phase verifies against it. Phase 7 can run in parallel with 5 + 6 once Phase 4 lands. Phase 9 unblocks once 4 + 7 are done.

## Global checklist

Mirror of the per-phase checklists. Tick a box here only when the corresponding phase file is fully checked.

### Phase 0 — Development environment

- [x] `compose.dev.yaml` defines `postgres`, `postgres-zitadel`, `zitadel`, `zitadel-bootstrap`, `mailhog` with healthchecks
- [x] Postgres images pinned to `postgres:18.2-alpine`; Zitadel image pinned to a specific tag
- [x] Named volumes for both Postgres instances; data persists across `up`/`down`
- [x] `scripts/zitadel-bootstrap/` idempotently ensures Limen project + Portal app + MCP RS app + sample org
- [x] `.env.example` documents every Limen env var the dev workflow needs
- [x] `make dev` / `make dev-reset` targets bring the stack up and tear it down cleanly
- [x] `docs/development.md` explains the first-run flow (MailHog UI included)
- [x] CI smoke job runs `docker compose up` + a basic OIDC discovery probe against Zitadel

### Phase 1 — Database foundation

- [x] `gorm.io/gorm`, `gorm.io/driver/postgres`, `github.com/oklog/ulid/v2` added to `go.mod`
- [x] `internal/ids/` exports `New(prefix)` / `Parse` / `MustParse` with prefix constants (`tnt_`, `usr_`, `ups_`, …)
- [x] `internal/storage/storage.go` (`Open(cfg)` opens Postgres pool with sane defaults)
- [x] `internal/storage/models.go` — `Base` struct (`ID`, `PublicID`, `CreatedAt`, `UpdatedAt`, `DeletedAt`) embedded in every model; `Tenant` (with `ZitadelOrgID`), `User` (with `ZitadelSubject`, no password), `Upstream`, `UpstreamStrategyConfig`, `UpstreamRegistration`, `UpstreamLink`, `ZitadelApp`. Composite uniques are partial (`WHERE deleted_at IS NULL`). Invitations and portal sessions live in Zitadel (no Limen tables).
- [x] `internal/storage/migrate.go` (`AutoMigrate` for portable parts)
- [x] `internal/storage/tenant.go` (`WithTenant`, `TenantFromCtx`, `Session(ctx)`, `WithSuperuser(ctx)`)
- [x] Integration CRUD tests against a `postgres:18.2-alpine` testcontainer

### Phase 2 — Crypto + config

- [ ] `internal/crypto/aesgcm.go` (AES-256-GCM with AAD)
- [ ] `${ENV}` and `${ENV:-default}` substitution implemented in `internal/config/config.go`
- [ ] New config sections: `database`, `security`, `oidc`, `oauth_proxy`
- [ ] Existing `upstreams` and `auth` config blocks removed
- [ ] Unit tests: roundtrip, tamper detection, AAD mismatch detection, env-substitution variants

### Phase 3 — Postgres Row-Level Security

- [ ] Migration script enables `ROW LEVEL SECURITY` + `FORCE` on all tenant-scoped tables
- [ ] `CREATE POLICY tenant_isolation` with `USING` and `WITH CHECK` on every tenant-scoped table
- [ ] `migrations/postgres/0002_audit_triggers.sql` installs the `set_updated_at()` BEFORE-UPDATE trigger on every Limen-owned table (including `Tenant`)
- [ ] `limen_app` runtime role + `limen_admin` `BYPASSRLS` role created and granted appropriately
- [ ] `internal/storage/rls.go` wires `SET LOCAL app.current_tenant` into every `Session(ctx)` (Postgres path)
- [ ] Tests against `postgres:18.2-alpine` prove cross-tenant `SELECT`/`INSERT` are blocked
- [ ] Test proves `db.Unscoped().Find(...)` inside a tenant session returns 0 rows

### Phase 4 — Tenant resolution, OIDC login, portal session

- [ ] `internal/tenancy/resolver.go` (URL param → tenant → ctx); reserved-slug list enforced (includes `auth`)
- [ ] `internal/auth/oidc.go` (Zitadel as OIDC provider; `rp.RelyingParty` built from config)
- [ ] `/auth/login`, `/auth/callback`, `/auth/logout` routes implemented
- [ ] HMAC-signed `state` carrying `(nonce, slug, return_to, expires_at)`
- [ ] Token validation: signature, `iss`, `aud`, `exp`, `nonce`, plus `urn:zitadel:iam:user:resourceowner:id` == `tenant.zitadel_org_id`
- [ ] `User` upserted by `(tenant_id, zitadel_subject)` on callback
- [ ] `internal/auth/session.go` issues opaque session tokens; cookie has `Path=/t/<slug>; HttpOnly; Secure; SameSite=Lax`
- [ ] `internal/auth/session.go` issues a portal cookie that wraps a Zitadel `SessionService` session (`sessionId` + `sessionToken` encrypted with AAD); cookie has `Path=/t/<slug>; HttpOnly; Secure; SameSite=Lax`
- [ ] Logout calls `SessionService.DeleteSession` then redirects to Zitadel's end-session URL after clearing the cookie
- [ ] `RequirePortalSession` validates via `SessionService.GetSession` with a 60 s positive cache
- [ ] `RequireRole(...)` middlewares (read roles from the `urn:zitadel:iam:org:project:roles` session claim, not from the DB)
- [ ] CLI: `-create-tenant` (creates Zitadel org + owner user + `AddUserGrant(owner)` + Limen rows), `-invite-user` (calls Zitadel `AddUserGrant(<role>)` + `UserService.CreateInviteCode`)
- [ ] Unit + HTTP-level tests for slug validation, state signing, full OIDC flow against a stub issuer

### Phase 5 — Zitadel integration (AS delegation + DCR proxy)

- [ ] `internal/oauthproxy/metadata.go` serves AS metadata with `registration_endpoint` rewritten to Limen
- [ ] `internal/oauthproxy/redirector.go` issues 302/307 redirects to Zitadel for `authorize`, `token`, `userinfo`, `jwks`, `revoke`, `introspect`, `end_session`
- [ ] `internal/oauthproxy/dcr.go` accepts MCP-spec DCR requests and creates Zitadel OIDC apps via the Management API
- [ ] DCR proxy enforces `tenant.DCREnabled` and optional `dcr_initial_access_token`; rate-limited per tenant
- [ ] PKCE S256 required on every DCR'd app; `redirect_uris` validated
- [ ] `ZitadelApp` mirror row persisted; `registration_access_token` encrypted with AAD
- [ ] RFC 7592 management endpoints implemented (`GET/PUT/DELETE /register/{client_id}`)
- [ ] `internal/oauthproxy/management.go` wraps the Zitadel Management API (PAT-based)
- [ ] Routes mounted under `/t/{tenant}/oauth/*` behind `RequireTenant`
- [ ] Integration tests: full discovery + DCR + authorize + token + /mcp roundtrip against the dev Zitadel
- [ ] Contract test: Zitadel app-create field mapping

### Phase 6 — Limen as MCP Resource Server

- [ ] `internal/mcprs/metadata.go` exposes PRM at `/t/{tenant}/mcp/.well-known/oauth-protected-resource`
- [ ] `internal/mcprs/challenge.go` constructs `WWW-Authenticate` with `resource_metadata` on every 401
- [ ] `internal/auth/middleware.go` validates Zitadel-issued JWTs
- [ ] `iss` checked against the configured Zitadel issuer
- [ ] `aud` checked against the configured MCP RS audience
- [ ] `urn:zitadel:iam:user:resourceowner:id` claim matched against `tenant.zitadel_org_id`
- [ ] Algorithm allowlist (`RS256`); `kid`-based key selection via cached `JWKSResolver`
- [ ] `*User` upserted/loaded by `(tenant_id, zitadel_subject)` and placed in ctx
- [ ] `Mcp-Session-Id` explicitly **not** used for identity
- [ ] Integration test: full discovery chain end-to-end
- [ ] Integration test: cross-tenant rejection via `org_id` mismatch
- [ ] Unit tests for each failure mode

### Phase 7 — Outbound upstream linking

- [ ] `internal/upstream/strategy.go` defines `Strategy` interface (including `RequiresLink`) and `Registry`
- [ ] `internal/upstream/mcpspec/{discovery,registrar,link,headers}.go` implement the OAuth-via-PRM strategy
- [ ] `internal/upstream/none/none.go` returns empty headers; `Provision` rejects upstreams that advertise PRM
- [ ] `internal/upstream/handlers.go` exposes connect/callback/disconnect under `/t/{tenant}/upstream/{name}/*`
- [ ] `internal/upstream/refresher.go` runs under `WithSuperuser(ctx)` with audit comment
- [ ] Tokens stored encrypted with AAD `tenant|user|"upstream.<kind>_token"`
- [ ] State signed with HMAC, one-shot consumption
- [ ] DCR responses persisted in `UpstreamRegistration` (RFC 7592-capable)
- [ ] Unit + integration tests for state signing, discovery, registration, refresh, `none.Provision` rejection

### Phase 8 — Per-tenant, per-user upstream injection

- [ ] `internal/upstream/authprovider.go` defines `AuthProvider` and `DBAuthProvider`
- [ ] `internal/gateway/upstream.go` uses `http.RoundTripper` that reads ctx and calls `AuthProvider.Headers`
- [ ] `UpstreamManager` indexed by tenant ID at startup
- [ ] `Gateway.ToolsForUser(ctx)` filters by `strategy.RequiresLink()` ∨ user-has-link
- [ ] `Gateway.CallTool(ctx, ...)` routes through the per-request transport
- [ ] Tool names prefixed by upstream to avoid collisions
- [ ] Missing-link surfaces as a structured MCP error
- [ ] `internal/gateway/codemode.go` exposes only user-scoped tools to the Goja sandbox
- [ ] MCP routes mounted only under `/t/{tenant}/mcp` behind `RequireMCPAuth`
- [ ] MCP server session state keyed by `(tenant_id, user_id, mcp_session_id)`
- [ ] Unit + integration tests for multi-tenant isolation and link-required visibility

### Phase 9 — Portal backend + Vue 3 SPA

- [ ] `proto/limen/portal/v1/portal.proto` with full `PortalService` definition (no `ChangePassword`; Zitadel owns passwords)
- [ ] `buf.yaml` + `buf.gen.yaml`; Go + TS codegen
- [ ] `internal/portal/` implements all RPCs against `storage.Session(ctx)`
- [ ] Interceptors: tenancy + portal-session + role; no `tenant_id` in any request message
- [ ] Vue 3 + Vite + Pinia + Vue Router + `@connectrpc/connect-web` scaffolded under `web/`
- [ ] Pages: Login (redirects to `/auth/login`), Dashboard, Upstreams, Members, MCP Clients, Settings
- [ ] SPA base path resolved at boot from `/t/<slug>/portal/`
- [ ] No password input in the SPA; logout goes through `/auth/logout`
- [ ] Built SPA embedded via `//go:embed web/dist/*` with SPA fallback route
- [ ] CSP header set on portal HTML responses
- [ ] `AGENTS.md` build section updated with `buf generate` and `pnpm build`
- [ ] Unit tests for the role-enforcement interceptor

### Phase 10 — Wiring, verification, hardening

- [ ] `cmd/gateway/main.go` wires DB → migrate → crypto → Zitadel client → OIDC RP → upstream registry → refresher → gateway → oauthproxy → middleware → portal → transport
- [ ] CLI subcommands: `-create-tenant`, `-invite-user`, `-create-upstream`, `-migrate`
- [ ] `config.yaml` updated with all new sections (`oidc`, `oauth_proxy`)
- [ ] `AGENTS.md` updated (architecture, build, setup, testing, security)
- [ ] `docs/runbook.md` drafted (Zitadel bootstrap, role provisioning, encryption key rotation, backup/restore)
- [ ] HTTP timeouts; graceful shutdown; healthcheck endpoint; structured logging
- [ ] `internal/audit/` skeleton with sensitive-event emitter
- [ ] Integration test matrix (12 scenarios) implemented and green
- [ ] Manual smoke against the real Atlassian Rovo MCP server
- [ ] `go vet ./...`, `pnpm typecheck`, `pnpm lint` all clean

### Phase 11 — Production deployment

- [ ] `compose.prod.yaml` defines `caddy`, `postgres`, `postgres-zitadel`, `limen-migrate`, `limen`, `zitadel`, `backup`, `backup-zitadel`
- [ ] All images pinned to specific versions; Postgres at `postgres:18.2-alpine`
- [ ] All secrets sourced from `docker secret` files
- [ ] `limen-migrate` one-shot gates `limen` via `condition: service_completed_successfully`
- [ ] Healthchecks on every long-running service; `restart: unless-stopped`
- [ ] Caddyfile configured for `limen.example.com` and `auth.limen.example.com` with HSTS
- [ ] `deploy/postgres/limen-init.sql` provisions `limen_admin` and `limen_app` roles
- [ ] Volumes named explicitly; backups mounted to a known host path
- [ ] Daily backup services with retention policy
- [ ] `docs/runbook.md` covers: first deploy, upgrade, rotate encryption key, backup, restore
- [ ] `.gitignore` blocks `secrets/` and `backups/`

## Cross-cutting decisions

- **Tenancy**: path prefix `/t/{slug}/...`; cookies path-scoped.
- **IDs**: every entity has an internal `int64` PK (used for FKs and joins) plus a public ULID with a Stripe-style type prefix (`tnt_`, `usr_`, `ups_`, `usc_`, `ureg_`, `ulnk_`, `zapp_`). Only the prefixed ULID appears in APIs, URLs, the SPA, and operator logs. ULIDs are time-sortable at millisecond resolution (and monotonic within a process), so cursor pagination uses `WHERE public_id > $cursor`.
- **Audit columns**: every table embeds `CreatedAt`, `UpdatedAt`, `DeletedAt` (`timestamptz`). `UpdatedAt` is maintained by a Postgres `BEFORE UPDATE` trigger; `DeletedAt` uses GORM's `gorm.DeletedAt` so soft-deleted rows are invisible by default and require `Unscoped()` to see. Composite uniques are partial (`WHERE deleted_at IS NULL`) so soft-deletes don't block re-creation.
- **Identity**: Zitadel is the OIDC provider. Tenant ↔ Zitadel organization (1:1). No local passwords in Limen.
- **DB**: GORM on PostgreSQL 18.2 with mandatory RLS + FORCE ROW LEVEL SECURITY + non-superuser app role. Dev and prod both run Postgres via Docker Compose.
- **Membership**: 1 user ↔ 1 tenant; same email in different tenants = different Zitadel accounts.
- **Roles**: `owner` / `admin` / `member` (stored in Limen, not Zitadel).
- **Token validation**: in-process JWT against Zitadel's JWKS (single issuer, single cache entry); cross-tenant defense via `org_id` claim match.
- **OIDC library**: `github.com/zitadel/oidc/v3` (RP side).
- **Crypto**: AES-256-GCM, AAD binds `tenant|user|kind`.
- **Outbound transport**: custom `http.RoundTripper` for per-request bearer injection (tenant + user read from ctx).
- **Deployment**: Docker Compose for both dev and prod, single declarative source of truth.

## Explicitly out of scope this iteration

SAML / SCIM (use Zitadel's roadmap for these); MFA enforcement policy (configured in Zitadel directly); audit logging beyond structured-log events; per-tenant rate limits at the application layer; billing/usage metering; fine-grained per-tool scopes; outbound strategies beyond `mcp_spec` and `none`; HA Kubernetes manifests (Phase 11 ships the single-VM reference compose).

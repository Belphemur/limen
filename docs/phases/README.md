# Implementation Phases

This folder breaks the multi-tenant MCP gateway work into fourteen phases (0 through 13). Each phase has its own file with detailed design notes, file-level deliverables, verification steps, and a per-phase checklist. This README is the **global index + checklist** — use it as the source of truth for overall progress.

## TL;DR of the work

Limen becomes a multi-tenant B2B MCP gateway:

- **Identity is delegated to [Zitadel](https://zitadel.com/)**. Limen is an OIDC Relying Party for portal users and an MCP Resource Server for `/t/{tenant}/mcp`. Tenant ↔ Zitadel **organization** (1:1). One Zitadel project, shared across orgs, hosts the Portal SPA app and the MCP RS app. MCP clients DCR via Limen's `/register` proxy onto Zitadel's Management API.
- **Authorization roles are Zitadel project roles**, not a Limen DB column. The shared project defines `owner`, `admin`, `member`; per-tenant grants live as Zitadel user grants in each tenant's org and arrive as the `urn:zitadel:iam:org:project:roles` claim. Portal admin RPCs mutate roles via `UserService.{AddUserGrant, UpdateUserGrant, RemoveUserGrant}` — Zitadel stays the single source of truth for both authn and authz.
- **Storage is PostgreSQL 18.2 with mandatory row-level security** — the only supported database. Runtime connects as `limen_app` (no `BYPASSRLS`); migrations + the cross-tenant refresher use `limen_admin`. Tenant-scoped tables have `FORCE ROW LEVEL SECURITY`.
- **Outbound** — Limen is an MCP client of upstream servers. Three strategies in v1: **`mcp_spec`** (auto-discovery + DCR + PKCE for OAuth-protected upstreams like Atlassian Rovo), **`static_header`** (admin-configured HTTP header auth — secret can be tenant-wide or per-user; users paste their own API key in the portal when the upstream is in user mode), and **`none`** (unauthenticated upstreams, for self-hosted dev / trusted-network internal MCPs). The strategy interface is designed so future modes (`oauth2_app`, `api_token`, `mtls`) plug in without re-architecting. Users see only the tools belonging to upstreams they've authenticated for (or that don't require per-user auth), and can enable/disable any link from the portal without losing the stored credentials.
- **Tenancy** is path-prefix (`/t/{tenant}/...`); portal cookies are path-scoped.
- **Frontend** is a Vue 3 SPA over Connect-RPC, shipped as plain static assets and served by the reverse proxy (Caddy `file_server`) or Cloudflare Pages — **not** embedded in the Go binary. Limen serves only JSON / OAuth / MCP endpoints. Same-origin deployment keeps cookie path-scoping (`Path=/t/<tenant>`) working unchanged. Login is a redirect into Zitadel's hosted UI — Limen never sees a password.
- **Deployment** is reproducible via Docker Compose: a dev stack ([Phase 0](phase-00-dev-environment.md)) and a production stack with TLS, secrets, and backups ([Phase 11](phase-11-production-deployment.md)).
- **SaaS operator surface** — a reserved **staff tenant** at `/t/_staff/` with a `super_admin` role, a backoffice SPA for cross-tenant visibility, and audited impersonation via Zitadel ([Phase 12](phase-12-staff-backoffice.md)).
- **Billing** — per-seat subscriptions via Stripe Billing ([Phase 13](phase-13-billing-stripe.md)). One Stripe Customer per customer tenant, quantity tracks Zitadel user grants, hosted Checkout + Customer Portal handle PCI scope. Usage-based pricing is designed-for but explicitly out of scope for v1. The staff tenant is never billed; self-hosters can disable Stripe entirely via `billing.enabled: false`.

## Phase index & dependencies

| #   | Phase                                                                                  | Depends on         | Status |
| --- | -------------------------------------------------------------------------------------- | ------------------ | ------ |
| 0   | [Development environment (Docker Compose)](phase-00-dev-environment.md)                | —                  | ✅     |
| 1   | [Database foundation](phase-01-database-foundation.md)                                 | —                  | ✅     |
| 2   | [Crypto + config](phase-02-crypto-config.md)                                           | —                  | ✅     |
| 3   | [Postgres Row-Level Security](phase-03-postgres-rls.md)                                | 1                  | ✅     |
| 4   | [Tenant resolution, OIDC login, portal session](phase-04-tenant-auth-session.md)       | 0, 1, 2, 3         | ☐      |
| 5   | [Zitadel integration (AS delegation + DCR proxy)](phase-05-authorization-server.md)    | 4                  | ☐      |
| 6   | [Limen as MCP Resource Server](phase-06-resource-server.md)                            | 5                  | ✅     |
| 7   | [Outbound upstream linking (strategies)](phase-07-outbound-upstream.md)                | 4                  | ✅     |
| 8   | [Per-tenant, per-user upstream injection](phase-08-per-tenant-injection.md)            | 6, 7               | ☐      |
| 9   | [Portal backend (Connect-RPC) + Vue 3 SPA](phase-09-portal-spa.md)                     | 4, 7               | ☐      |
| 9b  | [Tenant administrative portal + self-serve signup](phase-09b-tenant-admin-spa.md)      | 4, 7, 9            | ☐      |
| 10  | [Wiring, verification, hardening](phase-10-wiring-hardening.md)                        | 0–9                | ☐      |
| 11  | [Production deployment (Docker Compose)](phase-11-production-deployment.md)            | 0–10               | ☐      |
| 12  | [Staff tenant & backoffice (super-admin, impersonation)](phase-12-staff-backoffice.md) | 0, 3, 4, 9, 10, 11 | ☐      |
| 13  | [Billing with Stripe (per-seat)](phase-13-billing-stripe.md)                           | 4, 9, 10, 11, 12   | ☐      |

Phases 1 + 2 can be done in parallel; Phase 0 is independent and should be stood up first since every other phase verifies against it. Phase 7 can run in parallel with 5 + 6 once Phase 4 lands. Phase 9 unblocks once 4 + 7 are done. Phase 12 (staff/backoffice) layers on top of everything and is the last phase before declaring the platform production-ready for paying customers — but its bootstrap step is wired into Phase 0 (Zitadel org) and Phase 11 (migrate ensure-row) so the staff tenant exists from day one. Phase 13 (billing) sits last and is opt-in: self-hosters can run the gateway indefinitely with `billing.enabled: false` and never touch Stripe.

## Global checklist

Mirror of the per-phase checklists. Tick a box here only when the corresponding phase file is fully checked.

### Phase 0 — Development environment

- [x] `compose.dev.yaml` defines `postgres`, `postgres-zitadel`, `zitadel`, `zitadel-bootstrap`, `mailhog` with healthchecks
- [x] Postgres images pinned to `postgres:18-alpine`; Zitadel image pinned to a specific tag
- [x] Named volumes for both Postgres instances; data persists across `up`/`down`
- [x] `scripts/zitadel-bootstrap/` idempotently ensures Limen project + Portal app + MCP RS app + sample org + `super_admin` role + `limen-staff` operator org + staff user (Phase 12 prerequisite); emits `LIMEN_STAFF_ZITADEL_ORG_ID`
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
- [x] Integration CRUD tests against a `postgres:18-alpine` testcontainer

### Phase 2 — Crypto + config

- [x] `internal/crypto/aessiv.go` (AES-SIV / RFC 5297 via `github.com/jedisct1/go-aes-siv`, with AAD)
- [x] `${ENV}` and `${ENV:-default}` substitution implemented in `internal/config/config.go`
- [x] New config sections: `database`, `security`, `oauth_server`
- [ ] Existing `upstreams` and `auth` config blocks removed
- [x] Unit tests: roundtrip, tamper detection, AAD mismatch detection, env-substitution variants

### Phase 3 — Postgres Row-Level Security

- [x] Migration script enables `ROW LEVEL SECURITY` + `FORCE` on all tenant-scoped tables
- [x] `CREATE POLICY tenant_isolation` with `USING` and `WITH CHECK` on every tenant-scoped table
- [x] `migrations/postgres/0002_audit_triggers.sql` installs the `set_updated_at()` BEFORE-UPDATE trigger on every Limen-owned table (including `Tenant`)
- [x] `limen_app` runtime role + `limen_admin` `BYPASSRLS` role created and granted appropriately
- [x] `internal/storage/rls.go` wires `SET LOCAL app.current_tenant` into every `Session(ctx)` (Postgres path)
- [x] Tests against `postgres:18-alpine` prove cross-tenant `SELECT`/`INSERT` are blocked
- [x] Test proves `db.Unscoped().Find(...)` inside a tenant session returns 0 rows

### Phase 4 — Tenant resolution, OIDC login, portal session

- [x] `internal/tenancy/resolver.go` (URL `{tenant}` segment → tenant → ctx); `PublicID` validated as `tnt_<ULID>`
- [x] `internal/auth/oidc.go` (Zitadel as OIDC provider; `rp.RelyingParty` built from config)
- [x] `/auth/login`, `/auth/callback`, `/auth/logout` routes mounted in `internal/transport`
- [x] HMAC-signed `state` carrying `(nonce, tenant, return_to, expires_at)`; tampering rejected
- [x] Token validation: signature, `iss`, `aud`, `exp`, `nonce`, plus `urn:zitadel:iam:user:resourceowner:id` == `tenant.zitadel_org_id`
- [x] `User` upserted by `(tenant_id, zitadel_subject)` on callback
- [x] Portal cookie is an AES-SIV-encrypted blob carrying `{idToken, refreshToken, expiresAt}` with AAD `{TenantID: <publicID>, Kind: "portal.oidc.tokens"}`; attributes `HttpOnly; Secure; SameSite=Lax; Path=/t/<tenant>` (no server-side session store, no `SessionService` round-trip)
- [x] `RequireSession` decrypts cookie, verifies via `rp.VerifyIDToken` against cached JWKS, transparently calls `rp.RefreshTokens` on `exp` failure; puts `*oidc.IDTokenClaims` on ctx
- [x] Logout clears the portal cookie and 302s to `rp.EndSession`'s URL with `id_token_hint`
- [x] `RequireRole(...)` reads roles from the `urn:zitadel:iam:org:project:roles` claim on the live ID token (no DB role column)
- [x] CLI: `limen create-tenant` (Zitadel org + owner user + `AddUserGrant(owner)` + Limen rows, prints Zitadel Console deep-link). Invites, role changes, member removal, password reset, MFA enrollment, and IdP federation are delegated to Zitadel Console — not Limen CLI subcommands (see Phase 4 _Self-service delegation_).
- [x] `cmd/gateway/main.go` is a Cobra root bootstrap; Viper binds `--config` + CLI flags to `LIMEN_*`
- [x] Unit tests for tenant id parsing and state signing
- [x] HTTP integration test for the full OIDC flow against a stub issuer (login → callback → cookie attributes → cross-tenant rejection → org mismatch)

### Phase 5 — Zitadel integration (AS delegation + DCR proxy)

- [x] Dead `OAuthServerConfig` removed; replaced by `OAuthProxyConfig` in `internal/config/config.go` and `config.yaml`
- [x] `internal/zitadel/apps.go` adds `AddOIDCApp` / `UpdateOIDCApp` / `DeleteOIDCApp` / `GetOIDCApp` on the existing shared client (no second wrapper)
- [ ] `internal/oauthproxy/metadata.go` serves AS metadata with `registration_endpoint` rewritten to Limen and `jwks_uri` pointing directly at Zitadel
- [ ] `internal/oauthproxy/redirector.go` issues 302 (GETs) / 307 (POSTs) redirects to Zitadel for `authorize`, `token`, `userinfo`, `revoke`, `introspect`, `end_session`
- [ ] `internal/oauthproxy/dcr.go` accepts MCP-spec DCR requests and creates Zitadel OIDC apps via the shared `*zitadel.Client`
- [ ] DCR proxy enforces `tenant.DCREnabled` and optional `dcr_initial_access_token`
- [ ] DCR proxy rejects unknown / unsupported metadata fields with `invalid_client_metadata` (default-deny)
- [ ] `internal/oauthproxy/ratelimit.go` enforces a per-tenant token bucket on `/register*` (`golang.org/x/time/rate`)
- [ ] PKCE S256 required on every DCR'd app; `redirect_uris` validated (HTTPS exact-match, RFC 8252 loopback, reverse-DNS custom schemes; wildcards / IDN / fragments rejected)
- [ ] `Tenant.DCRRedirectURIAllowlist` column added; when non-empty, every DCR `redirect_uri` must additionally match a tenant-admin-defined glob pattern (subtractive — floor still applies). Editor lives in the [Phase 9b](phase-09b-tenant-admin-spa.md) admin SPA Settings page
- [ ] Registration lifecycle documented as operator-driven for v1 (no auto-expiry reaper)
- [ ] `ZitadelApp` mirror row persisted; `registration_access_token_hash` column (SHA-256) added by migration and used for constant-time auth
- [ ] RFC 7592 management endpoints implemented (`GET/PUT/DELETE /register/{client_id}`)
- [ ] Routes mounted under `/t/{tenant}/oauth/*` behind `RequireTenant`
- [ ] Integration tests: full discovery + DCR + authorize + token + /mcp roundtrip against the dev Zitadel
- [ ] Contract test: Zitadel app-create field mapping

### Phase 6 — Limen as MCP Resource Server

- [x] `internal/mcprs/metadata.go` exposes PRM at `/t/{tenant}/mcp/.well-known/oauth-protected-resource`
- [x] `internal/mcprs/challenge.go` constructs `WWW-Authenticate` with `resource_metadata` on every 401/403
- [x] `internal/auth/middleware.go` validates Zitadel-issued JWTs (`MCPAuth` / `RequireMCPAuth`)
- [x] `iss` checked against the configured Zitadel issuer
- [x] `aud` checked against the configured MCP RS audience (`zitadel.mcp_resource_audience`)
- [x] `urn:zitadel:iam:user:resourceowner:id` claim matched against `tenant.zitadel_org_id` (→ 403 on mismatch)
- [x] Algorithm allowlist (`RS256`); `kid`-based key selection via cached `rp.RemoteKeySet`
- [x] `*User` loaded by `(tenant_id, zitadel_subject)` and placed in ctx (no auto-provision on RS path)
- [x] `Mcp-Session-Id` explicitly **not** used for identity
- [x] Integration test: discovery chain end-to-end (in-process; full Zitadel-backed pass deferred to Phase 10)
- [x] Integration test: cross-tenant rejection via `org_id` mismatch
- [x] Unit tests for each failure mode

### Phase 7 — Outbound upstream linking

- [ ] `internal/upstream/strategy.go` defines `Strategy` interface (including `RequiresLink`) and `Registry`
- [ ] `internal/upstream/mcpspec/{discovery,registrar,link,headers}.go` implement the OAuth-via-PRM strategy
- [ ] `internal/upstream/statichdr/{config,link,headers}.go` implement the static-header strategy (tenant-wide secret and per-user API key modes)
- [ ] `internal/upstream/none/none.go` returns empty headers; `Provision` rejects upstreams that advertise PRM
- [ ] `internal/upstream/handlers.go` exposes connect/callback/disconnect under `/t/{tenant}/upstream/{name}/*`
- [ ] `internal/upstream/refresher.go` runs under `WithSuperuser(ctx)` with audit comment; skips strategies whose `Maintain` is a no-op
- [ ] Tokens / API keys stored encrypted with AAD `tenant|user|"upstream.<kind>_token"` (or `tenant|""|"upstream.strategy_config"` for tenant-wide secrets)
- [ ] `UpstreamLink.Enabled` field added (default `true`); migration shipped
- [ ] `UpstreamLink.NeedsRelink` field added (default `false`); migration shipped
- [ ] `UpstreamLink` health columns added: `ConsecutiveFailures`, `FirstFailureAt`, `LastFailureAt`, `LastFailureReason`, `AutoDisabledAt`; auto-disable when ≥5 consecutive failures over ≥15 min, or `NeedsRelink=true` for ≥24 h; successful call/refresh resets the counter atomically
- [ ] `mcp_spec` refresh is centralized: one `refreshLocked` function called by `Headers` (proactive), the round-tripper (reactive on 401, single retry), and `Maintain` (background); single-flight + `SELECT FOR UPDATE SKIP LOCKED` collapse concurrent refreshes; refresh-token rotation is persisted; `invalid_grant` flips `NeedsRelink=true`
- [ ] State signed with HMAC, one-shot consumption via Valkey (`GETDEL` + TTL)
- [ ] DCR responses persisted in `UpstreamRegistration` (RFC 7592-capable)
- [ ] Unit + integration tests for state signing, discovery, registration, refresh, `none.Provision` rejection, `static_header` template rendering + mode dispatch, link enable/disable visibility

### Phase 8 — Per-tenant, per-user upstream injection

- [ ] `internal/upstream/authprovider.go` defines `AuthProvider` (`Headers` + `HeadersForceRefresh`) and `DBAuthProvider`
- [ ] `internal/gateway/upstream.go` uses `http.RoundTripper` that reads ctx and calls `AuthProvider.Headers`; retries once on upstream `401` via `HeadersForceRefresh`
- [ ] `UpstreamManager` indexed by tenant ID at startup
- [ ] `Gateway.ToolsForUser(ctx)` filters by `strategy.RequiresLink()==false` ∨ (user-has-link ∧ `link.Enabled` ∧ `link.AutoDisabledAt IS NULL`)
- [ ] `Gateway.CallTool(ctx, ...)` routes through the per-request transport; rejects disabled / auto-disabled links with the same structured error as missing links; transport updates the link's health columns after every call
- [ ] Tool names prefixed by upstream to avoid collisions
- [ ] Missing-link surfaces as a structured MCP error
- [ ] `internal/gateway/codemode.go` exposes only user-scoped tools to the Goja sandbox
- [ ] MCP routes mounted only under `/t/{tenant}/mcp` behind `RequireMCPAuth`
- [ ] MCP server session state keyed by `(tenant_id, user_id, mcp_session_id)`
- [ ] Unit + integration tests for multi-tenant isolation and link-required visibility

### Phase 9 — Portal backend + Vue 3 SPA

- [ ] `proto/limen/portal/v1/portal.proto` with full `PortalService` definition (no `ChangePassword`; Zitadel owns passwords); includes `SubmitUpstreamAPIKey` and `SetUpstreamLinkEnabled`
- [ ] `buf.yaml` + `buf.gen.yaml`; Go + TS codegen
- [ ] `internal/portal/` implements all RPCs against `storage.Session(ctx)`
- [ ] Interceptors: tenancy + portal-session + role; no `tenant_id` in any request message
- [ ] Vue 3 + Vite + Pinia + Vue Router + `@connectrpc/connect-web` scaffolded under `web/`
- [ ] Pages: Login (redirects to `/auth/login`), Dashboard, Upstreams, Members, MCP Clients, Settings
- [ ] SPA base path resolved at boot from `/t/<tenant>/portal/`
- [ ] No password input in the SPA; logout goes through `/auth/logout`
- [ ] Built SPA published as plain static assets (no `embed.FS`); served by Caddy `file_server` in self-hosted mode and/or Cloudflare Pages in managed mode; same-origin with the Limen API
- [ ] SPA build is base-path-agnostic; tenant `PublicID` resolved at boot from `window.location.pathname`
- [ ] CSP header set on portal HTML responses
- [ ] `AGENTS.md` build section updated with `buf generate` and `pnpm build`
- [ ] Unit tests for the role-enforcement interceptor

### Phase 10 — Wiring, verification, hardening

- [ ] `cmd/gateway/main.go` wires DB → migrate → crypto → Zitadel client → OIDC RP → upstream registry → refresher → gateway → oauthproxy → middleware → portal → transport
- [ ] CLI subcommands: `-create-tenant`, `-create-upstream`, `-migrate`
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
- [ ] All images pinned to specific versions; Postgres at `postgres:18-alpine`
- [ ] All secrets sourced from `docker secret` files
- [ ] `limen-migrate` one-shot gates `limen` via `condition: service_completed_successfully`
- [ ] Healthchecks on every long-running service; `restart: unless-stopped`
- [ ] Caddyfile configured for `limen.example.com` (reverse-proxies `/t/*/api`, `/auth`, `/oauth`, `/mcp`, `/upstream` to Limen; serves SPA assets via `file_server` or reverse-proxies non-API paths to Cloudflare Pages) and `auth.limen.example.com` with HSTS
- [ ] `deploy/postgres/limen-init.sql` provisions `limen_admin` and `limen_app` roles
- [ ] Volumes named explicitly; backups mounted to a known host path
- [ ] Daily backup services with retention policy
- [ ] `docs/runbook.md` covers: first deploy, upgrade, rotate encryption key, backup, restore
- [ ] `.gitignore` blocks `secrets/` and `backups/`

### Phase 12 — Staff tenant & backoffice

- [ ] `Tenant.Kind` enum (`customer` | `staff`) with partial unique index for `staff`
- [ ] Reserved staff tenant `_staff` documented in Phase 4 and enforced in tenant-creation paths (customer URLs are `tnt_<ULID>` so collisions are structurally impossible)
- [ ] Zitadel `super_admin` project role defined in the bootstrap script; honored only inside the staff tenant
- [ ] Phase 0 bootstrap idempotently creates the `limen-staff` org + one staff user from `LIMEN_STAFF_BOOTSTRAP_EMAIL`
- [ ] Phase 11 `limen-migrate` ensures the `_staff` tenant row exists; refuses prod start without `LIMEN_STAFF_ZITADEL_ORG_ID`
- [ ] RLS `SELECT` policies extended with `current_setting('limen.staff_mode', true) = 'on'`; write policies unchanged
- [ ] `storage.WithStaffRead(ctx)` helper sets the GUC transaction-locally via `set_config`
- [ ] `proto/limen/staff/v1/staff.proto` + Go/TS codegen; mounted at `/t/_staff/api/...`
- [ ] `internal/staff/` package implements every RPC + impersonation flow
- [ ] `RequireStaffSession` + `RequireSuperAdmin` + `AuditingInterceptor` mounted on the staff API
- [ ] `staff_audit_log` partitioned table + `audit.append(...)` SECURITY DEFINER insert function
- [ ] Impersonation cookie separate from staff session cookie, scoped to `/t/<target-tenant>`, hard 15-min TTL, never auto-renewed
- [ ] MFA freshness check on the staff session enforced server-side before impersonation start
- [ ] Customer SPA renders a non-dismissible banner whenever an impersonation cookie is present
- [ ] OAuth-handshake CTAs (`mcp_spec` connect, `static_header` user-key submission) disabled inside impersonated sessions
- [ ] Upstream tokens stay encrypted-at-rest under staff visibility; decryption remains inside the upstream-call transport only
- [ ] SPA route bundles split: customer tenants never download the staff bundle, and vice versa
- [ ] Integration tests cover: role isolation, RLS staff-mode read, write blocked from staff-mode, impersonation happy path, MFA gate, TTL expiry, force-unlink audit row, bundle separation
- [ ] Phase 10 runbook updated with the impersonation procedure and audit-log query examples

### Phase 13 — Billing with Stripe (per-seat)

- [ ] `tenant_billing` table + RLS policies + partial unique `(tenant_id) WHERE deleted_at IS NULL` + staff-mode SELECT clause from Phase 12
- [ ] `internal/billing/` package (client / seats / webhook / middleware / service); Stripe SDK calls wrapped in `internal/resilience.Client("stripe.*", cfg)`
- [ ] `proto/limen/portal/v1/portal.proto` `BillingService` (owner-only): `GetBillingSummary`, `CreateCheckoutSession`, `OpenCustomerPortal`
- [ ] `proto/limen/staff/v1/staff.proto` extended: `GetTenantBilling`, `ExtendGrace`, `CompTenant`, `ForceCancel` (all audited)
- [ ] `RequireBillingActive` middleware on `/t/{tenant}/mcp` and `/t/{tenant}/api/*` (except the billing namespace); staff tenant exempt
- [ ] Stripe webhook at `/billing/stripe/webhook` with signature verification, idempotency by Stripe object id, async drain
- [ ] Seat reconciler: reactive (after every Members-mutation RPC) + periodic (6 h jittered loop)
- [ ] Free-tier limits enforced in Members and Upstream paths when `status='none'`
- [ ] SPA `Billing.vue` + past-due banner + nav item gated on `role=owner`
- [ ] `config.yaml` `billing:` section: `enabled`, Stripe key/secret refs, `seat_price_id`, `trial_days`, `grace_days`, `free_tier.*`
- [ ] Stripe Dashboard runbook: product + seat price + webhook endpoint + Customer Portal toggles + Tax configuration
- [ ] Staff-audit-log records `billing.comp`, `billing.extend_grace`, `billing.force_cancel` with reason
- [ ] Integration tests covering: subscribe happy path, reactive + periodic reconciliation, webhook signature + idempotency, payment failure → grace → 402, payment recovery, cancel-at-period-end, staff comp, free-tier limits, staff-tenant exempt, `billing.enabled: false` short-circuit

## Cross-cutting decisions

- **Tenancy**: path prefix `/t/{tenant}/...`; cookies path-scoped.
- **IDs**: every entity has an internal `int64` PK (used for FKs and joins) plus a public ULID with a Stripe-style type prefix (`tnt_`, `usr_`, `ups_`, `usc_`, `ureg_`, `ulnk_`, `zapp_`). Only the prefixed ULID appears in APIs, URLs, the SPA, and operator logs. ULIDs are time-sortable at millisecond resolution (and monotonic within a process), so cursor pagination uses `WHERE public_id > $cursor`.
- **Audit columns**: every table embeds `CreatedAt`, `UpdatedAt`, `DeletedAt` (`timestamptz`). `UpdatedAt` is maintained by a Postgres `BEFORE UPDATE` trigger; `DeletedAt` uses GORM's `gorm.DeletedAt` so soft-deleted rows are invisible by default and require `Unscoped()` to see. Composite uniques are partial (`WHERE deleted_at IS NULL`) so soft-deletes don't block re-creation.
- **Identity**: Zitadel is the OIDC provider. Tenant ↔ Zitadel organization (1:1). No local passwords in Limen.
- **DB**: GORM on PostgreSQL 18.2 with mandatory RLS + FORCE ROW LEVEL SECURITY + non-superuser app role. Dev and prod both run Postgres via Docker Compose.
- **Membership**: 1 user ↔ 1 tenant; same email in different tenants = different Zitadel accounts.
- **Roles**: `owner` / `admin` / `member` (stored in Limen, not Zitadel).
- **Token validation**: in-process JWT against Zitadel's JWKS (single issuer, single cache entry); cross-tenant defense via `org_id` claim match.
- **OIDC library**: `github.com/zitadel/oidc/v3` (RP side).
- **CLI**: `github.com/spf13/cobra` for the subcommand tree (`serve`, `create-tenant`, `migrate`, …) + `github.com/spf13/viper` for flag-to-env binding (`LIMEN_*` prefix). The Phase 2 YAML loader (`internal/config.Load`) stays as-is for file-based config because Viper doesn't natively support our `${ENV:-default}` substitution.
- **Zitadel API**: `github.com/zitadel/zitadel-go/v3` (official SDK, wraps the generated gRPC clients for Management / User / Session / Org services). Hand-rolled HTTP/JSON against Zitadel is avoided — every Management/User/Session call goes through the SDK behind a thin `internal/zitadel/` wrapper. Auth uses `client.PAT` in dev and `client.DefaultServiceUserAuthentication` (private-key JWT) in production.
- **Crypto**: AES-256-GCM, AAD binds `tenant|user|kind`.
- **Outbound transport**: custom `http.RoundTripper` for per-request bearer injection (tenant + user read from ctx).
- **Resilience**: every outbound HTTP dependency (upstream MCP servers, upstream OAuth token endpoints, Zitadel Management / Session / JWKS, DCR endpoints) is wrapped in a context-aware **timeout → retry-with-exponential-backoff-and-jitter → circuit-breaker** stack. One shared package, `internal/resilience/`, exports a `Client(name, cfg) *http.Client` helper that composes `github.com/cenkalti/backoff/v4` + `github.com/sony/gobreaker/v2` into an `http.RoundTripper`. Per-dependency policies (max retries, base / max interval, breaker thresholds, retryable status codes) live in `config.yaml`. Retries only fire on transport errors and `5xx` / `429`; `4xx` is terminal. Each breaker exposes Prometheus-style state via structured logs (`closed` / `half-open` / `open`) for observability.
- **SaaS-operator visibility**: a fourth Zitadel project role `super_admin` exists alongside `owner` / `admin` / `member`, but is honored **only** inside the reserved staff tenant `_staff` (Phase 12). Cross-tenant visibility is granted at the data layer via a Postgres GUC `limen.staff_mode` that loosens `SELECT` RLS policies only — writes still require `limen.tenant_id` to be set explicitly, so even staff cannot accidentally cross-write. Targeted support actions go through audited RPCs (force-unlink, force re-enable, breaker control) and impersonation rides on Zitadel token-exchange with a hard 15-minute TTL plus customer-side banner.
- **Billing model**: per-seat Stripe Billing subscription per customer tenant (Phase 13). Seat = Zitadel user grant against the Limen project for that tenant's org. Reconciliation is both reactive (after every Members-mutation RPC) and periodic (6 h loop). No card data ever touches Limen — Stripe-hosted Checkout + Customer Portal handle all PCI scope. The staff tenant is never billed; self-hosters set `billing.enabled: false` to skip Stripe entirely.
- **Deployment**: Docker Compose for both dev and prod, single declarative source of truth.

## Explicitly out of scope this iteration

SAML / SCIM (use Zitadel's roadmap for these); MFA enforcement policy (configured in Zitadel directly); audit logging beyond structured-log events; per-tenant rate limits at the application layer; usage-based / metered billing (Phase 13 ships seat-only — usage metering is designed-for but deferred); fine-grained per-tool scopes; outbound strategies beyond `mcp_spec`, `static_header`, and `none`; HA Kubernetes manifests (Phase 11 ships the single-VM reference compose).

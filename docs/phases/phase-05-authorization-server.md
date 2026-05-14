# Phase 5 — Zitadel integration (AS delegation + DCR proxy)

**Depends on**: Phase 4 (tenant resolution + OIDC RP)
**Unblocks**: Phase 6

## Goal

Make Limen a **first-class MCP-spec citizen without implementing an Authorization Server itself**. Zitadel is the AS. Limen's job in this phase:

1. Confirm that the Zitadel project + apps provisioned in [Phase 0](phase-00-dev-environment.md) match what Limen expects.
2. Implement a **DCR proxy** at `/t/{tenant}/oauth/register` that lets MCP clients dynamically register themselves (per MCP spec) and routes those registrations into the tenant's Zitadel organization via the Zitadel Management API.
3. Implement tenant lifecycle hooks: creating / disabling a Limen tenant creates / disables the corresponding Zitadel organization.
4. Expose helper endpoints that MCP clients expect at `/t/{tenant}/oauth/*` — but as **thin redirects** to the real Zitadel endpoints (`authorize`, `token`, `jwks`, `userinfo`), not reimplementations.

The actual JWT validation happens in [Phase 6](phase-06-resource-server.md). This phase is about plumbing Limen's surface area into Zitadel's reality.

## Why not implement the AS ourselves

We considered (and earlier drafted) a per-tenant AS using `zitadel/oidc/v3`'s `op` subpackage. The pivot to Zitadel-the-product instead of Zitadel-the-library is driven by:

- **Free user management UI** — password reset, MFA, email verification, audit log, admin console: Zitadel ships them.
- **Battle-tested OIDC**: device flow, refresh-token rotation, introspection, end-session, RP-initiated logout are already implemented.
- **B2B model**: Zitadel's _organizations_ feature is exactly our multi-tenancy concept. One Zitadel instance, many orgs, shared projects.
- **Reduced cryptographic surface in Limen**: no signing keys, no key rotation logic, no DCR storage. Smaller blast radius.

The trade-off: Limen now has an operational dependency on Zitadel. Both [Phase 0](phase-00-dev-environment.md) and [Phase 11](phase-11-production-deployment.md) make that dependency reproducible.

## Architecture

```
                MCP client (VS Code, Claude Desktop, ...)
                                │
                                │ 1. GET /t/acme/mcp                    (401 + WWW-Authenticate)
                                │ 2. GET PRM                            (points at Zitadel)
                                │ 3. GET AS metadata from Zitadel
                                │ 4. POST /t/acme/oauth/register        ←─ Limen DCR proxy
                                │    └─ Limen calls Zitadel Mgmt API to
                                │       create an OIDC app in acme's org
                                │ 5. /authorize on Zitadel              (PKCE + resource indicator)
                                │ 6. /token on Zitadel                  (returns JWT for aud=Limen MCP RS)
                                │ 7. GET /t/acme/mcp with bearer        → 200
                                ▼
                       ┌───────────────┐
                       │     Limen     │
                       └───────────────┘
```

The DCR proxy is the _only_ OAuth-shaped endpoint Limen actually owns in this phase. Everything else is either pass-through metadata or a 302 redirect.

## Endpoints under `/t/{tenant}/oauth/`

Mounted behind `RequireTenant` (no portal session required — these are MCP-client-facing).

| Method         | Path                                      | Behavior                                                                                                                                         |
| -------------- | ----------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| GET            | `/.well-known/oauth-authorization-server` | **Static** JSON proxying Zitadel's AS metadata with one adjustment: `registration_endpoint` points at Limen's `/register`                        |
| GET            | `/.well-known/openid-configuration`       | Same — alias                                                                                                                                     |
| POST           | `/register`                               | Limen's DCR proxy (see below)                                                                                                                    |
| GET/PUT/DELETE | `/register/{client_id}`                   | RFC 7592 client management — proxied to Zitadel Management API                                                                                   |
| GET            | `/authorize`                              | **302** to Zitadel's `/oauth/v2/authorize` with the same query (Zitadel handles login UI, MFA, consent)                                          |
| POST           | `/token`                                  | **302/proxy** to Zitadel's `/oauth/v2/token`                                                                                                     |
| GET            | `/userinfo`                               | **302** to Zitadel's `/oidc/v1/userinfo`                                                                                                         |
| —              | `/jwks`                                   | **Not mounted** — `jwks_uri` in the metadata points directly at Zitadel so Phase 6's in-process JWT verifier fetches keys without a redirect hop |
| POST           | `/revoke`                                 | **302/proxy** to Zitadel's `/oauth/v2/revoke`                                                                                                    |
| POST           | `/introspect`                             | **302/proxy** to Zitadel's `/oauth/v2/introspect`                                                                                                |
| GET            | `/end_session`                            | **302** to Zitadel's `/oidc/v1/end_session`                                                                                                      |

### Why proxy vs. redirect

- **GET endpoints (authorize, userinfo, end_session)** → 302 redirects. The user agent / MCP client follows them. No request bodies to forward. Zero crypto in Limen.
- **POST endpoints (token, revoke, introspect)** can either redirect (HTTP 307/308 preserve POST and bodies — supported by all modern clients) or reverse-proxy. Pick **redirect** for simplicity; document the MCP client compatibility footprint.
- **`jwks_uri`** advertised in the metadata document is Zitadel's URL directly. Limen does **not** redirect or proxy JWKS — the RS (Phase 6) is in-process and fetches Zitadel's JWKS itself, with caching. Adding a redirector layer would only slow key resolution down.
- **`/register`** is genuinely intercepted because Limen needs to translate the MCP client's DCR request into a Zitadel Management API call scoped to the tenant's org.

### Metadata document

Limen serves the metadata file itself (rather than redirecting) because it must rewrite `registration_endpoint` to point at Limen. Sample:

```json
{
  "issuer": "https://auth.limen.example.com",
  "authorization_endpoint": "https://limen.example.com/t/acme/oauth/authorize",
  "token_endpoint": "https://limen.example.com/t/acme/oauth/token",
  "jwks_uri": "https://auth.limen.example.com/oauth/v2/keys",
  "userinfo_endpoint": "https://limen.example.com/t/acme/oauth/userinfo",
  "registration_endpoint": "https://limen.example.com/t/acme/oauth/register",
  "revocation_endpoint": "https://limen.example.com/t/acme/oauth/revoke",
  "introspection_endpoint": "https://limen.example.com/t/acme/oauth/introspect",
  "end_session_endpoint": "https://limen.example.com/t/acme/oauth/end_session",
  "scopes_supported": ["openid", "profile", "email", "offline_access"],
  "response_types_supported": ["code"],
  "grant_types_supported": ["authorization_code", "refresh_token"],
  "code_challenge_methods_supported": ["S256"],
  "token_endpoint_auth_methods_supported": [
    "none",
    "client_secret_basic",
    "client_secret_post"
  ],
  "resource_indicators_supported": true
}
```

Note: `issuer` is **Zitadel's issuer**, not Limen's. The `iss` claim on issued tokens will be Zitadel's, and Limen's RS (Phase 6) verifies against that. The endpoints route through Limen so the AS Metadata stays one document and clients only need to know Limen's URL.

### DCR proxy (`internal/oauthproxy/dcr.go`)

Flow:

1. Receive `POST /t/{tenant}/oauth/register` with the MCP client's metadata document.
2. Validate input (default-deny):
   - `redirect_uris` is non-empty and each URI matches the rules below.
   - `grant_types` ⊆ {`authorization_code`, `refresh_token`}; `response_types` = {`code`}.
   - `token_endpoint_auth_method` ∈ {`none`, `client_secret_basic`}.
   - `application_type` defaults to `native`.
   - **Reject unknown / unsupported fields** with `invalid_client_metadata` rather than silently ignoring them — prevents future-spec features (implicit grant, hybrid flows, etc.) from leaking in.

   Allowed `redirect_uris` shapes:

   | Scheme                                                         | Constraint                                                                                                                                              |
   | -------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
   | `https://...`                                                  | Exact match. Host must not be an IP, must not be IDN-encoded, no `#fragment`.                                                                           |
   | `http://127.0.0.1[:port]/...` / `http://[::1][:port]/...`      | RFC 8252 §7.3 loopback. Any port, any path; no `#fragment`.                                                                                             |
   | `http://localhost[:port]/...`                                  | Same as loopback (Limen treats `localhost` as an alias).                                                                                                |
   | Custom scheme (e.g. `cursor://callback`, `com.example.app://`) | Reverse-DNS-shaped scheme only (`^[a-z][a-z0-9+\-.]*$` containing at least one dot, no `data:` / `javascript:` / `file:`). Host + path opaque to Limen. |

   Wildcards in any component, IDN hosts, and trailing-slash mismatches are all rejected. Validation is shared between `POST /register` and `PUT /register/{client_id}`.

   **Tenant-configurable allowlist (subtractive).** `tenant.DCRRedirectURIAllowlist` is a **list** of glob patterns — a tenant admin can add as many entries as needed (e.g. one per environment, one per first-party app, one per public-beta client). When the list is non-empty, every `redirect_uri` in the request must **additionally** match **at least one** pattern in the list. The list can only narrow what the floor allows — it can never relax the floor (e.g. you cannot allowlist a `*.com` wildcard or a `file://` scheme). Empty list = floor only. Order is irrelevant; duplicates are deduped at save time.

   Pattern syntax (glob, not regex):

   | Pattern                             | Matches                                |
   | ----------------------------------- | -------------------------------------- |
   | `https://app.acme.com/callback`     | exact URI                              |
   | `https://*.acme.com/oauth/callback` | any single host label                  |
   | `https://*.acme.com/**`             | any single host label + any path depth |
   | `http://127.0.0.1:*/**`             | any port + any path (loopback)         |
   | `cursor://**`                       | any path under a custom scheme         |

   Matching is component-wise: scheme exact; host glob (`*` = one label, no leading-`*` against `<2` fixed suffix labels — i.e. `*.acme.com` ok, `*.com` rejected at policy-save time); port literal or `*`; path glob (`*` = one segment, `**` = multi-segment). Patterns are validated at save time so an admin can't store something the matcher would later reject.

   A DCR rejection due to allowlist mismatch emits a structured log (`tenant_id`, rejected URI, active patterns) for ops triage.

3. If `tenant.DCREnabled == false` → 403.
4. If `oauth_proxy.dcr_initial_access_token` is configured, require it on the request.
5. Call Zitadel's Management API (via the shared `*zitadel.Client` — see [internal/zitadel/](../../internal/zitadel/)) to create an OIDC app inside `tenant.zitadel_org_id`'s project. Map the MCP DCR fields to Zitadel's app-create payload:

   | MCP DCR field                     | Zitadel app field                                        |
   | --------------------------------- | -------------------------------------------------------- |
   | `client_name`                     | `name`                                                   |
   | `redirect_uris`                   | `redirectUris`                                           |
   | `grant_types`                     | `grantTypes` (map names)                                 |
   | `response_types`                  | `responseTypes`                                          |
   | `token_endpoint_auth_method`      | `authMethodType` (`none` ↔ `OIDC_AUTH_METHOD_TYPE_NONE`) |
   | `application_type=native`         | `appType=OIDC_APP_TYPE_NATIVE`                           |
   | `software_id`, `software_version` | persisted in Limen's mirror row, not Zitadel             |

6. Persist a `ZitadelApp` row in Limen with `(tenant_id, zitadel_app_id, client_id, client_secret_encrypted?, name, software_id, software_version, created_at, registration_access_token_hash)`. This mirror exists so the portal can list MCP clients per tenant without re-querying Zitadel on every page load, and so RFC 7592 (`/register/{client_id}`) can authenticate the management token.
7. Return the DCR response: `client_id`, optional `client_secret`, `client_id_issued_at`, `client_secret_expires_at=0`, `registration_access_token`, `registration_client_uri=<Limen>/t/{tenant}/oauth/register/{client_id}`.

`registration_access_token` is generated by Limen with `crypto/rand` (32 bytes, base64url-encoded). Only its **SHA-256 hash** is persisted — same model as OAuth client secrets elsewhere — and verified with `subtle.ConstantTimeCompare` on `/register/{client_id}` operations. Hashing is sufficient on its own; we don't double-wrap with AES-SIV because the row already isn't reversible to a usable credential.

### Zitadel app management

App CRUD against Zitadel is **not** wrapped by a second adapter in `oauthproxy`. Instead, we extend the existing shared client at [internal/zitadel/](../../internal/zitadel/) with `apps.go`, adding `AddOIDCApp`, `UpdateOIDCApp`, `DeleteOIDCApp`, and `GetOIDCApp`. The `oauthproxy` package declares a small consumer-side interface (`type appManager interface { ... }`) that `*zitadel.Client` satisfies — SOLID's ISP, DRY for the existing auth-mode + transport plumbing.

This keeps **one** Zitadel client in the binary, used by:

- Tenant creation CLI ([Phase 4](phase-04-tenant-auth-session.md)) — orgs + users.
- DCR proxy (this phase) — apps.
- Portal admin RPCs ([Phase 9](phase-09-portal-spa.md)) — invite / disable / role-change / MCP-client revocation.

### Routes mounted in `internal/transport/http.go`

```
/t/{tenant}/oauth/.well-known/oauth-authorization-server   → metadata handler
/t/{tenant}/oauth/.well-known/openid-configuration         → metadata handler (alias)
/t/{tenant}/oauth/register                                 → DCR proxy
/t/{tenant}/oauth/register/{client_id}                     → DCR management proxy
/t/{tenant}/oauth/{authorize,token,userinfo,revoke,introspect,end_session}       → redirector
```

All behind `RequireTenant`.

### Package layout

```
internal/oauthproxy/
├── metadata.go       // /.well-known/* → static rewritten Zitadel metadata
├── redirector.go     // /authorize, /token, /userinfo, /revoke, /introspect, /end_session
├── dcr.go            // /register and /register/{client_id}
└── ratelimit.go      // per-tenant token-bucket middleware (golang.org/x/time/rate)

internal/zitadel/
└── apps.go           // (new) AddOIDCApp / UpdateOIDCApp / DeleteOIDCApp / GetOIDCApp
```

## Deliverables

- New files listed above under `internal/oauthproxy/` and `internal/zitadel/apps.go`.
- Existing `ZitadelApp` model ([Phase 1](phase-01-database-foundation.md)) gets a migration adding the `registration_access_token_hash` column (replacing the originally planned encrypted variant — see DCR proxy section).
- Existing `Tenant` model ([Phase 1](phase-01-database-foundation.md)) gets a migration adding `dcr_redirect_uri_allowlist JSONB NOT NULL DEFAULT '[]'`. Validated at save time against the glob syntax + “≥2 fixed suffix labels” rule; surfaced in the [Phase 9b tenant-admin SPA](phase-09b-tenant-admin-spa.md) Settings page (gated by `RequireRole(owner|admin)`, i.e. tenant administrators) and on `limen create-tenant` via a repeatable `--dcr-redirect-uri-allow` flag for operator bootstrapping.
- New shared matcher: `internal/oauthproxy/uripolicy.go` — implements both the global floor table and the tenant-allowlist glob matcher; consumed by `dcr.go` and the [Phase 9b](phase-09b-tenant-admin-spa.md) tenant-admin RPC that validates patterns before saving.
- `internal/config/config.go`: the dead `OAuthServerConfig` (signing algo / TTLs / consent — all moot now that Zitadel is the AS) is **dropped** and replaced with `OAuthProxyConfig { DCREnabled bool; DCRInitialAccessToken string; RateLimit { RPS, Burst int } }`. The Zitadel PAT / project ID are **not** duplicated here — the proxy reuses the existing top-level `zitadel:` block (and the `*zitadel.Client` constructed from it).
- Updated `internal/transport/http.go` to mount the routes.

## Security & operational notes

- **Service account PAT** is the most sensitive credential after the encryption key. Stored in the configured secret store; never in `config.yaml` literally. Rotate periodically (Zitadel supports multiple PATs per service user).
- **PAT scope** must be limited to the Limen project: grant only the org/user/app management permissions actually used. Audit via Zitadel's admin console.
- **DCR rate limiting** at the proxy is mandatory — an unauthenticated `/register` is otherwise an abuse vector. Per-tenant `golang.org/x/time/rate` token bucket (default 10 rps / burst 20), keyed by `tenant.PublicID`, applied only to the `/register*` subtree.
- **`registration_access_token`** is generated by Limen with `crypto/rand` (32 bytes), base64url-encoded. Only the SHA-256 hash is persisted; verification uses `subtle.ConstantTimeCompare`.
- **PKCE S256 mandatory** on the Zitadel app — Limen configures every DCR'd app with PKCE required.
- **Redirect URIs validated** against the table in the DCR proxy section; default-deny on anything outside it.
- **Registration lifecycle is operator-driven in v1.** A successful `POST /register` creates a row that lives until an operator (portal MCP Clients page, Phase 9) or the client itself (RFC 7592 `DELETE /register/{client_id}`) removes it. **No automatic expiry** — we intentionally avoid a reaper for v1 because (a) registrations are tenant-scoped so the blast radius of staleness is one org, (b) auto-expiring a still-in-use desktop client mid-session is a worse UX than carrying a few dead rows. A scheduled “unused for N days” cleanup is tracked as a hardening item ([Phase 10](phase-10-wiring-hardening.md) / [Phase 11](phase-11-production-deployment.md)).
- **Client impersonation** — DCR is open by design; a malicious client can self-declare `client_name: "Claude Desktop"`. Mitigations available today: (1) require `dcr_initial_access_token` for tenants that need pre-approval, (2) the portal's MCP Clients page surfaces every registration to the tenant owner for review/revocation. Software-statement / attestation-based brand trust is **not** in v1 scope.
- **307 redirects on POST endpoints**: tested with the MCP client matrix (VS Code, Claude Desktop, Cursor) — fall back to reverse-proxy if any client mishandles 307 with bodies.

## Verification

- **Metadata document**: `GET /t/acme/oauth/.well-known/oauth-authorization-server` returns a JSON whose `registration_endpoint` is `<Limen>/t/acme/oauth/register` and whose `issuer` matches Zitadel.
- **DCR happy path**: `POST /t/acme/oauth/register` with a valid metadata doc returns 201 with `client_id`, `registration_access_token`, etc. A Zitadel app appears under acme's org with matching settings. A `ZitadelApp` row exists in Limen.
- **DCR auth**: when `dcr_initial_access_token` is set, requests without it return 401.
- **RFC 7592 management**: `GET /t/acme/oauth/register/{client_id}` with the registration access token returns the current configuration; `PUT` updates it (via Zitadel); `DELETE` removes the app (Zitadel + Limen mirror).
- **Redirects**: `GET /t/acme/oauth/authorize?...` returns 302 with `Location: https://auth.limen.example.com/oauth/v2/authorize?...` preserving the query.
- **End-to-end discovery**: an MCP client hits `/t/acme/mcp` → 401 with `WWW-Authenticate` pointing at Limen's PRM → discovers Limen's AS metadata → DCRs → drives authorize on Zitadel → token on Zitadel → token has `iss=https://auth.limen.example.com`, `aud=<Limen MCP RS resource URI>`, `org_id=acme's zitadel org id` → bearer succeeds at `/t/acme/mcp`.

## Risks

- **307 POST handling**: some HTTP clients silently downgrade to GET. Test against real MCP clients early; fall back to reverse-proxy if needed.
- **Zitadel API rate limits**: bulk operations (e.g. creating many tenants) may hit them. The DCR path is one Zitadel call per registration — fine for normal load; the CLI tenant creation is operator-driven and infrequent.
- **Field mapping drift**: Zitadel's app-create API evolves. Pin the API version (Zitadel's gRPC is versioned) and add a contract test that round-trips a DCR through to verify the mapping still works.
- **Single project model**: All tenant orgs share one Limen project. If a tenant needs its own project (e.g. separate signing keys / policies), Zitadel supports it but the architecture would need to evolve. Document this limit; not in scope for v1.
- **CIMD (deferred)**: The MCP spec update of 2025-11-25 introduced [Client ID Metadata Documents](https://workos.com/blog/client-id-metadata-documents-cimd-oauth-client-registration-mcp) and flipped the default for new clients to CIMD over DCR. We deliberately ship DCR-only in v1 for two reasons. (1) Our target client matrix — VS Code, Claude Desktop, Cursor, IDE plugins, local agents — has no stable HTTPS origin to host a CIMD document on; the article calls these out as DCR's remaining sweet spot. (2) Zitadel does not implement CIMD natively, and adding it on Limen's side would require turning the `/authorize` + `/token` redirectors into a full reverse-proxy that intercepts `client_id=https://...`, fetches the document, and materializes (and caches) a Zitadel client per CIMD URL — which reintroduces exactly the server-side state the CIMD design is trying to eliminate, _and_ undoes Phase 5's “Zitadel is the AS, Limen is a thin proxy” simplification. Revisit when Zitadel ships CIMD upstream, or if a tenant onboards a fleet of web-hosted MCP agents with public origins.

## Checklist

- [x] Dead `OAuthServerConfig` removed from `internal/config/config.go` (and `config.yaml`); replaced by `OAuthProxyConfig`
- [x] `internal/zitadel/apps.go` adds `AddOIDCApp` / `UpdateOIDCApp` / `DeleteOIDCApp` / `GetOIDCApp` to the existing client (no second wrapper package)
- [ ] `internal/oauthproxy/metadata.go` serves a static metadata document with `registration_endpoint` rewritten to Limen and `jwks_uri` pointing directly at Zitadel
- [ ] `internal/oauthproxy/redirector.go` issues 302 redirects for `authorize`, `userinfo`, `end_session` and 307 redirects for `token`, `revoke`, `introspect`
- [ ] `internal/oauthproxy/ratelimit.go` enforces a per-tenant token bucket on `/register*` (default 10 rps / burst 20)
- [ ] `internal/oauthproxy/dcr.go` accepts MCP-spec DCR requests and creates Zitadel OIDC apps via the shared `*zitadel.Client`
- [ ] DCR proxy enforces `tenant.DCREnabled` and optional `dcr_initial_access_token`
- [ ] DCR proxy **rejects unknown / unsupported metadata fields** with `invalid_client_metadata` (default-deny)
- [ ] PKCE S256 required on every DCR'd app
- [ ] `redirect_uris` validated per the table in the DCR proxy section (HTTPS exact-match, RFC 8252 loopback, reverse-DNS custom schemes); wildcards / IDN / fragments rejected; same validator used by `POST /register` and `PUT /register/{client_id}`
- [ ] `Tenant.DCRRedirectURIAllowlist` column added by migration; when non-empty, every DCR `redirect_uri` must additionally match a tenant pattern (subtractive — floor still applies)
- [ ] `internal/oauthproxy/uripolicy.go` implements the shared floor + allowlist matcher; reused by the DCR proxy and the [Phase 9b](phase-09b-tenant-admin-spa.md) tenant-admin validation RPC
- [ ] Glob patterns validated at save time (`*` = one host label / path segment, `**` = multi-segment path; leading-wildcard host requires ≥2 fixed suffix labels)
- [ ] Allowlist surfaced in the [Phase 9b](phase-09b-tenant-admin-spa.md) tenant-admin SPA Settings page (gated by `RequireRole(owner|admin)`); `limen create-tenant` accepts a repeatable `--dcr-redirect-uri-allow` flag for operator bootstrapping
- [ ] Allowlist-mismatch DCR rejection emits a structured log (`tenant_id`, rejected URI, active patterns)
- [ ] Registration lifecycle documented as operator-driven for v1 (no auto-expiry reaper)
- [ ] `ZitadelApp` mirror row persisted per registration; `registration_access_token_hash` column added by migration and used for constant-time auth
- [ ] RFC 7592 management endpoints (`GET/PUT/DELETE /register/{client_id}`) implemented and authenticated via the registration access token
- [ ] Config additions: `oauth_proxy.{dcr_enabled, dcr_initial_access_token, rate_limit.{rps, burst}}` (Zitadel PAT / project ID are **reused** from the existing top-level `zitadel:` block)
- [ ] Routes mounted under `/t/{tenant}/oauth/*` behind `RequireTenant`
- [ ] Integration test: full inbound discovery → DCR → authorize → token → `/mcp` roundtrip against the dev Zitadel container
- [ ] Integration test: DCR with missing initial access token → 401
- [ ] Integration test: RFC 7592 GET/PUT/DELETE with valid and invalid registration access tokens
- [ ] Contract test: Zitadel app-create field mapping (covers a future API drift)

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

| Method         | Path                                      | Behavior                                                                                                                  |
| -------------- | ----------------------------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| GET            | `/.well-known/oauth-authorization-server` | **Static** JSON proxying Zitadel's AS metadata with one adjustment: `registration_endpoint` points at Limen's `/register` |
| GET            | `/.well-known/openid-configuration`       | Same — alias                                                                                                              |
| POST           | `/register`                               | Limen's DCR proxy (see below)                                                                                             |
| GET/PUT/DELETE | `/register/{client_id}`                   | RFC 7592 client management — proxied to Zitadel Management API                                                            |
| GET            | `/authorize`                              | **302** to Zitadel's `/oauth/v2/authorize` with the same query (Zitadel handles login UI, MFA, consent)                   |
| POST           | `/token`                                  | **302/proxy** to Zitadel's `/oauth/v2/token`                                                                              |
| GET            | `/userinfo`                               | **302** to Zitadel's `/oidc/v1/userinfo`                                                                                  |
| GET            | `/jwks`                                   | **302** to Zitadel's `/oauth/v2/keys`                                                                                     |
| POST           | `/revoke`                                 | **302/proxy** to Zitadel's `/oauth/v2/revoke`                                                                             |
| POST           | `/introspect`                             | **302/proxy** to Zitadel's `/oauth/v2/introspect`                                                                         |
| GET            | `/end_session`                            | **302** to Zitadel's `/oidc/v1/end_session`                                                                               |

### Why proxy vs. redirect

- **GET endpoints (authorize, jwks, userinfo, end_session)** → 302 redirects. The user agent / MCP client follows them. No request bodies to forward. Zero crypto in Limen.
- **POST endpoints (token, revoke, introspect)** can either redirect (HTTP 307/308 preserve POST and bodies — supported by all modern clients) or reverse-proxy. Pick **redirect** for simplicity; document the MCP client compatibility footprint.
- **`/register`** is genuinely intercepted because Limen needs to translate the MCP client's DCR request into a Zitadel Management API call scoped to the tenant's org.

### Metadata document

Limen serves the metadata file itself (rather than redirecting) because it must rewrite `registration_endpoint` to point at Limen. Sample:

```json
{
  "issuer": "https://auth.limen.example.com",
  "authorization_endpoint": "https://limen.example.com/t/acme/oauth/authorize",
  "token_endpoint": "https://limen.example.com/t/acme/oauth/token",
  "jwks_uri": "https://limen.example.com/t/acme/oauth/jwks",
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
2. Validate input: `redirect_uris` is non-empty and each is a valid URI; `grant_types` ⊆ {`authorization_code`, `refresh_token`}; `token_endpoint_auth_method` ∈ {`none`, `client_secret_basic`}; `application_type` defaults to `native`.
3. If `tenant.DCREnabled == false` → 403.
4. If `oauth_server.dcr_initial_access_token` is configured, require it on the request.
5. Call Zitadel's Management API to create an OIDC app inside `tenant.zitadel_org_id`'s project. Map the MCP DCR fields to Zitadel's app-create payload:

   | MCP DCR field                     | Zitadel app field                                        |
   | --------------------------------- | -------------------------------------------------------- |
   | `client_name`                     | `name`                                                   |
   | `redirect_uris`                   | `redirectUris`                                           |
   | `grant_types`                     | `grantTypes` (map names)                                 |
   | `response_types`                  | `responseTypes`                                          |
   | `token_endpoint_auth_method`      | `authMethodType` (`none` ↔ `OIDC_AUTH_METHOD_TYPE_NONE`) |
   | `application_type=native`         | `appType=OIDC_APP_TYPE_NATIVE`                           |
   | `software_id`, `software_version` | persisted in Limen's mirror row, not Zitadel             |

6. Persist a `ZitadelApp` row in Limen with `(tenant_id, zitadel_app_id, client_id, client_secret_encrypted?, name, software_id, software_version, created_at, registration_access_token_encrypted)`. This mirror exists so the portal can list MCP clients per tenant without re-querying Zitadel on every page load, and so RFC 7592 (`/register/{client_id}`) can authenticate the management token.
7. Return the DCR response: `client_id`, optional `client_secret`, `client_id_issued_at`, `client_secret_expires_at=0`, `registration_access_token`, `registration_client_uri=<Limen>/t/{tenant}/oauth/register/{client_id}`.

`registration_access_token` is generated by Limen (not Zitadel), stored encrypted with AAD `tenant|client_id|"dcr.registration_access_token"`, and required on `/register/{client_id}` operations.

### `internal/oauthproxy/management.go`

Wraps the Zitadel Management API client. Uses a **service account PAT** (Personal Access Token) loaded from secrets at startup. Exposes:

```go
type Management interface {
    CreateOrganization(ctx, name string) (orgID string, err error)
    DisableOrganization(ctx, orgID string) error
    EnableOrganization(ctx, orgID string) error

    CreateOIDCApp(ctx, orgID string, req CreateAppReq) (CreateAppResp, error)
    UpdateOIDCApp(ctx, orgID, appID string, req UpdateAppReq) (UpdateAppResp, error)
    DeleteOIDCApp(ctx, orgID, appID string) error

    CreateHumanUser(ctx, orgID string, req CreateUserReq) (CreateUserResp, error)
    DisableUser(ctx, orgID, userID string) error
    GrantProjectRole(ctx, orgID, userID, role string) error
}
```

This is reused by:

- Tenant creation CLI ([Phase 4](phase-04-tenant-auth-session.md)).
- DCR proxy (this phase).
- Portal admin RPCs ([Phase 9](phase-09-portal-spa.md)) for invite/disable/role-change.

### Routes mounted in `internal/transport/http.go`

```
/t/{tenant}/oauth/.well-known/oauth-authorization-server   → metadata handler
/t/{tenant}/oauth/.well-known/openid-configuration         → metadata handler (alias)
/t/{tenant}/oauth/register                                 → DCR proxy
/t/{tenant}/oauth/register/{client_id}                     → DCR management proxy
/t/{tenant}/oauth/{authorize,token,userinfo,jwks,revoke,introspect,end_session}  → redirector
```

All behind `RequireTenant`.

### Package layout

```
internal/oauthproxy/
├── metadata.go       // /.well-known/* → static rewritten Zitadel metadata
├── redirector.go     // /authorize, /token, /userinfo, /jwks, /revoke, /introspect, /end_session
├── dcr.go            // /register and /register/{client_id}
└── management.go     // Zitadel Management API client wrapper
```

## Deliverables

- New files listed above under `internal/oauthproxy/`.
- New model `ZitadelApp` in [Phase 1's](phase-01-database-foundation.md) `internal/storage/models.go`. (See the model-list update there.)
- `internal/config/config.go` additions: `oauth_proxy.dcr_enabled`, `oauth_proxy.dcr_initial_access_token`, `oauth_proxy.zitadel_management_pat`, `oauth_proxy.zitadel_project_id`.
- Updated `internal/transport/http.go` to mount the routes.

## Security & operational notes

- **Service account PAT** is the most sensitive credential after the encryption key. Stored in the configured secret store; never in `config.yaml` literally. Rotate periodically (Zitadel supports multiple PATs per service user).
- **PAT scope** must be limited to the Limen project: grant only the org/user/app management permissions actually used. Audit via Zitadel's admin console.
- **DCR rate limiting** at the proxy is recommended — an unauthenticated `/register` is otherwise an abuse vector. A simple per-tenant token-bucket suffices (10 reg/min).
- **`registration_access_token`** is generated by Limen with `crypto/rand` (32 bytes), stored hashed (SHA-256) + encrypted, and constant-time-compared on management requests.
- **PKCE S256 mandatory** on the Zitadel app — Limen configures every DCR'd app with PKCE required.
- **Redirect URIs validated** as absolute URIs with allowed schemes (`https`, plus `http://localhost*` for native clients).
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

## Checklist

- [ ] `internal/oauthproxy/metadata.go` serves a static metadata document with `registration_endpoint` rewritten to Limen
- [ ] `internal/oauthproxy/redirector.go` issues 302 redirects for `authorize`, `userinfo`, `jwks`, `end_session`
- [ ] POST endpoints (`token`, `revoke`, `introspect`) use 307 redirects (or reverse-proxy as fallback)
- [ ] `internal/oauthproxy/dcr.go` accepts MCP-spec DCR requests and creates Zitadel OIDC apps via the Management API
- [ ] DCR proxy enforces `tenant.DCREnabled` and optional `dcr_initial_access_token`
- [ ] PKCE S256 required on every DCR'd app
- [ ] `redirect_uris` validated (HTTPS or localhost)
- [ ] DCR rate limit applied per tenant
- [ ] `ZitadelApp` mirror row persisted per registration, with `registration_access_token` encrypted (AAD-bound)
- [ ] RFC 7592 management endpoints (`GET/PUT/DELETE /register/{client_id}`) implemented and authenticated via the registration access token
- [ ] `internal/oauthproxy/management.go` wraps Zitadel Management API with PAT-based auth
- [ ] Config additions: `oauth_proxy.{dcr_enabled, dcr_initial_access_token, zitadel_management_pat, zitadel_project_id}`
- [ ] Routes mounted under `/t/{tenant}/oauth/*` behind `RequireTenant`
- [ ] Integration test: full inbound discovery → DCR → authorize → token → `/mcp` roundtrip against the dev Zitadel container
- [ ] Integration test: DCR with missing initial access token → 401
- [ ] Integration test: RFC 7592 GET/PUT/DELETE with valid and invalid registration access tokens
- [ ] Contract test: Zitadel app-create field mapping (covers a future API drift)

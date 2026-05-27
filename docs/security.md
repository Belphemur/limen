# Security

This document describes the security model for Limen -- its threat model, isolation guarantees, and operational safeguards.

## Threat Model

Limen sits between LLM clients and upstream MCP servers. The key attack surfaces are:

| Surface                | Risk                                                                                    | Mitigation                                                                            |
| ---------------------- | --------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------- |
| **LLM client prompts** | Malicious prompts injected into tool arguments could attempt sandbox escape             | Goja sandbox isolation -- no host access beyond injected tools                        |
| **Configuration file** | Secrets embedded in config (tokens, API keys) could be leaked                           | `${ENV_VAR}` substitution; never commit real tokens to version control                |
| **Upstream responses** | Malicious or malformed responses from upstream servers could carry payloads             | Tool responses pass through the gateway without modification; validate upstream trust |
| **Sandbox JS code**    | LLM-generated JavaScript in Code Mode could attempt resource exhaustion or exfiltration | Goja sandbox blocks filesystem, network, process spawning; execution timeout enforced |

## Goja Sandbox Guarantees

Code Mode runs agent-provided JavaScript inside a [Goja](https://github.com/dop251/goja) runtime with strict isolation:

### Blocked APIs

The following are **not available** inside the sandbox:

| Category   | Blocked APIs                                                |
| ---------- | ----------------------------------------------------------- |
| Filesystem | `os`, `fs`, `path` module equivalents                       |
| Network    | `fetch`, `XMLHttpRequest`, `http`, `net` module equivalents |
| Processes  | `child_process`, `exec`, `spawn` equivalents                |
| Modules    | `require()`, `import` -- no module resolution               |
| Go runtime | Direct access to Go heap, goroutines, or reflection         |

### Available APIs

Only the `codemode` object is explicitly injected:

- `codemode.tools()` -- enumerate upstream tools
- `codemode.call(name, args)` -- invoke a tool by name
- `codemode.toolName(args)` -- invoke tools as direct methods

Nothing else. The sandbox contains no globals beyond standard JavaScript built-ins (objects, arrays, strings, math, dates).

## Execution Timeout

To prevent infinite loops and resource exhaustion, Goja execution is interrupted via `vm.Interrupt` after a configurable timeout:

```yaml
codemode:
  timeout: 30s # JS execution limit
  memory: 64MB # JS heap cap
```

When the timeout fires, the Goja runtime raises an interrupt that immediately halts script execution. The gateway returns an error to the client -- the loop never completes.

## Tenant Isolation (Row-Level Security)

Every tenant-scoped table (`users`, `upstreams`, `upstream_strategy_configs`,
`upstream_registrations`, `upstream_links`, `upstream_tools`, `zitadel_apps`,
`tenant_settings`) carries `FORCE ROW LEVEL SECURITY` with a single policy:

```sql
USING      (tenant_id = current_setting('app.current_tenant', true)::bigint)
WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::bigint)
```

The GUC is set once per transaction by `storage.Session(ctx)`:

```go
SELECT set_config('app.current_tenant', <tenantID>, true);
```

### What this means for application code

| Path                           | Pool                        | Tenant pin?       | Must filter by `tenant_id` in `WHERE`?                                            |
| ------------------------------ | --------------------------- | ----------------- | --------------------------------------------------------------------------------- |
| `Session(WithTenant(ctx, id))` | `limen_app`                 | yes (`SET LOCAL`) | **No.** RLS rewrites every query.                                                 |
| `Session(WithSuperuser(ctx))`  | `limen_admin` (`BYPASSRLS`) | none              | **Yes — required.** Without an explicit predicate the query touches every tenant. |
| `Session(ctx)` with no marker  | —                           | —                 | Returns `ErrNoTenant`; fail-closed.                                               |

The default app role `limen_app` has **no `BYPASSRLS`** privilege. An
unset GUC makes the policy predicate `tenant_id = NULL::bigint` (the
`true` second arg to `current_setting` is the NULL-safe form), which
matches zero rows — so a forgotten tenant pin fails closed.

### Rule of thumb

- **Never** sprinkle `WHERE tenant_id = ?` into queries that run under
  `WithTenant(ctx, …)`. It's redundant with RLS, gives a false sense of
  safety (the GUC is the real fence), and obscures the few places that
  legitimately bypass RLS.
- **Always** include `tenant_id` (and the right column predicates) on
  queries that run under `WithSuperuser(ctx)`. The bypass is purely a
  transactional convenience for cross-tenant work; without an explicit
  filter you will read or wipe other tenants' rows.

The canonical superuser users in tree today are:

- `internal/tenant/service.go` — `Delete()` cascade across every
  tenant-owned table.
- `internal/transport/portal.go` — `upsertPortalUser()` during the OIDC
  callback (the tenant pin from the URL has been validated, but the
  callback runs before the user-row exists, so we route through the
  admin pool by convention).
- `internal/tenancy/resolver.go` — `Resolve()` / `ResolveByZitadelOrg()`
  read the non-RLS `tenants` table.
- `internal/cli/create_tenant.go` — bootstrap.
- `internal/upstream/health.go`, `internal/upstream/mcpspec/refresh.go`
  — cross-tenant background refreshers.

Inserts on RLS-scoped tables must still populate `tenant_id` on the row
itself — the `WITH CHECK` clause rejects mismatched or `NULL` values.

### What is NOT RLS-protected

The `tenants` table is intentionally **not** under RLS — the resolver
needs to look up a tenant by `public_id` before any tenant is bound to
ctx. Queries against `tenants` always run on the admin pool and key off
`id` or `public_id`, never `tenant_id`.

## Authentication

Limen validates inbound MCP requests as a standards-compliant OAuth 2.0
Resource Server. Every request to `/t/{tenant}/mcp/*` (except the public
PRM document) must carry a Bearer access token issued by the configured
Zitadel issuer.

### Validation pipeline

1. Client sends request with `Authorization: Bearer <token>`.
2. Gateway extracts the JWT and validates `iss`, signature (RS256 only),
   `exp`, and `nbf` against the issuer's JWKS (fetched and cached via
   OIDC discovery at startup).
3. Gateway verifies the `aud` claim contains the configured
   `zitadel.mcp_resource_audience`.
4. Gateway enforces tenant binding: the
   `urn:zitadel:iam:user:resourceowner:id` claim must equal the URL
   tenant's `zitadel_org_id` — cross-tenant tokens get a 403.
5. Gateway resolves the local `users` row by `(tenant_id, zitadel_subject)`;
   unprovisioned users get a 401 (no auto-provision on the RS path).
6. On every failure the response carries an RFC 9728-compliant
   `WWW-Authenticate: Bearer realm="mcp", resource_metadata="…"` header
   pointing at `/t/{tenant}/mcp/.well-known/oauth-protected-resource`.

### Configuration

```yaml
oidc:
  issuer: "https://auth.example.com"
zitadel:
  mcp_resource_audience: "my-zitadel-project-id"
```

## Session Cookie Security

Limen stores OIDC tokens in per-tenant session cookies. Each cookie is
defense-in-depth layered:

1. **Binary packed** — the `{idToken, refreshToken, accessToken, expiresAt}`
   quadruple is packed with `uint16` LE length-prefixed strings and an
   `int64` LE timestamp (14 bytes framing overhead, eliminates JSON
   field-name bloat).

2. **zstd compressed** — packed bytes are compressed (SpeedFastest) to
   reduce JWT base64 text by ~50%, keeping the final cookie under the
   browser 4KB limit even with 2-3KB access tokens.

3. **AES-SIV encrypted** — compressed bytes are encrypted with AES-SIV
   (RFC 5297) using tenant-scoped AAD (`{TenantID, Kind}`). Cross-tenant
   replay fails cryptographically on AAD mismatch before any token is
   inspected.

Final cookie shape: `base64(aes_siv(zstd(binary_pack(tokens))))`.

### Cookie attributes

| Cookie                     | Path          | Kind AAD          | Purpose                                                    |
| -------------------------- | ------------- | ----------------- | ---------------------------------------------------------- |
| `limen_portal`               | `/t/{tenant}`   | `portal.oidc.tokens` | Browser login session (OIDC code flow)                       |
| `limen_portal_impersonate`   | `/t/{tenant}`   | `portal.impersonate` | Admin-impersonating-service-account session (Token Exchange) |
| `limen_state`                | `/auth/callback` | N/A (HMAC-SHA256)   | OIDC state parameter (anti-CSRF)                             |

All token-carrying cookies use `HttpOnly; Secure; SameSite=Lax` (no
`Domain`, no `SameSite=None`). The `Path=/t/{tenant}` scope provides
physical cross-tenant isolation: a browser carrying tenant A's cookie
cannot physically send it on a request to tenant B.

## Service Account Impersonation Security

Admins and owners can impersonate service accounts via Zitadel's RFC 8693
Token Exchange to configure upstream MCP connections on behalf of the
machine identity. For the full flow, role table, and token exchange app
details, see the dedicated [Impersonation](impersonation.md) document.
This section summarizes the security layers that protect the flow.

### Defense layers

1. **Zitadel instance policy.** Impersonation is disabled by default at
   the Zitadel instance level. The bootstrap script idempotently enables
   it via `SetSecuritySettings{EnableImpersonation: true}` at startup.

2. **Zitadel org-level role gating.** The actor MUST hold the appropriate
   Zitadel org membership role (see [Impersonation: Zitadel Roles](impersonation.md#zitadel-roles)
   for the canonical role table): owners receive `ORG_ADMIN_IMPERSONATOR`
   (plus `ORG_OWNER_VIEWER`, `ORG_SETTINGS_MANAGER`, `ORG_USER_MANAGER`)
   which can impersonate any member in the org, while admins receive
   `ORG_END_USER_IMPERSONATOR` (plus `ORG_USER_MANAGER`) which can
   impersonate end users and service accounts but not other admins.
   Roles are automatically granted on promotion and removed on
   demotion (see [Member Management](phases/phase-04-tenant-auth-session.md)
   — role sync). The bootstrap seed owner receives
   `ORG_ADMIN_IMPERSONATOR` at provisioning time.

3. **Zitadel project-level role gating (Limen side).** The
   `RoleInterceptor` requires at least `RoleAdmin` to call
   `ImpersonateServiceAccount`. The handler additionally rejects
   impersonation of service accounts that do not belong to the requesting
   tenant (`sa.TenantID != t.ID` — defense-in-depth on top of RLS).

4. **Within-tenant only.** Token exchange is scoped to the same Zitadel
   organization. An admin in tenant A cannot exchange for a service
   account in tenant B. The `subject_token` is the service account's
   Zitadel user ID, and Zitadel verifies both identities belong to the
   same org before issuing the exchanged token.

5. **Confidential client authentication.** The token exchange request to
   Zitadel uses HTTP Basic Auth (`client_id` + `client_secret`) with a
   dedicated confidential OIDC application ("Limen Token Exchange").
   This app has `AuthMethodType: BASIC` and `GrantTypes: [TOKEN_EXCHANGE]`
   — it is separate from the Portal PKCE app that browsers use.
   Unauthenticated token exchange requests are rejected. See
   [Impersonation](impersonation.md#token-exchange-app) for details.

6. **Audit trail.** Every action taken during an impersonation session
   carries the impersonating admin's user ID in the `on_behalf_of_user_id`
   column of the audit event (see [Audit](audit.md)). The `act` claim on
   the Zitadel-issued ID token provides a server-side audit record.

7. **Cookie isolation.** Impersonation sessions use a dedicated cookie
   (`limen_portal_impersonate`, AAD Kind `"portal.impersonate"`) that is
   cryptographically isolated from the normal portal cookie (AAD Kind
   `"portal.oidc.tokens"`). An impersonation cookie cannot be replayed as
   a normal login session and vice versa. See [Session Cookie
   Security](#session-cookie-security) for the full cookie format.

8. **Exit is self-serve.** `ExitImpersonation` clears the impersonation
   cookie immediately. The original portal cookie is untouched — the admin
   returns to their own session. The `RoleInterceptor` allows
   `ExitImpersonation` at `RoleMember`, so any authenticated user can
   exit an impersonation session (even if their admin session expired
   while impersonating).

### What impersonation grants

| Capability                                            | Yes / No |
| ----------------------------------------------------- | -------- |
| Create/configure upstream MCP connections             | Yes      |
| Set upstream API keys and OAuth tokens                | Yes      |
| View portal dashboard as the service account          | Yes      |
| Create, delete, or regenerate service accounts        | No       |
| Invite, update, or remove human members               | No       |
| Change tenant settings or IDE presets                 | No       |
| Delete the tenant                                     | No       |
| Exit the impersonation session                        | Yes      |

The impersonation session is scoped to `RoleMember` (the service
account's own role, capped at admin). The admin's own higher role is NOT
propagated — the impersonation is a "drop-privilege" operation.

## Tenant ↔ Zitadel org binding

Limen is multi-tenant. Every tenant has its own URL prefix (`/t/{tenant}/...`)
and is mirrored 1:1 to a Zitadel **organization**. Two questions drive the
design:

1. _How does Limen know which tenant a request belongs to?_ — from the
   `{tenant}` (the tenant's `PublicID`, a `tnt_<ULID>`) in the URL,
   resolved through the `tenants` table.
2. _How does Limen prove the logged-in user is allowed in that tenant?_ —
   by checking that the user's **home org in Zitadel** matches the
   tenant's stored `zitadel_org_id`.

The second check is the whole point of this section. Without it, anyone
who can log into Zitadel could hit `/t/<any-tenant>/portal/` regardless of
which org owns them.

### The trust chain

```
URL tenant id (PublicID) ──▶ tenants.zitadel_org_id (Limen DB)
                       │
                       ▼  must equal
ID token ──▶ urn:zitadel:iam:user:resourceowner:id  (Zitadel)
```

- `tenants.zitadel_org_id` is set at tenant-creation time by
  `limen create-tenant` (either by creating a new Zitadel org or by
  binding to an existing one via `--zitadel-org-id`).
- `urn:zitadel:iam:user:resourceowner:id` is a Zitadel-specific claim
  carrying the **resource owner** (org id) of the authenticated user.
  It is **only emitted when the OIDC scope
  `urn:zitadel:iam:user:resourceowner` is requested**.

### Why a scope and not project roles?

You could imagine using the `urn:zitadel:iam:org:project:roles` claim
(project-role assertion) to derive the user's org. That claim has two
problems for this check:

- It only lists orgs through which the user has been **granted a project
  role**. A user who is a member of an org but has not been granted a
  Limen project role would have no entry.
- It is a multi-valued map: a user can hold roles across several orgs
  through project grants. There is no single canonical "this user
  belongs to org X" answer.

`urn:zitadel:iam:user:resourceowner:id` is single-valued and reflects the
user's **home org** — the org that owns the user record. That is exactly
the binding we want: a `staff@limen.dev` user owned by the `limen-staff`
org cannot impersonate a user inside the `acme` org just by holding a
project role there.

### How the check runs

[internal/auth/oidc.go](../internal/auth/oidc.go) `CallbackHandler`:

```go
gotOrgID, _ := tokens.IDTokenClaims.Claims["urn:zitadel:iam:user:resourceowner:id"].(string)
if gotOrgID != wantOrgID {
    http.Error(w, "access denied", http.StatusForbidden)
    return
}
```

If the user's home org doesn't match `tenants.zitadel_org_id` for the
tenant in the URL, the callback returns 403 _before_ a session cookie is
ever issued. The user must restart the flow against a tenant they
actually belong to.

### What to configure

The default OIDC scopes in `internal/config/config.go` already include
`urn:zitadel:iam:user:resourceowner`. If you override `oidc.scopes` in
`config.yaml`, you **must** keep that scope, or every login will fail
with `"org mismatch" want=<id> got=""` in the logs and `access denied`
in the browser.

### What this does NOT cover

The org binding only proves _home org membership_. It does not yet
enforce:

- **Project-role-based authorisation** — gating individual portal pages
  or API endpoints by `member` / `admin` / `owner` / `super_admin`.
  That comes in Phase 6 (Resource Server) where the access token's
  `urn:zitadel:iam:org:project:roles` claim is validated per request.
- **Cross-org switching** — a single user belonging to two orgs cannot
  switch tenants in the same session; they log out and back in.

## Secret Management

Secrets in the configuration file use environment variable substitution to avoid committing tokens:

```yaml
upstreams:
  - name: github
    url: https://api.example.com/mcp
    headers:
      Authorization: "Bearer ${GITHUB_TOKEN}"

  - name: jira
    url: https://jira.example.com/mcp
    headers:
      Authorization: "Bearer ${JIRA_API_KEY}"
```

At startup, Limen replaces `${VAR_NAME}` patterns with values from the process environment:

```bash
export GITHUB_TOKEN="ghp_xxxx"
export JIRA_API_KEY="jira_xxxx"
./limen -config config.yaml
```

### Rules

- **Never** commit real tokens or API keys to the repository
- Use `.gitignore` to exclude local config files with embedded secrets
- Use `.env` files for local development (not committed)
- Rotating tokens requires a gateway restart

### Upstream credentials (strategy config + per-user links)

Upstream credentials are **not** read from YAML. They are stored per-tenant
in Postgres as encrypted blobs and never appear in plaintext anywhere on
disk:

| Table                       | Column        | AAD `(tenant, user, kind)`              | Carries                                                                                       |
| --------------------------- | ------------- | --------------------------------------- | --------------------------------------------------------------------------------------------- |
| `upstream_strategy_configs` | `config_json` | `(tenant, ∅, upstream.strategy_config)` | `static_header` shared secret + header template; `mcp_spec` static client id/secret/endpoints |
| `upstream_links`            | `tokens_json` | `(tenant, user, upstream.tokens)`       | `mcp_spec` user access/refresh tokens                                                         |
| `upstream_links`            | `extra_json`  | `(tenant, user, upstream.extra)`        | `static_header` per-user override secret (when `allow_user_override = true`)                  |

All three use AES-SIV (RFC 5297) via `crypto.SecretField`; the AAD binds
the ciphertext to the tenant (and user, where applicable), so a row stolen
from one tenant cannot be decrypted under another tenant's id even with
the key. See [internal/crypto/secret_field.go](../internal/crypto/secret_field.go).

For `static_header` specifically: the shared secret travels **inside** the
strategy-config JSON blob — not as a separate column — so a single AAD-bound
decryption gates access to every field (header name, template, shared
secret, override flag) at once. The `allow_user_override` flag itself is
not a secret and only controls whether `extra_json` is consulted at
request time.

## Remote Upstreams Only

Limen supports **only remote MCP servers** via the Streamable HTTP transport. Local process execution (stdio transport) is intentionally disabled:

- Eliminates local supply chain risk from arbitrary MCP server binaries
- Forces network-level isolation between Limen and upstream servers
- Enables centralized authentication and monitoring

## What Code Mode CANNOT Do

The Goja sandbox is intentionally narrow. Agent-generated JavaScript in Code Mode:

- **Cannot** read or write files on disk
- **Cannot** make HTTP requests or open network connections
- **Cannot** spawn subprocesses or execute shell commands
- **Cannot** access the Go runtime, memory, or goroutines
- **Cannot** persist state between invocations
- **Cannot** require or import external modules
- **Cannot** exceed configured memory or timeout limits

Code Mode exists solely to orchestrate tool calls across upstream MCP servers. Any computation beyond that must happen in the LLM's context.

## Security Checklist

Before deploying Limen to production:

- [ ] Implement JWKS token validation in `auth/middleware.go`
- [ ] Set strong execution timeout (30s or less)
- [ ] Set memory limit appropriate for workload (64MB default)
- [ ] Rotate all tokens used in `${ENV_VAR}` substitutions
- [ ] Verify no secrets in configuration files under version control
- [ ] Audit upstream server trustworthiness (remote-only means you depend on their security)
- [ ] Review planned DLP scanning for tool response inspection (roadmap item)

# Security

This document describes the security model for Limen -- its threat model, isolation guarantees, and operational safeguards.

## Threat Model

Limen sits between LLM clients and upstream MCP servers. The key attack surfaces are:

| Surface | Risk | Mitigation |
|---------|------|------------|
| **LLM client prompts** | Malicious prompts injected into tool arguments could attempt sandbox escape | Goja sandbox isolation -- no host access beyond injected tools |
| **Configuration file** | Secrets embedded in config (tokens, API keys) could be leaked | `${ENV_VAR}` substitution; never commit real tokens to version control |
| **Upstream responses** | Malicious or malformed responses from upstream servers could carry payloads | Tool responses pass through the gateway without modification; validate upstream trust |
| **Sandbox JS code** | LLM-generated JavaScript in Code Mode could attempt resource exhaustion or exfiltration | Goja sandbox blocks filesystem, network, process spawning; execution timeout enforced |

## Goja Sandbox Guarantees

Code Mode runs agent-provided JavaScript inside a [Goja](https://github.com/dop251/goja) runtime with strict isolation:

### Blocked APIs

The following are **not available** inside the sandbox:

| Category | Blocked APIs |
|----------|-------------|
| Filesystem | `os`, `fs`, `path` module equivalents |
| Network | `fetch`, `XMLHttpRequest`, `http`, `net` module equivalents |
| Processes | `child_process`, `exec`, `spawn` equivalents |
| Modules | `require()`, `import` -- no module resolution |
| Go runtime | Direct access to Go heap, goroutines, or reflection |

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
  timeout: 30s    # JS execution limit
  memory: 64MB    # JS heap cap
```

When the timeout fires, the Goja runtime raises an interrupt that immediately halts script execution. The gateway returns an error to the client -- the loop never completes.

## Authentication

Limen validates client requests through JWT Bearer token authentication:

### Current State

The auth middleware in `auth/middleware.go` extracts and validates Bearer tokens from the `Authorization` header. **JWKS (JSON Web Key Set) validation is currently stubbed** and needs real implementation before production use.

### Planned: JWKS Validation

The intended flow:

1. Client sends request with `Authorization: Bearer <token>`
2. Gateway extracts the JWT
3. Gateway fetches the public key from the configured JWKS endpoint
4. Gateway validates the token signature, expiry, and claims
5. Request proceeds only if validation succeeds

### Configuration

```yaml
auth:
  jwks_url: "https://auth.example.com/.well-known/jwks.json"
  required_claims:
    - "aud"
    - "sub"
```

## Tenant ↔ Zitadel org binding

Limen is multi-tenant. Every tenant has its own URL prefix (`/t/{slug}/...`)
and is mirrored 1:1 to a Zitadel **organization**. Two questions drive the
design:

1. *How does Limen know which tenant a request belongs to?* — from the
   `{slug}` in the URL, resolved through the `tenants` table.
2. *How does Limen prove the logged-in user is allowed in that tenant?* —
   by checking that the user's **home org in Zitadel** matches the
   tenant's stored `zitadel_org_id`.

The second check is the whole point of this section. Without it, anyone
who can log into Zitadel could hit `/t/<any-slug>/portal/` regardless of
which org owns them.

### The trust chain

```
URL slug ──▶ tenants.zitadel_org_id (Limen DB)
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
slug in the URL, the callback returns 403 *before* a session cookie is
ever issued. The user must restart the flow against a tenant they
actually belong to.

### What to configure

The default OIDC scopes in `internal/config/config.go` already include
`urn:zitadel:iam:user:resourceowner`. If you override `oidc.scopes` in
`config.yaml`, you **must** keep that scope, or every login will fail
with `"org mismatch" want=<id> got=""` in the logs and `access denied`
in the browser.

### What this does NOT cover

The org binding only proves *home org membership*. It does not yet
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

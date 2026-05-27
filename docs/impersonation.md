# Impersonation

This document describes how Limen's service account impersonation works — end to end, from the admin click to the cryptographic cookie isolation.

## Overview

Impersonation lets a tenant owner or admin temporarily act as a service account to configure upstream MCP connections on behalf of that machine identity. Instead of sharing the service account's PAT with a human, the admin exchanges their own Zitadel access token for an impersonated token via RFC 8693 Token Exchange, then operates the portal UI and admin RPCs as the service account.

Once the exchange succeeds, all subsequent requests in that browser tab carry the service account's identity. The admin returns to their own session by clicking "Exit Impersonation", which clears the impersonation cookie without touching the original session.

## Architecture

Impersonation relies on two cooperating mechanisms:

1. **RFC 8693 Token Exchange (Zitadel)** — the admin's access token is sent as `actor_token` to Zitadel's `/oauth/v2/token` endpoint alongside the service account's user ID as `subject_token`. Zitadel validates that the actor holds the appropriate `ORG_*_IMPERSONATOR` role and returns an impersonated token set (access, ID, and refresh tokens) on behalf of the service account.

2. **Cookie-based session (Limen)** — the exchanged tokens are encrypted and stored in a `limen_portal_impersonate` cookie, keyed to the AAD kind `"portal.impersonate"`. The session interceptor (`internal/session/interceptor.go`) attempts to decrypt the impersonation cookie before the normal portal cookie. If it succeeds, the resolved identity is the service account; all downstream code operates under that identity.

## Zitadel Roles

The org-level roles that enable impersonation are granted automatically during role promotion (see `internal/zitadel/roles.go::OrgRolesForLimenRole`).

| Limen Role | Zitadel Org Roles Granted                                                                     |
| ---------- | --------------------------------------------------------------------------------------------- |
| Owner      | `ORG_OWNER_VIEWER`, `ORG_SETTINGS_MANAGER`, `ORG_USER_MANAGER`, `ORG_ADMIN_IMPERSONATOR`      |
| Admin      | `ORG_END_USER_IMPERSONATOR`, `ORG_USER_MANAGER`                                                |
| Member     | None                                                                                          |

The canonical definition is `internal/zitadel/roles.go` — that is the single source of truth for which Zitadel org roles each Limen project role receives.

### Role semantics

- `ORG_ADMIN_IMPERSONATOR` — the holder can impersonate any identity in the organization (owners only).
- `ORG_END_USER_IMPERSONATOR` — the holder can impersonate end users and service accounts (admins only), but not other admins.
- `ORG_USER_MANAGER` — needed to grant/revoke project roles during member and service account lifecycle.

Members receive no org-level roles, so they cannot initiate token exchange against Zitadel.

## Token Exchange App

The bootstrap script creates a dedicated confidential OIDC application named **"Limen Token Exchange"** during provisioning. This app exists solely for RFC 8693 token exchange — it is separate from the Portal PKCE app that browsers use.

| Property                      | Value                                                    |
| ----------------------------- | -------------------------------------------------------- |
| `AuthMethodType`              | `BASIC` (`client_secret_basic`) — confidential client    |
| `GrantTypes`                  | `[TOKEN_EXCHANGE]` — single-purpose                      |
| `AccessTokenType`             | `JWT`                                                    |
| `AccessTokenRoleAssertion`    | `true` — roles are included in exchanged tokens          |
| `DevelopmentMode`             | `true`                                                   |

The app's credentials are consumed at runtime from two environment variables:

| Variable                             | Purpose                                      |
| ------------------------------------ | -------------------------------------------- |
| `LIMEN_OIDC_TOKEN_EXCHANGE_CLIENT_ID`      | Client ID of the token exchange application    |
| `LIMEN_OIDC_TOKEN_EXCHANGE_CLIENT_SECRET`  | Client secret (HTTP Basic Auth)              |

These map to `OIDCConfig.TokenExchangeClientID` and `OIDCConfig.TokenExchangeClientSecret` in `internal/config/config.go`. The bootstrap outputs these values to `.bootstrap-out.env` after creating the app.

> **Important:** If the token exchange app already exists when the bootstrap re-runs, the secret is *not* returned (Zitadel behavior). Rotate manually in the Zitadel Console if needed.

## Flow

The step-by-step flow from the admin's click to session restoration:

1. **Admin clicks "Impersonate"** on a service account in the portal UI.

2. **Portal calls `ImpersonateServiceAccount` RPC** — the `RoleInterceptor` enforces `RoleAdmin` as the minimum role. Unknown procedures default-deny (see `internal/admin/roles.go`).

3. **Admin service validates the SA belongs to the same tenant** — defense-in-depth check on top of RLS. The handler rejects cross-tenant attempts (`sa.TenantID != t.ID`). See `internal/admin/service_accounts.go`.

4. **Admin service extracts the admin's access token** from the session context via `session.AccessTokenFromContext(ctx)`.

5. **HTTP POST to Zitadel `/oauth/v2/token`** with the following form parameters:
   - `grant_type=urn:ietf:params:oauth:grant-type:token-exchange`
   - `subject_token=<SA's Zitadel user ID>`
   - `subject_token_type=urn:zitadel:params:oauth:token-type:user_id`
   - `actor_token=<admin's access token>`
   - `actor_token_type=urn:ietf:params:oauth:token-type:access_token`
   - `requested_token_type=urn:ietf:params:oauth:token-type:jwt`
   - `scope=openid profile email offline_access urn:zitadel:iam:org:project:id:<projectID>:aud`

   The request is authenticated via HTTP Basic Auth using the token exchange app's client ID and secret.

6. **Zitadel validates the request** — it checks the admin's access token is valid, confirms the admin holds the appropriate `ORG_*_IMPERSONATOR` role in the target org, and issues impersonated tokens (access, ID, refresh).

7. **Limen encrypts the exchanged tokens** into the `limen_portal_impersonate` cookie using the standard cookie pipeline (binary pack → zstd compress → AES-SIV encrypt with AAD `{TenantID, "portal.impersonate"}`). The cookie is returned as a `Set-Cookie` header.

8. **Session interceptor detects the impersonation cookie** on subsequent requests. `internal/session/interceptor.go` attempts `impersonationResolve` before the normal `resolve`. If the impersonation cookie decrypts successfully, the service account's identity is used.

9. **All subsequent requests operate as the service account** — the portal UI, admin RPCs, and upstream OAuth configurations all see the SA's identity. The admin's role is *not* propagated; the impersonated session is scoped to the SA's own role (capped at `RoleMember`).

10. **"Exit Impersonation" clears the cookie** — the `ExitImpersonation` RPC (accessible at `RoleMember`) returns a `Set-Cookie` header with `MaxAge=-1` for the impersonation cookie. The original `limen_portal` cookie is untouched — the admin returns to their own session.

## Defense Layers

Impersonation is protected by eight independent security layers:

1. **Instance-level impersonation policy.** Impersonation is disabled by default in Zitadel. The bootstrap enables it idempotently via `SetSecuritySettings{EnableImpersonation: true}`. Without this, every token exchange fails.

2. **Org-level role gating.** The actor must hold the appropriate Zitadel org membership role (`ORG_ADMIN_IMPERSONATOR` for owners, `ORG_END_USER_IMPERSONATOR` for admins). These are automatically granted on promotion and revoked on demotion via Zitadel's native role system.

3. **Project-level role gating (Limen side).** The `RoleInterceptor` requires at least `RoleAdmin` to call `ImpersonateServiceAccount`. The role lookup is a server-side database query against the `users` table — not trusted from the client.

4. **Within-tenant only.** The service account must belong to the same tenant as the requesting admin. The handler performs an explicit `sa.TenantID != t.ID` check, in addition to RLS enforcement. Token exchange is scoped to the same Zitadel org — Zitadel verifies both identities belong to the same org before issuing tokens.

5. **Confidential client authentication.** The token exchange request to Zitadel uses HTTP Basic Auth with a dedicated confidential OIDC application (`"Limen Token Exchange"`). The app's `AuthMethodType` is `BASIC` (`client_secret_basic`), not PKCE. This keeps the exchange server-to-server only.

6. **Audit trail.** Every action taken during an impersonation session carries the impersonating admin's user ID in the `on_behalf_of_user_id` column of audit events. Zitadel's `act` claim on the issued ID token provides a server-side record of who initiated the exchange.

7. **Cookie isolation.** Impersonation sessions use a dedicated cookie (`limen_portal_impersonate`, AAD kind `"portal.impersonate"`) that is cryptographically isolated from the normal portal cookie (`limen_portal`, AAD kind `"portal.oidc.tokens"`). An impersonation cookie cannot be replayed as a normal login session and vice versa — AES-SIV AAD mismatch fails before any token is decrypted.

8. **Self-serve exit.** `ExitImpersonation` is accessible at `RoleMember`, so any authenticated user can exit an impersonation session — even if the admin session expired while impersonating. The impersonation cookie is cleared immediately; the original portal cookie remains untouched.

## What Impersonation Grants

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

The impersonation session is scoped to the service account's own role. The admin's higher privileges are *not* propagated — impersonation is a "drop-privilege" operation.

## Limitations

- **Impersonated tokens cannot call the Zitadel API** — Zitadel restricts impersonated tokens from accessing its admin APIs. The exchanged tokens are intended for downstream resource servers (in Limen's case, the MCP gateway).

- **Service accounts cannot impersonate other identities** — service accounts lack the actor access token required for token exchange. They have no browser session to carry one.

- **New OAuth handshakes are disabled during impersonation** — while impersonating, the portal operates as the service account. Initiating a new upstream OAuth flow would attempt to link the SA's identity to the upstream, which may not be the intended outcome.

- **Impersonation is "drop-privilege"** — the admin operates *as* the service account, not *with elevated privileges over* it. The admin can do everything the SA can do, but not everything the admin can do in their own session.

## Configuration

### Required environment variables

| Variable                             | Description                                      |
| ------------------------------------ | ------------------------------------------------ |
| `LIMEN_OIDC_TOKEN_EXCHANGE_CLIENT_ID`      | Client ID of the token exchange OIDC application |
| `LIMEN_OIDC_TOKEN_EXCHANGE_CLIENT_SECRET`  | Client secret for HTTP Basic Auth              |

### YAML configuration (optional, if not using env vars)

```yaml
oidc:
  token_exchange_client_id: "${LIMEN_OIDC_TOKEN_EXCHANGE_CLIENT_ID}"
  token_exchange_client_secret: "${LIMEN_OIDC_TOKEN_EXCHANGE_CLIENT_SECRET}"
```

### Zitadel prerequisites

The Zitadel instance must have impersonation enabled. The bootstrap handles this automatically via `SetSecuritySettings{EnableImpersonation: true}`, but in production environments provisioned by Terraform (Phase 11), ensure the instance security setting `EnableImpersonation` is `true`. Without it, every token exchange returns `"Impersonation.PolicyDisabled"`.

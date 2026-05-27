---
phase: "9i"
title: "Service Accounts & API Tokens"
status: planned
progress: 0
depends_on: ["5", "9c", "9d"]
updated: "2026-05-10"
---

# Phase 9i — Service Accounts & API Tokens

> **Depends on**: Phase 5 (Zitadel integration), Phase 9c (tenant admin SPA), Phase 9d (shared session service)
> **Unblocks**: programmatic MCP gateway access without browser OAuth; cloud agent setup flow

## Goal

Allow tenant admins and owners to create Zitadel-backed service accounts (machine users) and obtain a
one-time PAT (Personal Access Token) usable as `Authorization: Bearer <pat>` for both Connect-RPC
and MCP endpoints, bypassing the browser OAuth flow. Admins/owners can impersonate service accounts
via Zitadel Token Exchange (RFC 8693) to configure their upstream MCP connections in the portal.
Update the admin onboarding to guide new tenants to create a service account as their first setup step.

## Background

Phase 4 ships the OIDC RP for browser users; Phase 6 ships the MCP Resource Server with JWT Bearer
auth. Both flows require a human user with a Zitadel account. For cloud agents, CI/CD pipelines, and
CLI tools, a machine identity is needed — one that can hold a long-lived API token and authenticate
directly without a browser redirect.

Zitadel v2 calls these "machine users" and supports PAT issuance via `AddPersonalAccessToken`.
The returned PAT is a JWT Bearer token that passes Zitadel's JWKS verification, the same mechanism
used by the MCP Resource Server. This means service accounts authenticate through the exact same
JWT validation path as human MCP users — no separate auth subsystem.

Service accounts cannot log in through the browser (no OIDC flow). To let admins configure upstream
connections on behalf of a service account, Zitadel supports **Token Exchange** (RFC 8693): the admin
exchanges their own access token for an ID token scoped to the service account. Zitadel manages the
token lifetime and adds an `act` claim for the audit trail. Limen stores this ID token in a dedicated
`limen_portal_impersonate` cookie (same binary-packed + zstd-compressed + AAD encryption format as the portal cookie, different Kind `"portal.impersonate"`) and the
session middleware checks it before the regular portal cookie.

## Sub-phases

### 9i-a: Foundation — Zitadel wrappers & storage model

- [ ] Add `PrefixServiceAccount = "sa"` to `internal/ids/prefixes.go`
- [ ] Create `internal/storage/model_service_account.go` with `ServiceAccount` struct:
  ```go
  type ServiceAccount struct {
      Base
      TenantID      int64  `gorm:"not null;index;uniqueIndex:idx_sa_tenant_zitadel,where:deleted_at IS NULL"`
      Name          string `gorm:"type:text;not null"`
      Description   string `gorm:"type:text"`
      ZitadelUserID string `gorm:"type:text;not null;uniqueIndex:idx_sa_tenant_zitadel,where:deleted_at IS NULL"`
      CreatedByID   int64  `gorm:"not null;index"`
      Role          string `gorm:"type:text;not null"` // "member" or "admin"
      Tenant        *Tenant `gorm:"foreignKey:TenantID;constraint:OnDelete:CASCADE"`
  }
  ```
  With `BeforeCreate` hook that sets public ID via `ids.New(ids.PrefixServiceAccount)`.
- [ ] Create goose migration `migrations/postgres/00004_service_accounts.sql`:
  - `CREATE TABLE service_accounts` with `set_updated_at` trigger + RLS policy scoped to `app.current_tenant`
  - `ALTER TABLE upstream_links ADD COLUMN service_account_id BIGINT`
  - Add CHECK constraint: `((user_id IS NULL) <> (service_account_id IS NULL))` — XOR, exactly one owner type
  - Add FK: `FOREIGN KEY (service_account_id) REFERENCES service_accounts(id) ON DELETE CASCADE`
  - Replace `idx_link_tenant_user_upstream` with two partial unique indexes covering `user_id` and `service_account_id` variants
  - No data migration needed: all existing rows have `user_id` populated (service accounts don't exist yet); CHECK constraint `(user_id IS NULL) <> (service_account_id IS NULL)` resolves to `(FALSE) <> (TRUE)` = `TRUE` for all existing rows
- [ ] Register `&ServiceAccount{}` in `AllModels()` in `internal/storage/models.go`
- [ ] Add `ServiceAccountID *int64` + `ServiceAccount *ServiceAccount` to `UpstreamLink` struct in `internal/storage/model_upstream.go`
- [ ] Create `internal/zitadel/service_accounts.go` with v2 wrappers:
  - `CreateMachineUser(ctx, orgID, name, description, accessTokenType) (zitadelUserID, error)`
  - `GetMachineUser(ctx, zitadelUserID) (*userV2.GetUserByIDResponse, error)`
  - `DeleteMachineUser(ctx, zitadelUserID) error`
  - `AddPersonalAccessToken(ctx, zitadelUserID, expiry *time.Time) (tokenID, token, error)`
  - `ListPersonalAccessTokens(ctx, zitadelUserID) ([]*userV2.PersonalAccessToken, error)`
  - `RemovePersonalAccessToken(ctx, zitadelUserID, tokenID) error`
- [ ] Write tests in `internal/zitadel/service_accounts_test.go`

### 9i-b: Proto & generated code

- [ ] Add to `proto/limen/admin/v1/admin.proto`:
  - `TokenType` enum: `TOKEN_TYPE_BEARER_JWT = 0`
  - `ServiceAccountRole` enum: `SERVICE_ACCOUNT_ROLE_UNSPECIFIED = 0`, `SERVICE_ACCOUNT_ROLE_MEMBER = 1`, `SERVICE_ACCOUNT_ROLE_ADMIN = 2`
  - `ServiceAccount` message: `public_id`, `name`, `description`, `role`, `created_by_id`, `created_at`
  - `CreateServiceAccountRequest`: `name`, `description`, `role` (ServiceAccountRole), `expiry_days` (uint32, 0 = no expiry)
  - `CreateServiceAccountResponse`: `service_account` (ServiceAccount), `token` (string)
  - `ListServiceAccountsRequest`: (empty)
  - `ListServiceAccountsResponse`: `service_accounts` (repeated ServiceAccount)
  - `DeleteServiceAccountRequest`: `public_id`
  - `DeleteServiceAccountResponse`: (empty)
  - `RegenerateServiceAccountTokenRequest`: `public_id`, `expiry_days`
  - `RegenerateServiceAccountTokenResponse`: `token`
  - `ImpersonateServiceAccountRequest`: `public_id`
  - `ImpersonateServiceAccountResponse`: (empty — sets cookie via header)
  - `ExitImpersonationRequest`: (empty)
  - `ExitImpersonationResponse`: (empty)
  - RPCs on `AdminService`:
    ```protobuf
    rpc CreateServiceAccount(CreateServiceAccountRequest) returns (CreateServiceAccountResponse);
    rpc ListServiceAccounts(ListServiceAccountsRequest) returns (ListServiceAccountsResponse);
    rpc DeleteServiceAccount(DeleteServiceAccountRequest) returns (DeleteServiceAccountResponse);
    rpc RegenerateServiceAccountToken(RegenerateServiceAccountTokenRequest) returns (RegenerateServiceAccountTokenResponse);
    rpc ImpersonateServiceAccount(ImpersonateServiceAccountRequest) returns (ImpersonateServiceAccountResponse);
    rpc ExitImpersonation(ExitImpersonationRequest) returns (ExitImpersonationResponse);
    ```
- [ ] Run `buf generate` to regenerate Go + Connect bindings

### 9i-c: OIDC cookie update — store access_token

- [ ] Add `AccessToken string` field to the portal cookie token data struct (`internal/auth/oidc.go` — the sealed blob that currently holds `{idToken, refreshToken, expiresAt}`)
- [ ] Update cookie `Seal`/`Unseal` to include the access_token in AAD-bound encryption
- [ ] Capture `access_token` from the OIDC callback token exchange and store it in the cookie on login + refresh
- [ ] Add `AccessTokenFromContext(ctx) (string, error)` accessor so handlers can extract it for token exchange

### 9i-d: Admin handlers

- [ ] Define `ServiceAccountDirectory` interface in `internal/admin/service_accounts.go`:
  ```go
  type ServiceAccountDirectory interface {
      CreateMachineUser(ctx context.Context, orgID, name, description string, accessTokenType userV2.AccessTokenType) (string, error)
      DeleteMachineUser(ctx context.Context, zitadelUserID string) error
      AddPersonalAccessToken(ctx context.Context, zitadelUserID string, expiry *time.Time) (string, string, error)
      ListPersonalAccessTokens(ctx context.Context, zitadelUserID string) ([]*userV2.PersonalAccessToken, error)
      RemovePersonalAccessToken(ctx context.Context, zitadelUserID, tokenID string) error
      AddUserGrant(ctx context.Context, projectID, orgID, userID string, roleKeys []string) (string, error)
      ListUserGrants(ctx context.Context, projectID, orgID string) ([]*authV2.UserGrant, error)
      RemoveUserGrant(ctx context.Context, grantID, orgID string) error
  }
  ```
- [ ] Implement `CreateServiceAccount` handler with compensation:
  1. Validate: name non-empty, role is MEMBER or ADMIN only (reject owner), default expiry 365 days when 0
  2. `CreateMachineUser` (Zitadel) → on failure return error
  3. `AddUserGrant` (Zitadel) → on failure delete machine user (compensation) then return error
  4. `AddPersonalAccessToken` (Zitadel) → on failure delete grant + delete machine user (compensation) then return error
  5. INSERT local `ServiceAccount` row → on failure remove PAT, remove grant, delete machine user (best-effort, logged) then return error
  6. Write audit entry `"service_account.created"`
  7. Return `ServiceAccount` proto + one-time `token`
  - Audit failures: log `"service_account.create_failed"` with reason on step 2-5 errors
- [ ] Implement `ListServiceAccounts` handler:
  - Query local rows for current tenant
  - Batch-fetch grants for org via single `ListUserGrants` call to populate role display
- [ ] Implement `DeleteServiceAccount` handler:
  - Look up local row by `public_id`; return `NotFound` if missing or soft-deleted
  - Soft-delete local row (cascade deletes upstream links via FK)
  - Remove PATs (Zitadel) — best-effort, log failures
  - Delete Zitadel machine user — best-effort, log failures
  - Write audit entry `"service_account.deleted"`
  - Edge case — active impersonation: if any admin is impersonating this SA, their `limen_portal_impersonate` cookie will fail at next ID token verification (Zitadel rejects tokens from deleted users). Session middleware clears invalid cookies automatically. No explicit cleanup needed.
- [ ] Implement `RegenerateServiceAccountToken` handler:
  - Generate NEW PAT before removing old ones (prevents zero-token window)
  - On generation failure, return error — old tokens remain intact
  - Remove ALL old PATs (best-effort, log failures)
  - Write audit entry `"service_account.token_regenerated"`
  - Return new token (shown only in this response)
- [ ] Implement `ImpersonateServiceAccount` handler:
  - Look up SA + role grant; return `NotFound` if missing or soft-deleted
  - Extract admin's access_token from session context via `AccessTokenFromContext(ctx)`
  - Call Zitadel token endpoint with Token Exchange (RFC 8693):
    ```
    POST {zitadel}/oauth/v2/token
    grant_type=urn:ietf:params:oauth:grant-type:token-exchange
    subject_token={sa_zitadel_user_id}
    actor_token={admin_access_token}
    scope=openid profile email offline_access urn:zitadel:iam:org:project:id:{project_id}:aud
    ```
  - Error mapping for Zitadel failures:
    - Network/timeout → `CodeUnavailable` ("Zitadel identity provider unavailable")
    - `invalid_grant` / `unauthorized_client` → `CodePermissionDenied` ("Cannot impersonate this service account")
    - Other 4xx → `CodeInvalidArgument` with Zitadel error detail
    - Other 5xx → `CodeInternal`
  - On success: encrypt resulting ID token + access token + refresh token into `limen_portal_impersonate` cookie (AAD Kind = `"portal.impersonate"`)
  - Set cookie via `connect.Response.Header().Set("Set-Cookie", ...)` with `Path=/t/{tenant}; HttpOnly; Secure; SameSite=Lax`
  - Write audit entry `"service_account.impersonation_started"`
  - Audit failures: log `"service_account.impersonation_failed"` with error reason
- [ ] Implement `ExitImpersonation` handler:
  - Clear `limen_portal_impersonate` cookie via `Set-Cookie: ...=; Max-Age=0; Path=/t/{tenant}`
  - Write audit entry `"service_account.impersonation_ended"`
- [ ] Add shared error mapper: Zitadel gRPC status codes → Connect codes (used by all handlers)
- [ ] Add entries to `requiredRole` map in `internal/admin/roles.go`:
  ```go
  "CreateServiceAccount":          session.RoleAdmin,
  "ListServiceAccounts":           session.RoleAdmin,
  "DeleteServiceAccount":          session.RoleAdmin,
  "RegenerateServiceAccountToken": session.RoleAdmin,
  "ImpersonateServiceAccount":     session.RoleAdmin,
  "ExitImpersonation":             session.RoleMember,
  ```
- [ ] Add `serviceAccounts ServiceAccountDirectory` field to `Service` struct in `internal/admin/service.go`
- [ ] Add `ServiceAccountID` field to `serviceAccountAdminPrefix` in the service test — add new handler names to `callers()` map and `implementedRPCs` set
- [ ] Wire `ServiceAccountDirectory` (zitadel client) in `internal/boot/adminmount/adminmount.go`

### 9i-e: Session middleware — impersonation cookie

- [ ] Update session `Interceptor` (`internal/session/interceptor.go`) to:
  - Check `limen_portal_impersonate` cookie first
  - Fall through to `limen_portal` if impersonation cookie is absent or invalid
  - Clear invalid/expired impersonation cookies automatically (set `Max-Age=0` on response)
- [ ] Update cookie sealing/unsealing in `internal/auth/oidc.go`:
  - Support Kind `"portal.impersonate"` (reuse same encrypt/decrypt functions, pass Kind as parameter)
  - Both cookies coexist; impersonation takes priority
- [ ] Update upstream connection code in `internal/portal/upstreams.go`:
  - Read `SessionAccountFromContext(ctx)` — a new accessor that returns either `*storage.User` or `*storage.ServiceAccount`
  - If impersonating, set `UpstreamLink.ServiceAccountID` instead of `UserID`
  - Create `SessionAccountFromContext` in `internal/session/context.go`

### 9i-f: Bearer auth interceptor

- [ ] Create `internal/session/bearer.go` with `BearerTokenInterceptor`:
  - Extract `Authorization: Bearer <token>` from Connect request metadata
  - Skip if session already on ctx (from portal or impersonation cookie)
  - Verify JWT against Zitadel JWKS (reuses MCPAuth verifier, cached via `RemoteKeySet`)
  - Error mapping:
    - Expired token → `CodeUnauthenticated` ("token expired")
    - Invalid signature / malformed → `CodeUnauthenticated` ("invalid token")
    - JWT valid but issuer mismatch → `CodeUnauthenticated` ("untrusted token issuer")
  - Check `urn:zitadel:iam:user:resourceowner:id` claim matches tenant's `ZitadelOrgID`; mismatch → `CodePermissionDenied`
  - Look up `ServiceAccount` by `zitadel_user_id` in local DB; not found → `CodeUnauthenticated` ("unknown service account")
  - Check SA is not soft-deleted; deleted → `CodeUnauthenticated` ("service account deactivated")
  - Extract roles from `urn:zitadel:iam:org:project:roles` claim
  - Synthesize `UserSession{Subject, Roles}` and pin on ctx
- [ ] Wire `BearerTokenInterceptor` into Connect interceptor stack in `internal/admin/service.go` `Handler()`:
  ```go
  TenancyInterceptor → Interceptor (portal + impersonate) → BearerTokenInterceptor → RoleInterceptor
  ```
- [ ] Add `lookupServiceAccount` fallback in `internal/auth/middleware.go` `RequireMCPAuth`:
  - After `lookupUser` returns `ErrRecordNotFound`, try service account lookup
  - Same validation: not soft-deleted, tenant match
  - On success, synthesize `MCPAccessClaims` and continue
- [ ] Add `AccountFromContext(ctx)` accessor in `internal/auth/middleware.go` (returns either `*storage.User` or `*storage.ServiceAccount`)

### 9i-g: Frontend — service accounts page & onboarding update

- [ ] Create `web/admin/src/pages/ServiceAccountsPage.vue`:
  - Table listing service accounts (name, description, role badge, created by, created at)
  - "New Service Account" button → modal dialog:
    - Name (required), Description (optional)
    - Role select: Member / Admin — **no Owner option**
    - Expiry days field (default 365; 0 for no expiry)
  - On create: show one-time token in a copyable text field with "Copied!" feedback and warning: "Copy this token now. You won't be able to see it again."
  - Each row actions:
    - Delete button (confirmation dialog: "Delete service account 'X'? This will revoke all tokens and disable any integrations using it.")
    - Regenerate Token button (warning dialog: "This will invalidate the current token and generate a new one." → shows new token once)
    - Impersonate button (calls RPC, sets impersonation cookie via response header, redirects to `/t/{tenant}/portal/`)
  - If currently impersonating, show banner: "Impersonating {name}" with "Exit impersonation" button
  - Empty state: "No service accounts yet" with CTA button "Create your first service account"
- [ ] Add route `serviceAccounts: '/org/service-accounts'` to `ROUTES` in `web/admin/src/router/routes.ts`
- [ ] Add route definition entry for service accounts page (lazy-import with `() => import(...)`)
- [ ] Add sidebar nav entry under "Organization" group:
  ```ts
  { kind: 'leaf', label: 'Service Accounts', path: ROUTES.serviceAccounts, icon: KeyRound }
  ```
  Using `KeyRound` from `@lucide/vue` (import alongside existing icon imports).
- [ ] Update `web/admin/src/pages/Dashboard.vue`:
  - Replace step 4 `TaskBentoCard` from "Configure Organization" to:
    - Icon: `KeyRound`
    - Title: "Create Service Account"
    - Body: "Generate an API token for cloud agents and CLI tools to access the gateway programmatically."
    - CTA: "Set Up Service Account"
    - `@activate` navigates to `ROUTES.serviceAccounts`
  - Rename `openSettings()` → `openServiceAccounts()` handler
  - Mark step as done when navigated (reuse existing `configuredAtNow` / `UpdateTenantSettings` mechanism)
- [ ] Portal dashboard onboarding (step 2: "Configure your IDE") remains unchanged

### 9i-h: Integration & verification

- [ ] Run `go build ./...` — all binaries compile
- [ ] Run `golangci-lint run ./...` — no new violations
- [ ] Run `go test ./...` — all tests pass
- [ ] Run `buf lint` — proto is valid
- [ ] Run `npm run build` in `web/admin/` — SPA builds without errors
- [ ] Update upstream link query code where `UserID` is assumed:
  - `internal/storage/model_upstream.go`: verify GORM BelongsTo association works with nullable FK; add `ServiceAccount *ServiceAccount` association
  - `internal/upstream/` package: check `GetUpstreamLink`, `ListUpstreamLinks`, `UpsertUpstreamLink` — make `UserID` optional when `ServiceAccountID` is set
  - `internal/gateway/` package: check link resolution during request dispatch — must work when owner is a service account
  - `internal/transport/` package: check upstream callback handlers — owner resolution must handle SA-owned links
- [ ] Integration test: full Create → List → Delete → Regenerate → Impersonate → Exit → UpstreamLink flow

## Design decisions

### Why Zitadel PAT (not JWT Profile Key)
PAT is a ready-to-use JWT Bearer token. JWT Profile Key requires the client to sign a JWT assertion on every request — more complex for end users. PAT passes the same JWKS verification as other tokens.

### Why Token Exchange for impersonation (not custom cookies)
Zitadel natively supports RFC 8693 Token Exchange. The admin presents their access token as the `actor_token` and receives an ID token scoped to the service account. Zitadel manages the token lifetime and adds an `act` claim for audit trail. This eliminates custom impersonation crypto and cookie infrastructure. Limen only needs to store the resulting ID token in its existing AES-SIV cookie format.

### Why XOR constraint on UpstreamLink
Service accounts own upstream links during impersonation. The owning identity is either a human user or a service account — never both, never neither. The CHECK constraint `((user_id IS NULL) <> (service_account_id IS NULL))` makes this illegal state unrepresentable at the database level (Parse, Don't Validate).

### Why compensation ordering (Zitadel-first, local DB last)
Creating a service account spans two systems (Zitadel + local DB). The flow is Zitadel-first: create machine user → grant role → generate PAT → insert local row. If any Zitadel step fails, the operation aborts cleanly. If the local DB insert fails, best-effort rollback cleans up the orphaned Zitadel resources. This minimizes the window where Zitadel has state the local DB doesn't.

### Why regenerate creates new PAT before removing old
Prevents a zero-token window. If the old PATs were removed first and generation failed, the service account would be left with no usable token. Generating the new PAT first ensures continuity: the admin holds the new token before the old ones are revoked. If generation fails, the old tokens remain valid.

### Role ceiling
Service accounts are capped at `member` or `admin`. They can never be `owner`. This is enforced at the handler level — `CreateServiceAccount` rejects `SERVICE_ACCOUNT_ROLE_OWNER` (or equivalent) with `CodeInvalidArgument`. The `RoleInterceptor` prevents any elevation path since role grants are managed through the same Zitadel Authorization V2 API used for human members.

### Interceptor stack ordering
The Connect-RPC interceptor stack for the admin API is:
1. `TenancyInterceptor` — validates tenant is on ctx
2. `Interceptor` (session) — decrypts `limen_portal_impersonate` first, falls back to `limen_portal`
3. `BearerTokenInterceptor` — checks `Authorization: Bearer <pat>`, skips if session already on ctx
4. `RoleInterceptor` — enforces per-RPC minimum role from `requiredRole` map

This allows both browser-authenticated admins (via cookie) and service accounts (via Bearer token) to call admin RPCs, with role enforcement applied uniformly after identity is resolved.

## Verification

- [ ] Admin creates a service account, receives a PAT, uses it with `curl -H "Authorization: Bearer <pat>"` against a Connect-RPC endpoint
- [ ] Admin creates a service account, receives a PAT, uses it with an MCP SSE connection at `/t/{tenant}/mcp/sse`
- [ ] Admin impersonates a service account, is redirected to portal, can create an upstream link
- [ ] Upstream link created during impersonation is owned by the service account (checked in DB)
- [ ] Admin exits impersonation, `limen_portal_impersonate` cookie is cleared
- [ ] Regenerate token invalidates old PAT; new PAT works, old PAT returns 401
- [ ] Delete service account soft-deletes local row, removes Zitadel user; old PAT returns 401
- [ ] Attempt to create SA with owner role is rejected with InvalidArgument
- [ ] Non-admin user cannot call CreateServiceAccount (403 from RoleInterceptor)
- [ ] Expired PAT returns Unauthenticated with descriptive message
- [ ] Admin onboarding step 4 navigates to service accounts page and marks step complete

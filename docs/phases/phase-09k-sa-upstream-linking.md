---
phase: "9k"
title: "Service Account Upstream Linking"
status: in_progress
progress: 85
depends_on: ["9i", "7", "9c"]
updated: "2026-05-27"
---

# Phase 9k — Service Account Upstream Linking

> **Depends on**: Phase 9i (service accounts), Phase 7 (upstream strategies), Phase 9c (admin SPA)
> **Unblocks**: programmatic MCP access with upstream credentials; full SA self-service

## Goal

Enable tenant admins to configure MCP upstream connections (OAuth, API key, shared secret) for
service accounts directly from the admin SPA — using dedicated admin RPCs that create
`UpstreamLink` rows owned by the service account (`service_account_id` FK), not the admin user.

## Background

Phase 9i delivers service accounts (Zitadel machine users with PATs) and basic upstream link
management (enable/disable toggles via `ListServiceAccountUpstreamLinks` and
`SetServiceAccountLinkEnabled`). However, it does **not** provide the full linking flow: OAuth
connect (`mcp_spec`), API key entry (`static_header` override mode), or disconnect.

Phase 7 defines three upstream strategies:

| Strategy        | Linking flow                                                    |
| --------------- | --------------------------------------------------------------- |
| `none`          | No auth — tools available to all tenant members automatically   |
| `static_header` | Tenant-wide shared secret OR per-user API key override          |
| `mcp_spec`      | Full OAuth 2.0 PKCE flow (DCR → authorize → callback → tokens)  |

The portal SPA (`web/portal/`) already supports all three strategies for human users via
`PortalService` RPCs (`StartConnect`, `SubmitUpstreamAPIKey`, `ClearUpstreamOverride`,
`Disconnect`). These RPCs resolve the caller via `callerContext()` → `LoadUserBySubject()` and
create `UpstreamLink` rows with `UserID` set.

Service accounts need the same linking capabilities but with `ServiceAccountID` set on the link
row instead of `UserID`. The `UpstreamLink` model already has the `ServiceAccountID *int64`
field with proper unique indexes (Phase 9i migration), and storage-layer query methods exist
(`ListUpstreamLinksByServiceAccount`, `GetUpstreamLinkByServiceAccountAndUpstream`) but are
currently dead code.

## Design

### Architecture: Direct Admin RPCs (not impersonation)

Two approaches were evaluated:

| Approach                  | Lines  | Files | Novel concepts                          |
| ------------------------- | ------ | ----- | --------------------------------------- |
| A: Impersonation          | ~560   | 9+    | Cookie system, resolver wrapper, portal session changes |
| **B: Direct admin RPCs**  | **~460** | **6** | **4 admin RPCs, SA-aware LinkContext**  |

**Decision: Approach B.** Fewer novel auth concepts, no portal changes, smaller blast radius.
The admin never operates "as" the service account — they target it when creating links.

### LinkContext extension

`upstream.LinkContext` gains an optional `ServiceAccountID *int64` field:

```go
type LinkContext struct {
    Tenant           *storage.Tenant
    User             *storage.User      // ALWAYS set — the admin initiator (for OAuth state AAD)
    ServiceAccountID *int64             // optional — when set, link FK targets SA instead of User
    Upstream         *storage.Upstream
    Link             *storage.UpstreamLink
    ReturnTo         string
}
```

Key invariant: **`User` is always the admin initiator.** This preserves the OAuth state envelope's
`UserID` field (used for AAD encryption) without changing `Envelope.Put` guards. The
`ServiceAccountID` is the link-target override read by `FinishLink` when creating the
`UpstreamLink` row.

Helper methods on `LinkContext`:

```go
func (l LinkContext) IsServiceAccount() bool  { return l.ServiceAccountID != nil }
func (l LinkContext) OwnerID() int64          { /* SA ID if set, else User.ID */ }
func (l LinkContext) OwnerIDStr() string      { /* strconv.FormatInt(OwnerID(), 10) */ }
```

`OwnerIDStr()` replaces hardcoded `userStr` in crypto AAD for token encryption
(`mcpspec/link.go`) and secret encryption (`statichdr/statichdr.go`), keeping encrypt/decrypt
symmetric regardless of owner type.

### OAuth state envelope

`oauthstate.Envelope` gains `ServiceAccountID *int64`:

```go
type Envelope struct {
    TenantID         int64
    UserID           int64    // admin initiator — used for AAD, unchanged
    UpstreamID       int64
    ServiceAccountID *int64   // link target — when set, FinishLink creates SA-owned link
    ReturnTo         string
    PKCEVerifier     string
    Nonce            string
}
```

`Envelope.Put` validation stays unchanged (`UserID != 0` still required — it's the admin's ID).
`StartLink` populates `ServiceAccountID` from `lctx.ServiceAccountID` when non-nil.
`FinishLink` reads `env.ServiceAccountID` after consuming the state and branches:
- If `env.ServiceAccountID != nil` → set `UpstreamLink.ServiceAccountID`, leave `UserID` nil
- Otherwise → set `UpstreamLink.UserID` as before

The OAuth callback handler at `transport/upstream.go:63` stays **unchanged** — it resolves the
admin user normally from the OIDC session. The SA targeting happens inside `FinishLink` via the
envelope metadata.

### Strategy-layer changes

**`mcpspec/link.go`:**
- `StartLink`: populate `Envelope.ServiceAccountID` from `lctx.ServiceAccountID`
- `FinishLink`: branch link FK on `env.ServiceAccountID`; use `lctx.OwnerIDStr()` for token AAD
- Existing-link check (line 162): query by `service_account_id` when `env.ServiceAccountID` set

**`statichdr/statichdr.go`:**
- `PersistUserSecret`: use `lctx.OwnerIDStr()` for secret AAD; query/create link by
  `service_account_id` when `lctx.IsServiceAccount()`
- `ClearUserOverride`: same — SA-aware link query and AAD
- `Headers`: use `lctx.OwnerIDStr()` for secret decryption AAD

**`upstream/service.go`:**
- `Disconnect`: SA-aware link deletion (branch on `IsServiceAccount()`)
- New `loadLinkByOwner(ctx, tenantID, lctx, upstreamID)` helper replacing the hardcoded
  `user_id` query in `loadLink`

### Admin proto RPCs

Four new RPCs on `AdminService`, all scoped to `owner` role:

```protobuf
rpc StartServiceAccountConnect(StartServiceAccountConnectRequest) returns (StartServiceAccountConnectResponse);
rpc SubmitServiceAccountAPIKey(SubmitServiceAccountAPIKeyRequest) returns (SubmitServiceAccountAPIKeyResponse);
rpc ClearServiceAccountOverride(ClearServiceAccountOverrideRequest) returns (ClearServiceAccountOverrideResponse);
rpc DisconnectServiceAccountUpstream(DisconnectServiceAccountUpstreamRequest) returns (DisconnectServiceAccountUpstreamResponse);
```

Request messages:

```protobuf
message StartServiceAccountConnectRequest {
    string service_account_public_id = 1;
    string upstream_identifier = 2;
    string return_to = 3;
}
message StartServiceAccountConnectResponse {
    string redirect_url = 1;
}

message SubmitServiceAccountAPIKeyRequest {
    string service_account_public_id = 1;
    string upstream_identifier = 2;
    string secret = 3;
}
message SubmitServiceAccountAPIKeyResponse {}

message ClearServiceAccountOverrideRequest {
    string service_account_public_id = 1;
    string upstream_identifier = 2;
}
message ClearServiceAccountOverrideResponse {}

message DisconnectServiceAccountUpstreamRequest {
    string service_account_public_id = 1;
    string upstream_identifier = 2;
}
message DisconnectServiceAccountUpstreamResponse {}
```

### Handler implementation

All four handlers live in `internal/admin/service_account_links.go` and follow the same pattern:

1. Resolve tenant from ctx
2. Resolve admin user from session (the initiator — for `LinkContext.User`)
3. Look up service account by `public_id` → get `sa.ID`
4. Build `LinkContext{Tenant, User: admin, ServiceAccountID: &sa.ID, Upstream: ...}`
5. Delegate to the corresponding `upstream.Service` method

### Admin SPA UI

The SA detail page's existing "MCP Portal" section already shows a merged upstream table with
link status pills. Phase 9k adds per-row action buttons driven by the upstream's strategy type
and link state:

| Strategy        | Link state     | Actions shown                                                |
| --------------- | -------------- | ------------------------------------------------------------ |
| `none`          | —              | _(no actions — tools available automatically)_               |
| `static_header` | Not linked     | "Enter API Key" (if override mode) or "Enable" (if shared)   |
| `static_header` | Linked         | "Rotate Key", "Use Shared Key" (if override), Enable/Disable, Disconnect |
| `mcp_spec`      | Not linked     | "Connect" (opens OAuth popup)                                |
| `mcp_spec`      | Linked         | Enable/Disable, Disconnect                                   |

An `ApiKeyModal` component (admin design tokens) handles static_header key entry/rotation.
OAuth popup reuses `openOAuthPopup` from `@limen/shared`.

The SA list page gains a "Links" column showing "X of Y" linked upstreams.

## Sub-phases

### 9k-a: Backend — SA-aware linking + admin RPCs

- [x] Add `ServiceAccountID *int64` to `upstream.LinkContext` + helpers `OwnerID()`, `OwnerIDStr()`, `IsServiceAccount()` → `internal/upstream/strategy.go`
- [x] Add `ServiceAccountID *int64` to `oauthstate.Envelope` → `internal/upstream/oauthstate/oauthstate.go`; populate in `StartLink`; branch in `FinishLink` for link FK
- [x] Update token AAD in `mcpspec/link.go` to use `lctx.OwnerIDStr()`; update secret AAD in `statichdr/statichdr.go`; update link queries to use `service_account_id` when `lctx.IsServiceAccount()`
- [x] Update existing-link check in `mcpspec/link.go:162` to query by `service_account_id` when applicable
- [x] Add SA-aware `loadLinkByOwner` to `internal/upstream/service.go`; update `Disconnect` and `SetLinkEnabled` for SA branching
- [x] Define 4 proto messages + RPCs in `proto/limen/admin/v1/admin.proto`; run `buf generate`
- [x] Implement 4 admin RPC handlers in `internal/admin/service_account_links.go`
- [x] Add `owner` role entries for 4 new RPCs in `internal/admin/roles.go`

### 9k-b: Frontend — Admin SPA SA linking UI

- [x] Replace enable/disable-only toggle in SA detail MCP Portal table with per-row action buttons (Connect, Enter API Key, Rotate Key, Use Shared Key, Disconnect, Enable/Disable) → `web/admin/src/pages/ServiceAccountDetail.vue`
- [x] Create `ApiKeyModal.vue` component (admin design tokens) for entering/rotating static_header keys → `web/admin/src/components/ApiKeyModal.vue`
- [x] Wire OAuth popup: reuse `openOAuthPopup` from `@limen/shared`; on popup close, refresh SA link state
- [x] Add busy state and error display per action button using `mutationError`/`busyMap` pattern
- [x] Add link count column to `ServiceAccounts.vue` table ("X of Y linked")

### 9k-c: Documentation

- [ ] Create `docs/future-works/impersonation.md` documenting user-to-user impersonation as a future capability: `ImpersonationResolver` wrapper, AES-SIV `limen_impersonate` cookie, `UserSession.ImpersonatedBy` audit field, role gate, no chaining, auto-expiry

## Design Decisions

### Why direct admin RPCs over impersonation

Impersonation (Approach A) would require a novel cookie system (`limen_impersonate`), a
`Resolver` wrapper that intercepts session resolution, changes to the portal SPA's
`callerContext()`, and a "stop impersonating" flow. It introduces ~560 lines across 9+ files
with new auth concepts.

Direct admin RPCs (Approach B) add 4 simple RPCs that build a `LinkContext` with the SA ID and
delegate to existing `upstream.Service` methods. The strategy layer gains SA-awareness through
a single `ServiceAccountID *int64` field and `OwnerIDStr()` helper — ~460 lines across 6 files,
no portal changes, no new cookie format.

### Why `LinkContext.User` is always set

The OAuth state envelope's `UserID` field is used as part of the AAD for Valkey encryption.
Changing `Envelope.Put` to allow `UserID == 0` would weaken the AAD binding. Instead, `User`
always carries the admin initiator's identity (for state AAD), while `ServiceAccountID` carries
the link-target override. This separation is clean: initiator ≠ beneficiary.

### Why the OAuth callback handler stays unchanged

The callback handler at `transport/upstream.go:63` resolves the admin user from the OIDC session
and passes it to `FinishCallback` → `FinishLink`. The SA targeting happens inside `FinishLink`
when it reads `env.ServiceAccountID` from the consumed state envelope. No transport-layer changes
needed — the admin's browser session handles the OAuth redirect naturally.

### Why `OwnerIDStr()` for crypto AAD

Token encryption (access/refresh tokens in `mcpspec`) and secret encryption (API keys in
`statichdr`) both use AAD that includes the owner identifier. When the link owner is a user,
this is `user_id`; when it's a service account, it's `service_account_id`. `OwnerIDStr()`
abstracts this so encrypt and decrypt stay symmetric regardless of owner type.

## Deliverables

| File | Change |
|------|--------|
| `internal/upstream/strategy.go` | Add `ServiceAccountID`, `OwnerID()`, `OwnerIDStr()`, `IsServiceAccount()` to `LinkContext` |
| `internal/upstream/oauthstate/oauthstate.go` | Add `ServiceAccountID *int64` to `Envelope` |
| `internal/upstream/mcpspec/link.go` | SA-aware `StartLink`, `FinishLink`, existing-link check, token AAD |
| `internal/upstream/statichdr/statichdr.go` | SA-aware `PersistUserSecret`, `ClearUserOverride`, `Headers` AAD |
| `internal/upstream/service.go` | SA-aware `loadLinkByOwner`, `Disconnect` |
| `internal/upstream/portal_ops.go` | SA-aware `SetLinkEnabled` |
| `proto/limen/admin/v1/admin.proto` | 4 new RPCs + request/response messages |
| `internal/admin/service_account_links.go` | 4 handler implementations |
| `internal/admin/roles.go` | Role entries for 4 new RPCs |
| `web/admin/src/pages/ServiceAccountDetail.vue` | Per-row action buttons for all strategy types |
| `web/admin/src/pages/ServiceAccounts.vue` | Link count column |
| `web/admin/src/components/ApiKeyModal.vue` | API key entry/rotation modal |
| `docs/future-works/impersonation.md` | Future-works document for user-to-user impersonation |
| `docs/phases/phase-09k-sa-upstream-linking.md` | This file |
| `docs/phases/README.md` | Updated index |

## Verification

- [ ] Admin connects an `mcp_spec` upstream for a service account via OAuth popup; link row has `service_account_id` set, `user_id` NULL
- [ ] Admin enters a `static_header` API key for a service account; encrypted secret uses SA-based AAD
- [ ] Admin clears a `static_header` override for a service account; link row preserved, `extra_json` zeroed
- [ ] Admin disconnects an upstream from a service account; link row soft-deleted
- [ ] Service account uses its PAT to call an MCP tool through an upstream link it owns (gateway resolves SA-owned link)
- [ ] OAuth callback completes successfully with admin's browser session; link created under SA
- [ ] `go build ./...`, `go vet ./...`, `golangci-lint run ./...` all clean
- [ ] `vue-tsc --noEmit`, `pnpm lint` in `web/admin/` all clean
- [ ] Integration test: full StartConnect → OAuth callback → VerifyLink → tool call flow for SA-owned link

## Risks

- **Gateway auth provider**: `internal/upstream/authprovider.go` currently resolves links by
  `user_id` only. The gateway's `DBAuthProvider.linkContext()` and `loadLink()` need SA-aware
  query branching for service accounts to use their upstream links at request time. This is a
  gateway-layer change that may belong in Phase 8 or a separate sub-phase.
- **Token refresh**: `mcpspec` background token refresh (`Maintain`) iterates links and decrypts
  tokens using AAD. SA-owned links must use SA-based AAD for decryption. The refresher must
  be updated to detect `ServiceAccountID` on the link and use the correct AAD.
- **Portal SPA parity**: The admin SPA's SA linking UI will be functionally equivalent to the
  portal's user linking UI but uses different design tokens. The shared `openOAuthPopup` utility
  and `ConfirmActionModal` component are reused, but the `ApiKeyModal` is admin-specific.

---
phase: "9j"
title: "Impersonation UX & Cookie Fix"
status: planned
progress: 0
depends_on: ["9i", "9d", "9b", "4"]
updated: "2026-05-20"
---

# Phase 9j — Impersonation UX & Cookie Fix

> **Depends on**: Phase 9i (service accounts), Phase 9d (shared session service), Phase 9b (portal SPA), Phase 4 (OIDC login)
> **Unblocks**: full admin impersonation flow with visible UI feedback; Phase 12 staff impersonation (reuses cookie format)

## Goal

Fix impersonation (currently broken: `invalid_scope` on token refresh) and add full impersonation UX to the portal SPA by switching to a claims-cached cookie format that eliminates Zitadel network calls on the read path.

## Background

The impersonation cookie stores the ID token + refresh token from the Zitadel token exchange. When the ID token expires, `ResolveImpersonationSession` calls `rp.RefreshTokens`, which uses the portal's default OIDC RP (public PKCE client) with `rp.OAuthConfig().Scopes` (`openid profile email offline_access urn:zitadel:iam:user:resourceowner`). But the impersonation token was minted with the token exchange's audience scope (`urn:zitadel:iam:org:project:id:<projectID>:aud`). Zitadel rejects the mismatched scopes with `invalid_scope`.

Additionally, the refresh token belongs to the token exchange confidential client, but `rp.RefreshTokens` authenticates as the portal PKCE client. This client mismatch compounds the scope problem — even if scopes aligned, the refresh would fail because the client credentials don't match the token's issuing client.

## Solution: Option C — Claims-Cached Cookie

Instead of fixing the refresh, eliminate it entirely. Store baked-in identity claims directly in the AES-SIV encrypted cookie. Zero Zitadel calls on the read path. Token exchange happens once (at impersonation start), claims are extracted from the exchange ID token at that point, and everything is packed into the cookie.

## Sub-phases

### 9J-a: Cookie Format v2

Define `CookiePayloadV2` struct:

```go
type CookiePayloadV2 struct {
    Version       uint8     // 0x01 — discriminator byte
    AccessToken   string    // kept for potential upstream use
    Subject       string    // Zitadel subject
    Email         string
    FirstName     string
    LastName      string
    Roles         []string
    ActorUserID    string
    ActorEmail     string
    ActorFirstName string
    ActorLastName  string
    Reason        string    // free-text reason from impersonation modal
    UserType      uint8     // 0=user, 1=service_account
    Impersonated  bool
    ExpiresAt     time.Time
}
```

Binary format: version byte (0x01) → each string as `uint16` length prefix + bytes → roles as `uint16` count + `uint16`-length-prefixed strings → `user_type` uint8 → `impersonated` uint8 → `expires_at` int64 (UnixNano).

- [ ] Implement `PackCookieV2(p *CookiePayloadV2) ([]byte, error)` — serializes to binary
- [ ] Implement `UnpackCookieV2(data []byte) (*CookiePayloadV2, error)` — deserializes; first byte must be 0x01; unknown versions return hard error
- [ ] Unit tests: round-trip with all fields populated; empty fields; zero roles; unknown version rejection (0x02, 0xFF)

### 9J-b: Impersonation Cookie Read Path

- [ ] Update `readImpersonationCookie` in `internal/auth/oidc.go` to detect version byte after decrypt + decompress — if first byte is 0x01, dispatch to `UnpackCookieV2`
- [ ] Update `ResolveImpersonationSession` in `internal/auth/oidc.go`:
  - Remove all refresh logic (lines 712–721: `rp.VerifyIDToken`, `rp.RefreshTokens`)
  - Check `ExpiresAt` against `time.Now()` — return nil, error if expired
  - Build `*oidc.IDTokenClaims` (or equivalent struct) from cached claims directly
- [ ] Update `OIDCImpersonationResolver` adapter in `internal/session/context.go` to build `UserSession` from v2 cookie claims
- [ ] Unit tests: fresh v2 cookie resolves correctly; expired v2 cookie returns error; zero HTTP calls to Zitadel during resolution

### 9J-c: Impersonation Cookie Write Path

- [ ] Add JWT claim extraction helper — parse the exchange ID token's base64-decoded JSON payload to extract `sub`, `email`, `given_name`, `family_name`, `urn:zitadel:iam:org:project:roles`. The ID token was just received from Zitadel's token exchange — it is trusted at this point. Map Zitadel role keys to Limen role strings.
- [ ] Update `ImpersonateServiceAccount` handler in `internal/admin/service_accounts.go`:
  - Extract admin's actor info from session context
  - Extract target claims from exchange ID token
  - Build `CookiePayloadV2` with `UserType=1` (service_account), `Impersonated=true`, `ExpiresAt` from exchange `expires_in` (capped at 12h max)
  - Pack → zstd → AES-SIV (same AAD Kind `"portal.impersonate"`) → base64 → cookie
- [ ] Remove old `buildImpersonationCookieValue`
- [ ] Unit tests: cookie round-trip through write → read pipeline

### 9J-d: Proto Update

- [ ] Add to `proto/limen/session/v1/session.proto`:
  ```protobuf
  message ImpersonationInfo {
    bool is_impersonating = 1;
    string actor_user_id = 2;
    string actor_email = 3;
    string actor_first_name = 4;
    string actor_last_name = 5;
    string reason = 6;
    string target_user_type = 7; // "service_account" or "user"
    string expires_at = 8; // RFC3339
  }
  ```
- [ ] Add `ImpersonationInfo impersonation = 4;` to `GetSessionResponse`
- [ ] Run `buf generate`
- [ ] Update `GetSession` handler in `internal/session/service.go` to populate `ImpersonationInfo` from the session context when the session was resolved from the impersonation cookie

### 9J-e: Portal SPA Impersonation Banner

- [ ] Update shared session store (`web/shared/src/session/store.ts`) to capture `impersonation` field from `GetSessionResponse`
- [ ] Create `web/portal/src/components/ImpersonationBanner.vue`:
  - Red persistent banner pinned to top of viewport (sticky, z-50)
  - Shows: "You are viewing as **{first_name} {last_name}** ({email}){service_account_label}. Impersonated by **{actor_first_name} {actor_last_name}** ({actor_email})."
  - If `target_user_type === "service_account"`, append " (Service Account)" after name
  - Live countdown timer displaying remaining time until `expires_at` — shows "HH:MM:SS" normally, "MMm SSs" when under 5 minutes
  - "End impersonation" button — calls `AdminService.ExitImpersonation` RPC, then hard-redirects to `${tenantPrefix}/admin/`
  - Banner is non-dismissible (no close/X button)
  - Auto-redirect to `${tenantPrefix}/admin/` when countdown hits zero
- [ ] Wire banner into `web/portal/src/App.vue` — rendered above `<RouterView>` when `session.impersonation?.is_impersonating` is true
- [ ] Add `endImpersonation()` method to session client (`web/shared/src/session/sessionClient.ts`) that calls the admin transport's `ExitImpersonation` RPC

### 9J-f: Verification

- [ ] Integration test: impersonation creates v2 cookie → cookie decrypts correctly → GetSession returns impersonation info → banner renders → exit impersonation clears cookie → redirect to admin

## Design Decisions

### Why cache claims instead of fixing refresh

The refresh path has two unfixable problems without significant Zitadel-side changes: scope mismatch (portal RP scopes ≠ token exchange scopes) and client mismatch (portal PKCE client ≠ token exchange confidential client). Caching claims in the cookie eliminates both problems and removes all Zitadel network latency from the read path.

### Why a version byte

The old `PortalCookieValue` format has no version discriminator. Adding 0x01 as the first byte makes future format changes cleanly detectable. Unknown versions are a hard error (force re-impersonation).

### Why AccessToken is kept

Some handlers may need it for upstream API calls. The token exchange provides it, and the cookie has room — keeping it costs nothing.

### Why no refresh token in v2

With claims cached directly, there's nothing to refresh. The impersonation session is bound to the exchange token's lifetime (max 12h).

### Why actor metadata in the cookie

Required for the portal banner display. Storing it avoids a DB lookup on every API call.

### Why Phase 12's `limen_impersonating` cookie is out of scope

Phase 12 (staff backoffice) needs a separate cookie at `Path=/t/` for cross-tenant awareness during staff impersonation of real users. Phase 09J only covers tenant admin → service account impersonation within a single tenant. Phase 12 will reuse the `CookiePayloadV2` format with `UserType=0` (real user) and staff-specific additions.

## Deliverables

Files touched:

| File | Change |
|------|--------|
| `internal/auth/oidc.go` | `CookiePayloadV2`, `PackCookieV2`, `UnpackCookieV2`, updated `ResolveImpersonationSession` |
| `internal/session/context.go` | Updated `OIDCImpersonationResolver` |
| `internal/admin/service_accounts.go` | Updated `ImpersonateServiceAccount`, JWT claim extraction |
| `proto/limen/session/v1/session.proto` | `ImpersonationInfo`, updated `GetSessionResponse` |
| `internal/session/service.go` | Updated `GetSession` handler |
| `web/shared/src/session/store.ts` | Impersonation state |
| `web/portal/src/components/ImpersonationBanner.vue` | New banner component |
| `web/portal/src/App.vue` | Banner wiring |
| `web/shared/src/session/sessionClient.ts` | `endImpersonation()` |
| `docs/phases/phase-09j-impersonation-ux.md` | This file |
| `docs/phases/README.md` | Updated index |

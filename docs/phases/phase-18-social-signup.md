# Phase 18 — Social signup

> **Status: deferred.** Captured here so [Phase 9h](phase-09h-signup.md) can
> ship the email-verify + Zitadel password-init flow without scope creep.
> Implement after Phase 9h has run in production long enough to collect
> funnel metrics that can inform the social UX.

## Goal

Allow new tenants to sign up via Zitadel-federated social IdPs (Google,
GitHub, Microsoft, Apple) instead of the Limen email-verify + Zitadel
password-init flow that [Phase 9h](phase-09h-signup.md) ships. Same end
state as Phase 9h: a verified `owner` user lands on `/t/<tenant>/admin/`
in one continuous browser flow.

The social path skips two of Phase 9h's steps:

- **No Limen-side email verification** — the social IdP already vouches
  for the email (and Zitadel's `email_verified` claim reflects this).
- **No Zitadel password-init handoff** — the credential is federated;
  there is no password to set.

## Background

Phase 9h establishes the pattern:

1. Captcha + rate-limited public RPC mints a `pending_signups` row.
2. Limen owns proof-of-control over the contact channel (email link).
3. Zitadel is touched only on `CompleteSignup` — org + user + grant in
   one atomic step.
4. The Limen `tenants` row + Zitadel `org_id` are bound 1:1 with the
   `limen_tenant_id` org-metadata key (per Phase 4).

This phase swaps step (2): the proof-of-control is delegated to the
social IdP's OAuth round-trip instead of an email link.

## Flow

```
StartSocialSignup(tenant_name, idp, captcha_token)
  ├─ Verifier.Verify(captcha_token, client_ip)
  ├─ rate-limit per IP (shared bucket with Phase 9h StartSignup)
  ├─ mint pending_social_signup row (id, tenant_name, idp, created_at)
  ├─ set pending_social_signup cookie (AES-SIV, Path=/signup, 30 min)
  └─ return { authorize_url: "<issuer>/oauth/v2/authorize?…&idp_id=<idp>&prompt=create" }

Browser → authorize_url → Zitadel hosted social-login dance
       → social IdP consent → Zitadel callback to /signup/social/callback?code=…

GET /signup/social/callback?code=…   (SPA route, calls server)
  └─ SignupService.CompleteSocialSignup(code) → exchange code for tokens
       ├─ require pending_social_signup cookie → load row
       ├─ idempotency: if completed_at IS NOT NULL → return cached response
       ├─ verify id_token; require email_verified=true
       ├─ Zitadel calls (first touch beyond the social-login itself):
       │    ├─ OrganizationService.CreateOrganization(name=row.tenant_name)
       │    ├─ AddUserGrant(zitadel_user_id, project_id, role="owner")
       │    └─ ManagementService.SetOrgMetadata("limen_tenant_id", tenant.PublicID)
       ├─ INSERT tenants row (PublicID = ids.MustMake(PrefixTenant))
       ├─ UPDATE pending_social_signups SET completed_at, zitadel_*, tenant_id
       ├─ clear pending_social_signup cookie
       └─ return { tenant_public_id, login_url: "/auth/login?tenant=<pid>&return_to=/t/<pid>/admin/" }

Browser → login_url → OIDC dance (the user is already authenticated to
       Zitadel from the social hop, so this is a silent prompt=none round-trip)
       → /t/<pid>/admin/ landing
```

Note that **the user already exists in Zitadel** by the time
`CompleteSocialSignup` fires — Zitadel auto-creates the human user on
first social login. Limen's job is just to mint the org, grant, and
tenant row.

## Persistence

Mirror of `PendingSignup`, minus the password-init bits:

```go
// internal/storage/model_pending_social_signup.go
type PendingSocialSignup struct {
    ID            string     `gorm:"primaryKey;column:id"`         // ssn_<ULID>
    TenantName    string     `gorm:"column:tenant_name;not null"`
    IDP           string     `gorm:"column:idp;not null"`          // "google" | "github" | "microsoft" | "apple"
    IP            string     `gorm:"column:ip;not null"`
    StateNonce    []byte     `gorm:"column:state_nonce;uniqueIndex;not null"`
    ZitadelOrgID  string     `gorm:"column:zitadel_org_id"`
    ZitadelUserID string     `gorm:"column:zitadel_user_id"`
    TenantID      *string    `gorm:"column:tenant_id;type:uuid"`
    CreatedAt     time.Time  `gorm:"column:created_at;not null"`
    CompletedAt   *time.Time `gorm:"column:completed_at;index"`
}
```

Registered in `AllModels()`. Pre-tenant, not RLS-scoped — same caveats as
Phase 9h.

## Proto

[`proto/limen/signup/v1/signup.proto`](../../proto/limen/signup/v1/signup.proto)
gains two RPCs alongside the Phase 9h trio:

```proto
service SignupService {
  // Phase 9h
  rpc StartSignup    (StartSignupRequest)    returns (StartSignupResponse);
  rpc VerifyEmail    (VerifyEmailRequest)    returns (VerifyEmailResponse);
  rpc CompleteSignup (CompleteSignupRequest) returns (CompleteSignupResponse);

  // Phase 18
  rpc StartSocialSignup    (StartSocialSignupRequest)    returns (StartSocialSignupResponse);
  rpc CompleteSocialSignup (CompleteSocialSignupRequest) returns (CompleteSocialSignupResponse);
}

message StartSocialSignupRequest {
  string tenant_name   = 1;
  IDP    idp           = 2;
  string captcha_token = 3;
}
enum IDP {
  IDP_UNSPECIFIED = 0;
  IDP_GOOGLE      = 1;
  IDP_GITHUB      = 2;
  IDP_MICROSOFT   = 3;
  IDP_APPLE       = 4;
}
message StartSocialSignupResponse  { string authorize_url = 1; }

message CompleteSocialSignupRequest  { string code = 1; string state = 2; }
message CompleteSocialSignupResponse { string tenant_public_id = 1; string login_url = 2; }
```

Closed enum (no `_OTHER` variant) keeps unknown IdPs unrepresentable on
the wire — the SPA can only request providers Limen has configured.

## Configuration

Per-IdP toggle + Zitadel IdP UUID (Zitadel mints one per configured
provider in the Limen-Gateway org). The provider's OAuth client_id /
secret live in Zitadel, **not** in Limen — Limen only knows which
IdP UUID to redirect to.

```yaml
signup:
  social:
    enabled: true
    idps:
      google: { enabled: true, zitadel_idp_id: "${LIMEN_IDP_GOOGLE_ID}" }
      github: { enabled: true, zitadel_idp_id: "${LIMEN_IDP_GITHUB_ID}" }
      microsoft: { enabled: false, zitadel_idp_id: "" }
      apple: { enabled: false, zitadel_idp_id: "" }
```

`GET /auth/discovery` extends to return the list of enabled IdPs so the
SPA renders the right buttons.

## Frontend

`SignupStart.vue` (the Phase 9h page) gains a "Continue with…" panel
above the email form. Each enabled IdP renders as a button that calls
`StartSocialSignup` and follows the returned `authorize_url`.

New SPA route `/signup/social/callback` renders `SignupSocialCallback.vue`,
which calls `CompleteSocialSignup({ code, state })` from the query
string and navigates to `login_url` on success.

The email-and-password form below the social panel is exactly the
Phase 9h flow — the two paths share the captcha widget and the
`pending_signups` row layout differs only in the strategy column.

## Open questions

These are deliberately left open for the implementing engineer to answer
with real funnel data from Phase 9h:

- **Account linking**: an existing email-signup `owner` returns via
  Google. Do we merge their identities (preferred) or treat as a second
  identity (Zitadel default)? Decision likely requires a Zitadel external-
  IdP-link config flag plus a Limen reconciliation step.
- **Auto-create-on-first-login**: do we mint a tenant on _every_ new
  Zitadel social-login user, or require an explicit "Create tenant" CTA
  post-login? Phase 18 v1 ships the explicit CTA path; auto-create is a
  v2 candidate.
- **Captcha placement**: before vs after the IdP redirect. Phase 18 v1
  ships captcha-before, matching Phase 9h.
- **Apple Sign-In quirks**: the `email` claim is private-relay by
  default and the user may revoke it. Phase 18 v1 declines to fall back
  to Limen-side email-verify in that case — the user must use a real
  email or pick a different IdP.

## Out of scope

- **Enterprise SAML / OIDC SSO signup** — that's the existing IdP-
  federation flow administered via Zitadel Console post-signup
  (per Phase 4 _Self-service delegation_).
- **Just-in-time provisioning for existing tenants** — adding members
  to an existing tenant is `AdminService.InviteMember` (Phase 9c
  Slice 4), not signup.
- **Magic-link email signup** (passwordless) — possible follow-up; not
  prioritised over social.

## Risks

- **Email enumeration via social hop**: Zitadel's social-login response
  reveals whether an email already exists. Mitigation: the SPA must
  treat the social-callback errors the same way Phase 9h treats
  `StartSignup` errors (generic message).
- **Tenant-name squatting**: a stranger could social-sign-up a `pending`
  row holding `tenant_name = "google"`. Mitigation: tenant names are
  display-only (the URL uses `tnt_<ULID>`); a future moderation pass via
  the staff backoffice (Phase 12) can rename or take down abusive names.
- **IdP outages**: a Google or GitHub outage breaks social signup.
  `signup.enabled: true` (Phase 9h email path) must always remain the
  fallback. The SPA already degrades gracefully if no IdPs are enabled.

## Checklist

- [ ] Two new proto RPCs + `IDP` enum
- [ ] `PendingSocialSignup` GORM model + AllModels registration
- [ ] `internal/signup/social.go` handler shares the captcha + rate-limit + sweeper with Phase 9h
- [ ] `GET /auth/discovery` returns the enabled-IdPs list
- [ ] `SignupStart.vue` panel + `SignupSocialCallback.vue` route
- [ ] Playwright e2e for at least one IdP (Google, via a Zitadel-side
      mock IdP wired in `compose.dev.yaml`)
- [ ] Doc updates: Phase 4 _Self-service delegation_ table notes that
      social signup is Limen-owned (mirrors the Phase 9h update)

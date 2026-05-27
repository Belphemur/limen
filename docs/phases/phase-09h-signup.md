---
phase: "9h"
title: "Self-serve signup wizard (Portal-side)"
status: planned
progress: 0
depends_on: ["5", "9a", "9b", "9c"]
updated: "2026-04-30"
---

# Phase 9h — Self-serve signup wizard

> **Status**: ready to implement. Closes the six unticked v1 bullets in
> [Phase 9c](phase-09c-tenant-admin-spa.md) so Phase 9c v1 ships as done.

## Goal

A stranger lands on `/signup`, fills out **tenant name + owner name + owner
email**, solves a captcha, receives a Limen-issued verification email, clicks
the link, sets a password on Limen's verify page, and is bounced into the
standard `/auth/login` flow which lands them on `/t/<tenant>/admin/` signed
in as `owner` — end-to-end, no operator intervention.

Three design tenets distinguish this slice from a naïve "create-the-org-then-
email-the-user" wizard:

1. **Limen owns email verification.** The `StartSignup` RPC sends a
   Limen-minted verification email via SMTP (Mailpit in dev, real relay in
   prod). Zitadel is **not** touched in `StartSignup` beyond a single
   `ListUsers` probe to reject already-registered emails up front.
2. **Zitadel is touched only at `CompleteSignup`.** Org + user + grant are
   created in one atomic step after the email is verified. No orphaned
   Zitadel orgs to garbage-collect; the sweeper only deletes stale Limen
   rows.
3. **The password transits Limen exactly once.** The user types it into
   Limen's `/signup/verify` page; `CompleteSignup` forwards it directly to
   Zitadel's `CreateUser` (no plaintext is logged, persisted, or wrapped
   into errors) and then returns a `/auth/login` URL that reuses the
   normal OIDC dance for the actual sign-in. The Zitadel hosted
   password-init UI is **not** used — Zitadel v4's LoginV2 ignores
   `returnURL` on `/ui/login/password/init`, which would otherwise strand
   the user on the Zitadel console.

Social signup (Google / GitHub / Microsoft) is **out of scope** for this
phase — captured separately in [Phase 18](phase-18-social-signup.md).

## Background

Phase 4 already ships the OIDC RP, the AES-SIV-encrypted `limen_portal`
cookie, and the tenancy resolver. Phase 9a split the gateway and portal
binaries; Phase 9b and 9c shipped the SPA shells and the admin RPC surface.
Phase 9c Slice 1 stubbed the `SignupService` proto + handler skeleton
(`CodeUnimplemented`) and mounted it at root-scoped
`/api/limen.signup.v1.SignupService/*` outside the tenancy / session / role
interceptor stack. Phase 9c Slice 4 shipped Limen-owned member management
via Zitadel User V2 + Authorization V2 pass-through.

This phase implements the `SignupService` body, adds a Limen-owned mailer,
adds three SPA pages, and wires the password-capture handoff into the
standard `/auth/login` OIDC flow.

## Flow

```
StartSignup(name, ownerName, ownerEmail, captchaToken)
  ├─ Verifier.Verify(captchaToken, clientIP)         // hCaptcha / Turnstile / dev-bypass
  ├─ rate-limit per IP (token bucket, in-memory)
  ├─ mint verify_token (32B crypto/rand) + HMAC-SHA256 hash with Phase-2 key
  ├─ INSERT pending_signups (no Zitadel call)
  ├─ mailer.Send(ownerEmail, "Confirm your Limen signup", link)
  │     link = ${baseURL}/signup/verify?token=<plaintext>
  └─ return generic success (same shape whether email is new or already-known)

GET /signup/verify?token=…   (SPA route)
  └─ SPA calls SignupService.VerifyEmail(token)
       ├─ hash(token), lookup row, check not expired (24h), check not completed
       ├─ set email_verified_at = now(), rotate verify_token_hash (single-use)
       ├─ mint completion_token (32B crypto/rand) + HMAC-SHA256 hash, persist on row
       └─ return { completion_token } → SPA navigates to /signup/finish

POST CompleteSignup(completion_token, password)
  ├─ hash(completion_token) → lookup row by completion_token_hash
  ├─ require email_verified_at IS NOT NULL → else FailedPrecondition
  ├─ reject password shorter than 8 chars (InvalidArgument)
  ├─ idempotency: if completed_at IS NOT NULL → return cached response
  ├─ Zitadel calls (first time we touch it beyond StartSignup's ListUsers probe):
  │    ├─ derive base slug from tenant_name (NFKD-fold, lowercase ASCII,
  │    │  collapse non-alphanumerics to '-', cap at 120 chars)
  │    ├─ OrganizationService.CreateOrganization(slug)            → zitadel_org_id
  │    │  ├─ on AlreadyExists: retry with slug-<safeword>-<safeword>,
  │    │  │  cap 5 safe-word attempts then fall back to slug-<8 hex>
  │    │  │  to guarantee termination. tenants.name keeps the user's
  │    │  │  display string verbatim — only the Zitadel slug carries
  │    │  │  the suffix.
  │    │  └─ safe-word list is a curated ~96-entry pool of neutral
  │    │     English words (e.g. "velvet", "otter", "willow") with
  │    │     crypto/rand selection
  │    ├─ UserService.CreateUser(email, given/family,             → zitadel_user_id
  │    │                         email_verified=true,
  │    │                         password=<plaintext>)
  │    ├─ AddUserGrant(user_id, project_id, role="owner")
  │    └─ ManagementService.SetOrgMetadata(org, "limen_tenant_id",
  │                                          tenant.PublicID)
  ├─ INSERT tenants row (PublicID = ids.MustMake(PrefixTenant))
  ├─ UPDATE pending_signups SET completed_at, zitadel_*, tenant_id
  └─ return {
       tenant_public_id,
       redirect_url: "/auth/login?tenant=<pid>&return_to=/t/<pid>/admin/"
     }

Browser → redirect_url → Limen /auth/login (Phase 4 OIDC start)
       → Zitadel hosted login UI (one credential prompt)
       → /auth/callback sets limen_portal cookie
       → /t/<pid>/admin/ landing
```

The plaintext password lives in the Go process across the
`CreateUser` round-trip and nowhere else — it is never logged, persisted,
or wrapped into an error string.

## Persistence — GORM AutoMigrate

Following the repo convention ([`internal/storage/migrate.go`](../../internal/storage/migrate.go)
treats GORM `AutoMigrate` as the source of truth for table DDL), the new
table ships as a model:

```go
// internal/storage/model_pending_signup.go
package storage

import "time"

type PendingSignup struct {
    ID              string     `gorm:"primaryKey;column:id"`              // snp_<ULID>
    EmailLower      string     `gorm:"column:email_lower;index;not null"`
    OwnerGivenName  string     `gorm:"column:owner_given_name;not null"`
    OwnerFamilyName string     `gorm:"column:owner_family_name;not null"`
    TenantName      string     `gorm:"column:tenant_name;not null"`
    IP              string     `gorm:"column:ip;not null"`                // store as TEXT — INET would need a custom type
    VerifyTokenHash     []byte     `gorm:"column:verify_token_hash;uniqueIndex;not null"`
    CompletionTokenHash []byte     `gorm:"column:completion_token_hash;uniqueIndex"`
    EmailVerifiedAt     *time.Time `gorm:"column:email_verified_at"`
    ZitadelOrgID    string     `gorm:"column:zitadel_org_id"`
    ZitadelUserID   string     `gorm:"column:zitadel_user_id"`
    TenantID        *string    `gorm:"column:tenant_id;type:uuid"`
    CreatedAt       time.Time  `gorm:"column:created_at;not null"`
    CompletedAt     *time.Time `gorm:"column:completed_at;index"`
}

func (PendingSignup) TableName() string { return "pending_signups" }
```

Register in `AllModels()` ([`internal/storage/models.go`](../../internal/storage/models.go)).

The `pending_signups` table is **not** RLS-scoped — signup is pre-tenant by
construction. All reads/writes go through `storage.WithSuperuser(ctx)` with
explicit `id` or `verify_token_hash` predicates (per
[internal/storage/AGENTS.md](../../internal/storage/AGENTS.md)).

## Package layout

```
internal/signup/
├── service.go          // SignupService Connect handler (StartSignup, VerifyEmail, CompleteSignup)
├── service_test.go     // integration tests against real Postgres + fake Zitadel + fake mailer
├── captcha.go          // Verifier interface + dev/hcaptcha/turnstile impls
├── captcha_test.go
├── ratelimit.go        // per-IP token bucket
├── ratelimit_test.go
├── tokens.go           // verify-token mint + hash + parse helpers
├── tokens_test.go
├── orgslug.go          // Zitadel-org-name slugifier + safe-word suffix retry helper
├── orgslug_test.go
├── sweeper.go          // periodic delete of stale pending_signups rows
└── sweeper_test.go

internal/mailer/
├── mailer.go           // SMTP wrapper, Mailer interface
├── mailer_test.go
└── templates/
    ├── signup_verify.html.tmpl
    └── signup_verify.txt.tmpl
```

### Tenant display name vs Zitadel org slug

Limen treats `tenants.name` as a **freely-renameable cosmetic label**
owned by the user, and the Zitadel organisation name as an **internal
slug** that exists only to satisfy Zitadel's instance-wide uniqueness
constraint on `organization.name`. The two strings are intentionally
decoupled:

| Concern                             | `tenants.name` (Limen)                      | Zitadel `organization.name`         |
| ----------------------------------- | ------------------------------------------- | ----------------------------------- |
| Storage                             | `tenants.name` (TEXT)                       | Zitadel instance, opaque to Limen   |
| Mutability                          | Owner can rename via `UpdateTenantSettings` | Set once at signup, never updated   |
| Uniqueness                          | Not enforced — collisions allowed           | Enforced by Zitadel instance-wide   |
| Visible to end users                | Yes — every UI surface                      | No — internal identifier            |
| Stable identity for cross-reference | `PublicID` (`tnt_<ULID>`)                   | `zitadel_org_id` on the tenants row |

The slug is derived in `internal/signup/orgslug.go`:

1. NFKD-fold the display name and drop combining marks.
2. Lowercase, replace any non `[a-z0-9]` run with a single `-`, trim
   leading/trailing hyphens.
3. Empty result → `"tenant"`. Cap at 120 characters.
4. On `CreateOrganization` returning `AlreadyExists`, append
   `"-<safeword>-<safeword>"` drawn from a curated ~96-word neutral
   English wordlist (e.g. `"velvet"`, `"otter"`, `"willow"`) with
   `crypto/rand` selection. Cap at 5 safe-word attempts.
5. Final fallback after the safe-word retries: `"-<8 hex chars>"`
   to guarantee statistical termination even under adversarial
   collisions.

Implications for the rest of the system:

- **`UpdateTenantSettings` rename is local-only.** No Zitadel call,
  no uniqueness pre-check. Two tenants can carry the same display
  name; the audit + members + invite surfaces must always show the
  `PublicID` alongside the name as the durable tiebreaker.
- **`tenants.name` is the user's input verbatim.** It is the only
  string the SPA renders; the slug never appears in the product UI.
  Support staff can recover the slug via Zitadel Console or
  `tenants.zitadel_org_id`.
- **Bootstrap and other administrative seeders are unaffected.**
  They keep using the explicit org name they choose; the slug
  derivation is exclusive to `internal/signup`.

### Captcha provider abstraction

```go
type Verifier interface {
    Verify(ctx context.Context, token, clientIP string) error
}
```

Implementations: `hcaptchaVerifier`, `turnstileVerifier`, `devBypassVerifier`
(accepts only the literal token `dev-captcha-bypass` and only when
`cfg.Captcha.Provider == "dev"`). Config:

```yaml
captcha:
  provider: dev | hcaptcha | turnstile
  site_key: ${LIMEN_CAPTCHA_SITE_KEY}
  secret_key: ${LIMEN_CAPTCHA_SECRET_KEY}
```

`site_key` is exposed to the SPA via the existing `GET /auth/discovery`
endpoint (alongside the Zitadel issuer URL) so the widget can be rendered
without a second config endpoint.

### Rate limit

Simple in-memory token bucket keyed by client IP. Defaults: 5 starts per IP
per hour, burst clamped at 3. Config-overridable
(`signup.rate_limit.per_hour`, `signup.rate_limit.burst`). No Redis dependency
— public endpoint, not a hot path; a process restart resets the counters,
which is acceptable for a signup funnel.

### Sweeper

Background goroutine that deletes `pending_signups` rows with
`completed_at IS NULL AND created_at < now() - 24h` every 15 min (jittered).
No Zitadel calls — the whole point of deferring org creation is that there
are no orphaned Zitadel resources to clean up.

Wired into `cmd/portal/main.go` and `cmd/limen/main.go` via the existing
boot-suite pattern ([`internal/boot/serveportal`](../../internal/boot/serveportal/),
[`internal/boot/serveall`](../../internal/boot/serveall/)). The gateway and
staff binaries do **not** run the sweeper.

### Mailer

```go
type Mailer interface {
    Send(ctx context.Context, to, subject, htmlBody, textBody string) error
}
```

`smtpMailer` uses Go's `net/smtp`. Config:

```yaml
mailer:
  smtp:
    host: localhost
    port: 1025
    from: "Limen <noreply@limen.local>"
    username: ${LIMEN_SMTP_USERNAME}
    password: ${LIMEN_SMTP_PASSWORD}
    tls: starttls | tls | none
```

Templates use Go's `html/template` + `text/template` for the HTML and plain
variants. One template pair for this phase (`signup_verify`); the package
interface is generic so future transactional needs (Phase 18 social signup
welcome mail, billing receipts, etc.) reuse it without reshaping.

## Proto

[`proto/limen/signup/v1/signup.proto`](../../proto/limen/signup/v1/signup.proto)
gains one new RPC and reshapes `CompleteSignupResponse`:

```proto
service SignupService {
  rpc StartSignup    (StartSignupRequest)    returns (StartSignupResponse);
  rpc VerifyEmail    (VerifyEmailRequest)    returns (VerifyEmailResponse);
  rpc CompleteSignup (CompleteSignupRequest) returns (CompleteSignupResponse);
}

message StartSignupRequest {
  string tenant_name        = 1;
  string owner_email        = 2;
  string owner_given_name   = 3;
  string owner_family_name  = 4;
  string captcha_token      = 5;
}
message StartSignupResponse {
  // Intentionally empty — generic success regardless of branch outcome
  // (new email vs already-known) so the response cannot be used to enumerate
  // existing accounts.
}

message VerifyEmailRequest  { string token = 1; }
message VerifyEmailResponse {
  // Opaque proof-of-verification handed back to the SPA. The SPA passes
  // it straight into CompleteSignup. Works across browsers/devices —
  // whichever device opens the email link receives the token.
  string completion_token = 1;
}

message CompleteSignupRequest {
  string completion_token = 1;
  string password         = 2;  // forwarded once to Zitadel CreateUser; never persisted
}
message CompleteSignupResponse {
  string tenant_public_id = 1;
  string redirect_url     = 2;  // /auth/login?tenant=<pid>&return_to=/t/<pid>/admin/
}
```

`buf generate` produces Go bindings under
`internal/signup/signupv1/` and TS bindings under
`web/admin/src/gen/limen/signup/v1/`.

## Frontend

Three new pages under `web/admin/src/pages/`:

| Route                 | Component              | Purpose                                                                                  |
| --------------------- | ---------------------- | ---------------------------------------------------------------------------------------- |
| `/signup`             | `SignupStart.vue`      | Tenant name + owner first/last/email + captcha; POSTs `StartSignup`; navigates to next.  |
| `/signup/check-email` | `SignupCheckEmail.vue` | "Check your inbox" landing; entered email displayed; resend button debounced 60 s.       |
| `/signup/verify`      | `SignupVerify.vue`     | Reads `?token=`; calls `VerifyEmail`, then presents a password form; on submit calls `CompleteSignup(token, password)` and navigates to `redirectUrl`. |

All three are part of the **admin SPA** bundle (signup is the admin
shell's tenant-bootstrap surface). The router entries are public (no
session required); they're already declared as `public: true` in the
admin router from Slice 1.

The captcha widget lazy-loads the provider's script in `onMounted` so the
bundle stays small. Dev mode (`provider === "dev"`) skips the widget
entirely and ships the literal `dev-captcha-bypass` token.

The Connect-Web transport for signup pages uses **`baseUrl: ""`** (relative
to origin) instead of the per-tenant base the admin/portal shells use,
because the SignupService is mounted at root-scoped
`/api/limen.signup.v1.SignupService/*` per
[Phase 9c § Slice 1](phase-09c-tenant-admin-spa.md).

### UX principles (`SignupStart.vue`)

The page is the first impression Limen makes on a stranger, so it has to
sell the product **and** convert in one screen. The layout follows the
common SaaS "marketing left / form right" anatomy
([reference](https://userpilot.medium.com/14-best-signup-page-examples-understanding-the-anatomy-of-signup-ui-7495af8427a4)):

| Element             | Why                                                                                                                   |
| ------------------- | --------------------------------------------------------------------------------------------------------------------- |
| Brand header        | Logo + wordmark + "Already have an account? Sign in" — returning users have one click out, new users see the brand.   |
| Eyebrow pill        | `Self-hosted MCP gateway` — categorises the product before the headline so the visitor knows what they're looking at. |
| Benefit headline    | One sentence, value-first: _"One MCP endpoint for every AI tool your team uses."_                                     |
| Supporting copy     | 2–3 lines naming the upstreams and IDE clients so visitors self-qualify.                                              |
| Three-icon benefits | Aggregation, isolation, Code Mode — covers the "what / safe / extensible" axes without a wall of text.                |
| Form card           | Right column, surface-1, shadow-soft. Only essential fields: tenant name + first/last/email (+ captcha in prod).      |
| Form microcopy      | One short reassurance line above the fields: _"You'll be the owner. We'll email a verification link to finish."_      |
| Primary CTA         | `Create my tenant` — first-person, action verb. Full-width, h-11, `bg-primary` for contrast against `surface-1`.      |
| Trust strip         | Below the CTA: `No credit card` · `We never see your password` with icons. Removes the two most common objections.    |
| Legal footnote      | Tiny links to Terms + Privacy under the strip — present but not visually competing with the CTA.                      |
| Page footer         | Repo link, docs, privacy, terms. Confirms Limen is open-source, which is itself a trust signal for an infra product.  |
| Success state       | Mail icon + headline + paragraph + expiry hint + "try again" link. Mirrors `SignupVerify.vue`'s status-card shape.    |

Anti-friction choices the page deliberately makes:

- **No password field.** Passwords are set in Zitadel's hosted form
  after email verification (see flow). Visitors never have to invent or
  evaluate a password to start.
- **No "confirm email" field.** A typo'd email simply means the verify
  link never arrives — the success state's "try again" link is the
  recovery path.
- **No marketing checkboxes.** Newsletter / product-update opt-ins are
  out of scope; if added later, they default to off and live below the
  CTA, never above it.
- **No social login on the start page.** Social sign-up is
  [Phase 18](phase-18-social-signup.md); shipping the form alone keeps
  the conversion path linear in v1.
- **Email enumeration resistance is partial by design** — `StartSignup`
  rejects an email that already maps to a Zitadel user with
  `CodeAlreadyExists` and a user-visible message, because the
  alternative (sending a verification mail to an address whose owner
  could never complete signup, then failing at `CompleteSignup` with
  "User already exists") was strictly worse UX. The probe surface is
  bounded by mandatory captcha + per-IP rate limit, which are the same
  controls a login form uses against the same enumeration vector.

## Routing

**Already wired.** Both [deploy/caddy/Caddyfile.dev](../../deploy/caddy/Caddyfile.dev)
and the Phase 11 production Caddyfile already route `/signup`, `/signup/*`,
`/auth/signup*`, `/api/limen.signup*`, and `/auth/discovery` to the Limen
backend. No Caddy edits required by this phase.

## Configuration

New keys in [config.yaml](../../config.yaml) (with `${ENV_VAR}` substitution):

```yaml
signup:
  enabled: true
  rate_limit:
    per_hour: 5
    burst: 3
  verify_token_ttl: 24h

captcha:
  provider: dev # dev | hcaptcha | turnstile
  site_key: ${LIMEN_CAPTCHA_SITE_KEY}
  secret_key: ${LIMEN_CAPTCHA_SECRET_KEY}

mailer:
  smtp:
    host: localhost
    port: 1025
    from: "Limen <noreply@limen.local>"
    username: ${LIMEN_SMTP_USERNAME}
    password: ${LIMEN_SMTP_PASSWORD}
    tls: none # none | starttls | tls
```

When `signup.enabled: false`, `StartSignup` returns
`CodeFailedPrecondition` and the SPA `/signup` route 404s. Useful for
self-hosted single-tenant deploys.

## Security

- **Captcha**: mandatory in non-dev configs. Server-side verification with
  the secret key; the `clientIP` parameter binds verification to the
  requesting host. Failures return a generic error.
- **Rate limit**: per-IP token bucket on `StartSignup`. Failures return
  `CodeResourceExhausted` with a generic message.
- **Email enumeration**: `StartSignup` calls
  `ZitadelClient.UserExistsByEmail` after captcha + rate-limit and
  returns `CodeAlreadyExists` when the address is already registered.
  This is an intentional trade: see the _Anti-enumeration trade-off_
  bullet above.
- **Verify token**: 32-byte cryptographically-random plaintext (in the
  email link only) hashed with HMAC-SHA256 before storage. Single-use:
  the row's hash is rotated on successful verification.
- **Completion token**: 32-byte cryptographically-random plaintext
  returned by `VerifyEmail` and re-submitted to `CompleteSignup`, hashed
  with HMAC-SHA256 (Phase 2 `LIMEN_TOKEN_ENCRYPTION_KEY`) before storage.
  Carries email-verification proof end-to-end without binding to a
  browser session, so the same flow works across devices (desktop
  start → phone email click → phone completion).
- **Password handling**: the plaintext arrives in `CompleteSignup`,
  is forwarded to Zitadel `CreateUser` in the same call, and is not
  logged, persisted, or wrapped into errors. The Zitadel hosted
  password-init UI is **not** used (its `returnURL` is ignored by
  Zitadel v4 LoginV2 and would strand the user on the Zitadel
  console).
- **Idempotency**: `CompleteSignup` is keyed off `completion_token_hash`
  and short-circuits if `completed_at IS NOT NULL`. The hash is **not**
  rotated on success, so an accidental refresh after completion replays
  the cached tenant + the same `/auth/login` redirect. Replays do not
  reset the Zitadel password — the first call wins.
- **Sweeper**: deletes stale pre-completion rows after 24 h. No Zitadel
  cleanup needed (deferred-creation design).
- **Org-name uniqueness is not a security boundary.** Tenants are
  identified by `PublicID`, isolation is enforced by Postgres RLS, and
  the Zitadel org slug is opaque to end users. Two tenants can hold
  identical display names without weakening any auth or tenancy
  invariant. See _Tenant display name vs Zitadel org slug_ above.

## Telemetry

Structured `zap` log fields on every RPC:

| Field              | Set on                                                                                                                      |
| ------------------ | --------------------------------------------------------------------------------------------------------------------------- |
| `signup.id`        | every RPC after row lookup                                                                                                  |
| `signup.outcome`   | one of `started`, `verified`, `completed`, `idempotent_replay`, `rate_limited`, `captcha_failed`, `expired`, `not_verified` |
| `signup.ip`        | every RPC                                                                                                                   |
| `tenant.public_id` | only on `completed` / `idempotent_replay`                                                                                   |

The `outcome` enum lets Phase 16 (Observability) build a funnel chart
without re-parsing free-text messages.

## Testing

### Integration tests (`internal/signup/service_test.go`)

Use the shared `startPostgres(t)` helper from
[`internal/storage/storage_test.go`](../../internal/storage/storage_test.go)
(postgres:18-alpine via testcontainers-go) and a fake `ZitadelClient` +
fake `Mailer`. Cover at minimum:

1. `StartSignup` happy path (captcha bypass + row inserted + email queued).
2. `StartSignup` rejects on captcha failure with generic error.
3. `StartSignup` rate-limit kicks in after N starts from the same IP;
   different IP still works.
4. `StartSignup` returns the **same** response shape on email-already-
   used as on a fresh email.
5. `VerifyEmail` happy path sets `email_verified_at` and rotates the
   token hash so the same link is single-use.
6. `VerifyEmail` rejects expired tokens with generic error.
7. `CompleteSignup` requires verified state.
8. `CompleteSignup` is idempotent: two calls with the same
   `completion_token` produce the same `tenant_public_id` and one tenant
   row.
9. `CompleteSignup` rolls back cleanly if the Zitadel calls fail
   (Limen-side `tenants` row not created on Zitadel error).
10. Sweeper deletes a >24 h-old pre-completion row; leaves a completed
    row alone.
11. `CompleteSignup` retries with a safe-word suffix when the first
    `CreateOrganization` call returns `AlreadyExists`, and succeeds on
    the second attempt. `tenants.name` keeps the original display
    string verbatim; `zitadel_org_id` references the suffix-disambig
    Zitadel org.
12. `orgslug.slugify` round-trips Unicode-heavy display names
    (`"Société Générale"` → `"societe-generale"`) and falls back to
    `"tenant"` for empty/punctuation-only inputs.

### Frontend unit tests (Vitest)

- `SignupStart.vue` renders the captcha widget only in non-dev provider
  mode; form validates required fields client-side.
- `SignupVerify.vue` parses `?token=`, calls `VerifyEmail`, renders the
  password form, then calls `CompleteSignup` and navigates to
  `redirectUrl` on success.

### End-to-end (Playwright, `web/admin/tests/e2e/signup.spec.ts`)

Full MailHog round-trip:

1. Load `/signup`, fill the form, submit.
2. Poll the MailHog HTTP API until the verification email arrives.
3. Extract the verify link, navigate to it.
4. Assert SPA reaches `SignupVerify.vue`, renders the password form,
   accepts a password, and navigates to `/auth/login?tenant=tnt_*&...`.
5. In the Zitadel hosted login form (driven via Playwright), enter the
   same email + password.
6. Assert `/auth/callback` lands on `/t/tnt_*/admin/`.
7. Assert the admin shell renders with `owner` role.

The MailHog + Zitadel containers are already in `compose.dev.yaml` — the
fixture reuses the existing dev environment.

### Bundle-separation test

`web/admin/tests/e2e/bundle-separation.spec.ts` (Playwright): loads
`/t/<fixture-tenant>/portal/` with a `member`-scoped fixture cookie,
asserts via `page.on('request')` that no request URL contains the admin
chunk-name prefix. This closes the last v1 bullet that isn't signup-scoped.

## Out of scope

- **Social signup** (Google / GitHub / Microsoft) — see
  [Phase 18](phase-18-social-signup.md).
- **Billing / Stripe** — Phase 13.
- **Anti-abuse beyond captcha + IP rate-limit** — no email-domain
  blocklists, no MX-record probing. The staff backoffice (Phase 12)
  handles takedown.
- **Custom domain / subdomain-per-tenant signup** — always `/signup`,
  always issues a `tnt_<ULID>` public ID.
- **Inviting additional members during signup** — owner-only at creation.
  Invites flow through `AdminService.InviteMember` (Phase 9c Slice 4).
- **Email change / re-verification** — out of scope; the owner can change
  their email via Zitadel Console post-signup.

## Risks

- **SMTP relay flakiness in production**: Phase 10 resilience breaker
  must wrap the mailer. Document the dev → prod SMTP swap in Phase 11.
- **Captcha vendor lock-in**: provider is a config knob; site key
  surfaced via `/auth/discovery`. Document the matrix in Phase 11.
- **Zitadel `CreateUser` password-policy rejection**: if Zitadel's
  configured policy rejects the chosen password, `CompleteSignup`
  returns the Zitadel error message verbatim and the SPA re-renders
  the password form. The pending_signups row stays open so a retry
  with a stronger password works without re-verifying email.
- **Verify-link phishing**: the email's `From:` and link host must match
  the configured `baseURL`. Mailer template MUST NOT include any URLs
  outside the configured `${baseURL}` host.

## Checklist

- [ ] `proto/limen/signup/v1/signup.proto` defines `StartSignup`,
      `VerifyEmail`, `CompleteSignup` with the message shapes above
- [ ] `buf generate` produces Go bindings under `internal/signup/signupv1/`
      and TS under `web/admin/src/gen/limen/signup/v1/`
- [ ] `internal/storage/model_pending_signup.go` registered in `AllModels()`;
      `make dev-migrate` creates the table cleanly
- [ ] `internal/signup/` package implements the three RPCs with the
      flow above; idempotency + email-enumeration resistance covered
- [ ] `internal/signup/captcha.go` ships dev-bypass + hCaptcha +
      Turnstile verifiers selected by `cfg.Captcha.Provider`
- [ ] `internal/signup/ratelimit.go` ships an in-memory per-IP token
      bucket with config-overridable defaults
- [ ] `internal/signup/sweeper.go` runs in `serveportal` + `serveall`
      only (not gateway, not staff); deletes stale pre-completion rows
- [ ] `internal/signup/orgslug.go` implements `slugify` + safe-word
      suffix retry helper; `CompleteSignup` retries `CreateOrganization`
      on `AlreadyExists` using `zitadel.IsAlreadyExists`
- [ ] `internal/mailer/` ships an SMTP-backed `Mailer` with HTML + text
      templates for the verify email
- [ ] `GET /auth/discovery` returns the captcha site key alongside the
      existing Zitadel issuer URL
- [ ] `web/admin/src/pages/SignupStart.vue`, `SignupCheckEmail.vue`,
      `SignupVerify.vue` shipped; admin router exposes the three public
      routes
- [ ] Signup pages use `baseUrl: ""` Connect-Web transport (root-scoped
      RPCs), not the tenant-scoped admin/portal transport
- [ ] Integration suite (Postgres + fake Zitadel + fake mailer) covers
      the 10 cases listed above; passes under `go test -race ./...`
- [ ] Playwright e2e drives the full MailHog → Zitadel → admin landing
      round-trip
- [ ] Bundle-separation Playwright test passes
- [ ] Phase 9c v1 checklist: the six unticked signup-scoped bullets
      flip to `[x]` after this phase merges

## Migration from Phase 9c Slice 1

No breaking changes — Slice 1's `StartSignup` / `CompleteSignup` stubs
returning `CodeUnimplemented` are replaced with real bodies, and a third
RPC (`VerifyEmail`) is added. `CompleteSignupResponse` reshapes from
empty to two fields. Per the AGENTS.md engineering posture, the proto
bump is direct — no `v1alpha → v1` aliases, no deprecation shims.

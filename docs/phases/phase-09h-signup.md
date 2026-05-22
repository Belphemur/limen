# Phase 9h — Self-serve signup wizard

> **Status**: ready to implement. Closes the six unticked v1 bullets in
> [Phase 9c](phase-09c-tenant-admin-spa.md) so Phase 9c v1 ships as done.

## Goal

A stranger lands on `/signup`, fills out **tenant name + owner name + owner
email**, solves a captcha, receives a Limen-issued verification email, clicks
the link, sets a password in **Zitadel's hosted UI**, and is bounced back to
their freshly-minted `/t/<tenant>/admin/` dashboard signed in as `owner` —
end-to-end, no operator intervention, no Limen code ever sees the password.

Three design tenets distinguish this slice from a naïve "create-the-org-then-
email-the-user" wizard:

1. **Limen owns email verification.** The `StartSignup` RPC sends a
   Limen-minted verification email via SMTP (MailHog in dev, real relay in
   prod). Zitadel is **not** touched in `StartSignup`.
2. **Zitadel is touched only at `CompleteSignup`.** Org + user + grant are
   created in one atomic step after the email is verified. No orphaned
   Zitadel orgs to garbage-collect; the sweeper only deletes stale Limen
   rows.
3. **Limen never sees the password.** The user sets their password in
   Zitadel's hosted `/ui/login/password/init` form via a one-time code Limen
   mints through Zitadel's `UserService.PasswordReset` with
   `MediumReturnCode`. The plaintext password is POSTed directly to Zitadel.

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
adds three SPA pages, and wires the Zitadel password-init handoff.

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
       ├─ set email_verified_at = now(), clear/rotate token hash (single-use)
       ├─ set pending_signup cookie (AES-SIV, Path=/signup, Secure, HttpOnly,
       │                             SameSite=Lax, TTL 30 min) carrying signup id
       └─ return ok → SPA navigates to /signup/finish

POST CompleteSignup()   (only callable with verified pending_signup cookie)
  ├─ read pending_signup cookie → load row by id
  ├─ require email_verified_at IS NOT NULL → else FailedPrecondition
  ├─ idempotency: if completed_at IS NOT NULL → return cached response
  ├─ Zitadel calls (first time we touch it):
  │    ├─ OrganizationService.CreateOrganization(name)            → zitadel_org_id
  │    ├─ UserService.AddHumanUser(email, given/family,           → zitadel_user_id
  │    │                           email_verified=true, no password)
  │    ├─ AddUserGrant(user_id, project_id, role="owner")
  │    ├─ ManagementService.SetOrgMetadata(org, "limen_tenant_id",
  │    │                                    tenant.PublicID)
  │    └─ UserService.PasswordReset(user_id, MediumReturnCode)    → init_code
  ├─ INSERT tenants row (PublicID = ids.MustMake(PrefixTenant))
  ├─ UPDATE pending_signups SET completed_at, zitadel_*, tenant_id
  ├─ clear pending_signup cookie
  └─ return {
       tenant_public_id,
       password_init_url: "<issuer>/ui/login/password/init?userID=<id>&code=<code>
                            &returnURL=<base>/auth/login?tenant=<pid>
                                       &return_to=/t/<pid>/admin/"
     }

Browser → password_init_url → Zitadel hosted form → password POSTed to Zitadel
       → Zitadel redirects to returnURL
       → Limen /auth/login starts OIDC dance (Phase 4)
       → /auth/callback sets limen_portal cookie
       → /t/<pid>/admin/ landing
```

The dotted line crossing into Zitadel is the **only** place the plaintext
password exists in memory anywhere in the system. Limen's request log,
audit log, and error traces are clean by construction.

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
    VerifyTokenHash []byte     `gorm:"column:verify_token_hash;uniqueIndex;not null"`
    EmailVerifiedAt *time.Time `gorm:"column:email_verified_at"`
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
├── sweeper.go          // periodic delete of stale pending_signups rows
└── sweeper_test.go

internal/mailer/
├── mailer.go           // SMTP wrapper, Mailer interface
├── mailer_test.go
└── templates/
    ├── signup_verify.html.tmpl
    └── signup_verify.txt.tmpl
```

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
message VerifyEmailResponse {}  // success; cookie now carries the verified state

message CompleteSignupRequest {}  // pending_signup cookie identifies the row
message CompleteSignupResponse {
  string tenant_public_id   = 1;
  string password_init_url  = 2;  // Zitadel hosted /ui/login/password/init with code + returnURL
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
| `/signup/verify`      | `SignupVerify.vue`     | Reads `?token=`; calls `VerifyEmail` → `CompleteSignup`; redirects to `passwordInitUrl`. |

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
  pending_signup_cookie_ttl: 30m

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
- **Email enumeration**: `StartSignup` returns the same response shape
  whether the email is new or already-known. Internal-only Debug log on
  the dedup branch.
- **Verify token**: 32-byte cryptographically-random plaintext (in the
  email link only) hashed with HMAC-SHA256 before storage. Single-use:
  the row's hash is rotated on successful verification.
- **`pending_signup` cookie**: AES-SIV-encrypted with the Phase 4
  `LIMEN_TOKEN_ENCRYPTION_KEY`. `Path=/signup`, `HttpOnly`, `Secure`,
  `SameSite=Lax`, 30 min TTL.
- **Password handling**: Limen never receives the plaintext. The
  `password_init_url` carries a Zitadel-minted one-time code; the user
  POSTs the password directly to Zitadel's hosted form.
- **Idempotency**: `CompleteSignup` is keyed off the cookie's signup id
  and short-circuits if `completed_at IS NOT NULL`. A second click of the
  same verify-link cannot double-create a tenant.
- **Sweeper**: deletes stale pre-completion rows after 24 h. No Zitadel
  cleanup needed (deferred-creation design).

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
8. `CompleteSignup` is idempotent: two calls with the same cookie produce
   the same `tenant_public_id` and one tenant row.
9. `CompleteSignup` rolls back cleanly if the Zitadel calls fail
   (Limen-side `tenants` row not created on Zitadel error).
10. Sweeper deletes a >24 h-old pre-completion row; leaves a completed
    row alone.

### Frontend unit tests (Vitest)

- `SignupStart.vue` renders the captcha widget only in non-dev provider
  mode; form validates required fields client-side.
- `SignupVerify.vue` parses `?token=`, calls both RPCs in sequence, sets
  `window.location.href` on success.

### End-to-end (Playwright, `web/admin/tests/e2e/signup.spec.ts`)

Full MailHog round-trip:

1. Load `/signup`, fill the form, submit.
2. Poll the MailHog HTTP API until the verification email arrives.
3. Extract the verify link, navigate to it.
4. Assert SPA reaches `SignupVerify.vue` and redirects to the Zitadel
   password-init URL.
5. In the Zitadel form (driven via Playwright), enter a password.
6. Assert Zitadel redirects through `/auth/login` → `/auth/callback` →
   `/t/tnt_*/admin/`.
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
- **Zitadel hosted password-init UI changes**: the URL shape
  (`<issuer>/ui/login/password/init?userID=…&code=…&returnURL=…`) is a
  stable v2 contract but worth a CI smoke-test against the dev Zitadel
  container, alongside the existing deep-link smoke-tests.
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

# AGENTS.md — `internal/zitadel`

## What this package is

A thin, Limen-shaped wrapper over the official Zitadel Go SDK
(`github.com/zitadel/zitadel-go/v3`). Every outbound call into Zitadel —
org creation, user provisioning, role grants, OIDC application CRUD,
metadata — goes through `*zitadel.Client` here so that:

- the rest of Limen sees Limen-shaped inputs/outputs (not gRPC types),
- auth mode (`pat` / `jwt_key`) and per-call org scoping are handled
  in one place,
- the SDK / API policy below is enforced consistently.

The Zitadel server we target is **v4.x** (see `compose.dev.yaml`).

---

## Zitadel SDK / API policy — read before touching code

The Zitadel API surface is split between **v2 resource-based services**
(the supported path going forward) and **v1 legacy services**
(`management/`, `admin/`, `auth/`, `system/` — still served, but in
maintenance only and no longer extended). The split is documented at
<https://zitadel.com/docs/apis/introduction>.

For this package the rules are:

1. **Use v2 services for every new call**, full stop. v2 covers
   `application/v2`, `authorization/v2`, `org/v2`, `user/v2`,
   `session/v2`, `oidc/v2`, `settings/v2`, `project/v2`, `idp/v2`,
   `webkey/v2`, `instance/v2`, `feature/v2`, `action/v2`, plus their
   `*v2beta` siblings where the stable v2 does not yet exist.
2. **Never call an SDK method that carries a `// Deprecated:` comment.**
   This is enforced by `staticcheck` (SA1019) in
   `golangci-lint run ./...`, which is part of the required pre-commit
   chain. Deprecated _types_ used purely as struct literals (e.g.
   `*userV2.AddHumanUserRequest` inside `AddOrganizationRequest_Admin_Human`)
   are tolerated only because the embedding API requires them; if the
   embedding API gains a non-deprecated alternative, migrate to it.
3. **v1 (`management/`, `admin/`, `auth/`, `system/`) is permitted only
   when no v2 equivalent exists** for the operation we need. Any such
   use must be accompanied by a single-line comment naming the missing
   v2 capability (e.g. `// v2 has no equivalent for X as of v3.29.0`).
   Do not paper over deprecations with `//nolint:staticcheck` — fix the
   call.
4. **Do not add ad-hoc `//nolint` directives** for SA1019. Either
   migrate the call, or — if you genuinely have to use a deprecated
   path — discuss it in the PR and amend the shared `.golangci.yml`
   with a written justification.

### Common v2 mappings

| Need                        | v2 service / method                                           |
| --------------------------- | ------------------------------------------------------------- |
| Create organization         | `OrganizationServiceV2().AddOrganization`                     |
| Set org metadata            | `OrganizationServiceV2().SetOrganizationMetadata`             |
| Create human user           | `UserServiceV2().CreateUser` (NOT `AddHumanUser`, deprecated) |
| Invite user                 | `UserServiceV2().CreateInviteCode`                            |
| Grant a user a project role | `AuthorizationServiceV2().CreateAuthorization`                |
| List user grants            | `AuthorizationServiceV2().ListAuthorizations`                 |
| Create OIDC application     | `ApplicationServiceV2().CreateApplication` (OIDC oneof)       |
| Update OIDC application     | `ApplicationServiceV2().UpdateApplication` (OIDC oneof)       |
| Get/Delete application      | `ApplicationServiceV2().GetApplication` / `DeleteApplication` |

When extending this table, prefer the gRPC service definitions in
`pkg/client/zitadel/*/v2/*.pb.go` over the legacy `management/` blob.

---

## Auth modes

Two modes for the SDK client, selected in config:

- `pat` — personal access token. Useful for local dev.
- `jwt_key` — JWT profile assertion using an org/service-user key. The
  recommended production mode.

Both are constructed in `client.go::New`; downstream code does not care
which is in use.

---

## Org scoping

All v1 management calls require an `x-zitadel-orgid` header injected
via `middleware.SetOrgID(ctx, orgID)`. v2 services generally take
`OrganizationId` as a request field instead and do **not** need the
header. Prefer the explicit request field where the v2 API offers it.

For the still-permitted v1 calls (see policy above), set the header on
ctx exactly once at the top of the wrapper method; downstream gRPC
plumbing picks it up.

---

## Conventions

- One Limen-shaped input struct and one Limen-shaped output struct per
  operation. Never return raw protobuf types from this package.
- Error messages: `fmt.Errorf("zitadel: <verb> ... (org=%q user=%q): %w", ...)`.
- Validate required inputs (org id, user id, etc.) before issuing the
  call. Return a `fmt.Errorf` describing the missing field; do not
  rely on Zitadel's gRPC validator for caller mistakes.
- The wrapper is intentionally thin — no retries, no caching, no fan-out.
  Higher-level orchestration belongs in callers.

## What this package is NOT

- Not a session store. The portal RP and the MCP RS live in
  [`internal/auth`](../auth/AGENTS.md).
- Not a config loader. Zitadel connection details come from
  `internal/config`.
- Not where DCR business logic lives — that's
  [`internal/oauthproxy`](../../docs/phases/phase-05-authorization-server.md).

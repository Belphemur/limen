---
phase: "12"
title: "Staff tenant & backoffice (super-admin)"
status: planned
progress: 0
depends_on: ["0", "3", "4", "9a", "9b", "9j", "10", "11"]
updated: "2026-03-20"
---

# Phase 12 — Staff tenant & backoffice (super-admin)

## Goal

Give the SaaS operator (the human/team running the Limen instance) a first-class surface for operating the platform:

- A dedicated **staff tenant** exists out of the box, isolated from every customer tenant.
- Users in the staff tenant carry a new **`super_admin`** role that grants read-only visibility across every customer tenant and a small set of audited write actions for support.
- A **backoffice SPA** at `/t/_staff/portal/` exposes tenant lists, per-user upstream link health, system status (refresh-queue depth, circuit-breaker states from [Phase 10](phase-10-wiring-hardening.md)), and an audit-log view.

This phase intentionally sits **after** the customer-facing portal (Phase 9b), the impersonation UX & cookie fix (Phase 09J), and hardening (Phase 10), and **after** production deployment (Phase 11) only on paper — in practice the staff bootstrap step must run as part of the first deploy. Phase 11's `limen-migrate` is extended here to provision the staff tenant idempotently.

## Design

### Staff tenant identity

- Reserved tenant: **`_staff`** is mounted directly under `/t/_staff/`. Customer tenants always use a `tnt_<ULID>` `PublicID` as their URL segment (see [Phase 4](phase-04-tenant-auth-session.md)), so the leading underscore is structurally outside the customer namespace — collisions are impossible by construction.
- A new `Tenant.Kind` column (enum `customer` | `staff`) — `customer` is the default; `staff` is enforced unique via a partial index `WHERE kind='staff' AND deleted_at IS NULL`. There is exactly one staff tenant per deployment.
- The staff tenant is bound 1:1 to a Zitadel organization `limen-staff` (created by the bootstrap script alongside the demo org in [Phase 0](phase-00-dev-environment.md)).
- The staff tenant **has no upstream MCP links of its own**. Visiting `/t/_staff/portal/` loads the backoffice routes; everything in the customer dashboard (Upstreams, Members, MCP Clients) is hidden.

### The `super_admin` role

- New project role on the shared Limen Zitadel project: `super_admin`. Bootstrap scripts grant it only to users inside the `limen-staff` org.
- **Authorization rule**: `super_admin` is honored only when the session is in the staff tenant. If a token carries `super_admin` against a customer org (which should never happen under correct provisioning), it is dropped at the role-extraction step in the OIDC RP.
- `super_admin` does **not** subsume `owner`/`admin`/`member` inside customer tenants. Cross-tenant write actions are limited to explicit force-action RPCs that target a specific tenant and are always audited.

### Read-only cross-tenant visibility (RLS staff-mode GUC)

Phase 3 RLS policies are extended with a new GUC: `limen.staff_mode`. When set to `'on'`, `SELECT` policies allow rows from every tenant; `INSERT`/`UPDATE`/`DELETE` policies do **not** consult the GUC and continue to require `limen.tenant_id`. The GUC is set via `set_config('limen.staff_mode', 'on', true)` (transaction-local) by `storage.WithStaffRead(ctx)` — a new helper that wraps `Session(ctx)`.

```sql
-- example: upstream_links SELECT policy after the staff-mode patch
CREATE POLICY upstream_links_select ON upstream_links
  FOR SELECT
  USING (
    tenant_id = current_setting('limen.tenant_id', true)::bigint
    OR current_setting('limen.staff_mode', true) = 'on'
  );

-- WRITE policies are unchanged; staff_mode has no effect on them.
```

Staff backoffice queries call `storage.WithStaffRead(ctx)`. Force-action endpoints (described below) instead set `limen.tenant_id` to the target tenant and run the write under the target's normal RLS — so even staff cannot accidentally cross-write.

### Backoffice feature set (v1)

| Surface             | Reads                                                                                                                                                                               | Writes                                                                                                        |
| ------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------- |
| **Tenants**         | List + search by name or `PublicID`; detail card (members count, configured upstreams, per-state link counts, last activity, Zitadel org id)                                                 | none                                                                                                          |
| **Users**           | Cross-tenant search by Zitadel sub / email; per-user detail (owning tenant, role, last sign-in)                                                                                     | none                                                                                                          |
| **Upstream health** | Every `UpstreamLink` aggregated by upstream × state; filter by `auto_disabled` reason / `needs_relink` age                                                                          | **Force-unlink** (deletes a user's link, audited); **Force re-enable** (clears `AutoDisabledAt`, audited)     |
| **System status**   | Refresh-queue depth, circuit-breaker state per dependency (live from `internal/resilience` registry), Zitadel reachability, Postgres replication lag, current `limen` build version | **Trip / reset breaker** for a named dependency (audited; behind a confirmation modal)                        |
| **Audit log**       | Scrollable + filterable; combines staff actions and customer admin actions                                                                                                          | none                                                                                                          |
| **Settings**        | Bootstrap-time invariants visible read-only (encryption key fingerprint, Zitadel issuer, AAD scheme version)                                                                        | **Rotate AAD label** (queues a re-encryption job, audited) — out of scope for v1, stub the button as disabled |

### Connect-RPC service

New proto: `proto/limen/staff/v1/staff.proto`. Mounted at `/t/_staff/api/limen.staff.v1.StaffService/*`. Selected RPCs:

```proto
service StaffService {
  rpc ListTenants(ListTenantsRequest) returns (ListTenantsResponse);
  rpc GetTenant(GetTenantRequest) returns (Tenant);
  rpc ListUsers(ListUsersRequest) returns (ListUsersResponse);
  rpc ListAllUpstreamLinks(ListAllUpstreamLinksRequest) returns (ListAllUpstreamLinksResponse);
  rpc GetSystemStatus(google.protobuf.Empty) returns (SystemStatus);
  rpc ListAuditLog(ListAuditLogRequest) returns (ListAuditLogResponse);

  rpc ForceUnlinkUpstream(ForceUnlinkUpstreamRequest) returns (google.protobuf.Empty);
  rpc ForceReEnableLink(ForceReEnableLinkRequest) returns (google.protobuf.Empty);
  rpc TripBreaker(TripBreakerRequest) returns (google.protobuf.Empty);
  rpc ResetBreaker(ResetBreakerRequest) returns (google.protobuf.Empty);
}
```

Interceptors: `RequireStaffSession` (tenant `PublicID` == `_staff` reserved literal) + `RequireSuperAdmin` (role claim) + a new `AuditingInterceptor` that records every RPC name, argument digest, staff user, target tenant/user (when extractable), and result status into `staff_audit_log`.

### Backoffice SPA

Shares the [Phase 9b](phase-09b-portal-spa.md) Vue 3 codebase and the same static-host deployment (Caddy `file_server` or Cloudflare Pages, see [Phase 11](phase-11-production-deployment.md)). The shell looks at the tenant segment on boot:

- `_staff` → lazy-load `web/src/staff/*` route bundle; customer routes are not loaded.
- anything else → existing customer routes; staff bundle is not loaded.

Both halves use the same `@connectrpc/connect-web` transport (same-origin), the same Pinia store shape, and the same auth-redirect flow (Phase 4). Splitting at the route-loader level keeps customer browsers from ever downloading the backoffice JS.

### Bootstrap

1. **[Phase 0](phase-00-dev-environment.md)** Zitadel bootstrap script gains an additional step: create the `limen-staff` org, add the `super_admin` project role to the shared Limen project, and create one staff user from `LIMEN_STAFF_BOOTSTRAP_EMAIL` (env var, sent to Mailpit for password setup in dev). Idempotent on repeat runs.
2. **[Phase 11](phase-11-production-deployment.md)** `limen-migrate` runs the staff-tenant ensure step after schema migration: `INSERT ... ON CONFLICT DO NOTHING` for the staff tenant row (kind `staff`, well-known URL segment `_staff`), linked to the Zitadel org id captured from the bootstrap output (passed via env var `LIMEN_STAFF_ZITADEL_ORG_ID`). Refuses to start if the env var is missing in prod — the deploy script verifies it from `secrets/`.
3. CLI: `limen staff bootstrap --email <addr>` for self-host operators running outside the standard Compose stack.

### Audit log table

> **Spec**: [docs/audit.md](../audit.md) is the normative reference for the `audit_events` table shape, partitioning, encryption envelope, action vocabulary, writer API, and read surfaces. This phase ships the migration + Go writer described there; the rest of this section is a Phase-12 summary, not a re-spec.

The audit log is **not staff-only**. Every consequential action — staff, tenant admin, end user, and automated system events — funnels into a single `audit_events` table. Per-actor surfaces (staff backoffice, tenant admin SPA, user portal) project the same rows through different filters. Centralizing the table is what makes it possible to answer questions like "who touched this upstream link in the last 24 h, in any role?" without joining three different logs.

Phase 12-specific responsibilities:

- Ship the migration that creates `audit_events` (partitioned monthly), the partition-creation helper, and the `audit.append(...)` `SECURITY DEFINER` function with grants exactly as [docs/audit.md](../audit.md) lays them out.
- Ship `internal/audit/` with the `Append(ctx, Event)` writer + the AAD-construction helper that targets `crypto.AESSIV.Seal`.
- Retrofit Phase 7's upstream lifecycle events and Phase 8's codemode lifecycle events to route through `audit.Append` once the writer is available. No backfill — pre-retrofit emissions remain only in the zap-log historical record.

#### Read surfaces

- **Staff backoffice** (this phase): unrestricted, paginated, filterable by `actor_type`, `action`, `target_tenant_id`, time range.
- **Tenant admin SPA** ([Phase 9c](phase-09c-tenant-admin-spa.md)): rows where `target_tenant_id = <viewer tenant>` AND `actor_type IN ('user','system')` — admins see their tenant's history, never staff actions performed on the tenant.
- **User portal** ([Phase 9b](phase-09b-portal-spa.md)): rows where `actor_user_id = <viewer user>` OR `target_user_id = <viewer user>` — "my activity". Out of scope for v1 SPA; the row format is the input.

#### Write surfaces (retrofits owned by this phase)

- **Phase 7** emits `upstream.connected`, `upstream.disconnected`, `upstream.link.enabled`, `upstream.link.disabled`, `upstream.link.api_key_rotated`, `upstream.auto_disabled`, `upstream.refresh_failed`. Until Phase 12 ships the writer, these are **structured zap logs** at INFO level carrying the same field set — no backfill is done when the table arrives.
- **[Phase 8](phase-08-per-tenant-injection.md)** emits the codemode lifecycle (`codemode.invocation.started`, `codemode.tool.called`, `codemode.tool.completed`, `codemode.tool.error`, `codemode.invocation.completed`). The two `invocation.*` rows additionally store the **raw script (started) and raw response (completed)** encrypted on the same row — see [docs/audit.md § Encrypted payloads](../audit.md#encrypted-payloads). The runtime zap log carries digests + byte counts only; the encrypted body is the audit row's responsibility. Same retrofit pattern as Phase 7: zap logs first, persisted rows once this phase lands, no backfill.
- **Phase 9b / 9c** emits portal + admin mutations through the writer once available (`mcp_client.revoked`, `tenant.settings.updated`, etc.).
- **Phase 12** (this phase) emits every staff action through the same writer.

A single `internal/audit/` package owns the writer (`audit.Append(ctx, Event)`), the SQL function binding, and the actor extraction from ctx so call sites stay trivial. The full API and the AAD-construction rules live in [docs/audit.md](../audit.md).

## Deliverables

- New `proto/limen/staff/v1/staff.proto` + buf wiring.
- New `internal/staff/` package: RPC handlers, breaker control.
- New `internal/storage/staff.go`: `WithStaffRead(ctx)` helper, staff-mode RLS migration.
- **Staff-managed tenant tags** — a key/value tag namespace owned by the staff backoffice and bound to `tenants` themselves (e.g. `tier=enterprise`, `region=eu`, `pilot=true`). Powers cross-tenant filtering in the Tenants list, future per-tier free-tier overrides, and EU-only feature gates. Schema is the same `tags` / `tag_bindings` shape introduced in [Phase 17](phase-17-policy-engine.md) but rows live in dedicated `staff_tenant_tags` + `staff_tenant_tag_bindings` tables (no RLS — staff-only, accessed via `WithStaffRead`) so customer admins cannot see or author them. The Phase 17 policy evaluator does **not** consult these; this is a staff-side organisational tool, not a runtime gate.
- New `internal/audit/` writer + `audit_events` migration (partitioned, **shared** across user / staff / system actors). Schema, encryption envelope, action vocabulary, and writer API are all specified in [docs/audit.md](../audit.md) — this phase implements that spec.
- Retrofit prior phases (Phase 7 first, then Phase 9b / 9c) to route their existing structured-log audit events through `audit.Append` once the writer is available.
- Extension to [Phase 0](phase-00-dev-environment.md) bootstrap: staff org + `super_admin` role + bootstrap user.
- Extension to [Phase 11](phase-11-production-deployment.md) `limen-migrate`: ensure `_staff` tenant row exists.
- SPA: new `web/src/staff/` route module; shared shell decides which bundle to load on boot.
- `cmd/gateway staff bootstrap` CLI subcommand.
- Docs: this file; runbook update in [Phase 10](phase-10-wiring-hardening.md) for the audit-log query reference.

## Verification

- **Bootstrap idempotency**: run `make dev-reset && make dev` twice; staff tenant row appears exactly once; bootstrap user appears exactly once.
- **Role isolation**: log in as a customer `admin` and attempt any `StaffService` RPC → `permission_denied`. Attempt to grant `super_admin` to a customer user via Zitadel directly → Limen drops the claim with a structured warning log.
- **Cross-tenant read**: as `super_admin`, `ListAllUpstreamLinks` returns rows from at least two distinct customer tenants in a seeded fixture.
- **Cross-tenant write blocked**: attempt a raw `UPDATE upstream_links SET enabled=false` from a staff-mode session → either zero rows affected or RLS denial; the only way to write is through the explicit `Force*` RPCs that set `tenant_id` to the target.
- **Force-unlink audit**: `ForceUnlinkUpstream` deletes the link, writes a `force.unlink` audit row with reason + target ids, customer-side `users_audit` also reflects the change with `acted_by=<staff_sub>`.
- **Bundle separation**: load `/t/_staff/portal/` and `/t/<customer-public-id>/portal/` from a clean cache; the staff bundle is fetched only on the first URL, the customer bundle only on the second.

## Risks

- **Staff-mode GUC drift**: an RLS policy that forgets to consult `limen.staff_mode` on `SELECT` will be invisible to staff. Mitigation: a Phase 12 integration test enumerates every tenant-scoped table and asserts `WithStaffRead` returns the seeded rows; new tables are forced into the test via a `_table_registry` listing.
- **Backoffice leak surface**: the backoffice is a large UI on top of every customer's metadata. Even with redaction, a single XSS or auth bug yields cross-tenant exposure. Mitigations: separate route bundle, separate cookie path `/t/_staff`, separate CSP nonce policy if the SPA introduces inline scripts later, separate Zitadel MFA enforcement on the staff org.

## Checklist

- [ ] `Tenant.Kind` column (`customer` | `staff`) added with partial unique index for `staff`
- [ ] Reserved staff tenant `_staff` documented in [Phase 4](phase-04-tenant-auth-session.md) and enforced in tenant-creation paths (customer URLs use `tnt_<ULID>` `PublicID`s, so collisions are structurally impossible)
- [ ] Zitadel project role `super_admin` defined in the bootstrap script
- [ ] `limen-staff` org bootstrapped idempotently with one staff user
- [ ] `Phase 11 limen-migrate` ensures the `_staff` tenant row exists; refuses to run in prod without `LIMEN_STAFF_ZITADEL_ORG_ID`
- [ ] RLS `SELECT` policies extended with the `limen.staff_mode` clause; write policies left untouched
- [ ] `storage.WithStaffRead(ctx)` helper sets the GUC via `set_config(..., true)` (transaction-local)
- [ ] `proto/limen/staff/v1/staff.proto` defined and codegen wired
- [ ] `internal/staff/` package implements every RPC
- [ ] `RequireStaffSession` + `RequireSuperAdmin` + `AuditingInterceptor` mounted on the staff API
- [ ] `audit_events` migration creates partitioned table + monthly partition helper; `audit.append(...)` SECURITY DEFINER function provisions append-only runtime writes (per [docs/audit.md](../audit.md))
- [ ] `audit_events` schema covers all three actor types (`user` / `staff` / `system`) and is reused by user-facing audit surfaces in Phase 9b / 9c, not just the staff backoffice
- [ ] Phase 7's `upstream.*` audit events — notably `upstream_auto_disabled` with `(tenant_id, user_id, upstream_id, reason, streak_started_at)`, currently a structured zap log — are routed through `audit.Append` once this phase lands; the retrofit is part of this phase's deliverables _(persisted-audit half moved from [Phase 7](phase-07-outbound-upstream.md) — Phase 7 ships the emission as a zap log because the `audit_events` table doesn't exist yet)_
- [ ] [Phase 8](phase-08-per-tenant-injection.md)'s codemode lifecycle events (`codemode.invocation.started`, `codemode.tool.called`, `codemode.tool.completed`, `codemode.tool.error`, `codemode.invocation.completed`) are routed through `audit.Append` with the same redacted field set (digests + byte counts, `codemode_invocation_id` as the join key); when the writer is available, both the invocation row and one row per tool call are persisted under `actor_type='user'`, `target_kind='codemode_invocation'`
- [ ] `audit_events` migration adds `payload_ciphertext` (`BYTEA`), `payload_ciphertext_aad` (`TEXT`), `payload_ciphertext_scheme` (`SMALLINT`); the `codemode_search` / `codemode_execute` invocation rows store the raw script (on `invocation.started`) and the raw response (on `invocation.completed`) AES-SIV-encrypted with AAD `<tenant>|<user>|audit.codemode.<search|execute>.<script|result>`; write failure on key-unavailable propagates as a 500, never a silent drop. Envelope details: [docs/audit.md § Encrypted payloads](../audit.md#encrypted-payloads).
- [ ] Staff backoffice surfaces codemode rows as metadata only (digest + size + outcome); decrypting the ciphertext is operator-offline tooling documented in the [Phase 10](phase-10-wiring-hardening.md) runbook, never an SPA action
- [ ] Payloads truncated above `audit.codemode.max_payload_bytes` (default 256 KiB post-encryption) carry `payload_json.truncated=true` + `payload_json.original_bytes` in cleartext
- [ ] Upstream tokens remain encrypted-at-rest; decryption still happens only inside the upstream-call transport
- [ ] `staff_tenant_tags` + `staff_tenant_tag_bindings` tables (staff-only, no RLS, accessed via `WithStaffRead`); `StaffService` RPCs `ListTenantTags`, `CreateTenantTag`, `BindTenantTag`, `UnbindTenantTag`; Tenants list gains a key/value filter chip row
- [ ] SPA route bundles split: `_staff` lazy-loads the backoffice; customer tenants never download the staff bundle
- [ ] Integration tests cover: role isolation, RLS staff-mode read, write-blocked-from-staff-mode, force-unlink audit row, bundle separation
- [ ] Runbook (Phase 10) updated with the audit-log query examples

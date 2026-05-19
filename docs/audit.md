# Audit Events

> **Status**: design spec. Ships in [Phase 12 — Staff tenant & backoffice](phases/phase-12-staff-backoffice.md). Earlier phases emit the same field set as **structured `zap` logs**; the persisted-row half of every event lands when this spec is implemented, with no backfill.

This document is the **single source of truth** for Limen's audit pipeline:

- The on-disk table (`audit_events`) and partitioning strategy.
- The encryption envelope for high-sensitivity payloads (today: code-mode scripts + responses).
- The actor / target / action vocabulary every phase writes against.
- The append-only write contract enforced via a `SECURITY DEFINER` SQL function.
- The read surfaces that project the table differently for staff, tenant admins, and end users.

Anything in this file should be read as **normative** for any code or migration that touches `audit_events`. Per-phase docs reference it; they do not duplicate it.

---

## Design goals

| Goal                                         | Rationale                                                                                                                                                                                                                                                                    |
| -------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **One table, all actors**                    | A single `audit_events` table for staff, tenant admin, end user, and system events lets us answer "who touched X in the last 24 h?" without joining three logs. Per-actor surfaces are SQL projections, not separate tables.                                                 |
| **Append-only at the runtime layer**         | The runtime DB role (`limen_app`) can `INSERT` but cannot `UPDATE` or `DELETE`. Mutation is reserved to operator tooling running under `limen_admin`. Enforced by SQL grants + a `SECURITY DEFINER` writer function, not by application convention.                          |
| **Encrypt the high-sensitivity bodies**      | Code-mode scripts and responses are tenant-supplied JavaScript and upstream replies. They are forensically valuable but a leak liability; they live encrypted on the same row, decrypted only by offline operator tooling holding the master key.                            |
| **Structured `zap` first, persisted second** | Earlier phases (Phase 7 upstream lifecycle, Phase 8 codemode lifecycle, Phase 9b portal mutations) ship the emission as structured logs immediately. The persisted row is a retrofit when the writer lands. No backfill — the zap logs are the historical record for the gap. |
| **Partition by time**                        | `RANGE (occurred_at)`, monthly. Retention is operator-configurable (default ≥ 24 months). Old partitions detach and ship to cold storage; nothing in the application code references partitions directly.                                                                    |

Non-goals (v1):

- Tamper-evident chaining (hash-linked rows, Merkle trees). The `SECURITY DEFINER` + grant story is the v1 tamper-resistance posture. A future phase may add chaining.
- Real-time streaming to an external SIEM. Out of scope; the table is the source, and a downstream sink can be added by an operator without schema changes.
- Per-tenant retention policies. Retention is platform-wide for v1.

---

## Table structure

```sql
CREATE TYPE audit_actor_type AS ENUM ('user', 'staff', 'system');

CREATE TABLE audit_events (
  id                          BIGSERIAL,
  public_id                   TEXT        NOT NULL,             -- aev_<ulid>
  occurred_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),

  -- Actor ------------------------------------------------------------------
  actor_type                  audit_actor_type NOT NULL,
  actor_user_id               BIGINT      REFERENCES users(id),     -- NULL when actor_type='system'
  actor_tenant_id             BIGINT      REFERENCES tenants(id),   -- NULL for system / staff cross-tenant
  on_behalf_of_user_id        BIGINT      REFERENCES users(id),     -- impersonation rows: the customer subject

  -- Action -----------------------------------------------------------------
  action                      TEXT        NOT NULL,             -- see "Action vocabulary" below
  result                      TEXT        NOT NULL,             -- 'ok' | 'error:<code>'
  reason                      TEXT,                             -- required for staff impersonation / force-actions

  -- Target -----------------------------------------------------------------
  target_tenant_id            BIGINT      REFERENCES tenants(id),
  target_user_id              BIGINT      REFERENCES users(id),
  target_kind                 TEXT,                             -- 'upstream_link', 'mcp_client', 'codemode_invocation', 'breaker', ...
  target_public_id            TEXT,                             -- public ID of the target row when applicable

  -- Payloads ---------------------------------------------------------------
  payload_json                JSONB       NOT NULL DEFAULT '{}', -- redacted / digested fields (always cleartext)
  payload_ciphertext          BYTEA,                             -- AES-SIV ciphertext (see "Encrypted payloads")
  payload_ciphertext_aad      TEXT,                              -- the AAD used; echoed for rotations
  payload_ciphertext_scheme   SMALLINT,                          -- ciphertext-layout version, starts at 1

  -- Lifecycle --------------------------------------------------------------
  ended_at                    TIMESTAMPTZ,                       -- impersonation rows: set on end

  PRIMARY KEY (id, occurred_at)                                 -- composite is required for partitioned PK
) PARTITION BY RANGE (occurred_at);

CREATE UNIQUE INDEX audit_events_public_id_uq ON audit_events (public_id);

CREATE INDEX audit_events_target_tenant_idx ON audit_events (target_tenant_id, occurred_at DESC);
CREATE INDEX audit_events_actor_user_idx    ON audit_events (actor_user_id, occurred_at DESC);
CREATE INDEX audit_events_action_idx        ON audit_events (action, occurred_at DESC);
CREATE INDEX audit_events_target_kind_idx   ON audit_events (target_kind, occurred_at DESC) WHERE target_kind IS NOT NULL;
```

Notes on the shape:

- The PK is composite `(id, occurred_at)` because Postgres partitioned tables require the partition key to be part of every unique constraint. `public_id` is uniquely indexed separately for API lookups.
- `payload_json` is **always cleartext**. Anything sensitive enough to need encryption belongs in `payload_ciphertext`, not in this column.
- `actor_tenant_id` and `target_tenant_id` are distinct on purpose. For non-staff rows they match; for staff rows they diverge (`actor_tenant_id` is the staff tenant, `target_tenant_id` is the customer tenant being touched).
- `on_behalf_of_user_id` is **only** populated on impersonation-context rows. Don't reuse it as a generic actor field.

### Partitioning

`RANGE (occurred_at)`, **monthly**. A scheduled job in the operator runbook (see [Phase 10 hardening](phases/phase-10-wiring-hardening.md) runbook section) pre-creates the next month's partition. Retention is configured under `audit.retention_months` (default 24); the same job detaches and ships partitions older than the retention window to cold storage.

If a partition is missing at write time the `audit.append(...)` function fails the insert and the caller surfaces it as an error — never falls back to a default partition.

### Grants

| Role                                                       | Grants on `audit_events` |
| ---------------------------------------------------------- | ------------------------ |
| `limen_admin` (migrations + operator tooling, `BYPASSRLS`) | `ALL`                    |
| `limen_app` (runtime)                                      | `SELECT` only            |
| `limen_app` (runtime) on `audit.append(...)`               | `EXECUTE`                |

The runtime role **cannot** `INSERT`, `UPDATE`, or `DELETE` directly. Every write goes through `audit.append(...)`, which is `SECURITY DEFINER`-owned by `limen_admin`. This means application code cannot delete or rewrite existing rows even with a compromised query; the worst case is appending a spurious row, which itself becomes visible evidence.

### RLS

`audit_events` is **not** under tenant-RLS. The cross-tenant staff backoffice needs unconstrained reads, and tenant-scoped views are implemented as parameterized `WHERE` clauses inside dedicated read RPCs (see _Read surfaces_). This is deliberate: an RLS bug must not silently hide audit rows.

---

## Encrypted payloads

Some events are forensically valuable but carry tenant-sensitive content (code-mode JS source, upstream replies that may include API responses). For those, the literal body is stored on the same row, **encrypted at rest** with AES-SIV (RFC 5297) via [internal/crypto](../internal/crypto/aessiv.go) — the same primitive used for upstream tokens in [Phase 2 / Phase 7](phases/phase-02-crypto-config.md).

### Envelope

| Column                      | Content                                                                                                                                            |
| --------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| `payload_ciphertext`        | AES-SIV ciphertext. Includes the SIV tag. Decryptable only with the master key + matching AAD.                                                     |
| `payload_ciphertext_aad`    | The literal AAD string used during encryption. Stored cleartext so a rotation or re-key tool can reason about it without DB-wide context.          |
| `payload_ciphertext_scheme` | Integer starting at `1`. Lets a future phase rotate AAD layout, payload codec, or algorithm without ambiguity. Decryption refuses unknown schemes. |

### AAD construction

The AAD reuses the Phase 2 convention `<tenant>|<user>|<purpose>`. Concretely, for code-mode rows:

| Row                                             | AAD                 |
| ----------------------------------------------- | ------------------- | ---------------- | ---------------------------------------- |
| `codemode.invocation.started` (script body)     | `<tenant_public_id> | <user_public_id> | audit.codemode.<search\|execute>.script` |
| `codemode.invocation.completed` (response body) | `<tenant_public_id> | <user_public_id> | audit.codemode.<search\|execute>.result` |

`<search|execute>` is the MCP tool name the invocation came in on (`codemode_search` vs `codemode_execute`). Mismatched AAD on decryption is a hard fail.

### Write contract

Encryption is **mandatory** when the event class is encryption-eligible:

- If the master key is unavailable at write time, `audit.append(...)` fails the insert and the calling handler surfaces a 500. Plaintext is never substituted, the row is never dropped silently.
- If the payload exceeds `audit.codemode.max_payload_bytes` (default `262144` — 256 KiB post-encryption), the payload is **truncated before encryption** and `payload_json` carries `{"truncated": true, "original_bytes": <n>}` in cleartext. The truncation marker is itself encryption-eligible only for the body; the marker is meant to be visible.

### Read contract

- The staff backoffice **never** decrypts in the SPA, even with `super_admin`. UI shows metadata only: tool name, digest, byte count, outcome, with a banner _"encrypted payload available via offline tooling."_
- Decryption is an operator-only offline path documented in the [Phase 10 hardening](phases/phase-10-wiring-hardening.md) runbook: load the master key from operator secrets, fetch the row, decrypt locally, emit to operator stdout. The plaintext never traverses the gateway runtime.
- Tenant users see their own activity through the [Phase 9b portal](phases/phase-09b-portal-spa.md) "my activity" view as metadata only — never the encrypted body.

### Eligible event classes

Today (Phase 8 / 12):

| Action                          | Encrypted content                                            | AAD purpose suffix                        |
| ------------------------------- | ------------------------------------------------------------ | ----------------------------------------- |
| `codemode.invocation.started`   | Raw script source for `codemode_search` / `codemode_execute` | `audit.codemode.<search\|execute>.script` |
| `codemode.invocation.completed` | Raw response returned by the sandbox                         | `audit.codemode.<search\|execute>.result` |

Other event classes leave `payload_ciphertext` `NULL` and put the necessary digested fields in `payload_json`. The encryption columns are reusable: future high-sensitivity events register their own AAD purpose suffix in this table.

---

## Action vocabulary

`action` is a free-form `TEXT` column but the values are namespaced — `<domain>.<noun>.<verb>` — and every phase that emits events must register them here. The list below is the v1 closed set; new actions go through a doc PR against this file.

| Action                          | Owner                                              | Actor               | Encrypted body?    | Notes                                                                              |
| ------------------------------- | -------------------------------------------------- | ------------------- | ------------------ | ---------------------------------------------------------------------------------- |
| `upstream.connected`            | [Phase 7](phases/phase-07-outbound-upstream.md)    | `user`              | no                 | `target_kind='upstream_link'`                                                      |
| `upstream.disconnected`         | Phase 7                                            | `user`              | no                 |                                                                                    |
| `upstream.link.enabled`         | Phase 7                                            | `user`              | no                 |                                                                                    |
| `upstream.link.disabled`        | Phase 7                                            | `user`              | no                 |                                                                                    |
| `upstream.link.api_key_rotated` | Phase 7                                            | `user`              | no                 | `static_header` user-mode key refresh                                              |
| `upstream.auto_disabled`        | Phase 7                                            | `system`            | no                 | `payload_json` carries `reason`, `streak_started_at`                               |
| `upstream.refresh_failed`       | Phase 7                                            | `system`            | no                 | `payload_json` carries `error_kind`                                                |
| `codemode.invocation.started`   | [Phase 8](phases/phase-08-per-tenant-injection.md) | `user`              | **yes** (script)   | `target_kind='codemode_invocation'`, `target_public_id=cmi_<ulid>`                 |
| `codemode.tool.called`          | Phase 8                                            | `user`              | no                 | `payload_json` carries `upstream`, `tool`, `args_sha256`, `args_bytes`, `call_seq` |
| `codemode.tool.completed`       | Phase 8                                            | `user`              | no                 | `payload_json` carries `outcome`, `duration_ms`, `result_bytes`                    |
| `codemode.tool.error`           | Phase 8                                            | `user`              | no                 | `payload_json` carries `error_kind`, redacted `error_message`                      |
| `codemode.invocation.completed` | Phase 8                                            | `user`              | **yes** (response) | `payload_json` carries `outcome`, `duration_ms`, `tool_calls_total`                |
| `mcp_client.revoked`            | [Phase 9b](phases/phase-09b-portal-spa.md)           | `user`              | no                 | Portal action                                                                      |
| `tenant.settings.updated`       | [Phase 9c](phases/phase-09c-tenant-admin-spa.md)   | `user`              | no                 |                                                                                    |
| `staff.impersonate.start`       | [Phase 12](phases/phase-12-staff-backoffice.md)    | `staff`             | no                 | `reason` required; `target_user_id` set                                            |
| `staff.impersonate.end`         | Phase 12                                           | `staff` or `system` | no                 | `system` when the 15-min TTL fires                                                 |
| `staff.force.unlink`            | Phase 12                                           | `staff`             | no                 | `reason` required                                                                  |
| `staff.force.reenable`          | Phase 12                                           | `staff`             | no                 | `reason` required                                                                  |
| `staff.breaker.trip`            | Phase 12                                           | `staff`             | no                 | `target_kind='breaker'`                                                            |
| `staff.breaker.reset`           | Phase 12                                           | `staff`             | no                 |                                                                                    |

---

## Writer API

A single Go package `internal/audit/` owns the writer. Call sites do not touch SQL.

```go
package audit

type Actor struct {
    Type    Type      // user | staff | system
    UserID  *uint64
    TenantID *uint64
}

type Event struct {
    Actor          Actor
    OnBehalfOfUser *uint64 // impersonation
    Action         string  // from the vocabulary table
    Result         string  // "ok" or "error:<code>"
    Reason         string  // required for staff.* / impersonation
    TargetTenant   *uint64
    TargetUser     *uint64
    TargetKind     string
    TargetPublicID string
    PayloadJSON    map[string]any   // cleartext, always
    Encrypted      *EncryptedBlob   // optional; populated for codemode.invocation.started/completed
}

type EncryptedBlob struct {
    Plaintext []byte
    AADPurpose string // e.g. "audit.codemode.search.script"
}

// Append writes the row. ctx must carry the request actor's tenant + user
// (the tenancy resolver and OIDC middleware ensure this). The writer
// extracts those, fills any missing actor fields from ctx, encrypts the
// blob if present, and calls audit.append(...) inside the active session.
func Append(ctx context.Context, e Event) error
```

The writer:

1. Validates required fields (`Action`, `Result`, and `Reason` for `staff.*` / impersonation actions).
2. If `Encrypted` is set, calls `crypto.AESSIV.Seal(plaintext, aad)` where `aad = "<tenant_public_id>|<user_public_id>|<aad_purpose>"`. Failure → return the error; **do not** insert.
3. Truncates the plaintext to `audit.codemode.max_payload_bytes` (post-encryption ceiling), setting `payload_json.truncated=true` + `payload_json.original_bytes`.
4. Calls `audit.append(...)` (the `SECURITY DEFINER` function) with the materialized parameters.

The writer is **idempotent on `public_id`**: callers may supply a precomputed `aev_<ulid>` (e.g. when retrying after a transient DB error). If omitted, the writer mints one.

### SECURITY DEFINER function

```sql
CREATE FUNCTION audit.append(
  p_public_id              TEXT,
  p_occurred_at            TIMESTAMPTZ,
  p_actor_type             audit_actor_type,
  p_actor_user_id          BIGINT,
  p_actor_tenant_id        BIGINT,
  p_on_behalf_of_user_id   BIGINT,
  p_action                 TEXT,
  p_result                 TEXT,
  p_reason                 TEXT,
  p_target_tenant_id       BIGINT,
  p_target_user_id         BIGINT,
  p_target_kind            TEXT,
  p_target_public_id       TEXT,
  p_payload_json           JSONB,
  p_payload_ciphertext     BYTEA,
  p_payload_ciphertext_aad TEXT,
  p_payload_ciphertext_scheme SMALLINT
) RETURNS BIGINT
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
  v_id BIGINT;
BEGIN
  INSERT INTO audit_events (
    public_id, occurred_at, actor_type, actor_user_id, actor_tenant_id,
    on_behalf_of_user_id, action, result, reason,
    target_tenant_id, target_user_id, target_kind, target_public_id,
    payload_json,
    payload_ciphertext, payload_ciphertext_aad, payload_ciphertext_scheme
  ) VALUES (
    p_public_id, COALESCE(p_occurred_at, now()), p_actor_type, p_actor_user_id, p_actor_tenant_id,
    p_on_behalf_of_user_id, p_action, p_result, p_reason,
    p_target_tenant_id, p_target_user_id, p_target_kind, p_target_public_id,
    COALESCE(p_payload_json, '{}'::jsonb),
    p_payload_ciphertext, p_payload_ciphertext_aad, p_payload_ciphertext_scheme
  )
  RETURNING id INTO v_id;
  RETURN v_id;
END;
$$;

REVOKE ALL ON FUNCTION audit.append(...) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION audit.append(...) TO limen_app;
```

The function is intentionally a thin insert with no policy logic — the policy decisions (who can act, what reasons are required, what gets encrypted) are enforced by the Go writer. Putting them in PL/pgSQL would scatter the policy across two layers.

---

## Read surfaces

| Surface                                                                 | Filter                                                                                                                                                                         |
| ----------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **Staff backoffice** ([Phase 12](phases/phase-12-staff-backoffice.md))  | Unrestricted. Pagination by `(occurred_at DESC, id DESC)` cursor. Filters: `actor_type`, `action`, `target_tenant_id`, time range.                                             |
| **Tenant admin SPA** ([Phase 9c](phases/phase-09c-tenant-admin-spa.md)) | `target_tenant_id = <viewer tenant>` AND `actor_type IN ('user','system')`. Admins see their tenant's history; staff actions performed on the tenant are intentionally hidden. |
| **User portal** ([Phase 9b](phases/phase-09b-portal-spa.md))              | `actor_user_id = <viewer user>` OR `target_user_id = <viewer user>`. v1 SPA does not ship this view; the row shape is the input for a v1.x add-on.                             |

None of these surfaces decrypts `payload_ciphertext`. The encrypted body is operator-offline only.

---

## Cross-references

- Crypto primitive: [internal/crypto/aessiv.go](../internal/crypto/aessiv.go)
- Phase that owns the table + writer: [Phase 12](phases/phase-12-staff-backoffice.md)
- Phase emitting codemode events: [Phase 8](phases/phase-08-per-tenant-injection.md)
- Phase emitting upstream events: [Phase 7](phases/phase-07-outbound-upstream.md)
- Operator runbook for offline decryption: [Phase 10](phases/phase-10-wiring-hardening.md) (added to `docs/runbook.md` when that ships)
- Database conventions: [database.md](database.md)
- Security model: [security.md](security.md)

# Phase 17 — Observability, active-user billing & service accounts

**Depends on**: [Phase 6](phase-06-resource-server.md) (tool-call hot path),
[Phase 8](phase-08-per-tenant-injection.md) +
[Phase 8b](phase-08b-codemode-async-tool-calls.md) (codemode dispatch),
[Phase 9c](phase-09c-tenant-admin-spa.md) (tenant admin SPA),
[Phase 11](phase-11-production-deployment.md) (production wiring),
[Phase 13](phase-13-billing-stripe.md) (Stripe per-seat billing —
**this phase changes the seat definition** from "Zitadel grant" to
"monthly active user"),
[`docs/audit.md`](../audit.md) (sibling audit pipeline).
**Unblocks**: tenant admins see real usage; ops gets Prometheus signals;
billing only charges for users who actually used the gateway; service
accounts can exist without inflating the seat count.

## Goal

Three closely-related capabilities served by **one event stream**.
Splitting them across phases would duplicate the recording pipeline,
so they ship together.

1. **Per-user, per-tool observability.** Every `tools/call` (direct or
   from inside codemode) records `success` / `failure` asynchronously,
   without blocking the dispatch path. Tenant admins see a dashboard
   with success / failure counts, top tools, top users, latency, and a
   24 h request-volume sparkline (the mock in the user's brief).
2. **Prometheus metrics** for SaaS ops — RED metrics (Rate, Errors,
   Duration) per upstream + per dispatch surface, plus
   tenant-aggregated gauges. Cardinality is bounded; we never label by
   `user_id`.
3. **Active-user billing.** A user becomes a billable seat in a given
   calendar month iff they made at least one successful (or failed —
   see "What counts" below) `tools/call` in that month. This replaces
   the [Phase 13](phase-13-billing-stripe.md) "count Zitadel grants"
   definition. Service accounts (new entity) are billed under a
   separate line-item and are **not** grants.

## Non-goals (v1)

- **Per-tool / per-upstream usage-based pricing.** [Phase 13](phase-13-billing-stripe.md)
  already calls this out. The event stream is the building block; the
  Stripe metering wiring is not in this phase.
- **Time-series retention beyond 90 days** of raw events. Older data
  rolls up into a monthly aggregate (kept indefinitely for billing
  audit). Tenant admins see "last 90 days" in the dashboard; longer
  ranges hit the aggregate.
- **A query language for the dashboard.** Fixed panels with filter
  chips. No PromQL-style box in v1.
- **Tracing.** Spans, OpenTelemetry export, distributed traces — out of
  scope. RED metrics + structured logs cover the v1 needs.

## Design

### The event stream

One table, one writer, one consumer per concern. DRY says: do not
write a `tool_call_metrics` table for the dashboard and a separate
`active_user_marks` table for billing — they read the same event.

```sql
CREATE TABLE tool_call_events (
  id              BIGSERIAL,
  occurred_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

  tenant_id       BIGINT       NOT NULL REFERENCES tenants(id),
  -- exactly one of user_id / service_account_id is set
  user_id             BIGINT   REFERENCES users(id),
  service_account_id  BIGINT   REFERENCES service_accounts(id),

  upstream_id     BIGINT       NOT NULL REFERENCES upstreams(id),
  tool_name       TEXT         NOT NULL,             -- after normalization (Phase 14 territory)
  surface         TEXT         NOT NULL,             -- 'mcp_direct' | 'codemode'

  outcome         TEXT         NOT NULL,             -- 'ok' | 'error'
  error_code      TEXT,                              -- 'policy_denied' | 'upstream_unavailable' | 'tool_error' | ...
  duration_ms     INTEGER      NOT NULL,             -- dispatch-level, not including upstream auth refresh
  request_bytes   INTEGER      NOT NULL DEFAULT 0,
  response_bytes  INTEGER      NOT NULL DEFAULT 0,

  PRIMARY KEY (id, occurred_at)
) PARTITION BY RANGE (occurred_at);

-- monthly partitions, retention = 90 d on raw; older partitions detach
-- after their rows have been folded into tool_call_monthly (below).

CREATE INDEX tool_call_events_tenant_time  ON tool_call_events (tenant_id, occurred_at DESC);
CREATE INDEX tool_call_events_tenant_user  ON tool_call_events (tenant_id, user_id, occurred_at DESC) WHERE user_id IS NOT NULL;
CREATE INDEX tool_call_events_tenant_upst  ON tool_call_events (tenant_id, upstream_id, occurred_at DESC);
CREATE INDEX tool_call_events_tenant_tool  ON tool_call_events (tenant_id, tool_name, occurred_at DESC);
```

Why a new table and not `audit_events`:

- Cardinality is orders of magnitude higher than audit. A busy tenant
  emits millions of tool calls per month and a few thousand audit
  rows. Mixing them either bloats audit indexes or starves the
  hot-path of insert throughput.
- Schema is different in shape (numeric `duration_ms`, byte counters)
  and intent (operational metrics, not provenance). They share the
  partitioning + retention idioms from [`docs/audit.md`](../audit.md)
  but nothing more.
- The `payload_*` encryption envelope from audit is not needed —
  there is no script body or upstream payload on a metric row.

RLS-forced under the standard tenant policy; staff cross-tenant
`SELECT` honours `limen.staff_mode`
([Phase 12](phase-12-staff-backoffice.md)).

### Async writer (non-blocking)

The dispatch path **must not** block on metric persistence. The same
in-memory channel + drain goroutine pattern that
[Phase 13's webhook handler](phase-13-billing-stripe.md#webhook-endpoint)
uses applies here, with tuned parameters:

```
internal/observability/recorder.go

type Recorder struct { ch chan Event; ... }

func (r *Recorder) Record(ev Event) {
    select {
    case r.ch <- ev:                  // happy path
    default:
        atomic.AddUint64(&r.dropped, 1) // shed load instead of stalling
    }
}
```

- Buffer size: 8192 events. At a sustained 1 K req/s the drain has
  ~8 s of head-room before shedding.
- Drain goroutine batches up to 256 events or 250 ms, whichever first,
  and `COPY`s into Postgres via `pgx`'s `CopyFrom`. (GORM is fine for
  CRUD; not for hot-path bulk inserts.)
- `dropped` counter is exported as a Prometheus gauge —
  `limen_observability_dropped_total` — alerting threshold lives in
  the ops runbook, not the code.
- The drain owns its own pool connection (`storage.WithSuperuser` is
  **not** needed; tenant id travels on each row and RLS still applies
  via the standard `limen_app` pool — the writer sets
  `limen.tenant_id` per `COPY` batch by sorting events by tenant and
  emitting one `SET LOCAL` + `COPY` per tenant inside one transaction).

The dispatch hook is a single call at one site — `internal/mcprs/`'s
tool-call wrapper — for both direct MCP and codemode-driven calls.
Codemode does **not** call `Record` itself; it calls the wrapped
dispatch, which records once. That keeps "one tool call = one row"
invariant intact even when the same script makes 50 fan-out calls.

### What counts

| Outcome class                                                | Recorded?                                          | Counts as "active" for billing?                                        |
| ------------------------------------------------------------ | -------------------------------------------------- | ---------------------------------------------------------------------- |
| `ok`                                                         | yes                                                | yes                                                                    |
| `error:tool_error` (upstream returned an error MCP response) | yes                                                | yes — the user did successfully invoke the gateway                     |
| `error:upstream_unavailable`                                 | yes                                                | no — gateway-side failure, not a usage signal                          |
| `error:policy_denied`                                        | yes                                                | no — the user was refused; charging them for a denial would be hostile |
| Validation errors before dispatch (malformed JSON-RPC, etc.) | logged only, **not** recorded as a tool_call_event | no                                                                     |

The billing column is encoded as a generated boolean column
`billable_active BOOLEAN GENERATED ALWAYS AS (outcome = 'ok' OR error_code = 'tool_error') STORED` so the
active-user query is a simple `SELECT DISTINCT user_id WHERE
billable_active AND occurred_at >= <month_start>`.

### Monthly rollup + active-user mark

A new table tracks "who was active in month M" so the billing query
doesn't have to scan 30 days of raw events.

```sql
CREATE TABLE active_user_months (
  tenant_id       BIGINT NOT NULL REFERENCES tenants(id),
  month_start     DATE   NOT NULL,                    -- first day of the calendar month UTC
  user_id         BIGINT REFERENCES users(id),
  service_account_id BIGINT REFERENCES service_accounts(id),
  first_seen_at   TIMESTAMPTZ NOT NULL,
  last_seen_at    TIMESTAMPTZ NOT NULL,
  call_count      INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, month_start, user_id, service_account_id)
);
```

Writer maintains it incrementally: on every batched insert, an upsert
of `(tenant_id, month_start, subject)` with `call_count = call_count +
batch_count_for_that_subject` and a `last_seen_at = max(...)`. Cheap;
runs in the same transaction as the `COPY`.

For aggregated dashboard panels, a second rollup is computed by a
cron-like background job (every 5 min, jittered):

```sql
CREATE MATERIALIZED VIEW tool_call_5m AS
SELECT tenant_id, date_trunc('5 minutes', occurred_at) AS bucket,
       upstream_id, surface, outcome,
       count(*)::int               AS calls,
       avg(duration_ms)::int       AS avg_duration_ms,
       percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_ms)::int AS p95_duration_ms
FROM tool_call_events
GROUP BY 1,2,3,4,5;

REFRESH MATERIALIZED VIEW CONCURRENTLY tool_call_5m;  -- every 5 min
```

Dashboard panels query the materialized view for everything except
the "live" 5 min, which they pull from raw `tool_call_events`. The
hybrid is invisible to the SPA.

### Tenant admin dashboard

Mirrors the user-supplied mock. New `/t/<tenant>/portal/admin/dashboard`
route (the existing "Dashboard" landing in the admin SPA already
points here). Panels:

| Card                           | Source                                                                                             |
| ------------------------------ | -------------------------------------------------------------------------------------------------- |
| Active MCP servers (X / total) | `upstreams` row count + per-upstream health from [Phase 10](phase-10-wiring-hardening.md) breakers |
| Connected clients              | distinct `actor_user_id` from session activity (last hour)                                         |
| Total requests / 24 h          | `sum(calls)` on `tool_call_5m` filtered to last 24 h                                               |
| Error rate                     | `sum(calls) WHERE outcome='error' / sum(calls)` last 24 h                                          |
| Request-volume sparkline       | `tool_call_5m` bucketed to hours                                                                   |
| Recent activity feed           | structured-log tail filtered to operational events (no PII)                                        |

New panels not in the mock but trivially backed by the same data:

- **Top users (by call count)** — internal display only, gated on
  the `policy.privacy_show_per_user` tenant setting (default `on`;
  EU tenants may want it off).
- **Top tools** — `tool_call_events` aggregated by `tool_name`.
- **Active users this month** — count from `active_user_months`
  rolled up at request time. Surfaced because billing depends on it
  and the admin needs to know what they will pay for.

The "Recent MCP Activity" feed is a projection of `audit_events` (not
`tool_call_events`) filtered to the operational verbs
(upstream up/down, breaker trips, link health changes). DRY: the
audit log already records these; we render them.

### Connect-RPC: `AdminService` additions

```proto
// proto/limen/admin/v1/admin.proto

message DashboardSummary {
  int32 active_upstream_count = 1;
  int32 configured_upstream_count = 2;
  int32 connected_clients_last_hour = 3;
  int64 calls_24h = 4;
  double error_rate_24h = 5;
  int32 active_users_this_month = 6;
}
message TimeBucket { google.protobuf.Timestamp bucket = 1; int64 calls = 2; int64 errors = 3; int32 p95_duration_ms = 4; }
message ToolUsage { string tool_name = 1; string upstream_public_id = 2; int64 calls = 3; int64 errors = 4; }
message UserUsage { string user_public_id = 1; int64 calls = 2; int64 errors = 3; bool active_this_month = 4; }

rpc GetDashboardSummary(DashboardRange) returns (DashboardSummary);
rpc GetRequestVolume(DashboardRange) returns (RequestVolumeResponse);     // repeated TimeBucket
rpc ListTopTools(DashboardRange) returns (ListTopToolsResponse);
rpc ListTopUsers(DashboardRange) returns (ListTopUsersResponse);
rpc ListActiveUsers(MonthRequest) returns (ListActiveUsersResponse);
```

`DashboardRange` is the same `last_24h | last_7d | last_30d` enum the
mock shows. Owner + admin can call all RPCs; member can call
`GetDashboardSummary` only (high-level usage is not sensitive).

### Prometheus metrics

`/metrics` on a dedicated admin port (not the public router). Already
mounted in [Phase 11](phase-11-production-deployment.md); this phase
adds the metrics themselves.

```
# RED metrics; tenant_id is omitted from labels to keep cardinality bounded.
# Tenant-level dashboards come from the SPA, not Prometheus.

limen_tool_calls_total{upstream="<public_id>",tool="<name>",surface="mcp_direct|codemode",outcome="ok|error"} counter
limen_tool_call_duration_ms{upstream,tool,surface} histogram (buckets: 10, 50, 100, 250, 500, 1000, 2500, 5000, 10000)
limen_active_users_current_month                                                                gauge   (global)
limen_active_users_current_month_per_tenant{tenant="<public_id>"}                               gauge   (opt-in via config; off by default)
limen_observability_event_queue_depth                                                           gauge
limen_observability_dropped_total                                                               counter

limen_upstream_health{upstream}                                                                 gauge   (0|1)
limen_breaker_state{name}                                                                       gauge   (0=closed,1=half,2=open)  -- from Phase 10 resilience registry
```

`tool` labels are bounded by the upstream's published tool set (we
register only known tools at startup, so unknown tools get an
`unknown` bucket — protects against label explosion from misbehaving
clients).

The per-tenant gauge is opt-in (`observability.per_tenant_metrics:
true` in `config.yaml`) for two reasons: (1) cardinality grows with
tenant count, (2) some operators don't want tenant identifiers in
their metrics pipeline. The default is off; staff use the dashboard
RPCs cross-tenantly when they need per-tenant numbers.

### Active users → billing (replaces Phase 13's seat counter)

This phase **supersedes** [Phase 13's](phase-13-billing-stripe.md)
"count Zitadel grants" rule. The new contract:

- `internal/billing/seats.go::Reconcile(tenantID)` now reads
  `active_user_months` for the current calendar month rather than
  Zitadel grants. The Zitadel-grant count is still surfaced in the
  staff backoffice and in the admin SPA as the **provisioned seat
  count** (how many people _could_ use the gateway) versus the
  **active seat count** (how many actually did). Both are useful;
  billing uses the active number.
- Stripe `quantity` is bumped **only upward** within a billing month —
  proration on add. We do not downward-prorate inside the month
  because (a) Stripe handles month-boundary cleanup automatically and
  (b) it makes the invoice unstable. At month boundary, the first
  reconcile of the new month resets `quantity` to that month's active
  count as we see it accumulate.
- The reconciler is no longer "reactive on RPC + 6 h periodic". It
  becomes "**1 h periodic** + **on first call from a previously-
  inactive user this month**". The drain goroutine emits a
  `billing.user_became_active` audit row + kicks the reconciler when
  it inserts a new `active_user_months` row for the current month.
- Free-tier limit (`free_tier.max_seats`) now means **max active
  users this month**, not max grants. A tenant on free tier with 50
  Zitadel grants is fine as long as only 2 distinct users actually
  call tools in any given month.
- The dashboard surfaces both numbers and a "current month is at X
  active / Y provisioned" line on the billing page.

`tenant_billing` gains:

```sql
ALTER TABLE tenant_billing
  ADD COLUMN active_seat_count INTEGER NOT NULL DEFAULT 0,
  ADD COLUMN seat_high_water_mark_month DATE;          -- the month the current active_seat_count belongs to
```

The old `seat_count` column is retained for the staff backoffice's
"provisioned seats" panel (kept in lockstep with Zitadel grants by the
old reactive hook, now renamed `provisioned_seat_count`). This is a
breaking rename — change the column and the call sites in one PR
(see [AGENTS.md engineering posture](../../AGENTS.md#engineering-posture-read-first):
no compat shims).

### Service accounts

New entity. Fully separate from `users` because:

- Service accounts authenticate to MCP via a **personal access token**
  (PAT) issued by Limen, not via Zitadel. They never log in to the
  portal; they don't have a Zitadel subject; they cannot own a tenant.
- Their billing rule is different (see below).
- Their policy-engine treatment ([Phase 16](phase-16-policy-engine.md))
  is identical (taggable, subject to policies) — service accounts are
  bound by `tag_bindings.subject_kind = 'service_account'` (new enum
  variant).

```sql
CREATE TABLE service_accounts (
  id          BIGSERIAL PRIMARY KEY,
  public_id   TEXT NOT NULL UNIQUE,              -- svc_<ulid>
  tenant_id   BIGINT NOT NULL REFERENCES tenants(id),
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  enabled     BOOLEAN NOT NULL DEFAULT true,
  created_by_user_id BIGINT NOT NULL REFERENCES users(id),  -- audit pointer
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at  TIMESTAMPTZ
);
CREATE UNIQUE INDEX service_accounts_tenant_name_uq
  ON service_accounts (tenant_id, name) WHERE deleted_at IS NULL;

CREATE TABLE service_account_tokens (
  id                  BIGSERIAL PRIMARY KEY,
  service_account_id  BIGINT NOT NULL REFERENCES service_accounts(id) ON DELETE CASCADE,
  public_id           TEXT NOT NULL UNIQUE,        -- pat_<ulid> (returned once, the only ID surfaced)
  token_hash          BYTEA NOT NULL,              -- argon2id of the secret; secret never persisted
  prefix              TEXT NOT NULL,               -- first 8 chars of secret, shown in UI ("limen_pat_8f3a…")
  last_used_at        TIMESTAMPTZ,
  expires_at          TIMESTAMPTZ,                 -- nullable; non-expiring tokens allowed but discouraged in UI
  revoked_at          TIMESTAMPTZ,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Auth path: `Authorization: Bearer limen_pat_<base32-secret>`. The MCP
RS middleware ([Phase 6](phase-06-resource-server.md)) recognises the
`limen_pat_` prefix and dispatches to the service-account validator
instead of the Zitadel JWT path. Validator looks up `prefix`, verifies
argon2id against `token_hash`, checks `revoked_at` / `expires_at`,
loads the parent service account, and pins the request context with
a `service_account_id` instead of a `user_id`. From here the request
is treated like a regular per-tenant user (policy engine evaluates
service-account tags, upstream injection respects per-service-account
links, etc.).

Service accounts have **no Zitadel role** — they cannot be `owner` /
`admin` / `member`. They are effectively a fourth principal type
gated entirely by tags + policies. The default tenant `policy_default`
applies; tenants who flip to default-deny must explicitly allow each
service account.

#### Service-account billing

[Phase 13](phase-13-billing-stripe.md) is extended:

- A new Stripe price `service_account_price_id` (configured next to
  `seat_price_id`) bills service accounts.
- Billing rule: a service account is billable in month M iff it had
  at least one billable call in M (same definition as users).
  Counted on the same `active_user_months` table via the
  `service_account_id` column.
- The Stripe subscription gains a second line item (one
  `SubscriptionItem` per price). `Reconcile` updates both quantities
  in the same `SubscriptionsService.Update` call.
- Free tier: a new
  `free_tier.max_service_accounts` knob (default 0 — paid feature).

#### Admin UX

New `/t/<tenant>/portal/admin/service-accounts` page:

- List with `Name | Created by | Last used | Active this month | Status`.
- Create flow: pick name + optional expiry; on submit the modal
  reveals the full PAT **exactly once** with a copy button and a
  prominent "we will not show this again" warning. Standard PAT idiom.
- Detail: list of tokens (with prefixes, never the full secret),
  revoke buttons, and the same tag-binding UI used for users
  ([Phase 16](phase-16-policy-engine.md)).
- Owner + admin can create / revoke; member cannot see the page.

### Audit

New verbs in [`docs/audit.md`](../audit.md):

- `service_account.created` / `.deleted` / `.enabled` / `.disabled`
- `service_account.token.issued` / `.revoked`
- `service_account.used_first_time_this_month`
- `billing.user_became_active` (one row per (tenant, user, month))
- `billing.service_account_became_active` (one row per (tenant, svc, month))
- `billing.active_seat_quantity_updated` (per Stripe push)
- `observability.metrics_queue_dropped` (emitted in batches when `dropped` ticks up)

The "became active" rows are **once per month** by construction
(uniqueness on the `active_user_months` insert path), so they are
self-rate-limited.

### Staff backoffice

`StaffService` ([Phase 12](phase-12-staff-backoffice.md)) gains:

- `GetTenantUsage(tenant_id, range)` — same shape as the admin
  `GetDashboardSummary` plus `provisioned_seat_count`,
  `active_seat_count_this_month`, `service_account_active_count`.
- `ListAllActiveUsers(month)` — cross-tenant active-user roster for
  finance reconciliation. Staff-mode SELECT.
- Existing **Tenants** detail card gains a "This month's activity"
  block: active users / provisioned seats / service-account count /
  delta-from-last-month sparkline.

### Codemode hook

Codemode tool calls already flow through the same dispatch wrapper as
direct MCP calls ([Phase 8b](phase-08b-codemode-async-tool-calls.md)),
so they're recorded automatically. A `surface='codemode'` label
distinguishes them in metrics. The script body itself is recorded in
the audit log per [`docs/audit.md`](../audit.md); the tool-call event
table only carries the per-tool outcome.

### Performance

- Hot-path overhead per tool call: one channel send + one `time.Now()`
  diff. Measured target < 5 μs at p99 in a microbench; will not
  measurably move dispatch latency.
- Postgres write rate: a tenant emitting 1 K req/s sustained is
  ~3.6 M rows/h. With monthly partitions and an UNLOGGED-staging step
  in the future if needed, this is comfortable on a single Postgres
  instance for years. We will not pre-optimise; the 90-d retention +
  monthly rollup is the relief valve.
- Materialized view refresh: ~seconds on a busy tenant, runs
  `CONCURRENTLY` so it doesn't lock readers.

## Verification

- Unit: `Recorder.Record` under load — fill the buffer, assert
  shed-load behaviour, assert no goroutine leak when the drain is
  stopped.
- Integration (`postgres:18-alpine`): emit 10 K events for two
  tenants, assert RLS isolation, assert `active_user_months` rolls up
  correctly across a month boundary (use `time.Now()` injection).
- Dashboard RPC: golden-file the response for a deterministic
  fixture set.
- Billing handoff: reconcile picks up the active number, not the
  Zitadel grant count; downward changes do not happen mid-month;
  month-boundary reset works.
- Service-account PAT: argon2id verification path; revoked tokens
  rejected; expiry honoured; pre-pat-prefix Bearer tokens still
  routed to the Zitadel validator (don't break customer logins).
- Prometheus: scrape the endpoint, assert label-set is bounded
  (no `user_id` label anywhere; no per-tenant labels unless opt-in).
- Codemode: a script that calls 50 tools produces 50 rows, one per
  call; the dispatching code does not double-record.

## Checklist

- [ ] Migration `migrations/postgres/00XX_observability.sql` —
      `tool_call_events` (partitioned), `active_user_months`,
      `service_accounts`, `service_account_tokens`, RLS policies,
      generated `billable_active` column, materialized view.
- [ ] Migration `migrations/postgres/00XX_billing_active_seats.sql` —
      `tenant_billing` rename `seat_count → provisioned_seat_count`,
      add `active_seat_count`, `seat_high_water_mark_month`. Update
      every call site in one PR (no compat alias).
- [ ] `internal/observability/recorder.go` — `Recorder`, drain loop,
      bounded channel, dropped-counter, batched `COPY` per tenant,
      incremental `active_user_months` upsert.
- [ ] Dispatch wrapper integration in `internal/mcprs/` (or wherever
      [Phase 6](phase-06-resource-server.md) lands tool-call dispatch);
      single recording site covers direct MCP **and** codemode.
- [ ] Prometheus metrics in `internal/observability/metrics.go`;
      `/metrics` mount on the admin port already provided by
      [Phase 11](phase-11-production-deployment.md).
- [ ] Background materialized-view refresher (every 5 min, jittered);
      runs in the same binary as the drain.
- [ ] AdminService dashboard RPCs (`GetDashboardSummary`,
      `GetRequestVolume`, `ListTopTools`, `ListTopUsers`,
      `ListActiveUsers`); buf-generated Go + TS.
- [ ] Service-account RPCs on AdminService:
      `CreateServiceAccount` / `Delete` / `Enable` / `Disable`,
      `IssueToken` / `RevokeToken`, `ListServiceAccounts`.
- [ ] MCP RS middleware: `limen_pat_` prefix dispatch + argon2id
      verification.
- [ ] [Phase 16](phase-16-policy-engine.md) integration:
      `tag_bindings.subject_kind` enum gains `service_account`;
      evaluator handles the new principal kind transparently.
- [ ] `internal/billing/seats.go` rewritten against
      `active_user_months`; reconciler triggers on
      `user_became_active` audit events.
- [ ] Tenant admin SPA: dashboard, service-accounts page, billing
      page update (active vs provisioned counts).
- [ ] Staff backoffice additions (`GetTenantUsage`,
      `ListAllActiveUsers`, tenant detail card block).
- [ ] Audit vocabulary additions to [`docs/audit.md`](../audit.md).
- [ ] Docs: new `docs/observability.md` (metrics catalogue, dashboard
      panels, retention) + update [`docs/configuration.md`](../configuration.md)
      with the new `observability:` block and the renamed billing
      knobs.

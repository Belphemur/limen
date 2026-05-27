---
phase: "16"
title: "Observability (metrics, dashboards, Prometheus)"
status: planned
progress: 0
depends_on: ["6", "8", "8b", "9c", "11", "13b"]
updated: "2026-05-27"
---

# Phase 16 — Observability (metrics, dashboards, Prometheus)

**Depends on**: [Phase 6](phase-06-resource-server.md) (tool-call hot path),
[Phase 8](phase-08-per-tenant-injection.md) +
[Phase 8b](phase-08b-codemode-async-tool-calls.md) (codemode dispatch),
[Phase 9c](phase-09c-tenant-admin-spa.md) (tenant admin SPA),
[Phase 11](phase-11-production-deployment.md) (production wiring),
[Phase 13b](phase-13b-billing-metrics-pipeline.md) (billing metrics pipeline —
shares the `active_user_months` table that Phase 16 reads for dashboard display).
**Unblocks**: tenant admins see real usage; ops gets Prometheus signals;
[Phase 17](phase-17-policy-engine.md) can use the event stream for policy decisions.

## Goal

Three closely-related capabilities served by **one event stream**, focused
purely on operational visibility.

1. **Per-user, per-tool observability.** Every `tools/call` (direct or
   from inside codemode) records `success` / `failure` asynchronously,
   without blocking the dispatch path. Events flow through a Valkey
   Stream → Postgres pipeline, and tenant admins see a dashboard with
   success / failure counts, top tools, top users, latency, and a
   24 h request-volume sparkline.
2. **Prometheus metrics** for SaaS ops — RED metrics (Rate, Errors,
   Duration) per upstream + per dispatch surface, plus
   tenant-aggregated gauges. Cardinality is bounded; we never label by
   `user_id`.
3. **Admin dashboard SPA** with operational panels backed by Connect-RPC
   additions to `AdminService`. Active-user counts surface from
   `active_user_months` (created and written by Phase 13b; Phase 16 is
   a reader) for the "Active users this month" panel.

Billing is handled by [Phase 13](phase-13-billing-stripe.md) (two-plan model)
and [Phase 13b](phase-13b-billing-metrics-pipeline.md) (billing metrics pipeline).
This phase focuses on operational visibility only.

## Non-goals (v1)

- **Per-tool / per-upstream usage-based pricing** — Phase 13 territory.
- **Time-series retention beyond 90 days** of raw events. Older data
  rolls up into a monthly aggregate (kept for dashboard summaries;
  billing reconciliation is Phase 13c).
- **A query language for the dashboard.** Fixed panels with filter
  chips. No PromQL-style box in v1.
- **Tracing.** Spans, OpenTelemetry export, distributed traces — out of
  scope. RED metrics + structured logs cover the v1 needs.
- **Billing reconciliation** — Phase 13c.
- **Service accounts** — Phase 9i.

## Design

### The event stream

One table, one writer, one consumer group. The event stream is the
backbone for all observability queries.

```sql
CREATE TABLE tool_call_events (
  id              BIGSERIAL,
  occurred_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

  tenant_id       BIGINT       NOT NULL REFERENCES tenants(id),
  user_id         BIGINT       NOT NULL REFERENCES users(id),

  upstream_id     BIGINT       NOT NULL REFERENCES upstreams(id),
  tool_name       TEXT         NOT NULL,             -- after normalization (Phase 14 territory)
  surface         TEXT         NOT NULL,             -- 'mcp_direct' | 'codemode'

  outcome         TEXT         NOT NULL,             -- 'ok' | 'error'
  error_code      TEXT,                              -- 'policy_denied' | 'upstream_unavailable' | 'tool_error' | ...
  duration_ms     INTEGER      NOT NULL,             -- dispatch-level, not including upstream auth refresh
  request_bytes   INTEGER      NOT NULL DEFAULT 0,
  response_bytes  INTEGER      NOT NULL DEFAULT 0,

  billable_active BOOLEAN
    GENERATED ALWAYS AS (outcome = 'ok' OR error_code = 'tool_error') STORED,

  PRIMARY KEY (id, occurred_at)
) PARTITION BY RANGE (occurred_at);

-- monthly partitions, retention = 90 d on raw; older partitions detach
-- after their rows have been folded into the 5-minute materialized view.

CREATE INDEX tool_call_events_tenant_time  ON tool_call_events (tenant_id, occurred_at DESC);
CREATE INDEX tool_call_events_tenant_user  ON tool_call_events (tenant_id, user_id, occurred_at DESC);
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

The `billable_active` generated column is retained for semantic clarity
("which rows would count for billing?") but the actual billing
determination is performed by Phase 13b's pipeline, not this phase.

The `active_user_months` table — referenced by dashboard queries for
"Active users this month" — is **owned by Phase 13b**. Phase 16 reads
it; it does not write to it. Phase 13b's billing metrics pipeline is
the sole writer.

RLS-forced under the standard tenant policy; staff cross-tenant
`SELECT` honours `limen.staff_mode`
([Phase 12](phase-12-staff-backoffice.md)).

### Event transport: Valkey Streams + observer binary

The dispatch path **must not** block on metric persistence — and with
multi-pod gateway deployments ([Phase 11](phase-11-production-deployment.md)),
per-pod in-memory buffers can't coordinate dedup or back-pressure.
Solution: gateway pods fire events at a **Valkey Stream**; a dedicated
**observer binary** consumes the stream and owns every Postgres write.

```
gateway pod (cmd/gateway)         valkey                   limen-observer (cmd/observer)
─────────────────────────         ──────                   ─────────────────────────────
dispatch.Record(ev)               Stream: tool_calls   ◀──XREADGROUP GROUP observer …
  → encode (msgpack)              MAXLEN ~ 1000000          batch 256 / 250ms
  → XADD * tenant_id=… …      ───▶ (~7 d worst-case            ↓
  → return immediately              retention; <1 min        COPY → tool_call_events
                                     in steady state)            ↓
                                                               XACK observer <ids…>
                                                               XDEL tool_calls <ids…>
```

Why Streams (not Pub/Sub):

- **Durable**: entries persist until ACK+DEL, so an observer restart
  loses nothing. Pub/Sub drops messages if no subscriber is connected
  at publish time — unacceptable for observability data.
- **Consumer groups** (`XREADGROUP GROUP observer worker-N`): the
  observer scales horizontally; each entry is delivered to exactly one
  worker, with a pending-entries list for crash recovery.
- **Bounded memory**: `MAXLEN ~ 1000000` on every `XADD` caps the
  stream; oldest entries are evicted if the observer falls catastrophically
  behind. Evictions are counted (`limen_observability_stream_evicted_total`)
  and alert in the ops runbook. The `~` is the approximate-trim flag —
  ~10× cheaper than exact trim, slop is at most a few thousand entries.
- **Replay**: bug in the consumer? Reset the consumer-group cursor and
  reprocess. `XDEL` happens only after a successful Postgres write, so
  in-flight events survive consumer code changes.

**Stream separation.** Phase 13b uses its own streams (`billing:active_users`,
`billing:sa_connections`) with its own consumer group (`billing_observer`).
Phase 16 uses the `tool_calls` stream with consumer group `observer`.
They are separate streams on the same Valkey instance, consumed by the
same observer binary (multiple consumer goroutines).

#### Retention

The stream is **ephemeral by design**. After the observer commits the
batch to Postgres it issues `XACK` _and_ `XDEL` in the same pipeline,
so processed entries disappear immediately. In steady state the
stream holds < 1 minute of unprocessed events; the `MAXLEN` cap is
purely the disaster-mode safety net. We do **not** retain
already-processed entries for replay — the source of truth is
Postgres, and Postgres supports any historical query we care about.

(Note: `XACK` alone does not delete the entry — it only clears the
pending-entries list for the consumer group. `XDEL` is what frees the
memory. Both must happen.)

#### Operational notes from the Valkey docs

A few specifics from the [Valkey Streams intro](https://valkey.io/topics/streams-intro/)
that shape the runbook (not the code):

- **AOF + fsync.** Valkey streams replicate asynchronously and the
  consumer-group state is in AOF/RDB like any other key. If a
  primary fails over before a recently-`XADD`-ed entry replicates,
  that entry is gone — at-most-once for the in-flight window. The
  operator runbook ([Phase 10](phase-10-wiring-hardening.md)) pins
  Valkey to `appendfsync everysec` minimum; metrics tolerate the
  worst-case 1 s window, and Postgres remains the source of truth
  for everything that has already been ACK-ed.
- **Stuck consumer recovery.** Observer pods use `XAUTOCLAIM` on
  startup (and periodically every 60 s) with `min-idle-time = 60000`
  to take over messages whose owning consumer crashed. The delivery
  counter on each pending entry is exposed as a Prometheus gauge;
  any entry with delivery count ≥ 5 is moved to a dead-letter
  stream (`tool_calls:dlq`) and surfaces an operator alert. The
  dead-letter stream is sized small (`MAXLEN 10000`) and inspected
  manually — it should be empty in steady state.
- **Consumer-group bootstrap.** First-time observer startup runs
  `XGROUP CREATE tool_calls observer $ MKSTREAM` (idempotent — we
  swallow `BUSYGROUP`). Using `$` means we start from "new entries
  only"; we explicitly do **not** want to replay history at first
  boot since the gateway hasn't been producing into the stream
  before the observer existed.
- **Single-stream throughput.** `XADD` is O(1) and benchmarks at
  ≥ 500 K inserts/s on a single Valkey node. We are nowhere near
  that ceiling; if we ever approach it, the response is to shard
  the stream by `(tenant_id mod N)`, not to scale Valkey vertically.

#### Gateway-side recorder

The gateway never touches `tool_call_events` directly. Its only job
is to put an event on the stream.

```go
// internal/observability/recorder.go

type Recorder struct {
    valkey *valkey.Client   // shared with the rest of the gateway
    fallback chan Event     // only used when valkey.enabled == false
    dropped  atomic.Uint64
}

func (r *Recorder) Record(ev Event) {
    payload := msgpack.Marshal(&ev)
    if err := r.valkey.XAdd(ctx, "tool_calls", payload, MaxLen(1_000_000)); err != nil {
        r.dropped.Add(1)
        return // shed load — never block dispatch
    }
}
```

- `XADD` round-trip target: < 200 μs on the same VPC, < 50 μs with
  Unix-socket Valkey in dev.
- Failure is non-fatal: increment `limen_observability_dropped_total`
  and move on. No retry queue in the gateway — the observer is the
  retry layer.
- The all-in-one `cmd/limen/` binary (dev + small self-hosted)
  short-circuits the stream when `valkey.enabled: false`: the
  Recorder owns an in-process channel and drains it on the same
  goroutine that would otherwise be the observer. Same code, same
  invariants; no Valkey infrastructure required.

The dispatch hook is a single call at one site —
`internal/mcprs/`'s tool-call wrapper — for both direct MCP and
codemode-driven calls. Codemode does **not** call `Record` itself;
it calls the wrapped dispatch, which records once. That keeps
"one tool call = one row" intact even when a script fans out 50
calls.

### `cmd/observer` — the consumer binary

This phase ships a sixth binary: `cmd/observer/main.go`, built and
deployed alongside the existing five. Same Go module, same
`internal/boot/` runtime, same Docker base image — the split is at
the entry-point + image boundary, per
[Phase 9a](phase-09a-binary-split.md)'s pattern.

Responsibilities (everything the gateway used to do on the drain
goroutine, plus a few things that benefit from being centralized):

1. **`tool_calls` stream consumer (Phase 16).** Joins consumer group
   `observer`, batches up to 256 events or 250 ms, `COPY`s into
   `tool_call_events`, then `XACK`+`XDEL`.
2. **`billing:active_users` stream consumer (Phase 13b).** Consumer
   group `billing_observer`; upserts `active_user_months`.
3. **`billing:sa_connections` stream consumer (Phase 13b).** Consumer
   group `billing_observer`; tracks service-account activity.
4. **`audit` stream consumer (Phase 12).** Same pattern for audit
   events — see [`docs/audit.md` § Asynchronous transport](../audit.md#asynchronous-transport).
5. **Materialized-view refresher.** Every 5 min jittered, runs
   `REFRESH MATERIALIZED VIEW CONCURRENTLY tool_call_5m`. Single-
   leader via a Valkey lease key (`SET observer:mv-lease … NX PX 60000`).

Deployment shape:

- **SaaS / multi-pod**: 2 observer pods minimum (rolling-update
  friendly), consumer-group ensures each event is processed once
  even with N workers. Lease-key ensures the materialized-view
  refresh runs on exactly one pod at a time.
- **Self-hosted small**: a single observer container alongside the
  gateway / portal pods.
- **All-in-one `cmd/limen/`**: no separate process; the observer
  runs as a goroutine inside the same binary, sharing the DB pool.

The observer holds its own `limen_app` pool connection. `tenant_id`
travels on each row; the observer sets `limen.tenant_id` per `COPY`
batch by sorting events by tenant and emitting one `SET LOCAL` +
`COPY` per tenant inside one transaction (the standard RLS pattern,
not `WithSuperuser`).

#### Why a separate binary

- **Hot-path isolation.** A slow Postgres or a long-running mat-view
  refresh cannot back-pressure the gateway. The blast radius is the
  observer + its stream backlog; the MCP request path is unaffected.
- **Scales independently.** Observer pods are CPU-bound on
  serialization + DB writes, gateway pods are CPU-bound on JS
  evaluation. Different shapes, different node sizes.
- **Single-writer guarantees.** All writes to `tool_call_events` and
  `audit_events` go through one binary, one connection pool. Easier
  to reason about transactions, idempotency, and rate limits.

### What counts

Phase 16 records **every** tool call regardless of outcome. The
determination of what counts as billable activity is managed by
Phase 13b's billing metrics pipeline, not this phase.

| Outcome class                                                | Recorded by Phase 16?                                |
| ------------------------------------------------------------ | ---------------------------------------------------- |
| `ok`                                                         | yes                                                  |
| `error:tool_error` (upstream returned an error MCP response) | yes                                                  |
| `error:upstream_unavailable`                                 | yes                                                  |
| `error:policy_denied`                                        | yes                                                  |
| Validation errors before dispatch (malformed JSON-RPC, etc.) | logged only, **not** recorded as a tool_call_event   |

The `billable_active` generated column (`outcome = 'ok' OR error_code = 'tool_error'`)
provides a semantic hint but Phase 16 does not use it for any billing
logic — it exists for query convenience and dashboard clarity.

### Monthly rollup + active-user mark

The `active_user_months` table is **owned by Phase 13b**. It tracks
"who was active in month M" so queries don't have to scan raw events.
Phase 16 reads it for dashboard display ("Active users this month").

Phase 16's own aggregation is the materialized view `tool_call_5m`:

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
| Top users (by call count)      | `tool_call_events` aggregated by `user_id`, gated on `policy.privacy_show_per_user`                |
| Top tools                      | `tool_call_events` aggregated by `tool_name`                                                       |
| Active users this month        | count from `active_user_months` for current calendar month (Phase 13b table, Phase 16 reader)      |

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
limen_active_users_current_month                                                          gauge   (global)
limen_active_users_current_month_per_tenant{tenant="<public_id>"}                         gauge   (opt-in via config; off by default)
limen_observability_event_queue_depth                                                     gauge
limen_observability_dropped_total                                                         counter
limen_observability_stream_evicted_total                                                  counter

limen_upstream_health{upstream}                                                           gauge   (0|1)
limen_breaker_state{name}                                                                 gauge   (0=closed,1=half,2=open)  -- from Phase 10 resilience registry
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

### Active users → dashboard display

Phase 16 reads `active_user_months` for the "Active users this month"
panel on the admin dashboard. It does **not** reconcile billing. It
does **not** push Stripe quantities. It is purely a **reader**.

The `active_user_months` table was created by Phase 13b's migration
and is written by Phase 13b's billing metrics pipeline. Phase 16's
dashboard RPC `ListActiveUsers` queries this table (joined with
`users` for display names) and returns counts per tenant.

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
  tenants, assert RLS isolation, assert month-boundary queries work
  across partitions (use `time.Now()` injection).
- Dashboard RPC: golden-file the response for a deterministic
  fixture set.
- Prometheus: scrape the endpoint, assert label-set is bounded
  (no `user_id` label anywhere; no per-tenant labels unless opt-in).
- Codemode: a script that calls 50 tools produces 50 rows, one per
  call; the dispatching code does not double-record.
- Cross-phase: dashboard `ListActiveUsers` reads `active_user_months`
  correctly and returns expected counts (Phase 13b table, Phase 16 reader).

## Checklist

- [ ] Migration: `tool_call_events` (partitioned), RLS policies,
      generated `billable_active` column, materialized view `tool_call_5m`
- [ ] `internal/observability/recorder.go` — Recorder, Valkey Stream XADD,
      dropped-counter
- [ ] `cmd/observer/` — tool_calls stream consumer (XREADGROUP → batch COPY → XACK+XDEL),
      materialized-view refresher
- [ ] Consumer-group bootstrap: `XGROUP CREATE tool_calls observer $ MKSTREAM`
- [ ] XAUTOCLAIM for stuck-consumer recovery (60s min-idle)
- [ ] Dead-letter stream for entries with delivery count ≥ 5
- [ ] All-in-one fallback: in-process drain goroutine when `valkey.enabled: false`
- [ ] Dispatch wrapper integration in `internal/mcprs/` — single recording site
      for direct MCP + codemode
- [ ] Prometheus metrics in `internal/observability/metrics.go`; `/metrics` mount on admin port
- [ ] Background materialized-view refresher (every 5 min, jittered, single-leader via Valkey lease)
- [ ] AdminService dashboard RPCs: `GetDashboardSummary`, `GetRequestVolume`,
      `ListTopTools`, `ListTopUsers`, `ListActiveUsers`
- [ ] Tenant admin SPA: dashboard page with all panels
- [ ] Staff backoffice: `GetTenantUsage` RPC, tenancy detail card with activity block
- [ ] Audit vocabulary additions (`observability.metrics_queue_dropped`)
- [ ] Docs: `docs/observability.md` (metrics catalogue, dashboard panels, retention)
- [ ] Valkey Prometheus metrics exported
- [ ] `config.yaml` `observability:` section with retention, per-tenant-metrics toggle
- [ ] `go build ./...`, `go test ./...` pass

## Deliverables

| File | Change |
|------|--------|
| `docs/phases/phase-16-observability-and-active-users.md` | This file — rewrite, remove billing + SA |
| `docs/phases/README.md` | Updated index + depends_on |
| `internal/observability/` | New package (recorder, metrics, drain) |
| `cmd/observer/` | New binary (or goroutine in cmd/limen) |
| `proto/limen/admin/v1/admin.proto` | Add dashboard RPCs |
| `web/admin/` | Dashboard page SPA |
| `config.yaml` | New `observability:` section |

## Risks

- **Valkey stream eviction under catastrophic observer outage** — mitigated
  by MAXLEN cap + Prometheus alert on `limen_observability_stream_evicted_total`.
- **Materialized view refresh locking** — `CONCURRENTLY` prevents read locks;
  single-leader lease prevents concurrent refresh contention.
- **Label cardinality explosion** — bounded by known tool names; no `user_id`
  labels on any Prometheus metric.
- **Observer binary scaling** — consumer groups handle horizontal scaling
  naturally; each worker owns its lease and pending-entry set.

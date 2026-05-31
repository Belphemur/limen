---
phase: "13b"
title: "Billing Metrics Pipeline (Valkey Streams)"
status: in_progress
progress: 71
depends_on: ["7", "9c", "9i", "10"]
updated: "2026-05-28"
---

# Phase 13b — Billing Metrics Pipeline (Valkey Streams)

**Depends on**: Phase 7 (Valkey client — already used for OAuth state, must be extended with `XADD`/`XREADGROUP`/`XACK`/`XDEL`/`XAUTOCLAIM` for Streams), Phase 9c (admin SPA + `AdminService` — hosts the dashboard chart RPCs and components), Phase 9i (service accounts — needed for SA connection snapshots), Phase 10 (resilience patterns, wiring).  
**Unblocks**: Phase 13c (Stripe integration — reconciler reads from these tables), Phase 16 (observability — reads `active_user_months` for cross-tenant dashboard display).

## Goal

The lightweight billing metrics pipeline that feeds the Phase 13 billing reconciler. Uses Valkey Streams as the transport layer and a dedicated observer consumer writing to Postgres. Tracks two things:
1. **Active users per month** — who made a tool call in this billing period
2. **Concurrent SA connections** — peak simultaneous service account MCP connections for billing

This is separate from Phase 16's full observability pipeline (which also uses Valkey Streams but for `tool_call_events`, materialized views, and dashboards). The two pipelines share the observer binary and Valkey instance but use DIFFERENT stream keys and consumer groups. This separation means billing can be built and shipped before the full analytics dashboard.

## Non-goals

- Cross-tenant analytics dashboards (Phase 16)
- Per-tool-call event recording (Phase 16's `tool_call_events` table)
- Prometheus RED metrics (Phase 16)
- Materialized view refreshes (Phase 16)
- Phase 16's system-wide admin dashboard (the tenant-scoped billing charts in the admin SPA ARE in scope — see sub-phase 13b-a below)

## Design

### Tables

#### `active_user_months`

Tracks per-tenant, per-month who was active (made at least one tool call). The billing reconciler counts distinct `user_id` rows for the current month to determine the Stripe quantity.

```sql
CREATE TABLE active_user_months (
  tenant_id          BIGINT NOT NULL REFERENCES tenants(id),
  month_start        DATE   NOT NULL,                    -- first day of the calendar month UTC
  user_id            BIGINT REFERENCES users(id),
  service_account_id BIGINT REFERENCES service_accounts(id),
  first_seen_at      TIMESTAMPTZ NOT NULL,
  last_seen_at       TIMESTAMPTZ NOT NULL,
  call_count         INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, month_start, user_id, service_account_id)
);
CREATE INDEX active_user_months_tenant_month ON active_user_months (tenant_id, month_start);
```

Upsert: `INSERT ... ON CONFLICT (tenant_id, month_start, user_id, service_account_id) DO UPDATE SET call_count = active_user_months.call_count + EXCLUDED.call_count, last_seen_at = GREATEST(active_user_months.last_seen_at, EXCLUDED.last_seen_at)`.

RLS: standard tenant isolation policy. Staff `SELECT` honours `limen.staff_mode`.

#### `sa_connection_snapshots`

Records connect/disconnect events so the reconciler can compute the peak concurrent SA connections in a billing period. Each row is one connection session.

```sql
CREATE TABLE sa_connection_snapshots (
  id                  BIGSERIAL PRIMARY KEY,
  tenant_id           BIGINT NOT NULL REFERENCES tenants(id),
  service_account_id  BIGINT NOT NULL REFERENCES service_accounts(id),
  connected_at        TIMESTAMPTZ NOT NULL,
  disconnected_at     TIMESTAMPTZ,                      -- NULL = still connected
  concurrent_count    INTEGER NOT NULL DEFAULT 0,       -- number of concurrent connections at connect time
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX sa_conn_snapshots_tenant_month ON sa_connection_snapshots (tenant_id, connected_at);
```

The reconciler computes the billing quantity as: `SELECT MAX(concurrent_count) FROM sa_connection_snapshots WHERE tenant_id = ? AND connected_at >= ? AND connected_at < ?` for the current billing month.

RLS: standard tenant isolation policy. Staff cross-tenant SELECT honours `limen.staff_mode`.

### Event Transport: Valkey Streams

```
gateway pod                           valkey                        observer consumer
─────────────────────────             ──────                        ──────────────────────
Tool call dispatch                     Stream: billing:active_users
  → BillingRecorder.RecordActiveUser   MAXLEN ~500000           ◀──XREADGROUP GROUP billing_observer …
  → XADD * tenant_id=… user_id=…      (~3 d worst-case              batch 256 / 250ms
  → return immediately                  retention; <1 min               ↓
                                        in steady state)           COPY → active_user_months (upsert)
                                                                       ↓
SA MCP connect/disconnect             Stream: billing:sa_connections   XACK billing_observer <ids…>
  → BillingRecorder.RecordConnection  MAXLEN ~100000                  XDEL billing:active_users <ids…>
  → XADD * tenant_id=… sa_id=…        
  → return immediately
```

**Why Valkey Streams** (same rationale as Phase 16):
- Durable: entries persist until ACK+DEL. Observer restarts lose nothing.
- Consumer groups: entries delivered exactly-once to a consumer. Horizontal scaling.
- Bounded memory: `MAXLEN` caps the stream. Oldest entries evicted if observer falls behind (counted as `limen_billing_stream_evicted_total`).
- Replay: reset consumer-group cursor to reprocess after bug fixes. `XDEL` only after successful Postgres write.

**Stream separation from Phase 16**: Phase 13b uses `billing:active_users` and `billing:sa_connections` streams with consumer group `billing_observer`. Phase 16 uses `tool_calls` stream with consumer group `observer`. They share the same Valkey instance and the same observer binary (which runs multiple consumer goroutines), but the streams and consumer groups are independent.

**Observer binary**: The `cmd/observer/` binary (shipped in Phase 16) hosts consumer goroutines for both phases. Phase 13b can ship its consumer as a goroutine in `cmd/limen/` (all-in-one mode) or in the separate observer binary when it exists. The consumer is the same code regardless.

#### Retention

Processed entries are `XACK` + `XDEL` immediately after successful Postgres write. In steady state the stream holds < 1 minute of unprocessed events. `MAXLEN` cap is the disaster-mode safety net (observer completely down for ~3 days at 10K events/min before eviction starts).

### Gateway-Side Recorder

```go
// internal/billing/metrics/recorder.go

type BillingRecorder struct {
    valkey   *valkey.Client
    fallback chan billingEvent     // used when valkey.enabled == false
    dropped  atomic.Uint64
}

func (r *BillingRecorder) RecordActiveUser(ctx context.Context, tenantID int64, userID int64, isServiceAccount bool) {
    ev := billingEvent{
        TenantID: tenantID,
        UserID:   userID,
        Kind:     "active_user",
        TS:       time.Now(),
    }
    if isServiceAccount {
        ev.ServiceAccountID = userID
    }
    payload := msgpack.Marshal(&ev)
    if err := r.valkey.XAdd(ctx, "billing:active_users", payload, MaxLen(500_000)); err != nil {
        r.dropped.Add(1)
        return // never block dispatch
    }
}

func (r *BillingRecorder) RecordSAConnection(ctx context.Context, tenantID int64, saID int64, connected bool) {
    ev := billingEvent{
        TenantID:         tenantID,
        ServiceAccountID: saID,
        Kind:             "sa_connection",
        Connected:        connected,
        TS:               time.Now(),
    }
    payload := msgpack.Marshal(&ev)
    if err := r.valkey.XAdd(ctx, "billing:sa_connections", payload, MaxLen(100_000)); err != nil {
        r.dropped.Add(1)
        return
    }
}
```

- `RecordActiveUser` is called from the single tool-call dispatch site in `internal/mcprs/` (the same hook that Phase 16 uses). It fires AFTER the tool call completes to ensure we only count actual usage, not pending requests.
- `RecordSAConnection` is called from the MCP transport layer when a service account connects (MCP session initialize) and disconnects (session close or timeout).
- Failure is non-fatal: increment `limen_billing_events_dropped_total` and move on. The reconciler's periodic loop handles gaps.

### Observer Consumer

The consumer goroutine (in `cmd/limen/` for all-in-one, or in `cmd/observer/` for split deployment):

```
XREADGROUP GROUP billing_observer consumer-1 BLOCK 250 COUNT 256 STREAMS billing:active_users billing:sa_connections > >
  → batch events into tenant-sorted slices
  → BEGIN transaction
    → SET LOCAL limen.tenant_id = <tenant_public_id> (for RLS)
    → COPY events into temp table
    → UPSERT active_user_months from temp table
    → INSERT sa_connection_snapshots from temp table (compute concurrent_count via subquery)
    → COMMIT
  → XACK billing_observer billing:active_users <ids…>
  → XDEL billing:active_users <ids…>
  → same for billing:sa_connections
```

Consumer-group bootstrap (idempotent):
```
XGROUP CREATE billing:active_users billing_observer $ MKSTREAM   -- swallow BUSYGROUP
XGROUP CREATE billing:sa_connections billing_observer $ MKSTREAM  -- swallow BUSYGROUP
```

XAUTOCLAIM for stuck-consumer recovery (every 60s):
```
XAUTOCLAIM billing:active_users billing_observer consumer-1 60000
```

Entries with delivery count ≥ 5 are moved to dead-letter stream (`billing:dlq`, MAXLEN 10000) and surface an operator alert.

### All-in-One Fallback

When `valkey.enabled: false` (dev + small self-hosted), the `BillingRecorder` sends events to an in-process buffered channel. A drain goroutine in `cmd/limen/` consumes the channel and writes directly to Postgres (same upsert logic, same transaction pattern, just without the Valkey indirection). No Valkey infrastructure required.

### Prometheus Metrics

- `limen_billing_events_dropped_total` — counter, incremented when XADD fails
- `limen_billing_stream_evicted_total` — counter, exposed by the observer when it detects evictions (comparing stream length to expected)

### 13b-a: Tenant Admin Dashboard Charts

The billing metrics pipeline powers two time-series charts in the admin SPA's `Dashboard.vue`. These give tenant owners visibility into their usage — what they're being billed for — without requiring the full Phase 16 analytics dashboard.

**Design principle**: The charts show billing-relevant data only. Active user chart tracks the daily distinct user count (the metric that drives the Stripe quantity). SA connection chart tracks the daily peak concurrent connections. Both reflect the upward-only, month-boundary-reset reconciliation rules from Phase 13.

#### Chart Visibility Gates

Charts are gated on **data existence**, not member count. A tenant who drops from 5 users to 1 still sees the active user chart showing the month's peak usage they're being billed for.

| Chart | Visible when | Check |
|-------|-------------|-------|
| Active User Chart | Any rows in `active_user_months` for this tenant | `SELECT EXISTS(...)` — instant index lookup |
| SA Connection Chart | Any rows in `sa_connection_snapshots` for this tenant | `SELECT EXISTS(...)` — instant index lookup |

#### Data RPCs

Two new `AdminService` RPCs. Both are tenant-scoped (the admin SPA's existing session mechanism provides the tenant context).

```protobuf
// proto/limen/admin/v1/admin.proto

message GetActiveUserChartRequest {
  google.protobuf.Timestamp from_date = 1;  // default: 30 days ago
  google.protobuf.Timestamp to_date = 2;    // default: now
}

message GetActiveUserChartResponse {
  message DataPoint {
    string date = 1;              // "2026-05-27"
    int32 active_user_count = 2;  // distinct users (human + SA) on that day
  }
  repeated DataPoint days = 1;
  bool has_data = 2;              // true if any data exists — gates chart visibility without fetching full range
}

message GetSAConnectionChartRequest {
  google.protobuf.Timestamp from_date = 1;  // default: 30 days ago
  google.protobuf.Timestamp to_date = 2;    // default: now
}

message GetSAConnectionChartResponse {
  message DataPoint {
    string date = 1;              // "2026-05-27"
    int32 peak_connections = 2;   // MAX(concurrent_count) on that day
  }
  repeated DataPoint days = 1;
  bool has_data = 2;              // true if any data exists — gates chart visibility without fetching full range
}
```

The `has_data` field is checked independently of the date range query — it's a fast `EXISTS` subquery that the Dashboard calls on mount to decide whether to render the chart section at all. Once rendered, the component fetches the full date range for chart data.

#### Handlers (`internal/admin/service.go`)

```go
// GetActiveUserChart queries active_user_months for daily distinct user counts.
func (s *Service) GetActiveUserChart(ctx context.Context, req *adminv1.GetActiveUserChartRequest) (*adminv1.GetActiveUserChartResponse, error) {
    tenantID := tenancy.FromContext(ctx)
    from := startOfDay(defaultRange(req.FromDate, -30*24*time.Hour))
    to := startOfDay(defaultRange(req.ToDate, 0)).Add(24 * time.Hour)

    hasData, _ := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM active_user_months WHERE tenant_id = $1)", tenantID).Scan(&hasData)
    if !hasData {
        return &adminv1.GetActiveUserChartResponse{HasData: false}, nil
    }

    rows, err := s.db.QueryContext(ctx, `
        SELECT d::date, COALESCE(SUM(cnt), 0)
        FROM generate_series($2::date, $3::date - 1, '1 day'::interval) d
        LEFT JOIN (
            SELECT month_start::date AS date, COUNT(DISTINCT COALESCE(user_id, service_account_id)) AS cnt
            FROM active_user_months
            WHERE tenant_id = $1 AND month_start >= $2 AND month_start < $3
            GROUP BY month_start::date
        ) t ON d::date = t.date
        GROUP BY d::date ORDER BY d::date`, tenantID, from, to)
    // ... map rows to DataPoint[]
    return &adminv1.GetActiveUserChartResponse{Days: points, HasData: true}, nil
}
```

Same pattern for `GetSAConnectionChart` — queries `sa_connection_snapshots` with `MAX(concurrent_count) GROUP BY connected_at::date`. Both use `generate_series` to fill gaps (zero-fill days with no activity).

#### Chart Components

Add `chart.js` + `vue-chartjs` to `web/admin/package.json`:

```json
"chart.js": "^4.4.0",
"vue-chartjs": "^5.3.0"
```

Two new components:

| Component | File | Chart type | Data RPC |
|---|---|---|---|
| `ActiveUserChart` | `web/admin/src/components/ActiveUserChart.vue` | Line chart with area fill | `GetActiveUserChart` |
| `SAConnectionChart` | `web/admin/src/components/SAConnectionChart.vue` | Line chart with area fill | `GetSAConnectionChart` |

Both components:

- Accept `from` / `to` date range props (default last 30 days)
- Call their RPC on mount, re-fetch when date range changes
- Show a loading skeleton while fetching
- Show "No data yet" empty state when `!has_data` or `days` is empty
- Use admin SPA design token colors (`var(--color-primary)`, `var(--color-on-surface-variant)`) — no hard-coded hex values
- Responsive: fill container width, maintain ~3:2 aspect ratio
- Tooltip on hover shows exact date + count

#### Dashboard Layout Change

In `web/admin/src/pages/Dashboard.vue`, the bottom row (`SystemHealthEmpty` + `QuickResources`) is replaced with a conditional **Usage** section:

```vue
<!-- Usage charts: visible when billing data exists for this tenant -->
<section v-if="hasActiveUserData || hasSAConnectionData" class="grid gap-gutter md:grid-cols-2">
  <ActiveUserChart v-if="hasActiveUserData" :from="chartFrom" :to="chartTo" />
  <SAConnectionChart v-if="hasSAConnectionData" :from="chartFrom" :to="chartTo" />
</section>
<!-- Fallback: original bottom row when no usage data yet -->
<section v-else class="grid gap-gutter md:grid-cols-2">
  <SystemHealthEmpty />
  <QuickResources />
</section>
```

`hasActiveUserData` / `hasSAConnectionData` are fetched as part of the `onMounted` data load. They can come from `getTenantSettings` (augmented with `has_active_user_data` / `has_sa_connection_data` booleans) or from the chart RPCs' `has_data` fields directly.

#### Verification

- Unit: `GetActiveUserChart` returns `has_data: false` for tenant with no rows in `active_user_months`
- Unit: `GetActiveUserChart` returns correct daily distinct user counts for tenant with mixed human/SA activity, including zero-fill for days with no activity
- Unit: `GetSAConnectionChart` returns correct daily peak concurrent connections
- Integration: Dashboard renders chart when data exists, renders `SystemHealthEmpty` + `QuickResources` when no data
- Integration: Tenant dropping from 5 users to 1 still sees the historical active user chart (data persists — gate is on data existence, not member count)
- Integration: Chart data matches raw SQL output for the same date range

## Deliverables

| File | Change |
|------|--------|
| `docs/phases/phase-13b-billing-metrics-pipeline.md` | This file |
| `docs/phases/README.md` | New row in index table |
| `docs/phases/phase-13-billing-stripe.md` | Reference to this phase |
| `internal/billing/metrics/recorder.go` | BillingRecorder + Valkey Stream producer |
| `internal/billing/metrics/consumer.go` | Observer consumer goroutine (shared by all-in-one and split binary) |
| `internal/mcprs/` | Hook `RecordActiveUser` into tool-call dispatch |
| `internal/gateway/` or MCP transport | Hook `RecordSAConnection` into SA connect/disconnect |
| migrations/ | `active_user_months` + `sa_connection_snapshots` tables + RLS + indexes |
| `cmd/limen/main.go` | Start consumer goroutine in all-in-one mode |
| `cmd/observer/main.go` | Start consumer goroutine in split mode (if observer binary exists) |
| `proto/limen/admin/v1/admin.proto` | Two new RPCs: `GetActiveUserChart`, `GetSAConnectionChart` |
| `internal/admin/service.go` | Two chart data handlers (query `active_user_months` + `sa_connection_snapshots`) |
| `web/admin/package.json` | Add `chart.js` ^4.4 + `vue-chartjs` ^5.3 |
| `web/admin/src/components/ActiveUserChart.vue` | Active user time-series line/area chart |
| `web/admin/src/components/SAConnectionChart.vue` | SA connection time-series line/area chart |
| `web/admin/src/pages/Dashboard.vue` | Conditional Usage section replacing bottom row when data exists |

## Verification

- Unit: `GetActiveUserChart` returns `has_data: false` for tenant with no rows in `active_user_months`
- Unit: `GetActiveUserChart` returns correct daily distinct user counts with zero-fill for gap days
- Unit: `GetSAConnectionChart` returns correct daily peak concurrent connections
- Integration: Dashboard renders chart when data exists, renders `SystemHealthEmpty` + `QuickResources` when no data
- Integration: Tenant dropping from 5 users to 1 still sees historical chart (gate is on data existence, not member count)
- Unit: `BillingRecorder` under load — assert shed-load behaviour, assert no goroutine leaks
- Unit: UPSERT logic for `active_user_months` — duplicate events increment `call_count` correctly
- Integration (`postgres:18-alpine`): emit events for two tenants, assert RLS isolation, assert month-boundary correctly creates new rows
- Integration: SA connection snapshot — connect → row with `disconnected_at = NULL` → disconnect → row updated with `disconnected_at`
- Integration: `concurrent_count` computation — connect 3 SAs simultaneously, assert each row has correct `concurrent_count` (1, 2, 3)
- Integration: Valkey Stream consumer — produce events, consumer reads and writes, assert `XACK` + `XDEL` clean up
- Integration: XAUTOCLAIM recovery — simulate consumer crash, new consumer claims pending entries
- Integration: dead-letter stream — force delivery count ≥ 5, assert entry lands in `billing:dlq`

## Checklist

- [x] Migration: `active_user_months` table + RLS + indexes on `(tenant_id, month_start)`
- [x] Migration: `sa_connection_snapshots` table + RLS + indexes on `(tenant_id, connected_at)`
- [x] `internal/billing/metrics/recorder.go` — `BillingRecorder` with `RecordActiveUser` and `RecordSAConnection`
- [x] Gateway integration: `RecordActiveUser` hooked into tool-call dispatch (`internal/gateway/manager.go` CallTool)
- [x] Gateway integration: `RecordSAConnection` hooked into SA MCP connect (`internal/transport/codemode_server.go` SSEHandler; disconnect is TODO for v1)
- [x] Consumer goroutine: XREADGROUP → batch → UPSERT active_user_months → INSERT sa_connection_snapshots → XACK+XDEL (in `internal/billing/metrics/consumer.go`; at-least-once semantics with per-batch commit/ACK)
- [x] Consumer-group bootstrap: `XGROUP CREATE billing:active_users billing_observer $ MKSTREAM` (idempotent, swallow BUSYGROUP) (in consumer.Bootstrap())
- [x] Consumer-group bootstrap: `XGROUP CREATE billing:sa_connections billing_observer $ MKSTREAM` (idempotent) (in consumer.Bootstrap())
- [x] `XAUTOCLAIM` for stuck-consumer recovery (60s min-idle, every 60s) (in consumer.Run())
- [ ] Dead-letter stream `billing:dlq` for entries with delivery count ≥ 5 (MAXLEN 10000)
- [ ] All-in-one fallback: drain goroutine when `valkey.enabled: false`
- [x] `concurrent_count` computation in consumer: subquery counts active (non-disconnected) connections at connect time (in consumer.processSAConnections())
- [ ] Prometheus metrics: `limen_billing_events_dropped_total`, `limen_billing_stream_evicted_total`
- [ ] Unit tests: recorder under load, channel backpressure, shed-load
- [ ] Integration tests: RLS isolation, month-boundary, SA concurrent-count correctness
- [ ] Integration tests: Valkey Stream consumer (happy path, crash recovery, dead-letter)
- [x] `proto/limen/admin/v1/admin.proto`: `GetActiveUserChart` and `GetSAConnectionChart` RPCs + request/response messages with `has_data` field
- [x] `internal/admin/billing.go`: `GetActiveUserChart` handler — query `active_user_months` with `EXISTS` pre-check + `generate_series` zero-fill
- [x] `internal/admin/billing.go`: `GetSAConnectionChart` handler — query `sa_connection_snapshots` with `EXISTS` pre-check + `generate_series` zero-fill
- [x] `web/admin/package.json`: add `chart.js` ^4.4 + `vue-chartjs` ^5.3
- [x] `web/admin/src/components/ActiveUserChart.vue` — line chart with area fill, loading/empty/error states, admin SPA design tokens
- [x] `web/admin/src/components/SAConnectionChart.vue` — line chart with area fill, loading/empty/error states, admin SPA design tokens
- [x] `web/admin/src/pages/Dashboard.vue` — conditional Usage section (charts when data exists, old bottom row otherwise)

## Implementation Notes

- **Valkey Streams extension**: Extended Client interface with XAdd/XReadGroup/XAck/XDel/XAutoClaim/XGroupCreate + full InMemory test fake (17 tests in `internal/valkey/valkey_test.go`). Both `realClient` (valkey-go builder API) and `InMemory` fake implement all operations.
- **GORM models**: `ActiveUserMonth` embeds Base (composite partial unique index via goose migration). `SAConnectionSnapshot` embeds Base (composite index on tenant_id + connected_at). Both registered in AllModels().
- **Consumer error handling**: Refactored to track `hasError` per-tenant batch, only commit+ACK on success. Failed batches roll back and leave messages in pending entries for XAUTOCLAIM retry. Non-timeout XReadGroup errors logged at warn level.
- **Split binary coverage**: Consumer goroutine started in both `serveall.go` (all-in-one) and `servegateway.go` (split gateway binary).
- **Admin RPCs**: Added to requiredRole map with session.RoleAdmin.
- **Design divergence**: Events sent as flat fields (`tenant_id=... user_id=...`) rather than msgpack-marshalled payloads for debuggability. Consumer parses fields at the boundary via parseInt64/parseOptionalInt64 helpers.
- **SSE disconnect**: Not yet tracked (TODO in codemode_server.go). Consumer's concurrent_count uses `disconnected_at IS NULL` subquery; stale rows from crashed connections may inflate peaks until a future disconnect hook or periodic reconciliation sweeper is added.

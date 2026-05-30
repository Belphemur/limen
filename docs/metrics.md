# Prometheus Metrics Specification

Telemetry and observability reference for the Limen codebase.

## Overview

Limen uses Prometheus-style metrics for operational observability. Metrics are collected by the Go Prometheus client library, exposed over the standard `/metrics` HTTP scrape endpoint, and consumed by any Prometheus-compatible monitoring stack (Prometheus server, Grafana Cloud, Datadog, etc.).

Metric instrumentation lives alongside the subsystem it observes. Each package that needs to emit metrics owns a `metrics/prometheus.go` file containing declarations, plus wrapper functions that subsystem code calls as safe entry points. This keeps metric registration and type logic consolidated while allowing callers to remain ignorant of the underlying `prometheus` package.

### Metrics Lifecycle

```
declaration ──► auto-registration ──► instrumentation call ──► /metrics scrape
(prometheus.go)   (prometheus.init)     (recorder, handler)     (Prometheus server)
```

1. **Declaration**: Metrics are declared as package-level variables in `metrics/prometheus.go`.
2. **Auto-registration**: `github.com/prometheus/client_golang/prometheus/promauto` registers each metric with the default registry at `init()` time.
3. **Instrumentation**: Subsystem code calls the exported helper functions (e.g., `IncEventsDropped()`).
4. **Scrape**: The Prometheus server pulls from the `/metrics` endpoint on each replica.

## Naming Conventions

All Limen metrics follow a strict naming pattern:

```
limen_<subsystem>_<name>[_<unit>]_<type>
```

| Component     | Description                                           | Example                        |
| ------------- | ----------------------------------------------------- | ------------------------------ |
| `limen`       | Namespace prefix — **always `limen`**                 | `limen_...`                    |
| `<subsystem>` | Owning subsystem or domain                            | `billing`, `gateway`, `portal` |
| `<name>`      | Descriptive event or value being measured (snake_case) | `events_dropped`               |
| `_<unit>`     | Optional Prometheus unit suffix                       | `_seconds`, `_bytes`, `_total` |
| `<type>`      | Cardinality indicator                                 | `_total`, `_bucket`, ...       |

### Type Suffixes

| Base Type       | Suffix           | Example                                  |
| --------------- | ---------------- | ---------------------------------------- |
| Counter         | `_total`         | `limen_billing_events_dropped_total`     |
| Gauge           | *(none)*         | `limen_gateway_connections_active`       |
| Histogram       | `_bucket`, `_sum`, `_count` | `limen_portal_request_duration_seconds_bucket` |
| Summary         | `_sum`, `_count`, `_quantile` | (rare; prefer Histogram)         |

### Rules

- **Namespace is fixed.** Always prefix with `limen_`. Do not use a subsystem name as the first segment.
- **Counters end in `_total`.** This is the Prometheus convention and enables `rate()` queries out of the box.
- **Snake case only.** No camelCase, no hyphens, no dots.
- **Use standard units.** When applicable, append the Prometheus convention: `_seconds`, `_bytes`, `_ratio`. Duration histograms should use `_seconds` even if internally tracked in milliseconds.
- **Be descriptive but concise.** `events_dropped` is preferred over `ed`. `request_duration_seconds` is preferred over `duration`.
- **No dynamic name segments.** A metric named `limen_billing_tenant_42_events` is wrong. Use labels instead: `limen_billing_events_total{tenant_id="42"}`.

## Metric Declaration & Auto-Registration

### File Placement

Each subsystem that needs metrics owns a dedicated `prometheus.go` file inside a `metrics/` subpackage:

```
internal/
  billing/
    metrics/
      prometheus.go    # metric declarations + helpers
      recorder.go      # BillingRecorder (calls metrics.IncEventsDropped())
      consumer.go      # Consumer (calls metrics.IncStreamEvicted())
```

This layout keeps metric declarations isolated from business logic and prevents circular imports.

### Use `promauto` for Registration

Always use `github.com/prometheus/client_golang/prometheus/promauto` rather than manual registration. `promauto` registers metrics against the default registry at `init()` time, eliminating boilerplate and avoiding double-registration panics.

```go
import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
)
```

### Use Vector Types, Not Dynamic Names

For any metric that needs per-tenant, per-upstream, or per-endpoint breakdowns, use the `*Vec` constructor and define **low-cardinality** labels:

```go
// GOOD — label cardinality is bounded (tenant_id is finite)
var requestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
    Name: "limen_gateway_requests_total",
    Help: "Total MCP requests processed.",
}, []string{"tenant_id", "method"})

// BAD — unbounded label values (path could be anything)
var pathRequests = promauto.NewCounterVec(prometheus.CounterOpts{
    Name: "limen_gateway_path_requests_total",
    Help: "Total requests per path.",
}, []string{"path"}) // cardinality explosion risk
```

**Keep label cardinality low.** Labels with unbounded values (user IDs, request IDs, full URL paths) cause memory growth in the Prometheus client and slow down queries. Prefer bucketed or aggregated label values when high cardinality is unavoidable.

### Export Safe Wrapper Functions

Declare metrics as unexported (`counterVar`) and expose typed helper functions (`IncEventsDropped()`). This prevents callers from arbitrarily setting values (e.g., `counterVar.Set(999)`) and keeps the public API intent-driven:

```go
// prometheus.go
var eventsDroppedTotal = promauto.NewCounter(prometheus.CounterOpts{
    Name: "limen_billing_events_dropped_total",
    Help: "Total billing events dropped due to Valkey failures or fallback capacity overflows.",
})

// IncEventsDropped should be called whenever a billing event fails to deliver.
func IncEventsDropped() {
    eventsDroppedTotal.Inc()
}
```

## Standard Code Layout Pattern

Below is a canonical `prometheus.go` demonstrating the expected layout, declaration style, and exported helpers. This is the pattern to follow for any new metrics file.

```go
// internal/billing/metrics/prometheus.go
package metrics

import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
)

var (
    // eventsDroppedTotal tracks events dropped due to Valkey failures or
    // fallback capacity overflows.
    eventsDroppedTotal = promauto.NewCounter(prometheus.CounterOpts{
        Name: "limen_billing_events_dropped_total",
        Help: "Total number of billing events dropped due to Valkey failures or fallback capacity overflows.",
    })

    // streamEvictedTotal tracks messages evicted from the billing stream
    // or moved to the dead-letter queue.
    streamEvictedTotal = promauto.NewCounter(prometheus.CounterOpts{
        Name: "limen_billing_stream_evicted_total",
        Help: "Total number of billing stream messages evicted or moved to dead-letter queue.",
    })

    // requestDurationSeconds tracks the latency of tenant-scoped RPCs.
    // Labels: tenant_kind (human | service_account), status (ok | error).
    requestDurationSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
        Name:    "limen_billing_request_duration_seconds",
        Help:    "Duration of tenant-scoped billing RPCs in seconds.",
        Buckets: prometheus.DefBuckets,
    }, []string{"tenant_kind", "status"})
)

// ---------------------------------------------------------------------------
// Public helpers — subsystem code calls these, not the raw prometheus objects.
// ---------------------------------------------------------------------------

// IncEventsDropped increments the billing events dropped counter.
func IncEventsDropped() {
    eventsDroppedTotal.Inc()
}

// IncStreamEvicted increments the stream eviction counter.
func IncStreamEvicted() {
    streamEvictedTotal.Inc()
}

// ObserveRequestDuration records a single RPC latency observation.
func ObserveRequestDuration(kind string, ok bool, durationSec float64) {
    status := "ok"
    if !ok {
        status = "error"
    }
    requestDurationSeconds.WithLabelValues(kind, status).Observe(durationSec)
}
```

Key conventions demonstrated:

1. **Unexported metric variables** (`eventsDroppedTotal`) — callers cannot mutate directly.
2. **Descriptive comments** above each declaration explaining what the metric measures.
3. **Prometheus naming convention** in `Name` field (namespace + subsystem + description + type suffix).
4. **Clear `Help` text** that reads as a complete sentence.
5. **`*Vec` constructors** with a small, bounded label set.
6. **Exported helper functions** that match the operation semantics (`Inc...`, `Observe...`, `Set...`).

## Testing Standards

### Unit Testing with `testutil`

Use `github.com/prometheus/client_golang/prometheus/testutil` to assert metric values in unit tests. The standard approach is `testutil.ToFloat64()` to read the current value of a counter, gauge, or histogram, and then assert the difference matches expected increments.

```go
package metrics

import (
    "testing"

    "github.com/prometheus/client_golang/prometheus/testutil"
)

func TestConsumer_SweepDLQ_IncrementsEvictionCounter(t *testing.T) {
    // Arrange: set up mock that triggers a DLQ move
    mock := &mockValkeyClient{shouldMoveToDLQ: true}
    c := NewConsumer(mock, nil, nil, "test-consumer")

    // Act: read counter before, call the method, read counter after
    before := testutil.ToFloat64(streamEvictedTotal)
    c.sweepDLQ(context.Background())
    after := testutil.ToFloat64(streamEvictedTotal)

    // Assert: counter incremented exactly once
    if after != before+1 {
        t.Errorf("streamEvictedTotal: expected %f -> %f (+1), got %f -> %f",
            before, before+1, before, after)
    }
}
```

### Testing Counters

For counters, use the before/after pattern shown above. Since counters are monotonically increasing and registered at `init()`, always read the value before and after the operation rather than asserting an absolute value.

### Testing Histograms

For histogram metrics, use `testutil.ToFloat64()` on the histogram to verify the `_count` metric incremented:

```go
func TestObserveRequestDuration(t *testing.T) {
    before := testutil.ToFloat64(requestDurationSeconds)
    ObserveRequestDuration("service_account", true, 0.050)
    after := testutil.ToFloat64(requestDurationSeconds)

    if after != before+1 {
        t.Errorf("requestDurationSeconds count: expected %f -> %f (+1), got %f -> %f",
            before, before+1, before, after)
    }
}
```

For more detailed bucket verification, use `testutil.CollectAndCompare()` with a text exposition format string:

```go
import (
    "bytes"
    "github.com/prometheus/client_golang/prometheus/testutil"
)

func TestHistogramBuckets(t *testing.T) {
    expected := `
# HELP limen_billing_request_duration_seconds Duration of tenant-scoped billing RPCs in seconds.
# TYPE limen_billing_request_duration_seconds histogram
limen_billing_request_duration_seconds_bucket{tenant_kind="service_account",status="ok",le="0.005"} 0
limen_billing_request_duration_seconds_bucket{tenant_kind="service_account",status="ok",le="0.01"} 0
limen_billing_request_duration_seconds_bucket{tenant_kind="service_account",status="ok",le="0.025"} 1
...
`
    if err := testutil.CollectAndCompare(requestDurationSeconds, bytes.NewBufferString(expected),
        "limen_billing_request_duration_seconds_bucket"); err != nil {
        t.Fatal(err)
    }
}
```

### Integration Testing

When Valkey or Postgres are involved (testcontainers), verify metrics alongside data assertions:

```go
func TestRecorder_FallbackDrain_IncrementsDroppedOnFull(t *testing.T) {
    store := openMigratedStore(t)
    recorder := NewBillingRecorder(nil, store, zap.NewNop())

    before := testutil.ToFloat64(eventsDroppedTotal)

    // Fill the fallback buffer past capacity
    for i := 0; i < 2000; i++ {
        recorder.RecordActiveUser(ctx, 1, int64(i), 0)
    }

    after := testutil.ToFloat64(eventsDroppedTotal)

    if after == before {
        t.Fatal("expected eventsDroppedTotal to increment")
    }

    if recorder.Dropped() == 0 {
        t.Fatal("expected dropped count > 0")
    }
}
```

### Test Organization

- Place metric assertions alongside the functional tests they validate — do not create a separate `metrics_test.go` file for every metric.
- Group related metric tests together (e.g., "DLQ sweep metrics", "recorder drop metrics").
- Use `testutil` for direct counter/gauge/histogram checks; use the subsystem's own accessor methods (e.g., `recorder.Dropped()`) for programmatic verification.

## Active Metrics Registry

### Billing Subsystem

| Metric | Type | Help | Called From |
| ------ | ---- | ---- | ----------- |
| `limen_billing_events_dropped_total` | Counter | Total number of billing events dropped due to Valkey failures or fallback capacity overflows. | `BillingRecorder.RecordActiveUser`, `BillingRecorder.RecordSAConnection`, `BillingRecorder.fallbackDrainLoop` |
| `limen_billing_stream_evicted_total` | Counter | Total number of billing stream messages evicted or moved to dead-letter queue. | `Consumer.sweepDLQ` |

### Counter Details

#### `limen_billing_events_dropped_total`

Incremented whenever a billing event cannot be delivered:

- **Valkey XADD failure**: Connection lost, stream eviction due to MAXLEN cap exceeded.
- **Fallback buffer overflow**: In-memory channel is full and drain cannot keep up (shed-load).
- **Fallback drain DB failure**: Postgres session creation, UPSERT/INSERT failure, or transaction commit failure.

```promql
# Alerting: sustained event drops
rate(limen_billing_events_dropped_total[5m]) > 0
```

#### `limen_billing_stream_evicted_total`

Incremented when a Valkey stream message exceeds the delivery threshold (default: 5 retries) and is moved to the dead-letter queue (`billing:dlq`). This indicates the consumer has repeatedly failed to process a message.

```promql
# Alerting: any DLQ movement warrants investigation
increase(limen_billing_stream_evicted_total[1h]) > 0
```

## Best Practices Checklist

Use this checklist when introducing new metrics to the codebase:

### Naming
- [ ] Name starts with `limen_` (fixed namespace)
- [ ] Follows `limen_<subsystem>_<name>[_<unit>]_<type>` pattern
- [ ] Counter ends with `_total`
- [ ] Snake case throughout — no camelCase, hyphens, or dots
- [ ] Standard unit suffix where applicable (`_seconds`, `_bytes`)

### Declaration
- [ ] Declared in a `metrics/prometheus.go` file within the owning subsystem
- [ ] Uses `promauto.NewCounter`, `promauto.NewGauge`, or `promauto.NewHistogramVec`
- [ ] Metric variable is unexported (`camelCase` name)
- [ ] `Help` text is a complete sentence, starting with a capital letter
- [ ] `Name` field exactly matches the declared metric name string

### Public API
- [ ] Exported helper function(s) exist for each metric (`Inc...`, `Set...`, `Observe...`)
- [ ] Helper function names describe the **operation**, not the metric
- [ ] Callers invoke helpers, not raw prometheus objects

### Labels
- [ ] Label values are bounded (finite set, not user IDs or request IDs)
- [ ] Label names are descriptive and snake_case
- [ ] Total cardinality (label value combinations) is reasonable (< 10K per metric)

### Testing
- [ ] At least one test covers the metric incrementing with `testutil.ToFloat64`
- [ ] Counter tests use the before/after pattern (not absolute assertion)
- [ ] Tests are co-located with the functional tests they validate

### Operational Readiness
- [ ] Metric can be queried with a standard PromQL expression (`rate()`, `increase()`)
- [ ] Alerting threshold considered (is this something to page on, or just observe?)
- [ ] Documented in this file under the Active Metrics Registry section

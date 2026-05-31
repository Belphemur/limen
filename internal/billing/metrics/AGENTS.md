# AGENTS.md — `internal/billing/metrics`

## What this is

The billing telemetry system. It is divided into two phases:
- **Publishing:** Non-blocking asynchronous writing of incoming telemetry events to a Redis/Valkey Stream using the `XADD` command. Handled inside the gateway layer.
- **Consumption (Decoupled):** Draining the stream, batching records, querying PostgreSQL for tenancy/upstream details, and inserting processed records into the database. Handled by `limen-observer` or `limen-allinone` (fallback).

## Key Components

- `recorder.go`: Publishes metrics safely with fallbacks.
- `consumer.go`: Consumes telemetry streams with robust backoff, retries, batching, and error handling.
- `queries.go`: Performs queries against Postgres and Valkey.
- `prometheus.go`: Exports internal metrics for Prometheus scraping.

## Conventions

- **Never block the gateway.** Telemetry streaming should have minimal performance overhead on HTTP/SSE clients.
- **Safe decryption.** Decrypt sensitive upstream configuration metadata using the AES-SIV cipher.

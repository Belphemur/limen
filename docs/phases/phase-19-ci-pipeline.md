---
phase: "19"
title: "CI Pipeline (Parallelized GHA tests and Gate checks)"
status: completed
progress: 100
depends_on: ["10", "13b"]
updated: "2026-05-31"
---

# Phase 19 — CI Pipeline (Parallelized GHA tests and Gate checks)

> **Depends on**: [Phase 10](phase-10-wiring-hardening.md) (Wiring, verification, hardening), [Phase 13b](phase-13b-billing-metrics-pipeline.md) (Billing Metrics Pipeline)
> **Unblocks**: [Phase 20](phase-20-cd-pipeline.md) (CD Pipeline & Multi-Arch Packaging)
> **Status**: Completed

## Goal

Design, implement, and optimize a highly parallelized, fast-running GitHub Actions CI pipeline on Linux that isolates backend unit, backend integration, frontend unit, and frontend E2E tests, and reports consolidated test status via a robust Merge Gate.

## Background

With the codebase now containing multiple Go binaries (gateway, portal, staff) and two Vue 3 SPAs, a single sequential test job is too slow and mixes independent failure domains. This phase introduces a parallel CI topology that runs independent test suites concurrently and aggregates results through a gate job that handles skipped/cancelled edge cases.

## Design

### Build Tag Isolation

Use Go build tags to separate container-dependent integration tests from lightweight unit tests:

```go
//go:build integration
```

Integration test files use `//go:build integration`; unit test files either use `//go:build !integration` or omit build tags (defaulting to the unit set). This guarantees:

- `go test -v ./...` runs only unit tests (no Docker/testcontainers dependency)
- `go test -v -tags=integration ./...` runs the full integration suite

### Centralized Runtime Versions

- **Go**: Read from `go.mod` — currently Go 1.26. The CI uses `actions/setup-go` and pins to this version.
- **Node**: Read from `.node-version` — contains `24`. The CI uses this file for consistency across all Node/JavaScript tooling.

### Parallel Job Matrix

Five parallelizable jobs, all running on `ubuntu-latest`:

| Job | Description | Caching Strategy |
|-----|-------------|------------------|
| `backend-unit` | `go test ./...` — no Docker dependencies | Go module cache (`~/go/pkg/mod`), Go build cache (`~/.cache/go-build`) |
| `backend-integration` | `go test -tags=integration ./...` — uses testcontainers-go for Postgres, Valkey | Go module cache, build cache, testcontainers image cache |
| `frontend-unit` | `pnpm test —run` for Portal and Admin SPAs | PNPM store cache (`pnpm/action-setup@v6`), `node_modules` cache |
| `frontend-e2e-portal` | Playwright E2E for Portal SPA | PNPM store cache, Playwright browser cache (`~/.cache/ms-playwright`) |
| `frontend-e2e-admin` | Playwright E2E for Admin SPA | PNPM store cache, Playwright browser cache (`~/.cache/ms-playwright`) |

### Merge Gate

The `ci-gate` job uses `if: always()` to run regardless of upstream job success/failure/skip/cancel. An explicit result-checking script iterates over all dependency jobs and determines the consolidated status. This prevents false-positives from jobs that were skipped due to path filters or cancelled mid-run.

```yaml
# Simplified gate logic
if (jobs['backend-unit'].result != 'success') return false;
if (jobs['backend-integration'].result != 'success') return false;
// ... etc
```

### Workflow File

Single workflow: `.github/workflows/ci.yml`

```
.github/workflows/ci.yml
  ├── jobs:
  │   ├── backend-unit         (ubuntu-latest)
  │   ├── backend-integration  (ubuntu-latest)
  │   ├── frontend-unit        (ubuntu-latest)
  │   ├── frontend-e2e-portal  (ubuntu-latest)
  │   ├── frontend-e2e-admin   (ubuntu-latest)
  │   └── ci-gate              (ubuntu-latest, needs: all above, if: always())
```

## Deliverables

| File | Change |
|------|--------|
| `.node-version` | Created with content `24` |
| `.github/workflows/ci.yml` | New file: parallel CI pipeline |
| `**/*_test.go` (24 files) | Add `//go:build integration` build tags |
| `docs/phases/phase-19-ci-pipeline.md` | This file |
| `docs/phases/README.md` | Updated index |

## Risks

- **Testcontainers in CI**: `backend-integration` requires Docker-in-Docker or a Docker socket mount. GitHub Actions provides this natively on `ubuntu-latest`. Ensure testcontainers-go uses the correct Ryuk container and cleans up properly.
- **Playwright browser cache size**: The `~/.cache/ms-playwright` directory can be ~270 MB. Cache size limits in GHA are 10 GB per repo, well within budget, but restore times should be monitored.
- **Flaky E2E tests**: Frontend E2E jobs can be non-deterministic. The `frontend-e2e-*` jobs should use `playwright test --retries=2` to reduce flakiness.
- **Build tag scope**: Exactly 23 database-dependent test files need the integration tag. Adding tags to the wrong files could hide tests from the default `go test ./...` run. Verification must confirm zero Docker dependencies for unit tests.

## Checklist

- [x] Add `//go:build integration` build tags to all database-dependent test files (24 files total)
- [x] Verify backend unit tests run in isolation with zero Docker dependencies: `go test -v ./...`
- [x] Verify backend integration tests execute successfully with testcontainers-go: `go test -v -tags=integration ./...`
- [x] Create a `.node-version` file containing `24` in the workspace root
- [x] Implement `.github/workflows/ci.yml` containing the 5 parallelizable test jobs and `ci-gate`
- [x] Verify GHA workflow syntax using `actionlint`

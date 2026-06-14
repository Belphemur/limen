---
phase: "7b"
title: "DCR per-client Zitadel projects (JIT in tenant org)"
status: completed
progress: 100
depends_on: ["5", "6"]
updated: "2026-06-14"
---

# Phase 7b — DCR per-client Zitadel projects (JIT in tenant org)

**Depends on**: Phase 5 (DCR proxy at `/t/{tenant}/oauth/register`), Phase 6 (Resource Server)
**Unblocks**: cleaner per-tenant authz surface; MCP clients (Cursor, Claude Desktop, VS Code, …) each get their own project under the tenant's Zitadel organization.

## Goal

When an MCP client dynamic-registers through Limen's DCR proxy, the resulting
Zitadel OIDC application should live in **a dedicated project owned by the
tenant's Zitadel organization** — named after the registering client
(`client_name`) — instead of inside Limen's shared gateway project.

Concretely, a DCR call with `client_name: "Cursor"` against tenant `acme`
must produce:

```
Zitadel
└── Organization: acme           (tenant.zitadel_org_id)
    └── Project: "Cursor"        (newly created, or reused if already present)
        └── OIDC Application: "Cursor [<ulid>]"   (DCR-created)
```

The Limen shared project (`zitadel.project_id` in config) is reserved for
the Portal SPA app and the MCP Resource Server app — it must **never**
receive DCR-created applications.

## Why

Before this phase, every DCR registration created an OIDC app inside Limen's
shared project, regardless of the tenant. That bound MCP-client app
lifecycles to a Limen-controlled project and made cross-tenant cleanup
awkward: dropping a tenant org left orphan apps behind in the shared
project. It also conflated two very different roles for the shared project
(housing Limen's own apps vs. an arbitrarily large set of tenant-customer
apps).

Per-client projects, scoped to the tenant org, give us:

- **Clean ownership**: deleting a tenant org cascades the projects and their
  apps. No orphan cleanup logic.
- **Logical grouping in the Zitadel console**: an operator opening
  `acme`'s org sees "Cursor", "Claude Desktop", "Custom-MCP" as separate
  projects instead of a wall of apps in someone else's project.
- **A natural authorization boundary**: Zitadel project roles and project
  grants apply per-app-family if we ever want to differentiate them.

## Design

### JIT find-or-create

Project creation is **just-in-time** and **idempotent on name**:

1. Limen receives `POST /t/{tenant}/oauth/register` with
   `client_name: "Cursor"` (after RFC 7591 normalization; missing
   `client_name` falls back to a synthesized name as before).
2. Limen calls `ProjectServiceV2.ListProjects` against the tenant org with a
   `ProjectNameFilter{Method: TEXT_FILTER_METHOD_EQUALS, ProjectName: "Cursor"}`.
3. If exactly one project with that name exists, reuse its `project_id`.
4. Otherwise, call `ProjectServiceV2.CreateProject` with
   `OrganizationId = tenant.ZitadelOrgID`, `Name = client_name`.

Project names are **not ULID-suffixed**. We want re-registrations of the
same client name to land in the same project. (The DCR-created application
inside the project keeps its `[<ULID>]` suffix, since Zitadel enforces app
name uniqueness inside a project.)

### Zitadel SDK wrapper

A new method lives in `internal/zitadel/projects.go`:

```go
// EnsureProject returns the projectID of a project named `name` inside
// orgID. If no such project exists it is created. The lookup uses an
// EQUALS name filter; the call is safe to retry.
func (c *Client) EnsureProject(ctx context.Context, orgID, name string) (string, error)
```

The implementation mirrors `scripts/zitadel-bootstrap/main.go::ensureProject`
but is a public package method available to the DCR handler.

### Application service rewiring

`AddOIDCAppInput`, `UpdateOIDCAppInput`, and the `DeleteOIDCApp` /
`GetOIDCApp` signatures gain a per-call `ProjectID` field/argument. When
set, that project is used; otherwise the call falls back to
`Client.projectID` (Limen's shared project, still used by the Portal SPA
and MCP RS bootstrap paths). Concretely:

```go
type AddOIDCAppInput struct {
    OrgID        string
    ProjectID    string // NEW — required for DCR-created apps
    Name         string
    // … existing fields
}

type UpdateOIDCAppInput struct {
    OrgID     string
    ProjectID string // NEW — required for DCR-created apps
    AppID     string
    // … existing fields
}

func (c *Client) DeleteOIDCApp(ctx context.Context, orgID, projectID, appID string) error
func (c *Client) GetOIDCApp(ctx context.Context, orgID, projectID, appID string) (*OIDCApp, error)
```

`internal/oauthproxy/dcr.go` is the only caller of these methods for DCR
flows, and threads `ProjectID` through from the per-row mirror.

### Storage: persist the project id

`storage.ZitadelApp` gains a `ZitadelProjectID` column so RFC 7592
management requests (`GET/PUT/DELETE /register/{client_id}`) can address
the right project without re-querying Zitadel:

```go
type ZitadelApp struct {
    Base
    TenantID         int64
    ZitadelAppID     string
    ZitadelProjectID string // NEW — project that owns the app
    // … existing fields
}
```

A new goose migration `00005_phase7b_dcr_project.sql` adds the column:

```sql
ALTER TABLE zitadel_apps
ADD COLUMN IF NOT EXISTS zitadel_project_id text NOT NULL DEFAULT '';
```

The `NOT NULL DEFAULT ''` keeps existing rows valid; the application code
treats empty `ZitadelProjectID` on read as "this row pre-dates phase 7b,
fall back to the shared project". New DCR rows always populate it.

### DCR handler flow

`(h *DCRHandler).Register`:

1. Normalize the request → `client_name`.
2. `projectID := h.apps.EnsureProject(ctx, tenant.ZitadelOrgID, client_name)`.
3. `app := h.apps.AddOIDCApp(ctx, AddOIDCAppInput{OrgID, ProjectID: projectID, …})`.
4. Persist the mirror row with `ZitadelProjectID = projectID`.

Rollback on subsequent failure: delete the app (`DeleteOIDCApp`). We
deliberately **do not** delete the project on rollback — it may host other
apps from prior registrations under the same client name. Empty projects
are cheap; an operator can prune them out-of-band.

`(h *DCRHandler).{Get,Update,Delete}` read `row.ZitadelProjectID` and
forward it to the app-service wrappers.

### appManager interface (test seam)

`internal/oauthproxy/dcr.go::appManager` gains:

```go
EnsureProject(ctx context.Context, orgID, name string) (string, error)
```

The fake in `dcr_integration_test.go` records calls and asserts:

- `EnsureProject` is invoked exactly once per registration.
- `AddOIDCApp` receives the project id returned by `EnsureProject`.
- Repeated registrations of the same `client_name` against the same tenant
  receive the same project id.

## Files

- `internal/zitadel/projects.go` (new) — `EnsureProject`.
- `internal/zitadel/apps.go` — extend `AddOIDCAppInput`, `UpdateOIDCAppInput`,
  and the `Delete`/`Get` signatures to accept per-call `ProjectID`.
- `internal/oauthproxy/dcr.go` — call `EnsureProject` in `Register`, thread
  `ProjectID` through all four handlers, persist on the mirror row.
- `internal/storage/models.go` — add `ZitadelProjectID` to `ZitadelApp`.
- `internal/storage/migrations/postgres/00005_phase7b_dcr_project.sql` (new).
- `internal/oauthproxy/dcr_integration_test.go` — extend `fakeAppManager`,
  add coverage for find-vs-create paths.

## Verification

- Register a brand-new client name from Cursor → check the Zitadel console
  shows a freshly-created project under the tenant org, with the OIDC app
  inside it. Token issuance still works end-to-end.
- Re-register the same `client_name` from a different machine →
  `ProjectServiceV2.ListProjects` returns the existing project; a second
  OIDC app is created **inside the same project** (with a different ULID
  suffix).
- Delete the tenant org in Zitadel → both projects vanish; Limen's mirror
  rows are deletable without leaving Zitadel orphans.
- Integration test: assert two consecutive `Register` calls with the same
  `client_name` produce identical `ZitadelProjectID` on the two mirror
  rows.

## Checklist

- [ ] `internal/zitadel/projects.go::EnsureProject` lands with a unit test
      stubbing `ProjectServiceV2`.
- [ ] `AddOIDCAppInput.ProjectID` / `UpdateOIDCAppInput.ProjectID` / new
      `projectID` args on Delete/Get; all callers updated.
- [ ] `storage.ZitadelApp.ZitadelProjectID` column + migration applied;
      AutoMigrate covers the new field shape.
- [ ] `dcr.go::Register` creates the project first, persists the project id
      on the mirror, threads it into `AddOIDCApp`.
- [ ] `dcr.go::{Read,Update,Delete}` use `row.ZitadelProjectID`.
- [ ] `fakeAppManager` in tests records `EnsureProject`; tests cover both
      first-registration and idempotent-on-name paths.
- [ ] `make` toolchain green (`go mod tidy`, `go fmt`, `go vet`,
      `golangci-lint`, `go test ./...`).

---
name: model-review
description: Storage model change review methodology for the Limen project. Use when reviewing any change to files under internal/storage/ (models, migrations, hooks). Enforces the GORM AutoMigrate vs. goose migration split defined in MIGRATIONS.md.
---

# Model Review — Schema Change Enforcement

The Limen schema is managed by two systems: GORM `AutoMigrate` and goose SQL migrations. This skill ensures every model change follows the correct path per `internal/storage/MIGRATIONS.md`.

## Core Rule

**New columns, indexes from struct tags, and table creation all belong to the GORM model — not goose.** Goose migrations are only for things GORM cannot model: RLS policies, triggers, functions, partial/expression indexes, CHECK/EXCLUDE constraints, data backfills, and column drops/renames.

## Decision Tree

Copy this from `internal/storage/MIGRATIONS.md`:

```
Change to a GORM model?
│
├── New column / dropped-but-keep-data column / changed type / new index from
│   a struct tag?
│       → AutoMigrate handles it. No goose migration needed.
│       → 🚫 REJECT any PR that adds a goose migration for this.
│
├── New tenant-scoped model (embeds TenantID)?
│       → GORM creates the table. ALSO need goose for:
│           - ENABLE/FORCE RLS + tenant_isolation policy
│           - set_updated_at trigger
│
├── Partial / expression index, CHECK constraint, EXCLUDE constraint,
│   GENERATED column, view, function, trigger?
│       → Goose migration. Required.
│
├── Data backfill or one-time correction?
│       → Goose migration. Required. Must be idempotent.
│
├── Drop / rename a column?
│       → Goose migration. Two-step for production.
│
└── Anything touching tenants, RLS policies, set_updated_at, goose machinery?
        → Goose migration. Required.
```

## Review Checklist

When reviewing a PR with model changes, verify ALL of:

- [ ] **No unnecessary migrations.** A new GORM struct field (column, index, type change) must NOT have a goose migration. GORM AutoMigrate handles it.
- [ ] **Required migrations present.** New tenant-scoped models MUST have goose migrations for RLS + trigger. Partial indexes, constraints, backfills MUST have goose.
- [ ] **Model completeness.** New models embed `Base`, implement `BeforeCreate` setting `PublicID`, and are listed in `AllModels()`.
- [ ] **Tenant scope.** Tenant-scoped models embed `TenantID int64` with `gorm:"not null;index"`.
- [ ] **Secret fields.** Credentials use `crypto.SecretField`, never `[]byte` or `string`.
- [ ] **Partial unique indexes.** Composite uniques use `where:deleted_at IS NULL` so soft-deletes don't block re-creation.
- [ ] **Cascade soft-delete.** Parent models implement `BeforeDelete` hook cascading to children, not service-layer cascades.
- [ ] **Migration naming.** Follows `NNNNN_short_snake_case.sql` convention. One concern per file.
- [ ] **Migration idempotency.** Uses `IF NOT EXISTS`, `IF EXISTS`, `ON CONFLICT DO NOTHING`.

Base directory for this skill: file:///home/balor/repos/limen/.agents/skills/model-review
Relative paths in this skill are relative to this base directory.

# AGENTS.md — `internal/ids`

## What this package is

Public identifier helpers. Every persistent entity carries two IDs:

- an internal `int64` (DB-assigned `BIGSERIAL`) used for joins and FKs;
- a public `string` of the form `<prefix>_<27-char-KSUID>` — the **only**
  identifier that appears in HTTP, Connect-RPC, the SPA, logs, or OAuth
  redirect URLs.

This package owns the public-ID format and the canonical prefix registry.

## Public surface

| Symbol                                         | Purpose                                                                   |
| ---------------------------------------------- | ------------------------------------------------------------------------- |
| `Prefix` (string type)                         | Stable per-entity tag (e.g. `tnt`, `usr`). Never rename once shipped.     |
| `Prefix*` constants in `prefixes.go`           | The full registry — append-only.                                          |
| `New(p Prefix) string`                         | Mint a fresh ID: `<p>_<ksuid.New()>`.                                     |
| `Parse(s string) (Prefix, ksuid.KSUID, error)` | Split a public ID into its parts and validate the KSUID body.             |
| `MustParse(expected, s)`                       | Parse and assert the prefix matches; intended for handler-level decoding. |

## Conventions

- **KSUIDs are time-sortable.** Cursor pagination is
  `WHERE public_id > $cursor ORDER BY public_id LIMIT n` — no need for a
  separate `created_at` index.
- **Prefixes are forever.** Renaming or reusing a prefix breaks all
  externally-stored URLs, audit logs, and bookmarks. Add new prefixes; do
  not change old ones.
- **Models assign via `BeforeCreate`.** Storage models call `ids.New(...)`
  from their GORM `BeforeCreate` hook so callers cannot accidentally leave
  `PublicID` empty.
- **Never log or marshal the internal `ID`.** `Base.ID` carries `json:"-"`;
  keep it that way.

## When to extend

- **New entity**: add a `Prefix*` constant in `prefixes.go`, add a `BeforeCreate`
  in the new model that calls `ids.New(<prefix>)`, and add the prefix row to the
  table in `docs/phases/phase-01-database-foundation.md`.

## What this package is NOT

- Not a UUID generator — we use KSUIDs specifically for lexicographic ordering.
  If you need an opaque random ID, still use a KSUID with an appropriate prefix.
- Not a JWT / opaque-token helper. Tokens live elsewhere (Phase 4/5).

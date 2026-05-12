# AGENTS.md — `internal/ids`

## What this package is

Public identifier helpers. Every persistent entity carries two IDs:

- an internal `int64` (DB-assigned `BIGSERIAL`) used for joins and FKs;
- a public `string` of the form `<prefix>_<26-char-ULID>` — the **only**
  identifier that appears in HTTP, Connect-RPC, the SPA, logs, or OAuth
  redirect URLs.

This package owns the public-ID format and the canonical prefix registry.
We use [oklog/ulid v2](https://github.com/oklog/ulid): Crockford base32,
millisecond timestamp resolution, and `ulid.Make` is monotonic within the
same millisecond.

## Public surface

| Symbol                                       | Purpose                                                                   |
| -------------------------------------------- | ------------------------------------------------------------------------- |
| `Prefix` (string type)                       | Stable per-entity tag (e.g. `tnt`, `usr`). Never rename once shipped.     |
| `Prefix*` constants in `prefixes.go`         | The full registry — append-only.                                          |
| `New(p Prefix) string`                       | Mint a fresh ID: `<p>_<ulid.Make()>`.                                     |
| `Parse(s string) (Prefix, ulid.ULID, error)` | Split a public ID into its parts and validate the ULID body.              |
| `MustParse(expected, s)`                     | Parse and assert the prefix matches; intended for handler-level decoding. |

## Conventions

- **ULIDs are time-sortable at millisecond resolution.** Cursor pagination is
  `WHERE public_id > $cursor ORDER BY public_id LIMIT n` — no need for a
  separate `created_at` index. `ulid.Make` is monotonic within a process, so
  back-to-back IDs in the same millisecond still sort correctly.
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

- Not a UUID generator — we use ULIDs specifically for lexicographic ordering.
  If you need an opaque random ID, still use a ULID with an appropriate prefix.
- Not a JWT / opaque-token helper. Tokens live elsewhere (Phase 4/5).

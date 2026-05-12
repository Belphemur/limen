# AGENTS.md — `internal/crypto`

## What this package is

Secret-at-rest primitives for storage models. In v1 it exposes a single type,
`SecretField`, that GORM and `database/sql` treat as a `bytea` column.

**Status: Phase 1 stub.** `SecretField` currently stores plaintext bytes.
Phase 2 replaces the `Scan` / `Value` implementations with authenticated
encryption (AES-GCM with a key derived from `LIMEN_TOKEN_ENCRYPTION_KEY`).
The type identity and DDL do not change, so callers and migrations are stable
across the upgrade.

## Public surface

| Symbol                   | Purpose                                                              |
| ------------------------ | -------------------------------------------------------------------- |
| `SecretField` (`[]byte`) | Round-trips through `database/sql`; rendered as `bytea` in Postgres. |
| `Scan` / `Value`         | `sql.Scanner` / `driver.Valuer` for transparent column I/O.          |
| `GormDataType()`         | Tells GORM to emit `bytea` rather than inferring from `[]byte`.      |

## Conventions

- A field that stores credentials, tokens, refresh tokens, registration access
  tokens, or any user-supplied secret payload **must** be typed `SecretField`,
  not `[]byte` or `string`. The type carries Phase 2's encryption invisibly.
- Always copy the buffer in `Scan` — `database/sql` may reuse the underlying
  array between rows.
- Treat `nil` as "absent" and `[]byte{}` as "empty value"; do not collapse the
  two.

## When to extend

- **Phase 2 encryption**: replace the body of `Scan`/`Value` to do
  AES-GCM-encrypt-then-store and decrypt-on-read. Do not change the exported
  type, methods, or `GormDataType()`. Add a `crypto.Init(key []byte) error`
  to receive the master key.
- **New secret-bearing model field**: just declare it as `crypto.SecretField`
  with the GORM tag `type:bytea`. Nothing else to wire.

## What this package is NOT

- Not a key management service. The master key comes from configuration
  (`LIMEN_TOKEN_ENCRYPTION_KEY`); rotation and KMS integration are Phase 2's
  responsibility.
- Not a hashing library. For password hashing we defer to Zitadel; for
  arbitrary application hashing, use `crypto/sha256` directly.
- Not for transport-layer secrets (HTTP body, headers). Those go through
  the normal request/response path under TLS.

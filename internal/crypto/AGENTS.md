# AGENTS.md — `internal/crypto`

## What this package is

Secret-at-rest primitives for storage models. v1 ships:

- **AES-SIV (RFC 5297)** via `github.com/jedisct1/go-aes-siv`. We pick SIV
  over plain GCM because it is nonce-misuse-resistant — a duplicated nonce
  only leaks plaintext equality, not the plaintext itself. Encryption is
  still randomized (fresh 16-byte nonce per call) so stored ciphertexts
  don't reveal duplicates.
- **AAD binding** (`tenant|user|kind`) so a ciphertext stolen from one
  column can't be replayed into another column, another tenant, or another
  user.
- **`SecretField`** — a GORM/`database/sql` type that transparently
  encrypts on `Value()` and decrypts on `Scan()` when a process-wide
  `Cipher` has been registered via `SetCipher`. Without a registered
  Cipher it falls back to plaintext passthrough, which keeps the storage
  integration tests cheap.

## Public surface

| Symbol                       | Purpose                                                             |
| ---------------------------- | ------------------------------------------------------------------- | --------- | ------- | --------------- |
| `Key [32]byte`               | AES-128-SIV key (32-byte total per RFC 5297 §2.6).                  |
| `ParseKey(string) (Key, _)`  | Decode a base64 or 64-char hex key; rejects wrong-sized input.      |
| `AAD{TenantID,UserID,Kind}`  | Required AAD; `TenantID` and `Kind` mandatory, `UserID` optional.   |
| `NewCipher(Key) (*Cipher,_)` | Build an AES-SIV AEAD.                                              |
| `Cipher.Encrypt/Decrypt`     | Sealed format: `0x01                                                | nonce(16) | tag(16) | ciphertext(N)`. |
| `SetCipher(*Cipher) *Cipher` | Register the process-wide Cipher; returns the previous (for tests). |
| `ActiveCipher() *Cipher`     | Inspect the registered Cipher.                                      |
| `SecretField`                | GORM `bytea` field with per-instance AAD binding via `SetAAD`.      |

## Conventions

- A column that stores credentials, tokens, refresh tokens, registration
  access tokens, or any other sensitive payload **must** be typed
  `SecretField`, not `[]byte` or `string`.
- Always call `field.SetAAD(tenantID, userID, kind)` before `Save` and
  before `Find` when a `Cipher` is active. There is no implicit AAD —
  this is verbose by design so every encryption decision is auditable.
- `kind` strings are short stable labels (`upstream.access_token`,
  `upstream.refresh_token`, `dcr.client_secret`, …). Treat them like
  database column names: don't rename casually, they become part of the
  ciphertext's identity.
- Never log AAD components that include user identifiers. Log the `Kind`
  only.
- `Scan` copies the driver buffer — `database/sql` may reuse the
  underlying array between rows.
- Treat `nil` as "absent" and `[]byte{}` as "empty value"; do not collapse
  the two.

## When to extend

- **Key rotation**: bump `versionV1` → `versionV2` with a key-id byte and
  add a multi-key `Decrypt` path. Do not silently re-encrypt; surface
  rotation as a deliberate migration step.
- **New secret-bearing model field**: declare it as `crypto.SecretField`
  with the GORM tag `type:bytea`. Add a `SetAAD` call next to every
  Save/Find site for that field.

## What this package is NOT

- Not a key management service. The master key comes from configuration
  (`LIMEN_TOKEN_ENCRYPTION_KEY`); KMS integration is future work.
- Not a hashing library. For password hashing we defer to Zitadel; for
  arbitrary application hashing, use `crypto/sha256` directly.
- Not for transport-layer secrets (HTTP body, headers). Those go through
  the normal request/response path under TLS.

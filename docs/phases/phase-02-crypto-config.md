# Phase 2 — Crypto + config

**Depends on**: nothing (can run in parallel with Phase 1)
**Unblocks**: Phases 4, 5, 7, 8

## Goal

Two small but load-bearing concerns:

1. **A symmetric encryption primitive** used to protect anything sensitive at rest (refresh tokens, access tokens cached for upstream calls, OAuth client secrets, DCR registration access tokens, RSA private keys for the per-tenant inbound AS).
2. **Configuration upgrades**: environment-variable substitution, plus the new top-level sections (`database`, `security`, `oauth_server`) that the rest of the work depends on.

These two are bundled because they're both small, both blocking, and the encryption key is itself a config value.

**No hand-rolled crypto.** We use [`github.com/jedisct1/go-aes-siv`](https://pkg.go.dev/github.com/jedisct1/go-aes-siv) (an audited, dependency-free pure-Go implementation of RFC 5297) rather than wiring `crypto/aes` + `crypto/cipher` ourselves. AES-SIV is nonce-misuse-resistant, which removes a whole class of footguns that plain AES-GCM has.

## Design

### `internal/crypto/aessiv.go`

- **Algorithm**: AES-SIV (RFC 5297) via `github.com/jedisct1/go-aes-siv`. The library is a pure-Go implementation with zero non-stdlib deps and implements `cipher.AEAD`. Key size is 32 bytes (AES-128-SIV — internally split into a 16-byte CMAC key and a 16-byte CTR key, per RFC 5297 §2.6).
- **Why SIV over GCM**: AES-SIV is nonce-misuse-resistant. With GCM, a single nonce reuse compromises confidentiality and authenticity catastrophically; with SIV, the only thing leaked is whether two plaintexts under the same key+AAD are identical. We still randomize each encryption with a fresh 16-byte nonce so stored ciphertexts don't reveal duplicates — SIV's misuse resistance is a safety net, not a license to skip nonce hygiene.
- **Key source**: 32 raw bytes loaded from config (`${LIMEN_TOKEN_ENCRYPTION_KEY}`), accepted as base64 (standard or URL, padded or unpadded) or 64-character hex. Fail-fast at startup if the key is missing or the wrong length.
- **AAD binding**: Additional Authenticated Data is `tenant_id|user_id|kind` (UTF-8, pipe-separated). `kind` is a short stable label for the field (`upstream.access_token`, `upstream.refresh_token`, `dcr.client_secret`, `dcr.registration_access_token`, `signing_key.private_pem`, `portal.session.token`, …). This makes a ciphertext stolen from one column unusable in another column or for another tenant/user.
- **API**:

  ```go
  type Key [32]byte
  type Cipher struct { /* ... */ }

  func ParseKey(s string) (Key, error)
  func NewCipher(key Key) (*Cipher, error)
  func (c *Cipher) Encrypt(plaintext []byte, aad AAD) ([]byte, error)
  func (c *Cipher) Decrypt(ciphertext []byte, aad AAD) ([]byte, error)

  type AAD struct {
      TenantID string // required
      UserID   string // "" allowed
      Kind     string // required, e.g. "upstream.access_token"
  }

  func SetCipher(*Cipher) *Cipher  // process-wide; returns previous (test hook)
  func ActiveCipher() *Cipher
  ```

- **GORM glue**: `SecretField` (stubbed in Phase 1) becomes a real type that holds plaintext in memory and delegates encrypt/decrypt to `ActiveCipher()` on `Value()`/`Scan()`. The AAD has to be set on the field before `Save` and before `Find` via `field.SetAAD(tenantID, userID, kind)`. This is verbose but keeps AAD binding explicit and auditable. If no `Cipher` is registered (e.g. storage integration tests), `SecretField` falls back to plaintext passthrough.
- **Key rotation** (forward-compat): the on-disk format is `version(1) | nonce(16) | tag(16) | ciphertext(N)`. Version `0x01` = primary key only. Future rotation can introduce `0x02` with a key-id byte and multi-key decrypt without breaking existing rows.

### Config upgrades — `internal/config/config.go`

The current loader does not expand environment variables in YAML values. Add a pre-parse pass that replaces `${VAR}` and `${VAR:-default}` tokens with values from `os.Environ()`. Fail-fast on missing required variables (i.e. `${VAR}` with no default and no value set).

The full config surface after Phase 2 (additions vs. current file are marked **new**):

```yaml
server:
  bind: ":8080"
  base_url: "https://limen.example.com" # new — needed for AS issuer URLs

database: # new
  driver: postgres
  dsn: "${LIMEN_DB_DSN}"
  max_open_conns: 25
  max_idle_conns: 5

security: # new
  token_encryption_key: "${LIMEN_TOKEN_ENCRYPTION_KEY}" # base64 or hex, 32 bytes
  portal_session_cookie_name: "limen_portal"
  portal_session_cookie_secure: true

oauth_server: # new — Phase 5 uses these
  signing_algorithm: RS256
  access_token_ttl: 10m
  refresh_token_ttl: 720h
  dcr_initial_access_token: "" # if non-empty, /register requires it
  authorize_consent: skip # skip | always | first_time

codemode:
  # unchanged

# upstreams stays for now but will be replaced in Phase 7.
# Auth-block (existing) becomes obsolete in Phase 6 — kept for the transition.
```

Add `Validate()` methods on each section so misconfiguration surfaces at startup:

- Encryption key must decode to exactly 32 bytes.
- `database.dsn` must be a non-empty Postgres connection string.
- `base_url` must be absolute and not include trailing slashes.
- TTLs must be positive.

### Package layout

```
internal/crypto/
├── aessiv.go        // Key, ParseKey, AAD, Cipher, SetCipher, ActiveCipher
└── secret_field.go  // SecretField (GORM/database-sql glue)

internal/config/
└── config.go        // existing file gets expansion + new sections
```

## Deliverables

- New files: `internal/crypto/aessiv.go` (+ updated `secret_field.go`)
- New dep: `github.com/jedisct1/go-aes-siv`
- Modified files: `internal/config/config.go` (env substitution, new sections, validation)
- Updated `config.yaml` example with the new sections.

## Security & operational notes

- The encryption key is a high-value secret. Document storing it in a secrets manager (Vault, AWS Secrets Manager, Kubernetes secret); never in `config.yaml` literally.
- AAD is mandatory — `Encrypt`/`Decrypt` reject empty `TenantID` or `Kind`. A pipe character in any AAD component is also rejected so the `tenant|user|kind` encoding stays unambiguous.
- AES-SIV is nonce-misuse-resistant, but we still generate fresh nonces with `crypto/rand` per encryption — don't derive nonces deterministically.
- Logs must never contain `Cipher.Decrypt` results or AAD components that include user identifiers — wrap with `zap.String("kind", ...)` only.

## Verification

- Unit tests:
  - Roundtrip with correct AAD.
  - Two encryptions of the same plaintext produce different ciphertexts (fresh nonce).
  - Tamper bit in ciphertext → `Decrypt` returns error.
  - AAD mismatch (different tenant, different user, different kind) → `Decrypt` returns error.
  - Empty `TenantID` or `Kind` → `Encrypt` returns error; pipe character in any component → error.
  - Version byte mismatch → `Decrypt` returns error.
  - Short ciphertext → `Decrypt` returns error.
  - `SecretField` roundtrip with cipher registered; plaintext passthrough without cipher; error when AAD missing while cipher active.
- Config tests:
  - `${VAR}` substitution with present and missing variables.
  - `${VAR:-default}` fallback.
  - Validation errors for short keys, bad driver, non-absolute `base_url`, zero TTLs, unknown consent mode, unsupported signing algorithm.

## Risks

- **GORM custom-type ergonomics**: `Value`/`Scan` don't have ctx, so AAD has to be threaded via the field itself or a thread-local. Plan to thread it via the field (`SetAAD`). Document this clearly.
- **Migration of existing data**: not applicable yet — no production data exists.

## Checklist

- [x] `github.com/jedisct1/go-aes-siv` added to `go.mod`
- [x] `internal/crypto/aessiv.go` exports `Key`, `ParseKey`, `Cipher`, `NewCipher`, `AAD`, `SetCipher`, `ActiveCipher`
- [x] AES-SIV (RFC 5297, 32-byte key), random 16-byte nonce per encryption, version-byte prefix `0x01`
- [x] AAD constructed from `tenant|user|kind`; empty `TenantID`/`Kind` and pipe characters rejected
- [x] `SecretField.Scan`/`Value` integrated with GORM and exercised by a unit test (plaintext mode + encrypted mode)
- [x] `${ENV}` and `${ENV:-default}` substitution in `internal/config/config.go`
- [x] `DatabaseConfig`, `SecurityConfig`, `OAuthServerConfig` structs added with `Validate()` methods
- [x] Top-level `Config.Validate()` runs all section validations and reports the first failure
- [x] `config.yaml` example file updated with the new sections (commented placeholders, no real secrets)
- [x] Unit tests for crypto: roundtrip, fresh-nonce, tamper, AAD mismatch, version mismatch, short ciphertext
- [x] Unit tests for config: env substitution, defaults, validation failures
- [x] `go vet ./...` and `go build ./...` clean

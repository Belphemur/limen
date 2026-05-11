# Phase 2 — Crypto + config

**Depends on**: nothing (can run in parallel with Phase 1)
**Unblocks**: Phases 4, 5, 7, 8

## Goal

Two small but load-bearing concerns:

1. **A symmetric encryption primitive** used to protect anything sensitive at rest (refresh tokens, access tokens cached for upstream calls, OAuth client secrets, DCR registration access tokens, RSA private keys for the per-tenant inbound AS).
2. **Configuration upgrades**: environment-variable substitution, plus the new top-level sections (`database`, `security`, `oauth_server`) that the rest of the work depends on.

These two are bundled because they're both small, both blocking, and the encryption key is itself a config value.

## Design

### `internal/crypto/aesgcm.go`

- **Algorithm**: AES-256-GCM. Random 12-byte nonce per encryption, prepended to ciphertext.
- **Key source**: 32 raw bytes loaded from config (`${LIMEN_TOKEN_ENCRYPTION_KEY}`), accepted as base64 or hex. Fail-fast at startup if the key is missing or the wrong length.
- **AAD binding**: Additional Authenticated Data is set to `tenant_id|user_id|kind` (UTF-8, pipe-separated). `kind` is a short string identifying the field (`upstream.access_token`, `upstream.refresh_token`, `dcr.client_secret`, `dcr.registration_access_token`, `signing_key.private_pem`, `portal.session.token`, …). This makes a ciphertext stolen from one column unusable in another column or for another tenant/user.
- **API**:

  ```go
  type Key [32]byte
  type Cipher struct { /* ... */ }

  func NewCipher(key Key) *Cipher
  func (c *Cipher) Encrypt(plaintext []byte, aad AAD) ([]byte, error)
  func (c *Cipher) Decrypt(ciphertext []byte, aad AAD) ([]byte, error)

  type AAD struct {
      TenantID string // required
      UserID   string // "" allowed
      Kind     string // required
  }
  ```

- **GORM glue**: `SecretField` (stubbed in Phase 1) becomes a real type that holds the encrypted bytes + delegates encrypt/decrypt to the configured `*Cipher` via a package-level setter `crypto.SetCipher(c)` called once at startup. Encryption happens in `Value()`, decryption in `Scan()`. The AAD has to be set on the field before `Save` and on the parent struct's loader before `Find` — the cleanest pattern is small helpers like `link.AccessToken.SetAAD(tenant, user, "upstream.access_token")` invoked next to every read/write site. This is verbose but keeps AAD binding explicit and auditable.
- **Key rotation** (forward-compat): the ciphertext format is `version(1) | nonce(12) | ciphertext+tag(N)`. Version `0x01` = primary key only. Future rotation can introduce `0x02` with multi-key decrypt.

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
└── aesgcm.go        // Cipher, AAD, SecretField, SetCipher

internal/config/
└── config.go        // existing file gets expansion + new sections
```

## Deliverables

- New files: `internal/crypto/aesgcm.go`
- Modified files: `internal/config/config.go` (env substitution, new sections, validation)
- Updated `config.yaml` example with the new sections.

## Security & operational notes

- The encryption key is a high-value secret. Document storing it in a secrets manager (Vault, AWS Secrets Manager, Kubernetes secret); never in `config.yaml` literally.
- AAD is mandatory — `Encrypt`/`Decrypt` must reject empty `Kind`. Catching this at compile-time isn't worth the API gymnastics, but a runtime check + test is cheap.
- AES-GCM nonce reuse is catastrophic — keep using `crypto/rand` and never derive nonces deterministically.
- Logs must never contain `Cipher.Decrypt` results or AAD components that include user identifiers — wrap with `zap.String("kind", ...)` only.

## Verification

- Unit tests:
  - Roundtrip with correct AAD.
  - Tamper bit in ciphertext → `Decrypt` returns error.
  - AAD mismatch (different tenant, different user, different kind) → `Decrypt` returns error.
  - Empty kind → `Encrypt` returns error.
  - Version byte mismatch → `Decrypt` returns error.
- Config tests:
  - `${VAR}` substitution with present and missing variables.
  - `${VAR:-default}` fallback.
  - Validation errors for short keys, bad driver, non-absolute `base_url`, zero TTLs.

## Risks

- **GORM custom-type ergonomics**: `Value`/`Scan` don't have ctx, so AAD has to be threaded via the field itself or a thread-local. Plan to thread it via the field (`SetAAD`). Document this clearly.
- **Migration of existing data**: not applicable yet — no production data exists.

## Checklist

- [ ] `internal/crypto/aesgcm.go` exports `Key`, `Cipher`, `AAD`, `SecretField`, `SetCipher`
- [ ] AES-256-GCM, random 12-byte nonce, version-byte prefix `0x01`
- [ ] AAD constructed from `tenant|user|kind`; empty `kind` rejected
- [ ] `SecretField.Scan`/`Value` integrated with GORM and exercised by a unit test
- [ ] `${ENV}` and `${ENV:-default}` substitution in `internal/config/config.go`
- [ ] `DatabaseConfig`, `SecurityConfig`, `OAuthServerConfig` structs added with `Validate()` methods
- [ ] Top-level `Config.Validate()` runs all section validations and reports the first failure
- [ ] `config.yaml` example file updated with the new sections (commented placeholders, no real secrets)
- [ ] Unit tests for crypto: roundtrip, tamper detection, AAD mismatch, version mismatch
- [ ] Unit tests for config: env substitution, defaults, validation failures
- [ ] `go vet ./...` and `go build ./...` clean

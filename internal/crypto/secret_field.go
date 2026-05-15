// Package crypto holds the secret-at-rest primitives used by storage models.
//
// Phase 2 wires SecretField through AES-256-GCM with AAD-bound encryption.
// When a process-wide Cipher is registered via SetCipher, every Save call
// transparently encrypts the field using the AAD set via SetAAD beforehand.
// Reads are lazy: Scan stores the raw ciphertext, and the caller must call
// Decrypt(tenant, user, kind) on the loaded field before reading plaintext.
// This shape is required by GORM v2, which scans into a sync.Pool-allocated
// SecretField (not the destination struct's field), so any AAD pre-set on
// the destination is invisible to the Scan code path.
//
// When no Cipher is registered the field falls back to plaintext passthrough
// in both directions — convenient for storage-layer tests that do not need
// to exercise the crypto stack.
package crypto

import (
	"database/sql/driver"
	"errors"
	"fmt"
)

// SecretField is a byte slice that round-trips through database/sql as a
// bytea column.
//
// Write path: callers must call SetAAD before Save so Value can encrypt.
//
// Read path: GORM scans the raw bytes into a pool-allocated SecretField and
// then copies it into the destination struct. After loading a row, callers
// must call Decrypt(tenantID, userID, kind) on each SecretField before
// reading its plaintext via Bytes/String. Until Decrypt runs, the field
// holds only ciphertext and Bytes/String report empty.
type SecretField struct {
	plaintext []byte
	cipher    []byte
	aad       AAD
}

// NewSecret returns a SecretField wrapping the given plaintext.
func NewSecret(b []byte) SecretField { return SecretField{plaintext: b} }

// Bytes returns the in-memory plaintext. Returns nil for an absent value or
// for a field that was loaded from the DB but has not yet been Decrypt-ed.
func (s SecretField) Bytes() []byte { return s.plaintext }

// String renders the plaintext as a string. Only safe for fields that are
// known to be UTF-8 and that have already been Decrypt-ed when loaded.
func (s SecretField) String() string { return string(s.plaintext) }

// IsZero reports whether the field carries no plaintext and no ciphertext.
func (s SecretField) IsZero() bool { return len(s.plaintext) == 0 && len(s.cipher) == 0 }

// Set replaces the in-memory plaintext.
func (s *SecretField) Set(b []byte) {
	s.plaintext = b
	s.cipher = nil
}

// SetAAD binds the field to a tenant/user/kind triple. Must be called before
// the field is saved when a Cipher is active. Read-path callers should use
// Decrypt instead — SetAAD does nothing for the lazy-decrypt flow.
func (s *SecretField) SetAAD(tenantID, userID, kind string) {
	s.aad = AAD{TenantID: tenantID, UserID: userID, Kind: kind}
}

// AAD returns the AAD currently bound to the field.
func (s SecretField) AAD() AAD { return s.aad }

// Scan implements sql.Scanner. The raw column bytes are stashed verbatim;
// when a Cipher is active the caller must follow up with Decrypt to obtain
// plaintext. When no Cipher is registered the bytes are treated as
// plaintext directly (the Phase 1 passthrough mode).
func (s *SecretField) Scan(src any) error {
	s.plaintext = nil
	s.cipher = nil
	if src == nil {
		return nil
	}
	var raw []byte
	switch v := src.(type) {
	case []byte:
		raw = make([]byte, len(v))
		copy(raw, v)
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("crypto: SecretField.Scan unsupported type %T", src)
	}
	if ActiveCipher() == nil {
		s.plaintext = raw
		return nil
	}
	s.cipher = raw
	return nil
}

// Decrypt resolves a scanned SecretField to its plaintext under the given
// AAD. It is a no-op when the field is already plaintext (no-cipher mode or
// an in-memory NewSecret value) or when the field is empty. After a
// successful call, Bytes/String return the decrypted value.
func (s *SecretField) Decrypt(tenantID, userID, kind string) error {
	if len(s.cipher) == 0 {
		// Either already decrypted, empty, or no-cipher passthrough.
		s.aad = AAD{TenantID: tenantID, UserID: userID, Kind: kind}
		return nil
	}
	c := ActiveCipher()
	if c == nil {
		// Cipher was unset between Scan and Decrypt — treat ciphertext as
		// plaintext to preserve passthrough behavior.
		s.plaintext = s.cipher
		s.cipher = nil
		return nil
	}
	aad := AAD{TenantID: tenantID, UserID: userID, Kind: kind}
	plain, err := c.Decrypt(s.cipher, aad)
	if err != nil {
		return err
	}
	s.plaintext = plain
	s.cipher = nil
	s.aad = aad
	return nil
}

// Value implements driver.Valuer. When a Cipher is active the plaintext is
// encrypted under the field's AAD before being handed to the driver.
func (s SecretField) Value() (driver.Value, error) {
	if s.plaintext == nil {
		return nil, nil
	}
	c := ActiveCipher()
	if c == nil {
		return append([]byte(nil), s.plaintext...), nil
	}
	if s.aad.IsZero() {
		return nil, errors.New("crypto: SecretField.Value called without SetAAD while Cipher is active")
	}
	return c.Encrypt(s.plaintext, s.aad)
}

// GormDataType reports the GORM data type for this field.
func (SecretField) GormDataType() string { return "bytea" }

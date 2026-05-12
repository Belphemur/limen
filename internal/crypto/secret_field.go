// Package crypto holds the secret-at-rest primitives used by storage models.
//
// Phase 2 wires SecretField through AES-256-GCM with AAD-bound encryption.
// When a process-wide Cipher is registered via SetCipher, every Save/Find
// roundtrip transparently encrypts/decrypts using the per-field AAD set via
// SecretField.SetAAD. When no Cipher is registered the field falls back to
// plaintext passthrough — convenient for storage-layer tests that do not
// need to exercise the crypto stack.
package crypto

import (
	"database/sql/driver"
	"errors"
	"fmt"
)

// SecretField is a byte slice that round-trips through database/sql as a
// bytea column. Callers must call SetAAD before Save (so Value can encrypt)
// and before Find (so Scan can decrypt) when a Cipher is active.
type SecretField struct {
	plaintext []byte
	aad       AAD
}

// NewSecret returns a SecretField wrapping the given plaintext.
func NewSecret(b []byte) SecretField { return SecretField{plaintext: b} }

// Bytes returns the in-memory plaintext. Returns nil for an absent value.
func (s SecretField) Bytes() []byte { return s.plaintext }

// String renders the plaintext as a string. Only safe for fields that are
// known to be UTF-8.
func (s SecretField) String() string { return string(s.plaintext) }

// IsZero reports whether the field carries no plaintext.
func (s SecretField) IsZero() bool { return len(s.plaintext) == 0 }

// Set replaces the in-memory plaintext.
func (s *SecretField) Set(b []byte) { s.plaintext = b }

// SetAAD binds the field to a tenant/user/kind triple. Must be called before
// the field is saved or scanned when a Cipher is active.
func (s *SecretField) SetAAD(tenantID, userID, kind string) {
	s.aad = AAD{TenantID: tenantID, UserID: userID, Kind: kind}
}

// AAD returns the AAD currently bound to the field.
func (s SecretField) AAD() AAD { return s.aad }

// Scan implements sql.Scanner. When a Cipher is active the source bytes are
// decrypted under the field's AAD. Without a Cipher the bytes are stored
// verbatim — the Phase 1 plaintext mode that storage tests still rely on.
func (s *SecretField) Scan(src any) error {
	if src == nil {
		s.plaintext = nil
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

	c := ActiveCipher()
	if c == nil {
		s.plaintext = raw
		return nil
	}
	if s.aad.IsZero() {
		return errors.New("crypto: SecretField.Scan called without SetAAD while Cipher is active")
	}
	plain, err := c.Decrypt(raw, s.aad)
	if err != nil {
		return err
	}
	s.plaintext = plain
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

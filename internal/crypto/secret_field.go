// Package crypto holds the secret-at-rest primitives used by storage models.
//
// Phase 1 stubs SecretField as a transparent []byte alias so migrations and
// model wiring can land first. Phase 2 replaces the Scan/Value implementations
// with AES-GCM authenticated encryption — the type identity does not change,
// so callers and DDL stay stable.
package crypto

import (
	"database/sql/driver"
	"fmt"
)

// SecretField is a byte slice that round-trips through database/sql. In
// Phase 2 this becomes an envelope-encrypted ciphertext; for now it stores
// plaintext bytes so the schema and call sites can be exercised end-to-end.
type SecretField []byte

// Scan implements sql.Scanner.
func (s *SecretField) Scan(src any) error {
	if src == nil {
		*s = nil
		return nil
	}
	switch v := src.(type) {
	case []byte:
		// Copy: database/sql may reuse the underlying buffer between rows.
		b := make([]byte, len(v))
		copy(b, v)
		*s = b
	case string:
		*s = []byte(v)
	default:
		return fmt.Errorf("crypto: SecretField.Scan unsupported type %T", src)
	}
	return nil
}

// Value implements driver.Valuer.
func (s SecretField) Value() (driver.Value, error) {
	if s == nil {
		return nil, nil
	}
	return []byte(s), nil
}

// GormDataType reports the GORM data type for this field.
func (SecretField) GormDataType() string { return "bytea" }

// Package crypto holds the secret-at-rest primitives used by storage models.
//
// Symmetric encryption is provided by AES-SIV (RFC 5297) via
// github.com/jedisct1/go-aes-siv. AES-SIV is a nonce-misuse-resistant AEAD
// — even if a nonce is reused, the only information leaked is whether two
// plaintexts under the same key+AAD are identical; confidentiality of the
// underlying data is preserved. We still generate a fresh random nonce per
// call so the stored ciphertext is randomized, but the misuse-resistance
// property removes a whole class of footguns that plain AES-GCM has.
//
// SecretField is the GORM/`database/sql` glue (see secret_field.go). The
// public surface here is:
//
//	type Key                          // 32-byte AES-128-SIV key
//	func ParseKey(string) (Key, err)  // base64 or hex
//	type AAD                          // tenant|user|kind binding
//	type Cipher                       // process-wide AEAD primitive
//	func NewCipher(Key) (*Cipher, err)
//	func SetCipher(*Cipher) *Cipher   // register globally; returns previous
//	func ActiveCipher() *Cipher
package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"

	aessiv "github.com/jedisct1/go-aes-siv"
)

const (
	// versionV1 prefixes every ciphertext. Future key-rotation work can
	// introduce versionV2 with a key-id byte without changing call sites.
	versionV1 byte = 0x01

	// nonceSize is the random nonce appended to each ciphertext. AES-SIV
	// accepts arbitrary-length nonces (passed as associated data
	// internally); 16 bytes matches the cipher's block size.
	nonceSize = 16

	// keySize is the SIV key length we accept. 32 bytes selects
	// AES-128-SIV — internally split into a 16-byte CMAC key and a
	// 16-byte CTR key.
	keySize = aessiv.KeySize256
)

// Key is the master encryption key.
type Key [keySize]byte

// ParseKey decodes a key from a base64 (standard or URL, padded or
// unpadded) or 64-character hex string and validates the length.
// Whitespace is trimmed. An empty input is rejected.
func ParseKey(s string) (Key, error) {
	var k Key
	s = strings.TrimSpace(s)
	if s == "" {
		return k, errors.New("crypto: encryption key is empty")
	}

	var (
		raw []byte
		err error
	)
	switch {
	case looksLikeHex(s):
		raw, err = hex.DecodeString(s)
	default:
		raw, err = decodeBase64(s)
	}
	if err != nil {
		return k, fmt.Errorf("crypto: decode encryption key: %w", err)
	}
	if len(raw) != keySize {
		return k, fmt.Errorf("crypto: encryption key must decode to %d bytes, got %d", keySize, len(raw))
	}
	copy(k[:], raw)
	return k, nil
}

func looksLikeHex(s string) bool {
	if len(s) != keySize*2 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

func decodeBase64(s string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawURLEncoding.DecodeString(s)
}

// AAD is the Additional Authenticated Data bound to every ciphertext. It
// pins a value to a tenant, optionally a user, and a logical field kind so
// that a ciphertext copied between rows, columns, or tenants fails to
// decrypt.
type AAD struct {
	TenantID string // required
	UserID   string // "" allowed (tenant-scoped, not user-scoped)
	Kind     string // required, e.g. "upstream.access_token"
}

// IsZero reports whether the AAD has not been initialised.
func (a AAD) IsZero() bool {
	return a.TenantID == "" && a.UserID == "" && a.Kind == ""
}

func (a AAD) bytes() ([]byte, error) {
	if a.TenantID == "" {
		return nil, errors.New("crypto: AAD.TenantID is required")
	}
	if a.Kind == "" {
		return nil, errors.New("crypto: AAD.Kind is required")
	}
	if strings.ContainsRune(a.TenantID, '|') || strings.ContainsRune(a.UserID, '|') || strings.ContainsRune(a.Kind, '|') {
		return nil, errors.New("crypto: AAD components must not contain '|'")
	}
	return []byte(a.TenantID + "|" + a.UserID + "|" + a.Kind), nil
}

// Cipher is the configured AEAD primitive. Construct once at startup and
// register it via SetCipher so SecretField can find it.
type Cipher struct {
	aead *aessiv.AESSIV
}

// NewCipher returns a Cipher backed by AES-SIV with the given key.
func NewCipher(key Key) (*Cipher, error) {
	aead, err := aessiv.New(key[:])
	if err != nil {
		return nil, fmt.Errorf("crypto: aessiv.New: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt seals plaintext under the given AAD. The output layout is
//
//	versionV1 | nonce(16) | tag(16) | ciphertext(N)
//
// The nonce is freshly randomized per call. AES-SIV remains secure even if
// a nonce repeats, but a fresh nonce keeps stored ciphertexts randomized.
func (c *Cipher) Encrypt(plaintext []byte, aad AAD) ([]byte, error) {
	aadBytes, err := aad.bytes()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("crypto: read nonce: %w", err)
	}
	// SealWithAssociatedDataList per RFC 5297 — the nonce is the final AD
	// element by convention.
	sealed := c.aead.SealWithAssociatedDataList(nil, [][]byte{aadBytes, nonce}, plaintext)

	out := make([]byte, 0, 1+nonceSize+len(sealed))
	out = append(out, versionV1)
	out = append(out, nonce...)
	out = append(out, sealed...)
	return out, nil
}

// Decrypt verifies and opens a ciphertext produced by Encrypt.
func (c *Cipher) Decrypt(ciphertext []byte, aad AAD) ([]byte, error) {
	if len(ciphertext) < 1+nonceSize+aessiv.TagSize {
		return nil, errors.New("crypto: ciphertext too short")
	}
	if ciphertext[0] != versionV1 {
		return nil, fmt.Errorf("crypto: unsupported ciphertext version 0x%02x", ciphertext[0])
	}
	aadBytes, err := aad.bytes()
	if err != nil {
		return nil, err
	}
	nonce := ciphertext[1 : 1+nonceSize]
	body := ciphertext[1+nonceSize:]
	plain, err := c.aead.OpenWithAssociatedDataList(nil, [][]byte{aadBytes, nonce}, body)
	if err != nil {
		return nil, fmt.Errorf("crypto: decrypt: %w", err)
	}
	return plain, nil
}

// activeCipher holds the process-wide Cipher. Storage models reach for it
// through SecretField. Tests may swap it in/out via SetCipher.
var activeCipher atomic.Pointer[Cipher]

// SetCipher registers the process-wide Cipher. Pass nil to clear (mainly
// useful in tests). The previous Cipher, if any, is returned.
func SetCipher(c *Cipher) *Cipher {
	return activeCipher.Swap(c)
}

// ActiveCipher returns the registered Cipher, or nil if none has been set.
func ActiveCipher() *Cipher { return activeCipher.Load() }

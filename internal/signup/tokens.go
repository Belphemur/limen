package signup

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// verifyTokenLen is the raw token length in bytes. 32 bytes = 256
// bits of entropy, well above the threshold for a single-use URL
// token. The user-facing form is URL-safe base64 without padding,
// which yields 43 ASCII characters.
const verifyTokenLen = 32

// mintVerifyToken produces (urlSafeToken, hmacSha256Hash). The raw
// bytes are URL-base64-encoded for the email link; the HMAC keyed by
// hmacKey is stored in the database. We hash rather than store the
// plaintext so a database leak alone cannot validate any pending
// link.
func mintVerifyToken(hmacKey []byte) (string, []byte, error) {
	if len(hmacKey) == 0 {
		return "", nil, fmt.Errorf("signup: token hmac key is empty")
	}
	buf := make([]byte, verifyTokenLen)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("signup: read random: %w", err)
	}
	enc := base64.RawURLEncoding.EncodeToString(buf)
	return enc, hashVerifyToken(hmacKey, enc), nil
}

// hashVerifyToken computes the HMAC-SHA256 over the URL-encoded
// token. The encoded form is what the user pastes back into the
// SPA, so hashing that form (not the raw bytes) is what makes the
// VerifyEmail lookup table-correct.
func hashVerifyToken(hmacKey []byte, token string) []byte {
	m := hmac.New(sha256.New, hmacKey)
	_, _ = m.Write([]byte(token))
	return m.Sum(nil)
}

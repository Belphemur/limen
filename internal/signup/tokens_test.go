package signup

import (
	"bytes"
	"testing"
)

func TestMintVerifyToken_ProducesUrlSafeStringAndStableHash(t *testing.T) {
	key := []byte("test-hmac-key-32-bytes-long-xxxxx")
	plain, hash, err := mintVerifyToken(key)
	if err != nil {
		t.Fatalf("mintVerifyToken: %v", err)
	}
	if len(plain) == 0 {
		t.Fatal("plaintext is empty")
	}
	for _, r := range plain {
		// RawURLEncoding emits only A-Z a-z 0-9 - _
		ok := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
		if !ok {
			t.Fatalf("plaintext contains non-URL-safe rune %q", r)
		}
	}
	if got := hashVerifyToken(key, plain); !bytes.Equal(got, hash) {
		t.Fatalf("hash is not stable: %x vs %x", got, hash)
	}
}

func TestHashVerifyToken_DiffersByKey(t *testing.T) {
	a := hashVerifyToken([]byte("k1"), "token")
	b := hashVerifyToken([]byte("k2"), "token")
	if bytes.Equal(a, b) {
		t.Fatal("hash must vary with key")
	}
}

func TestMintVerifyToken_RejectsEmptyKey(t *testing.T) {
	if _, _, err := mintVerifyToken(nil); err == nil {
		t.Fatal("want error on empty key")
	}
}

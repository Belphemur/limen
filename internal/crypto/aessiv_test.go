package crypto

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

func randomKey(t *testing.T) Key {
	t.Helper()
	var raw [keySize]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return raw
}

func newTestCipher(t *testing.T) *Cipher {
	t.Helper()
	c, err := NewCipher(randomKey(t))
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	return c
}

func TestParseKey_AcceptsHexAndBase64(t *testing.T) {
	var raw [keySize]byte
	for i := range raw {
		raw[i] = byte(i)
	}

	cases := map[string]string{
		"hex":         hex.EncodeToString(raw[:]),
		"hex-upper":   strings.ToUpper(hex.EncodeToString(raw[:])),
		"base64-std":  base64.StdEncoding.EncodeToString(raw[:]),
		"base64-raw":  base64.RawStdEncoding.EncodeToString(raw[:]),
		"base64-url":  base64.URLEncoding.EncodeToString(raw[:]),
		"base64-rawu": base64.RawURLEncoding.EncodeToString(raw[:]),
		"with-spaces": "  " + hex.EncodeToString(raw[:]) + "\n",
	}

	for name, enc := range cases {
		t.Run(name, func(t *testing.T) {
			k, err := ParseKey(enc)
			if err != nil {
				t.Fatalf("ParseKey(%q): %v", enc, err)
			}
			if !bytes.Equal(k[:], raw[:]) {
				t.Fatalf("ParseKey returned wrong bytes")
			}
		})
	}
}

func TestParseKey_RejectsBadInputs(t *testing.T) {
	cases := []string{
		"",
		"   ",
		"not-a-valid-key",
		hex.EncodeToString(make([]byte, 16)), // too short
		base64.StdEncoding.EncodeToString(make([]byte, 16)),
	}
	for _, in := range cases {
		if _, err := ParseKey(in); err == nil {
			t.Fatalf("ParseKey(%q) returned no error", in)
		}
	}
}

func TestCipher_Roundtrip(t *testing.T) {
	c := newTestCipher(t)
	aad := AAD{TenantID: "tnt_1", UserID: "usr_2", Kind: "upstream.access_token"}

	plain := []byte("hello secret world")
	ct, err := c.Encrypt(plain, aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Contains(ct, plain) {
		t.Fatalf("ciphertext contains plaintext")
	}
	if ct[0] != versionV1 {
		t.Fatalf("missing version byte")
	}

	got, err := c.Decrypt(ct, aad)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("roundtrip mismatch: got %q want %q", got, plain)
	}
}

func TestCipher_FreshNoncePerEncrypt(t *testing.T) {
	c := newTestCipher(t)
	aad := AAD{TenantID: "tnt_1", Kind: "k"}
	a, err := c.Encrypt([]byte("same"), aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	b, err := c.Encrypt([]byte("same"), aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Fatalf("two encryptions produced identical ciphertext — nonce reuse?")
	}
}

func TestCipher_TamperDetected(t *testing.T) {
	c := newTestCipher(t)
	aad := AAD{TenantID: "tnt_1", Kind: "k"}
	ct, err := c.Encrypt([]byte("data"), aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	// flip one bit in the ciphertext body (after version+nonce).
	ct[len(ct)-1] ^= 0x01
	if _, err := c.Decrypt(ct, aad); err == nil {
		t.Fatalf("Decrypt accepted tampered ciphertext")
	}
}

func TestCipher_AADMismatchRejected(t *testing.T) {
	c := newTestCipher(t)
	good := AAD{TenantID: "tnt_1", UserID: "usr_2", Kind: "k"}
	ct, err := c.Encrypt([]byte("data"), good)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	mutations := []AAD{
		{TenantID: "tnt_OTHER", UserID: "usr_2", Kind: "k"},
		{TenantID: "tnt_1", UserID: "usr_OTHER", Kind: "k"},
		{TenantID: "tnt_1", UserID: "usr_2", Kind: "different"},
	}
	for _, bad := range mutations {
		if _, err := c.Decrypt(ct, bad); err == nil {
			t.Fatalf("Decrypt accepted ciphertext under wrong AAD %+v", bad)
		}
	}
}

func TestAAD_RequiredFields(t *testing.T) {
	c := newTestCipher(t)
	cases := []AAD{
		{Kind: "k"},                                   // missing tenant
		{TenantID: "tnt"},                             // missing kind
		{TenantID: "tnt|with-pipe", Kind: "k"},        // forbidden char
		{TenantID: "tnt", UserID: "usr|x", Kind: "k"}, // forbidden char
		{TenantID: "tnt", Kind: "k|x"},                // forbidden char
	}
	for _, a := range cases {
		if _, err := c.Encrypt([]byte("x"), a); err == nil {
			t.Fatalf("Encrypt accepted invalid AAD %+v", a)
		}
	}
}

func TestCipher_VersionMismatchRejected(t *testing.T) {
	c := newTestCipher(t)
	aad := AAD{TenantID: "tnt", Kind: "k"}
	ct, err := c.Encrypt([]byte("data"), aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	ct[0] = 0x02
	if _, err := c.Decrypt(ct, aad); err == nil {
		t.Fatalf("Decrypt accepted unknown version byte")
	}
}

func TestCipher_DecryptShortCiphertext(t *testing.T) {
	c := newTestCipher(t)
	aad := AAD{TenantID: "tnt", Kind: "k"}
	if _, err := c.Decrypt([]byte{0x01, 0x02}, aad); err == nil {
		t.Fatalf("Decrypt accepted too-short input")
	}
}

func TestSecretField_PlaintextModeWhenNoCipher(t *testing.T) {
	defer SetCipher(SetCipher(nil)) // restore whatever was set

	var f SecretField
	f.Set([]byte("hello"))
	v, err := f.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if !bytes.Equal(v.([]byte), []byte("hello")) {
		t.Fatalf("plaintext mode: Value did not return plaintext")
	}

	var dst SecretField
	if err := dst.Scan([]byte("hello")); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !bytes.Equal(dst.Bytes(), []byte("hello")) {
		t.Fatalf("plaintext mode: Scan did not return plaintext")
	}
}

func TestSecretField_EncryptedRoundtripWithCipher(t *testing.T) {
	prev := SetCipher(newTestCipher(t))
	defer SetCipher(prev)

	var src SecretField
	src.Set([]byte("super-secret"))
	src.SetAAD("tnt_1", "usr_2", "upstream.access_token")

	encoded, err := src.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	enc := encoded.([]byte)
	if bytes.Contains(enc, []byte("super-secret")) {
		t.Fatalf("encoded payload leaks plaintext")
	}

	var dst SecretField
	dst.SetAAD("tnt_1", "usr_2", "upstream.access_token")
	if err := dst.Scan(enc); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got := dst.String(); got != "super-secret" {
		t.Fatalf("roundtrip mismatch: %q", got)
	}
}

func TestSecretField_MissingAADWithCipher(t *testing.T) {
	prev := SetCipher(newTestCipher(t))
	defer SetCipher(prev)

	var f SecretField
	f.Set([]byte("x"))
	if _, err := f.Value(); err == nil {
		t.Fatalf("Value succeeded without AAD")
	}

	var g SecretField
	if err := g.Scan([]byte("anything")); err == nil {
		t.Fatalf("Scan succeeded without AAD")
	}
}

func TestSecretField_NilRoundtrip(t *testing.T) {
	prev := SetCipher(newTestCipher(t))
	defer SetCipher(prev)

	var f SecretField
	v, err := f.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if v != nil {
		t.Fatalf("Value of empty field should be nil, got %T", v)
	}

	var g SecretField
	if err := g.Scan(nil); err != nil {
		t.Fatalf("Scan(nil): %v", err)
	}
	if g.Bytes() != nil {
		t.Fatalf("Scan(nil) should leave plaintext nil")
	}
}

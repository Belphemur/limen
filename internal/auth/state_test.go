package auth

import (
	"strings"
	"testing"
	"time"
)

var testMaster = []byte("0123456789abcdef-test-master-key")

func mustSigner(t *testing.T, master []byte) *StateSigner {
	t.Helper()
	s, err := NewStateSigner(master)
	if err != nil {
		t.Fatalf("NewStateSigner: %v", err)
	}
	return s
}

func TestNewStateSigner_RejectsShortKey(t *testing.T) {
	tests := []struct {
		name    string
		master  []byte
		wantErr bool
	}{
		{"empty", []byte{}, true},
		{"too short", make([]byte, 15), true},
		{"min 16", make([]byte, 16), false},
		{"32", testMaster, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewStateSigner(tt.master)
			if tt.wantErr && err == nil {
				t.Errorf("want error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("want nil, got %v", err)
			}
		})
	}
}

func TestStateSigner_Roundtrip(t *testing.T) {
	s := mustSigner(t, testMaster)

	st, err := NewState("acme", "/t/acme/portal/")
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	token, err := s.Sign(st)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !strings.Contains(token, ".") {
		t.Fatalf("expected body.sig form, got %q", token)
	}

	got, err := s.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.Slug != "acme" || got.ReturnTo != "/t/acme/portal/" {
		t.Errorf("payload mismatch: %+v", got)
	}
	if got.Nonce != st.Nonce {
		t.Errorf("nonce drift: want %q got %q", st.Nonce, got.Nonce)
	}
}

func TestStateSigner_RejectsTamperedSignature(t *testing.T) {
	s := mustSigner(t, testMaster)
	st, _ := NewState("acme", "/")
	tok, _ := s.Sign(st)

	parts := strings.SplitN(tok, ".", 2)
	flipped := flipByte(parts[1])
	tampered := parts[0] + "." + flipped

	if _, err := s.Verify(tampered); err == nil {
		t.Fatal("expected verify error on tampered signature")
	}
}

func TestStateSigner_RejectsTamperedPayload(t *testing.T) {
	s := mustSigner(t, testMaster)
	st, _ := NewState("acme", "/")
	tok, _ := s.Sign(st)

	parts := strings.SplitN(tok, ".", 2)
	flipped := flipByte(parts[0])
	tampered := flipped + "." + parts[1]

	if _, err := s.Verify(tampered); err == nil {
		t.Fatal("expected verify error on tampered payload")
	}
}

func TestStateSigner_RejectsMalformed(t *testing.T) {
	s := mustSigner(t, testMaster)
	cases := []string{"", "no-dot", ".only-sig", "only-body."}
	for _, c := range cases {
		if _, err := s.Verify(c); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}

func TestStateSigner_RejectsExpired(t *testing.T) {
	s := mustSigner(t, testMaster)
	st, _ := NewState("acme", "/")
	st.ExpiresAt = time.Now().UTC().Add(-time.Second)
	tok, err := s.Sign(st)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := s.Verify(tok); err == nil {
		t.Fatal("expected expired-state error")
	}
}

func TestStateSigner_DifferentMasterKeysRejectEachOther(t *testing.T) {
	a := mustSigner(t, []byte("aaaaaaaaaaaaaaaa-key-a-1234567890"))
	b := mustSigner(t, []byte("bbbbbbbbbbbbbbbb-key-b-1234567890"))

	st, _ := NewState("acme", "/")
	tok, _ := a.Sign(st)
	if _, err := b.Verify(tok); err == nil {
		t.Fatal("expected cross-key verify to fail")
	}
}

func TestNewState_NoncesUnique(t *testing.T) {
	a, err := NewState("acme", "/")
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	b, err := NewState("acme", "/")
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	if a.Nonce == b.Nonce {
		t.Errorf("nonces collided: %q", a.Nonce)
	}
	if a.Nonce == "" {
		t.Errorf("empty nonce")
	}
}

func TestNewState_SetsTTLInFuture(t *testing.T) {
	st, err := NewState("acme", "/")
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	if !st.ExpiresAt.After(time.Now().UTC().Add(stateTTL - time.Minute)) {
		t.Errorf("ExpiresAt too soon: %v", st.ExpiresAt)
	}
	if !st.ExpiresAt.Before(time.Now().UTC().Add(stateTTL + time.Minute)) {
		t.Errorf("ExpiresAt too far: %v", st.ExpiresAt)
	}
}

// flipByte mutates a single character in a base64-urlsafe string so the
// result still decodes but the bytes differ from the original.
func flipByte(s string) string {
	if s == "" {
		return "A"
	}
	b := []byte(s)
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	for i := 0; i < len(b); i++ {
		for _, c := range []byte(alphabet) {
			if c != b[i] {
				b[i] = c
				return string(b)
			}
		}
	}
	return s
}

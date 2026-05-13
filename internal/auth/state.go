// Package auth holds the portal-facing authentication: OIDC relying party,
// session cookie, state cookie, and the middlewares that gate Limen's
// tenant-scoped routes.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// stateTTL bounds the lifetime of the OIDC state cookie. Long enough to
// cover a normal redirect-and-return, short enough that a leaked cookie
// becomes worthless quickly.
const stateTTL = 10 * time.Minute

// stateDomainTag is the domain-separation label fed into HKDF so the state
// MAC cannot be confused with any other MAC keyed off the same master key.
const stateDomainTag = "oidc.state"

// State is the payload signed into the OIDC state cookie. The nonce makes
// each state unique even for the same (slug, return_to) pair and lets us
// detect replays if we later add a one-shot store.
type State struct {
	Nonce     string    `json:"n"`
	Slug      string    `json:"s"`
	ReturnTo  string    `json:"r"`
	ExpiresAt time.Time `json:"e"`
}

// StateSigner produces and verifies HMAC-SHA256 signed state strings using
// a key derived from the process-wide encryption key plus a domain tag.
type StateSigner struct {
	key []byte
}

// NewStateSigner derives a per-purpose key from master via HKDF-like
// HMAC(master, tag); master must be at least 16 bytes.
func NewStateSigner(master []byte) (*StateSigner, error) {
	if len(master) < 16 {
		return nil, errors.New("auth: state signer key too short")
	}
	mac := hmac.New(sha256.New, master)
	_, _ = mac.Write([]byte(stateDomainTag))
	return &StateSigner{key: mac.Sum(nil)}, nil
}

// NewState builds a State with a fresh 16-byte random nonce and the
// configured TTL.
func NewState(slug, returnTo string) (State, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return State{}, fmt.Errorf("auth: state nonce: %w", err)
	}
	return State{
		Nonce:     base64.RawURLEncoding.EncodeToString(b[:]),
		Slug:      slug,
		ReturnTo:  returnTo,
		ExpiresAt: time.Now().UTC().Add(stateTTL),
	}, nil
}

// Sign serializes State to JSON and returns "<b64(json)>.<b64(mac)>".
func (s *StateSigner) Sign(st State) (string, error) {
	payload, err := json.Marshal(st)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte(body))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return body + "." + sig, nil
}

// Verify parses, MAC-checks, and TTL-checks a string produced by Sign.
func (s *StateSigner) Verify(token string) (State, error) {
	var zero State
	before, after, ok := strings.Cut(token, ".")
	if !ok {
		return zero, errors.New("auth: malformed state")
	}
	body, sig := before, after
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte(body))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return zero, errors.New("auth: state signature decode")
	}
	if !hmac.Equal(want, got) {
		return zero, errors.New("auth: state signature mismatch")
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return zero, errors.New("auth: state payload decode")
	}
	var st State
	if err := json.Unmarshal(payload, &st); err != nil {
		return zero, errors.New("auth: state payload parse")
	}
	if time.Now().UTC().After(st.ExpiresAt) {
		return zero, errors.New("auth: state expired")
	}
	return st, nil
}

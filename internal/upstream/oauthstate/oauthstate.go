// Package oauthstate stores Phase 7's one-shot OAuth state envelopes in
// Valkey. The state value (the random string echoed by the AS in the
// authorize / callback exchange) doubles as the Valkey key; consumption is
// atomic via GETDEL so a replay of the same state finds nothing.
//
// The payload (tenant, user, upstream, return_to, nonce, PKCE verifier) is
// AES-SIV encrypted under AAD tenant|user|"upstream.oauth.state" so even
// an attacker with raw Valkey access can't read or forge state envelopes.
package oauthstate

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/valkey"
)

// DefaultTTL is the lifetime of a stored state envelope. Long enough for a
// normal authorize round-trip; short enough that leaked rows expire fast.
const DefaultTTL = 10 * time.Minute

// keyPrefix namespaces Limen's OAuth state under the shared "limen:*"
// keyspace so the same Valkey can host other Limen short-lived data.
const keyPrefix = "limen:upstream:oauth_state:"

// kindAAD is the SecretField AAD "kind" component used to bind the
// ciphertext to "upstream.oauth.state". Tenant + user provide the other
// two AAD components.
const kindAAD = "upstream.oauth.state"

// Envelope is the per-attempt OAuth state payload. The tenant/user IDs are
// the int64 PKs; the upstream id is the int64 PK on upstreams. PKCEVerifier
// is the plain string (S256 will hash it before sending). ReturnTo is the
// SPA path to land on after FinishLink.
type Envelope struct {
	TenantID         int64  `json:"t"`
	UserID           int64  `json:"u"`
	UpstreamID       int64  `json:"up"`
	ReturnTo         string `json:"r"`
	PKCEVerifier     string `json:"pkce"`
	Nonce            string `json:"n"`
	ServiceAccountID *int64 `json:"sa,omitempty"`
}

// Store is the Valkey-backed one-shot OAuth state store.
type Store struct {
	vk     valkey.Client
	cipher *crypto.Cipher
	ttl    time.Duration
}

// New builds a Store. cipher must be the process-wide crypto.Cipher used by
// SecretField everywhere else; reusing it keeps the AAD scheme uniform. ttl
// is optional — pass 0 for DefaultTTL.
func New(vk valkey.Client, cipher *crypto.Cipher, ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Store{vk: vk, cipher: cipher, ttl: ttl}
}

// Put serializes and encrypts env, generates a fresh 32-byte random state
// value, writes it to Valkey with the configured TTL, and returns the
// state value the caller must send to the AS as the "state" parameter.
func (s *Store) Put(ctx context.Context, env Envelope) (string, error) {
	if env.TenantID == 0 || env.UserID == 0 || env.UpstreamID == 0 {
		return "", errors.New("oauthstate: envelope missing required ids")
	}
	stateValue, err := randomState()
	if err != nil {
		return "", err
	}
	plaintext, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("oauthstate: marshal: %w", err)
	}
	aad := crypto.AAD{
		TenantID: fmt.Sprintf("%d", env.TenantID),
		UserID:   fmt.Sprintf("%d", env.UserID),
		Kind:     kindAAD,
	}
	ciphertext, err := s.cipher.Encrypt(plaintext, aad)
	if err != nil {
		return "", fmt.Errorf("oauthstate: encrypt: %w", err)
	}
	if err := s.vk.SetEX(ctx, keyPrefix+stateValue, ciphertext, s.ttl); err != nil {
		return "", err
	}
	return stateValue, nil
}

// Consume atomically reads and deletes the envelope for stateValue. The
// expectedTenantID / expectedUserID arguments scope decryption: a state
// minted under tenant A can never be decrypted under tenant B because AAD
// mismatch turns into an authentication failure inside the cipher. Returns
// ErrNotFound if the state is absent (expired or already consumed).
func (s *Store) Consume(ctx context.Context, stateValue string, expectedTenantID, expectedUserID int64) (Envelope, error) {
	var zero Envelope
	if stateValue == "" {
		return zero, ErrNotFound
	}
	ciphertext, err := s.vk.GetDel(ctx, keyPrefix+stateValue)
	if err != nil {
		if errors.Is(err, valkey.ErrNotFound) {
			return zero, ErrNotFound
		}
		return zero, err
	}
	aad := crypto.AAD{
		TenantID: fmt.Sprintf("%d", expectedTenantID),
		UserID:   fmt.Sprintf("%d", expectedUserID),
		Kind:     kindAAD,
	}
	plaintext, err := s.cipher.Decrypt(ciphertext, aad)
	if err != nil {
		// AAD mismatch or tamper — treat the same as not-found so we
		// never leak which case it was to the caller.
		return zero, ErrNotFound
	}
	var env Envelope
	if err := json.Unmarshal(plaintext, &env); err != nil {
		return zero, fmt.Errorf("oauthstate: unmarshal: %w", err)
	}
	if env.TenantID != expectedTenantID || env.UserID != expectedUserID {
		return zero, ErrNotFound
	}
	return env, nil
}

// ErrNotFound signals "this state value is unknown" — either it was never
// minted, it expired, or it was already consumed. Callers must treat all
// three identically so timing channels don't leak the distinction.
var ErrNotFound = errors.New("oauthstate: state not found")

func randomState() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("oauthstate: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

package storage

import (
	"time"

	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/ids"
)

// PendingSignup is the bookkeeping row for an in-flight self-serve
// signup. It is NOT under row-level security — there is no tenant
// yet, so RLS has nothing to scope. All access goes through the
// admin pool via storage.WithSuperuser, with the row identified
// either by id (snp_<ULID>), by verify_token_hash, or by
// completion_token_hash. Callers MUST pass an explicit predicate;
// an admin-pool query without one is a security bug.
//
// Lifecycle:
//
//  1. StartSignup INSERTs the row, mints a 32-byte verify token (raw
//     bytes returned to the email; HMAC-SHA256 hash stored here).
//  2. VerifyEmail looks up by verify token hash, sets EmailVerifiedAt,
//     ROTATES VerifyTokenHash to fresh random bytes (single-use),
//     mints a fresh completion token (plaintext returned to the SPA;
//     HMAC-SHA256 hash stored in CompletionTokenHash).
//  3. CompleteSignup looks up by completion token hash, provisions
//     the Zitadel org + tenant row, then writes CompletedAt /
//     ZitadelOrgID / ZitadelUserID / TenantID for idempotency.
//     The completion token hash is NOT rotated so a page refresh
//     after success can replay safely.
//
// A background sweeper deletes rows with CompletedAt IS NULL older
// than 24h.
type PendingSignup struct {
	// PublicID is the only externally referenced identifier ("snp_<ULID>").
	// It is set by BeforeCreate.
	ID        int64          `gorm:"primaryKey;autoIncrement" json:"-"`
	PublicID  string         `gorm:"type:text;uniqueIndex;not null" json:"id"`
	CreatedAt time.Time      `gorm:"type:timestamptz;not null;default:now()" json:"createdAt"`
	UpdatedAt time.Time      `gorm:"type:timestamptz;not null;default:now()" json:"updatedAt"`
	DeletedAt gorm.DeletedAt `gorm:"type:timestamptz;index" json:"-"`

	// EmailLower is the owner email lowercased — the form value
	// kept verbatim is held in OwnerEmail. An index on EmailLower
	// keeps the "already in use" probe fast without leaking case
	// variation.
	EmailLower      string `gorm:"type:text;not null;index"`
	OwnerEmail      string `gorm:"type:text;not null"`
	OwnerGivenName  string `gorm:"type:text;not null"`
	OwnerFamilyName string `gorm:"type:text;not null"`
	TenantName      string `gorm:"type:text;not null"`
	// IP is the request remote address at StartSignup time (rate-limit
	// audit + abuse correlation). Best-effort — proxies may rewrite.
	IP string `gorm:"type:text"`
	// VerifyTokenHash is the HMAC-SHA256 of the verification token
	// the user receives by email. Single-use: rotated to fresh random
	// bytes the first time VerifyEmail consumes it, so the link in
	// the inbox stops working.
	VerifyTokenHash []byte `gorm:"type:bytea;not null;uniqueIndex"`

	// CompletionTokenHash is the HMAC-SHA256 of the completion token
	// returned by VerifyEmail and consumed by CompleteSignup. Written
	// by VerifyEmail; nil until then. Kept after CompleteSignup so a
	// retry on the same token replays the success path. Unique because
	// it is the only secret authorising provisioning.
	CompletionTokenHash []byte `gorm:"type:bytea;uniqueIndex"`

	// EmailVerifiedAt is set by VerifyEmail. CompleteSignup refuses to
	// run when nil.
	EmailVerifiedAt *time.Time `gorm:"type:timestamptz"`

	// ZitadelOrgID + ZitadelUserID + TenantID are written by
	// CompleteSignup. They make the row self-describing for the
	// idempotent-replay path (returning the same tenant + a freshly
	// minted password-init link on retry).
	ZitadelOrgID  string `gorm:"type:text"`
	ZitadelUserID string `gorm:"type:text"`
	TenantID      *int64 `gorm:"index"`

	// CompletedAt is the idempotency signal: non-nil means
	// CompleteSignup has already finalised this signup; replay returns
	// the cached tenant id and a fresh password-init code.
	CompletedAt *time.Time `gorm:"type:timestamptz;index"`
}

func (p *PendingSignup) BeforeCreate(_ *gorm.DB) error {
	if p.PublicID == "" {
		p.PublicID = ids.New(ids.PrefixPendingSignup)
	}
	return nil
}

// TableName pins the table name so renaming the Go type later does
// not silently rename the table.
func (PendingSignup) TableName() string { return "pending_signups" }

package storage

import (
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/ids"
)

// ZitadelApp mirrors an MCP client registered through Limen's DCR proxy into
// Zitadel (see Phase 5). Allows the portal to list MCP clients and to
// authenticate RFC 7592 management requests without round-tripping Zitadel.
type ZitadelApp struct {
	Base
	TenantID         int64              `gorm:"not null;index;uniqueIndex:idx_zapp_tenant_zid,where:deleted_at IS NULL"`
	ZitadelAppID     string             `gorm:"type:text;not null;uniqueIndex:idx_zapp_tenant_zid,where:deleted_at IS NULL"`
	ZitadelProjectID string             `gorm:"type:text;not null;default:''"`
	ClientID         string             `gorm:"type:text;not null"`
	ClientSecret     crypto.SecretField `gorm:"type:bytea"`
	Name             string             `gorm:"type:text;not null"`
	RedirectURIs     string             `gorm:"type:text;not null;default:''"` // newline-joined; Phase 5 may swap for pq.StringArray
	SoftwareID       string             `gorm:"type:text"`
	SoftwareVersion  string             `gorm:"type:text"`
	// RegistrationAccessTokenHash is the SHA-256 digest of the token issued
	// on DCR; the plaintext token is returned to the client once and never
	// stored. RFC 7592 management endpoints compare via
	// subtle.ConstantTimeCompare. See docs/phases/phase-05-authorization-server.md.
	RegistrationAccessTokenHash []byte `gorm:"type:bytea;not null;default:'\\x'"`

	Tenant *Tenant `gorm:"foreignKey:TenantID;constraint:OnDelete:CASCADE"`
}

func (z *ZitadelApp) BeforeCreate(_ *gorm.DB) error {
	if z.PublicID == "" {
		z.PublicID = ids.New(ids.PrefixZitadelApp)
	}
	return nil
}

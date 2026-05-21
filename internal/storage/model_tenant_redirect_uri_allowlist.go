package storage

import (
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/ids"
)

// TenantRedirectURIAllowlist is one row per (tenant, pattern) on
// Limen's DCR redirect-URI allowlist. Rows seeded from an IDE preset
// carry IDEKey; free-form admin entries set IDEKey to NULL. RLS-forced
// on tenant_id; see migration 00010_tenant_redirect_uri_allowlist.sql.
//
// Label is denormalised on purpose: when a preset is later renamed
// (or the preset row vanishes and FK fires ON DELETE SET NULL), the
// row still has a human-readable label without a JOIN.
type TenantRedirectURIAllowlist struct {
	Base
	TenantID int64   `gorm:"not null;index"`
	IDEKey   *string `gorm:"type:text;column:ide_key"`
	Label    string  `gorm:"type:text;not null"`
	Pattern  string  `gorm:"type:text;not null"`

	Tenant *Tenant    `gorm:"foreignKey:TenantID;constraint:OnDelete:CASCADE"`
	IDE    *IDEPreset `gorm:"foreignKey:IDEKey;references:Key;constraint:OnDelete:SET NULL"`
}

// TableName pins the table name; the default pluraliser would emit
// "tenant_redirect_uri_allowlists" which reads oddly. Manual override
// matches the SQL migration's CREATE TABLE.
func (TenantRedirectURIAllowlist) TableName() string {
	return "tenant_redirect_uri_allowlist"
}

func (a *TenantRedirectURIAllowlist) BeforeCreate(_ *gorm.DB) error {
	if a.PublicID == "" {
		a.PublicID = ids.New(ids.PrefixAllowlistEntry)
	}
	return nil
}

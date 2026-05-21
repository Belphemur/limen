package storage

import (
	"time"

	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/ids"
)

// TenantSettings carries the mutable preferences + onboarding-progress
// timestamps associated with a Tenant. Identity columns (Name,
// PublicID, ZitadelOrgID, DCRRedirectURIAllowlist) stay on Tenant; this
// table is purely for things that change over the tenant's lifetime
// without altering its identity. One row per tenant; created lazily on
// first read by tenant.Service.LoadSettings.
type TenantSettings struct {
	Base
	TenantID      int64      `gorm:"uniqueIndex;not null"`
	InvitedTeamAt *time.Time `gorm:"type:timestamptz"`
	ConfiguredAt  *time.Time `gorm:"type:timestamptz"`

	Tenant *Tenant `gorm:"foreignKey:TenantID;constraint:OnDelete:CASCADE"`
}

func (t *TenantSettings) BeforeCreate(_ *gorm.DB) error {
	if t.PublicID == "" {
		t.PublicID = ids.New(ids.PrefixTenantSettings)
	}
	return nil
}

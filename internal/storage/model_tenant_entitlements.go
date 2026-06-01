package storage

import (
	"github.com/belphemur/limen/internal/ids"
	"gorm.io/gorm"
)

// TenantEntitlement holds a parsed feature entitlement for a tenant.
// Populated by the Stripe webhook handler from
// entitlements.active_entitlement_summary.updated events. The
// (tenant_id, feature) pair is unique when the row is live.
type TenantEntitlement struct {
	Base
	TenantID   int64   `gorm:"not null;uniqueIndex:idx_tenant_entitlements_tenant_feature,where:deleted_at IS NULL"`
	Feature    string  `gorm:"type:text;not null;uniqueIndex:idx_tenant_entitlements_tenant_feature,where:deleted_at IS NULL"`
	LimitValue int32   `gorm:"not null"` // -1 = unlimited/enabled, >0 = numeric cap, 0 = disabled
	Tenant     *Tenant `gorm:"foreignKey:TenantID;constraint:OnDelete:CASCADE"`
}

// TableName pins the table name to match the schema and migrations.
func (TenantEntitlement) TableName() string { return "tenant_entitlements" }

func (m *TenantEntitlement) BeforeCreate(_ *gorm.DB) error {
	if m.PublicID == "" {
		m.PublicID = ids.New(ids.PrefixTenantEntitlement)
	}
	return nil
}

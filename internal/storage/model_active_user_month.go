package storage

import (
	"time"

	"github.com/belphemur/limen/internal/ids"
	"gorm.io/gorm"
)

// ActiveUserMonth tracks per-tenant, per-month active users for billing.
type ActiveUserMonth struct {
	Base
	TenantID         int64     `gorm:"not null;index:idx_aum_tenant_month;uniqueIndex:idx_aum_unique,where:deleted_at IS NULL"`
	MonthStart       string    `gorm:"type:text;not null;index:idx_aum_tenant_month;uniqueIndex:idx_aum_unique,where:deleted_at IS NULL"` // "2026-05-01"
	UserID           *int64    `gorm:"index;uniqueIndex:idx_aum_unique,where:deleted_at IS NULL"`
	ServiceAccountID *int64    `gorm:"index;uniqueIndex:idx_aum_unique,where:deleted_at IS NULL"`
	FirstSeenAt      time.Time `gorm:"type:timestamptz;not null"`
	LastSeenAt       time.Time `gorm:"type:timestamptz;not null"`
	CallCount        int32     `gorm:"not null;default:0"`
	Tenant           *Tenant   `gorm:"foreignKey:TenantID;constraint:OnDelete:CASCADE"`
}

func (m *ActiveUserMonth) BeforeCreate(_ *gorm.DB) error {
	if m.PublicID == "" {
		m.PublicID = ids.New(ids.PrefixActiveUserMonth)
	}
	return nil
}

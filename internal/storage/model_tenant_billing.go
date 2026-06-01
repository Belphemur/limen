package storage

import (
	"time"

	"github.com/belphemur/limen/internal/ids"
	"gorm.io/gorm"
)

// TenantBilling holds per-tenant Stripe subscription state. One row per
// tenant — tenant_id is unique. Mirrors Stripe's Customer/Subscription
// objects.
type TenantBilling struct {
	Base
	TenantID                  int64      `gorm:"not null;uniqueIndex:idx_tenant_billing_tenant,where:deleted_at IS NULL"`
	StripeCustomerID          *string    `gorm:"type:text"`
	StripeSubscriptionID      *string    `gorm:"type:text"`
	Status                    string     `gorm:"type:text;not null;default:'none'"`
	Plan                      string     `gorm:"type:text;not null;default:'developer'"`
	ActiveUserCount           int32      `gorm:"not null;default:0"`
	ActiveSAConnectionCount   int32      `gorm:"not null;default:0"`
	StripeActiveUserPriceID   *string    `gorm:"type:text"`
	StripeSAConnectionPriceID *string    `gorm:"type:text"`
	CurrentPeriodEnd          *time.Time `gorm:"type:timestamptz"`
	CancelAtPeriodEnd         bool       `gorm:"not null;default:false"`
	GraceUntil                *time.Time `gorm:"type:timestamptz"`
	LastSyncedAt              *time.Time `gorm:"type:timestamptz"`
	Tenant                    *Tenant    `gorm:"foreignKey:TenantID;constraint:OnDelete:CASCADE"`
}

// TableName pins the table name; the default pluraliser would emit
// tenant_billings, but the schema and migrations use tenant_billing.
func (TenantBilling) TableName() string { return "tenant_billing" }

func (m *TenantBilling) BeforeCreate(_ *gorm.DB) error {
	if m.PublicID == "" {
		m.PublicID = ids.New(ids.PrefixTenantBilling)
	}
	return nil
}

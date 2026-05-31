package storage

import (
	"time"

	"github.com/belphemur/limen/internal/ids"
	"gorm.io/gorm"
)

// SAConnectionSnapshot records service account connect/disconnect events for billing.
type SAConnectionSnapshot struct {
	Base
	TenantID         int64      `gorm:"not null;index:idx_sacs_tenant_month"`
	ServiceAccountID int64      `gorm:"not null;index"`
	ConnectedAt      time.Time  `gorm:"type:timestamptz;not null;index:idx_sacs_tenant_month"`
	DisconnectedAt   *time.Time `gorm:"type:timestamptz"` // NULL = still connected
	ConcurrentCount  int32      `gorm:"not null;default:0"`
	Tenant           *Tenant    `gorm:"foreignKey:TenantID;constraint:OnDelete:CASCADE"`
}

func (s *SAConnectionSnapshot) BeforeCreate(_ *gorm.DB) error {
	if s.PublicID == "" {
		s.PublicID = ids.New(ids.PrefixSAConnectionSnapshot)
	}
	return nil
}

package storage

import (
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/ids"
)

// User is a tenant-scoped mirror of a Zitadel human user. Credentials and
// invitation flow live entirely in Zitadel; this row records the local
// representation so we can attach upstream links and emit audit logs without
// hitting the IdP on every request.
type User struct {
	Base
	TenantID       int64  `gorm:"not null;index;uniqueIndex:idx_user_tenant_email,where:deleted_at IS NULL;uniqueIndex:idx_user_tenant_zsub,where:deleted_at IS NULL"`
	Email          string `gorm:"type:text;not null;uniqueIndex:idx_user_tenant_email,where:deleted_at IS NULL"`
	Name           string `gorm:"type:text;not null"`
	ZitadelSubject string `gorm:"type:text;not null;uniqueIndex:idx_user_tenant_zsub,where:deleted_at IS NULL"`

	Tenant *Tenant `gorm:"foreignKey:TenantID;constraint:OnDelete:CASCADE"`
}

func (u *User) BeforeCreate(_ *gorm.DB) error {
	if u.PublicID == "" {
		u.PublicID = ids.New(ids.PrefixUser)
	}
	return nil
}

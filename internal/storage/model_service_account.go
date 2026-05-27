package storage

import (
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/ids"
)

// ServiceAccount is a tenant-scoped mirror of a Zitadel machine user. It
// represents a non-human identity that can hold a long-lived API token
// (PAT) and authenticate to Connect-RPC and MCP endpoints via Bearer token.
// Service accounts cannot log in through the browser OIDC flow — admins
// configure upstream links for service accounts via the admin UI.
type ServiceAccount struct {
	Base
	TenantID      int64  `gorm:"not null;index;uniqueIndex:idx_sa_tenant_zitadel,where:deleted_at IS NULL"`
	Name          string `gorm:"type:text;not null"`
	Description   string `gorm:"type:text"`
	ZitadelUserID string `gorm:"type:text;not null;uniqueIndex:idx_sa_tenant_zitadel,where:deleted_at IS NULL"`
	CreatedByID   int64  `gorm:"not null;index"`
	Role          string `gorm:"type:text;not null"`

	Tenant *Tenant `gorm:"foreignKey:TenantID;constraint:OnDelete:CASCADE"`
}

func (sa *ServiceAccount) BeforeCreate(_ *gorm.DB) error {
	if sa.PublicID == "" {
		sa.PublicID = ids.New(ids.PrefixServiceAccount)
	}
	return nil
}

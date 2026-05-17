package storage

import (
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/ids"
)

// Tenant is the root multi-tenancy entity. One tenant maps 1:1 to a Zitadel
// organization (see Phase 4 for the binding). The tenant's PublicID (a
// "tnt_<ULID>" string from internal/ids) is the only externally visible
// identifier: it is the URL path component (`/t/{tenant}/...`) and the
// value mirrored back into Zitadel org metadata at provision time.
type Tenant struct {
	Base
	Name         string `gorm:"type:text;not null"`
	ZitadelOrgID string `gorm:"type:text;uniqueIndex;not null"`
	DCREnabled   bool   `gorm:"not null;default:false"`
	// DCRRedirectURIAllowlist is the per-tenant subtractive glob filter on
	// redirect_uri values accepted by Limen's DCR proxy. Empty list = floor
	// only; see internal/oauthproxy/uripolicy.go.
	DCRRedirectURIAllowlist []string `gorm:"type:jsonb;serializer:json;not null;default:'[]'"`
}

func (t *Tenant) BeforeCreate(_ *gorm.DB) error {
	if t.PublicID == "" {
		t.PublicID = ids.New(ids.PrefixTenant)
	}
	return nil
}

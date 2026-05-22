package storage

import (
	"time"

	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/ids"
)

// UpstreamTool is the cached MCP tool catalog for an upstream (Phase 8).
// One row per (upstream, tool name). Populated by the catalog indexer on
// Provision (tenant-mode strategies) and on FinishLink (per-user
// strategies); refreshed periodically by the upstream refresher.
//
// The tool catalog is per-upstream — every user of the same upstream sees
// the same tool surface. InputSchemaJSON stores the raw JSON Schema as a
// jsonb column so it can be served back to the codemode sandbox without
// re-parsing.
type UpstreamTool struct {
	Base
	TenantID        int64     `gorm:"not null;index;uniqueIndex:idx_upstream_tool_unique,where:deleted_at IS NULL"`
	UpstreamID      int64     `gorm:"not null;index;uniqueIndex:idx_upstream_tool_unique,where:deleted_at IS NULL"`
	Name            string    `gorm:"type:text;not null;uniqueIndex:idx_upstream_tool_unique,where:deleted_at IS NULL"`
	Description     string    `gorm:"type:text;not null;default:''"`
	InputSchemaJSON []byte    `gorm:"type:jsonb;not null;default:'{}'"`
	LastIndexedAt   time.Time `gorm:"type:timestamptz;not null;default:now()"`

	Tenant   *Tenant   `gorm:"foreignKey:TenantID;constraint:OnDelete:CASCADE"`
	Upstream *Upstream `gorm:"foreignKey:UpstreamID;constraint:OnDelete:CASCADE"`
}

func (t *UpstreamTool) BeforeCreate(_ *gorm.DB) error {
	if t.PublicID == "" {
		t.PublicID = ids.New(ids.PrefixUpstreamTool)
	}
	return nil
}

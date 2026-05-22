// Package storage defines the persistent data models for Limen.
//
// Each model lives in its own file (model_*.go) and is registered in
// AllModels() below; AutoMigrate consumes that list to create / evolve
// the Postgres schema. Cross-cutting concerns — base columns, soft-delete
// semantics, the audit-trigger contract — live here so adding a new model
// only means a new file plus one entry in AllModels.
package storage

import (
	"time"

	"gorm.io/gorm"
)

// Base is embedded by every persistent model. It carries the internal int64
// primary key, the public KSUID-with-prefix ID, and the audit timestamps.
//
// CreatedAt is set by the DB default. UpdatedAt is maintained by the
// set_updated_at trigger installed in Phase 3 — application code must never
// touch it. DeletedAt is a soft-delete sentinel; GORM transparently filters
// out rows where DeletedAt is non-NULL unless Unscoped() is used.
type Base struct {
	ID        int64          `gorm:"primaryKey;autoIncrement" json:"-"`
	PublicID  string         `gorm:"type:text;uniqueIndex;not null" json:"id"`
	CreatedAt time.Time      `gorm:"type:timestamptz;not null;default:now()" json:"createdAt"`
	UpdatedAt time.Time      `gorm:"type:timestamptz;not null;default:now()" json:"updatedAt"`
	DeletedAt gorm.DeletedAt `gorm:"type:timestamptz;index" json:"-"`
}

// AllModels is the canonical list passed to AutoMigrate. Adding a model
// requires creating its model_*.go file, appending it here, and assigning
// a prefix in internal/ids.
func AllModels() []any {
	return []any{
		&Tenant{},
		&TenantSettings{},
		&User{},
		&Upstream{},
		&UpstreamStrategyConfig{},
		&UpstreamRegistration{},
		&UpstreamLink{},
		&UpstreamTool{},
		&ZitadelApp{},
		&IDEPreset{},
		&IDEPresetPattern{},
		&TenantRedirectURIAllowlist{},
		&PendingSignup{},
	}
}

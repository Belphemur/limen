package storage

import (
	"time"

	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/ids"
)

// Upstream describes a single MCP server registered under a tenant.
// StrategyType is the extension point for future credential strategies; v1
// recognizes "mcp_spec" (RFC 8707 DCR) and "none" (no auth).
type Upstream struct {
	Base
	TenantID     int64  `gorm:"not null;index;uniqueIndex:idx_upstream_tenant_name,where:deleted_at IS NULL"`
	Name         string `gorm:"type:text;not null;uniqueIndex:idx_upstream_tenant_name,where:deleted_at IS NULL"`
	StrategyType string `gorm:"type:text;not null"`
	McpServerURL string `gorm:"type:text;not null"`

	// Phase 8c — ambient context + alias discovery.
	//
	// DefaultsJSON: per-upstream defaults merged under UpstreamLink.ContextJSON
	// at read time and surfaced on codemode.tools() groups. Tenant-admin
	// owned. Validated by gateway.ValidateContextBlob on write.
	//
	// AliasesJSON: JSON array of derived prefix aliases (e.g. ["jira",
	// "confluence"]). Recomputed by IndexUpstream after each successful
	// tools/list reconcile, with a tenant-wide collision pass.
	DefaultsJSON []byte `gorm:"column:defaults_json;type:jsonb;not null;default:'{}'::jsonb"`
	AliasesJSON  []byte `gorm:"column:aliases_json;type:jsonb;not null;default:'[]'::jsonb"`

	Tenant *Tenant `gorm:"foreignKey:TenantID;constraint:OnDelete:CASCADE"`
}

func (u *Upstream) BeforeCreate(_ *gorm.DB) error {
	if u.PublicID == "" {
		u.PublicID = ids.New(ids.PrefixUpstream)
	}
	return nil
}

// UpstreamStrategyConfig holds strategy-specific parameters as opaque-to-storage
// JSON. mcp_spec and none leave ConfigJSON empty in v1.
type UpstreamStrategyConfig struct {
	Base
	TenantID   int64              `gorm:"not null;index"`
	UpstreamID int64              `gorm:"not null;uniqueIndex,where:deleted_at IS NULL"`
	Type       string             `gorm:"type:text;not null"`
	ConfigJSON crypto.SecretField `gorm:"type:bytea"`

	Upstream *Upstream `gorm:"foreignKey:UpstreamID;constraint:OnDelete:CASCADE"`
}

func (u *UpstreamStrategyConfig) BeforeCreate(_ *gorm.DB) error {
	if u.PublicID == "" {
		u.PublicID = ids.New(ids.PrefixUpstreamStrategyConfig)
	}
	return nil
}

// UpstreamRegistration is the DCR result per (tenant, upstream) against an
// *external* MCP server. "none" strategies leave this empty.
type UpstreamRegistration struct {
	Base
	TenantID                int64              `gorm:"not null;index"`
	UpstreamID              int64              `gorm:"not null;uniqueIndex,where:deleted_at IS NULL"`
	Issuer                  string             `gorm:"type:text;not null"`
	ClientID                string             `gorm:"type:text;not null"`
	ClientSecret            crypto.SecretField `gorm:"type:bytea"`
	RegistrationAccessToken crypto.SecretField `gorm:"type:bytea"`
	RegistrationClientURI   string             `gorm:"type:text"`
	ResourceURI             string             `gorm:"type:text;not null"`

	Upstream *Upstream `gorm:"foreignKey:UpstreamID;constraint:OnDelete:CASCADE"`
}

func (u *UpstreamRegistration) BeforeCreate(_ *gorm.DB) error {
	if u.PublicID == "" {
		u.PublicID = ids.New(ids.PrefixUpstreamRegistration)
	}
	return nil
}

// UpstreamLink binds a tenant user to an upstream's per-user credentials.
// Only created when the upstream strategy reports RequiresLink()==true.
type UpstreamLink struct {
	Base
	TenantID     int64              `gorm:"not null;index;uniqueIndex:idx_link_tenant_user_upstream,where:deleted_at IS NULL"`
	UserID       int64              `gorm:"not null;index;uniqueIndex:idx_link_tenant_user_upstream,where:deleted_at IS NULL"`
	UpstreamID   int64              `gorm:"not null;index;uniqueIndex:idx_link_tenant_user_upstream,where:deleted_at IS NULL"`
	AccessToken  crypto.SecretField `gorm:"type:bytea"`
	RefreshToken crypto.SecretField `gorm:"type:bytea"`
	ExpiresAt    *time.Time         `gorm:"type:timestamptz"`
	Scopes       string             `gorm:"type:text;not null;default:''"`
	ResourceURI  string             `gorm:"type:text;not null;default:''"`
	ExtraJSON    crypto.SecretField `gorm:"type:bytea"`

	// Phase 7 — health / lifecycle. See docs/phases/phase-07-outbound-upstream.md.
	//
	// Enabled is the user's explicit toggle; AutoDisabledAt is set by the
	// auto-disable trip in health.RecordFailure. NeedsRelink flips true
	// when the AS returns invalid_grant during a refresh. FirstFailureAt
	// is the streak-start timestamp (set on the 0→1 transition; cleared
	// on any success) so the "≥15 min over the streak" rule has a stable
	// anchor that LastFailureAt alone cannot provide.
	Enabled             bool       `gorm:"not null;default:true"`
	NeedsRelink         bool       `gorm:"not null;default:false;index"`
	ConsecutiveFailures int        `gorm:"not null;default:0"`
	FirstFailureAt      *time.Time `gorm:"type:timestamptz"`
	LastFailureAt       *time.Time `gorm:"type:timestamptz"`
	LastFailureReason   string     `gorm:"type:text;not null;default:''"`
	AutoDisabledAt      *time.Time `gorm:"type:timestamptz;index"`

	// Phase 8c — per-link context overlay. Merged over Upstream.DefaultsJSON
	// at read time and surfaced on codemode.tools() groups. Validated by
	// gateway.ValidateContextBlob on write.
	ContextJSON []byte `gorm:"column:context_json;type:jsonb;not null;default:'{}'::jsonb"`

	User     *User     `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE"`
	Upstream *Upstream `gorm:"foreignKey:UpstreamID;constraint:OnDelete:CASCADE"`
}

func (u *UpstreamLink) BeforeCreate(_ *gorm.DB) error {
	if u.PublicID == "" {
		u.PublicID = ids.New(ids.PrefixUpstreamLink)
	}
	return nil
}

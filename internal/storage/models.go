package storage

import (
	"time"

	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/ids"
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
// requires appending it here and assigning a prefix in internal/ids.
func AllModels() []any {
	return []any{
		&Tenant{},
		&User{},
		&Upstream{},
		&UpstreamStrategyConfig{},
		&UpstreamRegistration{},
		&UpstreamLink{},
		&ZitadelApp{},
	}
}

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

// Upstream describes a single MCP server registered under a tenant.
// StrategyType is the extension point for future credential strategies; v1
// recognizes "mcp_spec" (RFC 8707 DCR) and "none" (no auth).
type Upstream struct {
	Base
	TenantID     int64  `gorm:"not null;index;uniqueIndex:idx_upstream_tenant_name,where:deleted_at IS NULL"`
	Name         string `gorm:"type:text;not null;uniqueIndex:idx_upstream_tenant_name,where:deleted_at IS NULL"`
	StrategyType string `gorm:"type:text;not null"`
	McpServerURL string `gorm:"type:text;not null"`

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

	User     *User     `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE"`
	Upstream *Upstream `gorm:"foreignKey:UpstreamID;constraint:OnDelete:CASCADE"`
}

func (u *UpstreamLink) BeforeCreate(_ *gorm.DB) error {
	if u.PublicID == "" {
		u.PublicID = ids.New(ids.PrefixUpstreamLink)
	}
	return nil
}

// ZitadelApp mirrors an MCP client registered through Limen's DCR proxy into
// Zitadel (see Phase 5). Allows the portal to list MCP clients and to
// authenticate RFC 7592 management requests without round-tripping Zitadel.
type ZitadelApp struct {
	Base
	TenantID        int64              `gorm:"not null;index;uniqueIndex:idx_zapp_tenant_zid,where:deleted_at IS NULL"`
	ZitadelAppID    string             `gorm:"type:text;not null;uniqueIndex:idx_zapp_tenant_zid,where:deleted_at IS NULL"`
	ClientID        string             `gorm:"type:text;not null"`
	ClientSecret    crypto.SecretField `gorm:"type:bytea"`
	Name            string             `gorm:"type:text;not null"`
	RedirectURIs    string             `gorm:"type:text;not null;default:''"` // newline-joined; Phase 5 may swap for pq.StringArray
	SoftwareID      string             `gorm:"type:text"`
	SoftwareVersion string             `gorm:"type:text"`
	// RegistrationAccessTokenHash is the SHA-256 digest of the token issued
	// on DCR; the plaintext token is returned to the client once and never
	// stored. RFC 7592 management endpoints compare via
	// subtle.ConstantTimeCompare. See docs/phases/phase-05-authorization-server.md.
	RegistrationAccessTokenHash []byte `gorm:"type:bytea;not null;default:'\\x'"`

	Tenant *Tenant `gorm:"foreignKey:TenantID;constraint:OnDelete:CASCADE"`
}

func (z *ZitadelApp) BeforeCreate(_ *gorm.DB) error {
	if z.PublicID == "" {
		z.PublicID = ids.New(ids.PrefixZitadelApp)
	}
	return nil
}

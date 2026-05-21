package storage

import "time"

// IDEPreset is a global, seeded row describing a well-known IDE / MCP
// client and the redirect-URI globs it uses. Seeded by SQL migration
// 00009_ide_presets.sql; runtime code (admin handlers, the SPA) only
// reads them. There is NO PublicID, no soft-delete: the key column is
// the externally addressable identifier, and presets are never
// deleted at runtime — only added or sort_order'd.
//
// The table is GLOBAL (not tenant-scoped) and therefore intentionally
// NOT covered by RLS — every tenant reads the same catalog.
type IDEPreset struct {
	Key         string    `gorm:"type:text;primaryKey"`
	DisplayName string    `gorm:"type:text;not null"`
	Icon        string    `gorm:"type:text;not null"`
	SortOrder   int       `gorm:"not null;default:100"`
	CreatedAt   time.Time `gorm:"type:timestamptz;not null;default:now()"`
	UpdatedAt   time.Time `gorm:"type:timestamptz;not null;default:now()"`

	Patterns []IDEPresetPattern `gorm:"foreignKey:IDEKey;references:Key;constraint:OnDelete:CASCADE"`
}

// IDEPresetPattern is one redirect-URI glob the preset declares.
// Every Pattern passes oauthproxy.CompilePattern at seed time (the
// SQL INSERT runs through the same validator via an integration test
// — a typo fails CI, not boot).
type IDEPresetPattern struct {
	ID        int64  `gorm:"primaryKey;autoIncrement"`
	IDEKey    string `gorm:"type:text;not null;index;column:ide_key"`
	Pattern   string `gorm:"type:text;not null"`
	SortOrder int    `gorm:"not null;default:0"`
}

// TableName pins the snake_case table name; default GORM pluralisation
// of "IDEPreset" would emit "ide_presets" anyway but we make it
// explicit so renames here can't accidentally rotate the table name.
func (IDEPreset) TableName() string        { return "ide_presets" }
func (IDEPresetPattern) TableName() string { return "ide_preset_patterns" }

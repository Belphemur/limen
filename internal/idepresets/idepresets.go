// Package idepresets is the read-only access layer for the global
// `ide_presets` + `ide_preset_patterns` catalog. The seed lives in SQL
// migration 00009_ide_presets.sql; runtime code only ever reads. See
// docs/phases/phase-09f-ide-presets-and-allowlist.md.
package idepresets

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/storage"
)

// Preset is the wire-friendly view of a preset row joined with its
// patterns, sorted in display order. The admin RPC handler converts
// this struct directly into adminv1.IDEPreset.
type Preset struct {
	Key         string
	DisplayName string
	Icon        string
	SortOrder   int
	Patterns    []string
}

// List returns every preset with its patterns, ordered by
// (preset.sort_order, preset.key) and then by (pattern.sort_order,
// pattern.id) inside each preset. Uses the supplied tx so callers can
// run inside an existing session; the function does NOT pin tenancy.
func List(ctx context.Context, tx *gorm.DB) ([]Preset, error) {
	var rows []storage.IDEPreset
	if err := tx.WithContext(ctx).
		Preload("Patterns", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order ASC, id ASC")
		}).
		Order("sort_order ASC, key ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("idepresets: list: %w", err)
	}
	out := make([]Preset, 0, len(rows))
	for _, r := range rows {
		p := Preset{
			Key:         r.Key,
			DisplayName: r.DisplayName,
			Icon:        r.Icon,
			SortOrder:   r.SortOrder,
			Patterns:    make([]string, 0, len(r.Patterns)),
		}
		for _, pp := range r.Patterns {
			p.Patterns = append(p.Patterns, pp.Pattern)
		}
		out = append(out, p)
	}
	return out, nil
}

// Get returns a single preset by key. Returns gorm.ErrRecordNotFound
// when no such preset exists.
func Get(ctx context.Context, tx *gorm.DB, key string) (Preset, error) {
	var r storage.IDEPreset
	if err := tx.WithContext(ctx).
		Preload("Patterns", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order ASC, id ASC")
		}).
		Where("key = ?", key).
		First(&r).Error; err != nil {
		return Preset{}, err
	}
	p := Preset{
		Key:         r.Key,
		DisplayName: r.DisplayName,
		Icon:        r.Icon,
		SortOrder:   r.SortOrder,
		Patterns:    make([]string, 0, len(r.Patterns)),
	}
	for _, pp := range r.Patterns {
		p.Patterns = append(p.Patterns, pp.Pattern)
	}
	return p, nil
}

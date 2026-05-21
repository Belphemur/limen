package tenant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/idepresets"
	"github.com/belphemur/limen/internal/oauthproxy"
	"github.com/belphemur/limen/internal/storage"
)

// AllowlistEntry is the per-row view of the DCR redirect-URI
// allowlist used by admin handlers + the oauthproxy DCR loader.
type AllowlistEntry struct {
	PublicID  string
	IDEKey    string // "" when the row is a free-form admin entry
	Label     string
	Pattern   string
	CreatedAt int64 // unix seconds, RFC3339-friendly
}

// AddAllowlistEntryInput is the per-row write surface.
type AddAllowlistEntryInput struct {
	IDEKey  string // "" for custom entries
	Label   string
	Pattern string
}

// UpdateAllowlistEntryInput drives Service.UpdateAllowlistEntry. Both
// fields are required (the row already exists; we only support full
// edits, not partial — matches the SPA's modal UX).
type UpdateAllowlistEntryInput struct {
	Label   string
	Pattern string
}

// ApplyPresetResult is the wire-friendly summary the admin RPC returns
// from ApplyIDEPreset.
type ApplyPresetResult struct {
	Added          int
	AlreadyPresent int
}

// ErrAllowlistEntryInvalid wraps a per-field validation failure with
// the field path the SPA should pin its error message to.
type ErrAllowlistEntryInvalid struct {
	Field string
	Err   error
}

func (e *ErrAllowlistEntryInvalid) Error() string {
	return fmt.Sprintf("tenant: allowlist %s: %v", e.Field, e.Err)
}
func (e *ErrAllowlistEntryInvalid) Unwrap() error { return e.Err }

// ErrAllowlistEntryDuplicate is returned when (tenant_id, pattern)
// already exists. Admin handler maps to AlreadyExists.
var ErrAllowlistEntryDuplicate = errors.New("tenant: allowlist entry already exists")

// ErrAllowlistEntryNotFound is returned when no row with the given
// public_id exists for the current tenant.
var ErrAllowlistEntryNotFound = errors.New("tenant: allowlist entry not found")

// ErrPresetNotFound is returned by ApplyIDEPreset / RemoveIDEPreset
// when no preset with the given key exists.
var ErrPresetNotFound = errors.New("tenant: ide preset not found")

// ErrPresetEmpty is returned by ApplyIDEPreset when the catalog row
// exists but has no patterns. Defence-in-depth — the seed always
// populates patterns.
var ErrPresetEmpty = errors.New("tenant: ide preset has no patterns")

const allowlistLabelMaxLen = 120

// ListAllowlistEntries returns every row for the current tenant,
// ordered (ide_key NULLS LAST, created_at ASC) so preset-grouped rows
// stay together and custom rows trail.
func (s *Service) ListAllowlistEntries(ctx context.Context, tenant *storage.Tenant) ([]AllowlistEntry, error) {
	if tenant == nil {
		return nil, errors.New("tenant: nil tenant")
	}
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenant.ID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = commit() }()

	var rows []storage.TenantRedirectURIAllowlist
	if err := tx.
		Order("ide_key NULLS LAST, created_at ASC, id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("tenant: list allowlist: %w", err)
	}
	return toAllowlistEntries(rows), nil
}

// ListAllowlistPatterns returns the deduplicated raw patterns for the
// current tenant. This is the single read site the oauthproxy DCR
// loader uses; keep the implementation tight — it runs on every DCR
// request.
func (s *Service) ListAllowlistPatterns(ctx context.Context, tenant *storage.Tenant) ([]string, error) {
	if tenant == nil {
		return nil, errors.New("tenant: nil tenant")
	}
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenant.ID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = commit() }()

	var patterns []string
	if err := tx.Model(&storage.TenantRedirectURIAllowlist{}).
		Distinct("pattern").
		Order("pattern ASC").
		Pluck("pattern", &patterns).Error; err != nil {
		return nil, fmt.Errorf("tenant: list allowlist patterns: %w", err)
	}
	return patterns, nil
}

// AddAllowlistEntry inserts a new row. Validates label + pattern;
// duplicate (tenant, pattern) returns ErrAllowlistEntryDuplicate.
func (s *Service) AddAllowlistEntry(ctx context.Context, tenant *storage.Tenant, in AddAllowlistEntryInput) (AllowlistEntry, error) {
	if tenant == nil {
		return AllowlistEntry{}, errors.New("tenant: nil tenant")
	}
	label, err := normaliseLabel(in.Label)
	if err != nil {
		return AllowlistEntry{}, err
	}
	pattern, err := normalisePattern(in.Pattern)
	if err != nil {
		return AllowlistEntry{}, err
	}
	ideKey := strings.TrimSpace(in.IDEKey)

	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenant.ID))
	if err != nil {
		return AllowlistEntry{}, err
	}

	if ideKey != "" {
		// Verify preset exists. Read against the same tx (no RLS
		// on ide_presets).
		var count int64
		if err := tx.Model(&storage.IDEPreset{}).Where("key = ?", ideKey).Count(&count).Error; err != nil {
			_ = commit()
			return AllowlistEntry{}, fmt.Errorf("tenant: verify preset: %w", err)
		}
		if count == 0 {
			_ = commit()
			return AllowlistEntry{}, ErrPresetNotFound
		}
	}

	row := storage.TenantRedirectURIAllowlist{
		TenantID: tenant.ID,
		Label:    label,
		Pattern:  pattern,
	}
	if ideKey != "" {
		row.IDEKey = &ideKey
	}
	if err := tx.Create(&row).Error; err != nil {
		_ = commit()
		if isUniqueViolation(err) {
			return AllowlistEntry{}, ErrAllowlistEntryDuplicate
		}
		return AllowlistEntry{}, fmt.Errorf("tenant: insert allowlist: %w", err)
	}
	if err := commit(); err != nil {
		return AllowlistEntry{}, err
	}
	return toAllowlistEntry(row), nil
}

// UpdateAllowlistEntry edits an existing row in-place. Both label and
// pattern are required (full-row replace — no partial updates).
func (s *Service) UpdateAllowlistEntry(ctx context.Context, tenant *storage.Tenant, publicID string, in UpdateAllowlistEntryInput) (AllowlistEntry, error) {
	if tenant == nil {
		return AllowlistEntry{}, errors.New("tenant: nil tenant")
	}
	publicID = strings.TrimSpace(publicID)
	if publicID == "" {
		return AllowlistEntry{}, &ErrAllowlistEntryInvalid{Field: "public_id", Err: errors.New("required")}
	}
	label, err := normaliseLabel(in.Label)
	if err != nil {
		return AllowlistEntry{}, err
	}
	pattern, err := normalisePattern(in.Pattern)
	if err != nil {
		return AllowlistEntry{}, err
	}

	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenant.ID))
	if err != nil {
		return AllowlistEntry{}, err
	}

	var row storage.TenantRedirectURIAllowlist
	if err := tx.Where("public_id = ?", publicID).First(&row).Error; err != nil {
		_ = commit()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return AllowlistEntry{}, ErrAllowlistEntryNotFound
		}
		return AllowlistEntry{}, fmt.Errorf("tenant: load allowlist: %w", err)
	}
	row.Label = label
	row.Pattern = pattern
	if err := tx.Save(&row).Error; err != nil {
		_ = commit()
		if isUniqueViolation(err) {
			return AllowlistEntry{}, ErrAllowlistEntryDuplicate
		}
		return AllowlistEntry{}, fmt.Errorf("tenant: update allowlist: %w", err)
	}
	if err := commit(); err != nil {
		return AllowlistEntry{}, err
	}
	return toAllowlistEntry(row), nil
}

// RemoveAllowlistEntry soft-deletes the row identified by publicID for
// the current tenant.
func (s *Service) RemoveAllowlistEntry(ctx context.Context, tenant *storage.Tenant, publicID string) error {
	if tenant == nil {
		return errors.New("tenant: nil tenant")
	}
	publicID = strings.TrimSpace(publicID)
	if publicID == "" {
		return &ErrAllowlistEntryInvalid{Field: "public_id", Err: errors.New("required")}
	}
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenant.ID))
	if err != nil {
		return err
	}
	res := tx.Where("public_id = ?", publicID).Delete(&storage.TenantRedirectURIAllowlist{})
	if res.Error != nil {
		_ = commit()
		return fmt.Errorf("tenant: delete allowlist: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		_ = commit()
		return ErrAllowlistEntryNotFound
	}
	return commit()
}

// ApplyIDEPreset inserts every pattern the preset declares that the
// tenant doesn't already have. Returns the tally of newly-added vs.
// already-present rows. Sets chose_ide_at on the first successful call
// per tenant (any preset counts).
func (s *Service) ApplyIDEPreset(ctx context.Context, tenant *storage.Tenant, ideKey string) (ApplyPresetResult, error) {
	if tenant == nil {
		return ApplyPresetResult{}, errors.New("tenant: nil tenant")
	}
	ideKey = strings.TrimSpace(ideKey)
	if ideKey == "" {
		return ApplyPresetResult{}, &ErrAllowlistEntryInvalid{Field: "ide_key", Err: errors.New("required")}
	}

	// Make sure the settings row exists so we can flip chose_ide_at
	// in the same tx.
	if _, err := s.loadOrCreateSettings(ctx, tenant.ID); err != nil {
		return ApplyPresetResult{}, err
	}

	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenant.ID))
	if err != nil {
		return ApplyPresetResult{}, err
	}

	preset, err := idepresets.Get(ctx, tx, ideKey)
	if err != nil {
		_ = commit()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ApplyPresetResult{}, ErrPresetNotFound
		}
		return ApplyPresetResult{}, fmt.Errorf("tenant: load preset: %w", err)
	}
	if len(preset.Patterns) == 0 {
		_ = commit()
		return ApplyPresetResult{}, ErrPresetEmpty
	}

	// Pull existing patterns under THIS preset so we re-apply
	// idempotently. Patterns owned by other presets (or by custom
	// rows) don't block us: the partial unique index is keyed on
	// (tenant_id, ide_key, pattern), so the same URI may legitimately
	// appear under several IDEs (Claude Code / Windsurf / Kiro all
	// declare http://localhost:*/callback).
	var existing []string
	if err := tx.Model(&storage.TenantRedirectURIAllowlist{}).
		Where("ide_key = ?", preset.Key).
		Pluck("pattern", &existing).Error; err != nil {
		_ = commit()
		return ApplyPresetResult{}, fmt.Errorf("tenant: load existing patterns: %w", err)
	}
	have := make(map[string]struct{}, len(existing))
	for _, p := range existing {
		have[p] = struct{}{}
	}

	result := ApplyPresetResult{}
	for _, p := range preset.Patterns {
		if _, ok := have[p]; ok {
			result.AlreadyPresent++
			continue
		}
		key := preset.Key
		row := storage.TenantRedirectURIAllowlist{
			TenantID: tenant.ID,
			IDEKey:   &key,
			Label:    preset.DisplayName,
			Pattern:  p,
		}
		if err := tx.Create(&row).Error; err != nil {
			_ = commit()
			if isUniqueViolation(err) {
				// Race: another writer beat us. Treat as already-present.
				result.AlreadyPresent++
				continue
			}
			return ApplyPresetResult{}, fmt.Errorf("tenant: insert preset row: %w", err)
		}
		result.Added++
	}

	// Flip chose_ide_at on first apply.
	if err := tx.Model(&storage.TenantSettings{}).
		Where("chose_ide_at IS NULL").
		Update("chose_ide_at", gorm.Expr("now()")).Error; err != nil {
		_ = commit()
		return ApplyPresetResult{}, fmt.Errorf("tenant: stamp chose_ide_at: %w", err)
	}
	if err := commit(); err != nil {
		return ApplyPresetResult{}, err
	}
	return result, nil
}

// RemoveIDEPreset deletes every allowlist row whose IDEKey matches the
// given key, leaving custom (IDEKey IS NULL) rows untouched. Returns
// the number of rows deleted.
func (s *Service) RemoveIDEPreset(ctx context.Context, tenant *storage.Tenant, ideKey string) (int, error) {
	if tenant == nil {
		return 0, errors.New("tenant: nil tenant")
	}
	ideKey = strings.TrimSpace(ideKey)
	if ideKey == "" {
		return 0, &ErrAllowlistEntryInvalid{Field: "ide_key", Err: errors.New("required")}
	}
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenant.ID))
	if err != nil {
		return 0, err
	}
	res := tx.Where("ide_key = ?", ideKey).Delete(&storage.TenantRedirectURIAllowlist{})
	if res.Error != nil {
		_ = commit()
		return 0, fmt.Errorf("tenant: delete preset rows: %w", res.Error)
	}
	if err := commit(); err != nil {
		return 0, err
	}
	return int(res.RowsAffected), nil
}

func normaliseLabel(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", &ErrAllowlistEntryInvalid{Field: "label", Err: errors.New("required")}
	}
	if len(trimmed) > allowlistLabelMaxLen {
		return "", &ErrAllowlistEntryInvalid{Field: "label", Err: fmt.Errorf("must be \u2264%d characters", allowlistLabelMaxLen)}
	}
	for _, r := range trimmed {
		if unicode.IsControl(r) {
			return "", &ErrAllowlistEntryInvalid{Field: "label", Err: errors.New("must not contain control characters")}
		}
	}
	return trimmed, nil
}

func normalisePattern(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", &ErrAllowlistEntryInvalid{Field: "pattern", Err: errors.New("required")}
	}
	if err := oauthproxy.ValidateRedirectURIPattern(trimmed); err != nil {
		return "", &ErrAllowlistEntryInvalid{Field: "pattern", Err: err}
	}
	return trimmed, nil
}

func toAllowlistEntry(row storage.TenantRedirectURIAllowlist) AllowlistEntry {
	out := AllowlistEntry{
		PublicID:  row.PublicID,
		Label:     row.Label,
		Pattern:   row.Pattern,
		CreatedAt: row.CreatedAt.UTC().Unix(),
	}
	if row.IDEKey != nil {
		out.IDEKey = *row.IDEKey
	}
	return out
}

func toAllowlistEntries(rows []storage.TenantRedirectURIAllowlist) []AllowlistEntry {
	out := make([]AllowlistEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, toAllowlistEntry(r))
	}
	return out
}

// isUniqueViolation tests for Postgres SQLSTATE 23505 without a hard
// dependency on the pgconn package: GORM wraps the driver error and
// preserves the message, which always contains "duplicate key value".
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "duplicate key value") ||
		strings.Contains(err.Error(), "SQLSTATE 23505")
}

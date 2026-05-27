package storage

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// GetServiceAccount returns a live service account row for the tenant
// pinned on ctx (via WithTenant) by its public id.
func (s *Store) GetServiceAccount(ctx context.Context, tenantID int64, publicID string) (*ServiceAccount, error) {
	_ = tenantID // tenant is pinned on ctx by Session; RLS enforces isolation.
	tx, commit, err := s.Session(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage: open session: %w", err)
	}
	var sa ServiceAccount
	if err := tx.Preload("CreatedBy").Where("public_id = ?", publicID).First(&sa).Error; err != nil {
		_ = commit()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("storage: get service account: %w", err)
	}
	if err := commit(); err != nil {
		return nil, err
	}
	return &sa, nil
}

// UpdateServiceAccount persists changes to an existing service account.
func (s *Store) UpdateServiceAccount(ctx context.Context, sa *ServiceAccount) error {
	tx, commit, err := s.Session(ctx)
	if err != nil {
		return fmt.Errorf("storage: open session: %w", err)
	}
	if err := tx.Model(sa).Select("Name", "Description").Updates(map[string]any{
		"name":        sa.Name,
		"description": sa.Description,
	}).Error; err != nil {
		_ = commit()
		return fmt.Errorf("storage: update service account: %w", err)
	}
	return commit()
}

// ListUpstreamLinksByServiceAccount returns all upstream links for a
// given service account, preloading the associated upstream row.
func (s *Store) ListUpstreamLinksByServiceAccount(ctx context.Context, tenantID int64, serviceAccountID int64) ([]UpstreamLink, error) {
	_ = tenantID // tenant is pinned on ctx by Session; RLS enforces isolation.
	tx, commit, err := s.Session(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage: open session: %w", err)
	}
	var links []UpstreamLink
	if err := tx.Preload("Upstream").
		Where("service_account_id = ?", serviceAccountID).
		Find(&links).Error; err != nil {
		_ = commit()
		return nil, fmt.Errorf("storage: list upstream links by service account: %w", err)
	}
	if err := commit(); err != nil {
		return nil, err
	}
	return links, nil
}

// GetUpstreamLinkByServiceAccountAndUpstream returns the single link row
// matching the given service account and upstream internal IDs.
func (s *Store) GetUpstreamLinkByServiceAccountAndUpstream(ctx context.Context, tenantID int64, serviceAccountID int64, upstreamID int64) (*UpstreamLink, error) {
	_ = tenantID // tenant is pinned on ctx by Session; RLS enforces isolation.
	tx, commit, err := s.Session(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage: open session: %w", err)
	}
	var link UpstreamLink
	if err := tx.Where("service_account_id = ? AND upstream_id = ?", serviceAccountID, upstreamID).
		First(&link).Error; err != nil {
		_ = commit()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("storage: get upstream link: %w", err)
	}
	if err := commit(); err != nil {
		return nil, err
	}
	return &link, nil
}

// UpdateUpstreamLink persists changes to an existing upstream link.
func (s *Store) UpdateUpstreamLink(ctx context.Context, link *UpstreamLink) error {
	tx, commit, err := s.Session(ctx)
	if err != nil {
		return fmt.Errorf("storage: open session: %w", err)
	}
	if err := tx.Model(link).Select("Enabled").Updates(map[string]any{
		"enabled": link.Enabled,
	}).Error; err != nil {
		_ = commit()
		return fmt.Errorf("storage: update upstream link: %w", err)
	}
	return commit()
}

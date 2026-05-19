package storage

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// ErrZitadelAppNotFound is returned when no live row matches (tenant_id,
// public_id). Soft-deleted rows are treated as not-found so the portal
// shows an idempotent "already revoked" experience.
var ErrZitadelAppNotFound = errors.New("storage: zitadel_app not found")

// ListZitadelAppsByTenant returns all live (non-soft-deleted) MCP client
// rows for the tenant pinned on ctx (via WithTenant). RLS enforces
// isolation in addition to the explicit tenant_id filter — both layers
// are intentional: belt-and-braces against any future bug that loses
// the GUC mid-tx.
func (s *Store) ListZitadelAppsByTenant(ctx context.Context, tenantID int64) ([]*ZitadelApp, error) {
	tx, commit, err := s.Session(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage: open session: %w", err)
	}
	var rows []*ZitadelApp
	if err := tx.Where("tenant_id = ?", tenantID).
		Order("created_at ASC").
		Find(&rows).Error; err != nil {
		_ = commit()
		return nil, fmt.Errorf("storage: list zitadel_apps: %w", err)
	}
	if err := commit(); err != nil {
		return nil, err
	}
	return rows, nil
}

// LoadZitadelAppByPublicID returns the live row for (tenant_id,
// public_id). Used by RevokeMCPClient before issuing the Zitadel call so
// we have ZitadelProjectID + ZitadelAppID without trusting client input.
func (s *Store) LoadZitadelAppByPublicID(ctx context.Context, tenantID int64, publicID string) (*ZitadelApp, error) {
	tx, commit, err := s.Session(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage: open session: %w", err)
	}
	var row ZitadelApp
	if err := tx.Where("tenant_id = ? AND public_id = ?", tenantID, publicID).
		First(&row).Error; err != nil {
		_ = commit()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrZitadelAppNotFound
		}
		return nil, fmt.Errorf("storage: load zitadel_app: %w", err)
	}
	if err := commit(); err != nil {
		return nil, err
	}
	return &row, nil
}

// SoftDeleteZitadelApp marks the row as deleted by stamping deleted_at.
// Returns ErrZitadelAppNotFound when no live row matches the
// (tenant_id, public_id) pair — keeps revocation idempotent.
func (s *Store) SoftDeleteZitadelApp(ctx context.Context, tenantID int64, publicID string) error {
	tx, commit, err := s.Session(ctx)
	if err != nil {
		return fmt.Errorf("storage: open session: %w", err)
	}
	res := tx.Where("tenant_id = ? AND public_id = ?", tenantID, publicID).
		Delete(&ZitadelApp{})
	if res.Error != nil {
		_ = commit()
		return fmt.Errorf("storage: soft-delete zitadel_app: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		_ = commit()
		return ErrZitadelAppNotFound
	}
	return commit()
}

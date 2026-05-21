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
// rows for the tenant pinned on ctx (via WithTenant). Tenant isolation
// is enforced by the RLS policy on zitadel_apps via the app.current_tenant
// GUC set by Session(ctx); no explicit tenant_id predicate is needed.
func (s *Store) ListZitadelAppsByTenant(ctx context.Context, tenantID int64) ([]*ZitadelApp, error) {
	_ = tenantID // tenant is pinned on ctx by Session; RLS enforces isolation.
	tx, commit, err := s.Session(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage: open session: %w", err)
	}
	var rows []*ZitadelApp
	if err := tx.Order("created_at ASC").
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
	_ = tenantID // tenant pinned on ctx; RLS scopes the SELECT
	var row ZitadelApp
	if err := tx.Where("public_id = ?", publicID).
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
	_ = tenantID // tenant pinned on ctx; RLS scopes the DELETE
	res := tx.Where("public_id = ?", publicID).
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

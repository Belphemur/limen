// Package tenant owns the lifecycle and settings operations for a
// Tenant: loading the per-tenant settings row, applying updates to
// the (Tenant, TenantSettings) pair atomically, and the soft-delete
// cascade that wipes every owned row.
//
// This package is the canonical home for the cross-table coordination
// admin RPCs need. Admin handlers must translate Connect requests
// into Service.<Op> calls — they must not touch GORM directly. The
// cascade list in Service.Delete is the single source of truth for
// "what does Limen own per tenant" and lives nowhere else.
package tenant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/oauthproxy"
	"github.com/belphemur/limen/internal/storage"
)

// ErrTenantNotFound is returned when the target tenant row has been
// soft-deleted or never existed.
var ErrTenantNotFound = errors.New("tenant: tenant not found")

// ErrConfirmationMismatch is returned by Delete when the caller's
// public_id confirmation does not match the tenant's PublicID.
var ErrConfirmationMismatch = errors.New("tenant: confirmation does not match tenant public_id")

// ErrAllowlistEntryInvalid wraps a per-entry validation failure from
// oauthproxy.ValidateRedirectURIPattern with the entry's index in the
// originally submitted slice so callers can pin SPA errors to the
// offending row.
type ErrAllowlistEntryInvalid struct {
	Index int
	Entry string
	Err   error
}

func (e *ErrAllowlistEntryInvalid) Error() string {
	return fmt.Sprintf("tenant: dcr_redirect_uri_allowlist[%d] %q: %v", e.Index, e.Entry, e.Err)
}

func (e *ErrAllowlistEntryInvalid) Unwrap() error { return e.Err }

// Service is the per-tenant lifecycle + settings coordinator.
type Service struct {
	store *storage.Store
}

// NewService builds a Service backed by store.
func NewService(store *storage.Store) *Service {
	return &Service{store: store}
}

// LoadSettings returns (settings, dcrAllowlist, zitadelOrgID). The
// settings row is created on first read so every later code path can
// assume the row exists.
func (s *Service) LoadSettings(ctx context.Context, tenant *storage.Tenant) (*storage.TenantSettings, []string, string, error) {
	if tenant == nil {
		return nil, nil, "", errors.New("tenant: nil tenant")
	}
	settings, err := s.loadOrCreateSettings(ctx, tenant.ID)
	if err != nil {
		return nil, nil, "", err
	}
	return settings, append([]string(nil), tenant.DCRRedirectURIAllowlist...), tenant.ZitadelOrgID, nil
}

// UpdateSettingsInput drives Service.UpdateSettings. Every field
// follows the convention "absent = leave alone, present = apply".
// AllowlistSet is the explicit sentinel that distinguishes "clear the
// list" (empty slice + true) from "no change" (nil slice + false).
type UpdateSettingsInput struct {
	// Name nil = leave; non-nil empty rejected as invalid_argument.
	Name *string
	// SetInvitedTeamAt flips InvitedTeamAt from NULL to now() exactly
	// once. Subsequent true values are no-ops.
	SetInvitedTeamAt bool
	SetConfiguredAt  bool

	// DCRRedirectURIAllowlist is honoured only when AllowlistSet
	// is true. The slice is replaced wholesale; the writer is the only
	// path that touches Tenant.DCRRedirectURIAllowlist via this RPC.
	DCRRedirectURIAllowlist []string
	AllowlistSet            bool
}

// UpdateSettings applies in to (Tenant, TenantSettings) inside a
// single transaction. Returns the post-update (settings, allowlist).
//
// Validation: empty Name (when set) → fmt.Errorf wrapping
// gorm.ErrInvalidValue (admin handler maps to invalid_argument).
// Each allowlist entry passes through
// oauthproxy.ValidateRedirectURIPattern; a failure is wrapped in
// *ErrAllowlistEntryInvalid so the handler can build the field path
// detail.
func (s *Service) UpdateSettings(ctx context.Context, tenant *storage.Tenant, in UpdateSettingsInput) (*storage.TenantSettings, []string, error) {
	if tenant == nil {
		return nil, nil, errors.New("tenant: nil tenant")
	}
	if in.Name != nil {
		trimmed := strings.TrimSpace(*in.Name)
		if trimmed == "" {
			return nil, nil, fmt.Errorf("tenant: name must not be empty: %w", gorm.ErrInvalidValue)
		}
		in.Name = &trimmed
	}
	if in.AllowlistSet {
		for i, raw := range in.DCRRedirectURIAllowlist {
			entry := strings.TrimSpace(raw)
			if entry == "" {
				return nil, nil, &ErrAllowlistEntryInvalid{Index: i, Entry: raw, Err: errors.New("entry is empty")}
			}
			if err := oauthproxy.ValidateRedirectURIPattern(entry); err != nil {
				return nil, nil, &ErrAllowlistEntryInvalid{Index: i, Entry: entry, Err: err}
			}
			in.DCRRedirectURIAllowlist[i] = entry
		}
	}

	// Ensure the settings row exists before opening the write tx; the
	// lazy-create path needs its own commit semantics.
	if _, err := s.loadOrCreateSettings(ctx, tenant.ID); err != nil {
		return nil, nil, err
	}

	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenant.ID))
	if err != nil {
		return nil, nil, err
	}

	now := time.Now().UTC()

	// Load the live Tenant row so we can mutate scalar fields and let
	// GORM's serializer:json handle the allowlist column on Save.
	// (Map-based Updates() bypass per-field serializers, which is why
	// we don't go that route here.)
	var refreshed storage.Tenant
	if err := tx.Where("id = ?", tenant.ID).First(&refreshed).Error; err != nil {
		_ = commit()
		return nil, nil, fmt.Errorf("tenant: load tenant: %w", err)
	}
	tenantDirty := false
	if in.Name != nil && refreshed.Name != *in.Name {
		refreshed.Name = *in.Name
		tenantDirty = true
	}
	if in.AllowlistSet {
		list := in.DCRRedirectURIAllowlist
		if list == nil {
			list = []string{}
		}
		refreshed.DCRRedirectURIAllowlist = list
		tenantDirty = true
	}
	if tenantDirty {
		if err := tx.Save(&refreshed).Error; err != nil {
			_ = commit()
			return nil, nil, fmt.Errorf("tenant: update tenant: %w", err)
		}
	}

	settingsUpdates := map[string]any{}
	var settings storage.TenantSettings
	if err := tx.Where("tenant_id = ?", tenant.ID).First(&settings).Error; err != nil {
		_ = commit()
		return nil, nil, fmt.Errorf("tenant: load settings: %w", err)
	}
	if in.SetInvitedTeamAt && settings.InvitedTeamAt == nil {
		settingsUpdates["invited_team_at"] = now
	}
	if in.SetConfiguredAt && settings.ConfiguredAt == nil {
		settingsUpdates["configured_at"] = now
	}
	if len(settingsUpdates) > 0 {
		if err := tx.Model(&storage.TenantSettings{}).Where("id = ?", settings.ID).
			Updates(settingsUpdates).Error; err != nil {
			_ = commit()
			return nil, nil, fmt.Errorf("tenant: update settings: %w", err)
		}
	}

	// Reload settings to get final state from a single source.
	if err := tx.Where("tenant_id = ?", tenant.ID).First(&settings).Error; err != nil {
		_ = commit()
		return nil, nil, fmt.Errorf("tenant: reload settings: %w", err)
	}
	if err := commit(); err != nil {
		return nil, nil, err
	}

	// Mirror the freshly-written name/allowlist onto the caller's
	// pointer so any downstream consumer sees the same state.
	tenant.Name = refreshed.Name
	tenant.DCRRedirectURIAllowlist = append([]string(nil), refreshed.DCRRedirectURIAllowlist...)
	return &settings, append([]string(nil), refreshed.DCRRedirectURIAllowlist...), nil
}

// Delete soft-deletes the tenant and every owned row inside a single
// admin-pool transaction. confirmationPublicID MUST match
// tenant.PublicID verbatim; mismatches return ErrConfirmationMismatch
// with no DB mutation. Idempotent on already-deleted tenants.
//
// Cascade order — keep this list in sync with internal/storage models:
//
//  1. upstream_tools                          (owned by upstream_links)
//  2. upstream_links / upstream_strategy_configs / upstream_registrations
//  3. upstreams
//  4. zitadel_apps
//  5. users
//  6. tenant_settings
//  7. tenants                                  (last; FK from above)
//
// We do not touch the Zitadel org — Limen does not own its lifecycle.
func (s *Service) Delete(ctx context.Context, tenant *storage.Tenant, confirmationPublicID string) error {
	if tenant == nil {
		return errors.New("tenant: nil tenant")
	}
	if strings.TrimSpace(confirmationPublicID) == "" || confirmationPublicID != tenant.PublicID {
		return ErrConfirmationMismatch
	}

	// Use the admin pool so we can cascade across every owned table in
	// one tx without juggling per-table RLS pins. The tenant id is
	// supplied explicitly in every WHERE — the bypass is purely a
	// transactional convenience.
	tx, commit, err := s.store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		return err
	}

	// Soft-delete order: leaf tables first so triggers / ON DELETE
	// constraints don't fight us if any are added later.
	cascades := []func() error{
		func() error {
			return tx.Where("tenant_id = ?", tenant.ID).Delete(&storage.UpstreamTool{}).Error
		},
		func() error {
			return tx.Where("tenant_id = ?", tenant.ID).Delete(&storage.UpstreamLink{}).Error
		},
		func() error {
			return tx.Where("tenant_id = ?", tenant.ID).Delete(&storage.UpstreamStrategyConfig{}).Error
		},
		func() error {
			return tx.Where("tenant_id = ?", tenant.ID).Delete(&storage.UpstreamRegistration{}).Error
		},
		func() error {
			return tx.Where("tenant_id = ?", tenant.ID).Delete(&storage.Upstream{}).Error
		},
		func() error {
			return tx.Where("tenant_id = ?", tenant.ID).Delete(&storage.ZitadelApp{}).Error
		},
		func() error {
			return tx.Where("tenant_id = ?", tenant.ID).Delete(&storage.User{}).Error
		},
		func() error {
			return tx.Where("tenant_id = ?", tenant.ID).Delete(&storage.TenantSettings{}).Error
		},
		func() error {
			return tx.Where("id = ?", tenant.ID).Delete(&storage.Tenant{}).Error
		},
	}
	for i, step := range cascades {
		if err := step(); err != nil {
			_ = commit()
			return fmt.Errorf("tenant: delete cascade step %d: %w", i, err)
		}
	}
	return commit()
}

// loadOrCreateSettings returns the tenant_settings row, creating an
// empty row on first read. Uses the tenant-scoped pool so RLS gives
// belt-and-braces isolation alongside the explicit tenant_id filter.
func (s *Service) loadOrCreateSettings(ctx context.Context, tenantID int64) (*storage.TenantSettings, error) {
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenantID))
	if err != nil {
		return nil, err
	}
	var row storage.TenantSettings
	err = tx.Where("tenant_id = ?", tenantID).First(&row).Error
	if err == nil {
		if cErr := commit(); cErr != nil {
			return nil, cErr
		}
		return &row, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		_ = commit()
		return nil, fmt.Errorf("tenant: load settings: %w", err)
	}
	row = storage.TenantSettings{TenantID: tenantID}
	if err := tx.Create(&row).Error; err != nil {
		_ = commit()
		return nil, fmt.Errorf("tenant: create settings: %w", err)
	}
	if cErr := commit(); cErr != nil {
		return nil, cErr
	}
	return &row, nil
}

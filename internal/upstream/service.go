// Package upstream — Service exposes the Phase 7 outbound-link operations
// as plain Go methods. Phase 9's portal Connect-RPC layer wraps these;
// Phase 7 itself only ships one HTTP route: the /callback redirect URI
// that upstream Authorization Servers redirect to after the user
// authorizes (mounted via transport).
package upstream

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/storage"
)

// ErrUpstreamNotFound is returned when the named upstream doesn't exist
// for the caller's tenant.
var ErrUpstreamNotFound = errors.New("upstream: not found")

// ErrLinkNotFound is returned when no UpstreamLink row exists for the
// (tenant, user, upstream) tuple.
var ErrLinkNotFound = errors.New("upstream: link not found")

// Service is the in-process facade over the strategy registry. Callers
// give us the upstream's name (per-tenant unique) and we route to the
// right Strategy.
type Service struct {
	store    *storage.Store
	registry *Registry
}

// NewService builds the service.
func NewService(store *storage.Store, registry *Registry) *Service {
	return &Service{store: store, registry: registry}
}

// StartConnect resolves the upstream + strategy for (tenant, user) and
// returns the URL the SPA should redirect the user to.
func (s *Service) StartConnect(ctx context.Context, tenant *storage.Tenant, user *storage.User, upstreamName, returnTo string) (string, error) {
	if tenant == nil || user == nil {
		return "", errors.New("upstream: tenant/user required")
	}
	up, err := s.loadUpstream(ctx, tenant.ID, upstreamName)
	if err != nil {
		return "", err
	}
	strat, err := s.registry.Resolve(StrategyType(up.StrategyType))
	if err != nil {
		return "", err
	}
	link, _ := s.loadLink(ctx, tenant.ID, user.ID, up.ID) // best-effort; may be nil
	lctx := LinkContext{
		Tenant:   tenant,
		User:     user,
		Upstream: up,
		Link:     link,
		ReturnTo: returnTo,
	}
	if err := strat.Provision(ctx, lctx); err != nil {
		return "", err
	}
	result, err := strat.StartLink(ctx, lctx)
	if err != nil {
		return "", err
	}
	return result.RedirectURL, nil
}

// FinishCallback drives FinishLink on the appropriate strategy. Returns
// the ReturnTo URL the SPA navigates to after the redirect lands.
func (s *Service) FinishCallback(ctx context.Context, tenant *storage.Tenant, user *storage.User, upstreamName, callbackQuery string) (string, error) {
	if tenant == nil || user == nil {
		return "", errors.New("upstream: tenant/user required")
	}
	up, err := s.loadUpstream(ctx, tenant.ID, upstreamName)
	if err != nil {
		return "", err
	}
	strat, err := s.registry.Resolve(StrategyType(up.StrategyType))
	if err != nil {
		return "", err
	}
	lctx := LinkContext{Tenant: tenant, User: user, Upstream: up}
	if err := strat.FinishLink(ctx, lctx, callbackQuery); err != nil {
		return "", err
	}
	return "", nil
}

// Disconnect soft-deletes the (tenant, user, upstream) link row. The
// strategy registry isn't involved — every strategy treats "delete the
// link" identically.
func (s *Service) Disconnect(ctx context.Context, tenant *storage.Tenant, user *storage.User, upstreamName string) error {
	if tenant == nil || user == nil {
		return errors.New("upstream: tenant/user required")
	}
	up, err := s.loadUpstream(ctx, tenant.ID, upstreamName)
	if err != nil {
		return err
	}
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenant.ID))
	if err != nil {
		return err
	}
	if err := tx.Where("tenant_id = ? AND user_id = ? AND upstream_id = ?", tenant.ID, user.ID, up.ID).
		Delete(&storage.UpstreamLink{}).Error; err != nil {
		_ = commit()
		return fmt.Errorf("upstream: delete link: %w", err)
	}
	return commit()
}

// LoadUpstream returns the Upstream row for (tenant, name). Public so
// the /callback HTTP handler can resolve the route before calling
// FinishCallback (we need the upstream + link to build LinkContext).
func (s *Service) LoadUpstream(ctx context.Context, tenantID int64, name string) (*storage.Upstream, error) {
	return s.loadUpstream(ctx, tenantID, name)
}

// ListUpstreams returns every (non-deleted) Upstream row for the tenant,
// ordered by name. Used by the portal PoC to render a connect/disconnect
// table; Phase 9's Connect-RPC will expose a richer shape.
func (s *Service) ListUpstreams(ctx context.Context, tenantID int64) ([]storage.Upstream, error) {
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenantID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = commit() }()

	var ups []storage.Upstream
	if err := tx.Where("tenant_id = ?", tenantID).Order("name ASC").Find(&ups).Error; err != nil {
		return nil, fmt.Errorf("upstream: list: %w", err)
	}
	return ups, nil
}

// LoadLink returns the UpstreamLink for (tenant, user, upstream) or
// ErrLinkNotFound. Public so the per-request roundtripper (Phase 8) can
// pull the link without re-running the resolution logic.
func (s *Service) LoadLink(ctx context.Context, tenantID, userID, upstreamID int64) (*storage.UpstreamLink, error) {
	return s.loadLink(ctx, tenantID, userID, upstreamID)
}

// IndexCatalog reconciles upstream_tools for up using whatever
// credentials the strategy makes available. The caller is responsible
// for enforcing the "first admin/owner to link bootstraps the catalog"
// rule from Phase 8 — Service has no view of OIDC roles. Pass link=nil
// for tenant-mode strategies (`none`, `static_header` tenant-wide).
func (s *Service) IndexCatalog(ctx context.Context, tenant *storage.Tenant, up *storage.Upstream, link *storage.UpstreamLink) error {
	return IndexUpstream(ctx, s.store, s.registry, tenant, up, link)
}

// ProvisionTenantMode runs the strategy's Provision step and then
// attempts a synchronous catalog index without a user link. It's safe
// to call for any strategy:
//
//   - For `none` and `static_header` in tenant mode the index succeeds
//     because the strategy's Headers() returns either no headers or the
//     tenant-wide secret.
//   - For `mcp_spec` and `static_header` in user mode Headers() returns
//     ErrNeedsRelink / ErrLinkNotFound; the indexer call is swallowed so
//     the caller (CLI / admin SPA CreateUpstream) doesn't fail \u2014 the
//     catalog will be filled in by the first user that links.
//
// Other errors (Provision rejection, transport failures, malformed
// strategy config) are surfaced.
func (s *Service) ProvisionTenantMode(ctx context.Context, tenant *storage.Tenant, up *storage.Upstream) error {
	if tenant == nil || up == nil {
		return errors.New("upstream: tenant/upstream required")
	}
	strat, err := s.registry.Resolve(StrategyType(up.StrategyType))
	if err != nil {
		return err
	}
	lctx := LinkContext{Tenant: tenant, Upstream: up}
	if err := strat.Provision(ctx, lctx); err != nil {
		return fmt.Errorf("upstream: provision: %w", err)
	}
	if err := IndexUpstream(ctx, s.store, s.registry, tenant, up, nil); err != nil {
		if errors.Is(err, ErrNeedsRelink) || errors.Is(err, ErrLinkNotFound) {
			return nil
		}
		return err
	}
	return nil
}

func (s *Service) loadUpstream(ctx context.Context, tenantID int64, name string) (*storage.Upstream, error) {
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenantID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = commit() }()

	var up storage.Upstream
	if err := tx.Where("tenant_id = ? AND name = ?", tenantID, name).First(&up).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrUpstreamNotFound
		}
		return nil, fmt.Errorf("upstream: load: %w", err)
	}
	return &up, nil
}

func (s *Service) loadLink(ctx context.Context, tenantID, userID, upstreamID int64) (*storage.UpstreamLink, error) {
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenantID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = commit() }()

	var link storage.UpstreamLink
	if err := tx.Preload("User").
		Where("tenant_id = ? AND user_id = ? AND upstream_id = ?", tenantID, userID, upstreamID).
		First(&link).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrLinkNotFound
		}
		return nil, fmt.Errorf("upstream: load link: %w", err)
	}
	return &link, nil
}

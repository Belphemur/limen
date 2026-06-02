// Package upstream — Service exposes the Phase 7 outbound-link operations
// as plain Go methods. Phase 9b's portal Connect-RPC layer wraps these;
// Phase 7 itself only ships one HTTP route: the /callback redirect URI
// that upstream Authorization Servers redirect to after the user
// authorizes (mounted via transport).
package upstream

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/storage"
)

// ErrUpstreamNotFound is returned when the named upstream doesn't exist
// for the caller's tenant.
var ErrUpstreamNotFound = errors.New("upstream: not found")

// ErrLinkNotFound is returned when no UpstreamLink row exists for the
// (tenant, user, upstream) tuple.
var ErrLinkNotFound = errors.New("upstream: link not found")

// ErrConfigNotFound is returned when an upstream has no
// UpstreamStrategyConfig row (e.g. `none` strategy, or a strategy that
// hasn't been provisioned yet).
var ErrConfigNotFound = errors.New("upstream: strategy config not found")

// Service is the in-process facade over the strategy registry. Callers
// give us the upstream's name (per-tenant unique) and we route to the
// right Strategy.
type Service struct {
	store    *storage.Store
	registry *Registry
	logger   *zap.Logger
}

// NewService builds the service.
func NewService(store *storage.Store, registry *Registry, logger *zap.Logger) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Service{store: store, registry: registry, logger: logger}
}

// StartConnect resolves the upstream + strategy for (tenant, user) and
// returns the URL the SPA should redirect the user to.
func (s *Service) StartConnect(ctx context.Context, tenant *storage.Tenant, user *storage.User, upstreamPublicID, returnTo string) (string, error) {
	if tenant == nil || user == nil {
		return "", errors.New("upstream: tenant/user required")
	}
	s.logger.Debug("StartConnect",
		zap.Int64("tenant_id", tenant.ID),
		zap.String("public_id", upstreamPublicID),
		zap.String("user_subject", user.ZitadelSubject))
	up, err := s.loadUpstreamByPublicID(ctx, tenant.ID, upstreamPublicID)
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
func (s *Service) FinishCallback(ctx context.Context, tenant *storage.Tenant, user *storage.User, upstreamPublicID, callbackQuery string) (string, error) {
	if tenant == nil || user == nil {
		return "", errors.New("upstream: tenant/user required")
	}
	up, err := s.loadUpstreamByPublicID(ctx, tenant.ID, upstreamPublicID)
	if err != nil {
		return "", err
	}
	strat, err := s.registry.Resolve(StrategyType(up.StrategyType))
	if err != nil {
		return "", err
	}
	lctx := LinkContext{Tenant: tenant, User: user, Upstream: up}
	returnTo, err := strat.FinishLink(ctx, lctx, callbackQuery)
	if err != nil {
		return "", err
	}
	return returnTo, nil
}

// Disconnect soft-deletes the (tenant, user, upstream) link row. The
// strategy registry isn't involved — every strategy treats "delete the
// link" identically.
func (s *Service) Disconnect(ctx context.Context, tenant *storage.Tenant, user *storage.User, upstreamPublicID string) error {
	if tenant == nil || user == nil {
		return errors.New("upstream: tenant/user required")
	}
	up, err := s.loadUpstreamByPublicID(ctx, tenant.ID, upstreamPublicID)
	if err != nil {
		return err
	}
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenant.ID))
	if err != nil {
		return err
	}
	if err := tx.Where("user_id = ? AND upstream_id = ?", user.ID, up.ID).
		Delete(&storage.UpstreamLink{}).Error; err != nil {
		_ = commit()
		return fmt.Errorf("upstream: delete link: %w", err)
	}
	return commit()
}

// LoadUpstream returns the Upstream row for (tenant, identifier).
// This is the gateway code-mode path — it resolves by identifier so
// the codemode namespace (codemode.<identifier>.<tool>()) stays readable.
// All RPC paths use LoadUpstreamByPublicID.
func (s *Service) LoadUpstream(ctx context.Context, tenantID int64, identifier string) (*storage.Upstream, error) {
	return s.loadUpstreamByIdentifier(ctx, tenantID, identifier)
}

// LoadUpstreamByPublicID resolves an upstream by its public_id for the given tenant.
func (s *Service) LoadUpstreamByPublicID(ctx context.Context, tenantID int64, publicID string) (*storage.Upstream, error) {
	return s.loadUpstreamByPublicID(ctx, tenantID, publicID)
}

// ListUpstreams returns every (non-deleted) Upstream row for the tenant,
// ordered by identifier. Used by the portal PoC to render a connect/disconnect
// table; Phase 9b's Connect-RPC will expose a richer shape.
func (s *Service) ListUpstreams(ctx context.Context, tenantID int64) ([]storage.Upstream, error) {
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenantID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = commit() }()

	var ups []storage.Upstream
	if err := tx.Order("identifier ASC").Find(&ups).Error; err != nil {
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
	return IndexUpstream(ctx, s.store, s.registry, tenant, up, link, nil, nil)
}

// VerifyLink confirms the credentials this link produces are accepted
// by the upstream MCP server by performing a single `initialize`
// round-trip. Returns nil on success, the raw transport error on any
// failure (401, 404, 5xx, network). Callers treat any non-nil return
// as "this link does not currently work" — the admin can retry.
//
// Some authorization servers (PayPal, observed) hand back a token
// even when the user refused consent; the AS round-trip looks
// successful but the resource server then rejects every call. This
// probe is the only reliable way to distinguish a real link from a
// consent-refused stub.
//
// For strategies that don't carry per-user links (`none`,
// `static_header` tenant-wide) this is a no-op returning nil.
func (s *Service) VerifyLink(ctx context.Context, tenant *storage.Tenant, up *storage.Upstream, link *storage.UpstreamLink) error {
	if tenant == nil || up == nil {
		return errors.New("upstream: tenant/upstream required")
	}
	strat, err := s.registry.Resolve(StrategyType(up.StrategyType))
	if err != nil {
		return err
	}
	if !strat.RequiresLink() {
		return nil
	}
	lctx := LinkContext{Tenant: tenant, Upstream: up, Link: link}
	if link != nil {
		lctx.User = link.User
	}
	headers, err := strat.Headers(ctx, lctx)
	if err != nil {
		return err
	}
	dialCtx, cancel := context.WithTimeout(ctx, indexTimeout)
	defer cancel()
	c, err := DialAndInitialize(dialCtx, up.McpServerURL, headers, nil, indexTimeout, "limen-link-verify", "0.1.0", up.Identifier)
	if err != nil {
		return err
	}
	_ = c.Close()
	return nil
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
	if err := IndexUpstream(ctx, s.store, s.registry, tenant, up, nil, nil, nil); err != nil {
		if errors.Is(err, ErrNeedsRelink) || errors.Is(err, ErrLinkNotFound) {
			return nil
		}
		return err
	}
	return nil
}

func (s *Service) loadUpstreamByIdentifier(ctx context.Context, tenantID int64, identifier string) (*storage.Upstream, error) {
	s.logger.Debug("loadUpstreamByIdentifier: starting query",
		zap.Int64("tenant_id", tenantID),
		zap.String("identifier", identifier))

	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenantID))
	if err != nil {
		s.logger.Debug("loadUpstreamByIdentifier: session failed", zap.Error(err))
		return nil, err
	}
	defer func() { _ = commit() }()

	var up storage.Upstream
	if err := tx.Where("identifier = ?", identifier).First(&up).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.Debug("loadUpstreamByIdentifier: not found",
				zap.Int64("tenant_id", tenantID),
				zap.String("identifier", identifier))
			return nil, ErrUpstreamNotFound
		}
		s.logger.Debug("loadUpstreamByIdentifier: query error", zap.Error(err))
		return nil, fmt.Errorf("upstream: load: %w", err)
	}
	s.logger.Debug("loadUpstreamByIdentifier: found",
		zap.Int64("tenant_id", tenantID),
		zap.String("identifier", identifier),
		zap.Int64("upstream_id", up.ID),
		zap.Int64("upstream_tenant_id", up.TenantID))
	return &up, nil
}

func (s *Service) loadTenantLink(ctx context.Context, tenantID, upstreamID int64) (*storage.UpstreamTenantLink, error) {
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenantID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = commit() }()

	var link storage.UpstreamTenantLink
	if err := tx.Where("tenant_id = ? AND upstream_id = ?", tenantID, upstreamID).First(&link).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrLinkNotFound
		}
		return nil, fmt.Errorf("upstream: load tenant link: %w", err)
	}
	return &link, nil
}

func (s *Service) loadLink(ctx context.Context, tenantID, userID, upstreamID int64) (*storage.UpstreamLink, error) {
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenantID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = commit() }()

	var link storage.UpstreamLink
	if err := tx.Preload("User").
		Where("user_id = ? AND upstream_id = ?", userID, upstreamID).
		First(&link).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrLinkNotFound
		}
		return nil, fmt.Errorf("upstream: load link: %w", err)
	}
	return &link, nil
}

// loadLinkByOwner loads an UpstreamLink by either UserID or ServiceAccountID,
// depending on which is non-nil. Returns ErrLinkNotFound when no match exists.
func (s *Service) loadLinkByOwner(ctx context.Context, tenantID int64, lctx LinkContext, upstreamID int64) (*storage.UpstreamLink, error) {
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenantID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = commit() }()

	var link storage.UpstreamLink
	query := tx
	if lctx.IsServiceAccount() {
		query = query.Where("service_account_id = ? AND upstream_id = ?", *lctx.ServiceAccountID, upstreamID)
	} else {
		query = query.Preload("User").Where("user_id = ? AND upstream_id = ?", lctx.User.ID, upstreamID)
	}
	if err := query.First(&link).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrLinkNotFound
		}
		return nil, fmt.Errorf("upstream: load link: %w", err)
	}
	return &link, nil
}

// DisconnectByOwner soft-deletes the link row for either a user or service
// account, depending on the LinkContext.
func (s *Service) DisconnectByOwner(ctx context.Context, tenant *storage.Tenant, lctx LinkContext, upstreamPublicID string) error {
	if tenant == nil {
		return errors.New("upstream: tenant required")
	}
	if !lctx.IsServiceAccount() && lctx.User == nil {
		return errors.New("upstream: user or service account required")
	}
	up, err := s.loadUpstreamByPublicID(ctx, tenant.ID, upstreamPublicID)
	if err != nil {
		return err
	}
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenant.ID))
	if err != nil {
		return err
	}
	query := tx
	if lctx.IsServiceAccount() {
		query = query.Where("service_account_id = ? AND upstream_id = ?", *lctx.ServiceAccountID, up.ID)
	} else {
		query = query.Where("user_id = ? AND upstream_id = ?", lctx.User.ID, up.ID)
	}
	if err := query.Delete(&storage.UpstreamLink{}).Error; err != nil {
		_ = commit()
		return fmt.Errorf("upstream: delete link: %w", err)
	}
	return commit()
}

// StartConnectForServiceAccount resolves the upstream + strategy and returns
// the OAuth URL for a service account link. The admin user is the initiator
// (for state AAD); the SA is the link target.
func (s *Service) StartConnectForServiceAccount(ctx context.Context, tenant *storage.Tenant, adminUser *storage.User, serviceAccountID int64, upstreamPublicID, returnTo string) (string, error) {
	if tenant == nil || adminUser == nil {
		return "", errors.New("upstream: tenant/user required")
	}
	up, err := s.loadUpstreamByPublicID(ctx, tenant.ID, upstreamPublicID)
	if err != nil {
		return "", err
	}
	strat, err := s.registry.Resolve(StrategyType(up.StrategyType))
	if err != nil {
		return "", err
	}
	saID := serviceAccountID
	lctx := LinkContext{
		Tenant:           tenant,
		User:             adminUser,
		ServiceAccountID: &saID,
		Upstream:         up,
		ReturnTo:         returnTo,
	}
	// Try loading existing SA link
	link, _ := s.loadLinkByOwner(ctx, tenant.ID, lctx, up.ID)
	lctx.Link = link
	if err := strat.Provision(ctx, lctx); err != nil {
		return "", err
	}
	result, err := strat.StartLink(ctx, lctx)
	if err != nil {
		return "", err
	}
	return result.RedirectURL, nil
}

// PersistServiceAccountStaticHeaderSecret routes to a static_header strategy's
// service-account-override secret persistence. Returns ErrUnsupported when the
// upstream is not `static_header` or when the admin has not enabled override.
func (s *Service) PersistServiceAccountStaticHeaderSecret(ctx context.Context, tenant *storage.Tenant, adminUser *storage.User, serviceAccountID int64, upstreamPublicID, secret string) error {
	if tenant == nil || adminUser == nil {
		return errors.New("upstream: tenant/user required")
	}
	up, err := s.loadUpstreamByPublicID(ctx, tenant.ID, upstreamPublicID)
	if err != nil {
		return err
	}
	strat, err := s.registry.Resolve(StrategyType(up.StrategyType))
	if err != nil {
		return err
	}
	p, ok := strat.(secretPersister)
	if !ok {
		return ErrUnsupported
	}
	saID := serviceAccountID
	lctx := LinkContext{Tenant: tenant, User: adminUser, ServiceAccountID: &saID, Upstream: up}
	return p.PersistUserSecret(ctx, lctx, secret)
}

// ClearServiceAccountStaticHeaderOverride drops the service account's override
// secret on a `static_header` link. Returns ErrUnsupported when the strategy is
// not `static_header`.
func (s *Service) ClearServiceAccountStaticHeaderOverride(ctx context.Context, tenant *storage.Tenant, adminUser *storage.User, serviceAccountID int64, upstreamPublicID string) error {
	if tenant == nil || adminUser == nil {
		return errors.New("upstream: tenant/user required")
	}
	up, err := s.loadUpstreamByPublicID(ctx, tenant.ID, upstreamPublicID)
	if err != nil {
		return err
	}
	strat, err := s.registry.Resolve(StrategyType(up.StrategyType))
	if err != nil {
		return err
	}
	c, ok := strat.(secretClearer)
	if !ok {
		return ErrUnsupported
	}
	saID := serviceAccountID
	lctx := LinkContext{Tenant: tenant, User: adminUser, ServiceAccountID: &saID, Upstream: up}
	return c.ClearUserOverride(ctx, lctx)
}

// StartTenantConnect starts the admin OAuth flow for tenant-level credentials.
func (s *Service) StartTenantConnect(ctx context.Context, tenant *storage.Tenant, admin *storage.User, upstreamPublicID, returnTo string) (string, error) {
	if tenant == nil || admin == nil {
		return "", errors.New("upstream: tenant/admin required")
	}
	up, err := s.loadUpstreamByPublicID(ctx, tenant.ID, upstreamPublicID)
	if err != nil {
		return "", err
	}
	strat, err := s.registry.Resolve(StrategyType(up.StrategyType))
	if err != nil {
		return "", err
	}
	starter, ok := strat.(tenantLinkStarter)
	if !ok {
		return "", fmt.Errorf("upstream: strategy %q does not support tenant linking", up.StrategyType)
	}
	lctx := LinkContext{
		Tenant:   tenant,
		User:     admin,
		Upstream: up,
		ReturnTo: returnTo,
	}
	if err := strat.Provision(ctx, lctx); err != nil {
		return "", err
	}
	result, err := starter.StartTenantLink(ctx, lctx)
	if err != nil {
		return "", err
	}
	return result.RedirectURL, nil
}

// FinishTenantCallback completes the admin OAuth flow for tenant-level credentials.
func (s *Service) FinishTenantCallback(ctx context.Context, tenant *storage.Tenant, admin *storage.User, upstreamPublicID, callbackQuery string) (string, error) {
	if tenant == nil || admin == nil {
		return "", errors.New("upstream: tenant/admin required")
	}
	up, err := s.loadUpstreamByPublicID(ctx, tenant.ID, upstreamPublicID)
	if err != nil {
		return "", err
	}
	strat, err := s.registry.Resolve(StrategyType(up.StrategyType))
	if err != nil {
		return "", err
	}
	finisher, ok := strat.(tenantLinkFinisher)
	if !ok {
		return "", fmt.Errorf("upstream: strategy %q does not support tenant linking", up.StrategyType)
	}
	lctx := LinkContext{Tenant: tenant, User: admin, Upstream: up}
	returnTo, err := finisher.FinishTenantLink(ctx, lctx, callbackQuery)
	if err != nil {
		return "", err
	}
	return returnTo, nil
}

// SetSALinkEnabled flips UpstreamLink.Enabled for a service account link.
// Re-enabling an auto_disabled link ALSO clears the failure counters.
func (s *Service) SetSALinkEnabled(ctx context.Context, tenant *storage.Tenant, serviceAccountID int64, upstreamPublicID string, enabled bool) error {
	if tenant == nil {
		return errors.New("upstream: tenant required")
	}
	up, err := s.loadUpstreamByPublicID(ctx, tenant.ID, upstreamPublicID)
	if err != nil {
		return err
	}
	saID := serviceAccountID
	lctx := LinkContext{ServiceAccountID: &saID}
	link, err := s.loadLinkByOwner(ctx, tenant.ID, lctx, up.ID)
	if err != nil {
		return err
	}
	wasAutoDisabled := link.AutoDisabledAt != nil
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenant.ID))
	if err != nil {
		return err
	}
	if err := tx.Model(&storage.UpstreamLink{}).Where("id = ?", link.ID).Update("enabled", enabled).Error; err != nil {
		_ = commit()
		return fmt.Errorf("upstream: set enabled: %w", err)
	}
	if err := commit(); err != nil {
		return err
	}
	if enabled && wasAutoDisabled {
		if err := RecordSuccess(ctx, s.store, link.ID); err != nil {
			return fmt.Errorf("upstream: clear auto-disable: %w", err)
		}
	}
	return nil
}

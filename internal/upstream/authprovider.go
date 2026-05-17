// Package upstream — AuthProvider seam for the gateway's per-request
// transport.
//
// The gateway round-tripper needs two operations from "auth land":
//
//   - Headers(ctx): the canonical credentials to attach to the next
//     outgoing request. Cheap to call once per request.
//   - HeadersForceRefresh(ctx): a single retry path after a 401, which
//     drops any cached token and re-runs the strategy's refresh dance.
//
// AuthProvider abstracts that surface so the transport never imports
// strategies, the storage layer, or auth-context helpers. DBAuthProvider
// is the production impl: one provider per (tenant, upstream) pair.
//
// Why no caller-side caching: the strategy itself owns single-flight
// (mcpspec uses singleflight + SELECT FOR UPDATE SKIP LOCKED); a second
// layer here would only hide that. Headers does load the link on every
// call — that's an indexed PK lookup, dwarfed by the upstream RTT.
package upstream

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/storage"
)

// AuthResult is what AuthProvider.Headers returns. LinkID is the
// UpstreamLink primary key for per-user strategies; 0 for tenant-mode
// strategies (which have no link and therefore no per-link health
// bookkeeping).
type AuthResult struct {
	Headers map[string]string
	LinkID  int64
}

// AuthProvider supplies HTTP headers for outgoing calls to a single
// (tenant, upstream) pair. The gateway round-tripper calls Headers once
// per request; on a 401 it calls HeadersForceRefresh exactly once.
type AuthProvider interface {
	// Headers returns the authorization headers for ctx. Returns
	// ErrLinkNotFound when the strategy requires a per-user link and the
	// caller hasn't connected yet, and ErrNeedsRelink when the link is
	// past saving.
	Headers(ctx context.Context) (AuthResult, error)
	// HeadersForceRefresh drops any cached credential and re-runs the
	// strategy refresh path. Used by the round-tripper after a 401.
	HeadersForceRefresh(ctx context.Context) (AuthResult, error)
}

// UserResolver pulls the active user out of ctx. The indirection avoids
// an upstream→auth import cycle; callers wire `auth.MCPUserFromContext`.
type UserResolver func(ctx context.Context) (*storage.User, bool)

// ErrNoUser is returned when the strategy needs a link and the request
// ctx has no authenticated user.
var ErrNoUser = errors.New("upstream: no authenticated user on ctx")

// DBAuthProvider is the production AuthProvider. Pinned to a single
// (tenant, upstream) pair so the gateway can keep one per Bundle.
type DBAuthProvider struct {
	store        *storage.Store
	registry     *Registry
	tenant       *storage.Tenant
	upstream     *storage.Upstream
	strategy     Strategy
	userResolver UserResolver
}

// NewDBAuthProvider resolves the strategy once at construction. Returns
// an error if the upstream references an unknown strategy.
func NewDBAuthProvider(
	store *storage.Store,
	registry *Registry,
	tenant *storage.Tenant,
	up *storage.Upstream,
	resolver UserResolver,
) (*DBAuthProvider, error) {
	if store == nil || registry == nil || tenant == nil || up == nil {
		return nil, errors.New("upstream: NewDBAuthProvider: store/registry/tenant/upstream required")
	}
	strat, err := registry.Resolve(StrategyType(up.StrategyType))
	if err != nil {
		return nil, err
	}
	if strat.RequiresLink() && resolver == nil {
		return nil, fmt.Errorf("upstream: NewDBAuthProvider: strategy %q requires a UserResolver", up.StrategyType)
	}
	return &DBAuthProvider{
		store:        store,
		registry:     registry,
		tenant:       tenant,
		upstream:     up,
		strategy:     strat,
		userResolver: resolver,
	}, nil
}

// Headers builds a LinkContext for the resolved user (if any) and
// delegates to Strategy.Headers.
func (p *DBAuthProvider) Headers(ctx context.Context) (AuthResult, error) {
	lctx, err := p.linkContext(ctx)
	if err != nil {
		return AuthResult{}, err
	}
	hdrs, err := p.strategy.Headers(ctx, lctx)
	if err != nil {
		return AuthResult{}, err
	}
	return AuthResult{Headers: hdrs, LinkID: linkIDOf(lctx.Link)}, nil
}

// HeadersForceRefresh is the same path as Headers but with the
// strategy's force-refresh flag set.
func (p *DBAuthProvider) HeadersForceRefresh(ctx context.Context) (AuthResult, error) {
	lctx, err := p.linkContext(ctx)
	if err != nil {
		return AuthResult{}, err
	}
	hdrs, err := p.strategy.HeadersForceRefresh(ctx, lctx)
	if err != nil {
		return AuthResult{}, err
	}
	return AuthResult{Headers: hdrs, LinkID: linkIDOf(lctx.Link)}, nil
}

func linkIDOf(link *storage.UpstreamLink) int64 {
	if link == nil {
		return 0
	}
	return link.ID
}

// linkContext resolves the active user (when the strategy requires one),
// loads the link, decrypts the encrypted columns under the right AAD, and
// returns a LinkContext ready for the strategy.
func (p *DBAuthProvider) linkContext(ctx context.Context) (LinkContext, error) {
	lctx := LinkContext{Tenant: p.tenant, Upstream: p.upstream}
	if !p.strategy.RequiresLink() {
		return lctx, nil
	}

	user, ok := p.userResolver(ctx)
	if !ok || user == nil {
		return lctx, ErrNoUser
	}
	lctx.User = user

	link, err := p.loadLink(ctx, user.ID)
	if err != nil {
		return lctx, err
	}
	if !link.Enabled || link.AutoDisabledAt != nil {
		return lctx, ErrNeedsRelink
	}
	lctx.Link = link
	return lctx, nil
}

// loadLink fetches the (tenant, user, upstream) link and decrypts the
// encrypted columns under tenant/user AAD. Returns ErrLinkNotFound when
// the user hasn't connected this upstream.
func (p *DBAuthProvider) loadLink(ctx context.Context, userID int64) (*storage.UpstreamLink, error) {
	tx, commit, err := p.store.Session(storage.WithTenant(ctx, p.tenant.ID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = commit() }()

	tenantStr := strconv.FormatInt(p.tenant.ID, 10)
	userStr := strconv.FormatInt(userID, 10)

	var link storage.UpstreamLink
	link.AccessToken.SetAAD(tenantStr, userStr, "upstream.access_token")
	link.RefreshToken.SetAAD(tenantStr, userStr, "upstream.refresh_token")
	link.ExtraJSON.SetAAD(tenantStr, userStr, "upstream.extra")
	err = tx.Where("tenant_id = ? AND user_id = ? AND upstream_id = ?",
		p.tenant.ID, userID, p.upstream.ID).First(&link).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrLinkNotFound
		}
		return nil, fmt.Errorf("upstream: load link: %w", err)
	}
	return &link, nil
}

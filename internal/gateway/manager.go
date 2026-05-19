package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/upstream"
)

// ManagerOptions configures a Manager. All fields are required except
// Timeout (defaulted) and Logger (no-op fallback).
type ManagerOptions struct {
	Store            *storage.Store
	Service          *upstream.Service
	Registry         *upstream.Registry
	HealthThresholds upstream.HealthThresholds
	Timeout          time.Duration
	Logger           *zap.Logger
}

// Manager owns the per-(tenant, upstream) Bundle cache and serves the
// request-time API for the MCP gateway: ToolsForUser + CallTool.
//
// Bundles are built lazily on first access for a given (tenantID,
// upstreamName) pair. Mutations (admin SPA, CLI) must call Invalidate
// after committing so the next lookup rebuilds the bundle with the new
// row. Process restarts also clear the cache.
type Manager struct {
	opts ManagerOptions

	mu      sync.RWMutex
	bundles map[int64]map[string]*Bundle
}

// NewManager constructs a Manager. Returns an error if any required
// option is missing.
func NewManager(opts ManagerOptions) (*Manager, error) {
	if opts.Store == nil || opts.Service == nil || opts.Registry == nil {
		return nil, errors.New("gateway: NewManager: store/service/registry required")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	return &Manager{
		opts:    opts,
		bundles: make(map[int64]map[string]*Bundle),
	}, nil
}

// Invalidate drops the cached Bundle for (tenantID, upstreamName) so the
// next access rebuilds it. Idempotent.
func (m *Manager) Invalidate(tenantID int64, upstreamName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if perTenant, ok := m.bundles[tenantID]; ok {
		delete(perTenant, upstreamName)
		if len(perTenant) == 0 {
			delete(m.bundles, tenantID)
		}
	}
}

// InvalidateTenant drops every cached Bundle for a tenant. Used when an
// upstream is renamed or hard-deleted under it.
func (m *Manager) InvalidateTenant(tenantID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.bundles, tenantID)
}

// CallTool looks up the (tenant, upstream) Bundle from ctx and dispatches
// the tool call. ctx must carry both a tenant (set by tenancy middleware)
// and an authenticated MCP user (set by auth middleware) for strategies
// that require a per-user link.
func (m *Manager) CallTool(ctx context.Context, upstreamName, toolName string, args map[string]any) (any, error) {
	tenant, ok := tenancy.TenantFromContext(ctx)
	if !ok {
		return nil, errors.New("gateway: no tenant on ctx")
	}
	b, err := m.bundleFor(ctx, tenant, upstreamName)
	if err != nil {
		return nil, err
	}
	resp, err := b.CallTool(ctx, toolName, args)
	if err != nil {
		return nil, err
	}
	return resp.Content, nil
}

// ToolsForUser returns the catalog union the user on ctx can see:
// every tool from tenant-mode upstreams plus, for per-user strategies,
// every tool whose upstream the user has a healthy link to (enabled,
// not auto-disabled, not needs-relink).
func (m *Manager) ToolsForUser(ctx context.Context) ([]ToolEntry, error) {
	tenant, ok := tenancy.TenantFromContext(ctx)
	if !ok {
		return nil, errors.New("gateway: no tenant on ctx")
	}
	user, _ := auth.MCPUserFromContext(ctx)

	tx, commit, err := m.opts.Store.Session(storage.WithTenant(ctx, tenant.ID))
	if err != nil {
		return nil, fmt.Errorf("gateway: tools: open session: %w", err)
	}
	defer func() { _ = commit() }()

	// Load every upstream for the tenant.
	var ups []storage.Upstream
	if err := tx.Where("tenant_id = ? AND deleted_at IS NULL", tenant.ID).
		Order("name ASC").
		Find(&ups).Error; err != nil {
		return nil, fmt.Errorf("gateway: tools: list upstreams: %w", err)
	}

	// Pre-load this user's healthy links indexed by upstream_id.
	healthyLink := map[int64]bool{}
	if user != nil {
		var links []storage.UpstreamLink
		if err := tx.Where(`user_id = ? AND deleted_at IS NULL
				AND enabled = true
				AND auto_disabled_at IS NULL
				AND needs_relink = false`, user.ID).
			Find(&links).Error; err != nil {
			return nil, fmt.Errorf("gateway: tools: list links: %w", err)
		}
		for _, l := range links {
			healthyLink[l.UpstreamID] = true
		}
	}

	// Decide which upstream IDs the user can see.
	visibleByID := make(map[int64]*storage.Upstream, len(ups))
	for i := range ups {
		up := &ups[i]
		strat, err := m.opts.Registry.Resolve(upstream.StrategyType(up.StrategyType))
		if err != nil {
			m.opts.Logger.Warn("tools: skipping upstream with unknown strategy",
				zap.String("upstream", up.Name),
				zap.String("strategy", up.StrategyType),
				zap.Error(err))
			continue
		}
		if !strat.RequiresLink() {
			visibleByID[up.ID] = up
			continue
		}
		if healthyLink[up.ID] {
			visibleByID[up.ID] = up
		}
	}
	if len(visibleByID) == 0 {
		return nil, nil
	}

	// Fetch cached tool rows for visible upstreams.
	upstreamIDs := make([]int64, 0, len(visibleByID))
	for id := range visibleByID {
		upstreamIDs = append(upstreamIDs, id)
	}
	var rows []storage.UpstreamTool
	if err := tx.Where("upstream_id IN ? AND deleted_at IS NULL", upstreamIDs).
		Order("upstream_id ASC, name ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("gateway: tools: list catalog: %w", err)
	}

	out := make([]ToolEntry, 0, len(rows))
	for _, r := range rows {
		up := visibleByID[r.UpstreamID]
		if up == nil {
			continue
		}
		schema := map[string]any{}
		if len(r.InputSchemaJSON) > 0 {
			_ = json.Unmarshal(r.InputSchemaJSON, &schema)
		}
		out = append(out, ToolEntry{
			Name:        r.Name,
			Description: r.Description,
			InputSchema: schema,
			Upstream:    up.Name,
		})
	}
	return out, nil
}

// UpstreamView is one upstream's metadata as the codemode handler needs
// it: canonical name, derived aliases (post-collision-pass), and the
// already-merged ambient context object. Built by UpstreamsForUser.
type UpstreamView struct {
	Name    string
	Aliases []string
	Context map[string]any
}

// UpstreamsForUser returns metadata for every upstream visible to the
// user on ctx (same visibility rule as ToolsForUser). Aliases are
// loaded from upstream.aliases_json (recomputed by IndexUpstream) and
// then filtered by a tenant-wide collision pass: any alias claimed by
// more than one upstream is dropped from all claimants. Context is the
// shallow merge of UpstreamLink.ContextJSON over Upstream.DefaultsJSON
// — invalid stored JSON degrades to an empty {} with a single warn
// log so the catalog still loads.
func (m *Manager) UpstreamsForUser(ctx context.Context) ([]UpstreamView, error) {
	tenant, ok := tenancy.TenantFromContext(ctx)
	if !ok {
		return nil, errors.New("gateway: no tenant on ctx")
	}
	user, _ := auth.MCPUserFromContext(ctx)

	tx, commit, err := m.opts.Store.Session(storage.WithTenant(ctx, tenant.ID))
	if err != nil {
		return nil, fmt.Errorf("gateway: upstreams: open session: %w", err)
	}
	defer func() { _ = commit() }()

	var ups []storage.Upstream
	if err := tx.Where("tenant_id = ? AND deleted_at IS NULL", tenant.ID).
		Order("name ASC").
		Find(&ups).Error; err != nil {
		return nil, fmt.Errorf("gateway: upstreams: list: %w", err)
	}

	linkByUpstream := map[int64]*storage.UpstreamLink{}
	if user != nil {
		var links []storage.UpstreamLink
		if err := tx.Where(`user_id = ? AND deleted_at IS NULL
				AND enabled = true
				AND auto_disabled_at IS NULL
				AND needs_relink = false`, user.ID).
			Find(&links).Error; err != nil {
			return nil, fmt.Errorf("gateway: upstreams: list links: %w", err)
		}
		for i := range links {
			linkByUpstream[links[i].UpstreamID] = &links[i]
		}
	}

	// Visibility + per-upstream alias slice for the tenant-wide
	// collision pass. We must collect aliases from EVERY visible
	// upstream first; otherwise the collision pass would miss claims.
	visible := make([]*storage.Upstream, 0, len(ups))
	aliasesByName := make(map[string][]string, len(ups))
	for i := range ups {
		up := &ups[i]
		strat, err := m.opts.Registry.Resolve(upstream.StrategyType(up.StrategyType))
		if err != nil {
			m.opts.Logger.Warn("upstreams: skipping unknown strategy",
				zap.String("upstream", up.Name),
				zap.String("strategy", up.StrategyType),
				zap.Error(err))
			continue
		}
		if strat.RequiresLink() {
			if _, ok := linkByUpstream[up.ID]; !ok {
				continue
			}
		}
		visible = append(visible, up)
		aliasesByName[up.Name] = decodeAliasesJSON(up.AliasesJSON)
	}

	resolved, collisions := upstream.ResolveAliasCollisions(aliasesByName)
	if len(collisions) > 0 {
		m.opts.Logger.Warn("gateway.alias.collision",
			zap.Int64("tenant_id", tenant.ID),
			zap.Strings("dropped", collisions))
	}

	out := make([]UpstreamView, 0, len(visible))
	for _, up := range visible {
		defaults, ok := SafeLoadContextBlob(up.DefaultsJSON)
		if !ok {
			m.opts.Logger.Warn("gateway.context.invalid_json",
				zap.Int64("tenant_id", tenant.ID),
				zap.String("upstream", up.Name),
				zap.String("source", "upstream.defaults_json"))
		}
		var linkCtx map[string]any
		if l := linkByUpstream[up.ID]; l != nil {
			var lok bool
			linkCtx, lok = SafeLoadContextBlob(l.ContextJSON)
			if !lok {
				m.opts.Logger.Warn("gateway.context.invalid_json",
					zap.Int64("tenant_id", tenant.ID),
					zap.String("upstream", up.Name),
					zap.String("source", "upstream_link.context_json"))
			}
		}
		out = append(out, UpstreamView{
			Name:    up.Name,
			Aliases: resolved[up.Name],
			Context: MergeContext(defaults, linkCtx),
		})
	}
	return out, nil
}

func decodeAliasesJSON(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// bundleFor returns the cached Bundle for (tenant, upstreamName) or
// builds and caches a new one.
func (m *Manager) bundleFor(ctx context.Context, tenant *storage.Tenant, upstreamName string) (*Bundle, error) {
	m.mu.RLock()
	if perTenant, ok := m.bundles[tenant.ID]; ok {
		if b, ok := perTenant[upstreamName]; ok {
			m.mu.RUnlock()
			return b, nil
		}
	}
	m.mu.RUnlock()

	b, err := m.buildBundle(ctx, tenant, upstreamName)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	perTenant, ok := m.bundles[tenant.ID]
	if !ok {
		perTenant = make(map[string]*Bundle)
		m.bundles[tenant.ID] = perTenant
	}
	if existing, ok := perTenant[upstreamName]; ok {
		// Lost a race; reuse the bundle the other goroutine cached.
		return existing, nil
	}
	perTenant[upstreamName] = b
	return b, nil
}

func (m *Manager) buildBundle(ctx context.Context, tenant *storage.Tenant, upstreamName string) (*Bundle, error) {
	up, err := m.opts.Service.LoadUpstream(ctx, tenant.ID, upstreamName)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, upstream.ErrUpstreamNotFound) {
			return nil, fmt.Errorf("gateway: upstream %q not found in tenant %d", upstreamName, tenant.ID)
		}
		return nil, err
	}
	strat, err := m.opts.Registry.Resolve(upstream.StrategyType(up.StrategyType))
	if err != nil {
		return nil, err
	}

	var resolver upstream.UserResolver
	if strat.RequiresLink() {
		resolver = ctxUserResolver
	}
	authProvider, err := upstream.NewDBAuthProvider(m.opts.Store, m.opts.Registry, tenant, up, resolver)
	if err != nil {
		return nil, err
	}

	// TODO(phase-10): swap http.DefaultTransport for
	// resilience.Client("upstream."+up.Name+".calls", cfg).Transport.
	// This is the single construction site referenced in
	// docs/phases/phase-10-wiring-hardening.md.
	rt := &AuthInjectingTransport{
		Base:             http.DefaultTransport,
		Auth:             authProvider,
		Store:            m.opts.Store,
		UpstreamName:     up.Name,
		HealthThresholds: m.opts.HealthThresholds,
		Logger:           m.opts.Logger,
	}

	return &Bundle{
		Tenant:     tenant,
		Upstream:   up,
		Strategy:   strat,
		Auth:       authProvider,
		HTTPClient: &http.Client{Transport: rt, Timeout: m.opts.Timeout},
		Logger:     m.opts.Logger,
		Timeout:    m.opts.Timeout,
	}, nil
}

func ctxUserResolver(ctx context.Context) (*storage.User, bool) {
	return auth.MCPUserFromContext(ctx)
}

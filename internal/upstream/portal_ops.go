package upstream

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/storage"
)

// LinkState is the lifecycle label rendered by the portal for a
// (user, upstream) pair. Derived from UpstreamLink presence + flags.
// Kept here (not in storage) because it is a presentation concept.
type LinkState string

const (
	LinkStateNone         LinkState = "none"
	LinkStateConnected    LinkState = "connected"
	LinkStateDisabled     LinkState = "disabled"
	LinkStateAutoDisabled LinkState = "auto_disabled"
	LinkStateNeedsRelink  LinkState = "needs_relink"
)

// UserUpstreamSummary is one row of the per-user upstream listing the
// portal renders. Carries the resolved LinkState + last-error info so
// the SPA can render a stable CTA without re-deriving the lifecycle.
type UserUpstreamSummary struct {
	Upstream        *storage.Upstream
	Link            *storage.UpstreamLink // nil when LinkState == LinkStateNone
	LinkState       LinkState
	StrategySubMode string // "shared"/"override" for static_header; "" otherwise
	RequiresLink    bool
	LastErrorReason string
	// HasUserOverride is true for static_header upstreams when the user
	// has submitted their own override secret (Link.ExtraJSON is
	// populated). Drives the portal CTA between Submit and Rotate/Clear.
	HasUserOverride bool
	// Tools is the cached MCP tool catalog for this upstream
	// (upstream_tools rows). Same for every user.
	Tools []storage.UpstreamTool
	// Aliases is the decoded prefix-alias list (post-collision-pass is
	// applied at gateway-fan-out time; here we surface the raw set
	// captured by the indexer).
	Aliases []string
}

// subModeProvider is an optional capability for strategies that have
// a per-upstream sub-mode (currently only static_header). Implementing
// it lets ListUpstreamsForUser surface "shared"/"override" without the
// upstream package importing the strategy package.
type subModeProvider interface {
	SubMode(ctx context.Context, lctx LinkContext) (string, error)
}

// secretPersister is the optional capability for strategies that
// accept a user-submitted secret out-of-band (currently only
// static_header with AllowUserOverride=true).
type secretPersister interface {
	PersistUserSecret(ctx context.Context, lctx LinkContext, secret string) error
}

// secretClearer is the optional capability for strategies that
// expose a server-side "clear user override" operation (currently
// only static_header with AllowUserOverride=true).
type secretClearer interface {
	ClearUserOverride(ctx context.Context, lctx LinkContext) error
}

// strategySubModeShared mirrors statichdr.SubModeShared without
// importing the strategy package. A static_header upstream in
// "shared" mode applies the same secret to every caller, so the
// summary surfaces it as tenant-mode (RequiresLink=false).
const strategySubModeShared = "shared"

// applyStrategyMeta fills in RequiresLink + StrategySubMode on a
// summary row. Centralises the "static_header shared mode is tenant-
// wide" rule so the admin and portal listings agree. Returns the
// resolved Strategy so the caller can keep working; returns the
// resolver error untouched when the strategy type is unknown.
func (s *Service) applyStrategyMeta(ctx context.Context, tenant *storage.Tenant, up *storage.Upstream, row *UserUpstreamSummary) (Strategy, error) {
	strat, err := s.registry.Resolve(StrategyType(up.StrategyType))
	if err != nil {
		return nil, err
	}
	row.RequiresLink = strat.RequiresLink()
	if smp, ok := strat.(subModeProvider); ok {
		lctx := LinkContext{Tenant: tenant, Upstream: up}
		if sub, err := smp.SubMode(ctx, lctx); err == nil {
			row.StrategySubMode = sub
			if sub == strategySubModeShared {
				row.RequiresLink = false
			}
		}
	}
	return strat, nil
}

// LoadUserBySubject returns the local User row for (tenant, zitadel
// subject). Used by the portal RPC layer to resolve the OIDC subject
// against Limen's mirror of Zitadel identities. Runs under the tenant
// pool; RLS catches a stale tenant pin defensively.
func (s *Service) LoadUserBySubject(ctx context.Context, tenantID int64, subject string) (*storage.User, error) {
	if subject == "" {
		return nil, errors.New("upstream: empty zitadel subject")
	}
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenantID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = commit() }()

	var u storage.User
	if err := tx.Where("zitadel_subject = ?", subject).First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("upstream: user not found")
		}
		return nil, fmt.Errorf("upstream: load user: %w", err)
	}
	return &u, nil
}

// loadToolCatalog returns the cached upstream_tools rows for upstreamID
// ordered by name. Empty slice when the catalog hasn't been indexed
// yet; never returns ErrRecordNotFound.
func (s *Service) loadToolCatalog(ctx context.Context, tenantID, upstreamID int64) ([]storage.UpstreamTool, error) {
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenantID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = commit() }()

	var rows []storage.UpstreamTool
	if err := tx.Where("upstream_id = ?", upstreamID).
		Order("name ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("upstream: load tool catalog: %w", err)
	}
	return rows, nil
}

// ListUpstreamsForUser returns every tenant upstream paired with the
// caller's link state. Soft-deleted upstreams are excluded by GORM.
func (s *Service) ListUpstreamsForUser(ctx context.Context, tenant *storage.Tenant, user *storage.User) ([]UserUpstreamSummary, error) {
	if tenant == nil || user == nil {
		return nil, errors.New("upstream: tenant/user required")
	}
	ups, err := s.ListUpstreams(ctx, tenant.ID)
	if err != nil {
		return nil, err
	}
	out := make([]UserUpstreamSummary, 0, len(ups))
	for i := range ups {
		up := &ups[i]
		row := s.summariseUpstream(ctx, tenant, user, up)
		out = append(out, row)
	}
	return out, nil
}

// summariseUpstream builds a single UserUpstreamSummary row. Extracted
// to keep ListUpstreamsForUser flat — the per-upstream branching used
// to live inline.
func (s *Service) summariseUpstream(ctx context.Context, tenant *storage.Tenant, user *storage.User, up *storage.Upstream) UserUpstreamSummary {
	row := UserUpstreamSummary{
		Upstream: up,
		Aliases:  DecodeAliasesJSON(up.AliasesJSON),
	}
	if tools, err := s.loadToolCatalog(ctx, tenant.ID, up.ID); err == nil {
		row.Tools = tools
	}
	if _, sErr := s.applyStrategyMeta(ctx, tenant, up, &row); sErr != nil {
		// Unknown strategy at runtime — keep listing the upstream but
		// surface the failure shape.
		row.LinkState = LinkStateNone
		row.LastErrorReason = sErr.Error()
		return row
	}
	link, lerr := s.loadLink(ctx, tenant.ID, user.ID, up.ID)
	switch {
	case errors.Is(lerr, ErrLinkNotFound):
		row.LinkState = LinkStateNone
	case lerr != nil:
		row.LinkState = LinkStateNone
		row.LastErrorReason = lerr.Error()
	default:
		row.Link = link
		row.LinkState = deriveLinkState(link)
		row.LastErrorReason = link.LastFailureReason
		row.HasUserOverride = !link.ExtraJSON.IsZero()
	}
	return row
}

// SetLinkEnabled flips UpstreamLink.Enabled. Re-enabling an
// auto_disabled link ALSO clears the failure counters (treat the
// explicit re-enable as an intentional reset signal — otherwise the
// next request would re-trip auto-disable on the stale counter).
func (s *Service) SetLinkEnabled(ctx context.Context, tenant *storage.Tenant, user *storage.User, upstreamIdentifier string, enabled bool) error {
	if tenant == nil || user == nil {
		return errors.New("upstream: tenant/user required")
	}
	up, err := s.loadUpstream(ctx, tenant.ID, upstreamIdentifier)
	if err != nil {
		return err
	}
	link, err := s.loadLink(ctx, tenant.ID, user.ID, up.ID)
	if err != nil {
		return err
	}
	wasAutoDisabled := link.AutoDisabledAt != nil
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenant.ID))
	if err != nil {
		return err
	}
	if err := tx.Model(&storage.UpstreamLink{}).
		Where("id = ?", link.ID).
		Update("enabled", enabled).Error; err != nil {
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

// PersistUserStaticHeaderSecret routes to a static_header strategy's
// user-override secret persistence. Returns ErrUnsupported when the
// upstream is not `static_header` or when the admin has not enabled
// per-user override on it. The secret is NEVER logged — callers should
// log only its length.
func (s *Service) PersistUserStaticHeaderSecret(ctx context.Context, tenant *storage.Tenant, user *storage.User, upstreamIdentifier, secret string) error {
	if tenant == nil || user == nil {
		return errors.New("upstream: tenant/user required")
	}
	up, err := s.loadUpstream(ctx, tenant.ID, upstreamIdentifier)
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
	lctx := LinkContext{Tenant: tenant, User: user, Upstream: up}
	return p.PersistUserSecret(ctx, lctx, secret)
}

// ClearUserStaticHeaderOverride drops the user's override secret on a
// `static_header` link, falling the user back to the admin-configured
// shared secret on the next request. Idempotent; safe when no override
// has ever been submitted. Returns ErrUnsupported when the strategy is
// not `static_header`.
func (s *Service) ClearUserStaticHeaderOverride(ctx context.Context, tenant *storage.Tenant, user *storage.User, upstreamIdentifier string) error {
	if tenant == nil || user == nil {
		return errors.New("upstream: tenant/user required")
	}
	up, err := s.loadUpstream(ctx, tenant.ID, upstreamIdentifier)
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
	lctx := LinkContext{Tenant: tenant, User: user, Upstream: up}
	return c.ClearUserOverride(ctx, lctx)
}

func deriveLinkState(link *storage.UpstreamLink) LinkState {
	switch {
	case link.AutoDisabledAt != nil:
		return LinkStateAutoDisabled
	case link.NeedsRelink:
		return LinkStateNeedsRelink
	case !link.Enabled:
		return LinkStateDisabled
	default:
		return LinkStateConnected
	}
}

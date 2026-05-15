// Package upstream — background refresher.
//
// A single goroutine per-process iterates upstream_links that are about
// to expire and calls Strategy.Maintain on each. Sweep also runs the
// NeedsRelink → auto_disabled_at trip via health.MaybeAutoDisableForRelink.
//
// The refresher reads on the admin pool (WithSuperuser) so it can scan
// across tenants in a single query, but the actual Maintain call runs
// inside the per-strategy code which switches to WithTenant.
package upstream

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/storage"
)

// RefresherOptions configures the background loop.
type RefresherOptions struct {
	// Interval is the sweep period.
	Interval time.Duration
	// RefreshWindow selects links with expires_at < now + RefreshWindow.
	RefreshWindow time.Duration
	// HealthThresholds drives MaybeAutoDisableForRelink + RecordFailure.
	HealthThresholds HealthThresholds
	// Logger for sweep stats. Optional.
	Logger *zap.Logger
}

// Refresher is the background goroutine.
type Refresher struct {
	store    *storage.Store
	registry *Registry
	opts     RefresherOptions
	logger   *zap.Logger
}

// NewRefresher builds a Refresher. Run() blocks until ctx is cancelled.
func NewRefresher(store *storage.Store, registry *Registry, opts RefresherOptions) *Refresher {
	if opts.Interval <= 0 {
		opts.Interval = 2 * time.Minute
	}
	if opts.RefreshWindow <= 0 {
		opts.RefreshWindow = 5 * time.Minute
	}
	logger := opts.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Refresher{store: store, registry: registry, opts: opts, logger: logger}
}

// Run starts the sweep loop. Blocks until ctx is cancelled.
func (r *Refresher) Run(ctx context.Context) {
	t := time.NewTicker(r.opts.Interval)
	defer t.Stop()
	// Run an initial sweep so dev/staging see refresh activity without
	// waiting a full Interval.
	r.sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.sweep(ctx)
		}
	}
}

func (r *Refresher) sweep(ctx context.Context) {
	if r.opts.HealthThresholds.NeedsRelinkWindow > 0 {
		if n, err := MaybeAutoDisableForRelink(ctx, r.store, r.opts.HealthThresholds); err != nil {
			r.logger.Warn("upstream refresher: auto-disable sweep failed", zap.Error(err))
		} else if n > 0 {
			r.logger.Info("upstream refresher: auto-disabled stale needs_relink rows", zap.Int64("count", n))
		}
	}

	links, err := r.dueLinks(ctx)
	if err != nil {
		r.logger.Warn("upstream refresher: load due links failed", zap.Error(err))
		return
	}
	r.logger.Debug("upstream refresher: sweeping", zap.Int("count", len(links)))

	for i := range links {
		link := &links[i]
		if err := r.maintainOne(ctx, link); err != nil {
			r.logger.Warn("upstream refresher: maintain failed",
				zap.Int64("link_id", link.ID),
				zap.Error(err))
		}
	}
}

// dueLinks returns links whose expires_at is inside the refresh window
// and that aren't auto-disabled / needs-relink. Uses the partial index
// installed by migration 00004.
func (r *Refresher) dueLinks(ctx context.Context) ([]storage.UpstreamLink, error) {
	tx, commit, err := r.store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		return nil, err
	}
	defer func() { _ = commit() }()

	cutoff := time.Now().Add(r.opts.RefreshWindow)
	var out []storage.UpstreamLink
	if err := tx.Where(`expires_at IS NOT NULL
		AND expires_at < ?
		AND needs_relink = false
		AND auto_disabled_at IS NULL
		AND enabled = true`, cutoff).
		Order("expires_at ASC").
		Limit(500).
		Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// maintainOne loads the upstream + tenant + user for a link, AAD-binds
// the encrypted columns under the freshly resolved tenant/user, and runs
// Strategy.Maintain.
func (r *Refresher) maintainOne(ctx context.Context, link *storage.UpstreamLink) error {
	tx, commit, err := r.store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		return err
	}
	var up storage.Upstream
	if err := tx.Where("id = ?", link.UpstreamID).First(&up).Error; err != nil {
		_ = commit()
		return err
	}
	var tenant storage.Tenant
	if err := tx.Where("id = ?", link.TenantID).First(&tenant).Error; err != nil {
		_ = commit()
		return err
	}
	var user storage.User
	if err := tx.Where("id = ?", link.UserID).First(&user).Error; err != nil {
		_ = commit()
		return err
	}
	_ = commit()

	tenantStr := strconv.FormatInt(tenant.ID, 10)
	userStr := strconv.FormatInt(user.ID, 10)
	if err := link.AccessToken.Decrypt(tenantStr, userStr, "upstream.access_token"); err != nil {
		return fmt.Errorf("upstream refresher: decrypt access_token: %w", err)
	}
	if err := link.RefreshToken.Decrypt(tenantStr, userStr, "upstream.refresh_token"); err != nil {
		return fmt.Errorf("upstream refresher: decrypt refresh_token: %w", err)
	}
	if err := link.ExtraJSON.Decrypt(tenantStr, userStr, "upstream.extra"); err != nil {
		return fmt.Errorf("upstream refresher: decrypt extra: %w", err)
	}

	strat, err := r.registry.Resolve(StrategyType(up.StrategyType))
	if err != nil {
		return err
	}

	lctx := LinkContext{
		Tenant:   &tenant,
		User:     &user,
		Upstream: &up,
		Link:     link,
	}
	if err := strat.Maintain(ctx, lctx); err != nil {
		// Record the failure unless it's the "user needs to re-link" case
		// (which markNeedsRelink already wrote on the link row).
		if errors.Is(err, ErrNeedsRelink) {
			return nil
		}
		reason := ReasonRefreshFailed
		_ = RecordFailure(ctx, r.store, link.ID, reason, false, r.opts.HealthThresholds)
		return err
	}
	return nil
}

// ensure gorm import is used in builds that don't reference it directly.
var _ = gorm.ErrRecordNotFound

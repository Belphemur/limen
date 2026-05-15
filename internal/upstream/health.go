package upstream

import (
	"context"
	"fmt"
	"time"

	"github.com/belphemur/limen/internal/storage"
)

// HealthThresholds parameterise auto-disable. Sourced from
// config.UpstreamRefreshConfig; the round-tripper and the refresher pass
// the same instance so the policy is consistent.
type HealthThresholds struct {
	FailThreshold     int
	FailWindow        time.Duration
	NeedsRelinkWindow time.Duration
}

// RecordSuccess clears every failure column on a link in a single UPDATE.
// Idempotent: a healthy link is a no-op write. Callers pass an open
// *gorm.DB so the write can join a larger transaction (Phase 8's
// round-tripper batches the success update with its tenant GUC pin).
func RecordSuccess(ctx context.Context, store *storage.Store, linkID int64) error {
	tx, commit, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		return fmt.Errorf("upstream: health: open session: %w", err)
	}
	res := tx.Exec(`UPDATE upstream_links
		SET consecutive_failures = 0,
		    first_failure_at = NULL,
		    last_failure_at = NULL,
		    last_failure_reason = '',
		    auto_disabled_at = NULL,
		    needs_relink = false
		WHERE id = ?`, linkID)
	if res.Error != nil {
		_ = commit()
		return fmt.Errorf("upstream: health: clear failures: %w", res.Error)
	}
	return commit()
}

// RecordFailure bumps the streak counter, anchors FirstFailureAt on the
// 0→1 transition, refreshes LastFailureAt, and trips auto-disable when the
// thresholds are met. needsRelink=true overrides any threshold logic and
// flips the link into the "user must re-link" state immediately.
//
// All of this is one SQL UPDATE so concurrent failures from the refresher
// and a tool call don't race the counter.
func RecordFailure(ctx context.Context, store *storage.Store, linkID int64, reason FailureReason, needsRelink bool, thresholds HealthThresholds) error {
	tx, commit, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		return fmt.Errorf("upstream: health: open session: %w", err)
	}
	// Increment, anchor first-failure, update last-failure, optionally
	// flip auto-disable. The CASE on auto_disabled_at preserves any
	// existing trip — we never reset it from a failure path.
	res := tx.Exec(`UPDATE upstream_links
		SET consecutive_failures = consecutive_failures + 1,
		    first_failure_at     = COALESCE(first_failure_at, now()),
		    last_failure_at      = now(),
		    last_failure_reason  = ?,
		    needs_relink         = needs_relink OR ?,
		    auto_disabled_at     = CASE
		      WHEN auto_disabled_at IS NOT NULL THEN auto_disabled_at
		      WHEN (consecutive_failures + 1) >= ?
		           AND now() - COALESCE(first_failure_at, now()) >= make_interval(secs => ?)
		        THEN now()
		      WHEN ? = true THEN auto_disabled_at  -- needs_relink alone does not trip; the refresher's 24h-rule does
		      ELSE auto_disabled_at
		    END
		WHERE id = ?`,
		string(reason),
		needsRelink,
		thresholds.FailThreshold,
		thresholds.FailWindow.Seconds(),
		needsRelink,
		linkID,
	)
	if res.Error != nil {
		_ = commit()
		return fmt.Errorf("upstream: health: record failure: %w", res.Error)
	}
	return commit()
}

// MaybeAutoDisableForRelink trips auto-disable on a link whose NeedsRelink
// has been true for at least thresholds.NeedsRelinkWindow. Called from the
// background refresher's sweep — the per-request path doesn't run this
// because it doesn't have a stable wall-clock anchor for the streak start
// (refresher uses LastFailureAt at the moment NeedsRelink flipped true).
func MaybeAutoDisableForRelink(ctx context.Context, store *storage.Store, thresholds HealthThresholds) (int64, error) {
	tx, commit, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		return 0, fmt.Errorf("upstream: health: open session: %w", err)
	}
	res := tx.Exec(`UPDATE upstream_links
		SET auto_disabled_at = now()
		WHERE deleted_at IS NULL
		  AND needs_relink = true
		  AND auto_disabled_at IS NULL
		  AND last_failure_at IS NOT NULL
		  AND now() - last_failure_at >= make_interval(secs => ?)`,
		thresholds.NeedsRelinkWindow.Seconds(),
	)
	if res.Error != nil {
		_ = commit()
		return 0, fmt.Errorf("upstream: health: trip relink auto-disable: %w", res.Error)
	}
	n := res.RowsAffected
	return n, commit()
}

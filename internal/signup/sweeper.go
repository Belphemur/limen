package signup

import (
	"context"
	"math/rand"
	"time"

	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/storage"
)

// SweepInterval is the base period between sweeps. Each tick adds a
// small random jitter (up to 25%) to avoid synchronised storms when
// multiple portal replicas start at the same time.
const SweepInterval = 15 * time.Minute

// SweepCutoff is the age above which an uncompleted pending signup
// row is deleted. Independent of VerifyTokenTTL — the token may
// expire earlier; the row sticks around long enough to surface
// support questions ("I tried to sign up yesterday and...").
const SweepCutoff = 24 * time.Hour

// Sweeper deletes stale pending_signups rows. Run launches the loop
// and blocks until ctx is cancelled.
type Sweeper struct {
	Store  *storage.Store
	Logger *zap.Logger
	// IdleSweep, when non-nil, is called once per tick AFTER the DB
	// sweep so a PerIPLimiter can evict idle buckets on the same
	// schedule. Optional.
	IdleSweep func()
}

// NewSweeper returns a Sweeper. Pass logger=nil for a silent sweeper.
func NewSweeper(store *storage.Store, logger *zap.Logger) *Sweeper {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Sweeper{Store: store, Logger: logger}
}

// Run blocks until ctx is cancelled. It performs one sweep
// immediately on start and then on every jittered tick.
func (s *Sweeper) Run(ctx context.Context) {
	s.sweepOnce(ctx)
	for {
		wait := SweepInterval + time.Duration(rand.Int63n(int64(SweepInterval/4)))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
			s.sweepOnce(ctx)
		}
	}
}

func (s *Sweeper) sweepOnce(ctx context.Context) {
	db, commit, err := s.Store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		s.Logger.Warn("signup sweeper: open session", zap.Error(err))
		return
	}
	cutoff := time.Now().Add(-SweepCutoff)
	res := db.
		Where("completed_at IS NULL AND created_at < ?", cutoff).
		Delete(&storage.PendingSignup{})
	if err := commit(); err != nil {
		s.Logger.Warn("signup sweeper: commit", zap.Error(err))
		return
	}
	if res.Error != nil {
		s.Logger.Warn("signup sweeper: delete", zap.Error(res.Error))
		return
	}
	if res.RowsAffected > 0 {
		s.Logger.Info("signup sweeper: deleted stale pending signups",
			zap.Int64("rows", res.RowsAffected))
	}
	if s.IdleSweep != nil {
		s.IdleSweep()
	}
}

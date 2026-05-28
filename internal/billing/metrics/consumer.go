package metrics

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/valkey"
	"go.uber.org/zap"
)

const (
	streamActiveUsers    = "billing:active_users"
	streamSAConnections  = "billing:sa_connections"
	groupBillingObserver = "billing_observer"
	batchSize            = 256
	blockMs              = 250
	autoClaimInterval    = 60 * time.Second
	autoClaimMinIdle     = 60_000 // ms
)

// Consumer reads billing events from Valkey Streams and writes to Postgres.
// One consumer per process. Safe to run as a goroutine.
type Consumer struct {
	valkey   valkey.Client
	store    *storage.Store
	logger   *zap.Logger
	consumer string // unique consumer name within the group
}

// NewConsumer creates a billing metrics consumer.
// consumerName should be unique per instance (e.g., hostname or ULID).
func NewConsumer(vc valkey.Client, store *storage.Store, logger *zap.Logger, consumerName string) *Consumer {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Consumer{
		valkey:   vc,
		store:    store,
		logger:   logger,
		consumer: consumerName,
	}
}

// Bootstrap creates consumer groups if they don't exist. Idempotent.
func (c *Consumer) Bootstrap(ctx context.Context) {
	for _, stream := range []string{streamActiveUsers, streamSAConnections} {
		if err := c.valkey.XGroupCreate(ctx, stream, groupBillingObserver, "$"); err != nil {
			// BUSYGROUP error means group already exists — that's fine
			c.logger.Debug("consumer group already exists (ok)", zap.String("stream", stream), zap.Error(err))
		} else {
			c.logger.Info("consumer group created", zap.String("stream", stream))
		}
	}
}

// Run starts the main consumer loop. Blocks until ctx is cancelled.
func (c *Consumer) Run(ctx context.Context) {
	c.Bootstrap(ctx)

	autoClaimTicker := time.NewTicker(autoClaimInterval)
	defer autoClaimTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-autoClaimTicker.C:
			c.runAutoClaim(ctx)
		default:
			c.processBatch(ctx)
		}
	}
}

// runAutoClaim transfers pending entries from timed-out consumers.
func (c *Consumer) runAutoClaim(ctx context.Context) {
	for _, stream := range []string{streamActiveUsers, streamSAConnections} {
		claimed, err := c.valkey.XAutoClaim(ctx, stream, groupBillingObserver, c.consumer, autoClaimMinIdle, batchSize)
		if err != nil {
			c.logger.Warn("autoclaim failed", zap.String("stream", stream), zap.Error(err))
			continue
		}
		if len(claimed) > 0 {
			c.logger.Info("autoclaimed pending messages", zap.String("stream", stream), zap.Int("count", len(claimed)))
		}
	}
}

// processBatch reads a batch of messages and writes them to Postgres.
func (c *Consumer) processBatch(ctx context.Context) {
	msgs, err := c.valkey.XReadGroup(ctx, groupBillingObserver, c.consumer, blockMs, batchSize, streamActiveUsers, streamSAConnections)
	if err != nil {
		// timeout on block is normal — the valkey-go client returns a timeout
		// error when BLOCK expires with no messages. Suppress this expected case.
		if !isTimeoutError(err) {
			c.logger.Warn("XReadGroup failed", zap.Error(err))
		}
		return
	}
	if len(msgs) == 0 {
		return
	}

	// Group messages by stream
	activeUserMsgs := make([]valkey.StreamMessage, 0)
	saConnMsgs := make([]valkey.StreamMessage, 0)
	for _, msg := range msgs {
		switch msg.Stream {
		case streamActiveUsers:
			activeUserMsgs = append(activeUserMsgs, msg)
		case streamSAConnections:
			saConnMsgs = append(saConnMsgs, msg)
		}
	}

	// Process each stream in a transaction
	if len(activeUserMsgs) > 0 {
		c.processActiveUsers(ctx, activeUserMsgs)
	}
	if len(saConnMsgs) > 0 {
		c.processSAConnections(ctx, saConnMsgs)
	}
}

// processActiveUsers upserts active_user_months rows from stream messages.
func (c *Consumer) processActiveUsers(ctx context.Context, msgs []valkey.StreamMessage) {
	tenantGroups := groupByTenant(msgs)
	ackIDs := make([]string, 0, len(msgs))

	for tenantID, groupMsgs := range tenantGroups {
		db, commit, err := c.store.Session(storage.WithTenant(ctx, tenantID))
		if err != nil {
			c.logger.Error("failed to get session", zap.Int64("tenant_id", tenantID), zap.Error(err))
			continue
		}

		hasError := false
		for _, msg := range groupMsgs {
			userID := parseOptionalInt64(msg.Fields, "user_id")
			saID := parseOptionalInt64(msg.Fields, "sa_id")
			ts := parseInt64(msg.Fields, "ts")
			t := time.UnixMilli(ts)
			monthStart := t.Format("2006-01") + "-01" // first of month

			// UPSERT: increment call_count, update last_seen_at
			err = db.Exec(`
				INSERT INTO active_user_months (tenant_id, month_start, user_id, service_account_id, first_seen_at, last_seen_at, call_count, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, 1, NOW(), NOW())
				ON CONFLICT (tenant_id, month_start, user_id, service_account_id) WHERE deleted_at IS NULL
				DO UPDATE SET
					call_count = active_user_months.call_count + 1,
					last_seen_at = GREATEST(active_user_months.last_seen_at, EXCLUDED.last_seen_at),
					updated_at = NOW()
			`, tenantID, monthStart, userID, saID, t, t).Error
			if err != nil {
				c.logger.Error("upsert active_user_months failed", zap.Error(err))
				hasError = true
			}
		}

		if hasError {
			_ = commit()
			c.logger.Warn("batch failed, messages will be retried",
				zap.Int64("tenant_id", tenantID),
				zap.Int("count", len(groupMsgs)),
			)
		} else {
			if err := commit(); err != nil {
				c.logger.Error("commit failed", zap.Error(err))
			} else {
				for _, msg := range groupMsgs {
					ackIDs = append(ackIDs, msg.ID)
				}
			}
		}
	}

	// ACK + DEL successfully processed messages
	for _, id := range ackIDs {
		if _, err := c.valkey.XAck(ctx, streamActiveUsers, groupBillingObserver, id); err != nil {
			c.logger.Error("failed to ack active user message", zap.String("id", id), zap.Error(err))
		}
		if _, err := c.valkey.XDel(ctx, streamActiveUsers, id); err != nil {
			c.logger.Error("failed to del active user message", zap.String("id", id), zap.Error(err))
		}
	}
}

// processSAConnections inserts sa_connection_snapshots rows from stream messages.
func (c *Consumer) processSAConnections(ctx context.Context, msgs []valkey.StreamMessage) {
	tenantGroups := groupByTenant(msgs)
	ackIDs := make([]string, 0, len(msgs))

	for tenantID, groupMsgs := range tenantGroups {
		db, commit, err := c.store.Session(storage.WithTenant(ctx, tenantID))
		if err != nil {
			c.logger.Error("failed to get session", zap.Int64("tenant_id", tenantID), zap.Error(err))
			continue
		}

		hasError := false
		for _, msg := range groupMsgs {
			saID := parseInt64(msg.Fields, "sa_id")
			connected := msg.Fields["connected"] == "1"
			ts := parseInt64(msg.Fields, "ts")
			t := time.UnixMilli(ts)

			if connected {
				// Compute concurrent count: count of currently connected SAs for this tenant
				var concurrentCount int64
				err = db.Raw(`
					SELECT COUNT(*) FROM sa_connection_snapshots
					WHERE tenant_id = ? AND disconnected_at IS NULL AND deleted_at IS NULL
				`, tenantID).Scan(&concurrentCount).Error
				if err != nil {
					c.logger.Error("failed to count concurrent connections", zap.Error(err))
					hasError = true
					continue
				}

				err = db.Exec(`
					INSERT INTO sa_connection_snapshots (tenant_id, service_account_id, connected_at, concurrent_count, created_at, updated_at)
					VALUES (?, ?, ?, ?, NOW(), NOW())
				`, tenantID, saID, t, int32(concurrentCount+1)).Error
				if err != nil {
					c.logger.Error("insert sa_connection_snapshot failed", zap.Error(err))
					hasError = true
					continue
				}
			} else {
				// Disconnect: find the most recent open connection for this SA and close it
				err = db.Exec(`
					UPDATE sa_connection_snapshots
					SET disconnected_at = ?, updated_at = NOW()
					WHERE id = (
						SELECT id FROM sa_connection_snapshots
						WHERE tenant_id = ? AND service_account_id = ? AND disconnected_at IS NULL AND deleted_at IS NULL
						ORDER BY connected_at DESC LIMIT 1
					)
				`, t, tenantID, saID).Error
				if err != nil {
					c.logger.Error("update sa_connection_snapshot disconnect failed", zap.Error(err))
					hasError = true
					continue
				}
			}
		}

		if hasError {
			_ = commit()
			c.logger.Warn("SA connections batch failed, messages will be retried",
				zap.Int64("tenant_id", tenantID),
				zap.Int("count", len(groupMsgs)),
			)
		} else {
			if err := commit(); err != nil {
				c.logger.Error("commit failed", zap.Error(err))
			} else {
				for _, msg := range groupMsgs {
					ackIDs = append(ackIDs, msg.ID)
				}
			}
		}
	}

	// ACK + DEL successfully processed messages
	for _, id := range ackIDs {
		if _, err := c.valkey.XAck(ctx, streamSAConnections, groupBillingObserver, id); err != nil {
			c.logger.Error("failed to ack sa connection message", zap.String("id", id), zap.Error(err))
		}
		if _, err := c.valkey.XDel(ctx, streamSAConnections, id); err != nil {
			c.logger.Error("failed to del sa connection message", zap.String("id", id), zap.Error(err))
		}
	}
}

// groupByTenant groups messages by tenant_id field. Used to batch per-tenant transactions.
func groupByTenant(msgs []valkey.StreamMessage) map[int64][]valkey.StreamMessage {
	groups := make(map[int64][]valkey.StreamMessage)
	for _, msg := range msgs {
		tenantID, err := strconv.ParseInt(msg.Fields["tenant_id"], 10, 64)
		if err != nil {
			continue
		}
		groups[tenantID] = append(groups[tenantID], msg)
	}
	return groups
}

// parseOptionalInt64 parses a field value, returning nil if empty or "0".
func parseOptionalInt64(fields map[string]string, key string) *int64 {
	s, ok := fields[key]
	if !ok || s == "" || s == "0" {
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil
	}
	if v == 0 {
		return nil
	}
	return &v
}

// parseInt64 parses a field value, returning 0 on error.
func parseInt64(fields map[string]string, key string) int64 {
	v, _ := strconv.ParseInt(fields[key], 10, 64)
	return v
}

// isTimeoutError checks if the error is a blocking-read timeout (expected).
func isTimeoutError(err error) bool {
	return err != nil &&
		(strings.Contains(err.Error(), "timeout") ||
			strings.Contains(err.Error(), "deadline") ||
			strings.Contains(err.Error(), "context deadline exceeded"))
}

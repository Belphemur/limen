package metrics

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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
	dlqStream            = "billing:dlq"
	dlqMaxLen            = 10000
	dlqDeliveryThreshold = 5
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

// runAutoClaim transfers pending entries from timed-out consumers and sweeps
// the dead-letter queue for messages that have exceeded the delivery threshold.
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
	c.sweepDLQ(ctx)
}

// sweepDLQ moves messages with delivery count >= threshold to the dead-letter stream.
func (c *Consumer) sweepDLQ(ctx context.Context) {
	for _, stream := range []string{streamActiveUsers, streamSAConnections} {
		pending, err := c.valkey.XPending(ctx, stream, groupBillingObserver, "-", "+", batchSize)
		if err != nil {
			c.logger.Warn("dlq sweep: xpending failed", zap.String("stream", stream), zap.Error(err))
			continue
		}

		for _, msg := range pending {
			if msg.DeliveryCount < dlqDeliveryThreshold {
				continue
			}

			// Retrieve original message fields
			entries, err := c.valkey.XRange(ctx, stream, msg.ID, msg.ID)
			if err != nil {
				c.logger.Warn("dlq sweep: xrange failed", zap.String("stream", stream), zap.String("id", msg.ID), zap.Error(err))
				continue
			}
			if len(entries) == 0 {
				c.logger.Warn("dlq sweep: message not found in stream", zap.String("stream", stream), zap.String("id", msg.ID))
				continue
			}

			// Move to DLQ
			fields := entries[0].Fields
			dlqID, err := c.valkey.XAdd(ctx, dlqStream, fields, dlqMaxLen)
			if err != nil {
				c.logger.Error("dlq sweep: failed to add to dlq", zap.String("stream", stream), zap.String("id", msg.ID), zap.Error(err))
				continue
			}

			streamEvictedTotal.Inc()

			// ACK and DEL original message
			if _, err := c.valkey.XAck(ctx, stream, groupBillingObserver, msg.ID); err != nil {
				c.logger.Error("dlq sweep: failed to ack original message after successful dlq add — message may be duplicated",
					zap.String("stream", stream),
					zap.String("msg_id", msg.ID),
					zap.String("dlq_entry_id", dlqID),
					zap.Error(err))
				continue
			}
			if _, err := c.valkey.XDel(ctx, stream, msg.ID); err != nil {
				c.logger.Error("dlq sweep: failed to del original message", zap.String("stream", stream), zap.String("id", msg.ID), zap.Error(err))
				continue
			}

			c.logger.Warn("message moved to dead-letter queue due to excessive delivery failures",
				zap.String("stream", stream),
				zap.String("id", msg.ID),
				zap.String("consumer", msg.Consumer),
				zap.Int64("delivery_count", msg.DeliveryCount),
			)
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
			ts, err := parseInt64(msg.Fields, "ts")
			if err != nil {
				c.logger.Warn("skipping active user message with invalid timestamp", zap.Error(err), zap.String("msg_id", msg.ID))
				hasError = true
				continue
			}
			t := time.UnixMilli(ts)
			monthStart := t.Format("2006-01") + "-01" // first of month

			// UPSERT: increment call_count, update last_seen_at
			err = db.Exec(UpsertActiveUserMonthSQL, tenantID, monthStart, userID, saID, t, t).Error
			if err != nil {
				c.logger.Error("upsert active_user_months failed", zap.Error(err))
				hasError = true
			}
		}

		if hasError {
			if rbErr := db.Rollback().Error; rbErr != nil {
				c.logger.Error("rollback failed", zap.Error(rbErr))
			}
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
			saID, err := parseInt64(msg.Fields, "sa_id")
			if err != nil {
				c.logger.Warn("skipping SA connection message with invalid sa_id", zap.Error(err), zap.String("msg_id", msg.ID))
				hasError = true
				continue
			}
			connected := msg.Fields["connected"] == "1"
			ts, err := parseInt64(msg.Fields, "ts")
			if err != nil {
				c.logger.Warn("skipping SA connection message with invalid timestamp", zap.Error(err), zap.String("msg_id", msg.ID))
				hasError = true
				continue
			}
			t := time.UnixMilli(ts)

			if connected {
				// Compute concurrent count at insert time using a DB-level subquery
				// to avoid race conditions when multiple connect events are processed
				// in the same batch.
				err = db.Exec(InsertSAConnectionSnapshotSQL, tenantID, saID, t, tenantID).Error
				if err != nil {
					c.logger.Error("insert sa_connection_snapshot failed", zap.Error(err))
					hasError = true
					continue
				}
			} else {
				// Disconnect: find the most recent open connection for this SA and close it
				err = db.Exec(UpdateSAConnectionSnapshotDisconnectSQL, t, tenantID, saID).Error
				if err != nil {
					c.logger.Error("update sa_connection_snapshot disconnect failed", zap.Error(err))
					hasError = true
					continue
				}
			}
		}

		if hasError {
			if rbErr := db.Rollback().Error; rbErr != nil {
				c.logger.Error("rollback failed", zap.Error(rbErr))
			}
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

// parseInt64 parses a field value, returning an error if missing or invalid.
func parseInt64(fields map[string]string, key string) (int64, error) {
	s, ok := fields[key]
	if !ok {
		return 0, fmt.Errorf("missing field %q", key)
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid int64 value %q for field %q: %w", s, key, err)
	}
	return v, nil
}

// isTimeoutError checks if the error is a blocking-read timeout (expected).
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

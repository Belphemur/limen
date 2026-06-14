package audit

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// Event represents a single auditable event. All fields are structured
// for consistent log indexing.
type Event struct {
	Action     string
	ActorID    string
	ActorEmail string
	TenantID   string
	TargetID   string
	TargetType string
	Metadata   map[string]string
	Outcome    string
	Error      string
	ClientIP   string
	Timestamp  time.Time
}

// Emitter writes audit events to a sink. Implementations must be safe
// for concurrent use.
type Emitter interface {
	Emit(ctx context.Context, event *Event)
}

// TODO(Phase 13d): Gate audit log persistence on the tenant's AuditLogs entitlement.
// When the entitlement is disabled, audit events should be dropped or logged
// locally only, not stored in the audit sink.

// LogEmitter writes audit events as structured zap logs at Info level.
type LogEmitter struct {
	logger *zap.Logger
}

// NewLogEmitter creates an Emitter that writes to logger.
func NewLogEmitter(logger *zap.Logger) *LogEmitter {
	return &LogEmitter{logger: logger.Named("audit")}
}

// Emit writes the event as a structured log line. Sets Timestamp to now
// if zero.
func (e *LogEmitter) Emit(ctx context.Context, event *Event) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	fields := []zap.Field{
		zap.String("action", event.Action),
		zap.String("outcome", event.Outcome),
		zap.Time("timestamp", event.Timestamp),
	}
	if event.ActorID != "" {
		fields = append(fields, zap.String("actor_id", event.ActorID))
	}
	if event.ActorEmail != "" {
		fields = append(fields, zap.String("actor_email", event.ActorEmail))
	}
	if event.TenantID != "" {
		fields = append(fields, zap.String("tenant_id", event.TenantID))
	}
	if event.TargetID != "" {
		fields = append(fields, zap.String("target_id", event.TargetID))
	}
	if event.TargetType != "" {
		fields = append(fields, zap.String("target_type", event.TargetType))
	}
	if event.ClientIP != "" {
		fields = append(fields, zap.String("client_ip", event.ClientIP))
	}
	if event.Error != "" {
		fields = append(fields, zap.String("error", event.Error))
	}
	for k, v := range event.Metadata {
		fields = append(fields, zap.String("meta_"+k, v))
	}
	e.logger.Info("audit_event", fields...)
}

var _ Emitter = (*LogEmitter)(nil)

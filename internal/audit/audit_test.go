package audit

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

func TestLogEmitterEmit(t *testing.T) {
	emitter := NewLogEmitter(zaptest.NewLogger(t))
	emitter.Emit(context.Background(), &Event{
		Action:  "test_action",
		Outcome: "success",
	})
}

func TestLogEmitterTimestamps(t *testing.T) {
	emitter := NewLogEmitter(zaptest.NewLogger(t))
	event := &Event{Action: "test", Outcome: "success"}
	emitter.Emit(context.Background(), event)
	if event.Timestamp.IsZero() {
		t.Error("Timestamp should have been set")
	}
}

func TestLogEmitterAllFields(t *testing.T) {
	emitter := NewLogEmitter(zaptest.NewLogger(t))
	emitter.Emit(context.Background(), &Event{
		Action:     "user_invited",
		ActorID:    "user-1",
		ActorEmail: "a@b.com",
		TenantID:   "tnt_abc",
		TargetID:   "upstream-1",
		TargetType: "upstream",
		Metadata:   map[string]string{"key": "val"},
		Outcome:    "success",
		ClientIP:   "1.2.3.4",
		Timestamp:  time.Now(),
	})
}

func TestEmitterInterface(t *testing.T) {
	// Compile-time and runtime check: NewLogEmitter satisfies Emitter.
	var _ Emitter = NewLogEmitter(zaptest.NewLogger(t))
}

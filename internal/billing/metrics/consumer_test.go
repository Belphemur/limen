package metrics

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/belphemur/limen/internal/valkey"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestConsumer_Bootstrap(t *testing.T) {
	vc := valkey.NewInMemory()
	c := NewConsumer(vc, nil, nil, "test-consumer")
	ctx := context.Background()
	c.Bootstrap(ctx)
	// Bootstrap is idempotent — call twice
	c.Bootstrap(ctx)
}

func TestGroupByTenant(t *testing.T) {
	msgs := []valkey.StreamMessage{
		{Fields: map[string]string{"tenant_id": "1"}, ID: "1-0"},
		{Fields: map[string]string{"tenant_id": "2"}, ID: "2-0"},
		{Fields: map[string]string{"tenant_id": "1"}, ID: "1-1"},
	}
	groups := groupByTenant(msgs)
	if len(groups) != 2 {
		t.Errorf("expected 2 groups, got %d", len(groups))
	}
	if len(groups[1]) != 2 {
		t.Errorf("expected 2 msgs for tenant 1, got %d", len(groups[1]))
	}
}

func TestParseOptionalInt64(t *testing.T) {
	tests := []struct {
		fields  map[string]string
		key     string
		wantNil bool
	}{
		{map[string]string{"a": "42"}, "a", false},
		{map[string]string{"a": ""}, "a", true},
		{map[string]string{"a": "0"}, "a", true},
		{map[string]string{}, "a", true},
	}
	for _, tt := range tests {
		v := parseOptionalInt64(tt.fields, tt.key)
		if tt.wantNil && v != nil {
			t.Errorf("expected nil for %v[%s], got %d", tt.fields, tt.key, *v)
		}
		if !tt.wantNil && v == nil {
			t.Errorf("expected non-nil for %v[%s]", tt.fields, tt.key)
		}
	}
}

// xAckFailClient is a mock valkey.Client that simulates an XAck failure
// after a successful XAdd to the DLQ.
type xAckFailClient struct {
	xAddCalled bool
	xAckCalled bool
	xDelCalled bool
	fields     map[string]string
}

func (m *xAckFailClient) SetEX(_ context.Context, _ string, _ []byte, _ time.Duration) error { return nil }
func (m *xAckFailClient) SetNX(_ context.Context, _ string, _ []byte, _ time.Duration) (bool, error) { return true, nil }
func (m *xAckFailClient) GetDel(_ context.Context, _ string) ([]byte, error) { return nil, valkey.ErrNotFound }
func (m *xAckFailClient) Del(_ context.Context, _ string) error { return nil }
func (m *xAckFailClient) Get(_ context.Context, _ string) ([]byte, error) { return nil, valkey.ErrNotFound }
func (m *xAckFailClient) Close() {}

func (m *xAckFailClient) XAdd(_ context.Context, _ string, fields map[string]string, _ int64) (string, error) {
	m.xAddCalled = true
	m.fields = fields
	return "dlq-1", nil
}

func (m *xAckFailClient) XReadGroup(_ context.Context, _, _ string, _, _ int64, _ ...string) ([]valkey.StreamMessage, error) {
	return nil, nil
}

func (m *xAckFailClient) XAck(_ context.Context, _, _ string, _ ...string) (int64, error) {
	m.xAckCalled = true
	return 0, errors.New("xack simulated failure")
}

func (m *xAckFailClient) XDel(_ context.Context, _ string, _ ...string) (int64, error) {
	m.xDelCalled = true
	return 0, nil
}

func (m *xAckFailClient) XAutoClaim(_ context.Context, _, _, _ string, _, _ int64) ([]string, error) {
	return nil, nil
}

func (m *xAckFailClient) XPending(_ context.Context, stream, _ string, _, _ string, _ int64) ([]valkey.PendingMessage, error) {
	if stream != streamActiveUsers {
		return nil, nil
	}
	return []valkey.PendingMessage{
		{ID: "1-0", Consumer: "other-consumer", IdleTimeMs: 1000, DeliveryCount: 5},
	}, nil
}

func (m *xAckFailClient) XRange(_ context.Context, stream, _, _ string) ([]valkey.StreamMessage, error) {
	return []valkey.StreamMessage{
		{ID: "1-0", Stream: stream, Fields: map[string]string{"tenant_id": "1", "user_id": "42"}},
	}, nil
}

func (m *xAckFailClient) XGroupCreate(_ context.Context, _, _, _ string) error { return nil }

func TestConsumer_SweepDLQ_XAckFailure(t *testing.T) {
	mock := &xAckFailClient{}

	observedZapCore, observedLogs := observer.New(zap.ErrorLevel)
	logger := zap.New(observedZapCore)

	c := NewConsumer(mock, nil, logger, "test-consumer")
	ctx := context.Background()

	before := testutil.ToFloat64(streamEvictedTotal)
	c.sweepDLQ(ctx)
	after := testutil.ToFloat64(streamEvictedTotal)

	if after != before+1 {
		t.Errorf("expected streamEvictedTotal to increment by 1, got %f -> %f", before, after)
	}

	if !mock.xAddCalled {
		t.Error("expected XAdd to be called")
	}
	if !mock.xAckCalled {
		t.Error("expected XAck to be called")
	}
	if mock.xDelCalled {
		t.Error("expected XDel to NOT be called when XAck fails")
	}

	found := false
	for _, log := range observedLogs.All() {
		if log.Message == "dlq sweep: failed to ack original message after successful dlq add — message may be duplicated" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected critical error log about potential message duplication")
	}
}

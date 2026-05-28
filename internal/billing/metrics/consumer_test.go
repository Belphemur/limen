package metrics

import (
	"context"
	"testing"

	"github.com/belphemur/limen/internal/valkey"
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

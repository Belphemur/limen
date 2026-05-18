package codemode

import (
	"context"
	"testing"
)

// TestSchemas exercises codemode.schemas(names) for both string and
// []string inputs, and asserts unknown names are silently omitted (so
// a typo in one name does not poison the whole batch).
func TestSchemas(t *testing.T) {
	tools := []Tool{
		{Name: "a", Upstream: "u1", InputSchema: map[string]any{"type": "object", "required": []any{"x"}}},
		{Name: "b", Upstream: "u2", InputSchema: map[string]any{"type": "object"}},
	}
	d := &fakeDispatcher{tools: tools}
	h := newTestHandler(t, d, Config{})

	t.Run("single string", func(t *testing.T) {
		got, err := h.Search(context.Background(), `codemode.schemas('a')`)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		arr, ok := got.([]ToolSchema)
		if !ok || len(arr) != 1 || arr[0].Name != "a" || arr[0].Upstream != "u1" {
			t.Fatalf("unexpected: %#v", got)
		}
	})

	t.Run("array with unknown", func(t *testing.T) {
		got, err := h.Search(context.Background(), `codemode.schemas(['a','nope','b']).map(s=>s.name)`)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		arr := got.([]any)
		if len(arr) != 2 || arr[0] != "a" || arr[1] != "b" {
			t.Errorf("unknown name should be omitted: %#v", arr)
		}
	})

	t.Run("wrong type rejects", func(t *testing.T) {
		_, err := h.Search(context.Background(), `codemode.schemas(123)`)
		if err == nil {
			t.Fatal("expected rejection for non-string argument")
		}
	})

	t.Run("missing arg rejects", func(t *testing.T) {
		_, err := h.Search(context.Background(), `codemode.schemas()`)
		if err == nil {
			t.Fatal("expected rejection for missing argument")
		}
	})
}

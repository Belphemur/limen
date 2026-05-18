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
		got, err := h.Search(context.Background(), `codemode.schemas('a').found`)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		arr, ok := got.([]ToolSchema)
		if !ok || len(arr) != 1 || arr[0].Name != "a" || arr[0].Upstream != "u1" {
			t.Fatalf("unexpected: %#v", got)
		}
	})

	t.Run("array with unknown", func(t *testing.T) {
		got, err := h.Search(context.Background(), `codemode.schemas(['a','nope','b']).found.map(s=>s.name)`)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		arr := got.([]any)
		if len(arr) != 2 || arr[0] != "a" || arr[1] != "b" {
			t.Errorf("unknown name should be omitted from found: %#v", arr)
		}
	})

	t.Run("missing names reported", func(t *testing.T) {
		got, err := h.Search(context.Background(), `codemode.schemas(['a','nope','also_nope']).missing`)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		arr, ok := got.([]string)
		if !ok || len(arr) != 2 || arr[0] != "nope" || arr[1] != "also_nope" {
			t.Fatalf("expected missing=[nope, also_nope], got %#v", got)
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

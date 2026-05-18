package codemode

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNamespacedDispatch(t *testing.T) {
	d := &fakeDispatcher{
		tools: []Tool{
			{Name: "search", Upstream: "github"},
			{Name: "search", Upstream: "gitlab"},
		},
		responses: map[string]any{
			"github/search": "gh-result",
			"gitlab/search": "gl-result",
		},
	}
	h := newTestHandler(t, d, Config{})
	result, err := h.Execute(context.Background(), `[codemode.github.search({q:"foo"}), codemode.gitlab.search({q:"bar"})]`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	arr, ok := result.([]any)
	if !ok || len(arr) != 2 || arr[0] != "gh-result" || arr[1] != "gl-result" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(d.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(d.calls))
	}
	if d.calls[0].Upstream != "github" || d.calls[0].Args["q"] != "foo" {
		t.Errorf("call[0] = %+v", d.calls[0])
	}
	if d.calls[1].Upstream != "gitlab" || d.calls[1].Args["q"] != "bar" {
		t.Errorf("call[1] = %+v", d.calls[1])
	}
}

func TestCallRequiresTwoArgs(t *testing.T) {
	d := &fakeDispatcher{
		tools:     []Tool{{Name: "search", Upstream: "github"}},
		responses: map[string]any{"github/search": "ok"},
	}
	h := newTestHandler(t, d, Config{})

	// 2-arg form succeeds.
	if _, err := h.Execute(context.Background(), `codemode.call("github", "search", {q:"x"})`); err != nil {
		t.Fatalf("2-arg call: %v", err)
	}

	// 1-arg form errors.
	if _, err := h.Execute(context.Background(), `codemode.call("github")`); err == nil {
		t.Fatal("expected error for 1-arg call, got nil")
	}

	// Missing upstream errors with the documented message.
	_, err := h.Execute(context.Background(), `codemode.call("nope", "x", {})`)
	if err == nil || !strings.Contains(err.Error(), "not found or no tools visible") {
		t.Fatalf("expected 'not found or no tools visible', got %v", err)
	}
}

func TestReservedUpstreamNamesAreSkippedFromPropertyChain(t *testing.T) {
	d := &fakeDispatcher{
		tools: []Tool{
			{Name: "x", Upstream: "tools"},
			{Name: "y", Upstream: "call"},
			{Name: "z", Upstream: "normal"},
		},
		responses: map[string]any{
			"tools/x":  "via-call",
			"call/y":   "via-call",
			"normal/z": "direct",
		},
	}
	h := newTestHandler(t, d, Config{})

	// Property chain access to a reserved name should fail (codemode.tools
	// is the function, not the upstream).
	if _, err := h.Execute(context.Background(), `codemode.tools.x({})`); err == nil {
		t.Fatal("expected error reaching reserved upstream via property chain")
	}

	// But codemode.call("tools", ...) still works.
	got, err := h.Execute(context.Background(), `codemode.call("tools", "x", {})`)
	if err != nil {
		t.Fatalf("call('tools', ...): %v", err)
	}
	if got != "via-call" {
		t.Errorf("got %v, want via-call", got)
	}

	// Normal upstream still accessible via property chain.
	got, err = h.Execute(context.Background(), `codemode.normal.z({})`)
	if err != nil {
		t.Fatalf("property-chain normal: %v", err)
	}
	if got != "direct" {
		t.Errorf("got %v, want direct", got)
	}

	// codemode.tools() still returns the catalog (function, not object).
	got, err = h.Execute(context.Background(), `codemode.tools().length`)
	if err != nil {
		t.Fatalf("tools(): %v", err)
	}
	if got != int64(3) {
		t.Errorf("tools().length = %v, want 3", got)
	}
}

func TestQuotaEnforced(t *testing.T) {
	d := &fakeDispatcher{
		tools:     []Tool{{Name: "search", Upstream: "github"}},
		responses: map[string]any{"github/search": "ok"},
	}
	h := newTestHandler(t, d, Config{MaxToolCalls: 2})
	_, err := h.Execute(context.Background(), `
		codemode.github.search({});
		codemode.github.search({});
		codemode.github.search({});
	`)
	if err == nil || !strings.Contains(err.Error(), "max_tool_calls exceeded") {
		t.Fatalf("expected quota error, got %v", err)
	}
	if len(d.calls) != 2 {
		t.Errorf("expected dispatcher invoked 2x before quota trip, got %d", len(d.calls))
	}
}

func TestToolError_PropagatedToScript(t *testing.T) {
	d := &fakeDispatcher{
		tools: []Tool{{Name: "search", Upstream: "github"}},
		errs:  map[string]error{"github/search": errors.New("link not found")},
	}
	h := newTestHandler(t, d, Config{})
	_, err := h.Execute(context.Background(), `codemode.github.search({})`)
	if err == nil {
		t.Fatal("expected tool error to surface")
	}
	if !strings.Contains(err.Error(), "search") {
		t.Errorf("expected tool name in wrapped error, got %v", err)
	}
}

func TestIsReservedCodemodeKey(t *testing.T) {
	for _, name := range []string{"tools", "call"} {
		if !isReservedCodemodeKey(name) {
			t.Errorf("%q should be reserved", name)
		}
	}
	for _, name := range []string{"github", "gitlab", "search", "Tools", "CALL"} {
		if isReservedCodemodeKey(name) {
			t.Errorf("%q should not be reserved", name)
		}
	}
}

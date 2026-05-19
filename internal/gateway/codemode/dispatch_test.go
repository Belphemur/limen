package codemode

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
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
	result, err := h.Execute(context.Background(), `(async () => Promise.all([codemode.github.search({q:"foo"}), codemode.gitlab.search({q:"bar"})]))()`)
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
	// Promise.all dispatches the proxies on the event loop in script
	// order, but the underlying workers race in goroutines, so the
	// recorded order on d.calls is non-deterministic. Assert as a set.
	seen := map[string]string{}
	for _, c := range d.calls {
		seen[c.Upstream] = c.Args["q"].(string)
	}
	if seen["github"] != "foo" {
		t.Errorf("github call args = %q, want %q", seen["github"], "foo")
	}
	if seen["gitlab"] != "bar" {
		t.Errorf("gitlab call args = %q, want %q", seen["gitlab"], "bar")
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
	_, err := h.Execute(context.Background(), `(async () => {
		await codemode.github.search({});
		await codemode.github.search({});
		await codemode.github.search({});
	})()`)
	if err == nil || !strings.Contains(err.Error(), "max_tool_calls exceeded") {
		t.Fatalf("expected quota error, got %v", err)
	}
	if len(d.calls) != 2 {
		t.Errorf("expected dispatcher invoked 2x before quota trip, got %d", len(d.calls))
	}
}

// TestQuotaIsUncatchable verifies the quota trip cannot be swallowed by
// a JS try/catch — it's raised via vm.Interrupt, which delivers an
// uncatchable InterruptedError. A pre-vm.Interrupt implementation that
// used panic(vm.NewGoError(...)) would let the script ignore the quota.
func TestQuotaIsUncatchable(t *testing.T) {
	d := &fakeDispatcher{
		tools:     []Tool{{Name: "search", Upstream: "github"}},
		responses: map[string]any{"github/search": "ok"},
	}
	h := newTestHandler(t, d, Config{MaxToolCalls: 1})
	_, err := h.Execute(context.Background(), `
		(async () => {
			try { codemode.github.search({}); } catch (e) {}
			try { codemode.github.search({}); } catch (e) { return "swallowed"; }
			return "never";
		})()
	`)
	if err == nil {
		t.Fatal("expected quota error to abort the script despite try/catch")
	}
	if !strings.Contains(err.Error(), "max_tool_calls exceeded") {
		t.Errorf("expected quota sentinel in error, got %v", err)
	}
}

func TestCodemodeJSON(t *testing.T) {
	d := &fakeDispatcher{
		tools: []Tool{{Name: "search", Upstream: "github"}},
		responses: map[string]any{
			"github/search": []any{map[string]any{"type": "text", "text": `{"hits":42}`}},
		},
	}
	h := newTestHandler(t, d, Config{})
	got, err := h.Execute(context.Background(), `
		(async () => {
			const r = await codemode.github.search({});
			return codemode.json(r);
		})()
	`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	m, ok := got.(map[string]any)
	if !ok || m["hits"] != float64(42) {
		t.Fatalf("expected {hits:42}, got %#v", got)
	}
}

func TestCodemodeJSON_FullResult(t *testing.T) {
	// Real dispatchers hand back *mcp.CallToolResult, not the bare
	// content array. Simulate that shape with a map so codemode.json
	// has to dig out .content itself.
	d := &fakeDispatcher{
		tools: []Tool{{Name: "search", Upstream: "github"}},
		responses: map[string]any{
			"github/search": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": `{"hits":42}`}},
				"isError": false,
			},
		},
	}
	h := newTestHandler(t, d, Config{})
	got, err := h.Execute(context.Background(), `
		(async () => codemode.json(await codemode.github.search({})))()
	`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	m, ok := got.(map[string]any)
	if !ok || m["hits"] != float64(42) {
		t.Fatalf("expected {hits:42}, got %#v", got)
	}
}

func TestCodemodeJSON_FallbackRaw(t *testing.T) {
	d := &fakeDispatcher{
		tools: []Tool{{Name: "search", Upstream: "github"}},
		responses: map[string]any{
			"github/search": []any{map[string]any{"type": "text", "text": "not json"}},
		},
	}
	h := newTestHandler(t, d, Config{})
	got, err := h.Execute(context.Background(), `
		(async () => codemode.json(await codemode.github.search({})))()
	`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	m, ok := got.(map[string]any)
	if !ok || m["raw"] != "not json" {
		t.Fatalf("expected {raw:'not json'}, got %#v", got)
	}
}

func TestCodemodeQuota(t *testing.T) {
	d := &fakeDispatcher{
		tools:     []Tool{{Name: "search", Upstream: "github"}},
		responses: map[string]any{"github/search": "ok"},
	}
	h := newTestHandler(t, d, Config{MaxToolCalls: 10, ScriptTimeout: 5 * time.Second})
	got, err := h.Execute(context.Background(), `
		(async () => {
			const before = codemode.quota();
			await codemode.github.search({});
			await codemode.github.search({});
			const after = codemode.quota();
			return { before, after };
		})()
	`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	m := got.(map[string]any)
	before := m["before"].(map[string]any)
	after := m["after"].(map[string]any)
	if before["used"] != int64(0) || before["max"] != int64(10) || before["remaining"] != int64(10) {
		t.Errorf("unexpected before: %#v", before)
	}
	if after["used"] != int64(2) || after["remaining"] != int64(8) {
		t.Errorf("unexpected after: %#v", after)
	}
	if dm, _ := after["deadline_ms"].(int64); dm <= 0 {
		t.Errorf("expected deadline_ms > 0, got %v", after["deadline_ms"])
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
	for _, name := range []string{"tools", "schemas", "call", "json", "quota"} {
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

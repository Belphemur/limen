package codemode

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// fakeDispatcher is a hand-rolled Dispatcher for unit tests. It records
// every CallTool invocation in order and returns scripted results /
// errors keyed by (upstream, name).
type fakeDispatcher struct {
	tools     []Tool
	toolsErr  error
	responses map[string]any
	errs      map[string]error
	calls     []dispatchCall
}

type dispatchCall struct {
	Upstream string
	Tool     string
	Args     map[string]any
}

func (f *fakeDispatcher) ToolsForUser(_ context.Context) ([]Tool, error) {
	if f.toolsErr != nil {
		return nil, f.toolsErr
	}
	return f.tools, nil
}

func (f *fakeDispatcher) CallTool(_ context.Context, upstream, name string, args map[string]any) (any, error) {
	f.calls = append(f.calls, dispatchCall{Upstream: upstream, Tool: name, Args: args})
	key := upstream + "/" + name
	if err, ok := f.errs[key]; ok {
		return nil, err
	}
	if r, ok := f.responses[key]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("fakeDispatcher: unexpected call %q", key)
}

func newTestHandler(t *testing.T, d *fakeDispatcher, cfg Config) *Handler {
	t.Helper()
	if cfg.ScriptTimeout == 0 {
		cfg.ScriptTimeout = 2 * time.Second
	}
	return New(d, cfg, zap.NewNop())
}

func TestScriptTimeout(t *testing.T) {
	d := &fakeDispatcher{}
	h := newTestHandler(t, d, Config{ScriptTimeout: 50 * time.Millisecond})
	_, err := h.Execute(context.Background(), `while (true) {}`)
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestSandboxDenials(t *testing.T) {
	d := &fakeDispatcher{}
	h := newTestHandler(t, d, Config{})
	// NOTE: `eval` and `globalThis` are still reachable today — they are
	// ECMAScript built-ins that Goja installs unconditionally. Lock down
	// here only what the sandbox actually denies; expand once the
	// sandbox-hardening follow-up lands.
	denied := []string{
		"fs", "process", "fetch", "require", "setTimeout",
		"setInterval", "console", "window", "Buffer", "XMLHttpRequest",
		"WebSocket", "__dirname", "__filename", "global",
	}
	for _, name := range denied {
		t.Run(name, func(t *testing.T) {
			_, err := h.Execute(context.Background(), name+";")
			if err == nil {
				t.Fatalf("expected reference error for %q", name)
			}
			// Goja surfaces these as ReferenceError; codemode wraps them.
			if !strings.Contains(strings.ToLower(err.Error()), "not defined") &&
				!strings.Contains(strings.ToLower(err.Error()), "referenceerror") {
				t.Errorf("expected reference error for %q, got %v", name, err)
			}
		})
	}
}

// TestAsyncArrowFunction_IsInvoked verifies the contract advertised by
// the codemode_search / codemode_execute tool descriptions: an
// `async () => { ... }` expression is the entry point, gets invoked
// once, and its returned promise is awaited.
func TestAsyncArrowFunction_IsInvoked(t *testing.T) {
	tools := []Tool{
		{Name: "search", Upstream: "github", InputSchema: map[string]any{"type": "object"}},
		{Name: "search", Upstream: "gitlab", InputSchema: map[string]any{"type": "object"}},
	}
	d := &fakeDispatcher{tools: tools}
	h := newTestHandler(t, d, Config{})

	got, err := h.Search(context.Background(), `async () => {
		const t = await codemode.tools();
		return { total: t.length, upstreams: [...new Set(t.map(x => x.upstream))].sort() };
	}`)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("want map result, got %T: %#v", got, got)
	}
	if m["total"] != int64(2) && m["total"] != float64(2) {
		t.Errorf("total: got %v want 2", m["total"])
	}
}

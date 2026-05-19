package codemode

import (
	"context"
	"fmt"
	"strings"
	"sync"
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
	upstreams []UpstreamMeta
	responses map[string]any
	errs      map[string]error
	mu        sync.Mutex
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

func (f *fakeDispatcher) UpstreamsForUser(_ context.Context) ([]UpstreamMeta, error) {
	if f.upstreams != nil {
		return f.upstreams, nil
	}
	// Default: synthesize one meta per distinct upstream in tools,
	// with empty aliases/context. This keeps tests that only set
	// `tools` working without ceremony.
	seen := map[string]struct{}{}
	out := make([]UpstreamMeta, 0)
	for _, t := range f.tools {
		if _, ok := seen[t.Upstream]; ok {
			continue
		}
		seen[t.Upstream] = struct{}{}
		out = append(out, UpstreamMeta{Name: t.Upstream})
	}
	return out, nil
}

func (f *fakeDispatcher) CallTool(_ context.Context, upstream, name string, args map[string]any) (any, error) {
	f.mu.Lock()
	f.calls = append(f.calls, dispatchCall{Upstream: upstream, Tool: name, Args: args})
	f.mu.Unlock()
	key := upstream + "/" + name
	if err, ok := f.errs[key]; ok {
		return nil, err
	}
	if r, ok := f.responses[key]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("fakeDispatcher: unexpected call %q", key)
}

func newTestHandler(t *testing.T, d Dispatcher, cfg Config) *Handler {
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
		"setInterval", "setImmediate",
		"clearTimeout", "clearInterval", "clearImmediate",
		"queueMicrotask",
		"console", "window", "Buffer", "XMLHttpRequest",
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
		const r = await codemode.tools();
		let total = 0;
		for (const g of r.upstreams) total += g.tools.length;
		return { total, upstreams: r.upstreams.map(g => g.name).sort() };
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

// TestAliasProxy_CallsResolveSameAsCanonical verifies the §2 contract:
// a tool dispatched via an alias proxy (codemode.<alias>.<tool>) lands
// on the same Dispatcher.CallTool invocation as the canonical proxy
// (codemode.<canonical>.<tool>), with the canonical upstream name on
// both call records. Quota accounting is unchanged because both paths
// go through the same dispatch wrapper.
func TestAliasProxy_CallsResolveSameAsCanonical(t *testing.T) {
	tools := []Tool{{Name: "jira_search", Upstream: "atlassian"}}
	d := &fakeDispatcher{
		tools: tools,
		upstreams: []UpstreamMeta{{
			Name:    "atlassian",
			Aliases: []string{"jira"},
			Context: map[string]any{},
		}},
		responses: map[string]any{"atlassian/jira_search": "ok"},
	}
	h := newTestHandler(t, d, Config{})

	if _, err := h.Execute(context.Background(), `(async () => {
		const a = await codemode.atlassian.jira_search({via: "canonical"});
		const b = await codemode.jira.jira_search({via: "alias"});
		return [a, b];
	})()`); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	d.mu.Lock()
	calls := append([]dispatchCall(nil), d.calls...)
	d.mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want 2: %#v", len(calls), calls)
	}
	for i, c := range calls {
		if c.Upstream != "atlassian" {
			t.Errorf("call[%d].Upstream = %q, want atlassian (alias must resolve to canonical)", i, c.Upstream)
		}
		if c.Tool != "jira_search" {
			t.Errorf("call[%d].Tool = %q, want jira_search", i, c.Tool)
		}
	}
	if calls[0].Args["via"] != "canonical" || calls[1].Args["via"] != "alias" {
		t.Errorf("args not preserved through alias proxy: %#v", calls)
	}
}

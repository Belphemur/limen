package gateway

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// fakeDispatcher is a hand-rolled toolDispatcher for unit tests. It
// records every CallTool invocation in order and returns scripted
// results / errors keyed by (upstream, name).
type fakeDispatcher struct {
	tools     []ToolEntry
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

func (f *fakeDispatcher) ToolsForUser(_ context.Context) ([]ToolEntry, error) {
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

func newTestHandler(t *testing.T, d *fakeDispatcher, cfg CodeModeConfig) *CodeModeHandler {
	t.Helper()
	if cfg.ScriptTimeout == 0 {
		cfg.ScriptTimeout = 2 * time.Second
	}
	return newCodeModeHandler(d, cfg, zap.NewNop())
}

func TestCodeMode_NamespacedDispatch(t *testing.T) {
	d := &fakeDispatcher{
		tools: []ToolEntry{
			{Name: "search", Upstream: "github"},
			{Name: "search", Upstream: "gitlab"},
		},
		responses: map[string]any{
			"github/search": "gh-result",
			"gitlab/search": "gl-result",
		},
	}
	h := newTestHandler(t, d, CodeModeConfig{})
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

func TestCodeMode_CallRequiresTwoArgs(t *testing.T) {
	d := &fakeDispatcher{
		tools:     []ToolEntry{{Name: "search", Upstream: "github"}},
		responses: map[string]any{"github/search": "ok"},
	}
	h := newTestHandler(t, d, CodeModeConfig{})

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

func TestCodeMode_ReservedUpstreamNamesAreSkippedFromPropertyChain(t *testing.T) {
	d := &fakeDispatcher{
		tools: []ToolEntry{
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
	h := newTestHandler(t, d, CodeModeConfig{})

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

func TestCodeMode_QuotaEnforced(t *testing.T) {
	d := &fakeDispatcher{
		tools:     []ToolEntry{{Name: "search", Upstream: "github"}},
		responses: map[string]any{"github/search": "ok"},
	}
	h := newTestHandler(t, d, CodeModeConfig{MaxToolCalls: 2})
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

func TestCodeMode_ScriptTimeout(t *testing.T) {
	d := &fakeDispatcher{}
	h := newTestHandler(t, d, CodeModeConfig{ScriptTimeout: 50 * time.Millisecond})
	_, err := h.Execute(context.Background(), `while (true) {}`)
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestCodeMode_SandboxDenials(t *testing.T) {
	d := &fakeDispatcher{}
	h := newTestHandler(t, d, CodeModeConfig{})
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

func TestCodeMode_ToolError_PropagatedToScript(t *testing.T) {
	d := &fakeDispatcher{
		tools: []ToolEntry{{Name: "search", Upstream: "github"}},
		errs:  map[string]error{"github/search": errors.New("link not found")},
	}
	h := newTestHandler(t, d, CodeModeConfig{})
	_, err := h.Execute(context.Background(), `codemode.github.search({})`)
	if err == nil {
		t.Fatal("expected tool error to surface")
	}
	if !strings.Contains(err.Error(), "search") {
		t.Errorf("expected tool name in wrapped error, got %v", err)
	}
}

func TestClassifyToolError(t *testing.T) {
	cases := []struct {
		msg         string
		wantKind    string
		wantOutcome string
	}{
		{"upstream: link needs re-link", "needs_relink", "denied_no_link"},
		{"needs_relink for github", "needs_relink", "denied_no_link"},
		{"link not found", "no_link", "denied_no_link"},
		{"no link for tenant", "no_link", "denied_no_link"},
		{"upstream auto_disabled at 5xx threshold", "auto_disabled", "denied_auto_disabled"},
		{"upstream auto-disabled", "auto_disabled", "denied_auto_disabled"},
		{"upstream_unavailable: breaker open", "upstream_unavailable", "upstream_error"},
		{"breaker tripped", "upstream_unavailable", "upstream_error"},
		{"random 500 from upstream", "upstream_error", "upstream_error"},
	}
	for _, tc := range cases {
		t.Run(tc.msg, func(t *testing.T) {
			kind, outcome := classifyToolError(errors.New(tc.msg))
			if kind != tc.wantKind || outcome != tc.wantOutcome {
				t.Errorf("got (%q, %q), want (%q, %q)", kind, outcome, tc.wantKind, tc.wantOutcome)
			}
		})
	}
}

func TestRedactSecrets(t *testing.T) {
	cases := []struct {
		in       string
		mustNot  string
		contains string
	}{
		{"Authorization: abc.def.ghi", "abc.def.ghi", "[REDACTED]"},
		{"Bearer eyJhbGciOi.payload.sig", "eyJhbGciOi", "[REDACTED]"},
		{"Cookie: session=abc123", "abc123", "[REDACTED]"},
		{"Set-Cookie: x=y; Path=/", "x=y", "[REDACTED]"},
		{`{"access_token":"abc","x":1}`, `"abc"`, "[REDACTED]"},
		{`{"refresh_token":"xyz"}`, `"xyz"`, "[REDACTED]"},
		{`{"api_key":"k"}`, `"k"`, "[REDACTED]"},
		{`{"client_secret":"s"}`, `"s"`, "[REDACTED]"},
		{`{"password":"p"}`, `"p"`, "[REDACTED]"},
		{"access_token=abc123&q=1", "abc123", "[REDACTED]"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			out := redactSecrets(tc.in)
			if tc.mustNot != "" && strings.Contains(out, tc.mustNot) {
				t.Errorf("expected %q to be scrubbed from %q, got %q", tc.mustNot, tc.in, out)
			}
			if !strings.Contains(out, tc.contains) {
				t.Errorf("expected %q in %q", tc.contains, out)
			}
		})
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

func TestCodeMode_ToolsCatalogShape(t *testing.T) {
	tools := []ToolEntry{
		{Name: "search", Description: "find stuff", Upstream: "github", InputSchema: map[string]any{"type": "object"}},
	}
	d := &fakeDispatcher{tools: tools}
	h := newTestHandler(t, d, CodeModeConfig{})
	got, err := h.Search(context.Background(), `codemode.tools()`)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	defs, ok := got.([]ToolDefinition)
	if !ok || len(defs) != 1 {
		t.Fatalf("unexpected: %#v", got)
	}
	want := ToolDefinition{
		Name:        "search",
		Description: "find stuff",
		Upstream:    "github",
		InputSchema: map[string]any{"type": "object"},
	}
	if !reflect.DeepEqual(defs[0], want) {
		t.Errorf("entry mismatch:\n got: %#v\nwant: %#v", defs[0], want)
	}
}

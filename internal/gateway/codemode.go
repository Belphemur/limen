package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/ids"
	"github.com/belphemur/limen/internal/tenancy"
)

// CodeModeConfig is the subset of config.CodeModeConfig the handler
// needs. Kept here so the gateway package doesn't import config.
type CodeModeConfig struct {
	// ScriptTimeout caps wall-clock for a single invocation.
	ScriptTimeout time.Duration
	// MaxToolCalls caps the number of upstream tool calls per
	// invocation. 0 means unlimited (not recommended).
	MaxToolCalls int
}

// CodeModeHandler runs tenant-supplied JavaScript through Goja with the
// per-user upstream tool catalog injected as `codemode.*`. All tool
// dispatch goes through the Manager, so the per-user auth-injecting
// transport, per-link health bookkeeping, and Phase 10 resilience all
// apply by construction.
type CodeModeHandler struct {
	manager toolDispatcher
	logger  *zap.Logger
	cfg     CodeModeConfig
}

// toolDispatcher is the minimal surface CodeModeHandler needs from a
// Manager. Carved out so unit tests can supply a fake without standing
// up a real Postgres-backed Manager. *Manager satisfies this by
// construction.
type toolDispatcher interface {
	ToolsForUser(ctx context.Context) ([]ToolEntry, error)
	CallTool(ctx context.Context, upstream, name string, args map[string]any) (any, error)
}

// NewCodeModeHandler wires a CodeModeHandler over a Manager.
func NewCodeModeHandler(mgr *Manager, cfg CodeModeConfig, logger *zap.Logger) *CodeModeHandler {
	return newCodeModeHandler(mgr, cfg, logger)
}

func newCodeModeHandler(mgr toolDispatcher, cfg CodeModeConfig, logger *zap.Logger) *CodeModeHandler {
	if logger == nil {
		logger = zap.NewNop()
	}
	if cfg.ScriptTimeout <= 0 {
		cfg.ScriptTimeout = 10 * time.Second
	}
	return &CodeModeHandler{manager: mgr, logger: logger, cfg: cfg}
}

// ToolDefinition is the shape exposed to the sandbox via codemode.tools().
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Upstream    string         `json:"upstream"`
}

// errQuotaExceeded is the marker error a tool proxy panics with when the
// per-invocation tool-call cap is reached.
var errQuotaExceeded = errors.New("codemode: max_tool_calls exceeded")

// Search runs `code` with codemode.tools() exposed. No proxies.
func (h *CodeModeHandler) Search(ctx context.Context, code string) (any, error) {
	return h.run(ctx, code, false)
}

// Execute runs `code` with codemode.tools() plus per-tool proxies
// (codemode.<tool>(args), codemode.call(name, args)).
func (h *CodeModeHandler) Execute(ctx context.Context, code string) (any, error) {
	return h.run(ctx, code, true)
}

func (h *CodeModeHandler) run(ctx context.Context, code string, withProxies bool) (any, error) {
	invocationID := ids.New(ids.PrefixCodemodeInvocation)
	base := h.baseLogFields(ctx, invocationID)

	tools, err := h.manager.ToolsForUser(ctx)
	if err != nil {
		h.logger.Error("codemode.invocation.failed_to_load_tools",
			append(base, zap.Error(err))...)
		return nil, fmt.Errorf("codemode: load tools: %w", err)
	}

	h.logger.Info("codemode.invocation.started", append(base,
		zap.String("script_sha256", sha256Hex([]byte(code))),
		zap.Int("script_bytes", len(code)),
		zap.Int("tool_count_visible", len(tools)),
	)...)

	start := time.Now()
	vm := goja.New()
	var callSeq int64
	codemodeObj := vm.NewObject()

	defs := toToolDefs(tools)

	if withProxies {
		byUpstream := make(map[string]map[string]ToolEntry, len(tools))
		for _, t := range tools {
			if _, ok := byUpstream[t.Upstream]; !ok {
				byUpstream[t.Upstream] = make(map[string]ToolEntry)
			}
			byUpstream[t.Upstream][t.Name] = t
		}
		for upstreamName, byName := range byUpstream {
			if isReservedCodemodeKey(upstreamName) {
				h.logger.Warn("codemode.invocation.upstream_name_collides_with_reserved_key",
					append(base, zap.String("upstream", upstreamName))...)
				continue
			}
			upObj := vm.NewObject()
			for _, t := range byName {
				tool := t
				_ = upObj.Set(tool.Name, h.makeProxy(ctx, vm, tool, &callSeq, invocationID))
			}
			_ = codemodeObj.Set(upstreamName, upObj)
		}
		_ = codemodeObj.Set("call", func(call goja.FunctionCall) goja.Value {
			if len(call.Arguments) < 2 {
				panic(vm.NewGoError(errors.New("codemode.call(upstream, name, args): upstream and name are required")))
			}
			upstreamName := call.Argument(0).String()
			name := call.Argument(1).String()
			byName, ok := byUpstream[upstreamName]
			if !ok {
				panic(vm.NewGoError(fmt.Errorf("upstream %q not found or no tools visible", upstreamName)))
			}
			tool, ok := byName[name]
			if !ok {
				panic(vm.NewGoError(fmt.Errorf("tool %q not found on upstream %q", name, upstreamName)))
			}
			args := exportArgs(call, 2)
			return h.dispatchTool(ctx, vm, tool, args, &callSeq, invocationID)
		})
	}

	// Reserved keys are set last so an upstream named "tools" or "call"
	// can never shadow the sandbox API.
	_ = codemodeObj.Set("tools", func() []ToolDefinition { return defs })

	_ = vm.Set("codemode", codemodeObj)

	result, runErr := h.runCode(ctx, vm, code)
	durMS := time.Since(start).Milliseconds()
	totalCalls := atomic.LoadInt64(&callSeq)

	outcome := "ok"
	if runErr != nil {
		switch {
		case errors.Is(runErr, context.Canceled), errors.Is(runErr, context.DeadlineExceeded):
			outcome = "timeout"
		default:
			outcome = "script_error"
		}
	}

	h.logger.Info("codemode.invocation.completed", append(base,
		zap.Int64("tool_calls_total", totalCalls),
		zap.Int64("duration_ms", durMS),
		zap.String("outcome", outcome),
		zap.Int("result_bytes", approxResultBytes(result)),
	)...)

	return result, runErr
}

func (h *CodeModeHandler) makeProxy(ctx context.Context, vm *goja.Runtime, tool ToolEntry, callSeq *int64, invocationID string) func(call goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		args := exportArgs(call, 0)
		return h.dispatchTool(ctx, vm, tool, args, callSeq, invocationID)
	}
}

func (h *CodeModeHandler) dispatchTool(ctx context.Context, vm *goja.Runtime, tool ToolEntry, args map[string]any, callSeq *int64, invocationID string) goja.Value {
	seq := atomic.AddInt64(callSeq, 1)
	base := append(h.baseLogFields(ctx, invocationID),
		zap.String("upstream", tool.Upstream),
		zap.String("tool", tool.Name),
		zap.Int64("call_seq", seq),
	)

	if h.cfg.MaxToolCalls > 0 && seq > int64(h.cfg.MaxToolCalls) {
		h.logger.Error("codemode.tool.error", append(base,
			zap.String("error_kind", "quota_exceeded"),
			zap.String("error_message", redactSecrets(errQuotaExceeded.Error())),
		)...)
		panic(vm.NewGoError(errQuotaExceeded))
	}

	argsJSON, _ := json.Marshal(args)
	h.logger.Info("codemode.tool.called", append(base,
		zap.String("args_sha256", sha256Hex(argsJSON)),
		zap.Int("args_bytes", len(argsJSON)),
	)...)

	callStart := time.Now()
	result, err := h.manager.CallTool(ctx, tool.Upstream, tool.Name, args)
	callDur := time.Since(callStart).Milliseconds()

	if err != nil {
		kind, outcome := classifyToolError(err)
		h.logger.Error("codemode.tool.error", append(base,
			zap.String("error_kind", kind),
			zap.String("error_message", redactSecrets(err.Error())),
		)...)
		h.logger.Info("codemode.tool.completed", append(base,
			zap.Int("result_bytes", 0),
			zap.Int64("duration_ms", callDur),
			zap.String("outcome", outcome),
		)...)
		panic(vm.NewGoError(fmt.Errorf("tool %q failed: %w", tool.Name, err)))
	}

	h.logger.Info("codemode.tool.completed", append(base,
		zap.Int("result_bytes", approxResultBytes(result)),
		zap.Int64("duration_ms", callDur),
		zap.String("outcome", "ok"),
	)...)
	return vm.ToValue(result)
}

func (h *CodeModeHandler) runCode(ctx context.Context, vm *goja.Runtime, code string) (any, error) {
	resultCh := make(chan any, 1)
	errCh := make(chan error, 1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				if ex, ok := r.(*goja.Exception); ok {
					errCh <- fmt.Errorf("javascript error: %s", redactSecrets(ex.String()))
				} else {
					errCh <- fmt.Errorf("panic: %v", r)
				}
			}
		}()

		prg, err := goja.Compile("codemode", code, false)
		if err != nil {
			errCh <- fmt.Errorf("compile error: %w", err)
			return
		}
		val, err := vm.RunProgram(prg)
		if err != nil {
			errCh <- fmt.Errorf("execution error: %w", err)
			return
		}
		resultCh <- val.Export()
	}()

	timer := time.NewTimer(h.cfg.ScriptTimeout)
	defer timer.Stop()

	select {
	case result := <-resultCh:
		return result, nil
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		vm.Interrupt("ctx cancelled")
		return nil, ctx.Err()
	case <-timer.C:
		vm.Interrupt("script timeout")
		return nil, fmt.Errorf("codemode: script exceeded %v timeout", h.cfg.ScriptTimeout)
	}
}

func (h *CodeModeHandler) baseLogFields(ctx context.Context, invocationID string) []zap.Field {
	fields := []zap.Field{zap.String("codemode_invocation_id", invocationID)}
	if t, ok := tenancy.TenantFromContext(ctx); ok {
		fields = append(fields, zap.Int64("tenant_id", t.ID), zap.String("tenant_public_id", t.PublicID))
	}
	if u, ok := auth.MCPUserFromContext(ctx); ok {
		fields = append(fields, zap.Int64("user_id", u.ID))
	}
	return fields
}

func toToolDefs(in []ToolEntry) []ToolDefinition {
	out := make([]ToolDefinition, len(in))
	for i, t := range in {
		out[i] = ToolDefinition(t)
	}
	return out
}

// isReservedCodemodeKey reports whether the given name would collide
// with a top-level key on the `codemode` sandbox object. Upstream
// namespaces matching one of these are skipped (with a Warn log) so
// the sandbox API surface stays stable regardless of admin naming.
func isReservedCodemodeKey(name string) bool {
	switch name {
	case "tools", "call":
		return true
	}
	return false
}

func exportArgs(call goja.FunctionCall, idx int) map[string]any {
	if len(call.Arguments) <= idx {
		return nil
	}
	exported := call.Argument(idx).Export()
	if exported == nil {
		return nil
	}
	b, err := json.Marshal(exported)
	if err != nil {
		return nil
	}
	var args map[string]any
	_ = json.Unmarshal(b, &args)
	return args
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func approxResultBytes(v any) int {
	if v == nil {
		return 0
	}
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

// classifyToolError maps a Manager.CallTool error into (error_kind,
// outcome) tags for structured logs. Message text is redacted before it
// goes into logs; only the kind is reported in the clear.
func classifyToolError(err error) (kind string, outcome string) {
	msg := err.Error()
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "needs re-link"), strings.Contains(low, "needs_relink"):
		return "needs_relink", "denied_no_link"
	case strings.Contains(low, "link not found"), strings.Contains(low, "no link"):
		return "no_link", "denied_no_link"
	case strings.Contains(low, "auto_disabled"), strings.Contains(low, "auto-disabled"):
		return "auto_disabled", "denied_auto_disabled"
	case strings.Contains(low, "upstream_unavailable"), strings.Contains(low, "breaker"):
		return "upstream_unavailable", "upstream_error"
	default:
		return "upstream_error", "upstream_error"
	}
}

// redactSecrets scrubs anything that looks like a credential from a
// string before it lands in a log line. Applied at every codemode log
// call site that emits user-derived content (error messages, JS
// exception strings).
func redactSecrets(s string) string {
	if s == "" {
		return s
	}
	out := s
	for _, r := range secretREs {
		out = r.re.ReplaceAllString(out, r.repl)
	}
	return out
}

type secretRE struct {
	re   *regexp.Regexp
	repl string
}

var secretREs = func() []secretRE {
	patterns := []struct {
		pattern string
		repl    string
	}{
		{`(?i)authorization:\s*[^\s,;]+`, "authorization: [REDACTED]"},
		{`(?i)bearer\s+[A-Za-z0-9._\-]+`, "Bearer [REDACTED]"},
		{`(?i)set-cookie:\s*[^\r\n]+`, "set-cookie: [REDACTED]"},
		{`(?i)cookie:\s*[^\r\n]+`, "cookie: [REDACTED]"},
		{`(?i)"(access_token|refresh_token|api_key|client_secret|password)"\s*:\s*"[^"]*"`, `"$1":"[REDACTED]"`},
		{`(?i)\b(access_token|refresh_token|api_key|client_secret|password)=([^&\s"]+)`, `$1=[REDACTED]`},
	}
	out := make([]secretRE, 0, len(patterns))
	for _, p := range patterns {
		out = append(out, secretRE{regexp.MustCompile(p.pattern), p.repl})
	}
	return out
}()

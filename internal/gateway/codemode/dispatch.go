package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
	"go.uber.org/zap"
)

// errQuotaExceeded is the marker error a tool proxy raises (via
// vm.Interrupt — uncatchable from JS) when the per-invocation tool-call
// cap is reached.
var errQuotaExceeded = errors.New("codemode: max_tool_calls exceeded")

// Reserved top-level keys on the `codemode` sandbox object. Adding a
// new helper means adding its name here AND wiring it in
// (*Handler).run. isReservedCodemodeKey switches off this list so the
// two never drift.
const (
	reservedKeyTools   = "tools"
	reservedKeySchemas = "schemas"
	reservedKeyCall    = "call"
	reservedKeyJSON    = "json"
	reservedKeyQuota   = "quota"
)

func (h *Handler) makeProxy(ctx context.Context, vm *goja.Runtime, tool Tool, callSeq *int64, invocationID string) func(call goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		args := exportArgs(call, 0)
		return h.dispatchTool(ctx, vm, tool, args, callSeq, invocationID)
	}
}

func (h *Handler) dispatchTool(ctx context.Context, vm *goja.Runtime, tool Tool, args map[string]any, callSeq *int64, invocationID string) goja.Value {
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
		// vm.Interrupt schedules an InterruptedError on the next
		// bytecode step — uncatchable from JS try/catch, unlike
		// panic(vm.NewGoError(...)). Returning Undefined here lets the
		// native call frame unwind cleanly; the very next opcode
		// triggers the interrupt and aborts the script.
		vm.Interrupt(errQuotaExceeded)
		return goja.Undefined()
	}

	argsJSON, _ := json.Marshal(args)
	h.logger.Info("codemode.tool.called", append(base,
		zap.String("args_sha256", sha256Hex(argsJSON)),
		zap.Int("args_bytes", len(argsJSON)),
	)...)

	callStart := time.Now()
	result, err := h.dispatcher.CallTool(ctx, tool.Upstream, tool.Name, args)
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

func (h *Handler) runCode(ctx context.Context, vm *goja.Runtime, code string) (any, error) {
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
			errCh <- wrapExecutionError(err)
			return
		}
		// The advertised contract is that `code` evaluates to a
		// zero-argument async arrow function which the runtime invokes
		// and whose returned promise it awaits. Existing tests also use
		// bare expressions (e.g. `codemode.tools()`) that evaluate
		// directly to a value, so we support both: if the script's
		// value is callable we invoke it, otherwise we take the value
		// as-is. If the resulting value is a Promise we resolve it
		// synchronously (goja drains microtasks before the call
		// returns, so an async function with no truly async ops settles
		// immediately).
		if fn, ok := goja.AssertFunction(val); ok {
			ret, callErr := fn(goja.Undefined())
			if callErr != nil {
				errCh <- wrapExecutionError(callErr)
				return
			}
			val = ret
		}
		if p, ok := val.Export().(*goja.Promise); ok {
			switch p.State() {
			case goja.PromiseStateFulfilled:
				resultCh <- exportValue(p.Result())
				return
			case goja.PromiseStateRejected:
				errCh <- fmt.Errorf("execution error: %s", redactSecrets(p.Result().String()))
				return
			default:
				errCh <- fmt.Errorf("execution error: returned promise did not settle synchronously (no event loop in sandbox)")
				return
			}
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

// isReservedCodemodeKey reports whether the given name would collide
// with a top-level key on the `codemode` sandbox object. Upstream
// namespaces matching one of these are skipped (with a Warn log) so
// the sandbox API surface stays stable regardless of admin naming.
func isReservedCodemodeKey(name string) bool {
	switch name {
	case reservedKeyTools, reservedKeySchemas, reservedKeyCall, reservedKeyJSON, reservedKeyQuota:
		return true
	}
	return false
}

// exportArgs converts the idx-th JS argument into a map[string]any.
// Goja's Export() already returns map[string]any for plain JS objects
// — fast-path that case to avoid a JSON round-trip on every tool call.
// Nested goja-specific types still get normalized via JSON for hot
// edge cases.
func exportArgs(call goja.FunctionCall, idx int) map[string]any {
	if len(call.Arguments) <= idx {
		return nil
	}
	exported := call.Argument(idx).Export()
	if exported == nil {
		return nil
	}
	if m, ok := exported.(map[string]any); ok {
		return m
	}
	b, err := json.Marshal(exported)
	if err != nil {
		return nil
	}
	var args map[string]any
	_ = json.Unmarshal(b, &args)
	return args
}

// exportValue returns v.Export(), or nil if v is undefined/nil. Goja's
// Export() on goja.Undefined() returns nil, but on explicit undefined
// values it can return a typed nil that confuses downstream JSON
// encoding; coalescing to a plain nil here keeps the boundary clean.
func exportValue(v goja.Value) any {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	return v.Export()
}

// wrapExecutionError normalizes goja's error types for the JS runner.
// In particular, an *goja.InterruptedError carrying errQuotaExceeded
// (raised via vm.Interrupt to bypass JS try/catch) is unwrapped so the
// outer error message and tests can match on the underlying sentinel.
func wrapExecutionError(err error) error {
	var ie *goja.InterruptedError
	if errors.As(err, &ie) {
		if e, ok := ie.Value().(error); ok {
			return fmt.Errorf("execution error: %w", e)
		}
		return fmt.Errorf("execution error: interrupted: %v", ie.Value())
	}
	return fmt.Errorf("execution error: %w", err)
}

// parseJSONResult implements the codemode.json(result) helper. It
// accepts any of:
//   - the full MCP CallToolResult (the value tool proxies return),
//   - a plain {content: [...]} map,
//   - the bare content array [{type:"text", text:"..."}, ...],
//
// and returns JSON.parse(text) of the first text block. Non-JSON
// text falls back to { raw: "<text>" }. Anything we don't recognize
// is passed through unchanged so callers can chain safely.
//
// Designed to absorb the repetitive r?.content?.[0]?.text + JSON.parse
// boilerplate every codemode script writes around tool results.
func parseJSONResult(v goja.Value) any {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	exported := v.Export()
	arr := extractContentArray(exported)
	if arr == nil {
		return exported
	}
	for _, item := range arr {
		text, ok := contentBlockText(item)
		if !ok {
			continue
		}
		var parsed any
		if err := json.Unmarshal([]byte(text), &parsed); err == nil {
			return parsed
		}
		return map[string]any{"raw": text}
	}
	return exported
}

// extractContentArray pulls the MCP "content" slice out of value
// shapes the dispatcher might hand back. The tool proxy returns a
// *mcp.CallToolResult; Goja exports that as a typed Go pointer, so a
// direct []any type-assertion fails. We JSON-roundtrip in that case to
// normalize on a map and dig out "content".
func extractContentArray(v any) []any {
	switch x := v.(type) {
	case nil:
		return nil
	case []any:
		return x
	case map[string]any:
		if c, ok := x["content"].([]any); ok {
			return c
		}
		return nil
	}
	// Struct or pointer-to-struct (e.g. *mcp.CallToolResult): normalize
	// through JSON. Cheap relative to a tool call and keeps this helper
	// decoupled from the mcp-go types.
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	if c, ok := m["content"].([]any); ok {
		return c
	}
	return nil
}

// contentBlockText returns the text of an MCP content block when its
// type is "text" (or unspecified). Blocks of other types are skipped
// so callers iterate to the next one.
func contentBlockText(item any) (string, bool) {
	block, ok := item.(map[string]any)
	if !ok {
		return "", false
	}
	if typ, _ := block["type"].(string); typ != "" && typ != "text" {
		return "", false
	}
	text, ok := block["text"].(string)
	if !ok {
		return "", false
	}
	return text, true
}

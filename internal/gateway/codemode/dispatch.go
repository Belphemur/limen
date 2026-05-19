package codemode

import (
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

// makeProxy wires `codemode.<upstream>.<tool>(args)` to dispatchAsync.
// The returned function runs on the VM goroutine; vm is captured so we
// don't depend on a Runtime field of goja.FunctionCall (which doesn't
// exist).
func (h *Handler) makeProxy(s *invocationState, vm *goja.Runtime, tool Tool, base []zap.Field) func(call goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		args := exportArgs(call, 0)
		return h.dispatchAsync(s, vm, tool, args, base)
	}
}

// dispatchAsync returns a goja Promise immediately and performs the
// upstream tool call on a background goroutine. Resolution and
// rejection are routed back onto the VM goroutine via the event loop;
// the worker never touches the Runtime directly.
//
// The MaxToolCalls quota check runs synchronously on the VM goroutine
// before the worker spawns, so a script that exceeds the budget hits
// vm.Interrupt(errQuotaExceeded) — uncatchable from JS try/catch — and
// the offending call's worker is never created.
func (h *Handler) dispatchAsync(s *invocationState, vm *goja.Runtime, tool Tool, args map[string]any, baseFields []zap.Field) goja.Value {
	seq := atomic.AddInt64(s.callSeq, 1)
	base := append(append([]zap.Field(nil), baseFields...),
		zap.String("upstream", tool.Upstream),
		zap.String("tool", tool.Name),
		zap.Int64("call_seq", seq),
	)

	if h.cfg.MaxToolCalls > 0 && seq > int64(h.cfg.MaxToolCalls) {
		h.logger.Error("codemode.tool.error", append(base,
			zap.String("error_kind", "quota_exceeded"),
			zap.String("error_message", redactSecrets(errQuotaExceeded.Error())),
		)...)
		// vm.Interrupt makes the abort uncatchable from JS try/catch
		// — goja raises an InterruptedError on the next bytecode step
		// which propagates past `catch` and `.catch()`. That same
		// uncatchability means it never reaches our attachSettlement
		// reject handler either, so we also report the error directly
		// through the synchronous side channel to unblock the outer
		// run() select.
		vm.Interrupt(errQuotaExceeded)
		s.reportSyncErr(fmt.Errorf("execution error: %w", errQuotaExceeded))
		return goja.Undefined()
	}

	argsJSON, _ := json.Marshal(args)
	h.logger.Info("codemode.tool.called", append(base,
		zap.String("args_sha256", sha256Hex(argsJSON)),
		zap.Int("args_bytes", len(argsJSON)),
	)...)

	p, resolve, reject := vm.NewPromise()
	promiseVal := vm.ToValue(p)

	go func() {
		waitStart := time.Now()
		if err := s.sem.Acquire(s.ctx, 1); err != nil {
			waitMS := time.Since(waitStart).Milliseconds()
			h.logger.Error("codemode.tool.error", append(base,
				zap.String("error_kind", "ctx_cancelled"),
				zap.String("error_message", redactSecrets(err.Error())),
			)...)
			h.logger.Info("codemode.tool.completed", append(base,
				zap.Int("result_bytes", 0),
				zap.Int64("wait_ms", waitMS),
				zap.Int64("dispatch_ms", 0),
				zap.Int64("duration_ms", waitMS),
				zap.String("outcome", "ctx_cancelled"),
			)...)
			rejErr := err
			s.loop.RunOnLoop(func(vm *goja.Runtime) {
				_ = reject(vm.NewGoError(fmt.Errorf("tool %q cancelled: %w", tool.Name, rejErr)))
			})
			return
		}
		waitMS := time.Since(waitStart).Milliseconds()

		cur := atomic.AddInt64(s.inFlight, 1)
		for {
			old := atomic.LoadInt64(s.peak)
			if cur <= old || atomic.CompareAndSwapInt64(s.peak, old, cur) {
				break
			}
		}

		dispatchStart := time.Now()
		result, callErr := h.dispatcher.CallTool(s.ctx, tool.Upstream, tool.Name, args)
		dispatchMS := time.Since(dispatchStart).Milliseconds()

		atomic.AddInt64(s.inFlight, -1)
		s.sem.Release(1)

		s.loop.RunOnLoop(func(vm *goja.Runtime) {
			if callErr != nil {
				kind, outcome := classifyToolError(callErr)
				h.logger.Error("codemode.tool.error", append(base,
					zap.String("error_kind", kind),
					zap.String("error_message", redactSecrets(callErr.Error())),
				)...)
				h.logger.Info("codemode.tool.completed", append(base,
					zap.Int("result_bytes", 0),
					zap.Int64("wait_ms", waitMS),
					zap.Int64("dispatch_ms", dispatchMS),
					zap.Int64("duration_ms", waitMS+dispatchMS),
					zap.String("outcome", outcome),
				)...)
				_ = reject(vm.NewGoError(fmt.Errorf("tool %q failed: %w", tool.Name, callErr)))
				return
			}
			h.logger.Info("codemode.tool.completed", append(base,
				zap.Int("result_bytes", approxResultBytes(result)),
				zap.Int64("wait_ms", waitMS),
				zap.Int64("dispatch_ms", dispatchMS),
				zap.Int64("duration_ms", waitMS+dispatchMS),
				zap.String("outcome", "ok"),
			)...)
			_ = resolve(vm.ToValue(result))
		})
	}()

	return promiseVal
}

// attachSettlement hooks resolve/reject onto the entry-point Promise so
// the event loop drives it to settlement. resolve / reject are plain
// JS functions backed by Go closures; they push onto resultCh / errCh
// which the outer (*Handler).run select drains.
func attachSettlement(vm *goja.Runtime, promiseVal goja.Value, resultCh chan<- any, errCh chan<- error) {
	resolveFn := func(call goja.FunctionCall) goja.Value {
		var v any
		if len(call.Arguments) > 0 {
			v = exportValue(call.Argument(0))
		}
		resultCh <- v
		return goja.Undefined()
	}
	rejectFn := func(call goja.FunctionCall) goja.Value {
		msg := ""
		if len(call.Arguments) > 0 {
			msg = call.Argument(0).String()
		}
		errCh <- fmt.Errorf("execution error: %s", redactSecrets(msg))
		return goja.Undefined()
	}
	shimVal, err := vm.RunString(`(p, resolve, reject) => p.then(resolve, reject)`)
	if err != nil {
		errCh <- fmt.Errorf("attach settlement: %w", err)
		return
	}
	shim, ok := goja.AssertFunction(shimVal)
	if !ok {
		errCh <- errors.New("attach settlement: shim is not a function")
		return
	}
	if _, err := shim(goja.Undefined(), promiseVal, vm.ToValue(resolveFn), vm.ToValue(rejectFn)); err != nil {
		errCh <- fmt.Errorf("attach settlement: %w", err)
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

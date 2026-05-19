// Package codemode implements the per-user Goja JavaScript sandbox
// that the MCP tools `codemode_search` and `codemode_execute` (defined
// in internal/gateway/codemodeaction) call into.
//
// The split across files mirrors the sandbox API surface:
//
//   - handler.go   — Handler struct, Config, Search/Execute entry
//     points, the run() loop that wires the sandbox.
//   - filter.go    — ToolListing + filterListings (the
//     codemode.tools(filter?) backend).
//   - schemas.go   — ToolSchema + schemasByName (the
//     codemode.schemas(names) backend).
//   - dispatch.go  — per-tool proxies, dispatchTool, runCode (the
//     codemode.<upstream>.<tool>() and codemode.call() backends + JS
//     runner).
//   - logging.go   — log helpers, secret redaction, error
//     classification.
//
// Changing the sandbox API also requires touching the prompt
// definitions in internal/gateway/codemodeaction (search.go /
// execute.go) — keep them in lock-step.
package codemode

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/eventloop"
	"go.uber.org/zap"
	"golang.org/x/sync/semaphore"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/ids"
	"github.com/belphemur/limen/internal/tenancy"
)

// Tool is the codemode-package representation of one upstream MCP tool
// visible to the calling (tenant, user). The gateway package projects
// its richer ToolEntry into this shape via the adapter that satisfies
// Dispatcher.
type Tool struct {
	Name        string
	Description string
	Upstream    string
	InputSchema map[string]any
}

// Dispatcher is the minimal surface the Handler needs from a tool
// provider. *gateway.Manager satisfies an adapter that wraps it; unit
// tests supply a fake directly. Carved out so the codemode package
// stays leaf-level — it must not import gateway.
type Dispatcher interface {
	ToolsForUser(ctx context.Context) ([]Tool, error)
	CallTool(ctx context.Context, upstream, name string, args map[string]any) (any, error)
}

// Config is the subset of config.CodeModeConfig the handler needs.
// Kept here so the codemode package doesn't import config.
type Config struct {
	// ScriptTimeout caps wall-clock for a single invocation.
	ScriptTimeout time.Duration
	// MaxToolCalls caps the number of upstream tool calls per
	// invocation. 0 means unlimited (not recommended).
	MaxToolCalls int
	// MaxConcurrentToolCalls caps in-flight upstream tool calls per
	// invocation (Phase 8b). 0 falls back to defaultMaxConcurrent.
	MaxConcurrentToolCalls int
}

const defaultMaxConcurrent = 8

// Handler runs tenant-supplied JavaScript through Goja with the
// per-user upstream tool catalog injected as `codemode.*`. All tool
// dispatch goes through the Dispatcher, so the per-user auth-injecting
// transport, per-link health bookkeeping, and Phase 10 resilience all
// apply by construction.
type Handler struct {
	dispatcher Dispatcher
	logger     *zap.Logger
	cfg        Config
}

// New wires a Handler over a Dispatcher.
func New(d Dispatcher, cfg Config, logger *zap.Logger) *Handler {
	if logger == nil {
		logger = zap.NewNop()
	}
	if cfg.ScriptTimeout <= 0 {
		cfg.ScriptTimeout = 30 * time.Second
	}
	if cfg.MaxConcurrentToolCalls <= 0 {
		cfg.MaxConcurrentToolCalls = defaultMaxConcurrent
	}
	return &Handler{dispatcher: d, logger: logger, cfg: cfg}
}

// Search runs `code` with codemode.tools() exposed. No proxies.
func (h *Handler) Search(ctx context.Context, code string) (any, error) {
	return h.run(ctx, code, false)
}

// Execute runs `code` with codemode.tools() plus per-tool proxies
// (codemode.<tool>(args), codemode.call(name, args)).
func (h *Handler) Execute(ctx context.Context, code string) (any, error) {
	return h.run(ctx, code, true)
}

// invocationState bundles the per-invocation state shared between the
// VM-goroutine tool proxies and the worker goroutines they spawn. The
// fields are all goroutine-safe (atomics, channels, semaphore, and a
// cancellable context).
type invocationState struct {
	ctx          context.Context
	loop         *eventloop.EventLoop
	sem          *semaphore.Weighted
	callSeq      *int64
	peak         *int64
	inFlight     *int64
	invocationID string
	// errCh is the side channel used by synchronous failure paths
	// (notably the MaxToolCalls quota trip) to deliver an error to the
	// outer (*Handler).run select. vm.Interrupt raises an uncatchable
	// goja error that does not propagate through Promise.then chains —
	// so attachSettlement would never see it. Pushing here unblocks
	// the outer select immediately. Send is non-blocking; the channel
	// is buffered 1.
	errCh chan<- error
}

// reportSyncErr posts an error onto state.errCh without blocking. The
// first sender wins; subsequent calls are dropped because the outer
// run() already has its error.
func (s *invocationState) reportSyncErr(err error) {
	select {
	case s.errCh <- err:
	default:
	}
}

func (h *Handler) run(ctx context.Context, code string, withProxies bool) (any, error) {
	invocationID := ids.New(ids.PrefixCodemodeInvocation)
	base := h.baseLogFields(ctx, invocationID)

	tools, err := h.dispatcher.ToolsForUser(ctx)
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

	prg, compileErr := goja.Compile("codemode", code, false)
	if compileErr != nil {
		h.logger.Info("codemode.invocation.completed", append(base,
			zap.Int64("tool_calls_total", 0),
			zap.Int64("tool_calls_concurrent_peak", 0),
			zap.Int64("duration_ms", 0),
			zap.String("outcome", "script_error"),
			zap.Int("result_bytes", 0),
		)...)
		return nil, fmt.Errorf("compile error: %w", compileErr)
	}

	start := time.Now()
	deadline := start.Add(h.cfg.ScriptTimeout)

	// EnableConsole(false) disables the `console` global the event
	// loop would otherwise install. The loop still vm.Set()s
	// setTimeout/setInterval/setImmediate/clear*; we delete those on
	// the VM goroutine before any tenant code runs. Same for `require`
	// which goja_nodejs/require always installs.
	loop := eventloop.NewEventLoop(eventloop.EnableConsole(false))
	loop.Start()
	defer loop.StopNoWait()

	dispatchCtx, cancelDispatch := context.WithCancel(ctx)
	defer cancelDispatch()

	cap := h.cfg.MaxConcurrentToolCalls
	if cap <= 0 {
		cap = defaultMaxConcurrent
	}
	sem := semaphore.NewWeighted(int64(cap))
	var callSeq int64
	var peak int64
	var inFlight int64
	resultCh := make(chan any, 1)
	errCh := make(chan error, 1)
	state := &invocationState{
		ctx:          dispatchCtx,
		loop:         loop,
		sem:          sem,
		callSeq:      &callSeq,
		peak:         &peak,
		inFlight:     &inFlight,
		invocationID: invocationID,
		errCh:        errCh,
	}

	vmCh := make(chan *goja.Runtime, 1)

	loop.RunOnLoop(func(vm *goja.Runtime) {
		// Publish the VM pointer for the outer goroutine so
		// ctx-cancel / timeout paths can call vm.Interrupt without
		// blocking the loop. Interrupt is documented goroutine-safe.
		select {
		case vmCh <- vm:
		default:
		}

		defer func() {
			if r := recover(); r != nil {
				if ex, ok := r.(*goja.Exception); ok {
					errCh <- fmt.Errorf("javascript error: %s", redactSecrets(ex.String()))
				} else {
					errCh <- fmt.Errorf("panic: %v", r)
				}
			}
		}()

		// Strip eventloop-injected globals so the sandbox surface
		// stays exactly as wide as before phase 8b. queueMicrotask is
		// not registered by the loop, but list it in case a future
		// goja release adds it as a builtin.
		g := vm.GlobalObject()
		for _, k := range []string{
			"setTimeout", "setInterval", "setImmediate",
			"clearTimeout", "clearInterval", "clearImmediate",
			"queueMicrotask", "require",
		} {
			_ = g.Delete(k)
		}

		// Expose Go struct fields and methods to JS using their `json`
		// tags so codemode.tools() returns {name, description, ...}
		// instead of Go-cased identifiers.
		vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))

		codemodeObj := vm.NewObject()
		listings := toListings(tools)

		if withProxies {
			byUpstream := make(map[string]map[string]Tool, len(tools))
			for _, t := range tools {
				if _, ok := byUpstream[t.Upstream]; !ok {
					byUpstream[t.Upstream] = make(map[string]Tool)
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
					_ = upObj.Set(tool.Name, h.makeProxy(state, vm, tool, base))
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
				return h.dispatchAsync(state, vm, tool, args, base)
			})
		}

		_ = codemodeObj.Set(reservedKeyTools, func(call goja.FunctionCall) goja.Value {
			var filter map[string]any
			if len(call.Arguments) > 0 {
				if exported, ok := call.Argument(0).Export().(map[string]any); ok {
					filter = exported
				}
			}
			out, err := filterListings(listings, filter)
			if err != nil {
				panic(vm.NewGoError(err))
			}
			return vm.ToValue(out)
		})
		_ = codemodeObj.Set(reservedKeySchemas, func(call goja.FunctionCall) goja.Value {
			if len(call.Arguments) == 0 {
				panic(vm.NewGoError(errors.New("codemode.schemas(names): names argument is required (string or string[])")))
			}
			names := exportSchemaNames(call.Argument(0))
			if names == nil {
				panic(vm.NewGoError(errors.New("codemode.schemas(names): names must be a string or array of strings")))
			}
			res := schemasByName(tools, names)
			return vm.ToValue(map[string]any{
				"found":   res.Found,
				"missing": res.Missing,
			})
		})
		_ = codemodeObj.Set(reservedKeyJSON, func(call goja.FunctionCall) goja.Value {
			if len(call.Arguments) == 0 {
				return goja.Null()
			}
			return vm.ToValue(parseJSONResult(call.Argument(0)))
		})
		_ = codemodeObj.Set(reservedKeyQuota, func(_ goja.FunctionCall) goja.Value {
			used := atomic.LoadInt64(&callSeq)
			remaining := int64(-1)
			if h.cfg.MaxToolCalls > 0 {
				remaining = int64(h.cfg.MaxToolCalls) - used
				if remaining < 0 {
					remaining = 0
				}
			}
			deadlineMS := time.Until(deadline).Milliseconds()
			if deadlineMS < 0 {
				deadlineMS = 0
			}
			return vm.ToValue(map[string]any{
				"used":        used,
				"max":         int64(h.cfg.MaxToolCalls),
				"remaining":   remaining,
				"deadline_ms": deadlineMS,
			})
		})

		_ = vm.Set("codemode", codemodeObj)

		val, runErr := vm.RunProgram(prg)
		if runErr != nil {
			errCh <- wrapExecutionError(runErr)
			return
		}
		// Async-arrow entry point: invoke once and adopt the returned
		// value (typically a Promise). Bare expressions evaluate
		// directly to a value.
		if fn, ok := goja.AssertFunction(val); ok {
			ret, callErr := fn(goja.Undefined())
			if callErr != nil {
				errCh <- wrapExecutionError(callErr)
				return
			}
			val = ret
		}
		if _, ok := val.Export().(*goja.Promise); ok {
			attachSettlement(vm, val, resultCh, errCh)
			return
		}
		resultCh <- exportValue(val)
	})

	timer := time.NewTimer(h.cfg.ScriptTimeout)
	defer timer.Stop()

	var result any
	var runErr error
	select {
	case result = <-resultCh:
	case runErr = <-errCh:
	case <-ctx.Done():
		cancelDispatch()
		if vm := tryRecvVM(vmCh); vm != nil {
			vm.Interrupt(ctx.Err())
		}
		runErr = ctx.Err()
	case <-timer.C:
		cancelDispatch()
		if vm := tryRecvVM(vmCh); vm != nil {
			vm.Interrupt("script timeout")
		}
		runErr = fmt.Errorf("codemode: script exceeded %v timeout", h.cfg.ScriptTimeout)
	}

	durMS := time.Since(start).Milliseconds()
	totalCalls := atomic.LoadInt64(&callSeq)
	peakObserved := atomic.LoadInt64(&peak)

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
		zap.Int64("tool_calls_concurrent_peak", peakObserved),
		zap.Int64("duration_ms", durMS),
		zap.String("outcome", outcome),
		zap.Int("result_bytes", approxResultBytes(result)),
	)...)

	return result, runErr
}

// tryRecvVM does a non-blocking receive on vmCh. Returns nil if the VM
// hasn't been published yet (the loop callback hasn't started).
func tryRecvVM(vmCh <-chan *goja.Runtime) *goja.Runtime {
	select {
	case vm := <-vmCh:
		return vm
	default:
		return nil
	}
}

func (h *Handler) baseLogFields(ctx context.Context, invocationID string) []zap.Field {
	fields := []zap.Field{zap.String("codemode_invocation_id", invocationID)}
	if t, ok := tenancy.TenantFromContext(ctx); ok {
		fields = append(fields, zap.Int64("tenant_id", t.ID), zap.String("tenant_public_id", t.PublicID))
	}
	if u, ok := auth.MCPUserFromContext(ctx); ok {
		fields = append(fields, zap.Int64("user_id", u.ID))
	}
	return fields
}

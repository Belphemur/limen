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
	"go.uber.org/zap"

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
}

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
		cfg.ScriptTimeout = 10 * time.Second
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

	start := time.Now()
	vm := goja.New()
	// Expose Go struct fields and methods to JS using their `json` tags
	// (and lowercased method names). Without this, `codemode.tools()`
	// returns objects whose properties are `Name`/`Description`/...
	// (Go field names), while the documented contract and the
	// downstream JSON marshalling both use `name`/`description`/...
	// The mismatch causes scripts like `tools.map(t => t.upstream)` to
	// silently yield `null` arrays.
	vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
	var callSeq int64
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
	_ = codemodeObj.Set("tools", func(call goja.FunctionCall) goja.Value {
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
	_ = codemodeObj.Set("schemas", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			panic(vm.NewGoError(errors.New("codemode.schemas(names): names argument is required (string or string[])")))
		}
		names := exportSchemaNames(call.Argument(0))
		if names == nil {
			panic(vm.NewGoError(errors.New("codemode.schemas(names): names must be a string or array of strings")))
		}
		return vm.ToValue(schemasByName(tools, names))
	})

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

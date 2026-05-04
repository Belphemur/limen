package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dop251/goja"
	"go.uber.org/zap"
)

type CodeModeHandler struct {
	gateway  *Gateway
	logger   *zap.Logger
	timeout  time.Duration
}

func NewCodeModeHandler(gw *Gateway, logger *zap.Logger, timeout time.Duration) *CodeModeHandler {
	return &CodeModeHandler{
		gateway: gw,
		logger:  logger,
		timeout: timeout,
	}
}

type ToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

func (h *CodeModeHandler) Search(ctx context.Context, code string) (interface{}, error) {
	vm := goja.New()

	tools := h.gateway.AllTools()
	defs := make([]ToolDefinition, len(tools))
	for i, t := range tools {
		defs[i] = ToolDefinition{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		}
	}

	toolsJSON, _ := json.Marshal(defs)
	vm.Set("__tools", string(toolsJSON))

	codemode := vm.NewObject()
	codemode.Set("tools", func() ([]ToolDefinition, error) {
		var parsed []ToolDefinition
		if err := json.Unmarshal([]byte(vm.Get("__tools").String()), &parsed); err != nil {
			return nil, err
		}
		return parsed, nil
	})

	vm.Set("codemode", codemode)

	return h.runCode(ctx, vm, code)
}

func (h *CodeModeHandler) Execute(ctx context.Context, code string) (interface{}, error) {
	vm := goja.New()

	tools := h.gateway.AllTools()
	proxy := vm.NewObject()

	for _, tool := range tools {
		tool := tool
		proxy.Set(tool.Name, func(call goja.FunctionCall) goja.Value {
			var args map[string]interface{}
			if len(call.Arguments) > 0 {
				exported := call.Argument(0).Export()
				b, _ := json.Marshal(exported)
				json.Unmarshal(b, &args)
			}

			result, err := h.gateway.CallTool(ctx, tool.Upstream, tool.Name, args)
			if err != nil {
				panic(vm.NewGoError(fmt.Errorf("tool %q failed: %w", tool.Name, err)))
			}

			val := vm.ToValue(result)
			return val
		})
	}

	codemode := vm.NewObject()
	codemode.Set("tools", func() ([]ToolDefinition, error) {
		var defs []ToolDefinition
		for _, t := range tools {
			defs = append(defs, ToolDefinition{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: t.InputSchema,
			})
		}
		return defs, nil
	})
	codemode.Set("call", func(call goja.FunctionCall) goja.Value {
		name := call.Argument(0).String()
		var args map[string]interface{}
		if len(call.Arguments) > 1 {
			exported := call.Argument(1).Export()
			b, _ := json.Marshal(exported)
			json.Unmarshal(b, &args)
		}

		tool, ok := h.gateway.FindTool(name)
		if !ok {
			panic(vm.NewGoError(fmt.Errorf("tool %q not found", name)))
		}

		result, err := h.gateway.CallTool(ctx, tool.Upstream, name, args)
		if err != nil {
			panic(vm.NewGoError(fmt.Errorf("tool %q failed: %w", name, err)))
		}

		val := vm.ToValue(result)
		return val
	})

	vm.Set("codemode", codemode)

	return h.runCode(ctx, vm, code)
}

func (h *CodeModeHandler) runCode(ctx context.Context, vm *goja.Runtime, code string) (interface{}, error) {
	resultCh := make(chan interface{}, 1)
	errCh := make(chan error, 1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				if gojaErr, ok := r.(*goja.Exception); ok {
					errCh <- fmt.Errorf("javascript error: %s", gojaErr.String())
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

	select {
	case result := <-resultCh:
		return result, nil
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		vm.Interrupt("execution timeout")
		return nil, ctx.Err()
	case <-time.After(h.timeout):
		vm.Interrupt("execution timeout")
		return nil, fmt.Errorf("code execution exceeded %v timeout", h.timeout)
	}
}

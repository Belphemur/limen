---
phase: "8b"
title: "Codemode async tool calls (event loop + Promise proxies)"
status: completed
progress: 100
depends_on: ["8"]
updated: "2026-02-10"
---

# Phase 8b — Make codemode tool calls actually async

**Depends on**: Phase 8 (per-tenant injection — gives the sandbox a real auth-injecting upstream client)
**Unblocks**: realistic parallel-fanout patterns in `codemode_execute`; performance work on multi-upstream agent workflows.

## Goal

Turn the codemode JavaScript sandbox into a runtime where `async` / `await`
and `Promise.all` mean what they say: tool calls are non-blocking from
JS's perspective, the goja VM does not stall on upstream I/O, and a
script that issues N tool calls in parallel finishes in roughly
`max(latency)` instead of `sum(latency)`.

Today (post phase 8) the sandbox advertises an `async () => { ... }`
entry contract but every tool proxy is synchronous: `dispatchTool`
blocks the goja goroutine on `manager.CallTool`, returns a plain
`goja.Value`, and `await` becomes a no-op. `Promise.all([codemode.a(),
codemode.b()])` does not parallelize — both calls execute sequentially
while the array literal is being built, before `Promise.all` ever sees
them.

After this phase:

- Tool proxies return a goja Promise immediately and dispatch the
  upstream call on a background goroutine.
- The runtime owns an event loop that drains microtasks and resolves
  Go-backed Promises onto the JS heap on a single VM-owning goroutine
  (goja is **not** thread-safe).
- `Promise.all` / `Promise.allSettled` fan out N tool calls
  concurrently, bounded by an explicit per-invocation cap.
- Cancellation (`ctx.Done`, script timeout, downstream client
  disconnect) propagates into in-flight tool dispatches.
- Existing bare-expression and async-arrow scripts keep working.

## Why now

- The synchronous model produces misleading latency for any script that
  touches more than one upstream — the LLM author writes
  `Promise.all(...)` and gets sequential behaviour. That is a
  correctness gap between the advertised API and the runtime.
- Tool calls now traverse the Phase 8 auth-injecting round-tripper, so
  the dominant cost per call is upstream HTTP + possibly a token
  refresh. Serializing 5 of those when 4 could overlap is real wall
  time, charged to the agent's request budget.
- The codemode tool description in [internal/transport/codemode_server.go](../../internal/transport/codemode_server.go)
  tells the LLM "use Promise.all to fan out" — we should make that
  statement true before more clients depend on it.

## Design

### Adopt an event loop

Pull in [`github.com/dop251/goja_nodejs/eventloop`](https://github.com/dop251/goja_nodejs)
as the runtime owner. Replace the bare `goja.New()` in
[`(*CodeModeHandler).run`](../../internal/gateway/codemode.go) with:

```go
loop := eventloop.NewEventLoop(eventloop.WithRegistry(nil))
loop.Start()
defer loop.Stop()
```

Every interaction with the VM — bindings setup, `RunProgram`, Promise
resolution — must happen via `loop.RunOnLoop(func(vm *goja.Runtime) { ... })`
because goja is single-threaded per runtime. The existing
`runCode` goroutine collapses into a single `loop.RunOnLoop` that
compiles the program, invokes the async arrow entry point, and
attaches resolve/reject callbacks to the returned Promise; the outer
Go code waits on a `done chan struct{}` plus the VM's settled result.

We deliberately do **not** import the full `goja_nodejs` package
(which would surface `console`, `require`, `process`, etc.). Only the
`eventloop` sub-package is needed; the sandbox denial tests in
[internal/gateway/codemode_test.go](../../internal/gateway/codemode_test.go)
must continue to pass for `setTimeout`, `setInterval`, `console`,
`require`, `process`, `Buffer`, `fetch`, `XMLHttpRequest`, `WebSocket`,
`__dirname`, `__filename`, `window`, `global`. The event loop's
`setTimeout` / `setImmediate` are intentionally **not** registered;
microtasks fire as a side-effect of Promise resolution, which is all
the runtime needs.

### Async tool proxies

`makeProxy` and the `codemode.call` dispatcher are rewritten to return
a Promise built via `vm.NewPromise()`:

```go
func (h *CodeModeHandler) makeProxy(ctx context.Context, loop *eventloop.EventLoop, tool ToolEntry, callSeq *int64, invocationID string) func(call goja.FunctionCall) goja.Value {
    return func(call goja.FunctionCall) goja.Value {
        args := exportArgs(call, 0)
        vm := call.Runtime
        p, resolve, reject := vm.NewPromise()

        // Quota check stays synchronous — it must run on the VM goroutine
        // before we hand the Promise back to JS.
        seq := atomic.AddInt64(callSeq, 1)
        if h.cfg.MaxToolCalls > 0 && seq > int64(h.cfg.MaxToolCalls) {
            panic(vm.NewGoError(errQuotaExceeded))
        }

        // Bounded concurrency: acquire a slot on the per-invocation
        // semaphore before launching the goroutine, so we never spawn
        // more than MaxConcurrentToolCalls workers per script.
        go func() {
            if err := h.dispatchSem.Acquire(ctx, 1); err != nil {
                loop.RunOnLoop(func(*goja.Runtime) { reject(vm.ToValue(err.Error())) })
                return
            }
            defer h.dispatchSem.Release(1)

            res, err := h.manager.CallTool(ctx, tool.Upstream, tool.Name, args)
            loop.RunOnLoop(func(vm *goja.Runtime) {
                if err != nil {
                    reject(vm.ToValue(redactSecrets(err.Error())))
                    return
                }
                resolve(vm.ToValue(res))
            })
        }()
        return vm.ToValue(p)
    }
}
```

Key invariants:

- The Promise is created **on the VM goroutine** (inside the JS call
  frame) so its identity belongs to that runtime.
- Dispatch happens off the VM goroutine; goja is never re-entered from
  the worker goroutine directly. Resolution is funnelled back through
  `loop.RunOnLoop`.
- `errQuotaExceeded` still panics synchronously — the cap protects the
  fan-out budget, so it must reject the call _before_ any goroutine
  spawns.
- `redactSecrets` runs on the goja goroutine, after we know which
  error string will reach JS. (Today it runs inline; the contract is
  unchanged.)

`codemode.tools()` stays synchronous — it reads a slice already
materialised by `manager.ToolsForUser` and never touches the network.

### Concurrency bound

A new config field, `code_mode.max_concurrent_tool_calls`, defaults to
`8`. Implemented with `golang.org/x/sync/semaphore`. Rationale:

- Without a cap, a malicious or buggy script can issue thousands of
  parallel calls and exhaust upstream connection pools (Phase 8's
  per-upstream `http.Transport` is shared across all in-flight
  requests for that upstream).
- 8 is large enough to give the agent meaningful fan-out (covers any
  realistic multi-upstream "find related work" pattern) and small
  enough that one runaway invocation cannot dominate the gateway.
- `MaxToolCalls` (total) and `MaxConcurrentToolCalls` (in-flight) are
  independent and must both be enforced; the latter is per-invocation,
  not global.

### Cancellation

Three cancellation sources must reach in-flight tool dispatches:

| Source                           | Mechanism today            | Mechanism after phase                                                                                                                                                                                                                               |
| -------------------------------- | -------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ctx.Done()` (client disconnect) | `vm.Interrupt` stops JS    | Same, plus the dispatch ctx (child of the invocation ctx) is cancelled, propagating into `manager.CallTool` and into upstream HTTP via `Request.WithContext`.                                                                                       |
| Script timeout                   | `vm.Interrupt` after timer | Same, plus a dedicated `dispatchCtx`, cancel-func pair is created per invocation; the timer cancels it. Workers observe `dispatchCtx.Err()` after `manager.CallTool` returns and reject the Promise rather than resolving with a half-formed value. |
| Quota exceeded                   | `panic` from proxy         | Unchanged — runs synchronously on the VM goroutine, so it short-circuits the call before a goroutine is spawned.                                                                                                                                    |

`vm.Interrupt` does not stop in-flight Go work; it only prevents the
next JS bytecode step. The new `dispatchCtx` is what actually unblocks
goroutines stuck in `CallTool`.

### Promise settlement guarantees

The current "synchronous Promise drain" in `runCode` (added in the
Phase 8 follow-up fix) is removed. With a real event loop, the entry
point's promise can be pending across many microtask turns. The new
shape:

```go
loop.RunOnLoop(func(vm *goja.Runtime) {
    // bindings setup …
    val, err := vm.RunProgram(prg)
    if err != nil { errCh <- err; return }
    if fn, ok := goja.AssertFunction(val); ok {
        ret, callErr := fn(goja.Undefined())
        if callErr != nil { errCh <- callErr; return }
        val = ret
    }
    if p, ok := val.Export().(*goja.Promise); ok {
        // Attach .then via the JS object so the event loop will
        // re-enter and call our resolve/reject when the chain settles.
        attachSettlement(vm, p, resultCh, errCh)
        return
    }
    resultCh <- val.Export()
})

select {
case res := <-resultCh: return res, nil
case err := <-errCh: return nil, err
case <-ctx.Done(): cancelDispatch(); vm.Interrupt(...); return nil, ctx.Err()
case <-timer.C: cancelDispatch(); vm.Interrupt(...); return nil, errScriptTimeout
}
```

`attachSettlement` is a small JS shim — `p.then(resolve, reject)` —
created once at runtime init, where `resolve` and `reject` are Go
functions that send onto `resultCh` / `errCh`.

### Logging

`codemode.invocation.completed` keeps `tool_calls_total`, `duration_ms`,
`outcome`, `result_bytes`. Add `tool_calls_concurrent_peak`
(`atomic.Int64`, max observed in-flight) so we can tell from logs
alone whether scripts are actually using the new parallelism. Per-tool
`codemode.tool.completed` logs gain `wait_ms` (semaphore acquisition
wait) and `dispatch_ms` (CallTool RTT) — useful for diagnosing whether
slowness comes from the cap, the upstream, or the auth round-trip.

## File-level deliverables

- [internal/gateway/codemode.go](../../internal/gateway/codemode.go)
  — rewrite `run` to own an `eventloop.EventLoop`; rewrite `makeProxy`
  and the `codemode.call` dispatcher to return goja Promises; introduce
  `dispatchSem *semaphore.Weighted`; thread a child `dispatchCtx`;
  drop the synchronous Promise drain in `runCode` in favour of the
  event-loop-driven settlement.
- [internal/gateway/codemode.go](../../internal/gateway/codemode.go)
  — `CodeModeConfig` gains `MaxConcurrentToolCalls int` with default
  `8`.
- [internal/gateway/codemode_test.go](../../internal/gateway/codemode_test.go)
  — new tests: `TestCodeMode_PromiseAllParallelizes` (two tool calls
  with `time.Sleep` in the fake dispatcher complete in
  `~max(latency)`), `TestCodeMode_ConcurrencyBoundEnforced` (with cap
  N, the (N+1)-th call observes the first N still in-flight),
  `TestCodeMode_CtxCancelStopsInFlight` (ctx cancelled mid-script
  unblocks workers and rejects the Promise),
  `TestCodeMode_ScriptTimeoutAbortsInFlight` (timer fires while
  workers are blocked; workers observe context cancellation).
- [internal/gateway/codemode_test.go](../../internal/gateway/codemode_test.go)
  — extend the existing sandbox-denial table to confirm `setTimeout`,
  `setInterval`, `setImmediate`, `clearTimeout`, `clearInterval`,
  `clearImmediate`, `queueMicrotask` all remain unavailable despite
  the event loop being present.
- [internal/config/config.go](../../internal/config/config.go) — add
  `code_mode.max_concurrent_tool_calls` to `CodeModeConfig` with the
  default applied in `applyDefaults`.
- [config.yaml](../../config.yaml) — document the new key under the
  existing `code_mode:` block.
- [docs/codemode.md](../../docs/codemode.md) — update the
  "How It Works" section: tool proxies now return Promises, so
  `Promise.all([...])` actually parallelizes; document the
  per-invocation cap and how it interacts with `MaxToolCalls`.
- [go.mod](../../go.mod) / [go.sum](../../go.sum) — add
  `github.com/dop251/goja_nodejs` (only the `eventloop` sub-package
  imported) and `golang.org/x/sync`.

## Risks

| Risk                                                                                                                                      | Mitigation                                                                                                                                                                                                                                          |
| ----------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `goja_nodejs` accidentally widens the sandbox API surface (e.g. someone imports the top-level package later and gets `require` for free). | Add a `forbidigo` rule to the `.golangci.yml` config blocking imports of `goja_nodejs` _outside_ of `internal/gateway/codemode.go`, and within that file restrict to `goja_nodejs/eventloop`. Sandbox-denial tests catch any regression at runtime. |
| The event loop owns its own goroutine; lifetime bugs leak goroutines per invocation.                                                      | `defer loop.StopNoWait()` — `eventloop.EventLoop.Stop` blocks until pending tasks drain, which is wrong on cancellation. Use `StopNoWait` plus `dispatchCtx` cancellation so in-flight workers unwind on their own.                                 |
| Two upstreams sharing one `http.Transport` (Phase 8 design) get connection-pool contention under heavy fan-out.                           | The `MaxConcurrentToolCalls` cap is global per invocation; per-upstream pool sizing is governed by `Transport.MaxConnsPerHost`. No change here, but document the interaction in [docs/codemode.md](../../docs/codemode.md).                         |
| `vm.Interrupt` races with a Promise resolution callback already queued on the event loop.                                                 | `loop.RunOnLoop` callbacks check `dispatchCtx.Err()` before touching `vm.ToValue` / resolve / reject; if cancelled, they no-op.                                                                                                                     |
| Adopting an event loop changes timing of microtasks vs. the JS author's expectations (e.g. ordering of resolved Promises).                | Document explicitly that microtask ordering follows the spec; add a deterministic ordering test using two pre-resolved Promises plus a tool call.                                                                                                   |

## Verification

1. Run `go test ./internal/gateway/... -race -count=1` — the new
   parallelization, concurrency-cap, and cancellation tests pass, no
   data races reported. The race detector is the load-bearing check
   here; if a goroutine touches the VM outside `RunOnLoop`, it will
   fire.
2. `go test ./internal/gateway/... -run TestCodeMode_SandboxDenials`
   passes unchanged. The sandbox is no wider than before.
3. Bench: `go test ./internal/gateway/... -bench BenchmarkCodemodeFanOut`
   with a fake dispatcher that sleeps 50 ms per call. With cap=8 and
   8 parallel calls the run completes in ~60 ms (was ~400 ms).
4. `LIMEN_LOG_LEVEL=debug make dev-run`, drive a codemode script with
   `Promise.all([codemode.a(...), codemode.b(...)])` from Cursor;
   confirm `codemode.invocation.completed` shows
   `tool_calls_concurrent_peak >= 2` and `duration_ms` close to the
   slower of the two RTTs.
5. Disconnect the MCP client mid-script (close Cursor tab while a long
   tool call is in flight); confirm the worker goroutine unwinds
   within ~1 s and that no goroutine leak shows in `pprof`'s goroutine
   profile.
6. `golangci-lint run ./...` — including the new `forbidigo` rule
   guarding the `goja_nodejs` import surface.

## Out of scope

- **Web Workers / multi-VM**: one runtime per invocation, single
  VM-owning goroutine. Multiple `goja.Runtime` instances per
  invocation are intentionally not added.
- **Real timers** (`setTimeout`, `setInterval`): not exposed even
  though the event loop technically supports them; would invite
  scripts that wait on wall-clock time, defeating
  `code_mode.script_timeout` semantics.
- **Streaming results**: tool calls remain request/response. Streaming
  the upstream `tools/call` SSE through to JS as an async iterator is
  a separate, larger phase.
- **Cross-invocation caching of compiled programs**: every invocation
  still compiles fresh. Cache + invalidation policy is a separate
  optimization phase.

## Checklist

- [x] Pull in `goja_nodejs/eventloop` and `golang.org/x/sync/semaphore`.
- [x] Rewrite `run` to own an event loop and route all VM access
      through `loop.RunOnLoop`.
- [x] Rewrite `makeProxy` + `codemode.call` to return goja Promises;
      dispatch off-VM, resolve back through the event loop.
- [x] Add `MaxConcurrentToolCalls` to `CodeModeConfig` (default 8),
      wire to `semaphore.Weighted`, surface in `config.yaml`.
- [x] Introduce per-invocation `dispatchCtx` and propagate cancellation
      from ctx/timeout into in-flight tool dispatches.
- [x] Drop the synchronous Promise drain from `runCode`; replace with
      event-loop-driven settlement onto `resultCh` / `errCh`.
- [x] Add `tool_calls_concurrent_peak`, `wait_ms`, `dispatch_ms` log
      fields.
- [x] New tests: parallelism, concurrency cap, ctx-cancel mid-flight,
      timeout mid-flight, microtask ordering. Run with `-race`.
- [x] Re-confirm sandbox denials (`setTimeout`, `setImmediate`,
      `queueMicrotask`, …) still fail.
- [x] `depguard` rule restricting `goja_nodejs` imports to
      `internal/gateway/codemode` and the `eventloop` sub-package.
- [x] Update [docs/codemode.md](../../docs/codemode.md) — Promise
      semantics, concurrency cap, interaction with `MaxToolCalls`.
- [x] Update [docs/phases/README.md](README.md) index with phase 8b
      and its dependency on phase 8.

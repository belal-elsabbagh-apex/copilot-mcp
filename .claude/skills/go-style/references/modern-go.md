# Modern Go in this repo

`go.mod` pins **go 1.27.0** (CI builds with 1.27.3). Everything through 1.27 is available. Use it only where
it removes code. When unsure an API exists at this version, grep `$(go env GOROOT)/api/go1.2*.txt`, not memory.

## Sorting — `slices`, never `sort`
The `sort` package is not used here (0 uses in `internal/`). `slices.SortStableFunc` takes a three-way `cmp` over
the **elements** (`internal/apex/backend_queues.go`); `cmp.Compare` for keys
(`internal/uipath/outputschema.go`). Use the **stable** form wherever equal elements must keep input order.
Collect map keys with `slices.Sorted(maps.Keys(m))` (`internal/config/resolve.go`) — the one-line form of
"map order never reaches output".

## `errors.AsType` (1.26)
`if e, ok := errors.AsType[*APIError](err); ok { … }` (`internal/uipath/errors.go`,
`internal/apex/client.go`) instead of the pre-declared-target `errors.As`. `errors.Is` is unchanged.
`shared.IsExpected` is built on it (`internal/shared/expected.go`).

## `new(expr)` (1.26)
`new` takes a value: `Const = new(any(string(config.PreProd)))` (`internal/server/tools_orchestrator_common.go`),
`new(float64(0))`; the repo has no `ptr[T]` helper. Prefer it to a one-off `v := x; &v`.

## Loops, builtins
`for i := range n`, per-iteration loop variables (never `c := c`), `min`/`max`/`clear`. `cmp.Or` collapses fallback
ladders **only where zero means absent** (`internal/queueitem/assemble.go`). `slices.Chunk` replaces the TS
`chunk()`; but fan-outs here are pools, not chunk barriers — see the concurrency reference.

## `sync` (1.21–1.25)
`wg.Go(fn)` (1.25) replaces `Add(1)` + `go func(){ defer wg.Done() }` (`internal/doctor/doctor.go`,
`internal/uipath/faults.go`). `sync.OnceValues` for process-lifetime parse-once data.

## `log/slog`
The logger everywhere; key-value pairs after a constant message, never `Sprintf` into it. Output is stderr only
(`server.InstallLogging`). Level is Warn unless `LOG_LEVEL`/`COPILOT_MCP_DEBUG` say otherwise
(`LogLevel` in `internal/server/server.go`).

## Testing-side
`t.Context()` (1.24) — the convention: `context.Background()` appears in no `_test.go`. `t.Chdir()` (1.24) for
config-file tests (`internal/config/config_test.go`). `testing/synctest` (1.25) is available and unused so
far — reach for it for settle-window/timer logic before adding sleeps.

## Not available / not used
`encoding/json/v2` is experiment-gated; use `encoding/json`. The go-sdk's logging API is deprecated (SEP-2577):
all use is confined to `internal/server/logging.go` with a file-level `//nolint:staticcheck` — don't spread it.

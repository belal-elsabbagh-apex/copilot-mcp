---
name: go-style
description: Use when writing, editing, reviewing, or refactoring Go in the copilot-mcp repo (cmd/copilot-mcp, internal/*). Covers this repo's conventions (stdlib-first, data over branches, JSON parity at the MCP edge, bounded concurrency, persistent terminal-job caches) plus Go traps and post-1.21 stdlib features.
---

# Go Style (copilot-mcp)

A Go MCP server (go-sdk v1.8.0, stdio) ported from TypeScript with **zero change to the MCP surface**: same tool,
prompt and resource names, same input schemas, same JSON results. Code here is deliberate: evidence in the
comments, guards before work, one chokepoint per external system. Match it.

Layers (import direction, never upward): `shared ← config ← {reference, memo} ← orchestrator ← {uipath, apex} ←
backends ← sweep ← server`; `copilot` (+ `copilot/settings`) sits beside `orchestrator` (imports shared, config,
memo); `queueitem` and `doctor` sit beside `backends` (`doctor` imports copilot, uipath and apex directly). `memo`
imports `config`. `internal/server` is wiring only; domain logic lives below it. A cycle means move the smaller
piece down. Cross-cutting helpers live in `internal/shared` — look there before writing one.

## Always

**Stdlib first.** Deps: go-sdk, `golang.org/x/sync` (errgroup, singleflight), `google/jsonschema-go` (go.mod). HTTP is
`net/http`, logging `log/slog`, tests `testing`. A new dependency needs a stated reason no stdlib package covers.

**A new rule is new data, not a new branch.** The settings sections are one table (`SettingsCatalog`,
`internal/copilot/settings/catalog.go`); tool schema rules are `Patch` values (`internal/server/schema.go`);
lookup maps beat if/else ladders (`lateStatus`, `internal/apex/readcache.go`). Add a row, not an `if`.

**Guard clauses, early return.** Validate, resolve, assert the write gate, *then* do I/O
(`apexRequest`, `internal/apex/client.go`: `assertWritable` before anything touches the network).

**stdout is the JSON-RPC channel.** No `fmt.Print*`/`log.Print*`/`os.Stdout` in non-test code. Diagnostics use
`slog` (stderr, `server.InstallLogging`, `internal/server/server.go`); client-visible logs use `s.mcpLog`.

**Errors: sentinels/typed errors, wrapped with `%w`, matched with `errors.Is` / `errors.AsType`.** A user-facing,
*expected* failure (bad profile, not configured, refused write) is a `shared.Expected` error
(`internal/shared/expected.go`): it carries no `reportIssue` block. Embed `shared.ExpectedError` in a
typed error (`config.ApexNotConfiguredError`, `apex.APIError`) and match with `errors.AsType[*apex.APIError]`
(`internal/apex/client.go`). **Error text is tool output** — keep the TS wording; don't "improve" a message.

**Every I/O function takes `ctx context.Context` first**; HTTP clients are package-level with one shared
`Transport` per upstream and `MaxIdleConnsPerHost` ≥ the fan-out bound (the default is 2:
`newTransport` in `internal/uipath/client.go`, `apexTransport` in `internal/apex/client.go`). Never store a ctx in a struct.

**Inject at boundaries; fake in `_test.go`.** `copilot.Client` is an interface (`internal/copilot/client.go`)
because mint/submit/docs are tested against scripted fakes (`fakeClient` in `internal/copilot/fake_test.go`). No other
interface exists "for later" — one implementation and no test seam means a concrete type.

**Comments carry the why, with evidence** — the incident, the measured number, the API quirk. Comments citing a
TS behaviour or an upstream quirk survive refactors. An unmeasured choice carries a `ponytail:` comment naming
the number to measure (`internal/memo/memo.go`, `internal/apex/pages.go`). Grep `ponytail:` for the
open list.

**Files stay small** (~250–350 lines). Past that, split by seam (`internal/apex/readcache.go`, `pages.go`,
`queuememo.go`), not alphabetically.

**`make check` before done** — vet, gofmt, golangci-lint, actionlint, `go test -race`, package smoke. Not "looks right".

## This repo's special rules

- **JSON parity at the edge.** Results are compared key-by-key with the TS server. TS optional → pointer/slice/map
  with `omitempty` (or `omitzero` for `shared.Field`, used in `internal/copilot/order_search.go`); TS `T | null` →
  pointer *without* omitempty (`orchestrator.JobFault`, `internal/orchestrator/types.go`); always-present
  arrays are non-nil (`ids := []string{}`, `internal/backends/findorder.go`). A TS `a ?? b` chain is
  `shared.Coalesce`; an NPI is always a string (`shared.NPIField`, a deliberate difference from the TS). Struct field order = TS key
  order. See `references/pitfalls.md`.
- **Pass-through upstream JSON stays `any`/`json.RawMessage`**, read with `shared.Prop`/`StringProp` and comma-ok
  assertions — never a bare `v.(T)`.
- **Encode with `shared.MarshalJSON`** (HTML escaping off), never an inline `json.Marshal`/`json.Encoder`, wherever
  output is compared or guarded: the default turns `<TO-FILL>` into `\u003cTO-FILL` and the guard never matches, and
  a `MarshalJSON` method's output bypasses `renderJSON`'s own escaping setting (`internal/server/render.go` is the
  one indenting encoder). `JSON.stringify` never HTML-escapes. Likewise `shared.SliceUTF16` / `shared.URIComponent`
  for JS `slice` / `encodeURIComponent`, `shared.JWTClaim` for token claims.
- **Concurrency is bounded and ordered.** Reach for `shared.MapIsolated` (per-item isolated, the
  `Promise.allSettled` shape) or `shared.MapOrdered` (fail-fast, `Promise.all`), or an `errgroup` for a fixed pair
  (`internal/queueitem/build.go`); results land in input order. `sync.WaitGroup.Go` over
  hand-rolled `Add/Done`. Never parallelize writes or the mint sequence. See `references/concurrency-and-caching.md`.
- **Single-flight** logins/tokens/crawls on the miss path with `shared.SingleFlight` (x/sync `singleflight.Group`;
  detaches the work with `context.WithoutCancel`, callers wait on their own ctx): `internal/copilot/session.go`,
  `internal/uipath/auth.go`, `internal/apex/session.go`. `memo.Fetch` has its own per-key flight.
- **Caches are persistent (memory + disk, 0700/0600, hashed filenames), terminal+settled-only, and never feed a write
  guard.** Cache-consulting read tools take `refresh` (`memo.RefreshIf`). Write modules call the raw function
  (`apex.getJob`, `uipath.FetchJobByKey`), never the cached wrapper. A UiPath hit makes no upstream call; an Apex hit still reads the job live first.

## Read these when

| Task | Reference |
|---|---|
| A loop/helper that might be stdlib; `sort`, iteration, `slog`, `new(expr)` | `references/modern-go.md` |
| Anything returning JSON, ranging a map, wrapping errors, `any` from a payload | `references/pitfalls.md` |
| Adding a goroutine, pool, cache, token or login | `references/concurrency-and-caching.md` (+ `docs/concurrency-and-caching.md`) |
| Writing or changing any `_test.go` | `references/testing.md` |

# Go pitfalls that bite this server

Ordered by damage, not fame. The recurring theme: the MCP edge is compared byte-for-byte against the TS server.

## Absent vs null vs zero at the JSON edge
TS `undefined` is **omitted**; TS `null` is `null`; TS `false`/`0`/`""` are values. Go's `omitempty` conflates the
last with absent, so:
- optional scalar/object → pointer + `omitempty` (omitted when nil);
- `T | null` → pointer **without** omitempty (marshals `null`): `orchestrator.JobFault` (`internal/orchestrator/types.go`);
- three-state pass-through of an upstream value → `shared.Field{V, Set}` with `omitzero` (`internal/shared/util.go`,
  used in `internal/copilot/order_search.go`); a TS `a ?? b ?? c` chain is `shared.Coalesce(...)` (first *present,
  non-null* field wins); an NPI goes through `shared.NPIField` so it is always a string (deliberately unlike the TS);
  the ordered-object type for wire-visible key order is `orchestrator.Fields`;
- an always-present array is a **non-nil** empty slice (`[]`, never `null`): `ids := []string{}`
  (`internal/backends/findorder.go`). Inside a package nil is fine; normalize once at the edge.
`make([]T, n)` pre-fills n zeros — with `append` use `make([]T, 0, n)`.

## Map iteration order
Randomized in Go, ordered in JS. A map `range` must never reach output: `slices.Sorted(maps.Keys(m))`
(`internal/config/resolve.go`). Where TS key order is **wire-visible**, a map is wrong: use a struct, a custom
`MarshalJSON` (`orchestrator.LevelCounts`, `internal/orchestrator/log_digest.go`), or `json.RawMessage`
embedded from a dump (`internal/reference/data/*.json`). `map[string]any` keys marshal sorted — only acceptable
where the TS key order was incidental (`meta`, `SpecificContent`).

## HTML escaping
`json.Marshal` turns `<`/`>`/`&` into `\u00XX`; `JSON.stringify` doesn't. Encode with `shared.MarshalJSON`
(`SetEscapeHTML(false)` inside) everywhere — request bodies, `MarshalJSON` methods, anything a guard scans — never an
inline `json.Marshal`/`json.NewEncoder`; the one other encoder is `renderJSON` (`internal/server/render.go`), which
adds indentation. A `MarshalJSON` method's output is copied into the tool result as-is, so an escaping encoder inside
one leaks `\u003c` to the client even though `renderJSON` escapes nothing. A guard that scans escaped text never
matches `<TO-FILL>` and fails **open**.

## `any` from JSON: every number is `float64`
Always comma-ok (`s, ok := v.(string)`); a bare assertion panics the day the BE changes a field. Use
`shared.Prop`/`StringProp`/`Field` instead of asserting inline. The few `forcetypeassert` exceptions carry a
`//nolint` with a reason (`internal/memo/memo.go`: the list only holds `*entry`).

## JS-vs-Go text semantics
JS `trim`, `\s`, `toLowerCase` and string length work on UTF-16 / Unicode whitespace; Go on runes/bytes. Where a
cap or normalization feeds a fault signature or user-visible text, the difference is documented with a
`ponytail:` (`internal/orchestrator/text.go`, `logTail` in `internal/apex/faults.go`). Cuts that must match JS
`slice`/`length` use `shared.SliceUTF16`; don't hand-roll a rune or byte cut. Don't "simplify" those.

## Error wrapping
`%w` wraps; `%v` flattens and breaks `errors.Is/AsType`. Wrap with context the caller lacks. User-visible
wording is part of the contract: staticcheck's "capitalised error string" is silenced per-line with a reason
(`internal/uipath/actions.go`), not by rewording.

## Non-2xx is not a Go error (Copilot BE client)
`copilot.Client.Req` returns an error only for transport failures; a 4xx/5xx is in `Response.Status`
(`internal/copilot/client.go`). Callers decide. Don't "fix" it into an error return.

## Context
`ctx` first; single-flight work runs under `context.WithoutCancel` so one cancelled caller can't fail the rest
(`shared.SingleFlight`, used in `internal/copilot/session.go`) — followers still wait on **their own** ctx. A memo that captures a sibling's
cancellation poisons the others, which is why `settings.fetchBoth` has no cancel-on-error context.

## Time
Use UTC for anything stored or compared; pass `now`/clock in rather than calling `time.Now()` inside logic you
want to test. Settle windows compare `time.Since(ended)` against the **upstream's** timestamp
(`settled` in `internal/apex/readcache.go`), and a missing/unparsable one means *not settled*.

## Concurrency
Races are the production bug class here: shared state is mutex-guarded and `go test -race` is the gate. See
`concurrency-and-caching.md`.

## Locks and config reload
Never hold a mutex that an `OnReload` listener takes across anything that can call `config.Load` (`DiskCacheEnabled`, `SessionCacheEnabled`, `memo.DropToken`/`SaveToken`/disk reads, `Uipath()`, `ResolveApex()`): the reload fires the listener on the same goroutine and self-deadlocks — capture under the lock, unlock, then call.

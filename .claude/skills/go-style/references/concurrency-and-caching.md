# Concurrency & caching conventions

Full numbers, keys and the do-not-cache list: `docs/concurrency-and-caching.md`. The rules in one page:

## Concurrency
- **Bound every fan-out.** `shared.MapIsolated` (`internal/shared/mapordered.go`; per-item isolated) for UiPath's
  per-job work at 8 (`maxConcurrency`, `internal/uipath/client.go`) and the sweep cross-check at 4
  (`crossCheckPool`, `internal/sweep/crosscheck.go`); `shared.MapOrdered` (fail-fast) for Apex fan-outs, under the
  data-GET semaphore of 8 inside the chokepoint (`fanOut`/`dataSem`, `internal/apex/client.go`); settings `Source`
  8-slot read semaphore taken only by leaf GETs so nested fan-outs can't deadlock (`readConcurrency`,
  `internal/copilot/settings/source.go`); page windows 4 (`pageWindow` in `internal/sweep/sweep.go` and
  `internal/apex/pages.go`) and 2 (`cloneScanWindow`, `internal/server/tools_orders_clone.go`).
- **Results in input order, failures isolated** (the `Promise.allSettled` contract) unless Promise.all semantics
  were the point (`MapOrdered` cancels on first error).
- **Shared pause on 429**: `rateGate` (`internal/uipath/client.go`) pauses all UiPath traffic until
  `Retry-After` (≤ 5 retries). UiPath replays a 429 and one 401 on any method (neither means the request was
  processed); Apex retries network errors and 429 on GETs only, plus one 401 re-login on any method.
- **Never**: parallelize the mint sequence, `apex.StartJob`, or `apply_settings_sync` writes; retry a write on a
  network error or an Apex 429.
- **Prefetch windows discard cleanly**: a cancelled `context.WithCancel` abandons pages past the early stop.

## Single-flight
Logins, tokens, folder map and crawls go through `shared.SingleFlight` over a `singleflight.Group`
(`copilot/session.go`, `uipath/auth.go`, `apex/session.go`); cache misses through `memo.Fetch`. The shared work runs under `context.WithoutCancel`; each
caller waits on its own ctx; a reset/flush during an in-flight call must not store stale state (generation
counter plus `flights.Forget` in `apex/session.go`, `Flush`-vs-fetch in `memo`).

## Caches
Memory + disk (`internal/memo`, `config.CacheDir()`; PHI at rest, dirs 0700 / files 0600, filenames are key hashes,
512 MiB per namespace, 30 days), terminal+settled-only, verdict from a **live** read in the same call, key includes
upstream URL + resolved folder id, never stores errors/not-found/non-terminal, memory tier flushed on
`config.OnReload`, `refresh: true` bypasses reads, and the write guards call the raw functions. A UiPath hit makes no
upstream call; an Apex hit is served only after a live `getJob` shows the row settled. Add a `ponytail:`
comment for any unmeasured window, cost estimate or bound.

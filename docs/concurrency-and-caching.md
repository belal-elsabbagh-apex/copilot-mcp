# Concurrency and caching

What the Go server caches, what runs concurrently and with which bound, what must never be cached, and what is
still a guess. Condensed from the TS→Go survey and checked against the code (paths are in `internal/`). The
conventions in one page: `.claude/skills/go-style/references/concurrency-and-caching.md`; the rule in CLAUDE.md
is the invariant "Read caches are terminal-and-settled-only, persistent on disk, and never feed a write guard".

## 1. Caches and single-flight

**Persistence is the default.** The terminal-job caches and the two orchestrator tokens live on disk as well as in
memory (§1a), so a restart or a second host process reuses them. `copilot.sessionCache: false` turns the whole
disk tier and all token persistence off (and so does a config that cannot be loaded — fail closed). Every cache
consulted by a read tool can be bypassed per call with `refresh: true` (§1b).

| What | Key | Bound / lifetime | Flushed by | Code |
|---|---|---|---|---|
| Copilot BE session (memory + disk) | `SessionKey(env, be, email, scope)` = sha256 prefix; scope `direct` / `support` / `clinic:<uid>` | fresh while the JWT `timestamp` claim is > 10 min away (`skew`); a token with no readable claim is not cached. Disk: `~/.cache/copilot-mcp/sessions.json` (0600, dir 0700; `COPILOT_MCP_CACHE`/`$XDG_CACHE_HOME`; `copilot.sessionCache:false` = memory only); no email on disk; written via unique temp file + rename | expiry; `ResetSessionCacheForTest` | `copilot/session.go`, `session_cache.go` |
| Session / support-login single-flight | the final session key, for **every** scope, incl. the clinic-less support login shared with `ListClinics` | one in-flight call per key via `shared.SingleFlight` (x/sync `singleflight.Group`), run under `context.WithoutCancel`; each caller waits on its own ctx. Protects the support account's 10 logins / 15 min budget | n/a | `copilot/session.go` (`Connect`, `supportCookies`), `shared/flight.go` |
| Apex JWT + login single-flight (`shared.SingleFlight`) | one process-level token; **persisted** under `<cache dir>/tokens/<sha256(key)>.json` (key = hash of apiUrl + username, never the secret), 0600 | `exp` minus 60 s (`tokenSkew`), 10 min fallback if `exp` unreadable; login capped at 60 s (`loginDeadline`). A restart reuses the persisted JWT instead of burning the rate-limited login route; a 401 drops it from disk too | `ResetClient` (registered on `config.OnReload`; memory only; it `Forget`s the in-flight login/folders keys) ; generation counter stops a reset-during-flight from storing stale state | `apex/session.go` (`loadOrLogin`, `dropToken`), `memo/disk.go` (`LoadToken`/`SaveToken`/`DropToken`) |
| Apex folder name→id map | one single-flighted listing for the process (first page of 200 folders) | process lifetime; a failed fetch is not cached | `ResetClient` | `apex/session.go` |
| UiPath OAuth token | one process-level token, single-flighted (`shared.SingleFlight`); **persisted** like the Apex JWT (key = hash of token URL + client id + scope) | `expires_in` (default 3600 s) minus 60 s; a 401 invalidates only if the cached token is still the rejected one (and drops the persisted file) | `config.OnReload` (unconditional, memory) | `uipath/auth.go` |
| Config | file path + mtime | re-stat on every `config.Load`; changes fire `OnReload` listeners | — | `config/load.go` |
| **Apex artifact listing** | `artifacts\|<apiUrl>\|<folderId>\|<jobId>` | `internal/memo` memory tier (64 MiB total, 8 MiB per entry, 24 h max age, LRU by bytes; row cost flat 256 B) in front of the disk tier (§1a), namespace `apex` | `ResetClient` → `readCache.Flush()` (memory tier) | `apex/readcache.go` |
| **Apex parsed `job.log`** | `log\|<apiUrl>\|<folderId>\|<jobId>\|<artifactId>\|<size>` | same cache and namespace; cost 1.5× raw bytes; a >5 MiB tail read is stored with its `partial` flag | same | `apex/readcache.go` |
| **UiPath RobotLogs** | orchestrator URL, resolved folder, job key, the **full** encoded OData params, `OnlyFailures`, tail | memory + disk, namespace `uipath-logs`; row cost 128 B + text | `config.OnReload` (memory tier) | `uipath/readcache.go` |
| **UiPath terminal job detail** (`Key eq` read incl. Input/OutputArguments; `get_job`, faults builder) | `<orchestrator URL>\|<folder>\|job\|<jobKey>` via `cachedJobByKey` | memory + disk, namespace `uipath-jobs`; stored only when terminal and ended > 5 min ago. `FetchJobByKey` and the `stop_job` guard stay raw | `config.OnReload` (memory tier) | `uipath/readcache.go` (`cachedJobByKey`) |
| Per-operation settings memo | one `Source` per logged-in client: order-type list, speciality tree, order-name tree | the lifetime of ONE tool call; never shared across calls (apply re-plans with fresh `Source`s) | goes out of scope | `copilot/settings/source.go` |
| Per-sweep Apex queue listing | `(env, folder)` inside a ctx made by `apex.WithQueueListMemo` | the lifetime of one `find_stuck_orders` call; errors are not retained; concurrent callers share one listing | goes out of scope | `apex/queuememo.go`, wired in `sweep/sweep.go` |

**The store rule (`apex/readcache.go`, `uipath/readcache.go`, `memo.Fetch`).** The cache never decides what is
safe to store. An entry is stored only when a LIVE read of the job *in the same call* shows it terminal **and**
settled. The live read gates *serving a hit* on Apex only: Apex's `getJob` runs on every call and `cachedRead`
serves a hit only when that live row is settled. UiPath serves a hit straight from `memo.Peek` with no upstream call
(terminal states are absorbing; a per-hit read would spend the tenant's 100 req/min `Jobs` budget) and runs its live
read only on a miss, inside the `memo.Fetch` closure.
- Terminal: UiPath `Successful`/`Faulted`/`Stopped`; Apex `completed`/`failed`/`cancelled`/`lost`/`timed_out`.
- Settled: ended ≥ **5 min** ago, **15 min** for Apex `cancelled`/`lost`/`timed_out` (the statuses most likely to
  be written late, e.g. a superseded retry parent). A missing/unparsable end time is *not* settled.
- Never stored: errors, not-found, non-terminal jobs, values over the per-entry cap (returned but not stored).
- A `Flush` while a fetch is in flight suppresses that fetch's store; if a single-flight leader fails, a waiter
  retries as leader.
- **Job logs and outputs are PHI and now persist on disk** (§1a): same protection as the artifacts directory
  (dir 0700 / files 0600, under the user cache dir, filenames are key hashes) — never commit or paste them. Keys carry
  the upstream URL **and the resolved folder id**, so a job read under folder A can never satisfy a call that
  overrides to folder B.
- Corrupt, expired or unreadable disk files read as a miss and are removed; a disk failure logs one warning per
  cache and the tool call carries on.
- UiPath `get_job_logs` makes one extra light `Jobs` read per id (`Id,Key,State,EndTime`, `fetchJobState`) on a
  **miss only**, to obtain the live verdict for the store decision; it runs in parallel with the logs fetch (no
  added wall time). A hit makes no upstream call at all.

### 1a. The disk tier (`internal/memo/disk.go`, `config/cachedir.go`)

- **Root:** `config.CacheDir()` — the directory of `COPILOT_MCP_CACHE` if set, else `$XDG_CACHE_HOME/copilot-mcp`
  (absolute paths only), else `~/.cache/copilot-mcp`. `copilot/session_cache.go` resolves through the same helper.
- **Layout:** `cache/{apex,uipath-logs,uipath-jobs}/<sha256(key)>.json` (each file `{storedAt, size, value}`, written
  via temp file + rename) and `tokens/<sha256(key)>.json`. Directories 0700, files 0600. Filenames are hashes, so no
  ids appear in a directory listing.
- **Bounds:** 512 MiB per namespace (oldest evicted first, by mtime) and 30 days max age (`memo.DiskMaxBytes`,
  `DiskMaxAge`, both `ponytail:` — unmeasured). An orphaned `*.tmp` (a killed write) older than 1 h is removed during
  eviction; a younger one counts toward the byte total but is not evictable. Memory limits are unchanged. Memory sits in front of disk; a disk hit
  repopulates memory.
- **Never stored:** errors, non-terminal jobs, jobs inside the settle window, over-cap values.
- **Flush:** `config.OnReload` flushes the memory tier only. Disk entries survive a reload but cannot be mis-served:
  keys carry the upstream URL and resolved folder id, and the token keys carry the account identity.
- **Debug:** `LOG_LEVEL=debug` logs `cache namespace=… key=<12-char hash prefix> event=fetch|stored|hit-memory|hit-disk|refresh-bypass`
  (no ids, no PHI).

### 1b. `refresh: true`

An optional boolean on `get_job`, `get_job_logs`, `build_faulted_job_issue` and `get_job_artifacts`. On the first
three it skips the cache **reads** (memory and disk) and still stores a cacheable fresh result, replacing the old
entry. `get_job_artifacts` never reads a cache (it always calls the raw `listArtifacts`), so there `refresh` only
re-downloads files even when the sidecar matches. It travels as a request-scoped context
flag (`memo.RefreshIf`/`WithRefresh`), not through the `orchestrator.Backend` arg structs. Use it when a cached
terminal job looks stale (late-arriving logs, a re-pinned release changing `ProcessVersion`).

## 2. Concurrency and bounds

| Where | Shape | Bound |
|---|---|---|
| UiPath `GetJobs`/`GetJobLogs` per-id work (`fetchJobsForKeys`, `fetchJobLogsForKeys`) | `shared.MapIsolated` — input-ordered, per-key error isolation, no chunk barrier | **8** (`maxConcurrency`, `uipath/client.go`) |
| UiPath transport | one shared `http.Transport` | `MaxIdleConnsPerHost` 16, `MaxIdleConns` 32 |
| UiPath 429 | shared `rateGate`: a 429 pauses **all** UiPath requests until `Retry-After` (default 5 s, clamped 65 s) | ≤ 5 retries on **any method** (`shared.MaxRateLimitRetries`); a 401 gets one token-refresh replay, also any method. Neither means the request was processed, so a write cannot double-post. No network-error retry |
| Apex data GETs and downloads | package semaphore `dataSem` inside `apexRequest`; `shared.MapOrdered` fan-outs (`get_job`/`get_job_logs` over ≤ 25 ids, `list_jobs` over workflows) | **8** (`fanOut`, `apex/client.go`); login and `/folders` are outside it |
| Apex find-order per-queue search and per-item job lookup | `shared.MapOrdered` | **6** (`findConcurrency`, `apex/backend.go`) |
| Apex paging (`apexPages`) | page 1 first (gives `total_pages`), then pages 2..N | window **4**, results in page order, never past `ceil(limit/pageSize)` |
| Apex transport | shared transport, `ResponseHeaderTimeout` 300 s (undici's default, which the TS ran under; a slow `POST /jobs` may already have started) | `MaxIdleConnsPerHost` 16; GET-only retries 3 (network) + 3 (429); a 401 re-logins once and replays any method; no other write retry |
| Settings crawls (`diff`/`get`/`plan`) | every section concurrently; each operation's `Source` has one read semaphore taken by **leaf GETs only** (nested fan-outs cannot deadlock) | **8** per client; payer-map fetch and all syncer crawls run together, planning stays pure |
| `find_stuck_orders` page scan | prefetch window; pages after the first empty one are discarded | **4** (`pageWindow`, `sweep/sweep.go`) |
| `find_stuck_orders` cross-check | order pool; per-order error isolation, input order kept; Apex queue listing memoized per sweep | **4** orders |
| `find_clone_candidates` page scan | prefetch, early stop at limit/empty page abandons ≤ 1 page | window **2** (`cloneScanWindow`) |
| `get_order` | order fetch ‖ `medicalAuthorizations` listing (requested once) | 2 |
| `build_queue_item` | `ClinicOwnerEmail` ‖ `FetchOrder` (errgroup) | 2 |
| Apex `list_triggers`, UiPath faults builder (job ‖ logs) | concurrent pairs | 2 |
| `backends.FindOrderAcross`, `doctor`, `connect(prod)‖connect(pre)` | one goroutine per backend / probe / env | 2 / ~10 / 2 |
| Apex artifact downloads | stream to a 0600 temp file in the target dir then rename (a failed download never leaves a partial under the real name); a file is skipped when it exists **and** the hidden sidecar `.<name>.meta.json` `{artifactId,sizeBytes}` matches the freshly listed artifact | sequential |

Cancellation: the MCP request context is threaded through; a cancelled tool call stops queued work. Single-flight
work is detached (`WithoutCancel`) so one cancelled caller cannot fail the others.

**Never parallel, and never retried on a network error or an Apex 429:** the `MintPreprodOrder` PUT sequence, `apex.StartJob` (reports the jobs already
started when a later POST fails), `apply_settings_sync` writes, `delete_preprod_order` (sequential loop), UiPath's
tier-1 → tier-2 search fallbacks, and Apex `getJob` → `listArtifacts` → `download` (state decides whether the later
steps run).

## 3. Do not cache

1. **Anything feeding a write guard.** Apex `listQueues`/`listWorkflows`/`getJob`/`getQueueItem` in
   `apex/actions.go`; UiPath `FetchJobByKey`/`GetQueueItem` in `uipath/actions.go`; the Apex folder map behind
   `assertEnvFoldersDistinct`. A stale answer could resolve the wrong queue, prove a stale folder membership, or
   skip a fetch-first refusal. Write paths and `get_job_artifacts` call the raw functions; tests prove a write after
   a cached read still re-reads live (Apex `StopJob`, `AddQueueItem` under a memo ctx, UiPath `stopJob`).
2. **Order → queue-item searches and any list** (`find_order_queue_items`, `list_jobs`, `list_queue_items`): they
   grow on re-queue/retry (UiPath old items go `Retried`; Apex a retry appends a job).
3. **UiPath `list_processes` / `list_triggers`** — pin and wiring verification before `start_job`; a cached pin
   would verify the wrong version.
4. **Copilot orders, `get_order`, `search_orders`, `get_order_category_stats`** — status changes after submit; no
   immutability contract.
5. **Settings reads between `plan_settings_sync` and `apply_settings_sync`** — apply must re-plan live; a cache
   would defeat the stale-id (`unmatchedIds`) protection.
6. **Non-terminal jobs, not-found, any error** (a job is "not found" for a moment after `start_job`).
7. **Apex job detail** (`callback_status`/`needs_review` can move after the end; a superseded retry parent can flip
   `failed` → `cancelled`). `getJob` is live on every call and is what supplies the settled verdict.
8. **Queue content (`SpecificContent`: member name/id) and anything not in the §1 table.** The persisted job caches
   hold PHI at rest by an explicit decision; they are bounded (§1a), 0700/0600, and overridable with `refresh`. Do not
   add a new on-disk cache without the same bounds and the settle rule.

## 4. Not done (yet)

The UiPath job-detail cache from the survey *is* done now (§1); its caveat stands: `ProcessVersion`/`RobotName` are live
`$expand` joins, so a cached detail reports them as of cache time (`refresh: true` re-reads).

From the survey; each with the reason it was left out.

- **UiPath unfiltered RobotLogs superset + client-side filtering.** Not done: OData `contains` collation differs
  from Go's, so a local refilter could change results. The cache keys on the exact query instead.
- **Item-attempts cache, UiPath queue-item-by-id cache, queue display-name cache.** Low value (one call per hit
  item / rare repeats); a `Failed` item can still become `Retried` or `Deleted`, so it is not immutable.
- **UiPath tenant token bucket (~90 req/min, burst ~10) and a separate RobotLogs bucket.** Not done: the 100/min
  limit is documented only for `Jobs`/`QueueItems`, whether RobotLogs shares it is unverified, and the bounded pool
  (8) + the shared 429 pause already removed the herd retry. Revisit if 429s still show in `find_stuck_orders` logs.
- **`ReadClient` / `WriteClient` interface split** enforced by the compiler. Not done: the rule is enforced by
  keeping write modules on the raw functions plus tests; the Apex read path needed the live `JobRow` anyway.
- **TTL caches for read-only directory lists** (Apex queues/workflows/schedules 30–60 s, `ListClinics` 5 min,
  `list_queues` 60 s). Not done: modest saving, and every one adds a staleness question next to a write path.
  `ListClinics` only gets the single-flight.
- **Apex folder map: refresh-once-on-miss and 1 h max age.** Not done: a newly created folder is invisible until
  restart or a config edit (as in the TS).
- **Apex artifact downloads with a pool of 3.** Not done: they stream to disk (the memory concern) but stay sequential.
- **Optional `delete_preprod_order` pool of 3.** Destructive, low value: stays sequential.
- **Cross-process disk-cache locking (`flock`).** Not done: a unique temp name + rename removes the torn-file case;
  a lost update costs at most one extra login.
- **Apex `job.log` streaming parse.** The digest read is buffered (including the 5 MiB tail read).

## 5. `ponytail:` numbers still to measure

`grep -rn 'ponytail:' internal/` is the live list. The ones about this subsystem:

| Number | Value now | Measure | Where |
|---|---|---|---|
| Settle windows | 5 min; 15 min Apex `cancelled`/`lost`/`timed_out`; 5 min UiPath | lag from `ended_at`/`EndTime` to the last artifact / last RobotLogs row | `apex/readcache.go` (`settleWindow`), `uipath/readcache.go` (`settleWindow`) |
| Memory cache bounds | 64 MiB total / 8 MiB per entry / 24 h | real `job.log` / RobotLogs sizes and hit rates | `memo/memo.go` (`DefaultTotalBytes`) |
| Disk cache bounds | 512 MiB per namespace / 30 days; a directory scan per write | real sizes, file counts, write cost | `memo/disk.go` (`DiskMaxBytes`, `evict`) |
| Cost estimates | Apex artifact row 256 B, parsed log 1.5× raw; UiPath row 128 B + text | heap per cached entry | `apex/readcache.go` (`artifactRowCost`), `uipath/readcache.go` (`logRowCost`) |
| Apex page window | 4 | Apex rate behaviour (no limit is documented) | `apex/pages.go` (`pageWindow`) |
| Apex data-GET semaphore | 8 | same | `apex/client.go` |
| UiPath pool | 8; transport idle conns 16/host | whether RobotLogs shares the 100/min Jobs bucket | `uipath/client.go` (`maxConcurrency`, `newTransport`) |
| Sweep page window / order pool | 4 / 4 | 429 rate and wall time on a real `find_stuck_orders` | `sweep/sweep.go`, `sweep/crosscheck.go` (`crossCheckPool`) |
| Apex folder listing | first page of 200 only | folder count in a real deployment | `apex/session.go` (`folderMap`) |

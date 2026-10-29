# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

An MCP server (`github.com/modelcontextprotocol/go-sdk` v1.8.0, `mcp.Server` over **stdio**) exposing EHR
Copilot order operations + orchestrator (UiPath and Apex) execution analysis. It is a Go program
(`go 1.27.0`, stdlib + the go-sdk + `golang.org/x/sync` + `google/jsonschema-go`). It is still **served
through npx**: the npm package's `bin/copilot-mcp.js` is a Node-builtins-only launcher that execs the
bundled static Go binary for the host from `dist/<goos>-<goarch>/copilot-mcp[.exe]`. Node is needed only to
run that launcher; Go is needed only to develop. **Not published to any registry** (`package.json` is
`"private": true`, there is no `.npmrc`): users install with `npx -y <GitHub release asset URL>`
(`…/releases/latest/download/copilot-mcp.tgz`, or `…/releases/download/vX.Y.Z/copilot-mcp.tgz`) or the `.mcpb`.
Only three platforms ship: linux/amd64, darwin/arm64, windows/amd64 (the tarball is ~13 MB: each Go binary carries
its own runtime).

The Go code follows Belal's referral-router conventions: **read `.claude/skills/go-style/SKILL.md` before
writing or reviewing Go here.** Concurrency and caching rules live in `docs/concurrency-and-caching.md`.

## Commands

```bash
make check                     # THE gate: vet, gofmt, golangci-lint v2.15.0, actionlint, go test -race, package smoke, release.sh self-test
make build                     # CGO_ENABLED=0 go build -> ./copilot-mcp (version from `git describe`)
make test                      # go test -race ./...
go test -race ./internal/copilot/settings/ -run TestName   # one package / one test (-race is the project norm)
make fmt | vet | lint | lint-actions   # the individual gate steps
make dist                      # cross-build dist/<goos>-<goarch>/ for the 3 shipped platforms (Makefile PLATFORMS)
make package                   # dist + launcher `initialize` smoke + tarball contents check + cold `npx -y http://127.0.0.1:<port>/copilot-mcp.tgz`
make mcpb                      # build copilot-mcp.mcpb (needs the version stamped into manifest.json)
make hooks                     # opt in: git core.hooksPath=.githooks, so pre-push runs `make check`
make release VERSION=X.Y.Z     # tag-driven release (DRY_RUN=1 prints the plan); see Releasing
go run ./cmd/copilot-mcp       # run from source (needs COPILOT_MCP_CONFIG)
COPILOT_MCP_CONFIG=… npx @modelcontextprotocol/inspector ./copilot-mcp   # MCP Inspector UI against a built binary
```

`make check` is the single definition of the gate — `.github/workflows/check.yml` runs exactly it, and
`release.yml` calls `check.yml` before building. Before finishing a change, a clean `make check` is the bar
(not "the code looks right"). Linters are pinned in the Makefile; whenever the `go` directive or
`check.yml`'s patch-pinned `go-version` moves, re-verify the `GOLANGCI` pin.

Logging is silent below warn so it never touches the stdio stream; `LOG_LEVEL=debug` (or
`COPILOT_MCP_DEBUG=1`) routes slog output to **stderr** (`internal/server/server.go`, `LogLevel`).

## Architecture

`internal/` is organized by domain; tests (`*_test.go`) live beside the code they cover. Import
direction, never upward (a cycle means: move the smaller piece down a layer):

```
shared ← config ← {reference, memo} ← orchestrator ← {uipath, apex} ← backends ← sweep ← server ← cmd/copilot-mcp
copilot (+ copilot/settings) sits beside orchestrator: it imports shared, config and memo (never orchestrator, uipath or apex).
queueitem imports copilot + reference + config + shared (not backends); doctor imports copilot, uipath and apex directly.
memo imports config (cache dir, disk-cache switch, reload hook) and is used by copilot, uipath and apex.
```

- `cmd/copilot-mcp` — entry point. `var version` is stamped with `-X main.version` from the git tag (no
  version constant anywhere). Runs `server.New(version).Run(ctx, &mcp.StdioTransport{})`.
- `internal/server` — **MCP wiring only**: `server.go` (`New`, `Run`, logging install), `tool.go`
  (`addTool`, `readOnlyHints`/`writeHints`, `toolError`, panic recovery), `schema.go` (JSON-schema patches:
  `Enum`, `Default`, `MinLen`, …), `render.go`, `resources.go`, `prompts*.go`, `feedback.go`,
  `notify.go`/`logging.go`, and one `tools_*.go` per tool family (each ends in `func init() { addToolSet(…) }`).
  Tool *logic* lives in the domain packages — keep this package declarative.
- `internal/config` — validated config loading (`Load`, live reload by file mtime, `OnReload`),
  `ResolveAuth` / `ResolveSupport` / `ListProfiles`, `ResolveApex`, `Uipath()`, `Feedback()`. `Env`
  (`config.Prod`, `config.PreProd`). `ApexNotConfiguredError` lives here so config never imports apex.
- `internal/shared` — the one copy of every cross-cutting helper (don't re-add a local one):
  `Expected`/`ExpectedError`/`Expectedf`/`IsExpected`; `Prop`/`StringProp`/`SafeJSONParse`/`Str`/`NonEmpty`; the
  three-state `Field` + `Pass`/`Coalesce` (JS `??` chains) + `NPIField`; `MarshalJSON` (JSON without HTML escaping);
  `SingleFlight` (over `x/sync/singleflight`); `MapOrdered` (fail-fast) and `MapIsolated` (per-item isolated), both
  errgroup-bounded; `JWTClaim`; `SliceUTF16` (JS `slice`) and `URIComponent` (JS `encodeURIComponent`);
  `StepProgress`; rate-limit constants + `RateLimitDelay`/`Sleep`.
- `internal/reference` — static data (portal registry, orchestrator folders, safety rules, config guide,
  schemas) embedded from `data/*.json`.
- `internal/orchestrator` — the backend-neutral contract: `types.go` (`JobCore`, `QueueItemCore`,
  `QueueCore`, `ProcessCore`, `TriggerCore`; backend-specific fields live in each core's `Details`),
  `backend.go` (`Backend`, the one interface both backends implement; its queue and write arg/result types live in
  `backend_queue.go` / `backend_writes.go`), plus generic code: `log_digest.go`, `log_semantics.go`, `safety.go`
  (`GuardQueueItemSafety`), `faults.go`, `portal.go`, `job_batch.go`, `fields.go` (`Field`/`Fields`, an ordered
  JSON object for wire-visible key order), `text.go` (JS trim/lower/UTF-16 semantics). Must NOT import
  copilot/uipath/apex.
- `internal/copilot` — Copilot BE: `client.go` (`Client`, the unexported env-tagging `newHTTPClient`, `AssertPreProdClient`), `session.go` +
  `session_cache.go` (`Connect`, `ListClinics`), `mirror.go` + `mirror_steps.go` (`MintPreprodOrder`), `order.go`
  (`SubmitOrder`, `FetchOrder`, `FilterOrders`), `order_docs.go`, `order_search.go`, `dates.go`. `settings/` is the
  settings catalog + diff/plan/apply (below).
- `internal/uipath` — UiPath backend (`uipath.Backend`) incl. the output schema (`outputschema.go`).
- `internal/apex` — Apex backend (`apex.Backend`): `client.go` (the chokepoint), `session.go` (login +
  folder map), `api.go`, `actions.go` (writes), `artifacts.go`, `joblog.go`, `faults.go`, `payload.go`,
  `readcache.go`/`queuememo.go`/`readsupport.go`/`pages.go` (read caching + bounded paging); fan-outs use
  `shared.MapOrdered`.
- `internal/memo` — byte-bounded LRU with its own per-key single-flight and an optional disk tier (`NewPersistent`,
  `disk.go`: entries + persisted tokens under `config.CacheDir()`, `WriteAtomic`), backing the terminal-job read
  caches; `RefreshIf` carries `refresh`.
- `internal/backends` — `BackendFor`, `SearchableBackends`, `SelectBackends`, `FindOrderAcross` (`server` and
  `sweep` dispatch orchestrator work through it; `doctor` is the other importer of both backends).
- `internal/queueitem` (canonical native queue-item content builder), `internal/sweep` (`find_stuck_orders`),
  `internal/doctor` (`doctor` probes).

Concepts:

- **Hybrid static/live model**: stable facts (portal registry, orchestrator folders, schemas, safety rules in
  `reference/`) are exposed as **resources**; anything that changes per-run (orders, queue items, jobs,
  settings) is a **tool**. Don't turn live data into a resource.
- **Three external systems**: Copilot BE via `copilot.Connect` (which builds its env-tagged client), UiPath Orchestrator via
  `internal/uipath`, and the Apex orchestrator via `internal/apex/client.go`. Each Copilot tenant exists
  per-env (`prod` / `pre_prod`); both orchestrators sit behind `orchestrator.Backend`.
- **Config**: one validated config (single-file `COPILOT_MCP_CONFIG`, or split legacy files via
  `COPILOT_MCP_LOCAL_DIR`). `config.ResolveAuth(profile, env)` returns an `EnvAuth` with `Mode` `ModeSupport`
  (a `ClinicUID` pointer — creds come from `config.ResolveSupport(env)`, i.e. `copilot.support.<env>`) or
  `ModeDirect` (the profile entry's own `be`/`email`/`password`). Profiles are dynamic — read them with
  `config.ListProfiles()`; never hardcode profile names. Reloads fire `config.OnReload` listeners (they flush
  every cache and token below).
- **`copilot/session.go`** is the single auth chokepoint for the Copilot BE — nothing else may POST
  `physician/login` or `support/switch-clinic`. `Connect(ctx, env, profile)` resolves the `EnvAuth` and either
  logs in directly or logs in the support account then switch-clinics, returning a `Session`. Sessions are
  cached by `SessionKey(env, be, email, scope)` in memory and, by default, on disk
  (`~/.cache/copilot-mcp/sessions.json`, mode `0600`, `COPILOT_MCP_CACHE` / `$XDG_CACHE_HOME` override;
  `copilot.sessionCache: false` disables disk), single-flighted (`shared.SingleFlight`) on the final key for every scope (direct,
  clinic-less support, clinic) so concurrent tool calls never double-login, and TTL-aware from the JWT's own
  expiry claim. `ListClinics` (backs `list_clinics`) shares that single-flight.
- **`copilot/settings/`** (`diff_settings` / `get_settings` / `plan_settings_sync` / `apply_settings_sync`):
  `SettingsCatalog` (`catalog.go`) is one data table of sections (`SettingsSection`, `types.go`); a section
  optionally carries a `Sync SectionSyncer` (see the Invariants entry below) — specialities and orders are the
  two implementations. `diffengine.go` strips env-specific noise (UIDs, timestamps, dummy emails, CDN hosts)
  and matches list items by a **semantic key**, never by UID. Each operation builds a `Source` per logged-in
  client (`source.go`): it owns an 8-slot read semaphore and memoizes the shared crawls for that operation
  only. Keep the pure diff/plan functions pure.
- **`internal/server/notify.go` / `logging.go`**: `s.mcpLog()` sends MCP `notifications/message`;
  `reportProgress()` sends `notifications/progress` only when the caller passed a progress token; both are
  no-throw side-channels. The SDK mutes logging until the client calls `logging/setLevel`, so `Server.Connect`
  / `Run` seed level `info` — always use those, never the embedded `mcp.Server.Connect`.
- **`internal/uipath`**: `client.go` (shared transport, `rateGate` 429 pause), `auth.go` (OAuth-first,
  static bearer PAT fallback; token fetch single-flighted via `shared.SingleFlight`), `actions.go` (the ONLY module
  that mutates UiPath), `readcache.go`, `odata.go`; per-job fan-outs use `shared.MapIsolated`, bound 8 (`maxConcurrency`).
- **`internal/apex`**: see the chokepoint invariant below. A transaction maps to many jobs on Apex (`Jobs[]`,
  newest first); verdicts use the latest attempt. State vocabulary on both backends is UiPath's labels (Apex
  statuses are mapped; `RawState` keeps the original). Config: `apex` block (`apiUrl` = LB host, never
  CloudFront; `username`/`password`; optional `folderByEnv`; `serverUrlByEnv`/`queueUrl` = write-guard limits),
  each of `apiUrl`/`username`/`password` falling back to `APEX_API_URL`/`APEX_USER_NAME`/`APEX_PASSWORD`
  (`config.ResolveApex`).
- **`orchestrator/faults.go` + `uipath/faults.go` + `apex/faults.go`** (`build_faulted_job_issue` + the
  `report-faulted-jobs` prompt): turn a faulted job into a ready-to-post GitHub issue payload but **post
  nothing** — this server holds no GitHub credentials. The prompt drives a **GitHub MCP server connected in the
  host**. Dedupe is by **fault signature** (`orchestrator.NormalizeError` strips GUIDs/ids/timestamps so
  recurrences group into one issue). UiPath signatures are byte-identical to the pre-2.0 ones; Apex signatures
  are `apex:`-prefixed so the two never collide.

## Invariants (do not break)

- **Every Copilot operation requires an `env` (`prod` | `pre_prod`) and a `profile`
  (`ossm`, `kafri`, …) plus its other args.** These are **required** tool/prompt parameters — never add a
  default env or default profile, and never silently assume one. In Go: a field without `omitempty` is required
  (`internal/server/schema.go`); `profile` is validated against the config at call time by
  `config.ResolveAuth`/`ResolveSupport`. The carve-out is `list_clinics` — it takes `env` but no `profile`,
  because it's the discovery tool that produces the profile → clinicUid mapping. Orchestrator tools
  (`list_jobs`, `get_job`, `get_job_logs`, `get_job_artifacts`, `build_faulted_job_issue`, `list_queues`,
  `list_queue_items`, `find_order_queue_items`, `pull_queue_item`, `list_processes`, `list_triggers`,
  `add_queue_item`, `delete_queue_item`, `start_job`, `stop_job`) also take `env` but **no `profile`** —
  orchestrators authenticate globally. UiPath: `uipath.oauth` (client-credentials, tried first) and/or the
  static `uipath.bearer` PAT (fallback; `resolveBearerToken` in `uipath/auth.go`, the chokepoint every request
  calls). Apex: one login (`apex/session.go`; requests go through `apex/client.go`). Still never default `env`.
- **Copilot BE auth is one chokepoint, two modes, and a shared session cache.** `copilot.Connect` is the ONLY
  place allowed to POST `physician/login` or `support/switch-clinic` — do not add a second call site. A
  profile's env entry is either support mode (a `clinicUid` pointer; creds from `copilot.support.<env>`,
  shared across every profile using that mode) or direct mode (own `be`/`email`/`password`); `clinicUid` wins
  if an entry carries both. Sessions are cached by `(env, be, email, scope)`, single-flighted, and TTL-aware
  from the JWT's own expiry claim (10-minute skew) — this is what keeps the support account's 10 logins / 15
  min budget from being burned. Disk persistence is on by default (`copilot.sessionCache: false` disables it;
  `COPILOT_MCP_CACHE` overrides the path), is written via a unique temp file + rename, and never holds the
  account email (cache keys are hashed).
- **stdout is the JSON-RPC channel.** Never `fmt.Print*` / `log.Print*` / `os.Stdout` in non-test code
  (currently none exist). Diagnostics go to `slog`, which `server.InstallLogging()` points at stderr; use
  `s.mcpLog()` for client-visible logs; stderr level via `LOG_LEVEL=debug` / `COPILOT_MCP_DEBUG=1`. The
  launcher (`bin/copilot-mcp.js`) likewise writes only to stderr, and `make package` fails if stdout carries
  more than the one `initialize` response line.
- **prod/pre-prod isolation**: writes target pre-prod only; `delete_preprod_order` never touches prod;
  `create_preprod_order` never submits (only `submit_preprod_order` does, and only with explicit user
  authorization); `build_queue_item` / `pull_queue_item` force `IsApproved=false` (`orchestrator.SafetyForce`)
  so a test run can never submit a real auth. BE clients are env-tagged at construction (`newHTTPClient(base, env, cookies)` in `copilot/client.go`) and
  every BE write engine (`MintPreprodOrder`, `SubmitOrder`, the `delete_preprod_order` handler,
  `settings.ApplySettingsSync`) calls `copilot.AssertPreProdClient`, which refuses prod-tagged AND untagged
  clients (fails closed) — tag any new client at construction and assert before any new write path.
- **Order minting is one engine, one producer.** `copilot.MintPreprodOrder(ctx, pre, spec, onProgress)`
  (`copilot/mirror.go`) owns the sequence invariants (order names reset speciality/provider; placeOfService
  LAST, post-forReview; E6001-tolerant `/process` retry) and **always stops at forReview** — it never submits.
  `create_preprod_order` is its only caller, minting from an explicit `MintSpec`; there is no dedicated "clone"
  tool — the `clone-and-verify-order` prompt reads a prod order via `get_order` and derives the spec fields
  itself (no automated cross-env reference matching exists; an agent resolves prod names to pre-prod uids via
  `get_settings`/`diff_settings`, and the settings tools diagnose a reference that doesn't resolve). Submitting
  is a fully separate step: `copilot.SubmitOrder` backs `submit_preprod_order`, the only thing that may submit,
  always explicit. Do NOT expose the raw atoms (create draft / PUT field / process) as tools — the tool
  boundary is the safe transaction, not the HTTP verb.
- **UiPath writes are dev-clone-only and guarded.** `add_queue_item`, `delete_queue_item`, `start_job`, and
  `stop_job` (UiPath half in `internal/uipath/actions.go` — the ONLY file that mutates UiPath Orchestrator
  state) take a `pre_prod`-only `env` at the schema layer (`preProdOnly()` in
  `internal/server/tools_orchestrator_common.go`, a JSON-schema `const`) AND assert `env == config.PreProd` in
  the domain function (`assertPreProd`). Every posted SpecificContent passes `orchestrator.GuardQueueItemSafety`
  (`orchestrator/safety.go`, pure, unit-tested — the single enforcement point for BOTH orchestrators, also used
  by `build_queue_item`/`pull_queue_item`): IsApproved forced false; non-empty `serverURL`/`queueUrl` must match
  the configured pre-prod values (fails closed when unconfigured); `<TO-FILL>` placeholders rejected.
  `delete_queue_item` is fetch-first and refuses any item whose Status isn't `New`; `stop_job` is likewise
  fetch-first (resolving the caller's GUID Key to Orchestrator's numeric Id scoped to the pre-prod folder) and
  refuses a job whose State is already `Successful`/`Faulted`/`Stopped`. The MCP never repins releases and never
  creates queues — `list_processes`/`list_queues`/`list_triggers` are the read-side discovery/verification tools
  (dev-clone queue ids differ from the prod ids in `reference.Portals`). Retries (`uipath/client.go`, as the TS
  client did): a 401 is replayed once with a fresh token and a 429 up to `shared.MaxRateLimitRetries` times after
  `Retry-After`, for **every** method — neither means the request was processed, so replaying a write cannot
  double-post. There is no network-error retry.
- **Apex has one HTTP chokepoint and always scopes to a folder.** All Apex HTTP lives in `internal/apex/client.go`
  (+ `session.go` for login and `/folders`). `apexRequest` is the only entry point to Apex's data and write
  routes: it always sends `X-Folder-Id` (omitting it silently reads the user's default folder), sends only
  verified query params (unknown ones are silently ignored by the server), and refuses any request other than GET
  unless `env == config.PreProd` (`assertWritable`) — before any network call. It also refuses a write whose
  pre_prod and prod folders resolve to the same id (`assertEnvFoldersDistinct`). The only other requests, all in
  `internal/apex`, are the login POST, the `GET /folders` listing, and `apexDownload` (GET-only, Bearer,
  same-origin `/api/v1/` paths, no folder header). Network-error and 429 retries are GET-only (a retried write could
  double-post); the one exception is `transport`'s single re-login on a 401, which replays any method because the
  server rejected the credentials and ran nothing. The response-header timeout is 300 s (undici's default, which the
  TS ran under: a slow `POST /jobs` may already have started).
- **Apex writes are test-folder-only and guarded.** Mirrors the UiPath invariant: schema `preProdOnly()`, domain
  assert (`assertPreProd`), the chokepoint gate, `GuardQueueItemSafety`, and fetch-first refusal
  (`delete_queue_item` only on `new`; `stop_job` refuses every terminal status including `lost`/`timed_out`), plus
  a client-side folder proof: `stop_job` refuses a job whose `folder_id` is not the pre_prod folder's, and
  `delete_queue_item` refuses a `queueId` missing from the pre_prod folder's queue listing (by-id routes are not
  verified to be folder-scoped server-side). Writes never take a `folder` override. `internal/apex/actions.go` is
  the ONLY file that mutates Apex. Prod and test folders share one deployment and one login, so the folder is the
  only boundary — a bug that drops the gate could write to prod.
- **`orchestrator` is required on job- and queue-level tools, never defaulted.** The carve-outs are
  `find_order_queue_items` and `find_stuck_orders` (omitted = every registered backend, via
  `backends.SearchableBackends`) and the Apex-only `get_job_artifacts` (no `orchestrator` arg). **The MCP never
  deploys, pins, creates Processes or queues, or touches Apex credentials, assets or Action Center.** Artifacts
  download only under `~/.cache/copilot-mcp/artifacts/` (mode `0700`), never inside a repo.
- **Settings sync is a plan/apply pair — additive, pre-prod-only.** `plan_settings_sync` is READ-ONLY and returns
  the planned actions, each with a stable id (`section:op:typeName:itemName`). `apply_settings_sync` never
  accepts request bodies — it **re-plans server-side** (fresh `Source`s) and executes only the actions selected
  by `actionIds` or an explicit `all:true` (**exactly one required**; no default "apply everything"; a stale id
  after state drift lands in `unmatchedIds` instead of executing). It copies prod-only settings into pre-prod and
  never overwrites/deletes (the `changed`/`onlyInPreProd` sets are left untouched). It covers two outbound
  order-type domains: **specialties** (`specialties` / `referred-providers` / `referred-facilities`: create
  prod-only specialties, merge prod-only facilities/providers into existing ones with a full-replace `PUT`) and
  **orders** (create prod-only orders under matching order types — create-only, no verified name-update
  endpoint). All env-specific references are **remapped** prod→pre by **name** (never stripped, never copied
  raw); anything with no pre-prod match is **dropped with a warning**. Sections with no verified write endpoint
  are reported under `skippedSections` automatically — add a syncable domain by implementing
  `settings.SectionSyncer` (`Domain()` + `Crawl(ctx, prod, pre *Source) (Planner, error)`, planning is pure) and
  attaching it to the section's `sync` field, only once its write endpoint is verified.
- **Read caches are persistent, terminal-and-settled-only, and never feed a write guard.** The only caches are
  the terminal-job read caches (`internal/memo` via `apex/readcache.go`, `uipath/readcache.go`: Apex artifact
  listing + parsed `job.log`, UiPath RobotLogs + terminal job detail) plus the single-flighted, persisted
  tokens/logins and the Apex folder map. They are memory **and** disk (`<config.CacheDir()>/cache/<ns>/`,
  `tokens/`; dirs 0700, files 0600, filenames are key hashes; 512 MiB per namespace, 30 days) — job logs/outputs
  are PHI at rest, protected like the artifacts dir. `copilot.sessionCache: false` disables the whole disk tier
  and token persistence. A value is stored only when a LIVE read in the same call shows the job terminal AND
  settled (5 min; 15 min for Apex `cancelled`/`lost`/`timed_out`); errors, not-found and non-terminal reads are
  never stored; keys include the upstream URL and the RESOLVED folder id; `config.OnReload` flushes the memory
  tier (disk keys are scoped, so stale entries cannot be mis-served). The hit path differs by backend: Apex reads the
  job live every call (`cachedRead` serves a hit only when that live row is settled); UiPath serves a hit with no
  upstream call (terminal states are absorbing; a per-hit read would spend the 100 req/min `Jobs` budget) and makes
  its light job-state read only on a miss. The cache-consulting read tools (`get_job`, `get_job_logs`,
  `build_faulted_job_issue`) take an optional `refresh: boolean` that skips cache reads and re-stores the fresh
  result (`memo.RefreshIf`); keep that arg on any new cache-consulting tool. `get_job_artifacts` also takes
  `refresh` but never reads a cache (it always calls the raw `listArtifacts`); there it only forces a re-download
  past the sidecar match.
  Write paths and fetch-first guards call the raw functions
  (`apex.getJob`/`listQueues`/`listWorkflows`/`getQueueItem`, `uipath.FetchJobByKey`/`GetQueueItem`) — never a
  cached wrapper. Do not add a cache without reading `docs/concurrency-and-caching.md` (it lists what must never
  be cached, and why).
- **Concurrency is always bounded, and writes are never parallel or blindly retried.** Fan-outs go through a bounded
  pool/semaphore — `shared.MapIsolated` (per-item isolated; UiPath 8, sweep cross-check 4) or `shared.MapOrdered`
  (fail-fast, input order; Apex), the Apex data-GET semaphore 8, settings `Source` 8, page windows 4/2 — with
  results in input order; a UiPath 429 pauses all UiPath traffic via the shared gate. Never parallelize the mint
  sequence, `StartJob`, or `apply_settings_sync` writes; never retry a write on a network error or an Apex 429
  (the only write replays are a rejected-credentials 401 on either backend and a UiPath 429, see the two
  orchestrator-write invariants). Shared mutable state is mutex-guarded and `go test -race` (part of `make check`)
  is the gate. Single-flight work goes through `shared.SingleFlight`, which runs under `context.WithoutCancel` so
  one cancelled tool call cannot fail the others sharing a login.
- **JSON out goes through `shared.MarshalJSON`, never an inline `json.Encoder`/`json.Marshal`.** `JSON.stringify`
  never HTML-escapes, and a `MarshalJSON` method's output is copied into the tool result as-is (it bypasses
  `renderJSON`'s escaping setting), so any local encoder would turn `<TO-FILL>` into `\u003cTO-FILL` and defeat the
  write guard. Text caps and URI escaping likewise use `shared.SliceUTF16` / `shared.URIComponent`.
- **NPI values are always strings in tool output** (`shared.NPIField`: a numeric BE NPI becomes its exact decimal
  string). This is a deliberate difference from the v2.0.0 TS server, which passed numbers through.
- Tools return through `addTool` (`internal/server/tool.go`), which renders results as JSON text and turns every
  error or panic into the `{error, reportIssue?}` error body, and declare all four annotation hints
  (`readOnlyHints(openWorld)` / `writeHints(destructive, idempotent)`). `registry_test.go`
  (`TestEveryToolDeclaresTitleDescriptionAndAllFourAnnotationHints`) enforces the wiring and
  `TestRegistersExactlyTheExpectedTools` pins the tool list.

## Releasing

Tag-driven, and **nothing is bumped by hand**: the version is the git tag (`-X main.version`); `package.json`
and `manifest.json` stay at `0.0.0-dev` in git and `release.yml` stamps the tag's version into them. To release:
add a `## [X.Y.Z] - YYYY-MM-DD` entry to `CHANGELOG.md` (Keep a Changelog; `scripts/release.sh` refuses a version
with no such heading), merge to `main`, then `make release VERSION=X.Y.Z` (`DRY_RUN=1` first). `release.sh`
refuses an existing tag, a dirty tree, or a HEAD that isn't `origin/main`, then pushes only the tag.
`.github/workflows/release.yml` then refuses a tag not on `origin/main`, runs the gate, stamps the tag's version
into `package.json`/`manifest.json`, runs `make dist VERSION=<tag without v>` (3 binaries) + `npm pack` +
`make mcpb VERSION=…`, and creates the GitHub release with `copilot-mcp.tgz`, the versioned
`belal-elsabbagh-apex-copilot-mcp-X.Y.Z.tgz`, `copilot-mcp.mcpb` and the raw binaries. **There is no publish step**
(no registry, no `packages: write`); users install from the release-asset URL. A `-` in the version
(`2.1.0-rc.1`) creates a GitHub prerelease. `VERSION` is passed explicitly because the stamp dirties the tree, and
the Makefile's `git describe --dirty` default would otherwise report `X.Y.Z-dirty`.

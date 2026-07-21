# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.23.1] - 2026-07-21

### Fixed

- **`build_faulted_job_issue` no longer fails outright on a transient robot-logs
  fetch error.** A prior change made `fetchFilteredJobLogs` propagate any non-404
  error instead of swallowing it, but this tool's logs fetch wasn't updated to
  catch per-item errors like its sibling batch paths (`get_job`, `analyze.ts`) —
  so a genuinely faulted job with a flaky logs fetch discarded an otherwise-valid
  job lookup. The logs fetch is now isolated in its own try/catch and degrades to
  a `logsError` field (issue still built from the job alone) instead of rejecting
  the whole call.

### Changed

- **Deduplicated the 403 `missing_token` session-refresh retry** (previously
  hand-copied in three places: `mirror.ts`'s PUTs, its note-upload POST, and
  `copilot-client.ts`'s `submitOrder`) into one shared `reqWithRefresh` helper.
- **`find_stuck_orders`'s `crossCheckUipath` cross-check now batches its
  Orchestrator lookups** (`chunk(10)` + `Promise.allSettled`, the same shape
  `uipath.ts` already uses for job batches) instead of awaiting one order at a
  time — latency no longer scales linearly with the number of stuck orders found.
- **`diff_settings`/`get_settings` fetch every catalog section concurrently**
  instead of one at a time; same for the orders and specialities tree crawls
  (`settings/orders.ts`, `settings/specialities.ts`) — each order type's/
  speciality's GET is independent, so they no longer pay each round-trip in turn.
- **`copilot-client.ts`'s `req()` now reuses `shared/util.ts`'s `safeJsonParse`**
  instead of a hand-rolled JSON/raw-text fallback that disagreed with `uipath.ts`
  on how an empty response body is represented (`""` vs `undefined`).
- **`queue-item.ts`'s `toMDY` is now a thin wrapper around `copilot-client.ts`'s
  `toMDY`** instead of a second, independently-drifting copy of the same
  date-parsing rules.
- **`mintPreprodOrder` reports progress via an optional `onProgress` callback**,
  wired to `reportProgress` in `create_preprod_order`'s handler — a caller
  watching a mint that's mid-retry (up to 6 `/process` attempts, 5s apart) now
  sees live progress instead of nothing until the tool returns.

## [1.23.0] - 2026-07-21

### Changed

- **Trimmed avoidable repetition out of several large, multi-row tool responses**,
  found by auditing real transcripts for GET-type calls returning many same-shaped
  elements:
  - **`list_jobs`/`get_job`/`start_job`**: rows no longer carry a per-row `deepLink`
    (a full Orchestrator URL differing only by the row's own `key` — measured at
    36.5% of one real 57-row `list_jobs` payload). Each call now returns a single
    top-level `jobDeepLinkBase` (a `"{key}"`-templated URL); build a job's link by
    substituting its `key` into that template. **Breaking**: `deepLink` is no
    longer present on job rows.
  - **`search_orders`**: a row no longer repeats `insurance`/`orderType` when the
    request already filtered to exactly one value for that dimension — restating
    the filter the caller just supplied added no information. Still included when
    that dimension is unfiltered or matches multiple values, since then per-row
    values can differ. **Breaking**: `insurance`/`orderType` may now be absent from
    a row.
  - **`analyze_order_execution`/`get_job`'s `logDigest.failures`**: the underlying
    failure-language heuristic (`isFailureLog`) now only scans the first 200
    characters of a log message instead of the whole thing. Fixes a real false
    positive found in the audit: a job that genuinely succeeded (`result:
    SUBMITTED`) was still showing up in the digest because one log line embedded
    an entire `result.json` object, and a generic word buried past character 400
    of that blob incidentally matched — with no bearing on the actual outcome.
    Genuine short failure messages (`"ABORT: ..."`, `"Unable to find submit
    button"`, etc.) all front-load their meaning well within the new window, so
    detection of real failures is unaffected.

## [1.22.0] - 2026-07-21

### Added

- **`stop_job`**: stop (`SoftStop`, graceful) or kill (`Kill`, immediate) a running job
  in the dev-clone folder. `pre_prod`-only (schema `z.literal("pre_prod")` + domain
  assert, same pattern as `add_queue_item`/`delete_queue_item`/`start_job`) and
  fetch-first like `delete_queue_item`: resolves the caller's GUID `Key` to
  Orchestrator's numeric `Id` scoped to the pre-prod folder (so a prod job's `Key`
  resolves to nothing here), and refuses a job already `Successful`/`Faulted`/`Stopped`.

### Changed

- **UiPath HTTP errors are now typed (`UiPathApiError`: status/method/url/body)**
  instead of a bare `Error` with the status baked into the message string, and
  `uipathRequest` retries once on a 401 with a freshly-refetched token (a
  revoked/expired OAuth token now self-heals instead of failing for the rest of the
  process). `UiPathApiError` extends the existing `ExpectedError` so `classify()`
  keeps treating upstream HTTP failures as expected, not a bug in this server.
- **Stopped silently swallowing real UiPath errors as "not found."** Four read
  helpers (`fetchJobDetailsById`, `getQueueDefinitionName`, `fetchJobVideoUrl`, plus
  `fetchJobByKey`/`fetchFilteredJobLogs`) used to catch *any* error — network, auth,
  500 — and return `null`/`""`/`[]`, indistinguishable from a legitimate empty
  result. They now only swallow a genuine 404; everything else propagates.
- **Batch/best-effort call sites report per-item errors instead of losing the whole
  result.** `get_job`'s `jobKeys` batch, `get_job_logs`'s `jobKeys` batch,
  `analyze_order_execution`'s per-job log/video fetch, and `pull_queue_item`'s
  queue-name fallback lookup now attach a real failure to just the affected
  item/job (`error`/`logsError`/`videoError`/`logDigestError`/`queueNameError`)
  instead of either losing the rest of the batch or silently degrading. A single
  explicit `get_job`/`get_job_logs` lookup still fails the call normally on a real
  error, same as any other tool error.

## [1.21.0] - 2026-07-20

### Added

- **`search_orders`**: filter an account's Copilot orders by location, insurance,
  referred-to, order type, auth/upload status, MRN, free-text search, or any of 5
  date ranges, via `POST /orders/filter`. No existing tool exposed multi-dimension
  order search — `get_order` is single-uid lookup, and `find_stuck_orders` /
  `find_clone_candidates` only scan pages unfiltered-by-dimension. READ-ONLY, returns
  slim non-PHI rows (no `patient` field), matching the existing bulk-tool convention
  rather than `get_order`'s full-detail-with-patient shape. Defaults to
  `type='Outbound Referral'`, `pageSize=100`, `pageNumber=1`.
- **`get_order_category_stats`**: cheap per-folder order counts (For Review, PCP
  Notes, Processing, Archived) for a given filter, via `POST /orders/category/stats`
  — same filter dimensions as `search_orders`, no pagination.

### Changed

- **Consolidated the `/orders/filter` request-building + pagination logic**, which
  had been duplicated independently three times (`copilot-client.ts`'s
  `fetchOrder`/`verify`, `sweep.ts`'s `findStuckOrders`, and `find_clone_candidates`
  in `server.ts`, each with its own local order-row interface), into shared
  `filterOrders()`/`categoryStats()` helpers in `copilot-client.ts`. Adding the two
  new tools was the trigger; no behavior change to the four existing call sites.

## [1.20.0] - 2026-07-20

### Changed

- **BREAKING: settings-catalog `group` renamed to `tags` (many-to-many)** across the five
  settings tools (`diff_settings`, `get_settings`, `plan_settings_sync`, `apply_settings_sync`:
  `groups` param → `tags`; `list_setting_sections`: `group` param → `tag`) and their response
  shapes (`sections[].group` → `sections[].tags`, `{groups}` → `{tags}`). `group` was never a
  live Copilot API concept — a static one-to-one categorization invented when the catalog was
  reverse-engineered from a HAR capture. `tags: string[]` lets a section belong to more than
  one category; every section still carries exactly the one tag it had before, in a 1-element
  array — no section was re-tagged in this pass. Filtering is now OR-across-requested-tags.

## [1.19.5] - 2026-07-20

### Fixed

- **`list_queue_items` was failing on every call** with a UiPath 400 (`Could not find
  a property named 'Name' on type ... QueueItemDto`). Combining an outer `$select`
  with the bare `$expand=QueueDefinition,Robot` this endpoint requires triggered the
  error; `getQueueItem` already avoided `$select` for the same reason. Dropped
  `$select` from `listQueueItems` — `toQueueItem` only reads the fields it needs, so
  fetching the full object costs nothing downstream.

## [1.19.4] - 2026-07-20

### Changed

- **`copilot/settings.ts` split into a `copilot/settings/` directory**, one file per catalog
  section (`sections/`) implementing a unified `SettingsSection` interface, assembled by
  `catalog.ts`. Write/sync behavior is now pluggable via a `SectionSyncer` interface
  (`types.ts`) that a section attaches via an optional `sync` field, replacing the previous
  hardcoded section-key arrays in the plan/apply dispatch — `specialities.ts` and `orders.ts`
  are the two current syncer implementations, each owning its own crawl + plan logic and
  sharing nothing but the interface. Internal only: no tool behavior, schema, or output shape
  changed.

## [1.19.3] - 2026-07-20

### Added

- **`plan_settings_sync` now audits per-facility payer-link drift** (#4, partial).
  The additive create/merge sync never inspects a facility/provider that already
  exists (matched by name) in both envs, so payer-link drift on it was invisible.
  A new `payerLinkFindings` array reports, per shared facility/provider: payer
  names linked only in pre-prod, only in prod, or a `payerUid` that resolves to
  no payer at all in that env (a dangling reference). Matched by payer NAME,
  never by payerUid — payer uids are per-env and never expected to match.
  Audit-only: nothing here writes anything, since reconciling an existing
  facility's links would mean modifying (not adding to) pre-prod settings,
  outside sync's additive-only contract. Does not yet disambiguate multiple
  facilities sharing the same name within one speciality — a pre-existing gap
  in the name-matching `planSpecialitySync` already uses.

## [1.19.2] - 2026-07-20

### Fixed

- **`plan_settings_sync`/`apply_settings_sync`: exact-string facility/provider name
  matching missed same-NPI duplicates, causing merge actions to 400** (#3, partial).
  A prod-only facility/provider is now checked against a tenant-wide index of every
  NPI already present in pre-prod (any order type, any speciality) before being added
  to a merge body — a same-NPI record under a different name/casing or a different
  speciality is dropped with a warning instead of silently duplicated (verified live:
  `Medical Supplies -> DME`'s `Verio Healthcare Inc - DME` / `VERIO HEALTHCARE INC -
  DME` collision no longer 400s). This does not yet cover records where pre-prod's own
  row carries no NPI at all — that needs fuzzy name matching and is being scoped
  separately given the risk of conflating two distinct providers.

## [1.19.1] - 2026-07-20

### Fixed

- **`apply_settings_sync`: every planned specialities create/merge action 400'd** (#2).
  The write bodies carried two GET-only fields the BE's write schema rejects —
  `source` (confirmed live: `POST .../specialities` 400'd with `"referredFacilities[0].
  source" is not allowed` until stripped) and `specialityName` (each facility/provider
  row's echo of its parent speciality's name, per a captured HAR of the Settings UI's
  edit-specialty PUT). Both are now stripped from create bodies and from the
  existing-item side of a merge (which previously wasn't cleaned at all).

## [1.19.0] - 2026-07-19

### Removed

- **`clone_order` tool** — it bundled prod-order extraction, best-effort facility
  remapping, and minting into one opaque call with no way to inspect or
  intervene when a cross-env reference didn't resolve. Replaced by the
  `clone-and-verify-order` prompt (see below). The config's per-prodUid
  `overrides` feature (and the legacy split `overrides.json` file) is removed
  along with it — nothing consumed it once the automated remap layer was gone.

### Added

- **`submit_preprod_order` tool** — submitting a pre-prod order sitting at
  forReview is now its own explicit, separate write (only `clone_order` could
  submit before). Always requires explicit user authorization.
- **`create_preprod_order` now returns `processMessage`** — the last
  `/process` response's message, so an order that doesn't reach forReview
  explains why (e.g. a missing reference) instead of just showing a bare status.

### Changed

- **`clone-and-verify-order` prompt rewritten** to chain `find_clone_candidates`
  → `get_order` → `create_preprod_order`, letting the agent resolve prod → pre-prod
  references itself via `get_settings`/`diff_settings` rather than an automated
  NPI/name matcher. On a failed or stuck mint, it diagnoses (via
  `list_setting_sections`/`diff_settings`/`plan_settings_sync`) and reports a
  proposed fix — it never applies one automatically.

## [1.18.0] - 2026-07-16

### Added

- **Optional OAuth (client-credentials) auth for UiPath Orchestrator**, alongside
  the existing static bearer/PAT. Configure `uipath.oauth` (`clientId` +
  `clientSecret`, plus optional `tokenUrl`/`scope`) to have the server fetch and
  cache an access token instead of using a long-lived PAT. `uipath.bearer` is now
  optional — at least one of `bearer`/`oauth` is required — and doubles as the
  runtime fallback if OAuth is unconfigured or its token request fails. `tokenUrl`
  is guessed from `orchestratorUrl` when omitted. `doctor` now reports which auth
  mode (`oauth`/`bearer`) is active per env.

## [1.17.0] - 2026-07-15

### Added

- **`get_job_logs` and `get_job` can now batch by `jobKeys` (up to 25)** — pass an
  array instead of a single `jobKey` to diagnose many jobs (e.g. every job a
  `list_jobs` scan just returned) in one MCP call instead of looping one call per
  job. RobotLogs has no server-side way to combine multiple `JobKey`s (`in`/`or`
  silently return wrong/empty results rather than erroring — confirmed live), so a
  batch still issues one HTTP call per job under bounded concurrency; what this
  collapses is MCP round-trips, not HTTP calls. Single-`jobKey` calls keep their
  exact existing response shape.
- **`list_jobs`/`get_job` now include `robotName`/`processVersion`**, and
  `list_queue_items`/`pull_queue_item` now include `robotName` and a corrected,
  always-current queue `name` — all via OData `$expand` folded into the existing
  query (same HTTP call count, richer response). The queue item's own `Name` field
  can be empty/stale after a queue rename; the expanded `QueueDefinition.Name` is
  authoritative.

### Changed

- **`list_triggers` now sends an explicit `$select`** instead of fetching every
  `ProcessSchedules` field (cron internals, `MachineRobots`, `Tags`, …), shrinking
  the response for the same single call.



### Added

- **Live config reload** — the config file is now re-read automatically when it
  changes on disk (checked cheaply via file mtime on every call) instead of being
  cached in memory forever after the first successful load, so editing credentials,
  profiles, or overrides takes effect on the next tool call with no server restart.
  The server sends an MCP `notifications/message` each time a reload happens. An
  edit that fails validation fails closed (throws, matching a cold-start error)
  rather than silently continuing to serve stale credentials.

### Changed

- **Default config filename is now `copilot-mcp.config.json`** (used when
  `COPILOT_MCP_CONFIG` isn't set), so it's obviously this server's config rather than
  a generic `config.local.json`. The old `config.local.json` name still works as a
  fallback (with a one-time warning) so existing setups aren't broken.

## [1.15.0] - 2026-07-12

### Changed

- **`get_settings` curates by default** — `normalized` now defaults to `true` (the same
  UID/timestamp/per-section noise-stripping `diff_settings` already applies). Pass
  `normalized: false` for the raw payload with real UIDs/timestamps visible.
- **`get_job_logs` truncates long messages by default** — each `Message` is capped at 400
  chars (reusing the log digest's cap) unless `fullMessages: true` is passed for the
  untruncated text (e.g. a complete stack trace).

### Fixed

- **`build_queue_item` no longer leaks a live Orchestrator bearer token.** Its `curl` field
  re-serialized the full ~40-field payload a second time *and* embedded the live
  `uipath.bearer` token in plaintext inside the tool result — i.e. into the model's
  context/conversation transcript. `curl` is removed; the response now carries
  `meta.postUrl`/`meta.folderPath` plus a note on submitting the payload with your own token.

## [1.14.0] - 2026-07-09

### Added

- **Condensed log digest for failure diagnosis** (`src/uipath/log-digest.ts`, pure) — instead
  of returning up to 500 raw robot-log lines per job, the diagnostic tools now return a
  digest: per-level counts, failure-only lines with consecutive near-duplicates collapsed
  into `{message, count, firstAt, lastAt}` (retry loops), the top inter-log stall gaps, and a
  `droppedFailures` count so truncation is never silent.
  - **`analyze_order_execution`** — each matched job now carries `durationMs`,
    `gapSincePreviousJobMs` (retry cadence), a structured `fault` (headline error, stable
    `normalizeError` signature — the same dedupe key as `build_faulted_job_issue` — and parsed
    exception type) for non-SUCCESS jobs, and `logDigest` in place of the raw `logs` array.
  - **`get_job`** gains `includeLogDigest` — returns the same structured `fault` + digest so a
    job found by Key alone (no orderUid) can be diagnosed without the heavy order-correlated
    path; `durationMs` is always returned.
  - **`list_jobs`** returns `endTime` + `durationMs` per job.
- The digest is deliberately concise; the tool descriptions and a `note` inside every digest
  direct the agent to **`get_job_logs`** (with `minLevel`/`contains`/`onlyFailures`/`tail`)
  when the full raw logs are needed.

## [1.13.0] - 2026-07-09

### Added

- **`build_mcp_issue` tool + `send-mcp-feedback` prompt** — turn a user's report about this
  MCP server itself (a bug or general feedback: idea, friction, docs gap) into a GitHub
  issue payload (`repo`/`title`/`body`/`labels` + a prefilled new-issue `url`). BUILD ONLY —
  posting stays with the host's GitHub tooling, or the user opens the prefilled URL.
  Independent of `feedback.enabled`, which only gates the automatic nudge on failures.
- **Config self-setup guide** — new `copilot://reference/config-guide` resource (config
  template with placeholders, field docs, verification steps), and the server's initialize
  instructions now probe the config at startup: an unconfigured server announces the
  self-setup path (read guide → write config → verify with `doctor`) instead of letting
  every tool fail cryptically. The config is re-read on the next call — no restart needed.
- **`get_job_logs` filters** — all opt-in; the default call still returns the full
  (capped 500) log set, oldest first:
  - `minLevel` (`warn` | `error`) and `contains` (substring on Message) are pushed into the
    OData `$filter`, so the 500-row window is spent on matching rows.
  - `onlyFailures` — semantic failure filter: keeps logs at error/fatal level OR whose
    message mentions failure indicators (exception, failed, timeout, "unable to", denied, …),
    catching failures worded at Info level.
  - `tail: N` — last N matching logs; fetches newest-first so it reaches the end of a
    >500-row job, still returned oldest first.
  - Results now include `returned`, `totalMatching`, and `truncated`, so a capped response
    is never mistaken for the whole log.

## [1.4.0] - 2026-06-23

### Added

- **`get_order` tool** — read-only; fetch a single order's normalized detail (status,
  submission status, patient, insurance + memberId, order type, facility, place of service,
  requiredAuthorization, appointment date, ICD/CPT codes, note presence) by uid in a chosen env.
- **`doctor` tool** — read-only; probe the server's connections to its external APIs (Copilot
  BE login for prod + pre-prod, one cheap authenticated UiPath Orchestrator call per env/folder)
  and report what is reachable. For setup/onboarding debugging.
- **`sync_settings` tool (stub)** — registered placeholder for the write-side of
  `diff_settings` (push selected sections prod → pre-prod, pre-prod only, dry-run first). Not
  implemented yet; calling it returns a not-implemented error.

## [1.3.1] - 2026-06-23

### Fixed

- Publishing no longer aborts on the `prepare` lifecycle script. `bun publish` runs
  `prepare` without `node_modules/.bin` on `PATH`, so `lefthook install` exited 127 and
  failed the Publish workflow (the package never reached GitHub Packages). `prepare` is now
  `lefthook install || true`, so a missing `lefthook` no-ops during publish while local dev
  installs still wire up the git hook.

## [1.3.0] - 2026-06-23

### Added

- **`diff_settings` tool** — log into an account's prod and pre-prod tenants and return a
  normalized diff of its settings across 18 sections (file manager, outbound order
  settings/types, locations/groups/regions/fax info, clinic payers, document
  routing/reviewing rules, auto-finish rules, eligibility visibility, rendering providers,
  and — crawled from each outbound order type's specialities — specialties + referred
  providers/facilities). Optional `emr` adds the account's `emrDetailsSettings`.
  - Env-specific noise (UIDs, timestamps, dummy emails, CDN hosts) is stripped, and list
    sections are matched on a semantic key (e.g. `name`) rather than UID, so only real
    drift surfaces. Rules without a stable cross-env key are compared as content sets.
  - Scope a run with `groups` (top-level, e.g. `['orders']`) and/or `sections` (exact keys);
    the two combine as AND. Unchanged sections are omitted unless `includeUnchanged: true`.
- **`list_setting_sections` tool** — read-only, no-network companion that lists the section
  keys, labels, top-level groups, kind, and `derived` flag, so callers can discover what to
  pass to `diff_settings`' `groups`/`sections` params. Optional `group`/`emr` filters.

### Changed

- Every tool now declares MCP annotation hints (`readOnlyHint`, `destructiveHint`,
  `idempotentHint`, `openWorldHint`) so hosts can surface whether a tool mutates state.
  All tools are read-only except `clone_order` and `delete_preprod_order`;
  `list_setting_sections` is the only tool that touches no external service.

## [1.2.0]

Baseline: order clone (prod → pre-prod), clone-candidate/stuck-order scans, UiPath
queue-item build, queue browse/pull, Orchestrator job listing/logs/execution analysis,
and bundled reference resources.

[1.4.0]: https://github.com/belal-elsabbagh-apex/copilot-mcp/compare/v1.3.1...v1.4.0
[1.3.1]: https://github.com/belal-elsabbagh-apex/copilot-mcp/compare/v1.3.0...v1.3.1
[1.3.0]: https://github.com/belal-elsabbagh-apex/copilot-mcp/compare/v1.2.0...v1.3.0
[1.2.0]: https://github.com/belal-elsabbagh-apex/copilot-mcp/releases/tag/v1.2.0

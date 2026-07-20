# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

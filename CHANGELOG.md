# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

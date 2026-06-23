# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

[1.3.0]: https://github.com/belal-elsabbagh-apex/copilot-mcp/compare/v1.2.0...v1.3.0
[1.2.0]: https://github.com/belal-elsabbagh-apex/copilot-mcp/releases/tag/v1.2.0

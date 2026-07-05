# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

An MCP server (`@modelcontextprotocol/sdk`, `McpServer` over **stdio**) exposing EHR Copilot
order operations + UiPath Orchestrator execution analysis. Built with Bun; `bun build` bundles
`src/` into a node-runnable `dist/server.js` that runs under `npx` or `bunx`. Published private
on GitHub Packages.

## Commands

```bash
bun test                       # full suite (bun's runner, src/**/*.test.ts)
bun test src/copilot/settings.test.ts  # one file
bun test -t "diffList"         # tests matching a name
bun run typecheck              # tsc --noEmit (strict; exactOptionalPropertyTypes, verbatimModuleSyntax)
bun run lint                   # biome check src   (bunx biome check --write src to autofix)
bun run build                  # bundle -> dist/server.js (what npx/bunx execute)
bun run start                  # run from source, no build
COPILOT_MCP_CONFIG=… bun run inspect   # MCP Inspector UI to list/call tools without a host
```

Before finishing a change: `bun run typecheck`, `bun test`, and `bunx biome check src` should all pass.

## Architecture

`src/` is organized by domain. Tests live beside the module they cover (`*.test.ts`):

- `src/server.ts` — entry point + tool/resource wiring (stays at the root).
- `src/config/` — `config.ts` (validated config loading).
- `src/copilot/` — Copilot BE domain: `copilot-client.ts`, `mirror.ts`, `sweep.ts`, `analyze.ts`,
  `output-analysis.ts`, `output-schema.ts`, `settings.ts`, `doctor.ts`.
- `src/uipath/` — UiPath Orchestrator domain: `uipath.ts`, `queue.ts`, `queue-item.ts`, `faults.ts`.
- `src/mcp/` — MCP-protocol concerns: `prompts.ts`, `notify.ts`, `feedback.ts`, `reference.ts`.
- `src/shared/` — cross-cutting helpers: `util.ts`.

- **`src/server.ts`** is a thin wiring layer only: it constructs the `McpServer`, then registers
  every tool (`server.registerTool`), the static resources + the `copilot://reference/portal/{name}`
  resource template, the prompts (`registerPrompts`), and logging (`registerLogging`). Tool *logic*
  lives in per-feature modules — keep server.ts declarative and push real work into a module.
- **Hybrid static/live model**: stable facts (portal registry, UiPath folder ids, schemas, safety
  rules in `mcp/reference.ts`) are exposed as **resources**; anything that changes per-run (orders,
  queue items, jobs, settings) is a **tool**. Don't turn live data into a resource.
- **Two external systems**: Copilot BE via `copilot/copilot-client.ts` (`makeClient`/`login`/`req`),
  and UiPath Orchestrator via `uipath/uipath.ts`. Each Copilot tenant exists per-env (`prod` / `pre_prod`).
- **`config/config.ts`** loads one validated config (single-file `COPILOT_MCP_CONFIG`, or split legacy
  files via `COPILOT_MCP_LOCAL_DIR`). `resolveCreds(profile)` returns `{ prod, pre_prod }` creds.
  Profiles are dynamic — read them with `listProfiles()`; never hardcode profile names.
- **`copilot/settings.ts`** (diff_settings / sync_settings): the hard part is cross-env noise. It strips
  env-specific fields (`stripNoise`: UIDs, timestamps, dummy emails, CDN hosts) and matches list
  items by a **semantic key** (`matchKey`, usually `name`), never by UID. The pure diff functions
  are unit-tested; keep them pure.
- **`mcp/notify.ts`**: `mcpLog()` sends MCP `notifications/message`; `reportProgress()` sends
  `notifications/progress` only when the caller passed a progressToken. Both are no-throw side-channels.
- **`uipath/faults.ts`** (`build_faulted_job_issue` + the `report-faulted-uipath-jobs` prompt):
  turns a faulted UiPath job into a ready-to-post GitHub issue payload but **posts nothing** — this
  server holds no GitHub credentials. The prompt drives a **GitHub MCP server connected in the host**
  to do the search/create/comment. Dedupe is by **fault signature** (`normalizeError` strips
  GUIDs/ids/timestamps so recurrences group into one issue, not by per-run job Key). The pure
  `formatFaultedJobIssue`/`normalizeError` are unit-tested; keep them pure.

## Invariants (do not break)

- **Every Copilot operation requires an `env` (`prod` | `pre_prod`) and a `profile`
  (`ossm`, `kafri`, …) plus its other args.** These are **required** tool/prompt parameters — never
  add a default env or default profile, and never silently assume one. `profile` is validated
  against the config at call time by `resolveCreds`. UiPath-only tools (`list_jobs`, `get_job_logs`,
  `build_faulted_job_issue`) take `env` but **no `profile`** — UiPath authenticates with the single
  global `uipath.bearer`, not per-profile creds. Still never default `env`.
- **stdout is the JSON-RPC channel.** A stray `console.log` to stdout corrupts the protocol.
  `console.*` is redirected to stderr in server.ts; use `mcpLog()` for client-visible logs and
  stderr (`LOG_LEVEL=debug` / `COPILOT_MCP_DEBUG=1`) for local diagnostics.
- **prod/pre-prod isolation**: writes target pre-prod only; `delete_preprod_order` never touches
  prod; `clone_order` is clone-only unless `submit` is explicitly authorized; `build_queue_item` /
  `pull_queue_item` force `IsApproved=false` so a test run can never submit a real auth.
- **`sync_settings` is additive, pre-prod-only, dry-run by default.** It copies prod-only settings into
  pre-prod and never overwrites/deletes (the `changed`/`onlyInPreProd` sets are left untouched). It
  covers two outbound order-type domains:
  - **specialties** (sections `specialties` / `referred-providers` / `referred-facilities`): create
    prod-only specialties (`POST .../types/{uid}/specialities`) and merge prod-only facilities/providers
    into existing ones (`PUT .../specialities/{uid}`, full-replace body = existing + additions).
  - **orders** (section `orders`, the UI's "Orders" section): create prod-only orders under matching
    order types (`POST .../types/{uid}/names`). Create-only — orders present in both envs are left
    untouched (no verified name-update endpoint).
  All env-specific references are **remapped** prod→pre by **name** (never stripped, never copied raw):
  `payersProviderId[].payerUid` / the CPT `payers` map for orders, plus orders' `facilitiesUids` and
  `authSubCategoryUids` / `referralSubCategoryUids`. Anything with no pre-prod match is **dropped with a
  warning** rather than linked to the wrong entity. Sections with no verified write endpoint are reported
  under `skippedSections` — add new domains via a `SectionSyncer`, only with a verified endpoint.
- Tools return via the `ok()` / `err()` helpers and declare all four annotation hints
  (readOnly/destructive/idempotent/openWorld); `server.test.ts` enforces the wiring.

## Releasing

Tag-driven: bump `version` in `package.json` (and the `McpServer` version string in `server.ts`),
commit, push a matching `vX.Y.Z` tag → `release.yml` + `publish.yml` fan out from the tag.

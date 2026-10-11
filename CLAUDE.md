# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

An MCP server (`@modelcontextprotocol/sdk`, `McpServer` over **stdio**) exposing EHR Copilot
order operations + orchestrator (UiPath and Apex) execution analysis. Built with Bun; `bun build` bundles
`src/` into a node-runnable `dist/server.js` that runs under `npx` or `bunx`. Published
on GitHub Packages.

## Commands

```bash
bun test                       # full suite (bun's runner, src/**/*.test.ts)
bun test src/copilot/settings          # one module (a directory)
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
- `src/config/` — `config.ts` (validated config loading; `resolveApex()` for the optional `apex` block).
- `src/copilot/` — Copilot BE domain: `copilot-client.ts`, `session.ts`, `mirror.ts`, `sweep.ts`,
  `analyze.ts`, `output-analysis.ts`, `output-schema.ts`, `settings/`, `doctor.ts`.
- `src/orchestrator/` — backend-neutral orchestrator contract + cross-backend tools (see below).
- `src/uipath/` — UiPath backend: `uipath.ts`, `auth.ts`, `queue.ts`, `map.ts`, `payload.ts`, `backend.ts`, `faults.ts`, `actions.ts`.
- `src/apex/` — Apex backend (see below).
- `src/mcp/` — MCP-protocol concerns: `prompts.ts`, `notify.ts`, `feedback.ts`, `reference.ts`.
- `src/shared/` — cross-cutting helpers: `util.ts`, `http.ts` (`sleep`, `rateLimitDelayMs`), `expected-error.ts`.

- **`src/server.ts`** is a thin wiring layer only: it constructs the `McpServer`, then registers
  every tool (`server.registerTool`), the static resources + the `copilot://reference/portal/{name}`
  resource template, the prompts (`registerPrompts`), and logging (`registerLogging`). Tool *logic*
  lives in per-feature modules — keep server.ts declarative and push real work into a module.
- **Hybrid static/live model**: stable facts (portal registry, orchestrator folders, schemas, safety
  rules in `mcp/reference.ts`) are exposed as **resources**; anything that changes per-run (orders,
  queue items, jobs, settings) is a **tool**. Don't turn live data into a resource.
- **Three external systems**: Copilot BE via `copilot/copilot-client.ts` (`makeClient`/`login`/`req`),
  UiPath Orchestrator via `uipath/uipath.ts`, and the Apex orchestrator via `apex/client.ts`. Each Copilot tenant
  exists per-env (`prod` / `pre_prod`); both orchestrators sit behind `orchestrator/backend.ts`.
- **`config/config.ts`** loads one validated config (single-file `COPILOT_MCP_CONFIG`, or split legacy
  files via `COPILOT_MCP_LOCAL_DIR`). `resolveAuth(profile, env)` returns an `EnvAuth` tagged
  `mode: "support"` (a `clinicUid` pointer — creds come from `resolveSupport(env)`, i.e.
  `copilot.support.<env>`) or `mode: "direct"` (the profile entry's own `be`/`email`/`password`).
  Profiles are dynamic — read them with `listProfiles()`; never hardcode profile names.
- **`copilot/session.ts`** is the single auth chokepoint for the Copilot BE — nothing else may POST
  `physician/login` or `support/switch-clinic`. `connect(env, profile)` resolves the profile's
  `EnvAuth` via `config.ts` and either logs in directly or logs in the support account then
  switch-clinics, returning a `Session` (token, mode, clinicUid, activeClinicId). Sessions are
  cached by `(env, be, email, scope)` in memory and, by default, on disk (`~/.cache/copilot-mcp/sessions.json`,
  mode `0600`, overridable via `COPILOT_MCP_CACHE`; `copilot.sessionCache: false` disables disk),
  single-flighted so concurrent tool calls never double-login, and TTL-aware from the JWT's own
  expiry claim. `listClinics(env)` (backs the `list_clinics` tool) is the only other caller of the
  support-account login path.
- **`copilot/settings/`** (diff_settings / get_settings / plan_settings_sync / apply_settings_sync):
  one file per catalog section under `sections/` implementing the `SettingsSection` interface
  (`types.ts`), assembled into `SETTINGS_CATALOG` by `catalog.ts`. The hard part is cross-env
  noise. `diff-engine.ts` strips env-specific fields (`stripNoise`: UIDs, timestamps, dummy
  emails, CDN hosts) and matches list items by a **semantic key** (`matchKey`, usually `name`),
  never by UID. The pure diff/plan/id functions are unit-tested; keep them pure. Write/sync
  behavior is pluggable: a section optionally carries a `sync: SectionSyncer` (see the
  Invariants entry below) — `specialities.ts` and `orders.ts` are the two current
  implementations, each owning its own crawl + plan logic. `index.ts` barrels the module's
  public surface for `server.ts` and other consumers.
- **`mcp/notify.ts`**: `mcpLog()` sends MCP `notifications/message`; `reportProgress()` sends
  `notifications/progress` only when the caller passed a progressToken. Both are no-throw side-channels.
- **`orchestrator/`** is the backend-neutral contract: `types.ts` (`Orchestrator`, `JobCore`, `QueueItemCore`,
  `QueueCore`, `ProcessCore`, `TriggerCore`; backend-specific fields live under each core's `details`),
  `backend.ts` (`OrchestratorBackend`, the one interface both backends implement), `registry.ts`
  (`BACKENDS` / `backendFor(orchestrator)`), plus the moved generic code (`log-digest.ts`, `log-semantics.ts`,
  `safety.ts`, `faults.ts`, `queue-item.ts` = the canonical native queue-item content builder, `find-order.ts` =
  cross-backend `find_order_queue_items`). `server.ts` dispatches every orchestrator tool through
  `backendFor(orchestrator)` and wraps the result as `{ orchestrator, env, …result }`. **Retiring UiPath = delete
  `src/uipath/`, its `BACKENDS` entry and the `"uipath"` enum value** — no tool renames.
- **`src/apex/`** is the Apex backend: `client.ts` (the chokepoint, see Invariants), `errors.ts`
  (`ApexApiError`, `ApexNotConfiguredError`), `api.ts` (typed endpoints + raw row types), `map.ts` (pure Apex →
  core mappers, dashboard label maps, terminal set), `job-log.ts` (pure `job.log` parser), `backend.ts`
  (`apexBackend`), `artifacts.ts` (`get_job_artifacts`), `faults.ts`, `payload.ts`, and `actions.ts` (writes). The
  state vocabulary on both backends is UiPath's labels (Apex statuses are mapped; `rawState` keeps the original).
  A transaction maps to many jobs on Apex (`jobs[]`, newest first); verdicts use the latest attempt. Config:
  `apex` block (`apiUrl` = LB host, never CloudFront; `username`/`password`; optional `folderByEnv`;
  `serverUrlByEnv`/`queueUrl` = write-guard limits), each of `apiUrl`/`username`/`password` falling back to
  `APEX_API_URL`/`APEX_USER_NAME`/`APEX_PASSWORD` when the file omits it (`resolveApex()`).
- **`orchestrator/faults.ts` + `uipath/faults.ts` + `apex/faults.ts`** (`build_faulted_job_issue` + the
  `report-faulted-jobs` prompt): turn a faulted job into a ready-to-post GitHub issue payload but **post nothing** —
  this server holds no GitHub credentials. The prompt drives a **GitHub MCP server connected in the host** to do the
  search/create/comment. Dedupe is by **fault signature** (`normalizeError` strips GUIDs/ids/timestamps so
  recurrences group into one issue, not by per-run job id). UiPath signatures are byte-identical to the pre-2.0
  ones; Apex signatures are `apex:`-prefixed so the two never collide. The pure `formatFaultedJobIssue` /
  `formatApexFaultIssue` / `normalizeError` are unit-tested; keep them pure.

## Invariants (do not break)

- **Every Copilot operation requires an `env` (`prod` | `pre_prod`) and a `profile`
  (`ossm`, `kafri`, …) plus its other args.** These are **required** tool/prompt parameters — never
  add a default env or default profile, and never silently assume one. `profile` is validated
  against the config at call time by `resolveAuth`/`resolveSupport`. The one carve-out is
  `list_clinics` — it takes `env` but no `profile`, because it's the discovery tool that produces
  the profile → clinicUid mapping (requiring a profile would be circular). Orchestrator tools
  (`list_jobs`, `get_job`, `get_job_logs`, `get_job_artifacts`, `build_faulted_job_issue`, `list_queues`,
  `list_queue_items`, `find_order_queue_items`, `pull_queue_item`, `list_processes`, `list_triggers`,
  `add_queue_item`, `delete_queue_item`, `start_job`, `stop_job`) also take `env` but **no `profile`** —
  orchestrators authenticate globally (not per-profile creds). UiPath: via `uipath.oauth`
  (client-credentials, tried first when configured) and/or the static `uipath.bearer` PAT (the fallback
  if `oauth` is absent or its token request fails — `uipath/auth.ts`'s `resolveBearerToken`, the single
  chokepoint `uipathRequest` calls; at least one of `bearer`/`oauth` is required by the config schema).
  Apex: one login (`apex.username`/`apex.password`, each falling back to `APEX_USER_NAME`/`APEX_PASSWORD`)
  through `apex/client.ts`. Still never default `env`.
- **Copilot BE auth is one chokepoint, two modes, and a shared session cache.**
  `copilot/session.ts`'s `connect(env, profile)` is the ONLY place allowed to POST
  `physician/login` or `support/switch-clinic` — do not add a second call site. A profile's env
  entry is either `mode: "support"` (a `clinicUid` pointer; creds come from
  `copilot.support.<env>`, shared across every profile using that mode) or `mode: "direct"` (its
  own `be`/`email`/`password`, unchanged pre-existing behavior); `clinicUid` wins if an entry
  somehow carries both. Sessions are cached by `(env, be, email, scope)`, single-flighted so
  concurrent tool calls never double-login, and TTL-aware from the JWT's own expiry claim — this
  is what keeps a support account's login-rate budget from being burned by every restart. Disk
  persistence (`~/.cache/copilot-mcp/sessions.json`, mode `0600`) is on by default
  (`copilot.sessionCache: false` disables it; `COPILOT_MCP_CACHE` overrides the path) and never
  holds the account email (cache keys are hashed).
- **stdout is the JSON-RPC channel.** A stray `console.log` to stdout corrupts the protocol.
  `console.*` is redirected to stderr in server.ts; use `mcpLog()` for client-visible logs and
  stderr (`LOG_LEVEL=debug` / `COPILOT_MCP_DEBUG=1`) for local diagnostics.
- **prod/pre-prod isolation**: writes target pre-prod only; `delete_preprod_order` never touches
  prod; `create_preprod_order` never submits (only `submit_preprod_order` does, and only with
  explicit user authorization); `build_queue_item` / `pull_queue_item` force `IsApproved=false` so
  a test run can never submit a real auth. BE clients are env-tagged (`makeClient(base, env)`) and
  every BE write engine (`mintPreprodOrder`, `submitOrder`, the `delete_preprod_order` handler,
  `applySettingsSync`) calls `assertPreProdClient`, which refuses prod-tagged AND untagged clients
  (fails closed) — tag any new client at construction and assert before any new write path.
- **Order minting is one engine, one producer.** `copilot/mirror.ts`'s `mintPreprodOrder(pre,
  spec)` owns the sequence invariants (order names reset speciality/provider; placeOfService LAST,
  post-forReview; E6001-tolerant `/process` retry) and **always stops at forReview** — it never
  submits. `create_preprod_order` is its only caller, minting from an explicit `MintSpec`; there is
  no dedicated "clone" tool — the `clone-and-verify-order` prompt reads a prod order via
  `get_order` and derives the spec fields itself (no automated cross-env reference matching exists;
  an agent resolves prod names to pre-prod uids via `get_settings`/`diff_settings`, and the
  settings tools diagnose a reference that doesn't resolve). Submitting is a fully separate step:
  `submitOrder` (in `copilot-client.ts`) backs the `submit_preprod_order` tool, the only thing that
  may submit, always explicit. Do NOT expose the raw atoms (create draft / PUT field / process) as
  tools — the tool boundary is the safe transaction, not the HTTP verb.
- **UiPath writes are dev-clone-only and guarded.** `add_queue_item`, `delete_queue_item`,
  `start_job`, and `stop_job` (UiPath half in `uipath/actions.ts` — the ONLY module that mutates
  UiPath Orchestrator state) take `env: z.literal("pre_prod")` at the schema layer AND assert
  `env === "pre_prod"` in the domain function. Every posted SpecificContent passes
  `guardQueueItemSafety` (`orchestrator/safety.ts`, pure, unit-tested — the single enforcement point
  for BOTH orchestrators, also used by `build_queue_item`/`pull_queue_item`): IsApproved forced false; non-empty
  `serverURL`/`queueUrl` must match the configured pre-prod values (fails closed when
  unconfigured); `<TO-FILL>` placeholders rejected. `delete_queue_item` is fetch-first and
  refuses any item whose Status isn't `New`; `stop_job` is likewise fetch-first (resolving the
  caller's GUID Key to Orchestrator's numeric Id scoped to the pre-prod folder) and refuses a job
  whose State is already `Successful`/`Faulted`/`Stopped`. The MCP never repins releases and
  never creates queues — `list_processes`/`list_queues`/`list_triggers` are the read-side
  discovery/verification tools (dev-clone queue ids differ from the prod ids in `PORTALS`).
- **Apex has one HTTP chokepoint and always scopes to a folder.** All Apex HTTP lives in
  `apex/client.ts`. `apexRequest` is the only entry point to Apex's data and write routes: it always
  sends `X-Folder-Id` (omitting it silently reads the user's default folder), sends only verified
  query params (unknown ones are silently ignored by the server), and refuses any request other than
  GET unless `env === "pre_prod"` — before any network call. The only other requests, all in
  `client.ts`, are the login POST, the `GET /folders` listing that resolves folder ids, and `apexDownload` (GET-only, Bearer, same-origin `/api/v1/` paths,
  no folder header). Writes are never auto-retried (a retry could double-post).
- **Apex writes are test-folder-only and guarded.** Mirrors the UiPath invariant: schema
  `z.literal("pre_prod")`, domain assert, the chokepoint gate, `guardQueueItemSafety`, and fetch-first
  refusal (`delete_queue_item` only on `new`; `stop_job` refuses every terminal status including `lost`/`timed_out`),
  plus a client-side folder proof: `stop_job` refuses a job whose `folder_id` is not the pre_prod folder's,
  and `delete_queue_item` refuses a `queueId` missing from the pre_prod folder's queue listing (by-id
  routes are not verified to be folder-scoped server-side).
  `apex/actions.ts` is the ONLY module that mutates Apex. Prod and test folders share one deployment and one login, so
  the folder is the only boundary — a bug that drops the gate could write to prod.
- **`orchestrator` is required on job- and queue-level tools, never defaulted.** The carve-outs are
  `find_order_queue_items` and `find_stuck_orders` (omitted = every registered backend) and the Apex-only
  `get_job_artifacts` (no `orchestrator` arg). **The MCP never deploys, pins, creates Processes or queues, or touches
  Apex credentials, assets or Action Center.** Artifacts download only under `~/.cache/copilot-mcp/artifacts/`
  (mode `0700`), never inside a repo.
- **Settings sync is a plan/apply pair — additive, pre-prod-only.** `plan_settings_sync` is
  READ-ONLY and returns the planned actions, each with a stable id (`section:op:typeName:itemName`).
  `apply_settings_sync` never accepts request bodies — it **re-plans server-side** and executes only
  the actions selected by `actionIds` or an explicit `all:true` (**exactly one required**; there is
  deliberately no default "apply everything"; a stale id after state drift lands in `unmatchedIds`
  instead of executing). It copies prod-only settings into pre-prod and never overwrites/deletes
  (the `changed`/`onlyInPreProd` sets are left untouched). It covers two outbound order-type domains:
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
  under `skippedSections` automatically (a section's `sync` field is simply omitted) — add a new
  syncable domain by writing a `SectionSyncer` (`copilot/settings/types.ts`) and attaching it to the
  relevant section(s)' `sync` field, only once its write endpoint is verified; `sync.ts`'s dispatch
  needs no changes.
- Tools return via the `ok()` / `err()` helpers and declare all four annotation hints
  (readOnly/destructive/idempotent/openWorld); `server.test.ts` enforces the wiring.

## Releasing

Tag-driven: bump `version` in `package.json` (and the `McpServer` version string in `server.ts`),
add the release's `CHANGELOG.md` entry (Keep a Changelog format), commit, push a matching
`vX.Y.Z` tag → `release.yml` + `publish.yml` fan out from the tag.

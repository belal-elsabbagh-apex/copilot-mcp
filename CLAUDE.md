# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

An MCP server (`@modelcontextprotocol/sdk`, `McpServer` over **stdio**) exposing EHR Copilot
order operations + UiPath Orchestrator execution analysis. Built with Bun; `bun build` bundles
`src/` into a node-runnable `dist/server.js` that runs under `npx` or `bunx`. Published
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
- **`copilot/settings.ts`** (diff_settings / get_settings / plan_settings_sync / apply_settings_sync):
  the hard part is cross-env noise. It strips env-specific fields (`stripNoise`: UIDs, timestamps,
  dummy emails, CDN hosts) and matches list items by a **semantic key** (`matchKey`, usually `name`),
  never by UID. The pure diff/plan/id functions are unit-tested; keep them pure.
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
  `get_job`, `list_queues`, `list_processes`, `list_triggers`, `build_faulted_job_issue`,
  `add_queue_item`, `delete_queue_item`, `start_job`) take `env` but **no `profile`** — UiPath
  authenticates with the single global `uipath.bearer`, not per-profile creds. Still never default `env`.
- **stdout is the JSON-RPC channel.** A stray `console.log` to stdout corrupts the protocol.
  `console.*` is redirected to stderr in server.ts; use `mcpLog()` for client-visible logs and
  stderr (`LOG_LEVEL=debug` / `COPILOT_MCP_DEBUG=1`) for local diagnostics.
- **prod/pre-prod isolation**: writes target pre-prod only; `delete_preprod_order` never touches
  prod; `clone_order` is clone-only unless `submit` is explicitly authorized; `build_queue_item` /
  `pull_queue_item` force `IsApproved=false` so a test run can never submit a real auth.
  BE clients are env-tagged (`makeClient(base, env)`) and every BE write engine
  (`mintPreprodOrder`, the `delete_preprod_order` handler, `applySettingsSync`) calls
  `assertPreProdClient`, which refuses prod-tagged AND untagged clients (fails closed) —
  tag any new client at construction and assert before any new write path.
- **Order minting is one engine, two producers.** `copilot/mirror.ts` splits into a pure
  `specFromProdOrder` (prod order + overrides + facility remap -> `MintSpec`, unit-tested) and
  `mintPreprodOrder(pre, spec, {submit})` — the engine owning the sequence invariants (order names
  reset speciality/provider; placeOfService LAST, post-forReview; E6001-tolerant `/process` retry).
  `clone_order` composes extract -> spec -> mint; `create_preprod_order` mints from an explicit
  spec and **always passes `submit:false`** (hand-minted test orders stop at forReview — only
  `clone_order` may submit, explicitly). Do NOT expose the raw atoms (create draft / PUT field /
  process) as tools — the tool boundary is the safe transaction, not the HTTP verb.
- **UiPath writes are dev-clone-only and guarded.** `add_queue_item`, `delete_queue_item` and
  `start_job` (all in `uipath/actions.ts` — the ONLY module that mutates Orchestrator state) take
  `env: z.literal("pre_prod")` at the schema layer AND assert `env === "pre_prod"` in the domain
  function. Every posted SpecificContent passes `guardQueueItemSafety` (`uipath/safety.ts`, pure,
  unit-tested — the single enforcement point also used by `build_queue_item`/`pull_queue_item`):
  IsApproved forced false; non-empty `serverURL`/`queueUrl` must match the configured pre-prod
  values (fails closed when unconfigured); `<TO-FILL>` placeholders rejected.
  `delete_queue_item` is fetch-first and refuses any item whose Status isn't `New`. The MCP never
  repins releases and never creates queues — `list_processes`/`list_queues`/`list_triggers` are
  the read-side discovery/verification tools (dev-clone queue ids differ from the prod ids in
  `PORTALS`).
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
  under `skippedSections` — add new domains via a `SectionSyncer`, only with a verified endpoint.
- Tools return via the `ok()` / `err()` helpers and declare all four annotation hints
  (readOnly/destructive/idempotent/openWorld); `server.test.ts` enforces the wiring.

## Releasing

Tag-driven: bump `version` in `package.json` (and the `McpServer` version string in `server.ts`),
commit, push a matching `vX.Y.Z` tag → `release.yml` + `publish.yml` fan out from the tag.

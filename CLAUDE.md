# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

An MCP server (`@modelcontextprotocol/sdk`, `McpServer` over **stdio**) exposing EHR Copilot
order operations + UiPath Orchestrator execution analysis. Built with Bun; `bun build` bundles
`src/` into a node-runnable `dist/server.js` that runs under `npx` or `bunx`. Published private
on GitHub Packages.

## Commands

```bash
bun test                       # full suite (bun's runner, src/*.test.ts)
bun test src/settings.test.ts  # one file
bun test -t "diffList"         # tests matching a name
bun run typecheck              # tsc --noEmit (strict; exactOptionalPropertyTypes, verbatimModuleSyntax)
bun run lint                   # biome check src   (bunx biome check --write src to autofix)
bun run build                  # bundle -> dist/server.js (what npx/bunx execute)
bun run start                  # run from source, no build
COPILOT_MCP_CONFIG=… bun run inspect   # MCP Inspector UI to list/call tools without a host
```

Before finishing a change: `bun run typecheck`, `bun test`, and `bunx biome check src` should all pass.

## Architecture

- **`src/server.ts`** is a thin wiring layer only: it constructs the `McpServer`, then registers
  every tool (`server.registerTool`), the static resources + the `copilot://reference/portal/{name}`
  resource template, the prompts (`registerPrompts`), and logging (`registerLogging`). Tool *logic*
  lives in per-feature modules — keep server.ts declarative and push real work into a module.
- **Hybrid static/live model**: stable facts (portal registry, UiPath folder ids, schemas, safety
  rules in `reference.ts`) are exposed as **resources**; anything that changes per-run (orders,
  queue items, jobs, settings) is a **tool**. Don't turn live data into a resource.
- **Two external systems**: Copilot BE via `copilot-client.ts` (`makeClient`/`login`/`req`), and
  UiPath Orchestrator via `uipath.ts`. Each Copilot tenant exists per-env (`prod` / `pre_prod`).
- **`config.ts`** loads one validated config (single-file `COPILOT_MCP_CONFIG`, or split legacy
  files via `COPILOT_MCP_LOCAL_DIR`). `resolveCreds(profile)` returns `{ prod, pre_prod }` creds.
  Profiles are dynamic — read them with `listProfiles()`; never hardcode profile names.
- **`settings.ts`** (diff_settings / sync_settings): the hard part is cross-env noise. It strips
  env-specific fields (`stripNoise`: UIDs, timestamps, dummy emails, CDN hosts) and matches list
  items by a **semantic key** (`matchKey`, usually `name`), never by UID. The pure diff functions
  are unit-tested; keep them pure.
- **`notify.ts`**: `mcpLog()` sends MCP `notifications/message`; `reportProgress()` sends
  `notifications/progress` only when the caller passed a progressToken. Both are no-throw side-channels.

## Invariants (do not break)

- **Every Copilot/UiPath operation requires an `env` (`prod` | `pre_prod`) and a `profile`
  (`ossm`, `kafri`, …) plus its other args.** These are **required** tool/prompt parameters — never
  add a default env or default profile, and never silently assume one. `profile` is validated
  against the config at call time by `resolveCreds`.
- **stdout is the JSON-RPC channel.** A stray `console.log` to stdout corrupts the protocol.
  `console.*` is redirected to stderr in server.ts; use `mcpLog()` for client-visible logs and
  stderr (`LOG_LEVEL=debug` / `COPILOT_MCP_DEBUG=1`) for local diagnostics.
- **prod/pre-prod isolation**: writes target pre-prod only; `delete_preprod_order` never touches
  prod; `clone_order` is clone-only unless `submit` is explicitly authorized; `build_queue_item` /
  `pull_queue_item` force `IsApproved=false` so a test run can never submit a real auth.
- **`sync_settings` is a not-implemented stub** with *additive* intent (add prod-only items missing
  in pre-prod; never overwrite/delete). It throws `NotImplementedError` — keep it that way until the
  per-section write endpoints are mapped.
- Tools return via the `ok()` / `err()` helpers and declare all four annotation hints
  (readOnly/destructive/idempotent/openWorld); `server.test.ts` enforces the wiring.

## Releasing

Tag-driven: bump `version` in `package.json` (and the `McpServer` version string in `server.ts`),
commit, push a matching `vX.Y.Z` tag → `release.yml` + `publish.yml` fan out from the tag.

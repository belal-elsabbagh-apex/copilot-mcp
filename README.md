# @belal-elsabbagh-apex/copilot-mcp

MCP server exposing EHR Copilot operations over stdio. Built with [Bun](https://bun.sh)
(`bun build` bundles `src/` → a node-runnable `dist/server.js`), so it runs under either
`bunx` **or** `npx`. Published on GitHub Packages.

## Tools

28 tools, grouped below by domain. **Args** lists only the arguments relevant to auth/scope
(`env`, `profile`) — see each tool's own schema for the rest. Every Copilot tool requires an
explicit `profile`; most also require an explicit `env` (`prod` | `pre_prod`) — neither is ever
defaulted. UiPath-only tools take `env` but no `profile` (UiPath auths globally, not per-account).

### Copilot orders

All read tools here are READ-ONLY. Writes (`delete_preprod_order`, `create_preprod_order`,
`submit_preprod_order`) only ever target **pre-prod**; prod is never mutated. There's no single
"clone" tool — the `clone-and-verify-order` prompt (see Prompts below) orchestrates `find_clone_candidates`
→ `get_order` → `create_preprod_order` (and the settings tools, if a reference doesn't resolve).

| Tool | Args | What it does |
|------|------|--------------|
| `find_clone_candidates` | `profile` | Scan recent PROD orders and return only the ones that will actually clone to forReview (have a referredFacility, orderType, and orderNames) — filters out the ones that would get stuck "incomplete". |
| `create_preprod_order` | `profile` | Mint a fresh PRE-PROD order from explicit data and drive it to forReview. Never submits (see `submit_preprod_order`). Handles the sequence quirks internally (order names reset speciality/provider; place-of-service applied last; `/process` retry). If it doesn't reach forReview, `processMessage` explains why. Composes with `build_queue_item` → `add_queue_item` for dev-clone robot tests. |
| `submit_preprod_order` | `profile` | Submit a PRE-PROD order sitting at forReview, advancing it to inProgress. A real, deliberate write — requires explicit user authorization before calling. |
| `delete_preprod_order` | `profile` | Delete one or more orders from PRE-PROD (`DELETE /api/v1/orders/{uid}`). Never targets prod. |
| `get_order` | `env`, `profile` | Fetch one order's normalized detail (status, insurance, ICD/CPT, facility, POS, note presence) plus `documents` — clickable CDN links for the auth-screenshot/authorization-summary PDFs when they exist (CloudFront signed-cookie protected; never fetched server-side). |
| `find_stuck_orders` | `env`, `profile` | Scan recent orders in an env and flag ones sitting in a non-terminal status (default `inProgress`/`incomplete`/`pending`). Optional `crossCheckUipath` correlates each to its UiPath job(s) for a coarse verdict. |
| `build_queue_item` | `env`, `profile` | Fetch an order and BUILD the UiPath AddQueueItem request payload from it — never POSTs. `IsApproved` is always `false`. Pair with `add_queue_item` to actually post it. |
| `get_login_token` | `env`, `profile` | Log into the Copilot BE and return the session JWT (the same token UiPath callbacks use as `SpecificContent.token`). Authenticates only — reads/writes no order data. |

### UiPath Orchestrator — reads

All READ-ONLY.

| Tool | Args | What it does |
|------|------|--------------|
| `analyze_order_execution` | `env`, `profile` | Trace a Copilot `orderUid` to its UiPath job(s) and diagnose the run: state/verdict, computed durations/retry gaps, a structured fault (headline, stable signature, exception type), a condensed log digest, video link, and Orchestrator deep link. Optionally enriches with the order's current BE status. |
| `list_jobs` | `env` | List the most recent Orchestrator jobs in a folder, newest first, no order correlation. |
| `get_job` | `env` | Fetch one (or up to 25 via `jobKeys`) job(s) by GUID Key — the light poll target after `start_job`. `includeLogDigest:true` adds a structured fault + condensed log digest. |
| `get_job_logs` | `env` | Fetch a job's robot logs (oldest first, capped 500), optionally batched via `jobKeys` (up to 25) and filtered (`minLevel`, `contains`, `onlyFailures`, `tail`); messages truncate to 400 chars unless `fullMessages:true`. |
| `list_queue_items` | `env` | Browse a portal's UiPath queue for triage — resolve by `queueName` (prod) or `queueDefId` (pre-prod), optionally filtered by status. |
| `list_queues` | `env` | List the queue definitions in a folder — use to discover a `queueDefId` (dev-clone ids differ from the prod portal registry) or the exact queue `Name` for `add_queue_item`. |
| `list_processes` | `env` | List the releases ("processes") in a folder — `key` is the `releaseKey` `start_job` takes; check `processVersion`/`isLatestVersion` for the pinned package before starting. |
| `list_triggers` | `env` | List triggers (queue + time) in a folder — verify a queue trigger's `releaseId` points at your dev release before enqueueing. |
| `pull_queue_item` | `env` (optional if `url` given) | Fetch one queue item (by Orchestrator URL or `txnId`) and return its `SpecificContent` as a ready-to-run test payload. `IsApproved` is always forced `false`. |
| `build_faulted_job_issue` | `env` | Read a faulted job (by Key) + its robot logs and BUILD a ready-to-post GitHub issue payload (`title`/`body`/`labels`/`faultSignature`/`searchQuery`/`recurrenceComment`). Posts nothing — the caller hands the payload to a GitHub MCP server. |

### UiPath Orchestrator — writes (pre-prod only)

`env` is a schema-enforced literal `"pre_prod"` on every tool below — `"prod"` is rejected before
any HTTP call, and every posted payload passes a safety guard.

| Tool | Args | What it does |
|------|------|--------------|
| `add_queue_item` | `env` | POST one item to a dev-clone queue. `IsApproved` is forced `false`; a configured `serverURL`/`queueUrl` must match pre-prod; `<TO-FILL>` placeholders are rejected. Pace ~1/s when enqueueing many. |
| `delete_queue_item` | `env` | Delete one queue item from the dev clone. Fetch-first: refuses unless the item's current status is `New`, so run history is never destroyed. |
| `start_job` | `env` | Start job(s) for a release in the dev clone. A job resolves its package version at START (not creation) — re-check `list_processes` right before starting if others are publishing. |

### Settings sync (prod ↔ pre-prod)

`diff_settings`/`get_settings`/`plan_settings_sync` are READ-ONLY; `apply_settings_sync` is the
only settings tool that writes, and it's additive-only against pre-prod (never overwrites/deletes).

| Tool | Args | What it does |
|------|------|--------------|
| `diff_settings` | `profile` | Diff an account's settings between prod and pre-prod (UID/timestamp/dummy-email noise stripped, list items matched by name not UID). Scope with `groups` (top-level, e.g. `['orders']`) and/or `sections` (exact keys). |
| `get_settings` | `env`, `profile` | Fetch an account's settings sections from ONE env — the single-env counterpart to `diff_settings`. Normalized (noise-stripped) by default; `normalized:false` for raw payloads with real UIDs. |
| `list_setting_sections` | — | List every section `diff_settings`/`get_settings`/`plan_settings_sync` can scope to (key, label, group, kind, whether it's "derived"/crawled, and its `matchKey`). Pure static-catalog read, no network. |
| `plan_settings_sync` | `profile` | Compute — WITHOUT writing — the additive actions that would copy prod-only settings into pre-prod. Covers the **specialties** domain (`specialties`/`referred-providers`/`referred-facilities`) and the **orders** domain (create-only). Each action gets a stable id (`section:op:typeName:itemName`); refs with no pre-prod match by name are dropped with a warning. Sections with no verified write endpoint land in `skippedSections`. |
| `apply_settings_sync` | `profile` | Execute a reviewed selection of `plan_settings_sync` actions against pre-prod. Never accepts request bodies — it **re-plans server-side** (same scoping args) and executes only `actionIds` or an explicit `all:true` (exactly one required). Stale ids land in `unmatchedIds` instead of executing. Every write is audit-logged to the client. |

### Meta / diagnostics

| Tool | Args | What it does |
|------|------|--------------|
| `doctor` | `profile` | Probe the server's external connections — Copilot BE login (both envs) + one cheap UiPath Orchestrator call per env/folder — and report what's reachable, plus which UiPath auth mode (`oauth`/`bearer`) is active. |
| `build_mcp_issue` | — | Compose a GitHub issue about **this MCP server** from a bug report or general feedback — no tool failure required. Posts nothing and holds no GitHub credentials; returns `title`/`body`/`labels` (+ a prefilled `url`) for the host's GitHub tooling to file. |

## Prompts

User-invokable workflow prompts chain the tools above for common ops tasks:
`diagnose-order`, `reconcile-settings`, `inspect-settings`, `clone-and-verify-order`,
`triage-stuck-orders`, and `report-faulted-uipath-jobs`.

`reconcile-settings` walks the full settings workflow: `list_setting_sections` →
`diff_settings` → `plan_settings_sync` → review the planned action ids with the user →
`apply_settings_sync` with exactly the approved ids. `inspect-settings` reads one env's
settings via `get_settings` and summarizes them.

`clone-and-verify-order` picks a cloneable prod order (or uses a given uid), reads it via
`get_order`, and mints an equivalent pre-prod order via `create_preprod_order` — there's no
dedicated "clone" tool, since whether a clone succeeds is order-specific (it depends on
pre-prod already having the referenced facility/speciality/order type/order names). If minting
fails or the order doesn't reach forReview, the prompt diagnoses via `list_setting_sections` →
`diff_settings` → `plan_settings_sync`, reporting a proposed fix rather than applying one.
Submitting the resulting order is a separate, explicit `submit_preprod_order` call that only
happens with the user's authorization.

`report-faulted-uipath-jobs` finds faulted UiPath jobs (prod by default) and files each as a
GitHub issue on `Apex-Medical-AI-Inc/RPAPlaywright` — creating one issue per distinct fault
and commenting on the existing issue when the same fault recurs. This server holds **no
GitHub credentials**: the prompt drives a **GitHub MCP server connected in the host** to do
the actual search/create/comment, so that server must be connected with write access to the
repo.

## Configuration

The server reads one validated config holding both the Copilot BE creds and the UiPath
args. Two ways to provide it:

1. **Single file (preferred):** set `COPILOT_MCP_CONFIG` to a JSON file shaped like
   [`copilot-mcp.config.example.json`](./copilot-mcp.config.example.json). If
   `COPILOT_MCP_CONFIG` isn't set, the server looks for `copilot-mcp.config.json` in its
   working directory (falling back to the older `config.local.json` name if that's what
   you already have).
2. **Split legacy files:** set `COPILOT_MCP_LOCAL_DIR` to a directory containing
   `order-copy-credentials.json` + `uipath-config.json`.

The `uipath.queueUrl / addQueueItemPath / serverUrlByEnv` fields are only
required by `build_queue_item`. Editing the config file while the server is running
takes effect on the next tool call — no restart needed — and the server sends an MCP
logging notification when it reloads.

When a tool fails for a reason that looks like a bug in this server (an unexpected
exception — not a bad profile, missing/invalid config, not-found, auth, or any HTTP error
the upstream system returned), the error response includes a `reportIssue` block with a
prefilled GitHub new-issue URL. This is on by default; the optional `feedback` block turns
it off or repoints it:

```json
"feedback": { "enabled": false, "repositoryUrl": "https://github.com/your-org/your-fork" }
```

## Local development

```bash
bun install          # also installs the lefthook pre-commit hook (via `prepare`)
bun run typecheck
bun run lint         # biome check
bun test             # bun's built-in test runner (src/*.test.ts)
bun run build        # bundle src -> dist/server.js (node-runnable, what npx/bunx execute)
bun run start        # run from source via bun (no build needed)
bun run dev          # watch mode
```

### Exercising the tools

`bun run inspect` launches the [MCP Inspector](https://github.com/modelcontextprotocol/inspector)
against the server from source — a UI to list and call the tools without a host. Point it
at a config first (e.g. `COPILOT_MCP_CONFIG=… bun run inspect`).

Logging is silent by default so it never corrupts the stdio JSON-RPC stream. Set
`LOG_LEVEL=debug` (or `COPILOT_MCP_DEBUG=1`) to route progress logs to **stderr**.

A `pre-commit` hook (lefthook) runs `biome check` + `typecheck` on commit; CI
(`.github/workflows/ci.yml`) runs typecheck/lint/build/test on every PR and push to `main`.

## Installing — run directly from the release asset

Every release attaches a self-contained tarball as a GitHub Release asset, and the repo is
public, so `npx`/`bunx` can run it straight from the download URL — **no registry, no
`.npmrc`, no auth token**. The `releases/latest/download/copilot-mcp.tgz` URL always points
at the newest release; swap in a `download/vX.Y.Z/…` URL to pin a version.

### Wire into `.mcp.json`

```json
{
  "mcpServers": {
    "copilot": {
      "command": "npx",
      "args": [
        "-y",
        "https://github.com/belal-elsabbagh-apex/copilot-mcp/releases/latest/download/copilot-mcp.tgz"
      ],
      "env": {
        "COPILOT_MCP_CONFIG": "/abs/path/to/copilot-mcp.config.json"
      }
    }
  }
}
```

`npx` and `bunx` are interchangeable here (the published bin, `dist/server.js`, is a plain
node ESM entry). To pin a specific version, use the version-stamped asset instead of
`latest`, e.g. `.../releases/download/v1.14.0/belal-elsabbagh-apex-copilot-mcp-1.14.0.tgz`.
Use `COPILOT_MCP_LOCAL_DIR` instead of `COPILOT_MCP_CONFIG` for the split legacy files.

The package is also published to this repo's GitHub Packages registry (see below), but that
path needs a `read:packages` token; the release-asset URL above is the zero-auth default.

## Publishing

Publishing is tag-driven. Bump `version` in `package.json`, commit, then push a matching
`vX.Y.Z` tag:

```bash
git tag v1.2.0
git push origin v1.2.0
```

That one tag push fans out to two workflows (both keyed off the tag, because a
`GITHUB_TOKEN`-created release does not trigger other workflows):

- **Release on tag** (`release.yml`) — builds the bundle, `npm pack`s it, and creates a GitHub
  Release with auto-generated notes. It attaches two tarball assets: the version-stamped
  `belal-elsabbagh-apex-copilot-mcp-X.Y.Z.tgz` (for pinning) and a stable-named
  `copilot-mcp.tgz` that backs the `releases/latest/download/copilot-mcp.tgz` URL used above.
- **Publish** (`publish.yml`) — typechecks, builds, and runs `bun publish` to GitHub
  Packages using the repo's `GITHUB_TOKEN` (`packages: write`); the package inherits the
  repository's public visibility. Also runnable manually via **workflow_dispatch**.

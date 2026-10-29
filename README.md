# @belal-elsabbagh-apex/copilot-mcp

MCP server exposing EHR Copilot operations over stdio. It is a Go program, **served through `npx`**: the
release tarball's `bin/copilot-mcp.js` is a tiny Node launcher that execs the bundled static Go binary for your
platform (`dist/<goos>-<goarch>/copilot-mcp[.exe]`).

**Platforms:** linux/amd64, darwin/arm64 (Apple Silicon) and windows/amd64 — nothing else ships (Intel Macs and
linux/arm64 are unsupported; the launcher exits with a message listing the three). **Requirements:** Node >= 20 to
run it (the launcher only); nothing else — the binary is self-contained. Go 1.27 is needed only to *develop* it.

## Install and run

**There is no npm registry.** The package is `private`, is not published to npmjs or GitHub Packages, and needs no
`.npmrc` or token. Every GitHub release attaches `copilot-mcp.tgz` and the repo is public, so `npx` runs it straight
from the download URL. `releases/latest/download/copilot-mcp.tgz` always points at the newest release; swap in
`releases/download/vX.Y.Z/copilot-mcp.tgz` to pin a version.

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

Pinned form: `npx -y https://github.com/belal-elsabbagh-apex/copilot-mcp/releases/download/vX.Y.Z/copilot-mcp.tgz`
(a release also carries the same tarball as `belal-elsabbagh-apex-copilot-mcp-X.Y.Z.tgz`).
Use `COPILOT_MCP_LOCAL_DIR` instead of `COPILOT_MCP_CONFIG` for the split legacy files.

The tarball is about 13 MB (v2.0.0's JS tarball was 174 KB): each Go binary carries its own runtime. `npx` caches
the package once per version, so a pinned URL downloads once.
A release also attaches the raw binaries (`copilot-mcp-linux-amd64`, `copilot-mcp-darwin-arm64`,
`copilot-mcp-windows-amd64.exe`) if you would rather run one directly with no Node at all.

### As an MCP Bundle (`.mcpb`)

Every release also attaches `copilot-mcp.mcpb` — a self-contained [MCP Bundle](https://github.com/anthropics/mcpb)
(launcher + the three platform binaries + `manifest.json` + `copilot-mcp.config.example.json`) for one-click install in
MCPB-supporting hosts (e.g. Claude for macOS/Windows). Download it from the
[latest release](https://github.com/belal-elsabbagh-apex/copilot-mcp/releases/latest), open it with the host, and
when prompted for the **Config file** option point it at your own `copilot-mcp.config.json` (see
[`copilot-mcp.config.example.json`](./copilot-mcp.config.example.json)). The host runs `node bin/copilot-mcp.js`
with `COPILOT_MCP_CONFIG` set to that path.

## Tools

33 tools, grouped below by domain. **Args** lists only the arguments relevant to auth/scope
(`env`, `profile`, `orchestrator`) — see each tool's own schema for the rest. Every Copilot tool requires an
explicit `profile`; most also require an explicit `env` (`prod` | `pre_prod`) — neither is ever
defaulted. The one exception is `list_clinics`, which takes `env` but no `profile`: it's the
discovery tool that produces the profile → clinicUid mapping, so requiring a profile would be
circular. Orchestrator tools also take `env` but no `profile` (orchestrators auth globally, not per-account), plus an
`orchestrator` (`uipath` | `apex`) that is **required and never defaulted** on every job- or queue-level tool. The carve-outs:
`find_order_queue_items` and `find_stuck_orders` (omitted = every registered backend) and the Apex-only `get_job_artifacts` (no `orchestrator` arg).

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
| `search_orders` | `env`, `profile` | Filter an account's orders by location, insurance, referred-to, order type, auth/upload status, MRN, free text or any of 5 date ranges (`POST /orders/filter`). Returns slim, non-PHI rows (`referredTo.npi` is always a string, even when the BE sends a number); defaults to type `Outbound Referral`, `pageSize` 100, `pageNumber` 1. |
| `get_order_category_stats` | `env`, `profile` | Cheap per-folder order counts (For Review, PCP Notes, Processing, Archived, with sub-buckets) for a filter, same dimensions as `search_orders`, no pagination. |
| `find_stuck_orders` | `env`, `profile`, `orchestrator?` | Scan recent orders in an env and flag ones sitting in a non-terminal status (default `inProgress`/`incomplete`/`pending`). Optional `crossCheck` correlates each to its queue item(s) and job(s) on UiPath and/or Apex (`orchestrator` omitted = both) for a coarse verdict using the latest attempt; adds `job-never-started` for an Apex job that failed before any worker claimed it. Each stuck order carries `orchestrator: { backend, verdict, … }`. |
| `build_queue_item` | `env`, `profile`, `orchestrator` | Fetch an order and BUILD the queue-item request payload for the chosen orchestrator — never POSTs. `IsApproved` is always `false`. UiPath stringifies array/object fields as before; Apex keeps native types. Pair with `add_queue_item` to actually post it. |
| `get_login_token` | `env`, `profile` | Log into the Copilot BE (reusing a cached session) and return the session JWT (the same token UiPath callbacks use as `SpecificContent.token`), plus `mode` (`support`/`direct`), `clinicUid`, and `activeClinicId`. Authenticates only — reads/writes no order data. |
| `list_clinics` | `env` (no `profile` — see note above) | List the clinics the configured **support account** can switch into (`clinicUid`, name, last-active, owner email), each annotated with the config profile already mapped to it (`null` if unmapped). Use it to fill `copilot.profiles.<name>.<env>.clinicUid`. Requires `copilot.support.<env>`. |

### Orchestrator (UiPath + Apex) — reads

All READ-ONLY. Every tool takes `env` and `orchestrator` (`uipath` | `apex`); results come back as `{ orchestrator, env, … }` with backend-neutral cores (`JobCore`, `QueueItemCore`, `QueueCore`, `ProcessCore`, `TriggerCore`) and backend-specific fields under `details`. Job `state` uses UiPath's vocabulary on both backends (Apex statuses are mapped; `rawState` keeps the original). One Apex transaction can have **many** jobs (a retry is a new job on the same queue item), so queue items carry `jobs[]`, newest first.

| Tool | Args | What it does |
|------|------|--------------|
| `list_jobs` | `env`, `orchestrator` | List the most recent jobs in a folder, newest first, no order correlation (`since`, `processName`, `top`, `folder`). Apex rows are thin (`fault.class` = `error_class`); use `get_job` for detail. |
| `get_job` | `env`, `orchestrator` | Fetch one (`jobId`) or up to 25 (`jobIds`) jobs — the light poll target after `start_job`. `includeLogDigest:true` adds a structured fault + condensed log digest; on Apex a missing log yields `logsUnavailable.reason` = `running` \| `never-started` \| `artifact-policy` instead of a digest. Optional `refresh: true` bypasses the persistent cache (see Caching). |
| `get_job_logs` | `env`, `orchestrator` | Fetch a job's logs (oldest first, capped 500), batchable via `jobIds` (up to 25) and filterable (`minLevel`, `contains`, `onlyFailures`, `tail`); messages truncate to 400 chars unless `fullMessages:true`; `includeRawFields` adds each row's structured fields; `includeVideo` adds a `video` reference. Optional `refresh: true` bypasses the persistent cache (see Caching). |
| `get_job_artifacts` | `env` (**no `orchestrator`: Apex-only**) | List a job's artifacts (`filename`, `type`, `sizeBytes`) and optionally `download` named files (or `"all"`) to `~/.cache/copilot-mcp/artifacts/<jobId>/` (mode `0700`) — `job.log`, `trace.zip`, screenshots, recording. PHI: never commit or paste them. Optional `refresh: true` bypasses the persistent cache (see Caching). |
| `find_order_queue_items` | `env`, `orchestrator?` | Find the queue item(s) an order flowed through and their job ids. `orchestrator` omitted = search every registered backend (each fails independently into `searchErrors`); `folder` is only accepted with an explicit `orchestrator`. Output `jobIds: { uipath?: […], apex?: […] }` feeds straight into `get_job`'s `jobIds`. |
| `list_queue_items` | `env`, `orchestrator` | Browse a portal's queue for triage — `queue` is a portal key/alias or a queue id, optionally filtered by `status`. |
| `list_queues` | `env`, `orchestrator` | List queues in a folder — use to discover a queue id (pre-prod ids differ from the prod portal registry) or the exact queue name for `add_queue_item`. |
| `list_processes` | `env`, `orchestrator` | List processes (UiPath releases / Apex workflows) — `id` is the `processId` `start_job` takes; check `version` (UiPath: `isLatestVersion`) and, on Apex, `details.artifactPolicy`. |
| `list_triggers` | `env`, `orchestrator` | List triggers (queue + time). Apex has no triggers API, so queue triggers are synthesized from queues (`enabled` = active and auto-drain) and time triggers come from schedules. |
| `pull_queue_item` | `orchestrator`, `env` (optional if `url` given) | Fetch one queue item (UiPath: Orchestrator `url` or `itemId`; Apex: `queueId` + `itemId`) and return its content as a ready-to-run test payload. `IsApproved` is always forced `false`. |
| `build_faulted_job_issue` | `env`, `orchestrator` | Read a faulted job + its logs and BUILD a ready-to-post GitHub issue payload (`title`/`body`/`labels`/`faultSignature`/`searchQuery`/`recurrenceComment`). Posts nothing — the caller hands the payload to a GitHub MCP server. Optional `refresh: true` bypasses the persistent cache (see Caching). |

### Orchestrator (UiPath + Apex) — writes (pre-prod only)

`env` is a schema-enforced literal `"pre_prod"` on every tool below — `"prod"` is rejected before
any HTTP call (UiPath: the dev-clone folder; Apex: the `Authorization Test` folder), and every posted payload passes a safety guard.

| Tool | Args | What it does |
|------|------|--------------|
| `add_queue_item` | `env`, `orchestrator` | POST one item to a pre-prod queue (`queue`, `reference`, `specificContent`). `IsApproved` is forced `false`; a configured `serverURL`/`queueUrl` must match pre-prod; `<TO-FILL>` placeholders are rejected. Pace ~1/s when enqueueing many. |
| `delete_queue_item` | `env`, `orchestrator` | Delete one queue item (`itemId`; Apex also `queueId`). Fetch-first: refuses unless the item's current status is `New`, so run history is never destroyed. Apex also refuses a `queueId` that isn't in the `Authorization Test` folder. |
| `start_job` | `env`, `orchestrator` | Start job(s) for a process (`processId`, `input`, `jobsCount` 1–5; Apex also requires `matchValue`). The job runs the process's pinned/current version as resolved at dispatch — re-check `list_processes` right before starting if others are publishing. |
| `stop_job` | `env`, `orchestrator` | Stop (`SoftStop`) or kill (`Kill`) a job. Fetch-first: refuses a job already in a terminal state. Apex also refuses a job outside the `Authorization Test` folder. |

### Settings sync (prod ↔ pre-prod)

`diff_settings`/`get_settings`/`plan_settings_sync` are READ-ONLY; `apply_settings_sync` is the
only settings tool that writes, and it's additive-only against pre-prod (never overwrites/deletes).

| Tool | Args | What it does |
|------|------|--------------|
| `diff_settings` | `profile` | Diff an account's settings between prod and pre-prod (UID/timestamp/dummy-email noise stripped, list items matched by name not UID). Scope with `tags` (e.g. `['orders']`) and/or `sections` (exact keys). |
| `get_settings` | `env`, `profile` | Fetch an account's settings sections from ONE env — the single-env counterpart to `diff_settings`. Normalized (noise-stripped) by default; `normalized:false` for raw payloads with real UIDs. |
| `list_setting_sections` | — | List every section `diff_settings`/`get_settings`/`plan_settings_sync` can scope to (key, label, `tags`, kind, whether it's "derived"/crawled, and its `matchKey`), plus the list of all tags; narrow with `tag` / `sections`. Pure static-catalog read, no network. |
| `plan_settings_sync` | `profile` | Compute — WITHOUT writing — the additive actions that would copy prod-only settings into pre-prod. Covers the **specialties** domain (`specialties`/`referred-providers`/`referred-facilities`) and the **orders** domain (create-only). Each action gets a stable id (`section:op:typeName:itemName`); refs with no pre-prod match by name are dropped with a warning. Sections with no verified write endpoint land in `skippedSections`. |
| `apply_settings_sync` | `profile` | Execute a reviewed selection of `plan_settings_sync` actions against pre-prod. Never accepts request bodies — it **re-plans server-side** (same scoping args) and executes only `actionIds` or an explicit `all:true` (exactly one required). Stale ids land in `unmatchedIds` instead of executing. Every write is audit-logged to the client. |

### Meta / diagnostics

| Tool | Args | What it does |
|------|------|--------------|
| `doctor` | `profile` | Probe the server's external connections — three checks per env (prod + pre-prod): Copilot BE auth (reusing a cached session; reports `mode` and, for support-account envs, the `activeClinicId` it switched to), a clinic-scope fingerprint (`GET /orders/locations`, which differs per clinic — the cheap proof a support session really landed on the intended clinic, not just that login succeeded), and one cheap UiPath Orchestrator call per env/folder. Reports what's reachable per check, plus which UiPath auth mode (`oauth`/`bearer`) is active. When Apex is configured it adds an Apex check: which config source supplied the credentials (file or env), that login succeeds, and that both folders resolve to ids. |
| `build_mcp_issue` | — | Compose a GitHub issue about **this MCP server** from a bug report or general feedback — no tool failure required. Posts nothing and holds no GitHub credentials; returns `title`/`body`/`labels` (+ a prefilled `url`) for the host's GitHub tooling to file. |

## Prompts

User-invokable workflow prompts chain the tools above for common ops tasks:
`diagnose-order`, `reconcile-settings`, `inspect-settings`, `clone-and-verify-order`,
`triage-stuck-orders`, `report-faulted-jobs`, and `send-mcp-feedback`.

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

`report-faulted-jobs` finds faulted jobs on the chosen `orchestrator` (`uipath` or `apex`; prod by default) and files each as a
GitHub issue on `Apex-Medical-AI-Inc/RPAPlaywright` — creating one issue per distinct fault
and commenting on the existing issue when the same fault recurs. Apex fault signatures are
`apex:`-prefixed so they never collide with UiPath ones. This server holds **no
GitHub credentials**: the prompt drives a **GitHub MCP server connected in the host** to do
the actual search/create/comment, so that server must be connected with write access to the
repo.

`send-mcp-feedback` (`summary`, optional `kind` = `bug` | `feedback`) turns a report about this server itself into a
GitHub issue via `build_mcp_issue` and the host's GitHub tooling.

## Configuration

The server reads one validated config holding the Copilot BE creds and the UiPath / Apex
args. Two ways to provide it:

1. **Single file (preferred):** set `COPILOT_MCP_CONFIG` to a JSON file shaped like
   [`copilot-mcp.config.example.json`](./copilot-mcp.config.example.json). If
   `COPILOT_MCP_CONFIG` isn't set, the server looks for `copilot-mcp.config.json` in its
   working directory (falling back to the older `config.local.json` name if that's what
   you already have).
2. **Split legacy files:** set `COPILOT_MCP_LOCAL_DIR` to a directory containing
   `order-copy-credentials.json` + `uipath-config.json`.

Each `copilot.profiles.<name>` entry has a `prod`/`pre_prod` env pair, and each env is
**either**:

- a **support-account pointer**, `{ "clinicUid": "..." }` — the credentials that actually log
  in come from `copilot.support.<env>` (shared across every profile using this mode for that
  env); use the `list_clinics` tool to look up each clinic's `clinicUid`, or
- a **direct login**, `{ "be", "email", "password", "fe"? }` — the original per-account shape,
  unchanged.

`clinicUid` wins if a profile entry somehow carries both, which is how an account is migrated
to the support-account model one env at a time without deleting its old credentials. Today
prod ships as support-account (`copilot.support.prod` required) and pre-prod stays on direct
login, but nothing hard-codes that split — it's whatever each profile's env entries say.

Authenticated sessions (cookies + JWT) are cached in memory and, by default, on disk at
`~/.cache/copilot-mcp/sessions.json` (mode `0600`; `$XDG_CACHE_HOME` is honored when set to an
absolute path; `COPILOT_MCP_CACHE` overrides the path outright) so a server restart doesn't
burn a login. Set `"copilot.sessionCache": false` to disable disk persistence (memory-only for
the process lifetime; this also disables the job caches and token persistence, see Caching). This is what keeps the support account's 10-logins-per-15-minutes budget
from being exhausted by routine restarts — never commit or echo that cache file, it holds live
tokens.

The `uipath.queueUrl / addQueueItemPath / serverUrlByEnv` fields are only
required by `build_queue_item`. Editing the config file while the server is running
takes effect on the next tool call — no restart needed — and the server sends an MCP
logging notification when it reloads.

### Apex orchestrator (optional)

Add an `apex` block to enable `orchestrator: "apex"`:

```json
"apex": {
  "apiUrl": "https://orchestrator.apexmedicalai.com",
  "username": "…", "password": "…",
  "folderByEnv": { "prod": "Authorization", "pre_prod": "Authorization Test" },
  "serverUrlByEnv": { "pre_prod": "https://pre-prod-be-batch.ehrcopilotbe.com" },
  "queueUrl": "https://sqs.…"
}
```

- `apiUrl` is the **load-balancer host, never a CloudFront host** (CloudFront caps request bodies at 8 KB).
- `username`/`password` are your own login — Apex audits writes per user.
- `folderByEnv` is optional (the values above are the defaults); folder names resolve to ids at runtime.
- `serverUrlByEnv` / `queueUrl` pin what a posted queue item may carry (the write guard fails closed when they are unset).
- Any of `apiUrl` / `username` / `password` the file leaves out is read from `APEX_API_URL` /
  `APEX_USER_NAME` / `APEX_PASSWORD`. This needs a host that passes its environment through (CLI
  hosts and `npx` do); **MCPB installs only pass `COPILOT_MCP_CONFIG`, so they must use the file.**
- With neither source configured, every `orchestrator: "apex"` call fails with "Apex is not
  configured"; UiPath and Copilot tools are unaffected. `doctor` reports which source was used.

`get_job_artifacts` downloads Apex artifacts to `~/.cache/copilot-mcp/artifacts/<jobId>/`
(directory mode `0700`, never inside a repo). Treat them as PHI.

See the `copilot://reference/config-guide` MCP resource for a full field-by-field walkthrough
(load order, an annotated JSON template, and the `doctor`/`list_clinics` verification steps) —
it's what an agent reads to set this server up from scratch.

When a tool fails for a reason that looks like a bug in this server (an unexpected
exception — not a bad profile, missing/invalid config, not-found, auth, or any HTTP error
the upstream system returned; a transport failure such as a refused/reset connection, a DNS error or a
timeout *is* flagged), the error response includes a `reportIssue` block with a
prefilled GitHub new-issue URL. This is on by default; the optional `feedback` block turns
it off or repoints it:

```json
"feedback": { "enabled": false, "repositoryUrl": "https://github.com/your-org/your-fork" }
```

## Caching

Settled jobs never change, so reads of them are cached **persistently** (memory + disk) and survive restarts:
Apex artifact listings and parsed `job.log`, UiPath RobotLogs and terminal job details, plus the Apex JWT and UiPath
OAuth token (so a restart doesn't burn a login). An entry is stored only when a live read shows the job
terminal **and** settled (ended 5+ minutes ago; 15 for Apex `cancelled`/`lost`/`timed_out`); errors, not-found
and running jobs are never cached, and write tools never read through a cache. The two backends differ on a
*hit*: Apex always reads the job live first (so a hit is served only after that read confirms the job is still
settled), while UiPath serves a hit with **no upstream call at all** (terminal states are absorbing, and a
per-hit read would spend the tenant's 100 req/min `Jobs` budget); UiPath makes its extra light `Jobs` read only on a miss.

- **Job logs and outputs are PHI and now persist on disk** under the cache directory (`~/.cache/copilot-mcp/`;
  `$XDG_CACHE_HOME` and `COPILOT_MCP_CACHE` are honored, same rules as the session cache): `cache/<namespace>/` and
  `tokens/`, directories `0700`, files `0600`, filenames are hashes. Same protection as the artifacts directory —
  never commit or paste them. Bounded to 512 MiB per namespace and 30 days.
- **Force a refresh:** pass `refresh: true` to `get_job`, `get_job_logs` or `build_faulted_job_issue`. It skips the
  cache read and stores the fresh result. `get_job_artifacts` also accepts it, but that tool never reads a cache
  (it always lists live); there `refresh` only re-downloads files whose local copy already matches.
- **Turn it off:** `"copilot.sessionCache": false` disables *all* disk persistence (sessions, tokens, job caches);
  they then live in memory for the process only. Or delete the cache directory at any time.
- `LOG_LEVEL=debug` logs cache events (`hit-disk`, `hit-memory`, `stored`, `refresh-bypass`) with hashed keys only.

Details, keys and bounds: [`docs/concurrency-and-caching.md`](./docs/concurrency-and-caching.md).

## Migrating from v1.x (v2.0.0 breaking changes)

v2.0.0 puts UiPath and Apex behind one tool surface, so argument names and output shapes became
backend-neutral. Skills/prompts that call the old names must be updated.

| v1.x | v2.0.0 |
|------|--------|
| (no such arg) | `orchestrator: "uipath" \| "apex"` — **required** on every job/queue-level tool; optional on `find_order_queue_items` / `find_stuck_orders` (omitted = every registered backend) |
| `jobKey` / `jobKeys` (`get_job`, `get_job_logs`, `build_faulted_job_issue`) | `jobId` / `jobIds` |
| `releaseKey` (`start_job`) | `processId` (UiPath release Key \| Apex workflow uuid, from `list_processes`) |
| `inputArguments` (`start_job`) | `input` |
| `queueName` (`list_queue_items`, `add_queue_item`) / `queueDefId` (`list_queue_items`) | `queue` — a portal key/alias or queue id (`list_queue_items`); the exact queue name or id (`add_queue_item`) |
| `queueDefinitionId` (`list_triggers`) | `queueId` |
| `txnId` (`delete_queue_item`, `pull_queue_item`) | `itemId` (string; Apex also needs `queueId`) |
| `crossCheckUipath` (`find_stuck_orders`) | `crossCheck` |
| `find_order_queue_items` → `jobKeys` | `jobIds: { uipath?: string[], apex?: string[] }` |
| `find_stuck_orders` → per-order `uipath` | per-order `orchestrator: { backend, verdict, … }` |
| `get_job_logs` → `videoUrl` | `video` (`{kind:"url"}` on UiPath, `{kind:"artifact"}` on Apex — fetch it with `get_job_artifacts`) |
| `jobDeepLinkBase` + `{key}` substitution | per-job `link` |
| `get_job` / `list_jobs` flat job rows | `JobCore` (`id`, `state`, `rawState`, `terminal`, `process`, `queueItem`, `createdAt`/`startedAt`/`endedAt`, `attempt`, `retryOfJobId`, `fault`, `link`, `details`); backend-specific fields live under `details` |
| tool results `{env, …}` | `{orchestrator, env, …}` |
| prompt `report-faulted-uipath-jobs` | `report-faulted-jobs` (adds `orchestrator`) |
| resource `copilot://reference/uipath-folders` | `copilot://reference/orchestrators` |

UiPath `state` values and UiPath fault signatures are unchanged. Apex job statuses are mapped
onto the same state vocabulary (`completed`→`Successful`, `failed`→`Faulted`, `cancelled`→`Stopped`, …);
the raw value is always in `rawState`.

## Local development

Needs Go 1.27 (and Node >= 20, `jq`, `curl` and `python3` for the package smoke). **`make check` is the single
gate** — vet, gofmt, golangci-lint (pinned), actionlint, `go test -race`, a package smoke (cross-builds the three
platforms, runs the launcher through `initialize`, checks the tarball contents, then cold-installs the tarball over
local HTTP with `npx -y http://127.0.0.1:<port>/copilot-mcp.tgz` — the shipped install path) and the release-script
self-test — and CI runs exactly it.

```bash
make build         # ./copilot-mcp (version from `git describe`, leading v stripped)
make test          # go test -race ./...
make check         # the whole gate
make hooks         # opt in: pre-push hook runs `make check`
go test -race ./internal/copilot/settings/ -run TestName   # one package / test
go run ./cmd/copilot-mcp                                   # run from source (needs COPILOT_MCP_CONFIG)
```

### Exercising the tools

Build, then launch the [MCP Inspector](https://github.com/modelcontextprotocol/inspector) against the binary —
a UI to list and call the tools without a host:

```bash
make build
COPILOT_MCP_CONFIG=/abs/path/to/copilot-mcp.config.json npx @modelcontextprotocol/inspector ./copilot-mcp
```

Logging is silent below warn by default so it never corrupts the stdio JSON-RPC stream. Set `LOG_LEVEL=debug`
(or `COPILOT_MCP_DEBUG=1`) to route logs to **stderr**.

Conventions, invariants and the package map: [`CLAUDE.md`](./CLAUDE.md); Go style:
[`.claude/skills/go-style/`](./.claude/skills/go-style/SKILL.md); concurrency and caching:
[`docs/concurrency-and-caching.md`](./docs/concurrency-and-caching.md).

## Releasing

Tag-driven; the version is the git tag, so there is **nothing to bump by hand** (`package.json` and
`manifest.json` stay at `0.0.0-dev`; the workflow stamps them). Add a `## [X.Y.Z] - date` entry to
`CHANGELOG.md`, merge to `main`, then:

```bash
make release VERSION=X.Y.Z DRY_RUN=1   # print the plan
make release VERSION=X.Y.Z             # tag origin/main and push only that tag
```

`scripts/release.sh` refuses an existing tag, a missing changelog entry, a dirty tree, or a HEAD that isn't
`origin/main`. The pushed tag triggers `.github/workflows/release.yml`: it refuses a tag that is not on
`origin/main`, runs the gate (`check.yml`), stamps the tag's version into `package.json` and `manifest.json`, then
`make dist` (the three binaries, `serverInfo.version` = the bare version, e.g. `2.1.0`), `npm pack` and `make mcpb`,
and creates the GitHub release. Nothing is published to a registry. The release attaches `copilot-mcp.tgz`,
`belal-elsabbagh-apex-copilot-mcp-X.Y.Z.tgz`, `copilot-mcp.mcpb` and the raw binaries. A `-` in the version
(`2.1.0-rc.1`) creates a GitHub prerelease (`releases/latest` skips it).

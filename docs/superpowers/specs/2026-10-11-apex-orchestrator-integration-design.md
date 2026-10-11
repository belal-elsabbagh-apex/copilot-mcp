# Apex orchestrator integration — design

Status: approved in brainstorming 2026-10-11 · Target release: **v2.0.0** · Branch: `feat/apex-orchestrator`
Appendix: [`2026-10-11-apex-orchestrator-divergences.md`](2026-10-11-apex-orchestrator-divergences.md) (the verified UiPath↔Apex divergence matrix this contract is built on).

## 1. Goal

Apex is replacing UiPath Orchestrator one portal at a time. During the migration an order can run on either orchestrator, and Cloud-EHR, not this repo, decides which one.
copilot-mcp must diagnose and (pre-prod only) drive both through **one orchestrator tool surface backed by two implementations**.
Retiring UiPath must come down to deleting `src/uipath/` and one enum value, with no tool renames.

### In scope

- Read parity with today's UiPath tools: jobs, logs and log digest, queues, queue items, processes, triggers, order → transaction → job correlation, stuck-order cross-check, and fault → GitHub-issue payload.
- Pre-prod write parity: `add_queue_item`, `delete_queue_item`, `start_job`, `stop_job`, plus the mixed `build_queue_item` and `pull_queue_item`.
- One Apex-only read: `get_job_artifacts`.

### Non-goals

These stay with CI, the dashboard, or the `apex-orchestrator` plugin:

- Deploy, publish, pin, and deploy tokens. `apex-pin.yml` owns these, and this MCP never repins.
- Creating Processes or queues.
- Transaction retry and replay.
- Action Center.
- Credentials, assets, mailboxes, buckets, shares.
- Schedule writes, workers, audit.
- An SSE live-log client.

## 2. Ground truth (verified 2026-10-11, GET-only, PHI-masked)

- **Deployment.** `https://orchestrator.apexmedicalai.com` is the **prod** Apex deployment (`platform-v1.3.3`).
  - Folders: `Authorization` (prod, the default folder), `Authorization Test`, `Eligibility Check`.
  - Staging shares prod's backend (apex-auth-submit `docs/superpowers/specs/2026-10-07-apex-pin-deploy-targets-design.md` F10, rows 50–52).
- **Current traffic.** Both Authorization folders currently hold **only pre-prod-BE items** (`serverURL` = the pre-prod batch host, `IsApproved=false`). Pre-prod BE submits land in `Authorization Test`. Prod still runs on UiPath.
- **Queue names.** Queues are named `<PORTAL KEY> auth submit queue`, the same as the UiPath QueueName and the `PORTALS` key. Casing varies (`Cencal`/`CENCAL`).
- **Auth.**
  - `POST /api/v1/auth/login {username,password}` returns the JWT **only** in the `access_token` HttpOnly Set-Cookie. It is valid for 24 h and is sent as `Authorization: Bearer`.
  - Folder scope is the `X-Folder-Id` UUID header. **Omitting it silently reads the default folder.**
- **API surface.** The route set and request bodies come from the dashboard JS bundles. The plugin docs (v1.4.2) are secondary.

## 3. Decisions

| # | Decision | Why |
|---|---|---|
| D1 | One tool surface. Each job- or queue-level tool takes a **required** `orchestrator: "uipath" \| "apex"`. | Job ids are GUID-shaped on both backends, so they can't be auto-detected. "Never default env" is already the house rule. |
| D2 | `find_order_queue_items` and `find_stuck_orders` make `orchestrator` optional; **omitted = both**. | Routing is per portal and decided outside this repo. Where the item actually sits is the truth. |
| D3 | Normalized cores plus a typed per-backend `details`. The state vocabulary is the **Apex dashboard's own UiPath-label map**. | UiPath `state` values stay unchanged. Agents reason across backends without learning two vocabularies. |
| D4 | A transaction maps to **many** jobs (`jobs[]`, newest first). Verdicts use the latest attempt. | Apex retries are new jobs on the same item (`retry_of_job_id`, `attempt_number`). UiPath retries are new items. |
| D5 | Apex calls always send `X-Folder-Id`, and only verified query filters are sent. | A missing header reads the wrong folder silently. Unknown params are silently ignored (`/jobs?queue_item_id=` returns everything). |
| D6 | Apex writes are pre-prod only, with three layers: schema literal, domain assert, and the chokepoint refuses any non-GET unless `env==="pre_prod"`. | Prod and test folders share one deployment and one login, so the folder is the only boundary. |
| D7 | UiPath fault signatures stay **byte-identical**. Apex signatures are `apex:`-prefixed. | Dedupe against existing GitHub issues keeps working. Signatures never collide across backends. |
| D8 | Config file first, then `APEX_API_URL` / `APEX_USER_NAME` / `APEX_PASSWORD` for any field the file doesn't set. | The user decided this. It matches the plugin's variable names. |
| D9 | Argument names become neutral in the same release: `jobKey(s)` → `jobId(s)`, `releaseKey` → `processId`. | Shapes change anyway in v2.0.0, so the rename costs nothing extra. |
| D10 | No Apex ids are hardcoded. Queues resolve by name and folders by name → UUID at runtime, cached per process. | Ids differ per folder and change at portal bring-up. |

## 4. Architecture

```
src/orchestrator/                 backend-neutral contract (new)
  types.ts                        Orchestrator, JobCore, QueueItemCore, QueueCore, ProcessCore,
                                  TriggerCore, JobRef, JobLog (moved), state label maps + terminal sets
  backend.ts                      OrchestratorBackend interface + `backends` registry {uipath, apex}
  log-digest.ts, log-semantics.ts moved from src/uipath/ (generic over JobLog rows)
  faults.ts                       moved: normalizeError + issue formatting (per-backend title/labels/signature)
  safety.ts                       moved: guardQueueItemSafety (limits supplied by the backend)
src/uipath/                       unchanged internals; uipath/backend.ts adapts to the interface;
                                  raw-message.ts (RawMessage → transactionOutcome) stays here
src/apex/                         new
  client.ts                       apexRequest chokepoint: config, auth, folders, transport, write gate
  job-log.ts                      pure job.log parser
  apex.ts                         reads + mapping to cores (implements the read half of the interface)
  actions.ts                      writes (the ONLY module that mutates Apex state)
```

- `server.ts` stays declarative. Each orchestrator tool calls `backends[orchestrator].<method>(…)`.
- `rateLimitDelayMs` moves to `src/shared/` so both transports can use it.
- `OrchestratorBackend` (approximately 12 methods): `listJobs`, `getJobs`, `getJobLogs`, `listQueues`, `listQueueItems`, `findOrderQueueItems`, `getQueueItem`, `listProcesses`, `listTriggers`, `addQueueItem`, `deleteQueueItem`, `startJob`, `stopJob`. Two real implementations exist from the start.

## 5. Contract

```ts
type Orchestrator = "uipath" | "apex";

interface JobRef { orchestrator: Orchestrator; id: string }

interface JobCore extends JobRef {
  state: string;          // dashboard label: Pending|Claimed|Running|Successful|Faulted|Stopped|Stopping|Lost|Timed Out|Suspended|Resumed|…
  rawState: string;       // UiPath State / Apex status, verbatim
  terminal: boolean;      // per-backend terminal set (below)
  process: { id: string | null; name: string; version: string | null };
  queueItem: { queueId: string; itemId: string; reference: string } | null;
  createdAt: string; startedAt: string | null; endedAt: string | null;
  attempt: number | null; retryOfJobId: string | null;   // UiPath: null
  fault: { message: string; signature: string; class: string | null } | null;
  link: string;           // dashboard deep link
  details: UiPathJobDetails | ApexJobDetails;            // discriminated by `orchestrator`
}

interface QueueItemCore {
  orchestrator: Orchestrator; queueId: string; queueName: string; itemId: string;
  reference: string; orderUid: string | null; status: string; rawStatus: string;
  exceptionType: "business" | "application" | null;
  createdAt: string; endedAt: string | null;
  jobs: JobRef[];         // newest first; at most one on UiPath
  details: UiPathItemDetails | ApexItemDetails;           // discriminated by `orchestrator`
}

interface QueueCore   { orchestrator; id; name; processId: string | null; active: boolean; details }
interface ProcessCore { orchestrator; id; name; package: string | null; version: string | null; active: boolean; details }
interface TriggerCore { orchestrator; id; name; kind: "queue" | "time"; processId; queueId: string | null;
                        enabled: boolean; cron: string | null; details }
```

- **Job state labels.** Apex → label uses the bundle map verbatim:
  `queued→Pending, claimed→Claimed, running→Running, completed→Successful, failed→Faulted, cancelled→Stopped, cancelling→Stopping, lost→Lost, timed_out→Timed Out, suspended→Suspended, resumed→Resumed`.
  UiPath `State` passes through unchanged.
- **Item status labels.** Apex `new→New, in_progress→InProgress, successful→Successful, failed→Failed, retried→Retried, abandoned→Abandoned, deleted→Deleted`. This is UiPath's spelling; the Apex dashboard displays "In Progress".
- **Terminal sets.**
  - UiPath: `Successful`, `Faulted`, `Stopped` (today's `actions.ts` set).
  - Apex: `completed`, `failed`, `cancelled`, `lost`, `timed_out`.
- **`fault`.**
  - UiPath: today's log-derived headline and `normalizeError` signature; `class: null`.
  - Apex: built from job **detail**. `message` = `error_message ?? outcome_label`; `class` = `outcome ?? error_class`; `signature` = `"apex:" + normalizeError(`${class}: ${message}`)`.
  - `list_jobs` rows on Apex carry only `error_class`, because list rows have no `outcome` or `exit_code`.
- **`ApexJobDetails`.** Carries `errorClass`, `outcome`, `outcomeLabel`, `exitCode`, `needsReview`, `callbackStatus`, `credentialName`, `matchValue` (PHI-bearing, so it's included only where UiPath already returns equivalent data), `source`, `artifactPolicy`, `effectiveTimeoutSeconds`, `neverStarted` (terminal + `started_at === null`), and `result` (`result_payload`). `input_payload` is returned with `token` and `callbackContext` stripped, as UiPath does today.

## 6. Config and auth

New optional section; UiPath and Copilot sections are unchanged:

```jsonc
"apex": {
  "apiUrl": "https://orchestrator.apexmedicalai.com",       // LB host, never a CloudFront host (8 KB body cap)
  "username": "…", "password": "…",                          // the user's own login: Apex audits writes per user
  "folderByEnv": { "prod": "Authorization", "pre_prod": "Authorization Test" },  // optional; these are the defaults
  "serverUrlByEnv": { "pre_prod": "https://…" },             // write guard: specific_content.serverURL must equal this
  "queueUrl": "https://sqs.…"                                // write guard: non-empty specific_content.queueUrl must equal this
}
```

- **Field precedence (D8).** `apiUrl` ?? `$APEX_API_URL`; `username` ?? `$APEX_USER_NAME`; `password` ?? `$APEX_PASSWORD`.
  - Numbered `APEX_API_URL_<n>` and `$APEX_URL` are not read.
  - The env fallback only works when the host passes its environment through, as CLI hosts and `bunx` do. MCPB installs only pass `COPILOT_MCP_CONFIG`, so they must use the file.
  - If neither source configures Apex, every Apex call fails with `ExpectedError("Apex is not configured …")`. UiPath and Copilot tools are unaffected.
- **Guard limits.** `serverUrlByEnv` and `queueUrl` mirror UiPath's keys. BE-built Apex items carry `serverURL`, an SQS `queueUrl` and `token`, all verified live. If they are unset, the guard fails closed and refuses writes. They are duplicated with the UiPath section on purpose, and are deleted along with it.
- **Token handling.** The session JWT is cached in memory only. It is single-flighted, with a TTL from its own `exp` minus 60 s. A 401 invalidates it and triggers one re-login. There is no disk cache: that's one login per MCP process against a 24 h token. If Apex login rate limits turn out to matter, the session cache pattern in `copilot/session.ts` can be adopted later.

## 7. `src/apex/client.ts` — the single Apex HTTP chokepoint

`apexRequest(env, method, path, { query?, body?, raw? })`. Nothing else in the repo calls `fetch` against Apex.

- **Folders.** `env` → folder name (`folderByEnv`) → UUID via `GET /folders?page_size=200`, cached per process. `X-Folder-Id` is sent on **every** request, and there is no unscoped variant. An explicit `folder` tool argument (a name) overrides the folder for reads only; writes ignore it.
- **Write gate.** Any method other than GET throws unless `env === "pre_prod"`. Writes never take a folder override.
- **Retries.**
  - GETs retry up to 3 times on network errors (connection resets happen live) with a short backoff.
  - A 429 honours `Retry-After` through the shared `rateLimitDelayMs`.
  - Writes are **never** retried automatically, because a retry could double-post.
- **Errors.** `ApexApiError extends ExpectedError { status, method, path }`. The message comes from `detail`, whether it's a string or a pydantic `[{loc,msg}]` list, capped at 300 characters. A 403 gets a permission hint (for example `jobs:download_artifacts`, `jobs:force_cancel`).
- **Pagination.** `page_size` is clamped to 200 (above that the server returns 422). `total_pages` is walked up to the caller's cap. Bare-array routes such as `/workflows/{id}/versions` and `/review-queue` are handled.
- **Query params.** There is no free-form passthrough. Endpoint functions take typed params limited to verified filters:
  - `/jobs`: `status`, `queue_id`, `workflow_id` (dashboard-used; count-checked before use), `created_after`, `reference`, `sort_by`, `sort_dir`.
  - `/queues/{id}/items`: `status`, `reference`, `search`.
- **Artifacts.** The listing sends the folder header, which non-default folders require. Downloads use the `download_url` from the listing with the Bearer token; a Range read gives a cheap tail.

## 8. Tool behaviour

The table lists Apex behaviour. UiPath behaviour is today's, re-shaped into the cores.

| Tool | Apex behaviour |
|---|---|
| `list_jobs(env, orchestrator, since?, top?, processName?, folder?)` | `processName` is matched as a substring against `/workflows`, then `workflow_id` filters the list. `since` → `created_after`. Rows are thin (`fault.class = error_class`) plus a note pointing to `get_job`. No N+1 detail fetches. |
| `get_job(env, orchestrator, jobId \| jobIds≤25, includeLogDigest?)` | Detail GETs run in parallel. `includeLogDigest` downloads `job.log`, parses it, and returns the digest (`byLevel`, `failures`, `stalls`) plus `run{exitCode, outcome, errorClass, timedOut, cancelled}`. If there is no log, the result is `logsAvailable:false` with a reason: `running` (live log is dashboard-only SSE), `never-started` (missing_credential reap, no artifacts) or `artifact-policy`. A digest failure is never fatal (`logDigestError`, as today). |
| `get_job_logs(env, orchestrator, jobId, minLevel?, contains?, onlyFailures?, tail?, fullMessages?, includeRawFields?, includeVideo?)` | Filters apply client-side over the parsed rows. `includeRawFields` returns the NDJSON `fields`. `includeVideo` returns `{artifact:"videos/recording.mp4", size}` and points to `get_job_artifacts`; no URL, because downloads 401 without the token. |
| **`get_job_artifacts(env, jobId, download?: string[] \| "all")`** — new, Apex-only, no `orchestrator` arg | Lists `{filename, type, size}`. Downloads the named files (or `all`) into `~/.cache/copilot-mcp/artifacts/<jobId>/` (dir mode 0700, never inside a repo) and returns the paths. Annotations: read-only (only local files change), not destructive, idempotent, open-world. |
| `list_queues(env, orchestrator, folder?)` | Core fields plus `details{autoDrain, maxConcurrentJobs, maxRetries, enforceUniqueReference}`. |
| `list_queue_items(env, orchestrator, portal \| queueId, status?, top?)` | Portal → queue named `<KEY> auth submit queue` (case-insensitive; aliases from `PORTALS`). Status is mapped from the label vocabulary. |
| `find_order_queue_items(env, orderUid, orchestrator?, folder?, since?, top?, includeSpecificContent?)` | Omitting `orchestrator` means both (D2). `folder` (a name override) is accepted only together with an explicit `orchestrator`. Apex steps: cached folder queues → `items?search=<orderUid>` per queue in parallel (concurrency 6) → exact `specific_content.orderUid` match → `/jobs?reference=<item.reference>` for each item gives `jobs[]`; `since` is applied client-side. Output: `items[]` plus `jobIds: {uipath: […], apex: […]}`, ready for `get_job`. Each backend fails independently into `searchErrors[orchestrator]`. |
| `pull_queue_item(env, orchestrator, …)` | UiPath: URL or `txn`, as today. Apex: `{queueId, itemId}` from list or find output. IsApproved is forced false. |
| `list_processes(env, orchestrator, folder?)` | `/workflows` → core fields plus `details{timeoutSeconds, maxAttempts, backoffStrategy, artifactPolicy}`. |
| `list_triggers(env, orchestrator, queueId?)` | Synthesized. Each queue becomes `kind:"queue"` with `enabled = is_active && auto_drain` and `details.maxConcurrent = max_concurrent_jobs`, the equivalent of a UiPath queue trigger and `maxJobsForActivation`. Cron `/schedules` become `kind:"time"`. `/triggers` returns 404 on Apex. |
| `find_stuck_orders(env, profile, …, crossCheck?)` | `crossCheckUipath` becomes `crossCheck` (both backends), and the `uipath` output field becomes `orchestrator{backend, verdict, …}`. Verdicts use the latest attempt. One new verdict, `job-never-started`, covers a failed job with `started_at === null` (missing_credential). |
| `build_faulted_job_issue(env, orchestrator, jobId, repo?, labels?)` | Apex: default labels `apex-fault`, `bug`; title `[Apex Fault] <class>: <message> (<env>)`. The body has process@version, the attempt chain, outcome/error_class/exit_code, the last non-INFO log rows (capped at 4000 characters), and a dashboard link. Signature per D7. UiPath output is unchanged. It still posts nothing. |
| `build_queue_item(env, profile, orderUid, orchestrator)` | One canonical native object, then a per-backend serializer. UiPath stringifies the array and object fields; its output must stay deep-equal to v1.33.1. Apex keeps the native types (§9). Apex output: `{queueName, queueId, body:{specific_content, reference, match_value}}`. |
| `add_queue_item(env:"pre_prod", orchestrator, …)` | Guard, then `POST /queues/{id}/items {specific_content, reference, match_value}`. `match_value` = `specific_content.automationId` (the live `match_key` is `automationId`). Priority is the queue default. |
| `delete_queue_item(env:"pre_prod", orchestrator, …)` | Fetch first, refuse unless `new`, then `POST …/items/{id}:delete`. The dashboard also allows deleting failed and abandoned items, but parity and evidence preservation keep the New-only rule. |
| `start_job(env:"pre_prod", orchestrator, processId, input?, matchValue?, jobsCount?)` | Apex: one `POST /jobs {workflow_id, folder_id, match_value, input_payload}` per count (1–5). `matchValue` is required. The UiPath pin-race warning is replaced by "the job runs the Process's `current_version` at dispatch". |
| `stop_job(env:"pre_prod", orchestrator, jobId, strategy?)` | Fetch first and refuse terminal states, then `SoftStop` → `POST /jobs/{id}/cancel {force:false}` or `Kill` → `POST /jobs/{id}/force_cancel`. A 403 becomes a permission error. |

## 9. Apex payload shape (`build_queue_item` / `pull_queue_item` / `add_queue_item`)

BE-built Apex items are the reference shape (verified live). These fields are native:

- **Arrays:** `Units`, `Diagnoses`, `Modifiers`, `Treatments`, `ServiceCode`, `DescriptionOfService`.
- **Booleans:** `IsRetro`, `IsUrgent`, `IsApproved`, `isSpeciality`, `addManualFacility`, `IsSameAsRequestingProvider`.
- **Numbers:** `retryCount`, `ReferredToCompleteReferralsCount`, `ReferredToSuccessfulReferralsCount`.
- **Object:** `callbackContext`.
- **Nulls allowed:** `OpenReferralInfo`, `eligibilityOtherPatientName`, `ReferredToReferralDistribution`.

MCP-built Apex items use reference `${orderUid}-${epoch}`. Queues have `enforce_unique_reference:false`, and this format keeps them distinguishable from the BE's 10-digit references. `guardQueueItemSafety` runs unchanged on the native object: it forces `IsApproved=false`, checks that `serverURL` and `queueUrl` match the configured pre-prod values, and rejects `<TO-FILL>`.

## 10. Invariants (to add to CLAUDE.md)

- **Apex has one HTTP chokepoint and always scopes to a folder.** `src/apex/client.ts` `apexRequest` is the only Apex `fetch`. It always sends `X-Folder-Id` and sends only verified query params. It refuses any request other than GET unless `env === "pre_prod"`.
- **Apex writes are test-folder-only and guarded.** This mirrors the UiPath dev-clone invariant: schema `z.literal("pre_prod")`, domain assert, the chokepoint gate, `guardQueueItemSafety`, and fetch-first refusal (delete only `new`; stop refuses terminal states). `src/apex/actions.ts` is the only module that mutates Apex.
- **`orchestrator` is required on job- and queue-level tools, never defaulted.** The carve-outs are `find_order_queue_items` and `find_stuck_orders`, where omitted means both, and the Apex-only `get_job_artifacts`.
- **The MCP never deploys, pins, creates Processes or queues, or touches Apex credentials, assets or Action Center.**

## 11. Resources, prompts, doctor, docs

- `copilot://reference/uipath-folders` becomes `copilot://reference/orchestrators`. It holds, per backend and per env, the folder name and ids, whether the folder makes real BE calls, and the write rule. It also includes the state label maps, the terminal sets, and three behaviour notes: many jobs per transaction, Apex logs exist only after the run and only per `artifact_policy`, and never-started jobs.
- `safety-rules` and `config-guide` get Apex entries.
- The prompt `report-faulted-uipath-jobs` becomes `report-faulted-jobs`, with an `orchestrator` argument. Other prompts that call the changed tools are updated.
- `doctor` adds an Apex check: which config source was used (file or env), that login succeeds, and that both folders resolve to ids.
- The `server.ts` header comment's tool list is re-synced; it is currently missing 7 tools.
- Also updated: README, CLAUDE.md (architecture plus §10 invariants), `copilot-mcp.config.example.json`, CHANGELOG, and the version in all three places (package.json, manifest.json, `server.ts` `VERSION`).

## 12. Testing

These are permanent `bun test` suites placed beside each module, in the existing fetch-stub style. Each row lists what fails if the logic breaks.

| Suite | Pins |
|---|---|
| `apex/job-log.test.ts` | Every line kind, using masked fixtures: header; `[apex-adapter]`; NDJSON INFO/WARN/ERROR with and without `fields`; plain; traceback; `[apex-adapter] result=`; footer. Timestamp inheritance for untimed lines. Footer and result parsing. |
| `orchestrator/types` state tests | Every Apex job and item status maps to its label. Terminal sets per backend. The UiPath terminal set is unchanged. |
| `apex/client.test.ts` | JWT taken from Set-Cookie. Single-flight login. Exactly one re-login on 401. `X-Folder-Id` present on every request. A non-GET on `prod` is refused before `fetch`. GETs are retried on reset, POSTs are not. `detail` as a string and as a list. `page_size` clamp. |
| `apex/actions.test.ts` | Delete refuses anything but `new`. Stop refuses each terminal state, including `lost` and `timed_out`. The guard is applied. Prod is refused at the domain layer. |
| Serializers | UiPath `build_queue_item` output is deep-equal to v1.33.1, via the existing tests. The Apex serializer's field types match the §9 map. |
| Cross-backend | `find_order_queue_items` merges both backends, isolates one backend's failure, and matches orderUid exactly. `find_stuck_orders` uses the latest attempt and emits `job-never-started`. |
| Faults | Existing UiPath signature tests are unchanged. Apex signatures are `apex:`-prefixed and stable across GUIDs and timestamps. |
| `server.test.ts` | The exact tool set (adds `get_job_artifacts`), `WRITE_TOOLS`, all four annotation hints, and schema accept/reject cases for `orchestrator`. |

Per repo guidance, tests that only pin wording or incidental behaviour get deleted when they are touched.

## 13. Live verification (before calling it done)

- **Apex reads (GET-only):**
  - `doctor`.
  - `find_order_queue_items` on a pre-prod order known to have several Apex items.
  - `get_job` with a digest on a failed job and on a never-started job.
  - `get_job_logs` filters.
  - `get_job_artifacts` downloading `job.log` and `trace.zip`.
  - `list_queues`, `list_queue_items`, `list_processes` and `list_triggers` on both folders.
  - Count checks that the `workflow_id` (`/jobs`) and `status` (items) filters actually filter.
- **UiPath regression:** the same reads on `prod` and `pre_prod`; the `state` values must be identical to v1.33.1.
- **Apex writes:**
  - `add_queue_item` then `delete_queue_item` on the **inactive** `CenCal shared-adapter test queue` in `Authorization Test`. It is `is_active:false`, so nothing dispatches and the item stays `New`.
  - `start_job` and `stop_job` cause a real (dry-run) portal login. They run only with the user's explicit go-ahead at the time, on an order the user picks, following the shared-credential preflight in the managed skill `trace-preprod-be-submit-to-apex-queue`.

## 14. Phases (each ends green on `bun run typecheck`, `bun test`, `bunx biome check src`)

1. Create `src/orchestrator/` (contract and moved generic modules) and put UiPath behind `OrchestratorBackend`, with the `orchestrator` arg and the D9 renames. UiPath behaviour is unchanged apart from argument names and output shape.
2. Apex config, `client.ts`, and the `doctor` check.
3. Apex reads, `job-log.ts`, and `get_job_artifacts`.
4. Cross-backend tools: `find_order_queue_items`, `find_stuck_orders`, `build_faulted_job_issue`, `build_queue_item` and `pull_queue_item` serializers.
5. Apex writes.
6. Resources, prompts, docs, CHANGELOG, the v2.0.0 version bump, then the live verification in §13.

## 15. Risks and still-unverified items

- **Unverified behaviour:**
  - `/jobs?workflow_id=` and the items `status` filter (count-check them in phase 3).
  - Apex 429 behaviour (none observed).
  - The `:delete` server-side preconditions (the client gate stays strict anyway).
  - Whether `force_cancel` is permitted for a normal user (it surfaces as a 403 if not).
- **PHI.**
  - `job.log` bot lines redact member fields per the adapter README. Adapter lines, `result_payload`, `input_payload` and artifacts (screenshots, `trace.zip`) should still be treated as PHI.
  - The MCP returns logs and results to the agent exactly as it does for UiPath today, and never writes artifacts inside a repo.
- **Folder-only boundary.** Prod and test folders share one login, so a bug that drops the gate could write to prod. §10 puts the gate at the chokepoint, with a test that runs before any `fetch` happens.
- **Breaking release.** Tool argument names and output shapes change (v2.0.0). Skills that call the old names (for example managed skills referencing `find_order_queue_items(env: pre_prod)` or `jobKey`) keep working only after they are updated.

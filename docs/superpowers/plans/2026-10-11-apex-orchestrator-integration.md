# Apex Orchestrator Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One orchestrator tool surface in copilot-mcp backed by UiPath and Apex, with pre-prod-only Apex writes, released as v2.0.0.

**Architecture:** `src/orchestrator/` holds the backend-neutral contract: the normalized cores, the `OrchestratorBackend` interface, the generic log/fault/safety code moved out of `src/uipath/`, and the cross-backend tools. `src/uipath/` and `src/apex/` each implement the interface, and `server.ts` dispatches every orchestrator tool through `backendFor(orchestrator)`. Apex HTTP goes through a single chokepoint (`src/apex/client.ts`). That chokepoint always sends `X-Folder-Id`, sends only query filters verified to work, and refuses any non-GET unless `env === "pre_prod"`. Retiring UiPath means deleting `src/uipath/`, its registry entry and its enum value.

**Tech Stack:** Bun 1.4 (runtime + `bun test`), TypeScript 7 via `tsc --noEmit`. The strict flags are `exactOptionalPropertyTypes`, `noUncheckedIndexedAccess`, `noPropertyAccessFromIndexSignature`, `verbatimModuleSyntax` and `noUnusedLocals/Parameters`. Also zod 3, `@modelcontextprotocol/sdk`, Biome, and lefthook (pre-commit runs biome + typecheck).

**Spec:** `docs/superpowers/specs/2026-10-11-apex-orchestrator-integration-design.md` (approved 2026-10-11). Appendix: `docs/superpowers/specs/2026-10-11-apex-orchestrator-divergences.md`. Read both before starting; this plan argues from them.

## Global Constraints

- **Baseline** before Task 1: `bun run typecheck`, `bun test` (397 pass across 30 files) and `bunx biome check src` all green on `feat/apex-orchestrator` @ `2b69b8f`.
- **Every commit passes lefthook pre-commit** (`bunx biome check` on staged files + `bun run typecheck`), so a task commits only after its code compiles. End-of-task gate: `bun run typecheck && bun test && bunx biome check src`.
- **stdout is the JSON-RPC channel:** no `console.log` in runtime code.
- **Tool conventions:** tools return via `ok()` / `toolError(name, e, VERSION)` and declare all four annotation hints. `server.test.ts` `EXPECTED`, `WRITE_TOOLS`, `CLOSED_WORLD_TOOLS` and the schema cases must match the registered tools exactly.
- **`env` is always required.** `env` (`prod` | `pre_prod`) is required on every orchestrator tool and never defaulted.
- **`orchestrator` is required on job- and queue-level tools.** The carve-outs are:
  - `find_order_queue_items` and `find_stuck_orders`: omitted means every registered backend.
  - `get_job_artifacts`: Apex-only, takes no `orchestrator` argument.
- **Writes have three layers:** schema `env: z.literal("pre_prod")`, a domain assert, and (Apex) the chokepoint gate before any `fetch`. Prod is never written.
- **Apex folders:** `prod` → `Authorization`, `pre_prod` → `Authorization Test`, both overridable by `apex.folderByEnv`. Apex config is read from the file first, then `APEX_API_URL` / `APEX_USER_NAME` / `APEX_PASSWORD`. The numbered `APEX_API_URL_<n>` and `$APEX_URL` are never read.
- **Apex query filters are limited to the verified set:**
  - `/jobs`: `status`, `queue_id`, `workflow_id`, `created_after`, `reference`, `sort_by`, `sort_dir`.
  - `/queues/{id}/items`: `status`, `reference`, `search`.
  - `page_size` is at most 200.
- **UiPath fault signatures stay byte-identical.** Apex signatures are prefixed with `apex:`.
- **Clean cutover:** migrate every caller, delete moved and obsolete code, no re-export shims, aliases or deprecated parameters.
- **Errors:** throw typed errors (`ExpectedError` subclasses, `UiPathApiError`, `ApexApiError`) and never swallow them. Best-effort attachments surface as `*Error` string fields.
- **Tests** use `bun:test` beside the module, with fetch stubbed and restored per test, and synthetic PHI-free data. They must catch real behaviour, never wiring or wording.
- **PHI and secrets:**
  - Never print tokens or member values.
  - Apex artifacts are written only under `~/.cache/copilot-mcp/artifacts/<jobId>/` (mode 0700), never inside a repo.
  - Live probes outside Task 20 are GET-only.
- **Commits** use conventional messages with a scope in the repo's style (`refactor(orchestrator): …`, `feat(apex): …`, `feat(orchestrator)!: …` for breaking tool shapes), and the body explains why.
- **Version 2.0.0** goes in `package.json`, `manifest.json` and `src/server.ts` `VERSION` (Task 19 only).

## File Structure

| Path | Status | Responsibility (task) |
|---|---|---|
| `src/orchestrator/types.ts` | new | Cores, `Orchestrator`, `ORCHESTRATORS`, `JobLog`, `PortalRef`, `QUEUE_ITEM_STATUSES` (T1; T17 adds `"apex"`) |
| `src/orchestrator/backend.ts` | new | `OrchestratorBackend` and every arg/result type (T1) |
| `src/orchestrator/log-digest.ts` | moved | Generic digest: collapse, stalls, failure cap (T1) |
| `src/orchestrator/log-semantics.ts` | moved | `isFailureLog` (T1) |
| `src/orchestrator/faults.ts` | new | `normalizeError`, `FaultedJobIssue`, `DEFAULT_REPO`, `firstLine`, `faultSearchQuery`, `signatureMarker` (T1) |
| `src/orchestrator/safety.ts` | moved | `guardQueueItemSafety` (T1) |
| `src/orchestrator/portal.ts` | new | `memberFilename`, `toPortalRef` (T3) |
| `src/orchestrator/queue-item.ts` | new | Canonical Copilot-order → native queue-item content (T4) |
| `src/orchestrator/registry.ts` | new | `BACKENDS`, `backendFor` (T5; T17 registers apex) |
| `src/orchestrator/find-order.ts` | new | Cross-backend `find_order_queue_items` (T13) |
| `src/shared/http.ts` | new | `sleep`, `rateLimitDelayMs` (T1) |
| `src/uipath/map.ts` | new | Pure UiPath → core mappers, `UIPATH_TERMINAL_STATES` (T2) |
| `src/uipath/backend.ts` | new | UiPath read functions (T3); writes, payload and the assembled `uipathBackend` (T4) |
| `src/uipath/payload.ts` | new | UiPath queue-item serializer (T4) |
| `src/uipath/queue-item.ts` | deleted | Split into `orchestrator/queue-item.ts` and `uipath/payload.ts` (T4) |
| `src/uipath/faults.ts` | trimmed | UiPath-only fault and issue formatting (T1, T5) |
| `src/apex/errors.ts` | new | `ApexNotConfiguredError` (T6), `ApexApiError` (T7) |
| `src/apex/client.ts` | new | The single Apex HTTP chokepoint (T7) |
| `src/apex/job-log.ts` | new | Pure `job.log` parser (T9) |
| `src/apex/api.ts` | new | Typed endpoint functions and raw row types (T10) |
| `src/apex/map.ts` | new | Pure Apex → core mappers, label maps, terminal set, fault (T10) |
| `src/apex/backend.ts` | new | Apex read functions (T11), fault issue (T15), payload (T16), assembled `apexBackend` (T17) |
| `src/apex/artifacts.ts` | new | `get_job_artifacts` logic (T12) |
| `src/apex/faults.ts` | new | Apex issue formatter (T15) |
| `src/apex/payload.ts` | new | Apex queue-item serializer (T16) |
| `src/apex/actions.ts` | new | Apex writes; the only module that mutates Apex (T17) |
| `src/config/config.ts` | modified | `apex` section and `resolveApex()` (T6) |
| `src/copilot/sweep.ts` | modified | Cross-backend cross-check, `job-never-started` (T14) |
| `src/copilot/doctor.ts` | modified | Apex checks (T8) |
| `src/mcp/reference.ts` | modified | `orchestrators` resource, safety rules, config guide (T18) |
| `src/mcp/prompts.ts` | modified | Arg renames (T5), `report-faulted-jobs` (T15), crossCheck (T14) |
| `src/server.ts` | modified | Thin dispatch (T5); `get_job_artifacts` (T12); find-order (T13); stuck orders (T14); apex descriptions (T17); resources and instructions (T18); version (T19) |

## Task order and why it is safe

Phase 1 (Tasks 1–5) puts UiPath behind the interface while preserving behaviour, apart from the D9 renames and the
normalized output shape. Phase 2 (Tasks 6–8) adds Apex config, the client and doctor. Phase 3 (Tasks 9–12) adds Apex reads as
unit-tested functions plus the Apex-only `get_job_artifacts` tool.

Phase 4 (Tasks 13–16) makes `find_order_queue_items`
and `find_stuck_orders` iterate `BACKENDS`, and adds the Apex fault issue and payload serializer. Phase 5 (Task 17) adds Apex writes, assembles `apexBackend`, and only then adds `"apex"` to `ORCHESTRATORS`, which exposes Apex on every
tool at once. Phase 6 (Tasks 18–20) covers resources, docs, release and live verification.

A few states are deliberately transient between tasks:
- After Task 3, the old handler logic still lives in `server.ts`; Task 5 deletes it.
- After Task 5, `find_order_queue_items` has `orchestrator` required; Task 13 relaxes it.
- `find_stuck_orders` stays UiPath-direct until Task 14.
- The prompt keeps its old name until Task 15.

---

## Phase 1: Contract + UiPath behind the interface

> Executor notes (Phase 1):
> - Every file/step below was applied in a scratch copy of the repo and the Task-end gate was run after each task: Task 1 → 407 tests, Task 2 → 433, Task 3 → 472 pass (baseline 397); `bun run typecheck` and `bunx biome check src` clean after each. Code blocks are the verified final text (post `biome check --write`).
> - **Task 1 also owns** the `ExpectedError` move (`src/shared/expected-error.ts`) and `ORCHESTRATORS = ["uipath", "apex"]` (see Global Constraints).
> - Extra exports beyond the contract table, all in `src/uipath/map.ts`: `toUiPathLogDigest(logs)` (digest + transactionOutcome; Task 3 uses it), `UiPathQueueDetails`, `UiPathProcessDetails`, `UiPathTriggerDetails`. `toQueueItemCoreFromItem(item, { queueName, specificContent })` and `toQueueItemCoreFromMatch(m, { queueName })` fix the `opts` shapes the contract left open. `EXCEPTION_TYPE` is exported from `src/uipath/faults.ts` (Task 2) so `uipathFault` doesn't call the old `extractFault`.
> - Behavior decisions made in Task 3 (accepted by Main): `list_queue_items` keeps the member triage columns as `details.memberId`/`details.member` (`specificContent: null` is the PHI-light projection); `get_job` with `includeLogDigest` whose logs fetch fails returns `fault: null` + `logDigestError` (old code synthesized a state-only fault from zero logs); `get_job_logs` `includeVideo` with no recording omits `video` (old: `videoUrl: ""`); `find_order` with `includeSpecificContent:false` gives `specificContent: null` (old: `{}`).
> - `src/uipath/backend.ts` is ~370 lines after Task 3 (one responsibility: the UiPath read surface; Task 4 appends the writes + assembly). Split only if you want to enforce the ≲200-line guideline — nothing else references its internals.


### Task 1: Contract types + generic module moves

Pure refactor: no behavior change. The one deliberate JSON difference: `get_job`'s `logDigest` object now serializes `transactionOutcome` after `note` instead of before it (same keys and values; `server.ts` spreads it in at its call site until Task 5 replaces that handler).

**Files:**
- Create: `src/orchestrator/types.ts`, `src/orchestrator/backend.ts` (verbatim contract), `src/orchestrator/faults.ts`, `src/orchestrator/faults.test.ts`, `src/shared/http.ts`, `src/shared/http.test.ts`, `src/shared/expected-error.ts`
- Move (`git mv`): `src/uipath/safety.ts` → `src/orchestrator/safety.ts` (+ `.test.ts`, unchanged bytes); `src/uipath/log-semantics.ts` → `src/orchestrator/log-semantics.ts` (+ `.test.ts`); `src/uipath/log-digest.ts` → `src/orchestrator/log-digest.ts` (generic part only, rewritten below); `src/uipath/log-digest.test.ts` → `src/orchestrator/log-digest.test.ts` (generic tests only)
- Modify: `src/uipath/faults.ts` (hunks A–G below), `src/uipath/faults.test.ts`, `src/uipath/uipath.ts`, `src/uipath/raw-message.ts`, `src/uipath/raw-message.test.ts`, `src/uipath/uipath.test.ts`, `src/uipath/queue.ts`, `src/uipath/queue-item.ts`, `src/uipath/actions.ts`, `src/uipath/errors.ts`, `src/uipath/errors.test.ts`, `src/copilot/settings/sync-actions.ts`, `src/copilot/settings/sync-actions.test.ts`, `src/mcp/feedback.ts`, `src/mcp/feedback.test.ts`, `src/server.ts` (imports + 3 lines)
- Delete: nothing outright (the three `uipath/` moves are renames)

**Interfaces:**
- Consumes: the contract's `types.ts`/`backend.ts` text; existing `normalizeError`, `topError`, `extractFault`, `extractTransactionOutcome`, `digestLogs`, `guardQueueItemSafety`.
- Produces: `src/orchestrator/types.ts` (all cores, `ORCHESTRATORS = ["uipath", "apex"]`, `JobLog`, `JobFault`, `PortalRef`), `src/orchestrator/backend.ts` (`OrchestratorBackend` + arg/result types), `orchestrator/log-digest.ts` (`truncate`, `MESSAGE_CAP`, `CollapsedLog`, `LogStall`, `JobLogDigest` without `transactionOutcome`, `collapseLogs`, `findLogStalls`, `digestLogs`), `orchestrator/faults.ts` (`DEFAULT_REPO`, `FaultedJobIssue`, `firstLine`, `normalizeError`, `signatureMarker`, `faultSearchQuery`), `orchestrator/safety.ts` + `orchestrator/log-semantics.ts` (moved), `shared/http.ts` (`sleep`, `rateLimitDelayMs`, `MAX_RATE_LIMIT_RETRIES`, `DEFAULT_RATE_LIMIT_RETRY_MS`, `MAX_RATE_LIMIT_RETRY_MS`), `shared/expected-error.ts` (`ExpectedError`), `UiPathLogRow extends JobLog { RawMessage?: string }` in `uipath/uipath.ts`. Still in `uipath/faults.ts` after this task: `DEFAULT_LABELS`, `FormatOptions`, `BuildOptions`, `topError`, `formatFaultedJobIssue`, `buildFaultedJobIssue`, `TransactionOutcome`, `extractTransactionOutcome`, and the OLD `JobFault` + `extractFault` (Task 5 deletes the last two together with the `server.ts` handler that calls them).

Pure refactor → no failing-test steps. Behavior is pinned by the existing tests (they must stay green unchanged except for moved import lines): `src/uipath/auth.test.ts` (429 `Retry-After` retry + give-up-after-5 paths exercise `sleep`/`rateLimitDelayMs`/`MAX_RATE_LIMIT_RETRIES` through `uipathRequest`), `src/uipath/uipath.test.ts` (`refineJobLogs`, `jobLogQueryParams`, which use `isFailureLog`), `src/uipath/raw-message.test.ts`, `src/uipath/faults.test.ts` (`formatFaultedJobIssue` title/body/searchQuery strings are asserted byte-for-byte, which pins `signatureMarker`/`faultSearchQuery`; `buildFaultedJobIssue`), `src/uipath/actions.test.ts`, `src/uipath/queue.test.ts`, `src/orchestrator/safety.test.ts`, `src/orchestrator/log-semantics.test.ts`, `src/orchestrator/log-digest.test.ts` (moved generic cases), `src/mcp/feedback.test.ts` + `src/uipath/errors.test.ts` + `src/copilot/settings/sync-actions.test.ts` (`ExpectedError` identity/classification), `src/server.test.ts`.

- [ ] **Step 1: Move files with git (history-preserving)**
```bash
mkdir -p src/orchestrator
git mv src/uipath/safety.ts src/orchestrator/safety.ts
git mv src/uipath/safety.test.ts src/orchestrator/safety.test.ts
git mv src/uipath/log-semantics.ts src/orchestrator/log-semantics.ts
git mv src/uipath/log-semantics.test.ts src/orchestrator/log-semantics.test.ts
git mv src/uipath/log-digest.ts src/orchestrator/log-digest.ts
git mv src/uipath/log-digest.test.ts src/orchestrator/log-digest.test.ts
```
`safety.ts` and `safety.test.ts` need no content change (no imports of moved modules; the test imports `./safety.js`, which now resolves beside it).

- [ ] **Step 2: Create `src/orchestrator/types.ts` (verbatim contract) and `src/orchestrator/backend.ts` (verbatim contract)**

`src/orchestrator/types.ts`:
```ts
// Backend-neutral orchestrator contract: the normalized shapes every orchestrator tool
// returns, whichever backend served the call. Backend-specific fields live under each
// core's `details`, never at the top level, so agents read one vocabulary across backends.

export const ORCHESTRATORS = ["uipath", "apex"] as const;
export type Orchestrator = (typeof ORCHESTRATORS)[number];

// One log row as the shared digest/filter code reads it. Backends extend it
// (UiPath adds RawMessage, Apex adds fields); shared code reads only these three.
export interface JobLog {
  Level: string;
  Message: string;
  TimeStamp: string;
}

// `class` is the backend's failure classification: UiPath = the exception type parsed
// from the headline error; Apex = the Process outcome, else error_class. null fields
// mean "not available from this read", never "no fault".
export interface JobFault {
  message: string | null;
  signature: string | null;
  class: string | null;
}

// One execution attempt of a transaction, as known from the queue-item read alone.
// state/neverStarted are null when the backend can't tell without fetching the job.
export interface JobAttempt {
  orchestrator: Orchestrator;
  id: string;
  state: string | null;
  neverStarted: boolean | null;
}

export interface JobCore<D extends object = object> {
  orchestrator: Orchestrator;
  id: string; // UiPath job Key (GUID) | Apex job uuid
  state: string; // dashboard label (UiPath State vocabulary)
  rawState: string; // the backend's own status string, verbatim
  terminal: boolean;
  process: { id: string | null; name: string; version: string | null };
  queueItem: { queueId: string; itemId: string; reference: string } | null;
  createdAt: string;
  startedAt: string | null;
  endedAt: string | null;
  durationMs: number | null;
  attempt: number | null;
  retryOfJobId: string | null;
  fault: JobFault | null;
  link: string; // dashboard deep link ("" when unknown)
  details: D;
}

// Queue-item statuses in UiPath's spelling; Apex statuses are mapped onto these.
export const QUEUE_ITEM_STATUSES = [
  "New",
  "InProgress",
  "Successful",
  "Failed",
  "Retried",
  "Abandoned",
  "Deleted",
] as const;
export type QueueItemStatus = (typeof QUEUE_ITEM_STATUSES)[number];

export interface QueueItemCore<D extends object = object> {
  orchestrator: Orchestrator;
  queueId: string;
  queueName: string;
  itemId: string;
  reference: string;
  orderUid: string | null;
  status: string; // QueueItemStatus spelling when the backend value maps, else verbatim
  rawStatus: string;
  exceptionType: "business" | "application" | null;
  createdAt: string;
  endedAt: string | null;
  jobs: JobAttempt[]; // newest first; at most one on UiPath
  specificContent: Record<string, unknown> | null; // null = not part of this projection
  details: D;
}

export interface QueueCore<D extends object = object> {
  orchestrator: Orchestrator;
  id: string;
  name: string;
  processId: string | null;
  active: boolean;
  details: D;
}

export interface ProcessCore<D extends object = object> {
  orchestrator: Orchestrator;
  id: string; // what start_job takes as processId (UiPath release Key | Apex workflow uuid)
  name: string;
  package: string | null;
  version: string | null;
  active: boolean;
  details: D;
}

export interface TriggerCore<D extends object = object> {
  orchestrator: Orchestrator;
  id: string;
  name: string;
  kind: "queue" | "time";
  processId: string | null;
  queueId: string | null;
  enabled: boolean;
  cron: string | null;
  details: D;
}

// A portal reference exposed on a pull result. matched=false carries empty sentinels.
export interface PortalRef {
  matched: boolean;
  key: string;
  portalDir: string;
  buildArtifact: string;
  family: string;
}
```

`src/orchestrator/backend.ts`:
```ts
// The one interface every orchestrator backend implements. server.ts dispatches each
// orchestrator tool through backendFor(orchestrator) (registry.ts) and wraps the result
// as { orchestrator, env, ...result }, so backends never shape tool envelopes themselves.

import type { Env } from "../config/config.js";
import type { StepProgress } from "../shared/util.js";
import type { FaultedJobIssue } from "./faults.js";
import type { JobLogDigest } from "./log-digest.js";
import type {
  JobCore,
  Orchestrator,
  PortalRef,
  ProcessCore,
  QueueCore,
  QueueItemCore,
  QueueItemStatus,
  TriggerCore,
} from "./types.js";

export interface ListJobsArgs {
  env: Env;
  folder?: string | undefined;
  since?: string | undefined;
  top: number;
  processName?: string | undefined;
}
export interface ListJobsResult {
  folder: string;
  jobs: JobCore[];
}

export type LogsUnavailableReason = "running" | "never-started" | "artifact-policy";
export interface LogsUnavailable {
  reason: LogsUnavailableReason;
  detail: string;
}

// batch=false: a lookup failure of the single id throws (fails the tool call);
// batch=true: it lands on that entry's `error` and the rest still return.
export interface GetJobsArgs {
  env: Env;
  jobIds: string[];
  batch: boolean;
  folder?: string | undefined;
  includeLogDigest: boolean;
}
export interface JobEntry {
  id: string;
  found: boolean;
  job?: JobCore;
  logDigest?: JobLogDigest; // backends return a subtype (UiPath +transactionOutcome, Apex +run)
  logsUnavailable?: LogsUnavailable;
  logDigestError?: string;
  error?: string;
}
export interface GetJobsResult {
  folder: string;
  entries: JobEntry[];
}

export interface LogFilter {
  minLevel?: "warn" | "error" | undefined;
  contains?: string | undefined;
  onlyFailures: boolean;
  tail?: number | undefined;
}
export interface GetJobLogsArgs {
  env: Env;
  jobIds: string[];
  batch: boolean;
  folder?: string | undefined;
  filter: LogFilter;
  fullMessages: boolean;
  includeRawFields: boolean;
  includeVideo: boolean;
}
export interface LogRow {
  Level: string;
  Message: string;
  TimeStamp: string;
  raw?: object | null; // only when includeRawFields: UiPath RawMessage fields | Apex NDJSON `fields`
}
export type VideoRef =
  | { kind: "url"; url: string }
  | { kind: "artifact"; artifact: string; sizeBytes: number; fetchWith: "get_job_artifacts" };
export interface JobLogsEntry {
  jobId: string;
  logs: LogRow[];
  returned: number;
  totalMatching?: number;
  truncated: boolean;
  logsUnavailable?: LogsUnavailable;
  error?: string;
  video?: VideoRef;
  videoError?: string;
}
export interface GetJobLogsResult {
  folder: string;
  entries: JobLogsEntry[];
}

export interface ListQueuesArgs {
  env: Env;
  folder?: string | undefined;
  nameContains?: string | undefined;
  top: number;
}

export type QueueKind = "submit" | "sync";
// `queue` is a portal key/alias (e.g. "PMG") or the backend's own queue id.
export interface ListQueueItemsArgs {
  env: Env;
  queue: string;
  queueKind: QueueKind;
  status?: QueueItemStatus | undefined;
  top: number;
}
export interface ListQueueItemsResult {
  queueId: string;
  queueName: string;
  items: QueueItemCore[];
}

export interface FindOrderArgs {
  env: Env;
  orderUid: string;
  folder?: string | undefined;
  since?: string | undefined;
  top: number;
  includeSpecificContent: boolean;
}
export interface FindOrderResult {
  folder: string;
  searchMode: string;
  scanned: number;
  items: QueueItemCore[]; // newest first; specificContent token value "[redacted]"
  notes: string[];
  searchError?: string;
}

// UiPath: url, or itemId (+ env, optional folderId). Apex: env + queueId + itemId.
export interface PullQueueItemArgs {
  env?: Env | undefined;
  url?: string | undefined;
  queueId?: string | undefined;
  itemId?: string | undefined;
  folderId?: string | undefined;
}
export interface PullQueueItemResult {
  env: Env;
  item: QueueItemCore; // specificContent present, IsApproved forced false
  portal: PortalRef;
  isApprovedForced: true;
  suggestedFilename: string;
  queueNameError?: string;
}

export interface ListProcessesArgs {
  env: Env;
  folder?: string | undefined;
  nameContains?: string | undefined;
  top: number;
}
export interface ListTriggersArgs {
  env: Env;
  folder?: string | undefined;
  queueId?: string | undefined;
  top: number;
}

export interface FaultIssueArgs {
  env: Env;
  jobId: string;
  folder?: string | undefined;
  repo?: string | undefined;
  labels?: string[] | undefined;
}
export type FaultedJobIssueResult = FaultedJobIssue & { found: boolean; logsError?: string };

// `content` is the canonical native-typed SpecificContent from orchestrator/queue-item.ts,
// WITHOUT serverURL/queueUrl; the backend fills those from its own config and serializes.
export interface QueueItemPayloadArgs {
  env: Env;
  queueName: string;
  orderUid: string;
  content: Record<string, unknown>;
  beBase?: string | undefined; // the Copilot BE the order was read from; UiPath serverURL fallback
}
export interface QueueItemPayload {
  payload: unknown;
  notes: string[];
  meta: Record<string, unknown>;
}

export type QueueItemPriority = "Low" | "Normal" | "High";
export interface AddQueueItemArgs {
  env: Env;
  queue: string; // UiPath: exact queue Name; Apex: queue name or uuid
  reference: string;
  specificContent: Record<string, unknown>;
  priority: QueueItemPriority; // UiPath only; Apex uses the queue's default
}
export interface AddQueueItemResult {
  env: Env;
  queueId: string;
  queueName: string;
  reference: string;
  itemId: string;
  status: string;
  forced: string[];
}
export interface DeleteQueueItemArgs {
  env: Env;
  itemId: string;
  queueId?: string | undefined; // required by Apex
}
export interface DeleteQueueItemResult {
  env: Env;
  itemId: string;
  deleted: true;
  previousStatus: string;
  reference: string;
}
export interface StartJobArgs {
  env: Env;
  processId: string;
  input: Record<string, unknown>;
  jobsCount: number;
  matchValue?: string | undefined; // required by Apex
}
export interface StartedJob {
  id: string;
  state: string;
  processName: string;
  link: string;
}
export interface StartJobResult {
  env: Env;
  jobs: StartedJob[];
}
export type StopStrategy = "SoftStop" | "Kill";
export interface StopJobArgs {
  env: Env;
  jobId: string;
  strategy: StopStrategy;
}
export interface StopJobResult {
  env: Env;
  jobId: string;
  strategy: StopStrategy;
  stopped: true;
}

export interface OrchestratorBackend {
  readonly name: Orchestrator;
  listJobs(args: ListJobsArgs): Promise<ListJobsResult>;
  getJobs(args: GetJobsArgs, onProgress?: StepProgress): Promise<GetJobsResult>;
  getJobLogs(args: GetJobLogsArgs, onProgress?: StepProgress): Promise<GetJobLogsResult>;
  listQueues(args: ListQueuesArgs): Promise<QueueCore[]>;
  listQueueItems(args: ListQueueItemsArgs): Promise<ListQueueItemsResult>;
  findOrderQueueItems(args: FindOrderArgs): Promise<FindOrderResult>;
  pullQueueItem(args: PullQueueItemArgs): Promise<PullQueueItemResult>;
  listProcesses(args: ListProcessesArgs): Promise<ProcessCore[]>;
  listTriggers(args: ListTriggersArgs): Promise<TriggerCore[]>;
  buildFaultedJobIssue(args: FaultIssueArgs): Promise<FaultedJobIssueResult>;
  buildQueueItemPayload(args: QueueItemPayloadArgs): QueueItemPayload;
  addQueueItem(args: AddQueueItemArgs): Promise<AddQueueItemResult>;
  deleteQueueItem(args: DeleteQueueItemArgs): Promise<DeleteQueueItemResult>;
  startJob(args: StartJobArgs): Promise<StartJobResult>;
  stopJob(args: StopJobArgs): Promise<StopJobResult>;
}
```


- [ ] **Step 3: Create `src/orchestrator/faults.ts` and overwrite `src/orchestrator/log-digest.ts`**

`src/orchestrator/faults.ts` (moves `DEFAULT_REPO`, `FaultedJobIssue`, `firstLine`, `normalizeError` from `src/uipath/faults.ts` lines 18-19, 22-30, 40, 42-59 verbatim; adds the two tiny helpers whose strings are byte-identical to the previously inline ones at `uipath/faults.ts:133` and `:136`):
```ts
// Backend-neutral fault helpers: the dedupe signature and the GitHub issue payload shape
// shared by every orchestrator's `build_faulted_job_issue`. The `<!-- fault-signature -->`
// marker in an issue body and the search query that finds it MUST stay in lockstep, so
// both strings are built here, once.

// Default target repo (the RPA automation project). Override per call via opts.repo.
export const DEFAULT_REPO = "Apex-Medical-AI-Inc/RPAPlaywright";

export interface FaultedJobIssue {
  repo: string;
  title: string;
  body: string;
  labels: string[];
  faultSignature: string; // stable, normalized error — the dedupe key
  searchQuery: string; // GitHub query to find an existing issue for this fault
  recurrenceComment: string; // post this on the existing issue when one is found
}

export const firstLine = (s: string): string => (s.split("\n")[0] ?? s).trim().slice(0, 120);

const GUID = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/gi;
const ISO_TS = /\d{4}-\d{2}-\d{2}[t ]\d{2}:\d{2}:\d{2}(?:\.\d+)?z?/gi;

// Collapse a raw error into a stable signature so the SAME recurring fault (a
// different job Key / ids / timestamps each run) maps to one issue. Strips GUIDs,
// ISO timestamps and long digit runs, drops quotes/backticks (they break GitHub
// search phrases), lowercases, collapses whitespace and truncates.
export function normalizeError(msg: string): string {
  return msg
    .toLowerCase()
    .replace(GUID, "<id>")
    .replace(ISO_TS, "<ts>")
    .replace(/\d{4,}/g, "<n>")
    .replace(/["`]/g, "")
    .replace(/\s+/g, " ")
    .trim()
    .slice(0, 160);
}

// Hidden marker appended to an issue body; faultSearchQuery finds it by its phrase.
export const signatureMarker = (signature: string): string =>
  `<!-- fault-signature: ${signature} -->`;

export const faultSearchQuery = (repo: string, signature: string): string =>
  `repo:${repo} is:issue is:open in:body "fault-signature: ${signature}"`;
```

`src/orchestrator/log-digest.ts` (overwrite the renamed file; `truncate`, `MESSAGE_CAP`, `CollapsedLog`, `LogStall`, `collapseLogs`, `findLogStalls`, `digestLogs` are the old lines 14-27, 61-116 and 151-177 verbatim except: `JobLogDigest` loses `transactionOutcome`, `digestLogs` loses its `transactionOutcome: extractTransactionOutcome(logs)` line, imports now come from `./faults.js`, `./log-semantics.js`, `./types.js`; the old `JobFault`, `TransactionOutcome`, `EXCEPTION_TYPE`, `extractTransactionOutcome`, `extractFault` move to `uipath/faults.ts` in Step 6):
```ts
// Condensed, failure-focused view of a job's robot logs: consecutive-duplicate
// collapsing (retry loops) and stall detection (largest timestamp gaps). Pure and
// backend-neutral (reads only Level/Message/TimeStamp) — get_job attaches the digest
// instead of the raw (up to 500-line) log dump; the complete logs stay one
// get_job_logs call away. Backends extend JobLogDigest with their own fields
// (UiPath: the queue transaction's outcome; Apex: the run summary).

import { normalizeError } from "./faults.js";
import { isFailureLog } from "./log-semantics.js";
import type { JobLog } from "./types.js";

export interface CollapsedLog {
  level: string;
  message: string; // first raw occurrence, truncated
  count: number;
  firstAt: string;
  lastAt: string;
}

export interface LogStall {
  gapMs: number;
  beforeAt: string;
  afterAt: string;
  beforeMessage: string; // what the robot logged last before going quiet
}

export interface JobLogDigest {
  fetched: number; // log rows fetched (capped at 500 upstream)
  byLevel: Record<string, number>;
  failures: CollapsedLog[]; // collapsed failure lines (error/fatal level or failure wording)
  droppedFailures: number; // collapsed failure entries cut by the cap
  stalls: LogStall[];
  note: string; // escalation pointer for the agent
}

export const MESSAGE_CAP = 400;
const FAILURE_CAP = 25;
const STALL_MIN_MS = 15_000;
const STALL_TOP = 3;

export const truncate = (s: string, cap = MESSAGE_CAP): string =>
  s.length > cap ? `${s.slice(0, cap)}…` : s;

// Consecutive logs collapse when level + normalized message match — retry loops
// repeat near-identical lines that differ only in ids/timestamps, so the collapse
// key reuses normalizeError (the fault-signature normalization).
export function collapseLogs(logs: JobLog[]): CollapsedLog[] {
  const out: CollapsedLog[] = [];
  let key = "";
  for (const log of logs) {
    const k = `${log.Level}|${normalizeError(log.Message || "")}`;
    const last = out[out.length - 1];
    if (last && k === key) {
      last.count += 1;
      last.lastAt = log.TimeStamp;
      continue;
    }
    key = k;
    out.push({
      level: log.Level,
      message: truncate(log.Message || ""),
      count: 1,
      firstAt: log.TimeStamp,
      lastAt: log.TimeStamp,
    });
  }
  return out;
}

// The largest gaps between consecutive log timestamps — where the run stalled.
// Logs must be oldest-first (the order every backend returns them).
export function findLogStalls(logs: JobLog[]): LogStall[] {
  const stalls: LogStall[] = [];
  for (let i = 1; i < logs.length; i++) {
    const prev = logs[i - 1];
    const cur = logs[i];
    if (!prev || !cur) continue;
    const a = Date.parse(prev.TimeStamp);
    const b = Date.parse(cur.TimeStamp);
    if (!Number.isFinite(a) || !Number.isFinite(b)) continue;
    const gapMs = b - a;
    if (gapMs < STALL_MIN_MS) continue;
    stalls.push({
      gapMs,
      beforeAt: prev.TimeStamp,
      afterAt: cur.TimeStamp,
      beforeMessage: truncate(prev.Message || "", 160),
    });
  }
  return stalls.sort((x, y) => y.gapMs - x.gapMs).slice(0, STALL_TOP);
}

export function digestLogs(logs: JobLog[]): JobLogDigest {
  const byLevel: Record<string, number> = {};
  for (const log of logs) {
    const level = log.Level || "Unknown";
    byLevel[level] = (byLevel[level] ?? 0) + 1;
  }
  const collapsed = collapseLogs(logs.filter(isFailureLog));
  // The cap keeps the first failure (often the root cause) and the tail (the
  // terminal fault + retry churn nearest the end).
  const head = collapsed[0];
  const failures =
    collapsed.length <= FAILURE_CAP || !head
      ? collapsed.slice(0, FAILURE_CAP)
      : [head, ...collapsed.slice(-(FAILURE_CAP - 1))];
  return {
    fetched: logs.length,
    byLevel,
    failures,
    droppedFailures: collapsed.length - failures.length,
    stalls: findLogStalls(logs),
    note:
      "Failure-focused digest (messages truncated, benign lines omitted). " +
      "If this is not enough, call get_job_logs with this job's key for the complete raw logs " +
      "(filters: minLevel, contains, onlyFailures, tail).",
  };
}
```

- [ ] **Step 4: Create `src/shared/http.ts` (moved from `src/uipath/uipath.ts` lines 97-121 verbatim, now exported) and `src/shared/expected-error.ts`**

`src/shared/http.ts`:
```ts
// HTTP helpers shared by every orchestrator client (UiPath, Apex): the pause primitive and the
// 429 Retry-After policy, so both backends back off identically.

// GET/odata/Jobs and GET/odata/QueueItems filtered queries are rate-limited by
// Orchestrator at 100 req/min/tenant for direct API usage (this server's usage —
// "non-automation" per UiPath's classification), 1000/min for the Orchestrator
// System Activities. That's exactly fetchJobByKey / searchQueueItemsByOrderId /
// listQueueItems — the calls analyze_order_execution and find_stuck_orders fan out
// concurrently per job/order. A 429 carries a `Retry-After` (seconds) telling the
// caller precisely when the window reopens
// (https://docs.uipath.com/orchestrator/automation-cloud/latest/api-guide/rate-limits#exposed-headers) —
// honor it instead of failing the whole analysis/sweep outright.
export const MAX_RATE_LIMIT_RETRIES = 5;
export const DEFAULT_RATE_LIMIT_RETRY_MS = 5_000; // used when Retry-After is missing/unparseable
export const MAX_RATE_LIMIT_RETRY_MS = 65_000; // clamp: one rate-limit window, never longer

export const sleep = (ms: number): Promise<void> => {
  const { promise, resolve } = Promise.withResolvers<void>();
  setTimeout(resolve, ms);
  return promise;
};

export function rateLimitDelayMs(retryAfterHeader: string | null): number {
  const seconds = retryAfterHeader ? Number(retryAfterHeader) : Number.NaN;
  const ms =
    Number.isFinite(seconds) && seconds >= 0 ? seconds * 1000 : DEFAULT_RATE_LIMIT_RETRY_MS;
  return Math.min(ms, MAX_RATE_LIMIT_RETRY_MS);
}
```

`src/shared/expected-error.ts` (the class moves out of `src/mcp/feedback.ts:24`; `config.ts` (Task 6) must throw `ExpectedError` subclasses and `feedback.ts` imports `config.ts`, so it has to be a leaf):
```ts
// Base marker for "known, user-actionable failure — not a bug". Throwing a subclass
// of this (or being one, like NotImplementedError) classifies as `expected` by identity
// (mcp/feedback.ts classify), independent of message wording. A leaf module so config.ts
// can throw subclasses without importing feedback.ts (which imports config.ts).
export class ExpectedError extends Error {}
```

- [ ] **Step 5: Migrate every `ExpectedError` importer** (found with `rg -n "ExpectedError" src`; `feedback.ts` stops exporting it)

| File | Old | New |
|---|---|---|
| `src/mcp/feedback.ts` | line 24 `export class ExpectedError extends Error {}`, its 3-line comment (lines 21-23) and the blank line after | deleted; add after line 15 (`import { getFeedbackConfig } …`): `import { ExpectedError } from "../shared/expected-error.js";` (`classify` at line 39 keeps using it) |
| `src/mcp/feedback.test.ts` | lines 7-14 `import { classify, ExpectedError, formatMcpIssue, issueUrl, REPO_URL, toolError } from "./feedback.js";` (multi-line) | `import { ExpectedError } from "../shared/expected-error.js";` (inserted after the `../copilot/settings/index.js` import) and `import { classify, formatMcpIssue, issueUrl, REPO_URL, toolError } from "./feedback.js";` |
| `src/copilot/settings/sync-actions.ts` line 4 | `import { ExpectedError } from "../../mcp/feedback.js";` | `import { ExpectedError } from "../../shared/expected-error.js";` |
| `src/copilot/settings/sync-actions.test.ts` line 2 | `import { ExpectedError } from "../../mcp/feedback.js";` | `import { ExpectedError } from "../../shared/expected-error.js";` |
| `src/uipath/errors.ts` line 7 | `import { ExpectedError } from "../mcp/feedback.js";` | `import { ExpectedError } from "../shared/expected-error.js";` |
| `src/uipath/errors.test.ts` line 2 | `import { ExpectedError } from "../mcp/feedback.js";` | `import { ExpectedError } from "../shared/expected-error.js";` |

(`src/server.test.ts:166` mentions the word in a comment only.) Check: `rg -n "ExpectedError" src | rg "mcp/feedback"` → no output.

- [ ] **Step 6: Edit `src/uipath/faults.ts`** — apply bottom-up (line numbers are the current file's)

Hunk G, line 173 (`buildFaultedJobIssue`):
```diff
-  let logs: JobLog[] = [];
+  let logs: UiPathLogRow[] = [];
```
Hunk F, line 136 (`formatFaultedJobIssue`):
```diff
-  const searchQuery = `repo:${repo} is:issue is:open in:body "fault-signature: ${signature}"`;
+  const searchQuery = faultSearchQuery(repo, signature);
```
Hunk E, line 133 (last element of the `body` array):
```diff
-    `<!-- fault-signature: ${signature} -->`,
+    signatureMarker(signature),
```
Hunk D, line 80 (`formatFaultedJobIssue` parameter; it needs `RawMessage` for `extractTransactionOutcome`):
```diff
-  logs: JobLog[],
+  logs: UiPathLogRow[],
```
Hunk C, insert after line 69 (end of `topError`) a blank line then (the old `log-digest.ts` lines 29-49 and 118-149, verbatim except `extractTransactionOutcome`'s parameter type `JobLog[]` → `UiPathLogRow[]`, since it reads `RawMessage`):
```ts
// A dotted .NET-style type ending in Exception/Error, e.g. System.NullReferenceException.
const EXCEPTION_TYPE = /\b(?:[A-Za-z_]\w*\.)*[A-Z]\w*(?:Exception|Error)\b/;

export interface JobFault {
  message: string; // headline error (first Error-level log, else last line, else state)
  signature: string; // normalizeError — same dedupe key as build_faulted_job_issue
  exceptionType: string | null; // e.g. System.NullReferenceException, parsed from the message
}

// The Transaction Ended row's structured fields (see raw-message.ts) — the
// queue's OWN record of how this job's transaction concluded, independent of
// (and more reliable for exception classification than) exceptionType's regex
// guess over free text. null whenever RawMessage wasn't fetched, or the job never
// reached a queue transaction end (faulted before starting one, or has no queue
// item at all).
export interface TransactionOutcome {
  transactionId: string | null;
  queueName: string | null;
  processingExceptionType: string | null;
  processingExceptionReason: string | null;
  transactionExecutionTimeSec: number | null;
  queueItemPriority: string | null;
  queueItemReviewStatus: string | null; // as logged at the time — a snapshot, not live queue-item state
}

// The queue transaction's own outcome fields, read off the "Transaction Ended"
// row (last one, if a job somehow logs more than one). Reverse-scans like
// topError — the terminal transaction is what matters, not the first.
export function extractTransactionOutcome(logs: UiPathLogRow[]): TransactionOutcome | null {
  for (const log of [...logs].reverse()) {
    const raw = parseRawMessage(log);
    if (raw?.transactionState === "Ended") {
      return {
        transactionId: raw.transactionId,
        queueName: raw.queueName,
        processingExceptionType: raw.processingExceptionType,
        processingExceptionReason: raw.processingExceptionReason,
        transactionExecutionTimeSec: raw.transactionExecutionTimeSec,
        queueItemPriority: raw.queueItemPriority,
        queueItemReviewStatus: raw.queueItemReviewStatus,
      };
    }
  }
  return null;
}

export function extractFault(job: UiPathJob, logs: JobLog[]): JobFault {
  const message = topError(job, logs);
  return {
    message: truncate(message),
    signature: normalizeError(message),
    exceptionType: message.match(EXCEPTION_TYPE)?.[0] ?? null,
  };
}
```
Hunk B, delete lines 40-60 (`firstLine`, `GUID`, `ISO_TS`, their comment, `normalizeError`, trailing blank line) — they live in `orchestrator/faults.ts` now.

Hunk A, replace lines 7-31 (imports, `DEFAULT_REPO` + comment, `DEFAULT_LABELS`, `FaultedJobIssue`, trailing blank) with (followed by one blank line):
```ts
import type { Env } from "../config/config.js";
import {
  DEFAULT_REPO,
  type FaultedJobIssue,
  faultSearchQuery,
  firstLine,
  normalizeError,
  signatureMarker,
} from "../orchestrator/faults.js";
import { truncate } from "../orchestrator/log-digest.js";
import type { JobLog } from "../orchestrator/types.js";
import { parseRawMessage } from "./raw-message.js";
import {
  fetchJobByKey,
  fetchJobLogs,
  jobDeepLink,
  resolveFolder,
  type UiPathJob,
  type UiPathLogRow,
} from "./uipath.js";

export const DEFAULT_LABELS = ["uipath-fault", "bug"];
```

(`FormatOptions` at old lines 32-38 stays directly after it; `topError`, `fmtLog`, the rest of `formatFaultedJobIssue` and `BuildOptions`/`buildFaultedJobIssue` are untouched apart from D–G.)

- [ ] **Step 7: Edit `src/uipath/uipath.ts`** (current line numbers)

1. Lines 6-10 (imports):
```diff
 import { type Env, getUipath } from "../config/config.js";
 import { folderIdFor } from "../mcp/reference.js";
+import { isFailureLog } from "../orchestrator/log-semantics.js";
+import type { JobLog } from "../orchestrator/types.js";
+import { MAX_RATE_LIMIT_RETRIES, rateLimitDelayMs, sleep } from "../shared/http.js";
 import { chunk, isRecord, type StepProgress, safeJsonParse } from "../shared/util.js";
 import { invalidateBearerToken, resolveBearerToken } from "./auth.js";
 import { isNotFound, UiPathApiError } from "./errors.js";
-import { isFailureLog } from "./log-semantics.js";
```
2. Lines 57-65: replace the `JobLog` interface's header (lines 57-60 `export interface JobLog {` + `Level`/`Message`/`TimeStamp`) so it reads (the `RawMessage?: string;` field and its 4-line comment stay as they are):
```diff
-export interface JobLog {
-  Level: string;
-  Message: string;
-  TimeStamp: string;
+export interface UiPathLogRow extends JobLog {
   // Only populated when JobLogFilter.includeRawFields is set — see raw-message.ts
```
3. Delete lines 97-122 (the rate-limit comment block, the three constants, `sleep`, `rateLimitDelayMs`, trailing blank line) — now in `shared/http.ts`.
4. Line 139: `// (see the rate-limit comment above) — attempt and rateLimitAttempt are independent` → `// (see shared/http.ts) — attempt and rateLimitAttempt are independent`.
5. Every remaining `JobLog` in this file becomes `UiPathLogRow` (lines 343, 351, 371, 380):
```diff
-export function refineJobLogs(logs: JobLog[], filter: JobLogFilter = {}): JobLog[] {
+export function refineJobLogs(logs: UiPathLogRow[], filter: JobLogFilter = {}): UiPathLogRow[] {
```
```diff
 export interface JobLogResult {
-  logs: JobLog[];
+  logs: UiPathLogRow[];
```
```diff
-    logs: refineJobLogs(odataValues<JobLog>(data), filter),
+    logs: refineJobLogs(odataValues<UiPathLogRow>(data), filter),
```
```diff
-): Promise<JobLog[]> {
+): Promise<UiPathLogRow[]> {
```


- [ ] **Step 8: Remaining import/type edits, file by file** (old → new, exact text)

`src/uipath/raw-message.ts` (reads `RawMessage`, so it takes the UiPath row type):
```diff
-import type { JobLog } from "./uipath.js";
+import type { UiPathLogRow } from "./uipath.js";
```
```diff
-export function parseRawMessage(log: JobLog): RawMessageFields | null {
+export function parseRawMessage(log: UiPathLogRow): RawMessageFields | null {
```
`src/uipath/raw-message.test.ts` (lines 3, 5):
```diff
-import type { JobLog } from "./uipath.js";
+import type { UiPathLogRow } from "./uipath.js";
-const log = (RawMessage?: string): JobLog => ({
+const log = (RawMessage?: string): UiPathLogRow => ({
```
`src/uipath/uipath.test.ts` (lines 1-9; the `log` helper at line 13 builds only the three shared fields, so `JobLog` is the right type there):
```diff
 import { describe, expect, test } from "bun:test";
+import type { JobLog } from "../orchestrator/types.js";
 import {
   confirmQueueItemMatch,
-  type JobLog,
   jobLogQueryParams,
```
`src/orchestrator/log-semantics.ts` (header comment + import; the rest verbatim):
```diff
-// imported at runtime by both uipath.ts (refineJobLogs) and log-digest.ts
+// imported at runtime by both uipath/uipath.ts (refineJobLogs) and log-digest.ts
 // (digestLogs) without creating a cycle.
 
-import type { JobLog } from "./uipath.js";
+import type { JobLog } from "./types.js";
```
`src/orchestrator/log-semantics.test.ts` line 3:
```diff
-import type { JobLog } from "./uipath.js";
+import type { JobLog } from "./types.js";
```
`src/uipath/actions.ts` (lines 10-12; keeps import order — `orchestrator` sorts before `shared`):
```diff
 import { type Env, getUipath } from "../config/config.js";
+import { guardQueueItemSafety, type QueueSafetyLimits } from "../orchestrator/safety.js";
 import { isRecord } from "../shared/util.js";
-import { guardQueueItemSafety, type QueueSafetyLimits } from "./safety.js";
```
`src/uipath/queue-item.ts` (lines 13-15; this file is deleted in Task 4, the edit keeps it compiling until then):
```diff
 import { ACCOUNT_AUTOMATION_IDS } from "../mcp/reference.js";
+import { guardQueueItemSafety } from "../orchestrator/safety.js";
 import { prop } from "../shared/util.js";
-import { guardQueueItemSafety } from "./safety.js";
```
`src/uipath/queue.ts` (line 18 import; delete the `PortalRef` interface and its comment, lines 62-69 — `PortalRef` lives in `orchestrator/types.ts` now; `PullQueueItemResult.portal: PortalRef` at line 94 keeps its text):
```diff
 } from "../mcp/reference.js";
-import { guardQueueItemSafety } from "./safety.js";
+import { guardQueueItemSafety } from "../orchestrator/safety.js";
+import type { PortalRef } from "../orchestrator/types.js";
```
```diff
-// A portal reference exposed on a pull result. matched=false carries empty sentinels.
-export interface PortalRef {
-  matched: boolean;
-  key: string;
-  portalDir: string;
-  buildArtifact: string;
-  family: string;
-}
-
```
`src/server.ts` (line numbers current; the only importers of the moved log-digest symbols; `orchestrator` sorts between `./mcp/reference.js` and `./shared/util.js`):
```diff
   UIPATH_FOLDERS,
 } from "./mcp/reference.js";
+import { digestLogs, truncate } from "./orchestrator/log-digest.js";
 import { msBetween, stringProp } from "./shared/util.js";
 import { addQueueItem, deleteQueueItem, startJob, stopJob } from "./uipath/actions.js";
-import { buildFaultedJobIssue } from "./uipath/faults.js";
-import { digestLogs, extractFault, truncate } from "./uipath/log-digest.js";
+import { buildFaultedJobIssue, extractFault, extractTransactionOutcome } from "./uipath/faults.js";
 import { findOrderQueueItems, listQueue, pullQueueItem } from "./uipath/queue.js";
```
```diff
   fetchJobVideoUrl,
-  type JobLog,
   type JobLogFilter,
```
```diff
   type UiPathJob,
+  type UiPathLogRow,
 } from "./uipath/uipath.js";
```
```diff
 function shapeJobDetail(
   job: UiPathJob,
-  logs: JobLog[],
+  logs: UiPathLogRow[],
   includeLogDigest: boolean,
```
```diff
-          logDigest: digestLogs(logs),
+          logDigest: { ...digestLogs(logs), transactionOutcome: extractTransactionOutcome(logs) },
```
```diff
-            let logs: JobLog[] = [];
+            let logs: UiPathLogRow[] = [];
```

Exhaustiveness checks (both must print nothing):
```bash
rg -n 'from "(\.\./uipath/|\./)(safety|log-digest|log-semantics)\.js"' src | rg -v "src/orchestrator/"
rg -n "\bJobLog\b" src/uipath src/server.ts | rg -v "orchestrator/types"
```
(the second prints only lines that still use the shared `JobLog` legitimately: `uipath/faults.ts` `topError`/`fmtLog`/`extractFault`, `uipath/uipath.ts` `extends JobLog`, `uipath/uipath.test.ts`, `uipath/faults.test.ts`, and their `import type { JobLog }` lines — nothing may reference a `JobLog` exported from `./uipath.js`.)

- [ ] **Step 9: Tests that move or are added**

`src/orchestrator/log-digest.test.ts` — overwrite the renamed file (old `truncate`/`collapseLogs`/`findLogStalls`/`digestLogs` cases unchanged; `extractFault`, `extractTransactionOutcome` and the `transactionOutcome`-null digest case moved to `uipath/faults.test.ts`; one new case pins the 25-entry failure cap):
```ts
import { describe, expect, test } from "bun:test";
import { collapseLogs, digestLogs, findLogStalls, MESSAGE_CAP, truncate } from "./log-digest.js";
import type { JobLog } from "./types.js";

const log = (Level: string, Message: string, TimeStamp: string): JobLog => ({
  Level,
  Message,
  TimeStamp,
});

describe("truncate", () => {
  test("leaves short strings untouched", () => {
    expect(truncate("short message")).toBe("short message");
  });
  test("cuts long strings at MESSAGE_CAP and appends an ellipsis", () => {
    const long = "x".repeat(MESSAGE_CAP + 50);
    const out = truncate(long);
    expect(out).toBe(`${"x".repeat(MESSAGE_CAP)}…`);
    expect(out.length).toBe(MESSAGE_CAP + 1);
  });
});

describe("collapseLogs", () => {
  test("consecutive retries differing only in ids/timestamps collapse into one entry", () => {
    const logs = [
      log(
        "Error",
        "Timeout reaching order 7bfea00c-1111-2222-3333-444444444444 at 2026-07-01T10:00:01Z",
        "2026-07-01T10:00:01Z",
      ),
      log(
        "Error",
        "Timeout reaching order 23ba359f-5555-6666-7777-888888888888 at 2026-07-01T10:00:31Z",
        "2026-07-01T10:00:31Z",
      ),
      log(
        "Error",
        "Timeout reaching order 508debf6-9999-aaaa-bbbb-cccccccccccc at 2026-07-01T10:01:01Z",
        "2026-07-01T10:01:01Z",
      ),
    ];
    const collapsed = collapseLogs(logs);
    expect(collapsed).toHaveLength(1);
    expect(collapsed[0]?.count).toBe(3);
    expect(collapsed[0]?.firstAt).toBe("2026-07-01T10:00:01Z");
    expect(collapsed[0]?.lastAt).toBe("2026-07-01T10:01:01Z");
    // the first raw occurrence is kept, not the normalized form
    expect(collapsed[0]?.message).toContain("7bfea00c");
  });

  test("distinct messages and level changes do not collapse", () => {
    const logs = [
      log("Info", "Opening portal", "2026-07-01T10:00:00Z"),
      log("Error", "Opening portal", "2026-07-01T10:00:01Z"),
      log("Error", "Submit failed", "2026-07-01T10:00:02Z"),
    ];
    expect(collapseLogs(logs)).toHaveLength(3);
  });

  test("non-consecutive repeats stay separate entries", () => {
    const logs = [
      log("Error", "Submit failed", "2026-07-01T10:00:00Z"),
      log("Info", "Retrying", "2026-07-01T10:00:01Z"),
      log("Error", "Submit failed", "2026-07-01T10:00:02Z"),
    ];
    expect(collapseLogs(logs)).toHaveLength(3);
  });
});

describe("findLogStalls", () => {
  test("reports the largest gaps above the threshold, biggest first", () => {
    const logs = [
      log("Info", "step 1", "2026-07-01T10:00:00Z"),
      log("Info", "step 2", "2026-07-01T10:00:05Z"), // 5s — under threshold
      log("Info", "step 3", "2026-07-01T10:01:05Z"), // 60s stall after "step 2"
      log("Info", "step 4", "2026-07-01T10:01:25Z"), // 20s stall after "step 3"
    ];
    const stalls = findLogStalls(logs);
    expect(stalls).toHaveLength(2);
    expect(stalls[0]?.gapMs).toBe(60_000);
    expect(stalls[0]?.beforeMessage).toBe("step 2");
    expect(stalls[1]?.gapMs).toBe(20_000);
  });

  test("no stalls in a tight log stream", () => {
    const logs = [
      log("Info", "a", "2026-07-01T10:00:00Z"),
      log("Info", "b", "2026-07-01T10:00:01Z"),
    ];
    expect(findLogStalls(logs)).toEqual([]);
  });
});

describe("digestLogs", () => {
  test("counts by level, keeps only failure lines, and points at get_job_logs", () => {
    const logs = [
      log("Info", "Opening portal", "2026-07-01T10:00:00Z"),
      log("Info", "Unable to find submit button", "2026-07-01T10:00:01Z"), // failure worded at Info
      log("Warn", "Slow response", "2026-07-01T10:00:02Z"),
      log("Error", "Submit failed", "2026-07-01T10:00:03Z"),
    ];
    const digest = digestLogs(logs);
    expect(digest.fetched).toBe(4);
    expect(digest.byLevel).toEqual({ Info: 2, Warn: 1, Error: 1 });
    expect(digest.failures.map((f) => f.message)).toEqual([
      "Unable to find submit button",
      "Submit failed",
    ]);
    expect(digest.droppedFailures).toBe(0);
    expect(digest.note).toContain("get_job_logs");
  });

  test("the failure cap keeps the first failure and the tail, and counts what it dropped", () => {
    const logs = Array.from({ length: 30 }, (_, i) =>
      log("Error", `failure ${i}`, "2026-07-01T10:00:00Z"),
    );
    const digest = digestLogs(logs);
    expect(digest.failures).toHaveLength(25);
    expect(digest.failures[0]?.message).toBe("failure 0");
    expect(digest.failures[24]?.message).toBe("failure 29");
    expect(digest.droppedFailures).toBe(5);
  });
});
```

`src/orchestrator/faults.test.ts` (the two `normalizeError` cases moved from `uipath/faults.test.ts`; the rest is new):
```ts
import { describe, expect, test } from "bun:test";
import { faultSearchQuery, firstLine, normalizeError, signatureMarker } from "./faults.js";

describe("normalizeError", () => {
  test("two faults with different ids/timestamps collapse to the same signature", () => {
    const a = normalizeError(
      "Selector not found for order 998877 at 2026-06-30T10:00:30Z (job 1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d)",
    );
    const b = normalizeError(
      "Selector not found for order 112233 at 2026-06-29T22:14:01Z (job 9f8e7d6c-5b4a-3210-fedc-ba9876543210)",
    );
    expect(a).toBe(b);
    expect(a).toContain("selector not found");
    expect(a).toContain("<id>");
    expect(a).toContain("<n>");
    expect(a).toContain("<ts>");
  });

  test("lowercases, drops quotes/backticks, collapses whitespace, truncates", () => {
    const s = normalizeError(`  Login   FAILED:  "bad creds"  \n\n with \`token\` `);
    expect(s).toBe("login failed: bad creds with token");
    expect(normalizeError("x".repeat(300)).length).toBe(160);
  });
});

describe("firstLine", () => {
  test("keeps only the first line, trimmed", () => {
    expect(firstLine("  boom  \nstack frame 1\nstack frame 2")).toBe("boom");
  });

  test("caps a long first line at 120 characters", () => {
    expect(firstLine("y".repeat(300))).toHaveLength(120);
  });
});

describe("signatureMarker / faultSearchQuery", () => {
  const signature = "selector not found for order <n>";

  test("the marker is the exact hidden-comment string the issue body embeds", () => {
    expect(signatureMarker(signature)).toBe(
      "<!-- fault-signature: selector not found for order <n> -->",
    );
  });

  test("the search query targets the repo, open issues, and the marker's phrase", () => {
    expect(faultSearchQuery("acme/repo", signature)).toBe(
      'repo:acme/repo is:issue is:open in:body "fault-signature: selector not found for order <n>"',
    );
  });

  test("the quoted search phrase occurs verbatim inside the marker, so a search finds its own issue", () => {
    const phrase = faultSearchQuery("acme/repo", signature).split('in:body "')[1]?.slice(0, -1);
    expect(phrase).toBeDefined();
    expect(signatureMarker(signature)).toContain(phrase ?? "");
  });
});
```

`src/shared/http.test.ts`:
```ts
import { describe, expect, test } from "bun:test";
import {
  DEFAULT_RATE_LIMIT_RETRY_MS,
  MAX_RATE_LIMIT_RETRY_MS,
  rateLimitDelayMs,
  sleep,
} from "./http.js";

describe("rateLimitDelayMs", () => {
  test("honors a Retry-After given in seconds", () => {
    expect(rateLimitDelayMs("7")).toBe(7_000);
    expect(rateLimitDelayMs("0")).toBe(0);
  });

  test("falls back to the default when the header is missing, negative or not a number", () => {
    expect(rateLimitDelayMs(null)).toBe(DEFAULT_RATE_LIMIT_RETRY_MS);
    expect(rateLimitDelayMs("-3")).toBe(DEFAULT_RATE_LIMIT_RETRY_MS);
    expect(rateLimitDelayMs("soon")).toBe(DEFAULT_RATE_LIMIT_RETRY_MS);
  });

  test("clamps to one rate-limit window", () => {
    expect(rateLimitDelayMs("999")).toBe(MAX_RATE_LIMIT_RETRY_MS);
  });
});

describe("sleep", () => {
  test("resolves after the delay", async () => {
    const start = Date.now();
    await sleep(20);
    expect(Date.now() - start).toBeGreaterThanOrEqual(15);
  });
});
```

`src/uipath/faults.test.ts` — three edits:
1. Replace imports lines 5-13 (everything after `resetConfigCache`) with:
```ts
import { DEFAULT_REPO } from "../orchestrator/faults.js";
import type { JobLog } from "../orchestrator/types.js";
import {
  buildFaultedJobIssue,
  DEFAULT_LABELS,
  extractFault,
  extractTransactionOutcome,
  formatFaultedJobIssue,
} from "./faults.js";
import type { UiPathJob, UiPathLogRow } from "./uipath.js";
```
2. Delete the whole `describe("normalizeError", …)` block (old lines 29-50 and its trailing blank line).
3. In its place (before `describe("formatFaultedJobIssue"`) insert:
```ts
describe("extractFault", () => {
  const faultJob: UiPathJob = { Key: "k", State: "Faulted" };

  test("parses the exception type and computes a stable signature", () => {
    const logs = [
      errLog(
        "System.NullReferenceException: Object reference not set. Job 7bfea00c-1111-2222-3333-444444444444",
      ),
    ];
    const fault = extractFault(faultJob, logs);
    expect(fault.exceptionType).toBe("System.NullReferenceException");
    expect(fault.signature).toContain("<id>");
    expect(fault.signature).not.toContain("7bfea00c");
  });

  test("two runs of the same fault share a signature despite different ids", () => {
    const a = extractFault(faultJob, [
      errLog("Timeout on order 7bfea00c-1111-2222-3333-444444444444"),
    ]);
    const b = extractFault(faultJob, [
      errLog("Timeout on order 508debf6-9999-aaaa-bbbb-cccccccccccc"),
    ]);
    expect(a.signature).toBe(b.signature);
  });

  test("falls back to a synthetic message when there are no logs", () => {
    const fault = extractFault(faultJob, []);
    expect(fault.message).toBe("Job ended in state Faulted");
    expect(fault.exceptionType).toBeNull();
  });
});

describe("extractTransactionOutcome", () => {
  const row = (Message: string, TimeStamp: string, RawMessage?: string): UiPathLogRow => ({
    Level: "Info",
    Message,
    TimeStamp,
    ...(RawMessage !== undefined ? { RawMessage } : {}),
  });

  test("reads the queue's own exception classification off the Transaction Ended row", () => {
    const ended = JSON.stringify({
      transactionState: "Ended",
      transactionId: "d211b063-20eb-45a9-801a-5634cd13568a",
      queueName: "CenCal auth sync queue",
      processingExceptionType: "BusinessException",
      processingExceptionReason: "",
      transactionExecutionTime: 49.93,
      queueItemPriority: "Normal",
      queueItemReviewStatus: "None",
    });
    const logs = [
      row("Transaction Started", "2026-07-01T10:00:00Z"),
      row("Transaction Ended", "2026-07-01T10:00:05Z", ended),
    ];
    expect(extractTransactionOutcome(logs)).toEqual({
      transactionId: "d211b063-20eb-45a9-801a-5634cd13568a",
      queueName: "CenCal auth sync queue",
      processingExceptionType: "BusinessException",
      processingExceptionReason: null,
      transactionExecutionTimeSec: 49.93,
      queueItemPriority: "Normal",
      queueItemReviewStatus: "None",
    });
  });

  test("null when RawMessage was never fetched", () => {
    expect(
      extractTransactionOutcome([row("Transaction Ended", "2026-07-01T10:00:00Z")]),
    ).toBeNull();
  });

  test("null when no row ever reaches a Transaction Ended state", () => {
    const logs = [
      row(
        "Transaction Started",
        "2026-07-01T10:00:00Z",
        JSON.stringify({ transactionState: "Started" }),
      ),
      {
        ...row("Job crashed before ending the transaction", "2026-07-01T10:00:01Z"),
        Level: "Error",
      },
    ];
    expect(extractTransactionOutcome(logs)).toBeNull();
  });

  test("takes the LAST Transaction Ended row when a job logs more than one", () => {
    const first = JSON.stringify({
      transactionState: "Ended",
      processingExceptionType: "BusinessException",
    });
    const second = JSON.stringify({ transactionState: "Ended", processingExceptionType: "" });
    const logs = [
      row("Transaction Ended", "2026-07-01T10:00:00Z", first),
      row("Transaction Ended", "2026-07-01T10:05:00Z", second),
    ];
    expect(extractTransactionOutcome(logs)?.processingExceptionType).toBeNull();
  });
});
```

- [ ] **Step 10: Run tests** — `bun test` → all pass (397 baseline + 10 new = **407**). If `bunx biome check src` reports import-order or line-width findings from the hand edits, run `bunx biome check --write src` (formatter/organizer only) and re-run.
- [ ] **Step 11: Full gate** — `bun run typecheck && bun test && bunx biome check src` → all green
- [ ] **Step 12: Commit**
```bash
git add -A src
git commit -m "refactor(orchestrator): contract types and generic module moves" -m "Create src/orchestrator/{types,backend}.ts (the backend-neutral contract) and move the backend-neutral parts of log-digest, log-semantics, safety and the fault helpers out of src/uipath so a second backend can share them. sleep/rateLimitDelayMs move to shared/http.ts and ExpectedError to a leaf shared/expected-error.ts (config.ts will need to throw it without importing feedback.ts). No behavior change."
```


### Task 2: UiPath pure mappers (`src/uipath/map.ts`)

**Files:**
- Create: `src/uipath/map.ts`
- Modify: `src/uipath/faults.ts` (export `EXCEPTION_TYPE`, line 47 after Task 1), `src/uipath/actions.ts` (`TERMINAL_JOB_STATES` → imported `UIPATH_TERMINAL_STATES`; lines 10-13 imports, 213-218)
- Test: `src/uipath/map.test.ts`

**Interfaces:**
- Consumes: `JobCore`, `QueueItemCore`, `QueueCore`, `ProcessCore`, `TriggerCore`, `JobAttempt`, `JobFault`, `JobLog` (`orchestrator/types.ts`); `JobLogDigest`, `digestLogs`, `truncate` (`orchestrator/log-digest.ts`); `normalizeError` (`orchestrator/faults.ts`); `topError`, `extractTransactionOutcome`, `TransactionOutcome` (`uipath/faults.ts`); `UiPathJob`, `QueueItem`, `QueueItemMatch`, `QueueDefinition`, `UiPathRelease`, `UiPathTrigger`, `UiPathLogRow` (`uipath/uipath.ts`); `msBetween` (`shared/util.ts`).
- Produces (all in `src/uipath/map.ts`): `UIPATH_TERMINAL_STATES: ReadonlySet<string>`; `UiPathJobDetails { numericId: string | null; robotName: string; output?: unknown }`; `UiPathItemDetails { retryNumber: number; robotName: string; queueDefinitionId: number; memberId: string; member: string }` (memberId/member = specificContent `MemberID`/`MemberFullName`, "" when absent — the same keys the old `listQueue` projection read; populated by all three item paths: list, find, pull); `UiPathQueueDetails`, `UiPathProcessDetails`, `UiPathTriggerDetails`; `UiPathLogDigest = JobLogDigest & { transactionOutcome: TransactionOutcome | null }`; `toJobCore(job: UiPathJob, opts: { link: string; output?: unknown }): JobCore<UiPathJobDetails>` (`fault` always null, `queueItem` null — a caller that read logs overlays `fault`); `toQueueItemCoreFromItem(item: QueueItem, opts: { queueName: string; specificContent: Record<string, unknown> | null }): QueueItemCore<UiPathItemDetails>`; `toQueueItemCoreFromMatch(m: QueueItemMatch, opts: { queueName: string }): QueueItemCore<UiPathItemDetails>`; `toQueueCore(q: QueueDefinition)`; `toProcessCore(r: UiPathRelease)`; `toTriggerCore(t: UiPathTrigger)`; `uipathFault(job: UiPathJob, logs: JobLog[]): JobFault`; `exceptionTypeOf(processingExceptionType: string): "business" | "application" | null`; `toUiPathLogDigest(logs: UiPathLogRow[]): UiPathLogDigest`.
- **Old code kept on purpose in this task:** `extractFault` and the old `JobFault { message, signature, exceptionType }` in `uipath/faults.ts`. `server.ts`'s get_job handler (`shapeJobDetail`) still calls `extractFault` until Task 5 rewires every tool; Task 5 deletes `extractFault`, the old `JobFault` and their tests (`describe("extractFault")` in `uipath/faults.test.ts`) together with that call site. `uipathFault` does **not** delegate to `extractFault` (no shims): it re-implements the same three lines against `topError`/`normalizeError`/`truncate`/`EXCEPTION_TYPE`, which stay.

- [ ] **Step 1: Write the failing test** — `src/uipath/map.test.ts`:
```ts
import { describe, expect, test } from "bun:test";
import {
  exceptionTypeOf,
  toJobCore,
  toProcessCore,
  toQueueCore,
  toQueueItemCoreFromItem,
  toQueueItemCoreFromMatch,
  toTriggerCore,
  toUiPathLogDigest,
  UIPATH_TERMINAL_STATES,
  uipathFault,
} from "./map.js";
import type { QueueItem, QueueItemMatch, UiPathJob, UiPathLogRow } from "./uipath.js";

const KEY = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee";
const LINK = `https://cloud.uipath.com/org/tenant/orchestrator_/jobs/${KEY}`;

const job: UiPathJob = {
  Id: "12345",
  Key: KEY,
  State: "Faulted",
  ReleaseName: "PMG auth submit",
  ProcessVersion: "1.2.3",
  RobotName: "robot-7",
  CreationTime: "2026-07-01T10:00:00Z",
  EndTime: "2026-07-01T10:01:30Z",
};

describe("UIPATH_TERMINAL_STATES", () => {
  test("is exactly Successful, Faulted, Stopped", () => {
    expect([...UIPATH_TERMINAL_STATES].sort()).toEqual(["Faulted", "Stopped", "Successful"]);
  });
});

describe("toJobCore", () => {
  test("maps a faulted job; state passes through verbatim and fault stays null", () => {
    const core = toJobCore(job, { link: LINK });
    expect(core).toEqual({
      orchestrator: "uipath",
      id: KEY,
      state: "Faulted",
      rawState: "Faulted",
      terminal: true,
      process: { id: null, name: "PMG auth submit", version: "1.2.3" },
      queueItem: null,
      createdAt: "2026-07-01T10:00:00Z",
      startedAt: null,
      endedAt: "2026-07-01T10:01:30Z",
      durationMs: 90_000,
      attempt: null,
      retryOfJobId: null,
      fault: null,
      link: LINK,
      details: { numericId: "12345", robotName: "robot-7" },
    });
  });

  test("a running job is not terminal and has no end/duration; absent fields default", () => {
    const core = toJobCore(
      { Key: KEY, State: "Running", CreationTime: "2026-07-01T10:00:00Z" },
      {
        link: "",
      },
    );
    expect(core.terminal).toBe(false);
    expect(core.endedAt).toBeNull();
    expect(core.durationMs).toBeNull();
    expect(core.process).toEqual({ id: null, name: "", version: null });
    expect(core.details).toEqual({ numericId: null, robotName: "" });
  });

  test("output is carried under details only when the read fetched it (null is a value)", () => {
    expect(toJobCore(job, { link: LINK }).details).not.toHaveProperty("output");
    expect(toJobCore(job, { link: LINK, output: null }).details).toHaveProperty("output", null);
    expect(toJobCore(job, { link: LINK, output: { ok: 1 } }).details.output).toEqual({ ok: 1 });
  });

  test("every UiPath state outside the terminal set is non-terminal", () => {
    for (const state of ["Pending", "Running", "Stopping", "Suspended", "Resumed"]) {
      expect(toJobCore({ Key: KEY, State: state }, { link: "" }).terminal).toBe(false);
    }
  });
});

describe("exceptionTypeOf", () => {
  const cases: Array<[string, "business" | "application" | null]> = [
    ["BusinessException", "business"],
    ["ApplicationException", "application"],
    ["Business", "business"],
    ["Application", "application"],
    ["System", "application"],
    [" businessexception ", "business"],
    ["", null],
    ["SomethingElse", null],
  ];
  for (const [input, expected] of cases) {
    test(`${JSON.stringify(input)} -> ${expected}`, () => {
      expect(exceptionTypeOf(input)).toBe(expected);
    });
  }
});

const item: QueueItem = {
  id: 5,
  status: "Failed",
  reference: "REF-1",
  creationTime: "2026-07-01T09:00:00Z",
  retryNumber: 1,
  queueDefinitionId: 77,
  name: "PMG auth submit queue",
  robotName: "robot-7",
  specificContent: { orderUid: "order-uid-1", MemberID: "m1", MemberFullName: "Doe, Jane" },
};

describe("toQueueItemCoreFromItem", () => {
  test("maps the item; the default projection withholds specificContent", () => {
    const core = toQueueItemCoreFromItem(item, { queueName: "PMG", specificContent: null });
    expect(core).toEqual({
      orchestrator: "uipath",
      queueId: "77",
      queueName: "PMG",
      itemId: "5",
      reference: "REF-1",
      orderUid: "order-uid-1",
      status: "Failed",
      rawStatus: "Failed",
      exceptionType: null,
      createdAt: "2026-07-01T09:00:00Z",
      endedAt: null,
      jobs: [],
      specificContent: null,
      details: {
        retryNumber: 1,
        robotName: "robot-7",
        queueDefinitionId: 77,
        memberId: "m1",
        member: "Doe, Jane",
      },
    });
    // PHI-light default: the member triage columns ride in details, the full payload does not
    expect(JSON.stringify(core.specificContent)).toBe("null");
  });

  test('member columns default to "" when absent or not strings', () => {
    const core = toQueueItemCoreFromItem(
      { ...item, specificContent: { MemberID: 5 } },
      { queueName: "PMG", specificContent: null },
    );
    expect(core.details.memberId).toBe("");
    expect(core.details.member).toBe("");
  });

  test("a supplied specificContent is carried as-is; a missing orderUid is null", () => {
    const sc = { IsApproved: false };
    const core = toQueueItemCoreFromItem(
      { ...item, specificContent: { orderUid: 42 } },
      { queueName: "PMG", specificContent: sc },
    );
    expect(core.specificContent).toBe(sc);
    expect(core.orderUid).toBeNull();
  });
});

const match: QueueItemMatch = {
  id: 9,
  reference: "REF-9",
  status: "Failed",
  executorJobKey: KEY,
  processingExceptionType: "BusinessException",
  retryNumber: 2,
  creationTime: "2026-07-01T09:30:00Z",
  specificContent: {
    orderUid: "order-uid-1",
    MemberID: "m1",
    MemberFullName: "Doe, Jane",
    token: "[redacted]",
  },
  queueDefinitionId: 77,
};

describe("toQueueItemCoreFromMatch", () => {
  test("a picked-up item yields one unknown-state job attempt and a classified exception", () => {
    const core = toQueueItemCoreFromMatch(match, { queueName: "PMG" });
    expect(core.jobs).toEqual([
      { orchestrator: "uipath", id: KEY, state: null, neverStarted: null },
    ]);
    expect(core.exceptionType).toBe("business");
    expect(core.orderUid).toBe("order-uid-1");
    expect(core.specificContent).toEqual({
      orderUid: "order-uid-1",
      MemberID: "m1",
      MemberFullName: "Doe, Jane",
      token: "[redacted]",
    });
    expect(core.details).toEqual({
      retryNumber: 2,
      robotName: "",
      queueDefinitionId: 77,
      memberId: "m1",
      member: "Doe, Jane",
    });
  });

  test("a not-yet-picked-up item has no jobs and no exception", () => {
    const core = toQueueItemCoreFromMatch(
      { ...match, executorJobKey: "", processingExceptionType: "", status: "New" },
      { queueName: "PMG" },
    );
    expect(core.jobs).toEqual([]);
    expect(core.exceptionType).toBeNull();
    expect(core.status).toBe("New");
  });
});

describe("toQueueCore / toProcessCore", () => {
  test("queue", () => {
    expect(
      toQueueCore({ id: 3, name: "PMG auth submit queue", description: "d", creationTime: "t" }),
    ).toEqual({
      orchestrator: "uipath",
      id: "3",
      name: "PMG auth submit queue",
      processId: null,
      active: true,
      details: { description: "d", creationTime: "t" },
    });
  });

  test("release: id is the Key (what start_job takes); empty package/version are null", () => {
    const base = {
      id: 8,
      key: KEY,
      name: "PMG auth submit",
      processKey: "pkg",
      processVersion: "1.2.3",
      isLatestVersion: true,
    };
    expect(toProcessCore(base)).toEqual({
      orchestrator: "uipath",
      id: KEY,
      name: "PMG auth submit",
      package: "pkg",
      version: "1.2.3",
      active: true,
      details: { numericId: "8", isLatestVersion: true },
    });
    const bare = toProcessCore({ ...base, processKey: "", processVersion: "" });
    expect(bare.package).toBeNull();
    expect(bare.version).toBeNull();
  });
});

describe("toTriggerCore", () => {
  const base = {
    id: 4,
    name: "trg",
    enabled: true,
    releaseId: 8,
    releaseKey: KEY,
    releaseName: "PMG auth submit",
    queueDefinitionId: 77,
    queueDefinitionName: "PMG auth submit queue",
    itemsActivationThreshold: 1,
    maxJobsForActivation: 2,
  };

  test("a queue trigger links process and queue", () => {
    const core = toTriggerCore(base);
    expect(core.kind).toBe("queue");
    expect(core.queueId).toBe("77");
    expect(core.processId).toBe(KEY);
    expect(core.cron).toBeNull();
    expect(core.details).toEqual({
      releaseId: 8,
      releaseName: "PMG auth submit",
      queueDefinitionName: "PMG auth submit queue",
      itemsActivationThreshold: 1,
      maxJobsForActivation: 2,
    });
  });

  test("a time trigger (queueDefinitionId 0) has no queue", () => {
    const core = toTriggerCore({ ...base, queueDefinitionId: 0, queueDefinitionName: "" });
    expect(core.kind).toBe("time");
    expect(core.queueId).toBeNull();
  });
});

const errLog = (Message: string): UiPathLogRow => ({
  Level: "Error",
  Message,
  TimeStamp: "2026-07-01T10:00:30Z",
});

describe("uipathFault", () => {
  test("class is the exception type parsed from the headline error; signature is stable", () => {
    const fault = uipathFault(job, [
      errLog("System.NullReferenceException: boom on 7bfea00c-1111-2222-3333-444444444444"),
    ]);
    expect(fault.class).toBe("System.NullReferenceException");
    expect(fault.signature).toContain("<id>");
    expect(fault.message).toContain("NullReferenceException");
  });

  test("no logs -> synthetic state message and a null class", () => {
    expect(uipathFault(job, [])).toEqual({
      message: "Job ended in state Faulted",
      signature: "job ended in state faulted",
      class: null,
    });
  });
});

describe("toUiPathLogDigest", () => {
  test("the generic digest plus the queue transaction's outcome", () => {
    const ended = JSON.stringify({
      transactionState: "Ended",
      processingExceptionType: "BusinessException",
    });
    const digest = toUiPathLogDigest([
      errLog("Submit failed"),
      {
        Level: "Info",
        Message: "Transaction Ended",
        TimeStamp: "2026-07-01T10:00:31Z",
        RawMessage: ended,
      },
    ]);
    expect(digest.fetched).toBe(2);
    expect(digest.failures.map((f) => f.message)).toEqual(["Submit failed"]);
    expect(digest.transactionOutcome?.processingExceptionType).toBe("BusinessException");
  });

  test("transactionOutcome is null when RawMessage was never fetched", () => {
    expect(toUiPathLogDigest([errLog("x")]).transactionOutcome).toBeNull();
  });
});
```

- [ ] **Step 2: Run it to verify it fails**
Run: `bun test src/uipath/map.test.ts` — Expected: FAIL (`Cannot find module './map.js'`)

- [ ] **Step 3: Implement**

`src/uipath/faults.ts` line 47 (so `map.ts` can reuse the one regex):
```diff
-const EXCEPTION_TYPE = /\b(?:[A-Za-z_]\w*\.)*[A-Z]\w*(?:Exception|Error)\b/;
+export const EXCEPTION_TYPE = /\b(?:[A-Za-z_]\w*\.)*[A-Z]\w*(?:Exception|Error)\b/;
```

`src/uipath/actions.ts` — the terminal set moves to `map.ts` (single owner; `assertStoppable` imports it):
```diff
 import { isRecord } from "../shared/util.js";
+import { UIPATH_TERMINAL_STATES } from "./map.js";
 import {
   fetchJobByKey,
```
```diff
-// A job already in one of these states has nothing left to stop.
-const TERMINAL_JOB_STATES = new Set(["Successful", "Faulted", "Stopped"]);
-
 export function assertStoppable(job: UiPathJob): void {
-  if (job.State && TERMINAL_JOB_STATES.has(job.State)) {
+  if (job.State && UIPATH_TERMINAL_STATES.has(job.State)) {
```

`src/uipath/map.ts`:
```ts
// Pure UiPath -> orchestrator-core mappers (no I/O). UiPath's own vocabulary is the
// contract's reference vocabulary, so State/Status pass through verbatim; everything
// UiPath-specific lives under each core's `details`.

import { normalizeError } from "../orchestrator/faults.js";
import { digestLogs, type JobLogDigest, truncate } from "../orchestrator/log-digest.js";
import type {
  JobAttempt,
  JobCore,
  JobFault,
  JobLog,
  ProcessCore,
  QueueCore,
  QueueItemCore,
  TriggerCore,
} from "../orchestrator/types.js";
import { msBetween } from "../shared/util.js";
import {
  EXCEPTION_TYPE,
  extractTransactionOutcome,
  type TransactionOutcome,
  topError,
} from "./faults.js";
import type {
  QueueDefinition,
  QueueItem,
  QueueItemMatch,
  UiPathJob,
  UiPathLogRow,
  UiPathRelease,
  UiPathTrigger,
} from "./uipath.js";

// A job already in one of these states has nothing left to stop.
export const UIPATH_TERMINAL_STATES: ReadonlySet<string> = new Set([
  "Successful",
  "Faulted",
  "Stopped",
]);

export interface UiPathJobDetails {
  numericId: string | null; // the OData Id; StopJob addresses jobs by it
  robotName: string; // "" when unassigned
  output?: unknown; // parsed OutputArguments; present only on reads that fetch it (get_job)
}
export interface UiPathItemDetails {
  retryNumber: number;
  robotName: string; // "" when unassigned or the read doesn't carry it
  queueDefinitionId: number;
  memberId: string; // specificContent.MemberID — the triage columns; "" when absent
  member: string; // specificContent.MemberFullName; "" when absent
}
export interface UiPathQueueDetails {
  description: string;
  creationTime: string;
}
export interface UiPathProcessDetails {
  numericId: string;
  isLatestVersion: boolean;
}
export interface UiPathTriggerDetails {
  releaseId: number;
  releaseName: string;
  queueDefinitionName: string;
  itemsActivationThreshold: number;
  maxJobsForActivation: number;
}
export type UiPathLogDigest = JobLogDigest & { transactionOutcome: TransactionOutcome | null };

// The jobs read carries no fault (it comes from logs) or queue item: both stay null here,
// and a caller that fetched logs overlays `fault`.
export function toJobCore(
  job: UiPathJob,
  opts: { link: string; output?: unknown },
): JobCore<UiPathJobDetails> {
  const state = job.State ?? "";
  return {
    orchestrator: "uipath",
    id: job.Key ?? "",
    state,
    rawState: state,
    terminal: UIPATH_TERMINAL_STATES.has(state),
    process: { id: null, name: job.ReleaseName ?? "", version: job.ProcessVersion ?? null },
    queueItem: null,
    createdAt: job.CreationTime ?? "",
    startedAt: null,
    endedAt: job.EndTime ?? null,
    durationMs: msBetween(job.CreationTime, job.EndTime),
    attempt: null,
    retryOfJobId: null,
    fault: null,
    link: opts.link,
    details: {
      numericId: job.Id === undefined ? null : String(job.Id),
      robotName: job.RobotName ?? "",
      ...(opts.output !== undefined ? { output: opts.output } : {}),
    },
  };
}

const EXCEPTION_TYPES: Record<string, "business" | "application"> = {
  businessexception: "business",
  business: "business",
  applicationexception: "application",
  application: "application",
  system: "application",
};

// UiPath's ProcessingExceptionType ("BusinessException" | "ApplicationException") onto
// the contract's two-valued classification; "" (none) and unknown values are null.
export const exceptionTypeOf = (
  processingExceptionType: string,
): "business" | "application" | null =>
  EXCEPTION_TYPES[processingExceptionType.trim().toLowerCase()] ?? null;

const orderUidOf = (sc: Record<string, unknown>): string | null => {
  const v = sc["orderUid"];
  return typeof v === "string" && v ? v : null;
};

const str = (v: unknown): string => (typeof v === "string" ? v : "");
const memberOf = (sc: Record<string, unknown>): { memberId: string; member: string } => ({
  memberId: str(sc["MemberID"]),
  member: str(sc["MemberFullName"]),
});

// `specificContent: null` is the PHI-light default projection; pull passes the guarded payload.
export function toQueueItemCoreFromItem(
  item: QueueItem,
  opts: { queueName: string; specificContent: Record<string, unknown> | null },
): QueueItemCore<UiPathItemDetails> {
  return {
    orchestrator: "uipath",
    queueId: String(item.queueDefinitionId),
    queueName: opts.queueName,
    itemId: String(item.id),
    reference: item.reference,
    orderUid: orderUidOf(item.specificContent),
    status: item.status,
    rawStatus: item.status,
    exceptionType: null, // the QueueItems read doesn't select ProcessingExceptionType
    createdAt: item.creationTime,
    endedAt: null,
    jobs: [],
    specificContent: opts.specificContent,
    details: {
      retryNumber: item.retryNumber,
      robotName: item.robotName,
      queueDefinitionId: item.queueDefinitionId,
      ...memberOf(item.specificContent),
    },
  };
}

// The order-correlation read: carries the job key and exception type, no robot.
export function toQueueItemCoreFromMatch(
  m: QueueItemMatch,
  opts: { queueName: string },
): QueueItemCore<UiPathItemDetails> {
  const jobs: JobAttempt[] = m.executorJobKey
    ? [{ orchestrator: "uipath", id: m.executorJobKey, state: null, neverStarted: null }]
    : [];
  return {
    orchestrator: "uipath",
    queueId: String(m.queueDefinitionId),
    queueName: opts.queueName,
    itemId: String(m.id),
    reference: m.reference,
    orderUid: orderUidOf(m.specificContent),
    status: m.status,
    rawStatus: m.status,
    exceptionType: exceptionTypeOf(m.processingExceptionType),
    createdAt: m.creationTime,
    endedAt: null,
    jobs,
    specificContent: m.specificContent,
    details: {
      retryNumber: m.retryNumber,
      robotName: "",
      queueDefinitionId: m.queueDefinitionId,
      ...memberOf(m.specificContent),
    },
  };
}

// Queue definitions and releases have no active/disabled flag in UiPath, so both read as active.
export const toQueueCore = (q: QueueDefinition): QueueCore<UiPathQueueDetails> => ({
  orchestrator: "uipath",
  id: String(q.id),
  name: q.name,
  processId: null,
  active: true,
  details: { description: q.description, creationTime: q.creationTime },
});

export const toProcessCore = (r: UiPathRelease): ProcessCore<UiPathProcessDetails> => ({
  orchestrator: "uipath",
  id: r.key,
  name: r.name,
  package: r.processKey || null,
  version: r.processVersion || null,
  active: true,
  details: { numericId: String(r.id), isLatestVersion: r.isLatestVersion },
});

// A queue trigger carries a non-zero queueDefinitionId; time triggers have 0 sentinels.
export const toTriggerCore = (t: UiPathTrigger): TriggerCore<UiPathTriggerDetails> => ({
  orchestrator: "uipath",
  id: String(t.id),
  name: t.name,
  kind: t.queueDefinitionId ? "queue" : "time",
  processId: t.releaseKey || null,
  queueId: t.queueDefinitionId ? String(t.queueDefinitionId) : null,
  enabled: t.enabled,
  cron: null, // the ProcessSchedules read doesn't select the cron fields
  details: {
    releaseId: t.releaseId,
    releaseName: t.releaseName,
    queueDefinitionName: t.queueDefinitionName,
    itemsActivationThreshold: t.itemsActivationThreshold,
    maxJobsForActivation: t.maxJobsForActivation,
  },
});

// `class` is the exception type parsed from the headline error; the signature is the
// same normalizeError dedupe key build_faulted_job_issue uses.
export function uipathFault(job: UiPathJob, logs: JobLog[]): JobFault {
  const message = topError(job, logs);
  return {
    message: truncate(message),
    signature: normalizeError(message),
    class: message.match(EXCEPTION_TYPE)?.[0] ?? null,
  };
}

export const toUiPathLogDigest = (logs: UiPathLogRow[]): UiPathLogDigest => ({
  ...digestLogs(logs),
  transactionOutcome: extractTransactionOutcome(logs),
});
```

- [ ] **Step 4: Run tests** — `bun test src/uipath/map.test.ts src/uipath/actions.test.ts` → PASS (`actions.test.ts`'s `assertStoppable` cases prove the moved terminal set is unchanged)
- [ ] **Step 5: Full gate** — `bun run typecheck && bun test && bunx biome check src` → all green (433 tests: 407 + 26 new)
- [ ] **Step 6: Commit**
```bash
git add src/uipath/map.ts src/uipath/map.test.ts src/uipath/faults.ts src/uipath/actions.ts
git commit -m "feat(uipath): pure mappers from UiPath rows to orchestrator cores" -m "map.ts turns jobs, queue items, queues, releases and triggers into the backend-neutral cores (UiPath's State/Status spelling is the reference vocabulary, so they pass through) and owns the terminal-state set that stop_job's guard used to hold privately."
```


### Task 3: `uipathBackend` read functions (`src/uipath/backend.ts`)

`src/server.ts` is **not modified** in this task: the old tool handlers keep working untouched on the old `uipath/queue.ts` functions. This task adds the new, interface-shaped implementation beside them; Task 5 rewires every tool to `backendFor(...)` and deletes the old handler logic (`shapeJobDetail`, the get_job batch loop, the get_job_logs entry loop, the list_jobs projection, the list_triggers filter, the pull_queue_item arg resolution) and the old `queue.ts` functions.

**Files:**
- Create: `src/orchestrator/portal.ts`, `src/orchestrator/portal.test.ts`, `src/uipath/backend.ts`
- Modify: `src/uipath/queue.ts` (delete `memberFilename`, lines 71-80 after Task 1; use `toPortalRef` in the old `pullQueueItem`'s result, lines 154-160; add import)
- Test: `src/orchestrator/portal.test.ts`, `src/uipath/backend.test.ts` (jobs, logs, fault issue), `src/uipath/backend-queues.test.ts` (queues, items, find, pull, processes, triggers)

**Interfaces:**
- Consumes: Task 1 contract types/args (`orchestrator/backend.ts`), `ExpectedError` (`shared/expected-error.ts`), Task 2 mappers (`toJobCore`, `toQueueItemCoreFromItem`, `toQueueItemCoreFromMatch`, `toQueueCore`, `toProcessCore`, `toTriggerCore`, `uipathFault`, `toUiPathLogDigest`), `normalizeOutput` (`copilot/output-schema.ts`, for job `output`), and from `uipath/uipath.ts`: `fetchJobLogs`, `fetchJobLogsForKeys`, `fetchJobsForKeys`, `fetchJobVideoUrl`, `getQueueDefinitionName`, `getQueueItem`, `jobDeepLink`, `listQueueDefinitions`, `listQueueItems`, `listRecentJobs`, `listReleases`, `listTriggers`, `resolveFolder`, `resolveOrgUnitId`, `scopeForEnv`, `searchQueueItemsByOrderId` (+ types `FolderScope`, `JobLogFilter`, `JobLookup`, `UiPathJob`); `buildFaultedJobIssue` (`uipath/faults.ts`); `parseQueueItemUrl` (`uipath/queue.ts`, the ONLY thing the backend imports from that file — it reimplements pull/list/find on the `uipath.ts` primitives, so Task 5 can delete the rest of `queue.ts`: `pullQueueItem`, `PullQueueItemArgs`/`Result`, `listQueue` + `ListQueue*`, `resolvePull`, `resolveQueueDefId`, `findOrderQueueItems` + `FindOrderQueueItems*`, `str`, the old `QueueKind`, and the whole of `queue.test.ts`, whose five `findOrderQueueItems` cases are re-pinned against `uipathFindOrderQueueItems` here).
- Produces: `memberFilename(sc: Record<string, unknown>): string` and `toPortalRef(p: ResolvedPortal): PortalRef` in `src/orchestrator/portal.ts` (Task 11's `apexPullQueueItem` consumes both); in `src/uipath/backend.ts`: `uipathListJobs`, `uipathGetJobs`, `uipathGetJobLogs`, `uipathListQueues`, `uipathListQueueItems`, `uipathFindOrderQueueItems`, `uipathPullQueueItem`, `uipathListProcesses`, `uipathListTriggers`, `uipathBuildFaultedJobIssue`, each with the exact signature of the matching `OrchestratorBackend` method. Task 4 appends the write/payload functions and the `export const uipathBackend: OrchestratorBackend = { name: "uipath", … }` assembly at the bottom of this file.

Semantics (each pinned by a test below): `batch` decides whether one id's lookup/log-fetch failure throws (`batch:false`) or lands on that entry's `error` (`batch:true`); `includeLogDigest` is best-effort (failure → `logDigestError`, `fault` null); `logDigest` is the UiPath subtype incl. `transactionOutcome`; `list_*` reads use the env's org-unit id and treat an explicit `folder` as a folder-path override only (what `find_order_queue_items` always did); queue items from `list_queue_items` carry `specificContent: null`; `pull` forces `IsApproved=false` and reports a failed queue-name fallback as `queueNameError`; a queue item's `jobs` is `[{ orchestrator: "uipath", id: executorJobKey, state: null, neverStarted: null }]` when a robot picked it up, else `[]`.

- [ ] **Step 1: Write the failing tests**

`src/orchestrator/portal.test.ts`:
```ts
import { describe, expect, test } from "bun:test";
import { resolvePortal } from "../mcp/reference.js";
import { memberFilename, toPortalRef } from "./portal.js";

describe("memberFilename", () => {
  test("first-last, lowercased and trimmed", () => {
    expect(memberFilename({ MemberFirstName: " Jane ", MemberLastName: "DOE" })).toBe(
      "jane-doe.yaml",
    );
  });

  test("one missing name part leaves no dangling dash", () => {
    expect(memberFilename({ MemberLastName: "Doe" })).toBe("doe.yaml");
    expect(memberFilename({ MemberFirstName: "Jane" })).toBe("jane.yaml");
  });

  test("falls back to the full name (commas dropped, spaces dashed), then to 'unknown'", () => {
    expect(memberFilename({ MemberFullName: "Doe, Jane Q" })).toBe("doe-jane-q.yaml");
    expect(memberFilename({})).toBe("unknown.yaml");
    expect(memberFilename({ MemberFirstName: 5, MemberLastName: null })).toBe("unknown.yaml");
  });
});

describe("toPortalRef", () => {
  test("projects exactly the five portal fields for a hit", () => {
    const ref = toPortalRef(resolvePortal("PMG"));
    expect(Object.keys(ref).sort()).toEqual([
      "buildArtifact",
      "family",
      "key",
      "matched",
      "portalDir",
    ]);
    expect(ref).toMatchObject({ matched: true, key: "PMG", buildArtifact: "pmg-auth.cjs" });
  });

  test("a miss carries the empty sentinels", () => {
    expect(toPortalRef(resolvePortal("NO SUCH PORTAL"))).toEqual({
      matched: false,
      key: "",
      portalDir: "",
      buildArtifact: "",
      family: "Standalone",
    });
  });
});
```

`src/uipath/backend.test.ts`:
```ts
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { resetConfigCache } from "../config/config.js";

const envCreds = (name: string) => ({
  be: `https://be.${name}.example.com`,
  email: `${name}@example.com`,
  password: "pw",
});

const FIXTURE = {
  copilot: { sessionCache: false, prod: envCreds("prod"), pre_prod: envCreds("preprod") },
  uipath: {
    orchestratorUrl: "https://cloud.uipath.com/myorg/mytenant/orchestrator_",
    bearer: "test-bearer",
  },
};

let dir: string;
let prevConfig: string | undefined;

beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), "copilot-mcp-backend-"));
  prevConfig = process.env["COPILOT_MCP_CONFIG"];
  process.env["COPILOT_MCP_CONFIG"] = join(dir, "config.json");
  writeFileSync(join(dir, "config.json"), JSON.stringify(FIXTURE));
  resetConfigCache();
});

afterAll(() => {
  if (prevConfig === undefined) delete process.env["COPILOT_MCP_CONFIG"];
  else process.env["COPILOT_MCP_CONFIG"] = prevConfig;
  resetConfigCache();
  rmSync(dir, { recursive: true, force: true });
});

// One handler per test decides each request's response from its URL.
let handler: (url: URL) => Response;
let calls: URL[];
const realFetch = globalThis.fetch;

beforeEach(() => {
  calls = [];
  handler = () => json({ value: [] });
  globalThis.fetch = (async (input: string | URL | Request) => {
    const url = new URL(String(input));
    calls.push(url);
    return handler(url);
  }) as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = realFetch;
});

const json = (obj: unknown, status = 200): Response =>
  new Response(JSON.stringify(obj), { status });

import {
  uipathBuildFaultedJobIssue,
  uipathGetJobLogs,
  uipathGetJobs,
  uipathListJobs,
} from "./backend.js";

const KEY = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee";
const KEY_2 = "bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee";
const KEY_3 = "cccccccc-bbbb-cccc-dddd-eeeeeeeeeeee";

// The job key a /odata/Jobs or /odata/RobotLogs request is filtered on.
const keyOf = (url: URL): string =>
  url.searchParams.get("$filter")?.match(/(?:Job)?Key eq (\S+)/)?.[1] ?? "";

const jobRow = (key: string, overrides: Record<string, unknown> = {}): Record<string, unknown> => ({
  Id: 1,
  Key: key,
  State: "Faulted",
  ReleaseName: "PMG auth submit",
  CreationTime: "2026-07-01T10:00:00Z",
  EndTime: "2026-07-01T10:01:00Z",
  ...overrides,
});

// Jobs by key (missing = no rows), logs by key (missing = no rows); a value of "error" = HTTP 500.
const serve = (
  jobs: Record<string, Record<string, unknown> | "error">,
  logs: Record<string, unknown[] | "error"> = {},
): void => {
  handler = (url) => {
    const key = keyOf(url);
    if (url.pathname.endsWith("/odata/Jobs")) {
      const row = jobs[key];
      if (row === "error") return json({ message: "boom" }, 500);
      return json({ value: row ? [row] : [] });
    }
    if (url.pathname.endsWith("/odata/RobotLogs")) {
      const rows = logs[key];
      if (rows === "error") return json({ message: "boom" }, 500);
      return json({ value: rows ?? [] });
    }
    return json({ value: [] });
  };
};

const logRow = (Level: string, Message: string, extra: Record<string, unknown> = {}) => ({
  Level,
  Message,
  TimeStamp: "2026-07-01T10:00:30Z",
  ...extra,
});

describe("uipathListJobs", () => {
  test("maps rows to JobCores with a per-job link and the resolved folder", async () => {
    handler = () => json({ value: [jobRow(KEY, { ProcessVersion: "1.2.3" })] });
    const res = await uipathListJobs({ env: "prod", top: 5 });
    expect(res.folder).toBe("Authorization");
    expect(res.jobs).toHaveLength(1);
    expect(res.jobs[0]).toMatchObject({
      orchestrator: "uipath",
      id: KEY,
      state: "Faulted",
      terminal: true,
      durationMs: 60_000,
      process: { name: "PMG auth submit", version: "1.2.3" },
    });
    expect(res.jobs[0]?.link).toContain(KEY);
  });

  test("an explicit folder wins over the env default", async () => {
    const res = await uipathListJobs({ env: "prod", top: 5, folder: "Some Folder" });
    expect(res.folder).toBe("Some Folder");
  });
});

describe("uipathGetJobs", () => {
  const base = { env: "prod" as const, includeLogDigest: false };

  test("a single id whose lookup fails fails the call", async () => {
    serve({ [KEY]: "error" });
    await expect(uipathGetJobs({ ...base, jobIds: [KEY], batch: false })).rejects.toThrow(/500/);
  });

  test("a batch keeps going: the failing id carries error, a missing id is found:false", async () => {
    serve({ [KEY]: jobRow(KEY), [KEY_2]: "error" });
    const res = await uipathGetJobs({ ...base, jobIds: [KEY, KEY_2, KEY_3], batch: true });
    expect(res.entries.map((e) => [e.id, e.found])).toEqual([
      [KEY, true],
      [KEY_2, false],
      [KEY_3, false],
    ]);
    expect(res.entries[0]?.job?.id).toBe(KEY);
    expect(res.entries[1]?.error).toMatch(/500/);
    expect(res.entries[2]).toEqual({ id: KEY_3, found: false });
  });

  test("a single id that resolves to nothing is found:false, not an error", async () => {
    serve({});
    const res = await uipathGetJobs({ ...base, jobIds: [KEY], batch: false });
    expect(res.entries).toEqual([{ id: KEY, found: false }]);
  });

  test("without includeLogDigest no logs are fetched and fault stays null", async () => {
    serve({ [KEY]: jobRow(KEY) });
    const res = await uipathGetJobs({ ...base, jobIds: [KEY], batch: false });
    expect(res.entries[0]?.job?.fault).toBeNull();
    expect(res.entries[0]).not.toHaveProperty("logDigest");
    expect(calls.some((u) => u.pathname.endsWith("/odata/RobotLogs"))).toBe(false);
  });

  test("includeLogDigest: fault from the logs, digest with the queue transaction outcome", async () => {
    const ended = JSON.stringify({
      transactionState: "Ended",
      processingExceptionType: "BusinessException",
      processingExceptionReason: "missing member id",
    });
    serve(
      { [KEY]: jobRow(KEY) },
      {
        [KEY]: [
          logRow("Error", "System.InvalidOperationException: submit rejected"),
          logRow("Info", "Transaction Ended", { RawMessage: ended }),
        ],
      },
    );
    const res = await uipathGetJobs({
      ...base,
      includeLogDigest: true,
      jobIds: [KEY],
      batch: false,
    });
    const entry = res.entries[0];
    expect(entry?.job?.fault).toEqual({
      message: "System.InvalidOperationException: submit rejected",
      signature: "system.invalidoperationexception: submit rejected",
      class: "System.InvalidOperationException",
    });
    expect(entry?.logDigest).toMatchObject({
      fetched: 2,
      transactionOutcome: {
        processingExceptionType: "BusinessException",
        processingExceptionReason: "missing member id",
      },
    });
    const logsCall = calls.find((u) => u.pathname.endsWith("/odata/RobotLogs"));
    expect(logsCall?.searchParams.get("$select")).toContain("RawMessage");
  });

  test("a Successful job still gets a digest but no fault", async () => {
    serve({ [KEY]: jobRow(KEY, { State: "Successful" }) }, { [KEY]: [logRow("Info", "done")] });
    const res = await uipathGetJobs({
      ...base,
      includeLogDigest: true,
      jobIds: [KEY],
      batch: false,
    });
    expect(res.entries[0]?.job?.fault).toBeNull();
    expect(res.entries[0]?.logDigest?.fetched).toBe(1);
  });

  test("a logs failure degrades to logDigestError; the job is still returned, even for a single id", async () => {
    serve({ [KEY]: jobRow(KEY) }, { [KEY]: "error" });
    const res = await uipathGetJobs({
      ...base,
      includeLogDigest: true,
      jobIds: [KEY],
      batch: false,
    });
    const entry = res.entries[0];
    expect(entry?.found).toBe(true);
    expect(entry?.job?.id).toBe(KEY);
    expect(entry?.job?.fault).toBeNull();
    expect(entry?.logDigestError).toMatch(/500/);
    expect(entry).not.toHaveProperty("logDigest");
  });

  test("details.output: parsed OutputArguments without the token; null when absent", async () => {
    serve({
      [KEY]: jobRow(KEY, {
        OutputArguments: JSON.stringify({ out_status: "ok", token: "jwt.secret" }),
      }),
      [KEY_2]: jobRow(KEY_2),
    });
    const res = await uipathGetJobs({ ...base, jobIds: [KEY, KEY_2], batch: true });
    const withOutput = res.entries[0]?.job?.details as { output?: unknown };
    expect(JSON.stringify(withOutput.output)).toContain("ok");
    expect(JSON.stringify(res)).not.toContain("jwt.secret");
    expect((res.entries[1]?.job?.details as { output?: unknown }).output).toBeNull();
  });

  test("progress counts every id off, in a batch", async () => {
    serve({ [KEY]: jobRow(KEY), [KEY_2]: jobRow(KEY_2), [KEY_3]: jobRow(KEY_3) });
    const seen: Array<[number, number]> = [];
    await uipathGetJobs({ ...base, jobIds: [KEY, KEY_2, KEY_3], batch: true }, (done, total) =>
      seen.push([done, total]),
    );
    expect(seen).toEqual([
      [1, 3],
      [2, 3],
      [3, 3],
    ]);
  });
});

describe("uipathGetJobLogs", () => {
  const base = {
    env: "prod" as const,
    filter: { onlyFailures: false },
    fullMessages: false,
    includeRawFields: false,
    includeVideo: false,
  };

  test("a single id whose logs fail fails the call; a batch puts it on that entry", async () => {
    serve({}, { [KEY]: "error", [KEY_2]: [logRow("Info", "ok")] });
    await expect(uipathGetJobLogs({ ...base, jobIds: [KEY], batch: false })).rejects.toThrow(/500/);
    const res = await uipathGetJobLogs({ ...base, jobIds: [KEY, KEY_2], batch: true });
    expect(res.entries[0]?.error).toMatch(/500/);
    expect(res.entries[0]?.logs).toEqual([]);
    expect(res.entries[1]).toMatchObject({ jobId: KEY_2, returned: 1, truncated: false });
  });

  test("messages truncate unless fullMessages; RawMessage never leaks, raw parses only on request", async () => {
    const long = "x".repeat(500);
    const raw = JSON.stringify({ transactionState: "Ended", queueName: "PMG" });
    serve({}, { [KEY]: [logRow("Info", long, { RawMessage: raw })] });

    const plain = await uipathGetJobLogs({ ...base, jobIds: [KEY], batch: false });
    const row = plain.entries[0]?.logs[0];
    expect(row?.Message).toBe(`${"x".repeat(400)}…`);
    expect(row).not.toHaveProperty("RawMessage");
    expect(row).not.toHaveProperty("raw");

    const rich = await uipathGetJobLogs({
      ...base,
      jobIds: [KEY],
      batch: false,
      fullMessages: true,
      includeRawFields: true,
    });
    const richRow = rich.entries[0]?.logs[0];
    expect(richRow?.Message).toBe(long);
    expect(richRow).not.toHaveProperty("RawMessage");
    expect(richRow?.raw).toMatchObject({ transactionState: "Ended", queueName: "PMG" });
  });

  test("totalMatching above the 500-row window marks the entry truncated", async () => {
    handler = () => json({ value: [logRow("Info", "a")], "@odata.count": 600 });
    const res = await uipathGetJobLogs({ ...base, jobIds: [KEY], batch: false });
    expect(res.entries[0]).toMatchObject({ totalMatching: 600, truncated: true });
  });

  test("includeVideo: a recording becomes a url VideoRef; none means no video key", async () => {
    const withVideo = "https://cdn.example.com/jobs/recording.webm";
    handler = (url) =>
      url.pathname.includes("/VideoRecording/")
        ? json(url.pathname.includes(KEY) ? [{ uri: withVideo }] : [])
        : json({ value: [] });
    const res = await uipathGetJobLogs({
      ...base,
      includeVideo: true,
      jobIds: [KEY, KEY_2],
      batch: true,
    });
    expect(res.entries[0]?.video).toEqual({ kind: "url", url: withVideo });
    expect(res.entries[1]).not.toHaveProperty("video");
    expect(res.entries[1]).not.toHaveProperty("videoError");
  });

  test("a video failure is videoError and never fails the logs", async () => {
    handler = (url) =>
      url.pathname.includes("/VideoRecording/")
        ? json({ message: "boom" }, 500)
        : json({ value: [logRow("Info", "a")] });
    const res = await uipathGetJobLogs({
      ...base,
      includeVideo: true,
      jobIds: [KEY],
      batch: false,
    });
    expect(res.entries[0]?.returned).toBe(1);
    expect(res.entries[0]?.videoError).toMatch(/500/);
  });
});

describe("uipathBuildFaultedJobIssue", () => {
  test("builds the payload from the job and its logs, honoring repo/labels", async () => {
    serve({ [KEY]: jobRow(KEY) }, { [KEY]: [logRow("Error", "Selector not found")] });
    const res = await uipathBuildFaultedJobIssue({
      env: "prod",
      jobId: KEY,
      repo: "acme/repo",
      labels: ["rpa"],
    });
    expect(res.found).toBe(true);
    expect(res.repo).toBe("acme/repo");
    expect(res.labels).toEqual(["rpa"]);
    expect(res.faultSignature).toBe("selector not found");
    expect(res.searchQuery).toContain("repo:acme/repo");
  });

  test("an unknown job still yields a stub payload with found:false", async () => {
    serve({});
    const res = await uipathBuildFaultedJobIssue({ env: "prod", jobId: KEY });
    expect(res.found).toBe(false);
    expect(res.faultSignature).toBe("job ended in state faulted");
  });
});
```

`src/uipath/backend-queues.test.ts`:
```ts
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { resetConfigCache } from "../config/config.js";

const envCreds = (name: string) => ({
  be: `https://be.${name}.example.com`,
  email: `${name}@example.com`,
  password: "pw",
});

const FIXTURE = {
  copilot: { sessionCache: false, prod: envCreds("prod"), pre_prod: envCreds("preprod") },
  uipath: {
    orchestratorUrl: "https://cloud.uipath.com/myorg/mytenant/orchestrator_",
    bearer: "test-bearer",
  },
};

let dir: string;
let prevConfig: string | undefined;

beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), "copilot-mcp-backend-queues-"));
  prevConfig = process.env["COPILOT_MCP_CONFIG"];
  process.env["COPILOT_MCP_CONFIG"] = join(dir, "config.json");
  writeFileSync(join(dir, "config.json"), JSON.stringify(FIXTURE));
  resetConfigCache();
});

afterAll(() => {
  if (prevConfig === undefined) delete process.env["COPILOT_MCP_CONFIG"];
  else process.env["COPILOT_MCP_CONFIG"] = prevConfig;
  resetConfigCache();
  rmSync(dir, { recursive: true, force: true });
});

// One handler per test decides each request's response from its URL.
let handler: (url: URL) => Response;
let calls: URL[];
const realFetch = globalThis.fetch;

beforeEach(() => {
  calls = [];
  handler = () => json({ value: [] });
  globalThis.fetch = (async (input: string | URL | Request) => {
    const url = new URL(String(input));
    calls.push(url);
    return handler(url);
  }) as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = realFetch;
});

const json = (obj: unknown, status = 200): Response =>
  new Response(JSON.stringify(obj), { status });

import { resolvePortal } from "../mcp/reference.js";
import { ExpectedError } from "../shared/expected-error.js";
import {
  uipathFindOrderQueueItems,
  uipathListProcesses,
  uipathListQueueItems,
  uipathListQueues,
  uipathListTriggers,
  uipathPullQueueItem,
} from "./backend.js";

const ORDER_UID = "test-order-uid-1";
const PMG = resolvePortal("PMG");

// Exactly the fields uipath.ts's QUEUE_ITEM_SELECT projects for — the tier-1 query's $select.
const QUEUE_ITEM_SELECT_FIELDS =
  "Id,Reference,Status,ExecutorJobKey,ProcessingExceptionType,RetryNumber,CreationTime,SpecificContent,QueueDefinitionId";

const queueItemRaw = (overrides: Record<string, unknown> = {}): Record<string, unknown> => ({
  Id: 1,
  Reference: "REF-1",
  Status: "New",
  ExecutorJobKey: "",
  ProcessingExceptionType: "",
  RetryNumber: 0,
  CreationTime: "2026-08-26T00:00:00.000Z",
  SpecificContent: {
    orderUid: ORDER_UID,
    token: "secret-jwt",
    MemberID: "m1",
    MemberFullName: "Doe, Jane",
  },
  QueueDefinitionId: PMG.submitQueueDefId,
  QueueDefinition: { Name: "PMG auth submit queue" },
  ...overrides,
});

const queueItemCalls = (): URL[] => calls.filter((u) => u.pathname.endsWith("/odata/QueueItems"));

describe("uipathListQueueItems", () => {
  const base = { env: "prod" as const, queueKind: "submit" as const, top: 10 };
  const filterOf = (u: URL | undefined): string => u?.searchParams.get("$filter") ?? "";

  test("a portal key resolves to its submit queue; sync kind picks the sync queue", async () => {
    handler = () => json({ value: [queueItemRaw()] });
    const submit = await uipathListQueueItems({ ...base, queue: "PMG" });
    expect(filterOf(queueItemCalls()[0])).toContain(`QueueDefinitionId eq ${PMG.submitQueueDefId}`);
    expect(submit).toMatchObject({ queueId: String(PMG.submitQueueDefId), queueName: "PMG" });

    await uipathListQueueItems({ ...base, queue: "pmg", queueKind: "sync" });
    expect(filterOf(queueItemCalls()[1])).toContain(`QueueDefinitionId eq ${PMG.syncQueueDefId}`);
  });

  test("a numeric queue id is used as-is (dev-clone queues); unregistered names come from the item", async () => {
    handler = () => json({ value: [queueItemRaw({ QueueDefinitionId: 999 })] });
    const res = await uipathListQueueItems({ ...base, queue: "999" });
    expect(filterOf(queueItemCalls()[0])).toContain("QueueDefinitionId eq 999");
    expect(res.queueId).toBe("999");
    expect(res.queueName).toBe("PMG");
  });

  test("an unknown portal key is an ExpectedError that lists the known portals", async () => {
    const err = await uipathListQueueItems({ ...base, queue: "NOPE" }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ExpectedError);
    expect((err as Error).message).toContain("PMG");
    expect(calls).toHaveLength(0);
  });

  test("a status filter reaches the query; items are PHI-light cores without specificContent", async () => {
    handler = () => json({ value: [queueItemRaw({ Status: "Failed", RetryNumber: 2 })] });
    const res = await uipathListQueueItems({ ...base, queue: "PMG", status: "Failed" });
    expect(filterOf(queueItemCalls()[0])).toContain("Status eq 'Failed'");
    expect(res.items[0]).toMatchObject({
      orchestrator: "uipath",
      itemId: "1",
      reference: "REF-1",
      orderUid: ORDER_UID,
      status: "Failed",
      specificContent: null,
      details: { retryNumber: 2, memberId: "m1", member: "Doe, Jane" },
    });
    expect(JSON.stringify(res)).not.toContain("secret-jwt");
  });
});

describe("uipathFindOrderQueueItems", () => {
  const base = { env: "prod" as const, orderUid: ORDER_UID, top: 50, includeSpecificContent: true };

  test("tier-1 query is time-bounded ($filter) and projected ($select)", async () => {
    handler = () => json({ value: [queueItemRaw()] });
    await uipathFindOrderQueueItems({ ...base, since: "2026-08-25" });
    expect(queueItemCalls()).toHaveLength(1);
    const params = queueItemCalls()[0]?.searchParams;
    expect(params?.get("$select")).toBe(QUEUE_ITEM_SELECT_FIELDS);
    expect(params?.get("$filter")).toContain("contains(SpecificData,");
    expect(params?.get("$filter")).toContain("CreationTime gt 2026-08-25T00:00:00.000Z");
  });

  test("a since-bounded search that finds nothing retries unbounded and says so", async () => {
    handler = () => json({ value: [] });
    const res = await uipathFindOrderQueueItems({ ...base, since: "2026-08-25" });
    expect(queueItemCalls()).toHaveLength(2);
    expect(queueItemCalls()[1]?.searchParams.get("$filter")).not.toContain("CreationTime");
    expect(res.notes.some((n) => n.includes("retried without the time bound"))).toBe(true);
  });

  test("items map to cores: job attempts only for picked-up items, exception classified", async () => {
    handler = () =>
      json({
        value: [
          queueItemRaw({
            Id: 1,
            ExecutorJobKey: "k1",
            Status: "Failed",
            ProcessingExceptionType: "BusinessException",
          }),
          queueItemRaw({ Id: 2, ExecutorJobKey: "" }),
        ],
      });
    const res = await uipathFindOrderQueueItems(base);
    expect(res.searchMode).toBe("contains");
    expect(res.scanned).toBe(2);
    expect(res.items[0]?.jobs).toEqual([
      { orchestrator: "uipath", id: "k1", state: null, neverStarted: null },
    ]);
    expect(res.items[0]?.exceptionType).toBe("business");
    expect(res.items[0]?.queueName).toBe("PMG");
    expect(res.items[1]?.jobs).toEqual([]);
    expect(res.items[1]?.exceptionType).toBeNull();
  });

  test("specificContent is returned with the token redacted", async () => {
    handler = () =>
      json({
        value: [
          queueItemRaw({
            SpecificContent: { orderUid: ORDER_UID, MemberID: "m1", token: "jwt.secret" },
          }),
        ],
      });
    const res = await uipathFindOrderQueueItems(base);
    expect(res.items[0]?.specificContent).toEqual({
      orderUid: ORDER_UID,
      MemberID: "m1",
      token: "[redacted]",
    });
    expect(JSON.stringify(res)).not.toContain("jwt.secret");
  });

  test("includeSpecificContent: false drops it from the projection (null)", async () => {
    handler = () => json({ value: [queueItemRaw()] });
    const res = await uipathFindOrderQueueItems({ ...base, includeSpecificContent: false });
    expect(res.items[0]?.specificContent).toBeNull();
    expect(res.items[0]?.orderUid).toBe(ORDER_UID);
  });
});

describe("uipathPullQueueItem", () => {
  const sc = {
    orderUid: ORDER_UID,
    IsApproved: true,
    MemberFirstName: "Jane",
    MemberLastName: "Doe",
  };
  const serveItem = (row: Record<string, unknown>): void => {
    handler = (url) =>
      url.pathname.includes("/odata/QueueItems(")
        ? json(row)
        : url.pathname.includes("/odata/QueueDefinitions(")
          ? json({ Name: "PMG auth submit queue" })
          : json({ value: [] });
  };

  test("by itemId + env: IsApproved forced false, portal resolved, filename from the member", async () => {
    serveItem(queueItemRaw({ Id: 5, SpecificContent: sc }));
    const res = await uipathPullQueueItem({ env: "pre_prod", itemId: "5" });
    expect(res.env).toBe("pre_prod");
    expect(res.isApprovedForced).toBe(true);
    expect(res.item.specificContent?.["IsApproved"]).toBe(false);
    expect(res.item).toMatchObject({ itemId: "5", queueName: "PMG", orderUid: ORDER_UID });
    expect(res.portal).toMatchObject({ matched: true, key: "PMG", buildArtifact: "pmg-auth.cjs" });
    expect(res.suggestedFilename).toBe("jane-doe.yaml");
    expect(calls[0]?.pathname).toContain("/odata/QueueItems(5)");
  });

  test("by url: env and folder come from the fid", async () => {
    serveItem(queueItemRaw({ Id: 7, SpecificContent: sc }));
    const url =
      "https://cloud.uipath.com/myorg/mytenant/orchestrator_/queues/38592/transactions(sidepanel:sidepanel/transactions/7/details)?fid=434039";
    const res = await uipathPullQueueItem({ url });
    expect(res.env).toBe("pre_prod");
    expect(res.item.itemId).toBe("7");
    expect(calls[0]?.pathname).toContain("/odata/QueueItems(7)");
  });

  test("an item with no usable queue name falls back to the QueueDefinition lookup", async () => {
    serveItem(queueItemRaw({ SpecificContent: sc, QueueDefinition: null, Name: "" }));
    const res = await uipathPullQueueItem({ env: "prod", itemId: "1" });
    expect(res.item.queueName).toBe("PMG");
    expect(res.portal.matched).toBe(true);
    expect(res).not.toHaveProperty("queueNameError");
  });

  test("a failing QueueDefinition lookup surfaces as queueNameError, the pull still succeeds", async () => {
    handler = (url) =>
      url.pathname.includes("/odata/QueueItems(")
        ? json(queueItemRaw({ SpecificContent: sc, QueueDefinition: null, Name: "" }))
        : json({ message: "boom" }, 500);
    const res = await uipathPullQueueItem({ env: "prod", itemId: "1" });
    expect(res.queueNameError).toMatch(/500/);
    expect(res.portal.matched).toBe(false);
  });

  test("itemId without env, or a non-numeric itemId, is an ExpectedError before any request", async () => {
    for (const args of [
      { itemId: "5" },
      { env: "prod" as const, itemId: "abc" },
      { env: "prod" as const },
    ]) {
      const err = await uipathPullQueueItem(args).catch((e: unknown) => e);
      expect(err).toBeInstanceOf(ExpectedError);
    }
    expect(calls).toHaveLength(0);
  });
});

describe("uipathListQueues / uipathListProcesses / uipathListTriggers", () => {
  test("queues and processes map to cores keyed the way the write tools take them", async () => {
    handler = (url) =>
      url.pathname.endsWith("/odata/QueueDefinitions")
        ? json({
            value: [{ Id: 3, Name: "PMG auth submit queue", Description: "d", CreationTime: "t" }],
          })
        : json({
            value: [
              {
                Id: 8,
                Key: "release-guid",
                Name: "PMG auth submit",
                ProcessKey: "pkg",
                ProcessVersion: "1.2.3",
                IsLatestVersion: true,
              },
            ],
          });
    const queues = await uipathListQueues({ env: "prod", top: 10 });
    expect(queues[0]).toMatchObject({ id: "3", name: "PMG auth submit queue" });
    const processes = await uipathListProcesses({ env: "prod", top: 10 });
    expect(processes[0]).toMatchObject({ id: "release-guid", package: "pkg", version: "1.2.3" });
  });

  test("triggers: queueId keeps only triggers watching that queue (time triggers never match)", async () => {
    const row = (Id: number, QueueDefinitionId: number) => ({
      Id,
      Name: `t${Id}`,
      Enabled: true,
      ReleaseId: 8,
      ReleaseKey: "release-guid",
      ReleaseName: "PMG auth submit",
      QueueDefinitionId,
      QueueDefinitionName: "",
      ItemsActivationThreshold: 1,
      MaxJobsForActivation: 1,
    });
    handler = () => json({ value: [row(1, 77), row(2, 88), row(3, 0)] });

    const all = await uipathListTriggers({ env: "prod", top: 10 });
    expect(all.map((t) => [t.id, t.kind])).toEqual([
      ["1", "queue"],
      ["2", "queue"],
      ["3", "time"],
    ]);
    const only = await uipathListTriggers({ env: "prod", top: 10, queueId: "77" });
    expect(only.map((t) => t.id)).toEqual(["1"]);
    expect(await uipathListTriggers({ env: "prod", top: 10, queueId: "0" })).toEqual([]);
  });
});
```

- [ ] **Step 2: Run them to verify they fail**
Run: `bun test src/orchestrator/portal.test.ts src/uipath/backend.test.ts src/uipath/backend-queues.test.ts` — Expected: FAIL (`Cannot find module './portal.js'` / `'./backend.js'`)

- [ ] **Step 3: Implement**

`src/orchestrator/portal.ts` (`memberFilename` is `src/uipath/queue.ts:71-80` verbatim, now exported; `toPortalRef` is the 5-field projection that was inline at `queue.ts:154-160`):
```ts
// Portal helpers shared by every backend's pull_queue_item.

import type { ResolvedPortal } from "../mcp/reference.js";
import type { PortalRef } from "./types.js";

// Build a PHI-light filename slug from a queue item's member name.
export function memberFilename(sc: Record<string, unknown>): string {
  const s = (v: unknown): string => (typeof v === "string" ? v.trim().toLowerCase() : "");
  const first = s(sc["MemberFirstName"]);
  const last = s(sc["MemberLastName"]);
  let slug = `${first}-${last}`.replace(/^-|-$/g, "");
  if (!slug) slug = s(sc["MemberFullName"]) || "unknown";
  slug = slug.replace(/,/g, "").replace(/\s+/g, "-");
  return `${slug}.yaml`;
}

export const toPortalRef = (p: ResolvedPortal): PortalRef => ({
  matched: p.matched,
  key: p.key,
  portalDir: p.portalDir,
  buildArtifact: p.buildArtifact,
  family: p.family,
});
```

`src/uipath/queue.ts` — the old `pullQueueItem` (still used by `server.ts` until Task 5) switches to the shared helpers; delete the local `memberFilename` (its one-line comment, the 10-line function and the blank line after it, between `parseQueueItemUrl` and the "Where to pull a queue item from" comment):
```diff
 } from "../mcp/reference.js";
+import { memberFilename, toPortalRef } from "../orchestrator/portal.js";
 import { guardQueueItemSafety } from "../orchestrator/safety.js";
 import type { PortalRef } from "../orchestrator/types.js";
```
```diff
-// Build a PHI-light filename slug from a queue item's member name.
-function memberFilename(sc: Record<string, unknown>): string {
-  const s = (v: unknown): string => (typeof v === "string" ? v.trim().toLowerCase() : "");
-  const first = s(sc["MemberFirstName"]);
-  const last = s(sc["MemberLastName"]);
-  let slug = `${first}-${last}`.replace(/^-|-$/g, "");
-  if (!slug) slug = s(sc["MemberFullName"]) || "unknown";
-  slug = slug.replace(/,/g, "").replace(/\s+/g, "-");
-  return `${slug}.yaml`;
-}
-
```
```diff
-    portal: {
-      matched: portal.matched,
-      key: portal.key,
-      portalDir: portal.portalDir,
-      buildArtifact: portal.buildArtifact,
-      family: portal.family,
-    },
+    portal: toPortalRef(portal),
```

`src/uipath/backend.ts`:
```ts
// UiPath implementation of the orchestrator read surface (OrchestratorBackend, see
// orchestrator/backend.ts): the handler logic that used to live in server.ts, returning
// the backend-neutral cores. Task 4 adds the write/payload functions and assembles
// `uipathBackend` at the bottom.

import type { Env } from "../config/config.js";
import { normalizeOutput } from "../copilot/output-schema.js";
import {
  envForFolderId,
  folderNameForId,
  normalizeQueueName,
  PORTALS,
  resolvePortal,
  resolvePortalByDefId,
} from "../mcp/reference.js";
import type {
  FaultedJobIssueResult,
  FaultIssueArgs,
  FindOrderArgs,
  FindOrderResult,
  GetJobLogsArgs,
  GetJobLogsResult,
  GetJobsArgs,
  GetJobsResult,
  JobEntry,
  JobLogsEntry,
  ListJobsArgs,
  ListJobsResult,
  ListProcessesArgs,
  ListQueueItemsArgs,
  ListQueueItemsResult,
  ListQueuesArgs,
  ListTriggersArgs,
  LogRow,
  PullQueueItemArgs,
  PullQueueItemResult,
  QueueKind,
} from "../orchestrator/backend.js";
import { truncate } from "../orchestrator/log-digest.js";
import { memberFilename, toPortalRef } from "../orchestrator/portal.js";
import { guardQueueItemSafety } from "../orchestrator/safety.js";
import type { ProcessCore, QueueCore, TriggerCore } from "../orchestrator/types.js";
import { ExpectedError } from "../shared/expected-error.js";
import type { StepProgress } from "../shared/util.js";
import { buildFaultedJobIssue } from "./faults.js";
import {
  toJobCore,
  toProcessCore,
  toQueueCore,
  toQueueItemCoreFromItem,
  toQueueItemCoreFromMatch,
  toTriggerCore,
  toUiPathLogDigest,
  uipathFault,
} from "./map.js";
import { parseQueueItemUrl } from "./queue.js";
import { parseRawMessage } from "./raw-message.js";
import {
  type FolderScope,
  fetchJobLogs,
  fetchJobLogsForKeys,
  fetchJobsForKeys,
  fetchJobVideoUrl,
  getQueueDefinitionName,
  getQueueItem,
  type JobLogFilter,
  type JobLookup,
  jobDeepLink,
  listQueueDefinitions,
  listQueueItems,
  listRecentJobs,
  listReleases,
  listTriggers,
  resolveFolder,
  resolveOrgUnitId,
  scopeForEnv,
  searchQueueItemsByOrderId,
  type UiPathJob,
} from "./uipath.js";

const toMessage = (e: unknown): string => (e instanceof Error ? e.message : String(e));

// Queue/process/trigger/item reads: the env's org-unit id always applies; an explicit
// `folder` overrides only the folder-path header (as find_order_queue_items always did).
const scopeFor = (env: Env, folder?: string): FolderScope => ({
  orgUnitId: resolveOrgUnitId(env),
  folderPath: resolveFolder(env, folder) ?? "",
});

// ---- jobs ------------------------------------------------------------------

export async function uipathListJobs(args: ListJobsArgs): Promise<ListJobsResult> {
  const folder = resolveFolder(args.env, args.folder);
  const jobs = await listRecentJobs(args.since, args.top, folder, args.processName);
  return {
    folder: folder ?? "",
    jobs: jobs.map((j) => toJobCore(j, { link: jobDeepLink(j.Key ?? "") })),
  };
}

// The job's parsed OutputArguments with token/callbackContext stripped (normalizeOutput);
// unparseable output degrades to its first 500 characters; none at all is null.
function parseJobOutput(job: UiPathJob): unknown {
  if (!job.OutputArguments) return null;
  try {
    const parsed = JSON.parse(job.OutputArguments) as Record<string, unknown>;
    return Object.fromEntries(normalizeOutput(parsed).fields);
  } catch {
    return job.OutputArguments.slice(0, 500);
  }
}

// One id's entry. A single explicit id fails like any other tool error; a batch keeps going
// and attaches the failure to just this entry. With includeLogDigest the logs are a
// best-effort attachment: a fetch failure lands on logDigestError and leaves fault null
// (a fault synthesized from zero logs would be a state-only signature that mis-dedupes).
async function jobEntry(
  id: string,
  lookup: JobLookup,
  args: GetJobsArgs,
  folder: string | undefined,
): Promise<JobEntry> {
  if (lookup.error && !args.batch) throw lookup.error;
  if (lookup.error) return { id, found: false, error: toMessage(lookup.error) };
  const { job } = lookup;
  if (!job) return { id, found: false };
  const core = toJobCore(job, { link: jobDeepLink(id), output: parseJobOutput(job) });
  if (!args.includeLogDigest) return { id, found: true, job: core };
  try {
    const logs = await fetchJobLogs(id, folder, { includeRawFields: true });
    const fault = job.State === "Successful" ? null : uipathFault(job, logs);
    return { id, found: true, job: { ...core, fault }, logDigest: toUiPathLogDigest(logs) };
  } catch (e) {
    return { id, found: true, job: core, logDigestError: toMessage(e) };
  }
}

export async function uipathGetJobs(
  args: GetJobsArgs,
  onProgress?: StepProgress,
): Promise<GetJobsResult> {
  const folder = resolveFolder(args.env, args.folder);
  const byId = await fetchJobsForKeys(args.jobIds, folder);
  // With includeLogDigest a 25-id batch is 50 Orchestrator calls — count them off as
  // each id finishes rather than going quiet for the whole batch.
  let done = 0;
  const entries = await Promise.all(
    args.jobIds.map(async (id) => {
      try {
        return await jobEntry(id, byId[id] ?? { job: null }, args, folder);
      } finally {
        done++;
        onProgress?.(done, args.jobIds.length, `diagnosed ${done}/${args.jobIds.length} job(s)`);
      }
    }),
  );
  return { folder: folder ?? "", entries };
}

export async function uipathGetJobLogs(
  args: GetJobLogsArgs,
  onProgress?: StepProgress,
): Promise<GetJobLogsResult> {
  const folder = resolveFolder(args.env, args.folder);
  const { minLevel, contains, onlyFailures, tail } = args.filter;
  const filter: JobLogFilter = {
    onlyFailures,
    includeRawFields: args.includeRawFields,
    ...(minLevel !== undefined ? { minLevel } : {}),
    ...(contains !== undefined ? { contains } : {}),
    ...(tail !== undefined ? { tail } : {}),
  };
  const byId = await fetchJobLogsForKeys(args.jobIds, folder, filter, onProgress);
  const entries = await Promise.all(
    args.jobIds.map(async (id): Promise<JobLogsEntry> => {
      const result = byId[id] ?? { logs: [], totalMatching: null };
      if (result.error && !args.batch) throw result.error;
      const { logs, totalMatching } = result;
      // RawMessage (the raw JSON text) never leaks into the output — only the parsed
      // `raw` object does, and only when includeRawFields was requested.
      const rows: LogRow[] = logs.map((l) => ({
        Level: l.Level,
        Message: args.fullMessages ? l.Message : truncate(l.Message),
        TimeStamp: l.TimeStamp,
        ...(args.includeRawFields ? { raw: parseRawMessage(l) } : {}),
      }));
      const entry: JobLogsEntry = {
        jobId: id,
        logs: rows,
        returned: rows.length,
        // truncated = the 500-row fetch window didn't cover every matching row
        // (a requested `tail` shorter than the total is not truncation).
        truncated: totalMatching !== null && totalMatching > 500,
        ...(totalMatching !== null ? { totalMatching } : {}),
        ...(result.error ? { error: toMessage(result.error) } : {}),
      };
      // Video is a secondary attachment, always best-effort — never fails the primary
      // logs result, single id or batch alike. No recording at all means no `video`.
      if (args.includeVideo) {
        try {
          const url = await fetchJobVideoUrl(id, folder);
          if (url) entry.video = { kind: "url", url };
        } catch (e) {
          entry.videoError = toMessage(e);
        }
      }
      return entry;
    }),
  );
  return { folder: folder ?? "", entries };
}

// ---- queues ----------------------------------------------------------------

export async function uipathListQueues(args: ListQueuesArgs): Promise<QueueCore[]> {
  const defs = await listQueueDefinitions(
    scopeFor(args.env, args.folder),
    args.nameContains ?? "",
    args.top,
  );
  return defs.map(toQueueCore);
}

// A numeric id is a QueueDefinitionId as-is; anything else is a portal key/alias whose
// submit/sync id comes from the static registry (prod ids only — pass the numeric id for
// dev-clone queues). `name` is the portal key when the registry knows the queue, else "".
function resolveQueue(queue: string, kind: QueueKind): { id: number; name: string } {
  if (/^\d+$/.test(queue)) {
    const id = Number(queue);
    return { id, name: resolvePortalByDefId(id).key };
  }
  const portal = resolvePortal(queue);
  if (!portal.matched) {
    const known = PORTALS.map((p) => p.key).join(", ");
    throw new ExpectedError(
      `unknown queue '${queue}'. Pass a numeric queue id or one of: ${known}`,
    );
  }
  const id = kind === "sync" ? portal.syncQueueDefId : portal.submitQueueDefId;
  if (!id) {
    throw new ExpectedError(
      `portal '${portal.key}' has no known ${kind} queue id — pass the numeric queue id`,
    );
  }
  return { id, name: portal.key };
}

// Browse a queue, newest first (PHI-light: no specificContent in the projection).
export async function uipathListQueueItems(
  args: ListQueueItemsArgs,
): Promise<ListQueueItemsResult> {
  const { id, name } = resolveQueue(args.queue, args.queueKind);
  const rows = await listQueueItems(id, scopeFor(args.env), args.status ?? "", args.top);
  return {
    queueId: String(id),
    queueName: name || normalizeQueueName(rows[0]?.name ?? ""),
    items: rows.map((r) =>
      toQueueItemCoreFromItem(r, { queueName: r.name, specificContent: null }),
    ),
  };
}

// Resolve orderUid -> the UiPath queue item(s) it flowed through. When a `since`-bounded
// search finds nothing, retry once without the bound (the item may predate it).
export async function uipathFindOrderQueueItems(args: FindOrderArgs): Promise<FindOrderResult> {
  const scope = scopeFor(args.env, args.folder);
  let q = await searchQueueItemsByOrderId(args.orderUid, scope, args.top, args.since);
  const notes = [...q.notes];
  if (args.since && q.matches.length === 0) {
    q = await searchQueueItemsByOrderId(args.orderUid, scope, args.top, undefined);
    notes.push(...q.notes);
    notes.push(`no queue item matched within since=${args.since}; retried without the time bound.`);
  }
  const items = q.matches.map((m) => {
    const core = toQueueItemCoreFromMatch(m, {
      queueName: resolvePortalByDefId(m.queueDefinitionId).key,
    });
    return args.includeSpecificContent ? core : { ...core, specificContent: null };
  });
  return {
    folder: scope.folderPath,
    searchMode: q.searchMode,
    scanned: q.scanned,
    items,
    notes,
    ...(q.searchError ? { searchError: q.searchError } : {}),
  };
}

// Where to pull from: an Orchestrator URL (fid + item id parsed out; env derived from the
// fid) or an explicit numeric itemId scoped by env (folderId omitted = the env default).
function resolvePull(args: PullQueueItemArgs): { itemId: number; env: Env; scope: FolderScope } {
  if (args.url) {
    const parsed = parseQueueItemUrl(args.url);
    return {
      itemId: parsed.txnId,
      env: envForFolderId(parsed.folderId),
      // Query by the URL's actual fid (correct even for folders we don't map).
      scope: { orgUnitId: parsed.folderId, folderPath: folderNameForId(parsed.folderId) },
    };
  }
  if (!args.env) {
    throw new ExpectedError("env is required when pulling by itemId (no url to derive it from)");
  }
  const itemId = Number(args.itemId);
  if (!Number.isInteger(itemId) || itemId <= 0) {
    throw new ExpectedError("UiPath pull_queue_item needs a url or a positive numeric itemId");
  }
  const scope: FolderScope = args.folderId
    ? { orgUnitId: args.folderId, folderPath: folderNameForId(args.folderId) }
    : scopeForEnv(args.env);
  return { itemId, env: args.env, scope };
}

// Fetch one queue item as a ready-to-run test payload.
// CRITICAL: IsApproved is forced false so a test run can never submit a real auth.
export async function uipathPullQueueItem(args: PullQueueItemArgs): Promise<PullQueueItemResult> {
  const { itemId, env, scope } = resolvePull(args);
  const item = await getQueueItem(itemId, scope);

  let queueName = normalizeQueueName(item.name);
  // Secondary "try harder" lookup — best-effort: a real failure here shouldn't fail the
  // whole pull when item.name is already a usable fallback, but it must still surface.
  let queueNameError: string | undefined;
  if ((!queueName || !resolvePortal(queueName).matched) && item.queueDefinitionId) {
    try {
      const resolved = await getQueueDefinitionName(item.queueDefinitionId, scope);
      if (resolved) queueName = normalizeQueueName(resolved);
    } catch (e) {
      queueNameError = toMessage(e);
    }
  }
  const specificContent = guardQueueItemSafety(item.specificContent, "force").specificContent;
  return {
    env,
    item: toQueueItemCoreFromItem(item, { queueName, specificContent }),
    portal: toPortalRef(resolvePortal(queueName)),
    isApprovedForced: true,
    suggestedFilename: memberFilename(specificContent),
    ...(queueNameError ? { queueNameError } : {}),
  };
}

// ---- processes / triggers --------------------------------------------------

export async function uipathListProcesses(args: ListProcessesArgs): Promise<ProcessCore[]> {
  const releases = await listReleases(
    scopeFor(args.env, args.folder),
    args.nameContains ?? "",
    args.top,
  );
  return releases.map(toProcessCore);
}

// ProcessSchedules has no server-side queue filter: `top` bounds the fetch, then the
// queueId filter applies to what came back.
export async function uipathListTriggers(args: ListTriggersArgs): Promise<TriggerCore[]> {
  const triggers = await listTriggers(scopeFor(args.env, args.folder), args.top);
  const cores = triggers.map(toTriggerCore);
  return args.queueId ? cores.filter((t) => t.queueId === args.queueId) : cores;
}

// ---- faulted-job issue -----------------------------------------------------

export function uipathBuildFaultedJobIssue(args: FaultIssueArgs): Promise<FaultedJobIssueResult> {
  return buildFaultedJobIssue(args.env, args.jobId, {
    folder: args.folder,
    repo: args.repo,
    labels: args.labels,
  });
}
```

- [ ] **Step 4: Run tests** — `bun test src/orchestrator/portal.test.ts src/uipath/backend.test.ts src/uipath/backend-queues.test.ts src/uipath/queue.test.ts` → PASS (`queue.test.ts` still passes against the untouched old `findOrderQueueItems`)
- [ ] **Step 5: Full gate** — `bun run typecheck && bun test && bunx biome check src` → all green (472 tests: 433 + 39 new)
- [ ] **Step 6: Commit**
```bash
git add src/orchestrator/portal.ts src/orchestrator/portal.test.ts src/uipath/backend.ts src/uipath/backend.test.ts src/uipath/backend-queues.test.ts src/uipath/queue.ts
git commit -m "feat(uipath): orchestrator read methods behind the backend interface" -m "Move the get_job/get_job_logs/list_*/pull/find handler logic out of server.ts into uipath/backend.ts functions that return the neutral cores (batch-vs-single error semantics, best-effort log digest, portal-key vs numeric queue resolution). server.ts is untouched until the registry cutover; memberFilename/toPortalRef move to orchestrator/portal.ts so the Apex backend shares them."
```

### Handoff notes (Tasks 4, 5)
- Task 4 (`uipath/payload.ts`, writes): append to `src/uipath/backend.ts`; do not reorder or rename the ten read exports. `uipath/actions.ts` still defines its own `QueueItemPriority`/`AddQueueItemArgs`/`StartJobArgs`/`StopJobArgs`/… (old shapes) — those collide by name with `orchestrator/backend.ts` exports, so import the contract ones with `import type { … } from "../orchestrator/backend.js"` in `backend.ts` only.
- Task 5: deletes in `uipath/faults.ts` the old `JobFault` + `extractFault` (+ `describe("extractFault")` in `faults.test.ts`); in `uipath/queue.ts` everything except `parseQueueItemUrl`/`ParsedQueueUrl` (+ delete `queue.test.ts`); `server.ts` stops importing `digestLogs`/`truncate`/`extractFault`/`extractTransactionOutcome`/`fetchJob*`/`listQueueDefinitions`/… (everything the new backend functions now own); `jobDeepLinkBase` in `uipath/uipath.ts` is used only by `actions.ts startJob` until Task 4 switches that to `jobDeepLink` — delete it when its last caller is gone.

> Executor note (Task 4): v1.33.1's `build_queue_item` fills `SpecificContent.serverURL` with
> `uipath.serverUrlByEnv[env] ?? <the Copilot BE base URL the order was read from>`. The second operand is only known to the
> content builder (`session.client.base`), so it travels to the UiPath serializer as the optional
> `QueueItemPayloadArgs.beBase` (already present in Task 1's `backend.ts`). `buildQueueItemContent` returns
> `BuiltQueueItem` (= the contract's `{content, queueName, notes, meta}` plus `beBase`). Apex (Task 16) ignores `beBase`
> and requires `apex.serverUrlByEnv.<env>` instead — no silent fallback on the Apex side.

### Task 4: Canonical queue-item content (`orchestrator/queue-item.ts`) + UiPath serializer (`uipath/payload.ts`) + `uipathBackend` write/payload methods

Two parts, two commits. Part A pins the shipped v1.33.1 `build_queue_item` output as a golden literal and extracts its pure half
in place (so the golden is captured *before* any reshaping). Part B reshapes: canonical native-typed content, UiPath serializer,
write adapters, `uipathBackend` assembly, and deletes `src/uipath/queue-item.ts`.

**Files:**
- Create: `src/uipath/payload.fixture.ts` (synthetic fixture order + the v1.33.1 golden literal), `src/orchestrator/queue-item.ts`, `src/orchestrator/queue-item.test.ts`, `src/uipath/payload.ts`, `src/uipath/payload.test.ts`, `src/uipath/backend-writes.test.ts`
- Modify: `src/uipath/queue-item.ts` (Part A only: extract `assembleQueueItem`; verified lines 76–101, 131, 176, 201, 211 of v1.33.1 — Task 1 only rewrites this file's `./safety.js` import, so line numbers are unchanged; anchors are quoted so re-locate by text if not), `src/uipath/actions.ts` (add `queueDefinitionId` to `addQueueItem`'s result; drop `jobDeepLinkBase` from `startJob`'s result — lines 16, 59, 100–105, 155–159, 192), `src/uipath/actions.test.ts` (lines 189, 206, 277–279), `src/uipath/backend.ts` (Task 3 created it: append the 4 write adapters + the `uipathBackend` assembly), `src/server.ts` (the `build_queue_item` handler + its imports only; Task 5 rewrites it again)
- Delete (Part B): `src/uipath/queue-item.ts`, `src/uipath/queue-item.test.ts`
- Temporary (Part A, never committed): `src/uipath/golden-head.test.ts`

**Interfaces:**
- Consumes: Task 1 — `QueueItemPayloadArgs`, `QueueItemPayload`, `AddQueueItemArgs/Result`, `DeleteQueueItemArgs/Result`, `StartJobArgs/Result`, `StopJobArgs/Result`, `OrchestratorBackend` (`src/orchestrator/backend.ts`), `guardQueueItemSafety` (`src/orchestrator/safety.ts`), `ExpectedError` (`src/shared/expected-error.ts`); Task 3 — `uipathListJobs, uipathGetJobs, uipathGetJobLogs, uipathListQueues, uipathListQueueItems, uipathFindOrderQueueItems, uipathPullQueueItem, uipathListProcesses, uipathListTriggers, uipathBuildFaultedJobIssue` (in `src/uipath/backend.ts`) and `jobDeepLink` (`src/uipath/uipath.ts`); existing `addQueueItem/deleteQueueItem/startJob/stopJob` (`src/uipath/actions.ts`).
- Produces: `buildQueueItemContent(orderUid, opts): Promise<BuiltQueueItem>`, `assembleQueueItemContent(input: AssembleInput): QueueItemContent`, `toMDY`, `decodeJwtId`, `splitName`, `resolveAutomationId` (all `src/orchestrator/queue-item.ts`); `serializeUipathContent(content)`, `uipathBuildQueueItemPayload(args: QueueItemPayloadArgs): QueueItemPayload` (`src/uipath/payload.ts`); `uipathAddQueueItem`, `uipathDeleteQueueItem`, `uipathStartJob`, `uipathStopJob`, `uipathBackend: OrchestratorBackend` (`src/uipath/backend.ts`).
- Decisions: (1) the `serverURL` fallback travels as `QueueItemPayloadArgs.beBase` (executor note above). (2) `build_queue_item`'s notes/meta are `[...built.notes, ...payload.notes]` / `{ ...built.meta, ...payload.meta }`: the content builder owns `profile/env/physicianId/queueName/orderStatus`, the backend owns `postUrl/folderPath` and the "To submit:" note — together exactly v1.33.1's `notes` (same order) and `meta`. (3) JSON key order inside `SpecificContent` is not semantic; the golden compares with `toEqual`. (4) UiPath's required-config check (`queueUrl`, `addQueueItemPath`, `folderPath`) now runs in the serializer, i.e. after the order fetch instead of before the login (same message). (5) The automationId warning drops the word "UiPath" (the note is now backend-neutral).

### Part A — pin v1.33.1 and extract its pure half

- [ ] **Step 1: Write the golden test (fixture + literal first)**

`src/uipath/payload.fixture.ts` — the literal `EXPECTED_V1_33_1` was captured by running the **unmodified v1.33.1 `buildQueueItem`** on `ORDER` (session + order fetch mocked, `Date.now` pinned); Step 4 re-proves it against the real function:
```ts
import type { BeOrder } from "../copilot/copilot-client.js";
// Synthetic order exercising every branch the builder maps (DOS fallback, CPT colon note,
// facility + referral-form precedence, support-account note).
export const ORDER_UID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee";
export const ORDER: BeOrder = {
  orderUid: ORDER_UID,
  status: "inProgress",
  patient: {
    patientName: "Doe, Jane Q",
    patientBirthDate: "1980-03-04",
    patientPhoneNumber: "555-0100",
  },
  insurance: { name: "Acme Health", memberId: "M123456" },
  speciality: { name: "Cardiology" },
  referredFacility: {
    name: "Example Heart Clinic",
    NPI: "1111111111",
    address: "1 Example Way",
    faxNumber: "555-0102",
    phone: "555-0103",
    placeOfService: "11",
    isSpeciality: true,
    type: "facility",
  },
  ICDCodes: [{ code: "I77.9" }, { code: "M54.5" }],
  CPTCodes: [
    {
      code: "99213",
      description: "Office visit: established patient",
      units: 1,
      treatments: ["ECG"],
    },
    { code: "93000", description: "ECG", units: "2", treatments: null },
  ],
  location: "Main Clinic",
  retro: true,
  urgency: "Urgent",
  comments: "Please expedite",
  appointmentDate: "5/6/2026",
  orderDate: "2026-05-01",
  placeOfService: "22",
  referralFormJSON: {
    from: {
      name: "Smith, Alan B",
      NPI: "2222222222",
      address: "9 Provider Rd",
      faxNumber: "555-0104",
      phoneNumber: "555-0105",
    },
    to: { name: "Example Heart Clinic", NPI: "1111111111", speciality: "Cardiology" },
    patient: { phoneNumber: "555-0199" },
  },
};

export const NOW_SEC = 1780000000;
const POST_URL =
  "https://cloud.uipath.com/myorg/mytenant/orchestrator_/odata/Queues/UiPathODataSvc.AddQueueItem";

// Captured by running the v1.33.1 builder body on ORDER (before any reshaping): the exact
// payload/notes/meta build_queue_item returned. The UiPath serializer must keep matching it.
export const EXPECTED_V1_33_1 = {
  payload: {
    itemData: {
      Name: "Acme Health auth submit queue",
      Priority: "Normal",
      Reference: `${ORDER_UID}-${NOW_SEC}`,
      SpecificContent: {
        ClinicNPI: "2222222222",
        DOS: "05/06/2026",
        DescriptionOfService: '["Office visit: established patient","ECG"]',
        Diagnoses: '["I77.9","M54.5"]',
        Facility: "Example Heart Clinic",
        IsApproved: false,
        IsRetro: true,
        IsSameAsRequestingProvider: false,
        IsUrgent: true,
        LinkedFacility: "",
        LinkedFacilityAddress: "",
        LinkedFacilityNPI: "",
        LinkedFacilityOtherName: "",
        LinkedFacilityType: "",
        LinkedPlaceOfService: "",
        LinkedProviderFullName: "",
        LinkedProviderID: "",
        LinkedProviderNPI: "",
        LinkedProviderType: "",
        LinkedSpecialty: "",
        Location: "Main Clinic",
        MemberDOB: "03/04/1980",
        MemberFax: "",
        MemberFirstName: "JANE",
        MemberFullName: "Doe, Jane Q",
        MemberID: "M123456",
        MemberLastName: "DOE",
        MemberPhone: "555-0199",
        OfficeComments: "Please expedite",
        ProviderAddress: "9 Provider Rd",
        ProviderCity: "",
        ProviderFax: "555-0104",
        ProviderFirstName: "ALAN",
        ProviderFullName: "Smith, Alan B",
        ProviderID: "",
        ProviderLastName: "SMITH",
        ProviderMail: "owner@example.com",
        ProviderNPI: "2222222222",
        ProviderPhone: "555-0105",
        ProviderZipCode: "",
        ReferredToAddress: "1 Example Way",
        ReferredToFax: "555-0102",
        ReferredToNPI: "1111111111",
        ReferredToOtherName: "",
        ReferredToPhone: "555-0103",
        ReferredToProviderID: "",
        ReferredToType: "facility",
        ServiceCode: '["99213","93000"]',
        Specialty: "Cardiology",
        Treatments: '["ECG"]',
        Units: '["1","2"]',
        addManualFacility: false,
        appointmentDate: "05/06/2026",
        automationId: "21a5ab32-817c-4a4a-95a0-bff16605ca6b",
        callbackContext: `{"physicianId":42,"orderUid":"${ORDER_UID}"}`,
        eligibilityOtherPatientName: null,
        isSpeciality: true,
        orderUid: ORDER_UID,
        placeOfService: "22",
        queueUrl: "https://sqs.example.com/queue",
        retryCount: 0,
        serverURL: "https://pre-prod-be.example.com",
        token: "eyJhbGciOiJub25lIn0.eyJpZCI6NDJ9.sig",
      },
    },
  },
  notes: [
    "DOS: order had no encounterDate; fell back to appointment/order date (may be empty).",
    "DescriptionOfService uses the order's full CPT descriptions (the BE's short-form descriptions are not in the order JSON).",
    "physicianId 42 is the support account's user id — under support-account auth the JWT 'id' claim is the support user in both pre- and post-switch tokens; callbackContext carries it as-is.",
    "To submit: POST payload.itemData as JSON to meta.postUrl with headers 'Authorization: Bearer <your uipath.bearer>' and 'X-UIPATH-FolderPath: Authorization Dev Clone'. Never done automatically by this tool.",
  ],
  meta: {
    profile: "ossm",
    env: "pre_prod",
    physicianId: 42,
    queueName: "Acme Health auth submit queue",
    orderStatus: "inProgress",
    postUrl: POST_URL,
    folderPath: "Authorization Dev Clone",
  },
};
```
`src/uipath/payload.test.ts` (Part A version — Part B rewrites it):
```ts
import { describe, expect, test } from "bun:test";
import { EXPECTED_V1_33_1, NOW_SEC, ORDER, ORDER_UID } from "./payload.fixture.js";
import { assembleQueueItem } from "./queue-item.js";

describe("v1.33.1 build_queue_item golden", () => {
  test("assembleQueueItem reproduces the shipped output for the fixture order", () => {
    const out = assembleQueueItem({
      order: ORDER,
      orderUid: ORDER_UID,
      profile: "ossm",
      env: "pre_prod",
      automationId: "21a5ab32-817c-4a4a-95a0-bff16605ca6b",
      token: "eyJhbGciOiJub25lIn0.eyJpZCI6NDJ9.sig", // {"id":42}
      physicianId: 42,
      supportMode: true,
      providerMail: "owner@example.com",
      beBase: "https://be.preprod.example.com",
      uipath: {
        orchestratorUrl: "https://cloud.uipath.com/myorg/mytenant/orchestrator_/",
        queueUrl: "https://sqs.example.com/queue",
        addQueueItemPath: "/odata/Queues/UiPathODataSvc.AddQueueItem",
        folderPath: "Authorization Dev Clone",
        serverUrl: "https://pre-prod-be.example.com",
      },
      nowSec: NOW_SEC,
    });
    expect(out).toEqual(EXPECTED_V1_33_1);
  });
});
```
- [ ] **Step 2: Run it to verify it fails**
Run: `bun test src/uipath/payload.test.ts` — Expected: FAIL (`Export named 'assembleQueueItem' not found in module './queue-item.js'` — the pure half is still inlined in `buildQueueItem`).
- [ ] **Step 3: Extract the pure half (behaviour-preserving)** in `src/uipath/queue-item.ts`

(a) Insert after line 80 (the closing `}` of `QueueItemResult`, before `export async function buildQueueItem(`):
```ts
export type QueueUipath = Pick<
  UipathConfig,
  "orchestratorUrl" | "queueUrl" | "addQueueItemPath" | "folderPath"
> & { serverUrl: string | undefined }; // serverUrl = uipath.serverUrlByEnv[env]

// Everything buildQueueItem reads from the outside world, so assembleQueueItem stays pure.
export interface AssembleInput {
  order: BeOrder;
  orderUid: string;
  profile: string | null;
  env: Env;
  automationId: string | undefined; // already resolved: explicit > account table
  token: string | undefined;
  physicianId: string | number | null;
  supportMode: boolean;
  providerMail: string;
  beBase: string; // the BE the order was read from (serverURL fallback)
  uipath: QueueUipath;
  nowSec: number;
}
```
(b) Replace lines 82–101 (from `export async function buildQueueItem(` through `  const notes: string[] = [];`, which includes the old `const physicianId = decodeJwtId(token);` and `const o = await fetchOrder(client, orderUid);`) with:
```ts
export async function buildQueueItem(
  orderUid: string,
  opts: { profile?: string | null; env: Env; automationId?: string | null }, // env is required — never defaulted (repo invariant)
): Promise<QueueItemResult> {
  const profile = opts.profile ?? null;
  const env: Env = opts.env;
  const automationId = resolveAutomationId(opts.automationId, profile);
  const uipath = getUipath();
  requireQueueFields(uipath);

  const session = await connect(env, profile);
  const { client, token } = session;
  const providerMail =
    session.mode === "support"
      ? ((await clinicOwnerEmail(env, session.clinicUid as string)) ?? "")
      : session.email;

  return assembleQueueItem({
    order: await fetchOrder(client, orderUid),
    orderUid,
    profile,
    env,
    automationId,
    token,
    physicianId: decodeJwtId(token),
    supportMode: session.mode === "support",
    providerMail,
    beBase: client.base,
    uipath: { ...uipath, serverUrl: uipath.serverUrlByEnv?.[env] },
    nowSec: Math.floor(Date.now() / 1000),
  });
}

export function assembleQueueItem(input: AssembleInput): QueueItemResult {
  const { order: o, orderUid, profile, env, automationId, token, physicianId, uipath } = input;
  const notes: string[] = [];
```
(c) The rest of the old body (lines 102–236, from the blank line after `const notes…` through the closing `}` of the function) is now the body of `assembleQueueItem`, moved verbatim with exactly these four one-line substitutions:

| v1.33.1 line | before | after |
|---|---|---|
| 131 | `  if (session.mode === "support")` | `  if (input.supportMode)` |
| 176 | `    ProviderMail: providerMail,` | `    ProviderMail: input.providerMail,` |
| 201 | `    serverURL: uipath.serverUrlByEnv?.[env] ?? client.base,` | `    serverURL: uipath.serverUrl ?? input.beBase,` |
| 211 | ``      Reference: `${orderUid}-${Math.floor(Date.now() / 1000)}`,`` | ``      Reference: `${orderUid}-${input.nowSec}`,`` |

- [ ] **Step 4: Prove the literal against the real v1.33.1 path**

Run: `bun test src/uipath/payload.test.ts` — Expected: PASS (pure half reproduces the golden).

Then create the throwaway `src/uipath/golden-head.test.ts` (runs the real `buildQueueItem` — session and order fetch mocked — against the same literal; `mock.module` leaks across files, which is why it is never committed):
```ts
// THROWAWAY (delete after Part A Step 4): runs the real buildQueueItem I/O path with the
// session + order fetch mocked, and expects the golden captured from v1.33.1.
import { expect, mock, test } from "bun:test";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { resetConfigCache } from "../config/config.js";
import * as realClient from "../copilot/copilot-client.js";
import * as realSession from "../copilot/session.js";
import { EXPECTED_V1_33_1, NOW_SEC, ORDER, ORDER_UID } from "./payload.fixture.js";

mock.module("../copilot/copilot-client.js", () => ({
  ...realClient,
  fetchOrder: async () => ORDER,
}));
mock.module("../copilot/session.js", () => ({
  ...realSession,
  connect: async () => ({
    mode: "support",
    clinicUid: "c1",
    email: "unused@example.com",
    token: "eyJhbGciOiJub25lIn0.eyJpZCI6NDJ9.sig", // {"id":42}
    client: { base: "https://be.preprod.example.com" },
  }),
  clinicOwnerEmail: async () => "owner@example.com",
}));

test("the real buildQueueItem reproduces the golden", async () => {
  const dir = mkdtempSync(join(tmpdir(), "golden-head-"));
  const cred = { be: "https://be.x.example.com", email: "a@example.com", password: "pw" };
  writeFileSync(
    join(dir, "config.json"),
    JSON.stringify({
      copilot: { sessionCache: false, prod: cred, pre_prod: cred },
      uipath: {
        orchestratorUrl: "https://cloud.uipath.com/myorg/mytenant/orchestrator_/",
        bearer: "b",
        queueUrl: "https://sqs.example.com/queue",
        addQueueItemPath: "/odata/Queues/UiPathODataSvc.AddQueueItem",
        folderPath: "Authorization Dev Clone",
        serverUrlByEnv: { pre_prod: "https://pre-prod-be.example.com" },
      },
    }),
  );
  process.env["COPILOT_MCP_CONFIG"] = join(dir, "config.json");
  resetConfigCache();
  Date.now = () => NOW_SEC * 1000;
  const { buildQueueItem } = await import("./queue-item.js");
  const out = await buildQueueItem(ORDER_UID, {
    profile: "ossm",
    env: "pre_prod",
    automationId: "21a5ab32-817c-4a4a-95a0-bff16605ca6b",
  });
  expect(out).toEqual(EXPECTED_V1_33_1);
});
```
Run: `bun test src/uipath/golden-head.test.ts` — Expected: PASS. Then `rm src/uipath/golden-head.test.ts`.
- [ ] **Step 5: Full gate** — `bun run typecheck && bun test && bunx biome check src` → all green
- [ ] **Step 6: Commit**
```bash
git add src/uipath/queue-item.ts src/uipath/payload.fixture.ts src/uipath/payload.test.ts
git commit -m "test(uipath): pin v1.33.1 build_queue_item output; extract pure assembleQueueItem" -m "Golden literal captured from the shipped builder before the queue-item content moves to the orchestrator layer, so the UiPath serializer can be proven byte-equivalent (deep-equal) afterwards."
```

### Part B — canonical content, UiPath serializer, write adapters

- [ ] **Step 7: Write the failing tests**

`src/orchestrator/queue-item.test.ts` — the four moved suites (`toMDY`, `splitName`, `resolveAutomationId`, `decodeJwtId`, unchanged from `src/uipath/queue-item.test.ts`) plus `assembleQueueItemContent` behaviour (native types, DOS precedence, referral-form vs facility precedence, comma-less provider, automationId/token warnings, support note):
```ts
import { describe, expect, test } from "bun:test";
import type { BeOrder } from "../copilot/copilot-client.js";
import {
  type AssembleInput,
  assembleQueueItemContent,
  decodeJwtId,
  resolveAutomationId,
  splitName,
  toMDY,
} from "./queue-item.js";

describe("toMDY", () => {
  test("zero-pads US M/D/YYYY", () => {
    expect(toMDY("1/2/2024")).toBe("01/02/2024");
    expect(toMDY("12/31/2024")).toBe("12/31/2024");
  });
  test("converts ISO YYYY-MM-DD to MM/DD/YYYY", () => {
    expect(toMDY("2024-03-04")).toBe("03/04/2024");
  });
  test("returns '' for empty/nullish/unparseable input", () => {
    expect(toMDY("")).toBe("");
    expect(toMDY(null)).toBe("");
    expect(toMDY(undefined)).toBe("");
    expect(toMDY("not a date")).toBe("");
  });
});

describe("splitName", () => {
  test("splits 'Last, First M' and uppercases", () => {
    expect(splitName("Doe, John A")).toEqual({ last: "DOE", first: "JOHN" });
  });
  test("treats a comma-less name as the last name", () => {
    expect(splitName("Madonna")).toEqual({ last: "MADONNA", first: "" });
  });
  test("handles undefined", () => {
    expect(splitName(undefined)).toEqual({ last: "", first: "" });
  });
});

describe("resolveAutomationId", () => {
  test("an explicit automationId always wins over the account table", () => {
    expect(resolveAutomationId("explicit-id", "kafri")).toBe("explicit-id");
  });
  test("resolves from a known account (profile), case-insensitively", () => {
    expect(resolveAutomationId(undefined, "kafri")).toBe("c741d791-3e3b-4102-b95c-f94549949c9a");
    expect(resolveAutomationId(undefined, "SCLC")).toBe("85619cc6-5e25-4219-ad12-fc9877aaf841");
  });
  test("returns undefined for an unknown/missing account and no explicit id", () => {
    expect(resolveAutomationId(undefined, "unknown-account")).toBeUndefined();
    expect(resolveAutomationId(undefined, null)).toBeUndefined();
    expect(resolveAutomationId("", null)).toBeUndefined();
  });
});

describe("decodeJwtId", () => {
  test("extracts a numeric id from a JWT payload", () => {
    const payload = Buffer.from(JSON.stringify({ id: 42 })).toString("base64");
    expect(decodeJwtId(`header.${payload}.sig`)).toBe(42);
  });
  test("extracts a string id", () => {
    const payload = Buffer.from(JSON.stringify({ id: "abc" })).toString("base64");
    expect(decodeJwtId(`header.${payload}.sig`)).toBe("abc");
  });
  test("returns null for malformed tokens or missing id", () => {
    expect(decodeJwtId(undefined)).toBeNull();
    expect(decodeJwtId("garbage")).toBeNull();
    const noId = Buffer.from(JSON.stringify({ sub: "x" })).toString("base64");
    expect(decodeJwtId(`h.${noId}.s`)).toBeNull();
  });
});

describe("assembleQueueItemContent", () => {
  const UID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee";
  const input = (order: BeOrder, over: Partial<AssembleInput> = {}): AssembleInput => ({
    order,
    orderUid: UID,
    profile: "ossm",
    env: "pre_prod",
    automationId: "auto-1",
    token: "h.p.s",
    physicianId: 7,
    supportMode: false,
    providerMail: "owner@example.com",
    ...over,
  });

  test("emits native types and leaves the backend-owned URL fields out", () => {
    const { content } = assembleQueueItemContent(
      input({
        ICDCodes: [{ code: "I77.9" }],
        CPTCodes: [{ code: "99213", description: "Visit", units: 2, treatments: ["ECG"] }],
      }),
    );
    expect(content["Diagnoses"]).toEqual(["I77.9"]);
    expect(content["Units"]).toEqual(["2"]);
    expect(content["Treatments"]).toEqual(["ECG"]);
    expect(content["callbackContext"]).toEqual({ physicianId: 7, orderUid: UID });
    expect(content["IsApproved"]).toBe(false);
    expect("serverURL" in content).toBe(false);
    expect("queueUrl" in content).toBe(false);
  });

  test("DOS prefers encounterDate, then appointmentDate, then orderDate; the fallback note appears only without an encounterDate", () => {
    const withEncounter = assembleQueueItemContent(
      input({ encounterDate: "1/2/2026", appointmentDate: "3/4/2026", orderDate: "5/6/2026" }),
    );
    expect(withEncounter.content["DOS"]).toBe("01/02/2026");
    expect(withEncounter.notes.some((n) => n.startsWith("DOS:"))).toBe(false);

    const fallback = assembleQueueItemContent(
      input({ appointmentDate: "3/4/2026", orderDate: "5/6/2026" }),
    );
    expect(fallback.content["DOS"]).toBe("03/04/2026");
    expect(fallback.notes.some((n) => n.startsWith("DOS:"))).toBe(true);
    expect(assembleQueueItemContent(input({ orderDate: "5/6/2026" })).content["DOS"]).toBe(
      "05/06/2026",
    );
  });

  test("referral-form values win over the referred facility, which fills the gaps", () => {
    const { content } = assembleQueueItemContent(
      input({
        referredFacility: {
          NPI: "FAC-NPI",
          address: "Facility Rd",
          phone: "555-0001",
          type: "facility",
        },
        referralFormJSON: { to: { NPI: "FORM-NPI" } },
      }),
    );
    expect(content["ReferredToNPI"]).toBe("FORM-NPI");
    expect(content["ReferredToAddress"]).toBe("Facility Rd");
    expect(content["ReferredToPhone"]).toBe("555-0001");
    expect(content["ReferredToType"]).toBe("facility");
  });

  test("ProviderLastName is empty when the provider name has no comma", () => {
    const { content } = assembleQueueItemContent(
      input({ referralFormJSON: { from: { name: "Clinic Group" } } }),
    );
    expect(content["ProviderFirstName"]).toBe("Clinic Group");
    expect(content["ProviderLastName"]).toBe("");
  });

  test("an unresolved automationId becomes a placeholder UUID with a warning; a resolved one does not", () => {
    const unresolved = assembleQueueItemContent(input({}, { automationId: undefined }));
    expect(unresolved.content["automationId"]).toMatch(/^[0-9a-f-]{36}$/);
    expect(unresolved.notes.some((n) => n.includes("no automationId was supplied"))).toBe(true);

    const resolved = assembleQueueItemContent(input({}));
    expect(resolved.content["automationId"]).toBe("auto-1");
    expect(resolved.notes.some((n) => n.includes("no automationId"))).toBe(false);
  });

  test("a missing token is flagged and emitted as an empty string", () => {
    const out = assembleQueueItemContent(input({}, { token: undefined }));
    expect(out.content["token"]).toBe("");
    expect(out.notes.some((n) => n.includes("login returned no token"))).toBe(true);
  });

  test("support-account auth adds the physicianId note; meta carries the queue name", () => {
    const out = assembleQueueItemContent(
      input({ insurance: { name: "Acme Health" }, status: "inProgress" }, { supportMode: true }),
    );
    expect(out.notes.some((n) => n.startsWith("physicianId 7 is the support account"))).toBe(true);
    expect(out.queueName).toBe("Acme Health auth submit queue");
    expect(out.meta).toEqual({
      profile: "ossm",
      env: "pre_prod",
      physicianId: 7,
      queueName: "Acme Health auth submit queue",
      orderStatus: "inProgress",
    });
  });
});
```
`src/uipath/payload.test.ts` (replaces the Part A file; the golden now runs through the canonical builder + UiPath serializer):
```ts
import { afterAll, afterEach, beforeAll, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { resetConfigCache } from "../config/config.js";
import { assembleQueueItemContent } from "../orchestrator/queue-item.js";
import { EXPECTED_V1_33_1, NOW_SEC, ORDER, ORDER_UID } from "./payload.fixture.js";
import { serializeUipathContent, uipathBuildQueueItemPayload } from "./payload.js";

const UIPATH = {
  orchestratorUrl: "https://cloud.uipath.com/myorg/mytenant/orchestrator_/",
  bearer: "test-bearer",
  queueUrl: "https://sqs.example.com/queue",
  addQueueItemPath: "/odata/Queues/UiPathODataSvc.AddQueueItem",
  folderPath: "Authorization Dev Clone",
  serverUrlByEnv: { pre_prod: "https://pre-prod-be.example.com" },
};
const creds = (name: string) => ({
  be: `https://be.${name}.example.com`,
  email: `${name}@example.com`,
  password: "pw",
});
const FIXTURE = {
  copilot: { sessionCache: false, prod: creds("prod"), pre_prod: creds("preprod") },
  uipath: UIPATH,
};

let dir: string;
let prevConfig: string | undefined;
const realNow = Date.now;

const writeConfig = (config: unknown): void => {
  writeFileSync(join(dir, "config.json"), JSON.stringify(config));
  resetConfigCache();
};

beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), "copilot-mcp-payload-"));
  prevConfig = process.env["COPILOT_MCP_CONFIG"];
  process.env["COPILOT_MCP_CONFIG"] = join(dir, "config.json");
  writeConfig(FIXTURE);
  Date.now = () => NOW_SEC * 1000;
});

afterEach(() => writeConfig(FIXTURE)); // undo per-test config overrides

afterAll(() => {
  Date.now = realNow;
  if (prevConfig === undefined) delete process.env["COPILOT_MCP_CONFIG"];
  else process.env["COPILOT_MCP_CONFIG"] = prevConfig;
  resetConfigCache();
  rmSync(dir, { recursive: true, force: true });
});

const assemble = () =>
  assembleQueueItemContent({
    order: ORDER,
    orderUid: ORDER_UID,
    profile: "ossm",
    env: "pre_prod",
    automationId: "21a5ab32-817c-4a4a-95a0-bff16605ca6b",
    token: "eyJhbGciOiJub25lIn0.eyJpZCI6NDJ9.sig",
    physicianId: 42,
    supportMode: true,
    providerMail: "owner@example.com",
  });

const payloadFor = (env: "prod" | "pre_prod", content: Record<string, unknown>) =>
  uipathBuildQueueItemPayload({
    env,
    queueName: "Acme Health auth submit queue",
    orderUid: ORDER_UID,
    content,
    beBase: "https://be.preprod.example.com",
  });

describe("v1.33.1 build_queue_item parity", () => {
  // build_queue_item = content builder + backend serializer, notes/meta merged in that order.
  test("content builder + UiPath serializer reproduce the shipped output for the fixture order", () => {
    const built = assemble();
    const p = uipathBuildQueueItemPayload({
      env: "pre_prod",
      queueName: built.queueName,
      orderUid: ORDER_UID,
      content: built.content,
      beBase: "https://be.preprod.example.com",
    });
    expect({
      payload: p.payload,
      notes: [...built.notes, ...p.notes],
      meta: { ...built.meta, ...p.meta },
    }).toEqual(EXPECTED_V1_33_1);
  });

  test("serverURL falls back to the BE the order was read from when no URL is pinned for the env", () => {
    const { payload } = payloadFor("prod", assemble().content);
    const sc = (payload as { itemData: { SpecificContent: Record<string, unknown> } }).itemData
      .SpecificContent;
    expect(sc["serverURL"]).toBe("https://be.preprod.example.com");
  });
});

describe("serializeUipathContent", () => {
  test("stringifies exactly the array fields and callbackContext, leaving native types elsewhere", () => {
    const out = serializeUipathContent({
      Diagnoses: ["I77.9"],
      Units: [],
      callbackContext: { physicianId: 7, orderUid: "u" },
      IsRetro: true,
      retryCount: 0,
      OpenReferralInfo: null,
      MemberID: "M1",
    });
    expect(out).toEqual({
      Diagnoses: '["I77.9"]',
      Units: "[]",
      callbackContext: '{"physicianId":7,"orderUid":"u"}',
      IsRetro: true,
      retryCount: 0,
      OpenReferralInfo: null,
      MemberID: "M1",
    });
  });

  test("does not invent a stringified field the content lacks", () => {
    expect(serializeUipathContent({ MemberID: "M1" })).toEqual({ MemberID: "M1" });
  });
});

describe("uipathBuildQueueItemPayload", () => {
  test("forces IsApproved=false even when the content says true", () => {
    const { payload } = payloadFor("pre_prod", { ...assemble().content, IsApproved: true });
    const sc = (payload as { itemData: { SpecificContent: Record<string, unknown> } }).itemData
      .SpecificContent;
    expect(sc["IsApproved"]).toBe(false);
  });

  test("names every missing queue-build field in the config error", () => {
    writeConfig({
      ...FIXTURE,
      uipath: { orchestratorUrl: UIPATH.orchestratorUrl, bearer: "test-bearer" },
    });
    expect(() => payloadFor("pre_prod", assemble().content)).toThrow(
      /missing fields required for build_queue_item: queueUrl, addQueueItemPath, folderPath/,
    );
  });
});
```
`src/uipath/backend-writes.test.ts`:
```ts
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { resetConfigCache } from "../config/config.js";
import { ExpectedError } from "../shared/expected-error.js";
import {
  uipathAddQueueItem,
  uipathDeleteQueueItem,
  uipathStartJob,
  uipathStopJob,
} from "./backend.js";

const ORCH = "https://cloud.uipath.com/myorg/mytenant/orchestrator_";
const creds = (name: string) => ({
  be: `https://be.${name}.example.com`,
  email: `${name}@example.com`,
  password: "pw",
});
const FIXTURE = {
  copilot: { sessionCache: false, prod: creds("prod"), pre_prod: creds("preprod") },
  uipath: {
    orchestratorUrl: ORCH,
    bearer: "test-bearer",
    serverUrlByEnv: { pre_prod: "https://pre-prod-be.example.com" },
  },
};

let dir: string;
let prevConfig: string | undefined;

beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), "copilot-mcp-uipath-writes-"));
  prevConfig = process.env["COPILOT_MCP_CONFIG"];
  process.env["COPILOT_MCP_CONFIG"] = join(dir, "config.json");
  writeFileSync(join(dir, "config.json"), JSON.stringify(FIXTURE));
  resetConfigCache();
});

afterAll(() => {
  if (prevConfig === undefined) delete process.env["COPILOT_MCP_CONFIG"];
  else process.env["COPILOT_MCP_CONFIG"] = prevConfig;
  resetConfigCache();
  rmSync(dir, { recursive: true, force: true });
});

interface RecordedCall {
  method: string;
  url: string;
  body: unknown;
}

let calls: RecordedCall[];
let responses: Response[];
const realFetch = globalThis.fetch;

// Stub fetch: record each call, pop the next queued response (default 200 {}).
beforeEach(() => {
  calls = [];
  responses = [];
  globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
    calls.push({
      method: init?.method ?? "GET",
      url: String(input),
      body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
    });
    return responses.shift() ?? new Response("{}", { status: 200 });
  }) as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = realFetch;
});

const json = (obj: unknown, status = 200): Response =>
  new Response(JSON.stringify(obj), { status });

describe("uipathAddQueueItem", () => {
  test("posts the guarded item to the queue named by `queue` and reports ids as strings", async () => {
    responses.push(json({ Id: 42, Status: "New", QueueDefinitionId: 79926 }));
    const out = await uipathAddQueueItem({
      env: "pre_prod",
      queue: "Scratch queue",
      reference: "REF-1",
      priority: "High",
      specificContent: { orderUid: "u1", IsApproved: true },
    });
    const body = calls[0]?.body as { itemData: Record<string, unknown> };
    expect(body.itemData["Name"]).toBe("Scratch queue");
    expect(body.itemData["Priority"]).toBe("High");
    expect(out).toEqual({
      env: "pre_prod",
      queueId: "79926",
      queueName: "Scratch queue",
      reference: "REF-1",
      itemId: "42",
      status: "New",
      forced: ["IsApproved"],
    });
  });

  test("an unreported queue id is an empty string, not '0'", async () => {
    responses.push(json({ Id: 1, Status: "New" }));
    const out = await uipathAddQueueItem({
      env: "pre_prod",
      queue: "Scratch queue",
      reference: "REF-1",
      priority: "Normal",
      specificContent: { orderUid: "u1" },
    });
    expect(out.queueId).toBe("");
  });
});

describe("uipathDeleteQueueItem", () => {
  for (const itemId of ["", "abc", "0", "-3", "1.5", "12abc", "99999999999999999999"]) {
    test(`rejects the non-positive-integer id '${itemId}' before any request`, async () => {
      const err = await uipathDeleteQueueItem({ env: "pre_prod", itemId }).catch((e) => e);
      expect(err).toBeInstanceOf(ExpectedError);
      expect(calls.length).toBe(0);
    });
  }

  test("converts the string id to the numeric OData key and returns it as a string", async () => {
    responses.push(json({ Id: 9, Status: "New", Reference: "r9" }));
    responses.push(new Response("", { status: 204 }));
    const out = await uipathDeleteQueueItem({ env: "pre_prod", itemId: "9", queueId: "ignored" });
    expect(calls[1]?.url).toBe(`${ORCH}/odata/QueueItems(9)`);
    expect(out).toEqual({
      env: "pre_prod",
      itemId: "9",
      deleted: true,
      previousStatus: "New",
      reference: "r9",
    });
  });
});

describe("uipathStartJob", () => {
  test("maps processId -> ReleaseKey and input -> InputArguments, and links each job", async () => {
    responses.push(
      json({ value: [{ Id: 5, Key: "job-key-1", State: "Pending", ReleaseName: "Proc.Dev" }] }),
    );
    const out = await uipathStartJob({
      env: "pre_prod",
      processId: "609b3602-b6f2-44b2-9ee2-6a8988fac1f5",
      input: { a: 1 },
      jobsCount: 2,
      matchValue: "ignored-on-uipath",
    });
    expect(calls[0]?.body).toEqual({
      startInfo: {
        ReleaseKey: "609b3602-b6f2-44b2-9ee2-6a8988fac1f5",
        Strategy: "ModernJobsCount",
        JobsCount: 2,
        InputArguments: '{"a":1}',
      },
    });
    expect(out).toEqual({
      env: "pre_prod",
      jobs: [
        {
          id: "job-key-1",
          state: "Pending",
          processName: "Proc.Dev",
          link: `${ORCH}/jobs(sidepanel:sidepanel/jobs/job-key-1/details)`,
        },
      ],
    });
  });
});

describe("uipathStopJob", () => {
  test("resolves the job by key and returns the key as jobId", async () => {
    responses.push(json({ value: [{ Id: 5, Key: "job-key-1", State: "Running" }] }));
    responses.push(json({}));
    const out = await uipathStopJob({ env: "pre_prod", jobId: "job-key-1", strategy: "Kill" });
    expect(calls[1]?.url).toBe(`${ORCH}/odata/Jobs(5)/UiPath.Server.Configuration.OData.StopJob`);
    expect(calls[1]?.body).toEqual({ strategy: "Kill" });
    expect(out).toEqual({ env: "pre_prod", jobId: "job-key-1", strategy: "Kill", stopped: true });
  });
});

describe("pre_prod-only", () => {
  test("every write adapter refuses prod before any request", async () => {
    const attempts = [
      () =>
        uipathAddQueueItem({
          env: "prod",
          queue: "q",
          reference: "r",
          priority: "Normal",
          specificContent: {},
        }),
      () => uipathDeleteQueueItem({ env: "prod", itemId: "1" }),
      () => uipathStartJob({ env: "prod", processId: "rk", input: {}, jobsCount: 1 }),
      () => uipathStopJob({ env: "prod", jobId: "job-key-1", strategy: "SoftStop" }),
    ];
    for (const attempt of attempts) await expect(attempt()).rejects.toThrow(/pre_prod-only/);
    expect(calls.length).toBe(0);
  });
});
```
In `src/uipath/actions.test.ts` (verified lines): line 189 `    responses.push(json({ Id: 42, Status: "New" }));` → `    responses.push(json({ Id: 42, Status: "New", QueueDefinitionId: 79926 }));`; in the `toEqual` at lines 202–209 add `      queueDefinitionId: 79926,` after `      itemId: 42,` (line 206); delete lines 277–279 (the `expect(out.jobDeepLinkBase).toBe(\n "https://cloud.uipath.com/…/jobs(sidepanel:sidepanel/jobs/{key}/details)",\n );` assertion — `startJob` no longer returns it; the per-job link is asserted in `backend-writes.test.ts`).
- [ ] **Step 8: Run them to verify they fail**
Run: `bun test src/orchestrator/queue-item.test.ts src/uipath/payload.test.ts src/uipath/backend-writes.test.ts src/uipath/actions.test.ts` — Expected: FAIL (modules `./queue-item.js`/`./payload.js` don't export the new names; `queueDefinitionId` missing from `addQueueItem`'s result).
- [ ] **Step 9: Implement**

`src/orchestrator/queue-item.ts` (new; `toMDY`, `decodeJwtId`, `splitName`, `resolveAutomationId` moved verbatim from `src/uipath/queue-item.ts` lines 17–61; the assembly is the v1.33.1 mapping with arrays/`callbackContext` left native and `queueUrl`/`serverURL` removed):
```ts
// Canonical Copilot-order -> queue-item content, shared by every orchestrator backend.
// Fetches the order and logs in to capture the BE callback JWT + physicianId, then maps the
// order onto SpecificContent in NATIVE types (arrays stay arrays, callbackContext is an
// object) and WITHOUT serverURL/queueUrl — each backend's serializer fills those from its
// own config and shapes the wire format. BUILD ONLY — never POSTs. IsApproved is ALWAYS
// false (see repo CLAUDE.md rule); backends re-assert it through guardQueueItemSafety.

import { randomUUID } from "node:crypto";
import type { Env } from "../config/config.js";
import { type BeOrder, fetchOrder, toMDY as toMDYStrict } from "../copilot/copilot-client.js";
import { clinicOwnerEmail, connect } from "../copilot/session.js";
import { ACCOUNT_AUTOMATION_IDS } from "../mcp/reference.js";
import { prop } from "../shared/util.js";

// Non-throwing MM/DD/YYYY normalizer (queue payloads prefer "" over an error) — the
// date-parsing rules themselves live once, in copilot-client.ts's toMDY.
export function toMDY(input: string | null | undefined): string {
  try {
    return toMDYStrict(input) ?? "";
  } catch {
    return "";
  }
}

export const decodeJwtId = (t: string | undefined): string | number | null => {
  try {
    const seg = (t ?? "").split(".")[1] ?? "";
    const payload: unknown = JSON.parse(Buffer.from(seg, "base64").toString());
    const id = prop(payload, "id");
    return typeof id === "string" || typeof id === "number" ? id : null;
  } catch {
    return null;
  }
};

// "Last, First M" -> { last:"LAST", first:"FIRST" } (first token after comma, uppercased)
export function splitName(full: string | undefined): { last: string; first: string } {
  const s = String(full ?? "").trim();
  const ci = s.indexOf(",");
  if (ci < 0) return { last: s.toUpperCase(), first: "" };
  const last = s.slice(0, ci).trim().toUpperCase();
  const first = (
    s
      .slice(ci + 1)
      .trim()
      .split(/\s+/)[0] ?? ""
  ).toUpperCase();
  return { last, first };
}

// An explicit automationId always wins; otherwise resolve it from `profile` (the account name
// from config, e.g. "kafri"/"sclc"/"ossm") against the known account table, matched
// case-insensitively. Returns undefined when neither source has one.
export function resolveAutomationId(
  explicit: string | null | undefined,
  profile: string | null | undefined,
): string | undefined {
  return explicit?.trim() || ACCOUNT_AUTOMATION_IDS[(profile ?? "").toUpperCase()];
}

export interface QueueItemContent {
  content: Record<string, unknown>; // native-typed SpecificContent, no serverURL/queueUrl
  queueName: string;
  notes: string[];
  meta: Record<string, unknown>;
}

// `beBase` is the Copilot BE the order was read from: the serverURL fallback for backends
// whose config pins no server URL for this env.
export interface BuiltQueueItem extends QueueItemContent {
  beBase: string;
}

// Everything buildQueueItemContent reads from the outside world, so assembly stays pure.
export interface AssembleInput {
  order: BeOrder;
  orderUid: string;
  profile: string | null;
  env: Env;
  automationId: string | undefined; // already resolved: explicit > account table
  token: string | undefined;
  physicianId: string | number | null;
  supportMode: boolean;
  providerMail: string;
}

const s = (v: unknown): string => (v == null ? "" : String(v));

export function assembleQueueItemContent(input: AssembleInput): QueueItemContent {
  const { order: o, orderUid, profile, env, automationId, token, physicianId } = input;
  const notes: string[] = [];

  const form: NonNullable<BeOrder["referralFormJSON"]> = o.referralFormJSON ?? {};
  const from: Record<string, unknown> = form.from ?? {};
  const to: Record<string, unknown> = form.to ?? {};
  const fac = o.referredFacility ?? {};
  const cpts = o.CPTCodes ?? [];
  const icds = o.ICDCodes ?? [];
  const member = splitName(o.patient?.patientName);
  const insuranceName = o.insurance?.name ?? "";

  const dos = toMDY(o.encounterDate) || toMDY(o.appointmentDate) || toMDY(o.orderDate);
  if (!o.encounterDate)
    notes.push(
      "DOS: order had no encounterDate; fell back to appointment/order date (may be empty).",
    );
  if (cpts.some((c) => /[:;]/.test(c.description ?? "")))
    notes.push(
      "DescriptionOfService uses the order's full CPT descriptions (the BE's short-form descriptions are not in the order JSON).",
    );
  if (!token)
    notes.push(
      "WARNING: login returned no token — SpecificContent.token is empty; the bot's BE callbacks will fail.",
    );
  if (!automationId)
    notes.push(
      `WARNING: no automationId was supplied and none is known for account '${profile ?? "(none)"}' ` +
        "— SpecificContent.automationId is a placeholder random UUID, not the account's real " +
        "automation id. Pass the correct value via the automationId argument before submitting.",
    );
  if (input.supportMode)
    notes.push(
      `physicianId ${physicianId} is the support account's user id — under support-account auth the JWT 'id' claim is the support user in both pre- and post-switch tokens; callbackContext carries it as-is.`,
    );

  const fromName = s(from["name"]);
  const queueName = `${insuranceName} auth submit queue`;
  const content = {
    ClinicNPI: s(from["NPI"]),
    DOS: dos,
    DescriptionOfService: cpts.map((c) => c.description ?? ""),
    Diagnoses: icds.map((i) => i.code),
    Facility: fac.name || s(to["name"]),
    IsApproved: false, // HARD RULE — never true
    IsRetro: !!o.retro,
    IsSameAsRequestingProvider: false,
    IsUrgent: String(o.urgency ?? "").toLowerCase() === "urgent",
    LinkedFacility: "",
    LinkedFacilityAddress: "",
    LinkedFacilityNPI: "",
    LinkedFacilityOtherName: "",
    LinkedFacilityType: "",
    LinkedPlaceOfService: "",
    LinkedProviderFullName: "",
    LinkedProviderID: "",
    LinkedProviderNPI: "",
    LinkedProviderType: "",
    LinkedSpecialty: "",
    Location: o.location ?? "",
    MemberDOB: toMDY(o.patient?.patientBirthDate),
    MemberFax: "",
    MemberFirstName: member.first,
    MemberFullName: o.patient?.patientName ?? "",
    MemberID: o.insurance?.memberId ?? "",
    MemberLastName: member.last,
    MemberPhone: s(form.patient?.phoneNumber) || o.patient?.patientPhoneNumber || "",
    OfficeComments: o.comments ?? "",
    ProviderAddress: s(from["address"]),
    ProviderCity: "",
    ProviderFax: s(from["faxNumber"]),
    ProviderFirstName: splitName(fromName).first || fromName,
    ProviderFullName: fromName,
    ProviderID: "",
    ProviderLastName:
      splitName(fromName).last && fromName.includes(",") ? splitName(fromName).last : "",
    ProviderMail: input.providerMail,
    ProviderNPI: s(from["NPI"]),
    ProviderPhone: s(from["phoneNumber"]),
    ProviderZipCode: "",
    ReferredToAddress: s(to["address"]) || fac.address || "",
    ReferredToFax: s(to["faxNumber"]) || fac.faxNumber || "",
    ReferredToNPI: s(to["NPI"]) || fac.NPI || "",
    ReferredToOtherName: "",
    ReferredToPhone: s(to["phoneNumber"]) || fac.phone || "",
    ReferredToProviderID: "",
    ReferredToType: fac.type || "provider",
    ServiceCode: cpts.map((c) => c.code),
    Specialty: o.speciality?.name || s(to["speciality"]),
    Treatments: cpts.flatMap((c) => c.treatments ?? []),
    Units: cpts.map((c) => String(c.units ?? "")),
    addManualFacility: false,
    appointmentDate: toMDY(o.appointmentDate),
    automationId: automationId || randomUUID(),
    callbackContext: { physicianId, orderUid },
    eligibilityOtherPatientName: null,
    isSpeciality: !!(fac.isSpeciality ?? false),
    orderUid,
    placeOfService: o.placeOfService || fac.placeOfService || "",
    retryCount: 0,
    token: token ?? "",
  };

  return {
    content,
    queueName,
    notes,
    meta: { profile: profile ?? "(default)", env, physicianId, queueName, orderStatus: o.status },
  };
}

export async function buildQueueItemContent(
  orderUid: string,
  opts: { profile?: string | null; env: Env; automationId?: string | null }, // env is required — never defaulted (repo invariant)
): Promise<BuiltQueueItem> {
  const profile = opts.profile ?? null;
  const session = await connect(opts.env, profile);
  const { client, token } = session;
  const providerMail =
    session.mode === "support"
      ? ((await clinicOwnerEmail(opts.env, session.clinicUid as string)) ?? "")
      : session.email;

  const built = assembleQueueItemContent({
    order: await fetchOrder(client, orderUid),
    orderUid,
    profile,
    env: opts.env,
    automationId: resolveAutomationId(opts.automationId, profile),
    token,
    physicianId: decodeJwtId(token),
    supportMode: session.mode === "support",
    providerMail,
  });
  return { ...built, beBase: client.base };
}
```
`src/uipath/payload.ts` (new):
```ts
// UiPath queue-item serializer: turns the canonical native-typed content from
// orchestrator/queue-item.ts into the AddQueueItem request body. UiPath's robot reads the
// array fields and callbackContext as JSON STRINGS, so those (and only those) are stringified.

import { getUipath, type UipathConfig } from "../config/config.js";
import type { QueueItemPayload, QueueItemPayloadArgs } from "../orchestrator/backend.js";
import { guardQueueItemSafety } from "../orchestrator/safety.js";

const STRINGIFIED_FIELDS = [
  "DescriptionOfService",
  "Diagnoses",
  "ServiceCode",
  "Treatments",
  "Units",
  "callbackContext",
] as const;

export function serializeUipathContent(content: Record<string, unknown>): Record<string, unknown> {
  const out = { ...content };
  for (const k of STRINGIFIED_FIELDS) if (k in out) out[k] = JSON.stringify(out[k]);
  return out;
}

// Require the queue-build-specific UiPath fields (only needed by build_queue_item).
function requireQueueFields(u: UipathConfig): {
  queueUrl: string;
  addQueueItemPath: string;
  folderPath: string;
} {
  const { queueUrl, addQueueItemPath, folderPath } = u;
  if (queueUrl && addQueueItemPath && folderPath) return { queueUrl, addQueueItemPath, folderPath };
  const missing = (["queueUrl", "addQueueItemPath", "folderPath"] as const).filter((k) => !u[k]);
  throw new Error(
    `uipath config is missing fields required for build_queue_item: ${missing.join(", ")}. ` +
      "Add them to your COPILOT_MCP_CONFIG uipath block (see copilot-mcp.config.example.json).",
  );
}

export function uipathBuildQueueItemPayload(args: QueueItemPayloadArgs): QueueItemPayload {
  const uipath = getUipath();
  const { queueUrl, addQueueItemPath, folderPath } = requireQueueFields(uipath);
  // Single enforcement point for the IsApproved=false rule.
  const specific = guardQueueItemSafety(
    {
      ...serializeUipathContent(args.content),
      queueUrl,
      serverURL: uipath.serverUrlByEnv?.[args.env] ?? args.beBase ?? "",
    },
    "force",
  ).specificContent;

  return {
    payload: {
      itemData: {
        Name: args.queueName,
        Priority: "Normal",
        Reference: `${args.orderUid}-${Math.floor(Date.now() / 1000)}`,
        SpecificContent: specific,
      },
    },
    notes: [
      "To submit: POST payload.itemData as JSON to meta.postUrl with headers " +
        "'Authorization: Bearer <your uipath.bearer>' and " +
        `'X-UIPATH-FolderPath: ${folderPath}'. Never done automatically by this tool.`,
    ],
    meta: { postUrl: uipath.orchestratorUrl.replace(/\/$/, "") + addQueueItemPath, folderPath },
  };
}
```
`src/uipath/actions.ts` — four hunks (v1.33.1 line numbers):
1. Imports, delete line 16: `  jobDeepLinkBase,` (the list stays alphabetical: `fetchJobByKey, getQueueItem, type QueueItem, …`).
2. `AddQueueItemResult` (lines 56–63), after line 59 `  itemId: number; // id of the created item (0 if the response omitted it)` add:
```ts
  queueDefinitionId: number; // the queue it landed in (0 if the response omitted it)
```
3. `addQueueItem`'s return (lines 97–105), after `    itemId: item.id,` add `    queueDefinitionId: item.queueDefinitionId,`.
4. `StartJobResult` (lines 155–159): delete line 157 `  jobDeepLinkBase: string;`; and line 192 `  return { env: args.env, jobDeepLinkBase: jobDeepLinkBase(), jobs };` → `  return { env: args.env, jobs };`.

`src/uipath/backend.ts` — imports. Task 3's import block already has `ExpectedError` (`../shared/expected-error.js`) and `jobDeepLink` (`./uipath.js`); add only the following, merged into the existing blocks (`bunx biome check --write src/uipath/backend.ts` sorts them). Naming rule (consistent, no aliases needed): this file imports the **contract** arg/result types from `../orchestrator/backend.js` and only the **functions** `addQueueItem/deleteQueueItem/startJob/stopJob` from `./actions.js` — it never imports `actions.ts`'s same-named old types (`AddQueueItemArgs`, `StartJobArgs`, …), so nothing clashes:
```ts
// into the existing `import type { … } from "../orchestrator/backend.js"` list
  AddQueueItemArgs,
  AddQueueItemResult,
  DeleteQueueItemArgs,
  DeleteQueueItemResult,
  OrchestratorBackend,
  StartJobArgs,
  StartJobResult,
  StopJobArgs,
  StopJobResult,
// new lines, next to the other "./…" imports
import { addQueueItem, deleteQueueItem, startJob, stopJob } from "./actions.js";
import { uipathBuildQueueItemPayload } from "./payload.js";
```
then append at the end of the file (after Task 3's read functions):
```ts
// ---- writes (Task 4): translate the neutral args onto actions.ts, pre_prod-only there ----

export async function uipathAddQueueItem(args: AddQueueItemArgs): Promise<AddQueueItemResult> {
  const r = await addQueueItem({
    env: args.env,
    queueName: args.queue,
    reference: args.reference,
    priority: args.priority,
    specificContent: args.specificContent,
  });
  return {
    env: r.env,
    queueId: r.queueDefinitionId ? String(r.queueDefinitionId) : "",
    queueName: r.queueName,
    reference: r.reference,
    itemId: String(r.itemId),
    status: r.status,
    forced: r.forced,
  };
}

// UiPath queue-item ids are positive integers; the neutral arg is a string (Apex ids are uuids).
function parseItemId(itemId: string): number {
  if (!/^[1-9]\d*$/.test(itemId) || !Number.isSafeInteger(Number(itemId)))
    throw new ExpectedError(`UiPath queue item ids are positive integers, got '${itemId}'`);
  return Number(itemId);
}

export async function uipathDeleteQueueItem(
  args: DeleteQueueItemArgs,
): Promise<DeleteQueueItemResult> {
  const r = await deleteQueueItem(parseItemId(args.itemId), args.env);
  return {
    env: r.env,
    itemId: String(r.itemId),
    deleted: true,
    previousStatus: r.previousStatus,
    reference: r.reference,
  };
}

export async function uipathStartJob(args: StartJobArgs): Promise<StartJobResult> {
  const r = await startJob({
    env: args.env,
    releaseKey: args.processId,
    inputArguments: args.input,
    jobsCount: args.jobsCount,
  });
  return {
    env: r.env,
    jobs: r.jobs.map((j) => ({
      id: j.key,
      state: j.state,
      processName: j.releaseName,
      link: jobDeepLink(j.key),
    })),
  };
}

export async function uipathStopJob(args: StopJobArgs): Promise<StopJobResult> {
  const r = await stopJob({ env: args.env, jobKey: args.jobId, strategy: args.strategy });
  return { env: r.env, jobId: r.jobKey, strategy: r.strategy, stopped: true };
}

export const uipathBackend: OrchestratorBackend = {
  name: "uipath",
  listJobs: uipathListJobs,
  getJobs: uipathGetJobs,
  getJobLogs: uipathGetJobLogs,
  listQueues: uipathListQueues,
  listQueueItems: uipathListQueueItems,
  findOrderQueueItems: uipathFindOrderQueueItems,
  pullQueueItem: uipathPullQueueItem,
  listProcesses: uipathListProcesses,
  listTriggers: uipathListTriggers,
  buildFaultedJobIssue: uipathBuildFaultedJobIssue,
  buildQueueItemPayload: uipathBuildQueueItemPayload,
  addQueueItem: uipathAddQueueItem,
  deleteQueueItem: uipathDeleteQueueItem,
  startJob: uipathStartJob,
  stopJob: uipathStopJob,
};
```
Delete `src/uipath/queue-item.ts` and `src/uipath/queue-item.test.ts` (the four moved suites live in `src/orchestrator/queue-item.test.ts`).

`src/server.ts` — keep it compiling until Task 5 rewrites the tool. Imports: delete `import { buildQueueItem } from "./uipath/queue-item.js";` and add
```ts
import { buildQueueItemContent } from "./orchestrator/queue-item.js";
import { uipathBuildQueueItemPayload } from "./uipath/payload.js";
```
In the `build_queue_item` handler (v1.33.1 lines 514–529) replace from `      const out = await withHeartbeat(` through `      return ok(out);` with:
```ts
      const built = await withHeartbeat(
        extra,
        "building queue item",
        buildQueueItemContent(orderUid, { profile, env, automationId: automationId ?? null }),
      );
      const { payload, notes, meta } = uipathBuildQueueItemPayload({
        env,
        queueName: built.queueName,
        orderUid,
        content: built.content,
        beBase: built.beBase,
      });
      return ok({ payload, notes: [...built.notes, ...notes], meta: { ...built.meta, ...meta } });
```
Run `bunx biome check --write src/server.ts src/uipath/backend.ts` (import ordering).
- [ ] **Step 10: Run tests** — `bun test src/orchestrator/queue-item.test.ts src/uipath/payload.test.ts src/uipath/backend-writes.test.ts src/uipath/actions.test.ts` → PASS
- [ ] **Step 11: Full gate** — `bun run typecheck && bun test && bunx biome check src` → all green
- [ ] **Step 12: Commit**
```bash
git add -A src/orchestrator src/uipath src/server.ts
git commit -m "refactor(orchestrator): canonical queue-item content, UiPath serializer and write adapters" -m "build_queue_item now assembles backend-neutral native-typed content (orchestrator/queue-item.ts) and each backend serializes it; the UiPath serializer is proven equal to the shipped v1.33.1 output by the golden test. uipathBackend gains the four pre_prod-only write adapters (string ids -> UiPath numeric ids) and is assembled."
```

### Task 5: Registry + `server.ts` rewiring of every orchestrator tool

**Files:**
- Create: `src/orchestrator/registry.ts`, `src/orchestrator/job-batch.ts`, `src/orchestrator/job-batch.test.ts`
- Modify: `src/server.ts` (header comment lines 3–33; imports lines 37–112; `toMessage` line 131; `build_queue_item` + `find_order_queue_items` lines 477–593; `pull_queue_item` … `stop_job` lines 715–1605 incl. `JobKeyResult`/`shapeJobDetail` lines 1238–1283), `src/server.test.ts` (lines 21–23 comment; 114–260 cases), `src/mcp/prompts.ts` (lines 43–55, 181–183, 246, 253, 257), `src/uipath/queue.ts` (reduced to `parseQueueItemUrl`), `src/uipath/faults.ts` (+ `faults.test.ts`: delete `JobFault`/`extractFault`)
- Delete: `src/uipath/queue.test.ts`
- Test: `src/orchestrator/job-batch.test.ts`, `src/server.test.ts`

Line numbers are v1.33.1; Tasks 1–4 shift them slightly, so every region below is anchored by its `// ---- <tool> ---` comment line (never by number) and replaced wholesale.

**Interfaces:**
- Consumes: Task 1 `OrchestratorBackend` and all arg/result types (`src/orchestrator/backend.ts`), `Orchestrator` + `QUEUE_ITEM_STATUSES` (`src/orchestrator/types.ts`), `ExpectedError` (`src/shared/expected-error.ts`); Task 3/4 `uipathBackend` (`src/uipath/backend.ts`); Task 4 `buildQueueItemContent` (`src/orchestrator/queue-item.ts`); existing `stepProgress`, `withHeartbeat`, `mcpLog`, `toolError`.
- Produces: `ENABLED_ORCHESTRATORS`, `EnabledOrchestrator`, `BACKENDS`, `backendFor` (`src/orchestrator/registry.ts`; Task 11/13/14/17 consume/replace them); `resolveJobIds`, `jobEntriesEnvelope` (`src/orchestrator/job-batch.ts`); in `server.ts`: `orchestratorArg`, `envArg`, `writeEnvArg` zod consts (Task 17 may edit their `.describe()` text) and every orchestrator tool per the contract's tool-surface table.
- Decisions: (1) the single-id vs batch shapes of `get_job`/`get_job_logs` follow the contract (`{orchestrator, env, folder, ...entries[0]}` vs `{orchestrator, env, folder, count, jobs}`). (2) Write handlers reply `ok({ orchestrator, ...result })` because every write result already carries `env` (spreading both is TS2783). (3) `find_order_queue_items` replies `{orchestrator, env, orderUid, folder, searchMode, scanned, count, jobIds:{[orchestrator]:[…]}, items, notes?, searchError?}` — `jobIds` is derived from `items[].jobs[].id` (deduped, in item order) until Task 13 (`findOrderAcross`) takes over; `orchestrator` is REQUIRED here (Task 13 relaxes it). (4) `find_stuck_orders` and `src/copilot/sweep.ts` are untouched (Task 14); the `report-faulted-uipath-jobs` prompt keeps its name (Task 15). (5) `ENABLED_ORCHESTRATORS = ["uipath"]` gates what tools expose while `ORCHESTRATORS` (types.ts) already contains `apex`; Task 17 deletes `ENABLED_ORCHESTRATORS`/`EnabledOrchestrator`.

- [ ] **Step 1: Write the failing tests**

`src/orchestrator/job-batch.test.ts`:
```ts
import { describe, expect, test } from "bun:test";
import { ExpectedError } from "../shared/expected-error.js";
import { jobEntriesEnvelope, resolveJobIds } from "./job-batch.js";

describe("resolveJobIds", () => {
  test("a lone jobId is a single (non-batch) lookup", () => {
    expect(resolveJobIds("job-1", undefined)).toEqual({ ids: ["job-1"], batch: false });
  });

  test("jobIds is a batch, even with one id", () => {
    expect(resolveJobIds(undefined, ["job-1"])).toEqual({ ids: ["job-1"], batch: true });
  });

  test("giving both, or neither, is a user-actionable error", () => {
    expect(() => resolveJobIds("job-1", ["job-2"])).toThrow(ExpectedError);
    expect(() => resolveJobIds(undefined, undefined)).toThrow(/either jobId or jobIds/);
  });
});

describe("jobEntriesEnvelope", () => {
  const entries = [
    { id: "a", found: true },
    { id: "b", found: false },
  ];

  test("single flattens the only entry beside the envelope fields", () => {
    expect(
      jobEntriesEnvelope("uipath", "prod", "Authorization", false, [entries[0] ?? {}]),
    ).toEqual({
      orchestrator: "uipath",
      env: "prod",
      folder: "Authorization",
      id: "a",
      found: true,
    });
  });

  test("batch nests every entry under jobs with a count", () => {
    expect(jobEntriesEnvelope("uipath", "prod", "Authorization", true, entries)).toEqual({
      orchestrator: "uipath",
      env: "prod",
      folder: "Authorization",
      count: 2,
      jobs: entries,
    });
  });
});
```
`src/server.test.ts`: (a) replace the comment at lines 21–23 (`// … the UiPath writes (add_queue_item, delete_queue_item,\n// start_job, stop_job) are schema-restricted to the pre_prod dev clone.`) with
```ts
// additively to pre-prod only; the orchestrator writes (add_queue_item, delete_queue_item,
// start_job, stop_job) are schema-restricted to pre_prod.
```
(keep the first comment line `// Tools that mutate state (everything else is read-only). apply_settings_sync writes` as is). `EXPECTED`, `WRITE_TOOLS`, `CLOSED_WORLD_TOOLS` are unchanged. (b) Replace everything from line 114 (`describe("tool input schemas accept representative payloads", () => {`) to the end of the file with the following. `cases` is hoisted to module scope so the new `orchestrator argument` suite can reuse the valid payloads; every orchestrator tool's `valid` carries `orchestrator: "uipath"`, read tools' `invalid` omits it (or breaks a bound: `status`, `url`, 26 ids), write tools' `invalid` is `env: "prod"`. The jobId-XOR-jobIds rule is runtime (`resolveJobIds`), not schema-tested here:
```ts
const JOB_ID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee";
const PROCESS_ID = "609b3602-b6f2-44b2-9ee2-6a8988fac1f5";

const cases: Record<string, { valid: unknown; invalid?: unknown }> = {
  // profile is required (any non-empty string; validated against config at call time).
  // env is required on every env-taking tool. Invalid cases omit a required field or
  // use a wrong type.
  find_clone_candidates: { valid: { profile: "ossm" }, invalid: {} },
  delete_preprod_order: {
    valid: { uids: ["abcdefgh"], profile: "ossm" },
    invalid: { uids: [], profile: "ossm" },
  },
  create_preprod_order: {
    valid: {
      profile: "ossm",
      patientName: "DOE, JOHN",
      patientBirthDate: "01/02/1980",
      insuranceName: "Acme Health",
      typeUid: "abcdefgh-type",
      orderNamesUids: ["abcdefgh-name"],
      icdCodes: [{ code: "M54.5" }],
    },
    invalid: {
      // profile is required
      patientName: "DOE, JOHN",
      patientBirthDate: "01/02/1980",
      insuranceName: "Acme Health",
      typeUid: "abcdefgh-type",
      orderNamesUids: ["abcdefgh-name"],
    },
  },
  submit_preprod_order: {
    valid: { profile: "ossm", orderUid: "abcdefgh" },
    invalid: { orderUid: "abcdefgh" }, // profile is required
  },
  build_queue_item: {
    valid: { orderUid: "abcdefgh", env: "pre_prod", profile: "ossm", orchestrator: "uipath" },
    invalid: { orderUid: "abcdefgh", env: "pre_prod", profile: "ossm" }, // orchestrator is required
  },
  find_order_queue_items: {
    // orchestrator is required until the cross-backend search makes it optional.
    valid: { orderUid: "abcdefgh", env: "prod", orchestrator: "uipath" },
    invalid: { orderUid: "abcdefgh", env: "prod" },
  },
  pull_queue_item: {
    valid: { orchestrator: "uipath", env: "pre_prod", itemId: "123" },
    invalid: { orchestrator: "uipath", url: "not a url" },
  },
  list_queue_items: {
    valid: { env: "pre_prod", orchestrator: "uipath", queue: "PMG", status: "New" },
    invalid: { env: "pre_prod", orchestrator: "uipath", queue: "PMG", status: "Bogus" },
  },
  diff_settings: { valid: { tags: ["orders"], profile: "ossm" }, invalid: { profile: 123 } },
  list_setting_sections: { valid: { tag: "orders" }, invalid: { tag: 123 } },
  get_settings: {
    valid: { env: "pre_prod", profile: "ossm", tags: ["orders"] },
    invalid: { profile: "ossm" }, // env is required
  },
  plan_settings_sync: {
    valid: { profile: "ossm", tags: ["orders"] },
    invalid: {}, // profile is required
  },
  apply_settings_sync: {
    // The actionIds-XOR-all rule is enforced at runtime (ExpectedError), not in the schema.
    valid: { profile: "ossm", actionIds: ["specialties:create:MRI:Cardiology"] },
    invalid: { actionIds: ["specialties:create:MRI:Cardiology"] }, // profile is required
  },
  get_order: {
    valid: { orderUid: "abcdefgh", env: "prod", profile: "ossm" },
    invalid: { orderUid: "abcdefgh", env: "prod" }, // profile is required
  },
  search_orders: {
    valid: { env: "prod", profile: "ossm", insurances: ["OPTUM CARE NETWORK"] },
    invalid: { profile: "ossm" }, // env is required
  },
  get_order_category_stats: {
    valid: { env: "prod", profile: "ossm" },
    invalid: { profile: "ossm" }, // env is required
  },
  get_login_token: {
    valid: { env: "prod", profile: "ossm" },
    invalid: { profile: "ossm" }, // env is required
  },
  list_clinics: {
    valid: { env: "prod" },
    invalid: {}, // env is required
  },
  doctor: { valid: { profile: "ossm" }, invalid: {} }, // profile is required
  build_faulted_job_issue: {
    valid: { env: "prod", orchestrator: "uipath", jobId: JOB_ID },
    invalid: { env: "prod", jobId: JOB_ID }, // orchestrator is required
  },
  list_jobs: {
    valid: { env: "prod", orchestrator: "uipath", processName: "OPTUM" },
    invalid: { env: "prod", processName: "OPTUM" }, // orchestrator is required
  },
  get_job_logs: {
    // The jobId-XOR-jobIds rule is enforced at runtime (ExpectedError), not in the schema.
    valid: { env: "prod", orchestrator: "uipath", jobIds: [JOB_ID, JOB_ID] },
    invalid: { env: "prod", orchestrator: "uipath", jobIds: Array(26).fill(JOB_ID) }, // max 25
  },
  list_queues: {
    valid: { env: "pre_prod", orchestrator: "uipath", nameContains: "auth" },
    invalid: { env: "pre_prod", nameContains: "auth" }, // orchestrator is required
  },
  list_processes: {
    valid: { env: "pre_prod", orchestrator: "uipath" },
    invalid: { env: "pre_prod" }, // orchestrator is required
  },
  list_triggers: {
    valid: { env: "pre_prod", orchestrator: "uipath", queueId: "79926" },
    invalid: { env: "pre_prod", queueId: "79926" }, // orchestrator is required
  },
  get_job: {
    valid: { env: "pre_prod", orchestrator: "uipath", jobId: JOB_ID },
    invalid: { env: "pre_prod", jobId: JOB_ID }, // orchestrator is required
  },
  add_queue_item: {
    valid: {
      env: "pre_prod",
      orchestrator: "uipath",
      queue: "Scratch queue",
      reference: "TEST-1-abcd1234",
      specificContent: { orderUid: "abcd1234" },
    },
    // env is a z.literal("pre_prod") — prod must be rejected at the schema layer
    invalid: {
      env: "prod",
      orchestrator: "uipath",
      queue: "Scratch queue",
      reference: "TEST-1-abcd1234",
      specificContent: { orderUid: "abcd1234" },
    },
  },
  delete_queue_item: {
    valid: { env: "pre_prod", orchestrator: "uipath", itemId: "123" },
    invalid: { env: "prod", orchestrator: "uipath", itemId: "123" }, // prod rejected by the schema literal
  },
  start_job: {
    valid: { env: "pre_prod", orchestrator: "uipath", processId: PROCESS_ID },
    invalid: { env: "prod", orchestrator: "uipath", processId: PROCESS_ID }, // prod rejected by the schema literal
  },
  stop_job: {
    valid: { env: "pre_prod", orchestrator: "uipath", jobId: JOB_ID, strategy: "Kill" },
    invalid: { env: "prod", orchestrator: "uipath", jobId: JOB_ID }, // prod rejected by the schema literal
  },
  build_mcp_issue: {
    valid: {
      kind: "feedback",
      title: "clone_order output is hard to scan",
      details: "The per-order rows mix successes and failures; a split summary would read better.",
    },
    invalid: { kind: "complaint", title: "x", details: "too short" },
  },
};

describe("tool input schemas accept representative payloads", () => {
  for (const [name, c] of Object.entries(cases)) {
    test(`${name} accepts a valid payload`, () => {
      expect(registered[name]?.inputSchema?.safeParse(c.valid).success).toBe(true);
    });
    if (c.invalid !== undefined) {
      test(`${name} rejects an invalid payload`, () => {
        expect(registered[name]?.inputSchema?.safeParse(c.invalid).success).toBe(false);
      });
    }
  }
});

// Every tool that dispatches through backendFor() must name its orchestrator, and only an
// enabled one. Derived from the cases above so a new orchestrator tool can't skip the check.
describe("orchestrator argument", () => {
  const ORCHESTRATOR_TOOLS = [
    "build_queue_item",
    "find_order_queue_items",
    "pull_queue_item",
    "list_queue_items",
    "list_jobs",
    "get_job_logs",
    "build_faulted_job_issue",
    "list_queues",
    "list_processes",
    "list_triggers",
    "get_job",
    "add_queue_item",
    "delete_queue_item",
    "start_job",
    "stop_job",
  ] as const;

  for (const name of ORCHESTRATOR_TOOLS) {
    test(`${name} rejects an unknown orchestrator`, () => {
      const valid = (cases[name]?.valid ?? {}) as Record<string, unknown>;
      const schema = registered[name]?.inputSchema;
      expect(schema?.safeParse({ ...valid, orchestrator: "uipath" }).success).toBe(true);
      expect(schema?.safeParse({ ...valid, orchestrator: "bogus" }).success).toBe(false);
    });
  }
});
```
- [ ] **Step 2: Run them to verify they fail**
Run: `bun test src/orchestrator/job-batch.test.ts src/server.test.ts` — Expected: FAIL (`./job-batch.js` missing; schemas still take `jobKey`/`releaseKey`/…, no `orchestrator`).
- [ ] **Step 3: Implement**

3a. `src/orchestrator/registry.ts` (new):
```ts
// Which backend serves each orchestrator value. ENABLED_ORCHESTRATORS gates what the tools
// expose; retiring a backend = delete its directory, its entry here and its ORCHESTRATORS value.

import { uipathBackend } from "../uipath/backend.js";
import type { OrchestratorBackend } from "./backend.js";
import type { Orchestrator } from "./types.js";

export const ENABLED_ORCHESTRATORS = ["uipath"] as const satisfies readonly Orchestrator[];
export type EnabledOrchestrator = (typeof ENABLED_ORCHESTRATORS)[number];

export const BACKENDS: Record<EnabledOrchestrator, OrchestratorBackend> = {
  uipath: uipathBackend,
};

export const backendFor = (o: EnabledOrchestrator): OrchestratorBackend => BACKENDS[o];
```
3b. `src/orchestrator/job-batch.ts` (new):
```ts
// get_job / get_job_logs accept one `jobId` XOR a `jobIds` batch; the answer shape follows.

import type { Env } from "../config/config.js";
import { ExpectedError } from "../shared/expected-error.js";
import type { Orchestrator } from "./types.js";

// A single id keeps failing the call on a bad lookup; a batch isolates failures per entry.
export function resolveJobIds(
  jobId: string | undefined,
  jobIds: string[] | undefined,
): { ids: string[]; batch: boolean } {
  if (jobId && jobIds) throw new ExpectedError("provide only one of jobId or jobIds");
  if (jobIds) return { ids: jobIds, batch: true };
  if (jobId) return { ids: [jobId], batch: false };
  throw new ExpectedError("provide either jobId or jobIds");
}

// Single => the entry's fields flat under the envelope; batch => { count, jobs: entries }.
export function jobEntriesEnvelope(
  orchestrator: Orchestrator,
  env: Env,
  folder: string,
  batch: boolean,
  entries: object[],
): object {
  if (batch) return { orchestrator, env, folder, count: entries.length, jobs: entries };
  return { orchestrator, env, folder, ...entries[0] };
}
```
3c. `src/server.ts`.

*Header comment:* replace lines 3–33 (from `// MCP server: EHR Copilot operations …` through `//   - doctor … (read-only)`; keep the trailing `//\n// See the copilot + copilot-order-mirror skills …` lines) with the following — it now lists all 32 registered tools (v1.33.1's list omitted `pull_queue_item`, `list_queue_items`, `list_jobs`, `get_job_logs`, `stop_job`, `find_stuck_orders`, `list_clinics`, `build_mcp_issue`):
```ts
// MCP server: EHR Copilot operations (order cloning + orchestrator execution analysis).
// Thin wiring layer — src/ is organized by domain (config/, copilot/, orchestrator/, uipath/, mcp/, shared/);
// tool logic lives in per-domain modules, e.g.:
//   - config/config.ts            single-file config (copilot creds + orchestrator args) + validation
//   - orchestrator/registry.ts    backendFor(orchestrator): the backend every orchestrator tool dispatches through
//   - orchestrator/queue-item.ts  build_queue_item's canonical order -> queue-item content
//   - uipath/backend.ts           the UiPath implementation of the orchestrator backend interface
// Tools (the orchestrator ones take an explicit orchestrator and reply { orchestrator, env, ... }):
//   - find_clone_candidates    list recent prod orders that are actually cloneable
//   - delete_preprod_order     delete pre-prod order(s)
//   - create_preprod_order     mint a fresh pre-prod order from an explicit spec (stops at forReview, never submits)
//   - submit_preprod_order     submit a pre-prod order sitting at forReview (explicit, separate write)
//   - build_queue_item         build an orchestrator queue-item request from an order (build-only)
//   - find_order_queue_items   find the queue item(s) an order flowed through + their job ids (read-only)
//   - pull_queue_item          fetch one queue item as a ready-to-run test payload, IsApproved forced false (read-only)
//   - list_queue_items         browse a portal's queue for triage (read-only)
//   - list_jobs                list recent jobs in a folder (read-only)
//   - get_job_logs             fetch one job's logs, or several via jobIds (read-only)
//   - build_faulted_job_issue  build a GitHub issue payload for a faulted job (read-only; posts nothing)
//   - list_queues              list queues in a folder (read-only; discovers pre-prod queue ids)
//   - list_processes           list processes in a folder (read-only; processId + pin verification)
//   - list_triggers            list triggers in a folder (read-only; verify queue trigger -> process wiring)
//   - get_job                  fetch one job's state/output by id, or several via jobIds (read-only)
//   - add_queue_item           POST one item to a pre-prod queue (pre_prod-only; test-safety guarded)
//   - delete_queue_item        delete a queue item from the pre-prod folder (pre_prod-only)
//   - start_job                start job(s) for a pre-prod process (pre_prod-only)
//   - stop_job                 stop or kill a pre-prod job (pre_prod-only; fetch-first)
//   - find_stuck_orders        scan recent orders for non-terminal ('stuck') statuses, optionally cross-checked against UiPath (read-only)
//   - diff_settings            diff an account's settings between prod and pre-prod (read-only)
//   - list_setting_sections    list the settings sections/tags the settings tools can scope to (read-only, no network)
//   - get_settings             fetch one env's settings sections (read-only)
//   - plan_settings_sync       plan the additive prod -> pre-prod sync actions (read-only)
//   - apply_settings_sync      execute selected planned sync actions against pre-prod (additive only)
//   - get_order                fetch a single order's normalized detail by uid (read-only)
//   - search_orders            filter orders by location/insurance/referredTo/type/auth/date range (read-only)
//   - get_order_category_stats per-folder order counts for a given filter (read-only)
//   - get_login_token          log into the Copilot BE for a profile/env and return the session JWT (read-only)
//   - list_clinics             list the clinics the support account can switch into (read-only)
//   - build_mcp_issue          compose a GitHub issue for feedback/bugs on this server (posts nothing)
//   - doctor                   probe the BE + orchestrator connections and report what's reachable (read-only)
```
*Imports:* the block from `import { realpathSync } from "node:fs";` through the last `} from "./uipath/uipath.js";` must end up exactly as below. Everything dropped is now owned by `uipathBackend`: the `normalizeOutput` import (`./copilot/output-schema.js`), `msBetween`, and every import from `./uipath/actions.js`, `./uipath/faults.js`, `./uipath/log-digest.js` (or its Task 1 location), `./uipath/payload.js` (Task 4's interim import), `./uipath/queue.js`, `./uipath/raw-message.js`, `./uipath/uipath.js`:
```ts
import { realpathSync } from "node:fs";
import process from "node:process";
import { fileURLToPath } from "node:url";
import { McpServer, ResourceTemplate } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { z } from "zod";
import { configStatus, getFeedbackConfig, loadConfig, onConfigReload } from "./config/config.js";
import {
  assertPreProdClient,
  type BeOrder,
  fetchOrder,
  filterOrders,
  normalizeOrder,
  ORDER_MODE,
  submitOrder,
  toMDY,
} from "./copilot/copilot-client.js";
import { runDoctor } from "./copilot/doctor.js";
import { type MintSpec, mintPreprodOrder } from "./copilot/mirror.js";
import { orderDocuments } from "./copilot/order-docs.js";
import { getOrderCategoryStats, searchOrders } from "./copilot/order-search.js";
import { connect, listClinics } from "./copilot/session.js";
import {
  applySettingsSync,
  diffSettings,
  getSettings,
  listSettingSections,
  planSettingsSyncOp,
} from "./copilot/settings/index.js";
import { findStuckOrders } from "./copilot/sweep.js";
import { formatMcpIssue, toolError } from "./mcp/feedback.js";
import {
  mcpLog,
  registerLogging,
  reportProgress,
  stepProgress,
  withHeartbeat,
} from "./mcp/notify.js";
import { registerPrompts } from "./mcp/prompts.js";
import {
  ACCOUNT_AUTOMATION_IDS,
  CONFIG_GUIDE,
  ORDER_LIFECYCLE,
  PORTALS,
  type PortalEntry,
  QUEUE_ITEM_SCHEMA,
  RESULT_CONTRACT,
  SAFETY_RULES,
  UIPATH_FOLDERS,
} from "./mcp/reference.js";
import { jobEntriesEnvelope, resolveJobIds } from "./orchestrator/job-batch.js";
import { buildQueueItemContent } from "./orchestrator/queue-item.js";
import { backendFor, ENABLED_ORCHESTRATORS } from "./orchestrator/registry.js";
import { QUEUE_ITEM_STATUSES } from "./orchestrator/types.js";
import { stringProp } from "./shared/util.js";
```
*Dead helper:* delete `const toMessage = (e: unknown): string => …` (only the deleted `get_job`/`get_job_logs` handlers used it; `tsc` reports TS6133 otherwise).

*Region 1 — build_queue_item + find_order_queue_items:* replace everything from the line `// ---- build_queue_item ---…` up to (not including) `// ---- Resources (static reference data) ---…` with:
```ts
// ---- Orchestrator tools: shared argument schemas ---------------------------
// Every orchestrator tool takes an explicit env and orchestrator (never defaulted) and
// replies { orchestrator, env, ...result } so agents read one vocabulary across backends.
const orchestratorArg = z
  .enum(ENABLED_ORCHESTRATORS)
  .describe(`Which orchestrator: ${ENABLED_ORCHESTRATORS.join(" | ")} (required)`);
const envArg = z
  .enum(["prod", "pre_prod"])
  .describe(
    "Selects the orchestrator's folder: UiPath prod='Authorization', pre_prod='Authorization Dev Clone'; " +
      "Apex prod='Authorization', pre_prod='Authorization Test' (required)",
  );
const writeEnvArg = z
  .literal("pre_prod")
  .describe(
    "Orchestrator writes are pre_prod-only (UiPath dev clone 'Authorization Dev Clone', Apex " +
      "'Authorization Test'); 'prod' is rejected by the schema (required)",
  );

// ---- build_queue_item ----------------------------------------------------
server.registerTool(
  "build_queue_item",
  {
    title: "Build an orchestrator queue-item request from an order",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    }, // BUILD ONLY — fetches an order, never POSTs
    description:
      "Fetch an EHR Copilot order (prod or pre-prod) and build the queue-item request payload for " +
      "the chosen orchestrator from its details. BUILD ONLY — never POSTs to the orchestrator; " +
      "submit it yourself (for UiPath, to meta.postUrl with your own Orchestrator bearer token — see " +
      "notes; or hand payload.itemData's Name/Reference/SpecificContent to add_queue_item). " +
      "IsApproved is always false. The payload's wire format is the orchestrator's own: UiPath " +
      "carries the array fields and callbackContext as JSON strings, Apex keeps native types. " +
      "Returns {payload, notes, meta}.",
    inputSchema: {
      orderUid: z.string().min(8).describe("Order UID to fetch (must exist in the chosen env)"),
      env: z
        .enum(["prod", "pre_prod"])
        .describe("Which env the order lives in; drives fetch + token + serverURL (required)"),
      profile: z
        .string()
        .min(1)
        .describe("Credential profile / account name from config (required)"),
      orchestrator: orchestratorArg,
      automationId: z
        .string()
        .min(1)
        .optional()
        .describe(
          "The account's real automation id (from its orchestrator asset). Usually unnecessary — " +
            "when omitted, it's resolved from `profile` against the known account table; only " +
            "needed to override that or for an account not yet in the table (falls back to a " +
            "placeholder random UUID + warning note in that case).",
        ),
    },
  },
  async ({ orderUid, env, profile, orchestrator, automationId }, extra) => {
    try {
      const built = await withHeartbeat(
        extra,
        "building queue item",
        buildQueueItemContent(orderUid, { profile, env, automationId: automationId ?? null }),
      );
      const { payload, notes, meta } = backendFor(orchestrator).buildQueueItemPayload({
        env,
        queueName: built.queueName,
        orderUid,
        content: built.content,
        beBase: built.beBase,
      });
      return ok({ payload, notes: [...built.notes, ...notes], meta: { ...built.meta, ...meta } });
    } catch (e) {
      return toolError("build_queue_item", e, VERSION);
    }
  },
);

// ---- find_order_queue_items ------------------------------------------------
server.registerTool(
  "find_order_queue_items",
  {
    title: "Find the queue item(s) a Copilot order flowed through",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Given a Copilot orderUid, find the orchestrator queue item(s) it flowed through and the " +
      "ids of the jobs that ran them — every order flows through a queue item carrying the " +
      "orderUid, so this reaches faulted/still-running consumer jobs an OutputArguments scan can't. " +
      "READ-ONLY. Each item's full specificContent is returned deliberately, in full " +
      "(member/clinical fields visible) — the point is divergence analysis: comparing what Copilot " +
      "actually sent to the orchestrator against the order record from get_order. Only the JWT " +
      "`token` is redacted ('[redacted]': a live credential, opaque, useless for divergence). Feed " +
      "`jobIds[orchestrator]` straight into get_job's jobIds batch with includeLogDigest=true to " +
      "diagnose every run and retry in one call. `count === 0` means the order never reached the " +
      "orchestrator; an empty jobIds list with items present means it is queued but not yet " +
      "picked up. Pass env (required) or, with it, an explicit folder name override. `since` " +
      "narrows the search (automatically retried unbounded if nothing matches) — pass the order's " +
      "creationDate from get_order. Returns {orchestrator, env, orderUid, folder, searchMode, " +
      "scanned, count, jobIds:{<orchestrator>:[id…]}, items:[{orchestrator,queueId,queueName," +
      "itemId,reference,orderUid,status,rawStatus,exceptionType,createdAt,endedAt," +
      "jobs:[{orchestrator,id,state,neverStarted}],specificContent,details}], notes?, searchError?}.",
    inputSchema: {
      env: envArg,
      orderUid: z.string().min(8).describe("Copilot orderUid to correlate"),
      orchestrator: orchestratorArg,
      folder: z.string().optional().describe("Explicit folder name override (wins over env)"),
      since: z
        .string()
        .optional()
        .describe(
          "ISO lower bound on the queue item's creation time — pass the order's creationDate " +
            "from get_order; retried without the bound if nothing matches",
        ),
      top: z.number().int().min(1).max(500).optional().default(50),
      includeSpecificContent: z.boolean().optional().default(true),
    },
  },
  async ({ env, orderUid, orchestrator, folder, since, top, includeSpecificContent }, extra) => {
    try {
      const result = await withHeartbeat(
        extra,
        "Orchestrator",
        backendFor(orchestrator).findOrderQueueItems({
          env,
          orderUid,
          folder,
          since,
          top,
          includeSpecificContent,
        }),
      );
      const jobIds = [...new Set(result.items.flatMap((i) => i.jobs.map((j) => j.id)))];
      return ok({
        orchestrator,
        env,
        orderUid,
        ...result,
        count: result.items.length,
        jobIds: { [orchestrator]: jobIds },
      });
    } catch (e) {
      return toolError("find_order_queue_items", e, VERSION);
    }
  },
);
```
*Region 2 — pull_queue_item … stop_job:* replace everything from the line `// ---- pull_queue_item ---…` up to (not including) `// ---- find_stuck_orders ---…` (this deletes `JobKeyResult` and `shapeJobDetail` too) with:
```ts
// ---- pull_queue_item -----------------------------------------------------
server.registerTool(
  "pull_queue_item",
  {
    title: "Pull a queue item as a test payload",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Fetch a single orchestrator queue item and return its specificContent as a ready-to-run " +
      "test payload, mapped to its portal. READ-ONLY. IsApproved is ALWAYS forced false so a test " +
      "run can never submit a real auth. UiPath: pass `url` (an Orchestrator queue-item URL — env " +
      "is derived from its fid) or `itemId` + `env` (+ optional `folderId`). Apex: pass `env` + " +
      "`queueId` + `itemId`. Returns {orchestrator, env, item:QueueItemCore (specificContent " +
      "present), portal:{matched,key,portalDir,buildArtifact,family}, isApprovedForced, " +
      "suggestedFilename, queueNameError?}.",
    inputSchema: {
      orchestrator: orchestratorArg,
      env: z
        .enum(["prod", "pre_prod"])
        .optional()
        .describe(
          "Folder/env; UiPath derives it from the URL's fid when a url is given, otherwise required",
        ),
      url: z
        .string()
        .url()
        .optional()
        .describe(
          "UiPath only: Orchestrator queue-item URL (copy from the UI; fid + item id are parsed out)",
        ),
      queueId: z
        .string()
        .min(1)
        .optional()
        .describe(
          "Apex: the queue's id (from list_queues); UiPath derives the queue from the item",
        ),
      itemId: z
        .string()
        .min(1)
        .optional()
        .describe("Queue item id (UiPath numeric id as a string | Apex uuid); use instead of url"),
      folderId: z
        .string()
        .optional()
        .describe("UiPath: numeric folder id (OrganizationUnitId) override when no url is given"),
    },
  },
  async ({ orchestrator, env, url, queueId, itemId, folderId }, extra) => {
    try {
      const result = await withHeartbeat(
        extra,
        "Orchestrator",
        backendFor(orchestrator).pullQueueItem({ env, url, queueId, itemId, folderId }),
      );
      return ok({ orchestrator, ...result });
    } catch (e) {
      return toolError("pull_queue_item", e, VERSION);
    }
  },
);

// ---- list_queue_items ----------------------------------------------------
server.registerTool(
  "list_queue_items",
  {
    title: "Browse a queue",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "List items in a portal's queue for triage, newest first. `queue` is a portal key/alias " +
      "(e.g. 'PMG', 'MOLINA' — resolved to that portal's submit or sync queue) or the " +
      "orchestrator's own queue id (from list_queues; required for pre_prod UiPath queues, whose " +
      "ids differ from the static portal registry). Optionally filter by status. READ-ONLY, " +
      "PHI-light projection (specificContent is null; member id/name live under each item's " +
      "details). Returns {orchestrator, env, queueId, queueName, count, items:[{orchestrator," +
      "queueId,queueName,itemId,reference,orderUid,status,rawStatus,exceptionType,createdAt," +
      "endedAt,jobs,specificContent:null,details}]}.",
    inputSchema: {
      env: envArg,
      orchestrator: orchestratorArg,
      queue: z
        .string()
        .min(1)
        .describe("Portal key/alias, e.g. 'PMG', or the orchestrator's queue id (see list_queues)"),
      queueKind: z
        .enum(["submit", "sync"])
        .optional()
        .default("submit")
        .describe("Which queue for a portal key (default submit)"),
      status: z.enum(QUEUE_ITEM_STATUSES).optional().describe("Optional queue-item status filter"),
      top: z.number().int().min(1).max(200).optional().default(50).describe("Max items to return"),
    },
  },
  async ({ env, orchestrator, queue, queueKind, status, top }, extra) => {
    try {
      const result = await withHeartbeat(
        extra,
        "Orchestrator",
        backendFor(orchestrator).listQueueItems({ env, queue, queueKind, status, top }),
      );
      return ok({ orchestrator, env, count: result.items.length, ...result });
    } catch (e) {
      return toolError("list_queue_items", e, VERSION);
    }
  },
);

// ---- list_jobs -----------------------------------------------------------
server.registerTool(
  "list_jobs",
  {
    title: "List recent orchestrator jobs",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "List the most recent jobs in an env's folder (newest first), without order correlation. " +
      "READ-ONLY. To diagnose a job from the list, call get_job with includeLogDigest=true " +
      "(condensed failure digest + fault signature) — get_job also takes a jobIds array to " +
      "diagnose several jobs from this list in one call. Returns {orchestrator, env, folder, " +
      "count, jobs:[{orchestrator,id,state (dashboard label),rawState (the backend's own status " +
      "string),terminal,process:{id,name,version},queueItem:{queueId,itemId,reference}|null," +
      "createdAt,startedAt,endedAt,durationMs,attempt,retryOfJobId,fault,link (dashboard deep " +
      "link),details}]}.",
    inputSchema: {
      env: envArg,
      orchestrator: orchestratorArg,
      folder: z.string().optional().describe("Explicit folder name override (wins over env)"),
      since: z.string().optional().describe("ISO date lower bound on creation time"),
      top: z.number().int().min(1).max(500).optional().default(50).describe("Max jobs to return"),
      processName: z
        .string()
        .optional()
        .describe("Substring filter on the process name, e.g. 'OPTUM'"),
    },
  },
  async ({ env, orchestrator, folder, since, top, processName }, extra) => {
    try {
      const result = await withHeartbeat(
        extra,
        "Orchestrator",
        backendFor(orchestrator).listJobs({ env, folder, since, top, processName }),
      );
      return ok({
        orchestrator,
        env,
        folder: result.folder,
        count: result.jobs.length,
        jobs: result.jobs,
      });
    } catch (e) {
      return toolError("list_jobs", e, VERSION);
    }
  },
);

// ---- get_job_logs --------------------------------------------------------
server.registerTool(
  "get_job_logs",
  {
    title: "Get a job's robot logs",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Fetch execution logs (oldest first, capped 500 per job) for a job by its id, optionally " +
      "with the video recording. Pass jobIds (up to 25) instead of jobId to fetch logs for several " +
      "jobs in ONE call — use this instead of looping get_job_logs once per job. All row filters " +
      "are opt-in and apply to every job in the batch — by default the full (capped) log set is " +
      "returned, but each Message is truncated to 400 chars (pass fullMessages=true for the " +
      "untruncated text, e.g. to read a complete stack trace). READ-ONLY. A jobIds batch never " +
      "fails as a whole on one job's error — that job's entry carries error instead, the rest " +
      "still return. `video` is always best-effort: a failure there surfaces as videoError, never " +
      "blocks the logs; it is {kind:'url',url} when the orchestrator serves a link, or " +
      "{kind:'artifact',artifact,sizeBytes,fetchWith:'get_job_artifacts'} when the recording is a " +
      "job artifact to download. A job whose logs cannot be read carries logsUnavailable " +
      "{reason: running | never-started | artifact-policy, detail}. includeRawFields=true adds " +
      "each row's backend-specific structured fields as `raw` (UiPath: the parsed RawMessage — " +
      "transactionId, queueName, processingExceptionType/Reason, execution timings, any custom " +
      "Add-Log-Fields data, ~3x the response size on real jobs; Apex: the bot's NDJSON `fields`); " +
      "`raw` is null when a row has none. Returns {orchestrator, env, folder, jobId, logs:[{Level," +
      "Message,TimeStamp,raw?}], returned, totalMatching?, truncated, logsUnavailable?, video?, " +
      "videoError?, error?} for a single jobId, or {orchestrator, env, folder, count, jobs:[{jobId," +
      "logs,returned,totalMatching?,truncated,logsUnavailable?,error?,video?,videoError?}]} for jobIds.",
    inputSchema: {
      env: envArg,
      orchestrator: orchestratorArg,
      jobId: z
        .string()
        .min(1)
        .optional()
        .describe(
          "The job's id (from find_order_queue_items / list_jobs). Provide this OR jobIds, not both",
        ),
      jobIds: z
        .array(z.string().min(1))
        .min(1)
        .max(25)
        .optional()
        .describe(
          "Batch: fetch logs for several jobs in one call. Provide this OR jobId, not both",
        ),
      folder: z.string().optional().describe("Explicit folder name override (wins over env)"),
      includeVideo: z
        .boolean()
        .optional()
        .default(false)
        .describe("Also fetch the job's video recording (extra round-trip)"),
      minLevel: z
        .enum(["warn", "error"])
        .optional()
        .describe(
          "Only logs at or above this level (warn=Warn/Error/Fatal, error=Error/Fatal). " +
            "Default: all levels",
        ),
      contains: z
        .string()
        .min(1)
        .optional()
        .describe("Only logs whose Message contains this substring (case-sensitive)"),
      onlyFailures: z
        .boolean()
        .optional()
        .default(false)
        .describe(
          "Semantic failure filter: keep only logs at error/fatal level OR whose message " +
            "mentions failure indicators (exception, failed, timeout, 'unable to', denied, …) " +
            "regardless of level. Catches failures logged at Info",
        ),
      tail: z
        .number()
        .int()
        .positive()
        .max(500)
        .optional()
        .describe("Only the last N matching logs (still returned oldest first)"),
      fullMessages: z
        .boolean()
        .optional()
        .default(false)
        .describe(
          "Return full untruncated Message text (default: truncated to 400 chars, matching " +
            "get_job's log digest). Use when you need a complete stack trace.",
        ),
      includeRawFields: z
        .boolean()
        .optional()
        .default(false)
        .describe(
          "Attach each row's backend-specific structured fields as `raw` — see the tool " +
            "description. Off by default: UiPath's parsed RawMessage is ~3x the response size.",
        ),
    },
  },
  async (
    {
      env,
      orchestrator,
      jobId,
      jobIds,
      folder,
      includeVideo,
      minLevel,
      contains,
      onlyFailures,
      tail,
      fullMessages,
      includeRawFields,
    },
    extra,
  ) => {
    try {
      const { ids, batch } = resolveJobIds(jobId, jobIds);
      const result = await backendFor(orchestrator).getJobLogs(
        {
          env,
          jobIds: ids,
          batch,
          folder,
          filter: { minLevel, contains, onlyFailures, tail },
          fullMessages,
          includeRawFields,
          includeVideo,
        },
        stepProgress(extra),
      );
      return ok(jobEntriesEnvelope(orchestrator, env, result.folder, batch, result.entries));
    } catch (e) {
      return toolError("get_job_logs", e, VERSION);
    }
  },
);

// ---- build_faulted_job_issue ---------------------------------------------
server.registerTool(
  "build_faulted_job_issue",
  {
    title: "Build a GitHub issue payload for a faulted job",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Read a faulted job (by its id) and its logs, and BUILD a ready-to-post GitHub issue " +
      "payload for the RPA repo. READ-ONLY: this does NOT post to GitHub — it returns " +
      "{orchestrator,env,repo,title,body,labels,faultSignature,searchQuery,recurrenceComment," +
      "found,logsError?}. The logs fetch is best-effort: a failure surfaces as logsError (issue " +
      "still built from the job alone) instead of failing the whole call. The caller (the " +
      "report-faulted-uipath-jobs prompt) hands this to the host's GitHub MCP server: search " +
      "with `searchQuery`, then add `recurrenceComment` to the matching open issue, or " +
      "create_issue with title/body/labels. `faultSignature` is the normalized error (stable " +
      "across reruns) used to dedupe.",
    inputSchema: {
      env: envArg,
      orchestrator: orchestratorArg,
      jobId: z.string().min(1).describe("The faulted job's id (from list_jobs)"),
      folder: z.string().optional().describe("Explicit folder name override (wins over env)"),
      repo: z
        .string()
        .optional()
        .describe("Target repo owner/name (default Apex-Medical-AI-Inc/RPAPlaywright)"),
      labels: z.array(z.string()).optional().describe("Issue labels (default uipath-fault, bug)"),
    },
  },
  async ({ env, orchestrator, jobId, folder, repo, labels }) => {
    try {
      const result = await backendFor(orchestrator).buildFaultedJobIssue({
        env,
        jobId,
        folder,
        repo,
        labels,
      });
      return ok({ orchestrator, env, ...result });
    } catch (e) {
      return toolError("build_faulted_job_issue", e, VERSION);
    }
  },
);

// ---- list_queues -----------------------------------------------------------
server.registerTool(
  "list_queues",
  {
    title: "List queues in a folder",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "List the queues (the queues themselves) in an env's folder, sorted by name. READ-ONLY. Use " +
      "this to discover a queue id for list_queue_items / pull_queue_item and the exact queue " +
      "name for add_queue_item — pre_prod queue ids differ from the prod ids in the static portal " +
      "registry. Queue CREATION is deliberately not a tool (rare one-time setup — do it in the " +
      "orchestrator UI). Returns {orchestrator, env, count, queues:[{orchestrator,id,name," +
      "processId,active,details}]}.",
    inputSchema: {
      env: envArg,
      orchestrator: orchestratorArg,
      folder: z.string().optional().describe("Explicit folder name override (wins over env)"),
      nameContains: z.string().optional().describe("Substring filter on the queue name"),
      top: z.number().int().min(1).max(200).optional().default(100).describe("Max queues"),
    },
  },
  async ({ env, orchestrator, folder, nameContains, top }) => {
    try {
      const queues = await backendFor(orchestrator).listQueues({ env, folder, nameContains, top });
      return ok({ orchestrator, env, count: queues.length, queues });
    } catch (e) {
      return toolError("list_queues", e, VERSION);
    }
  },
);

// ---- list_processes --------------------------------------------------------
server.registerTool(
  "list_processes",
  {
    title: "List processes in a folder",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "List the processes (UiPath releases / Apex workflows) in an env's folder, sorted by name. " +
      "READ-ONLY. `id` is what start_job takes as processId; `version` is the pinned package " +
      "version — a UiPath job resolves it at START, so verify the pin here right before starting " +
      "(repinning is manual in the orchestrator UI; this MCP never repins). Returns " +
      "{orchestrator, env, count, processes:[{orchestrator,id,name,package,version,active,details}]}.",
    inputSchema: {
      env: envArg,
      orchestrator: orchestratorArg,
      folder: z.string().optional().describe("Explicit folder name override (wins over env)"),
      nameContains: z.string().optional().describe("Substring filter on the process name"),
      top: z.number().int().min(1).max(200).optional().default(100).describe("Max processes"),
    },
  },
  async ({ env, orchestrator, folder, nameContains, top }) => {
    try {
      const processes = await backendFor(orchestrator).listProcesses({
        env,
        folder,
        nameContains,
        top,
      });
      return ok({ orchestrator, env, count: processes.length, processes });
    } catch (e) {
      return toolError("list_processes", e, VERSION);
    }
  },
);

// ---- list_triggers ---------------------------------------------------------
server.registerTool(
  "list_triggers",
  {
    title: "List triggers (queue + time) in a folder",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "List the triggers (queue + time schedules) in an env's folder. READ-ONLY. Queue triggers " +
      "carry a queueId + the processId they start — use this to verify a queue's trigger points " +
      "at YOUR pre-prod process before enqueueing (cross-check processId against list_processes). " +
      "Time triggers have queueId null and a cron. Returns {orchestrator, env, count, " +
      "triggers:[{orchestrator,id,name,kind:'queue'|'time',processId,queueId,enabled,cron," +
      "details}]}.",
    inputSchema: {
      env: envArg,
      orchestrator: orchestratorArg,
      folder: z.string().optional().describe("Explicit folder name override (wins over env)"),
      queueId: z.string().min(1).optional().describe("Only triggers watching this queue"),
      top: z.number().int().min(1).max(200).optional().default(100).describe("Max triggers"),
    },
  },
  async ({ env, orchestrator, folder, queueId, top }) => {
    try {
      const triggers = await backendFor(orchestrator).listTriggers({ env, folder, queueId, top });
      return ok({ orchestrator, env, count: triggers.length, triggers });
    } catch (e) {
      return toolError("list_triggers", e, VERSION);
    }
  },
);

// ---- get_job ---------------------------------------------------------------
server.registerTool(
  "get_job",
  {
    title: "Get a single job's state by id",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Fetch one job by its id — the light poll target after start_job, and the job-first " +
      "diagnostic when you have an id but no orderUid (list_jobs is an unkeyed scan; " +
      "find_order_queue_items is order-correlated). Pass jobIds (up to 25) instead of jobId to " +
      "diagnose several jobs — e.g. every job list_jobs just returned — in ONE call instead of " +
      "looping get_job per id. READ-ONLY. `job` is the normalized job (state = dashboard label, " +
      "rawState = the backend's own status, terminal, process, queueItem, timings, attempt, " +
      "retryOfJobId, fault, link, details — UiPath details carry the parsed output with " +
      "token/callbackContext stripped). With includeLogDigest=true it also returns the log digest " +
      "(failure-focused condensed logs + stalls) and a structured fault (headline error, stable " +
      "signature, class); UiPath adds transactionOutcome — the queue transaction's OWN " +
      "processingExceptionType/Reason and timing from the Transaction-Ended log line, more " +
      "reliable for exception classification than the free-text-derived class, null when the job " +
      "never reached one; Apex adds run {exitCode, outcome, errorClass, timedOut, cancelled}. " +
      "When a job has no readable log, logsUnavailable {reason: running | never-started | " +
      "artifact-policy, detail} says why. If the digest is not enough, call get_job_logs (also " +
      "batchable via jobIds) for the complete logs. A jobIds batch never fails as a whole on one " +
      "id's error — that entry carries error instead, the rest still return; a single jobId " +
      "fails the call normally, like any other error. includeLogDigest is always best-effort: a " +
      "failure fetching it surfaces as logDigestError, never blocks the job data. Returns " +
      "{orchestrator, env, folder, id, found, job?, logDigest?, logsUnavailable?, logDigestError?, " +
      "error?} for a single jobId, or {orchestrator, env, folder, count, jobs:[{id,found,job?," +
      "logDigest?,logsUnavailable?,logDigestError?,error?}]} for jobIds.",
    inputSchema: {
      env: envArg,
      orchestrator: orchestratorArg,
      jobId: z
        .string()
        .min(8)
        .optional()
        .describe("The job's id (from start_job / list_jobs). Provide this OR jobIds, not both"),
      jobIds: z
        .array(z.string().min(8))
        .min(1)
        .max(25)
        .optional()
        .describe("Batch: diagnose several jobs in one call. Provide this OR jobId, not both"),
      folder: z.string().optional().describe("Explicit folder name override (wins over env)"),
      includeLogDigest: z
        .boolean()
        .optional()
        .default(false)
        .describe(
          "Also fetch each job's logs (extra call per job) and return a condensed failure " +
            "digest + structured fault — use get_job_logs for the complete logs",
        ),
    },
  },
  async ({ env, orchestrator, jobId, jobIds, folder, includeLogDigest }, extra) => {
    try {
      const { ids, batch } = resolveJobIds(jobId, jobIds);
      // With includeLogDigest a 25-id batch is 50 orchestrator calls — the backend counts
      // them off through stepProgress as each id finishes.
      const result = await backendFor(orchestrator).getJobs(
        { env, jobIds: ids, batch, folder, includeLogDigest },
        stepProgress(extra),
      );
      return ok(jobEntriesEnvelope(orchestrator, env, result.folder, batch, result.entries));
    } catch (e) {
      return toolError("get_job", e, VERSION);
    }
  },
);

// ---- add_queue_item --------------------------------------------------------
server.registerTool(
  "add_queue_item",
  {
    title: "Post a queue item to a pre-prod queue",
    annotations: {
      readOnlyHint: false,
      destructiveHint: false, // additive — creates an item, never overwrites
      idempotentHint: false, // each call mints a new item
      openWorldHint: true,
    },
    description:
      "POST one item to a queue in the PRE-PROD folder (pre_prod ONLY — the schema rejects prod). " +
      "Every payload passes the test-safety guard first: IsApproved is forced false; a non-empty " +
      "serverURL/queueUrl must match the config's pre-prod values; <TO-FILL> placeholders are " +
      "rejected. To post a build_queue_item result, pass payload.itemData.Name as queue, " +
      "payload.itemData.Reference as reference and payload.itemData.SpecificContent as " +
      "specificContent. priority applies to UiPath only (Apex uses the queue's default). When " +
      "enqueueing many items, pace ~1/s. Returns {orchestrator, env, queueId, queueName, " +
      "reference, itemId, status, forced}.",
    inputSchema: {
      env: writeEnvArg,
      orchestrator: orchestratorArg,
      queue: z
        .string()
        .min(1)
        .describe(
          "Queue in the pre-prod folder: UiPath exact queue Name; Apex queue name or id (see list_queues)",
        ),
      reference: z
        .string()
        .min(1)
        .describe("Queue-item Reference, e.g. '<PORTAL>-DEVTEST-<n>-<orderUid8>'"),
      specificContent: z
        .record(z.unknown())
        .describe("The item's SpecificContent (fixture record or build_queue_item output)"),
      priority: z.enum(["Low", "Normal", "High"]).optional().default("Normal"),
    },
  },
  async ({ env, orchestrator, queue, reference, specificContent, priority }) => {
    try {
      const result = await backendFor(orchestrator).addQueueItem({
        env,
        queue,
        reference,
        priority,
        specificContent,
      });
      // Audit trail: surface every orchestrator write to the client log.
      mcpLog(server, "warning", `posted queue item to pre-prod queue '${queue}'`, {
        orchestrator,
        reference,
        itemId: result.itemId,
        forced: result.forced,
      });
      return ok({ orchestrator, ...result }); // result carries env
    } catch (e) {
      return toolError("add_queue_item", e, VERSION);
    }
  },
);

// ---- delete_queue_item -----------------------------------------------------
server.registerTool(
  "delete_queue_item",
  {
    title: "Delete a New queue item from the pre-prod folder",
    annotations: {
      readOnlyHint: false,
      destructiveHint: true, // deletes an item
      idempotentHint: true, // re-deleting fails safe (status is no longer New)
      openWorldHint: true,
    },
    description:
      "Delete one queue item from the PRE-PROD folder (pre_prod ONLY — the schema rejects prod). " +
      "UiPath is fetch-first: it refuses unless the item's current Status is 'New', so " +
      "InProgress/Successful/Failed history is never destroyed. Use to clear stale test items " +
      "before re-enqueueing after a re-mint. Returns {orchestrator, env, itemId, deleted, " +
      "previousStatus, reference}.",
    inputSchema: {
      env: writeEnvArg,
      orchestrator: orchestratorArg,
      itemId: z
        .string()
        .min(1)
        .describe("Queue item id (UiPath: a positive integer; from list_queue_items)"),
      queueId: z
        .string()
        .min(1)
        .optional()
        .describe("Queue id (required by Apex; ignored by UiPath)"),
    },
  },
  async ({ env, orchestrator, itemId, queueId }) => {
    try {
      const result = await backendFor(orchestrator).deleteQueueItem({ env, itemId, queueId });
      mcpLog(server, "warning", `deleted pre-prod queue item ${itemId}`, {
        orchestrator,
        reference: result.reference,
      });
      return ok({ orchestrator, ...result }); // result carries env
    } catch (e) {
      return toolError("delete_queue_item", e, VERSION);
    }
  },
);

// ---- start_job -------------------------------------------------------------
server.registerTool(
  "start_job",
  {
    title: "Start a job on the pre-prod folder",
    annotations: {
      readOnlyHint: false,
      destructiveHint: false, // starts a run; existing state untouched
      idempotentHint: false, // each call starts new job(s)
      openWorldHint: true,
    },
    description:
      "Start job(s) for a process in the PRE-PROD folder (pre_prod ONLY — the schema rejects " +
      "prod). Get the processId from list_processes. PIN RACE (UiPath): a job resolves its " +
      "package version at START, not creation — if others are publishing, verify the process's " +
      "version via list_processes right before starting (repinning is manual in the orchestrator " +
      "UI; this MCP never repins). Poll the result with get_job. Returns {orchestrator, env, " +
      "jobs:[{id,state,processName,link}]}.",
    inputSchema: {
      env: writeEnvArg,
      orchestrator: orchestratorArg,
      processId: z.string().min(8).describe("Process id (from list_processes)"),
      input: z
        .record(z.unknown())
        .optional()
        .default({})
        .describe("Job input arguments (omit for none)"),
      jobsCount: z.number().int().min(1).max(5).optional().default(1).describe("Jobs to start"),
      matchValue: z
        .string()
        .min(1)
        .optional()
        .describe("Apex only: the match value the job is dispatched under (required by Apex)"),
    },
  },
  async ({ env, orchestrator, processId, input, jobsCount, matchValue }) => {
    try {
      const result = await backendFor(orchestrator).startJob({
        env,
        processId,
        input,
        jobsCount,
        matchValue,
      });
      mcpLog(server, "warning", `started ${result.jobs.length} pre-prod job(s)`, {
        orchestrator,
        processId,
        ids: result.jobs.map((j) => j.id),
      });
      return ok({ orchestrator, ...result }); // result carries env
    } catch (e) {
      return toolError("start_job", e, VERSION);
    }
  },
);

// ---- stop_job ---------------------------------------------------------------
server.registerTool(
  "stop_job",
  {
    title: "Stop or kill a running job on the pre-prod folder",
    annotations: {
      readOnlyHint: false,
      destructiveHint: true, // aborts a run in progress
      idempotentHint: true, // fetch-first: refuses (no write) once already terminal
      openWorldHint: true,
    },
    description:
      "Stop (SoftStop, graceful) or Kill (immediate) a job in the PRE-PROD folder (pre_prod " +
      "ONLY — the schema rejects prod). Takes the job's id (from start_job / list_jobs / " +
      "get_job); the id is resolved inside the pre-prod folder first, so a prod job's id " +
      "resolves to nothing here. Fetch-first: refuses if the job is already in a terminal state. " +
      "Use to abort a hung or misbehaving pre-prod run. Returns {orchestrator, env, jobId, " +
      "strategy, stopped}.",
    inputSchema: {
      env: writeEnvArg,
      orchestrator: orchestratorArg,
      jobId: z.string().min(8).describe("Job id (from start_job / list_jobs / get_job)"),
      strategy: z
        .enum(["SoftStop", "Kill"])
        .optional()
        .default("SoftStop")
        .describe("SoftStop = graceful (default), Kill = immediate"),
    },
  },
  async ({ env, orchestrator, jobId, strategy }) => {
    try {
      const result = await backendFor(orchestrator).stopJob({ env, jobId, strategy });
      mcpLog(server, "warning", `stopped pre-prod job ${jobId} (${strategy})`, { orchestrator });
      return ok({ orchestrator, ...result }); // result carries env
    } catch (e) {
      return toolError("stop_job", e, VERSION);
    }
  },
);
```
3d. `src/mcp/prompts.ts` (renamed args/outputs; `report-faulted-uipath-jobs` keeps its name until Task 15; output paths follow the new `JobCore` — `job.details.output` is UiPath's parsed OutputArguments).

*`diagnose-order`:* in the `userText(...)` call replace the steps 2–4 lines (v1.33.1 lines 43–50: from `` `2. Call find_order_queue_items … `` through `` …previous (older) run's endTime.\n` + ``) and the step-6 lines (55–56) so the surrounding text reads (steps 1, 5, 7 unchanged):
```ts
          `1. Call get_order with orderUid="${orderUid}"${envArg} to read its current normalized state; note its creationDate.\n` +
          `2. Call find_order_queue_items with orderUid="${orderUid}"${envArg}, orchestrator="uipath" and since=<that creationDate> to find the ` +
          `queue item(s) and their job ids (jobIds.uipath). If count is 0, the order never reached UiPath — report and stop. If items ` +
          `exist but jobIds.uipath is empty, it is queued and not yet picked up by a robot — report the item statuses and stop.\n` +
          `3. Call get_job with env${env ? `="${env}"` : ""}, orchestrator="uipath" and jobIds=<every id from jobIds.uipath in step 2> and includeLogDigest=true — ` +
          `ONE call covering the original run and every retry. Read job.state, job.details.output, job.fault, logDigest per job.\n` +
          `4. Derive per-job verdict yourself: job.details.output.out_Result when present, else job.state — Successful -> success, ` +
          `Faulted/Stopped -> failure, anything else -> still running or pending. The newest run is the latest ` +
          `job.createdAt; retry cadence is each run's createdAt minus the previous (older) run's endedAt.\n` +
          // 5. (unchanged)
          `6. Only if the digest is not enough, call get_job_logs with the failing jobId and onlyFailures=true ` +
          `(add fullMessages=true for a full stack trace, includeVideo=true for the recording).\n` +
```
*`triage-stuck-orders`:* replace the step-2 lines (181–183) with:
```ts
          `2. For the stuck orders whose uipath.verdict warrants a closer look, call find_order_queue_items per order ` +
          `(orchestrator="uipath") to get each order's jobIds.uipath, then diagnose them in ONE get_job call with orchestrator="uipath" and jobIds=[...] (up to 25 ids per ` +
          `call) and includeLogDigest=true to see whether each job failed, is still running, or never started.\n` +
```
*`report-faulted-uipath-jobs`:* replace line 246 (`const listArgs = [...]`) with the multi-line form below, and edit lines 253 and 257 as shown:
```ts
      const listArgs = [
        `env="${e}"`,
        'orchestrator="uipath"',
        since ? `since="${since}"` : "",
        top ? `top=${top}` : "",
      ]
```
```ts
          `2. For each faulted job, call build_faulted_job_issue with env="${e}", orchestrator="uipath" and that job's id as jobId${repo ? `, repo="${repo}"` : ""} to get the issue payload (it does NOT post to GitHub).\n` +
```
```ts
          `4. Report a per-job summary: faulted job id -> created issue (#/url) or commented on existing issue (#/url).\n` +
```
Then `bunx biome check --write src/mcp/prompts.ts`.

3e. UiPath dead code (everything the backend now owns):
 - `src/uipath/queue.ts`: replace the whole file with the following (Task 3's `uipathPullQueueItem` only imports `parseQueueItemUrl`/`ParsedQueueUrl`; `pullQueueItem`, `listQueue`, `findOrderQueueItems`, their arg/result types, `resolvePull`, `resolveQueueDefId`, `str` and the old `QueueKind` are deleted; their behaviour is pinned by `uipathPullQueueItem`/`uipathListQueueItems`/`uipathFindOrderQueueItems` tests in `backend.test.ts` from Task 3):
```ts
// UiPath queue-item URL parsing — the one UiPath-specific piece of pull_queue_item that is
// not an OData call (uipathPullQueueItem in backend.ts does the fetching).

const URL_TXN_RE = /\/transactions\/(\d+)\/details/;
const URL_QUEUE_RE = /\/queues\/(\d+)\/transactions/;

export interface ParsedQueueUrl {
  txnId: number;
  folderId: string;
  queueId: number; // 0 if the URL had no /queues/<id>/ segment
}

// Pull (txnId, folderId, queueId) out of an Orchestrator queue-item URL. The txn id
// lives only in the matrix-parameter `(sidepanel:.../transactions/<id>/details)`
// path segment, and the folder id only in the `fid` query param. Throws if either
// required part is absent.
export function parseQueueItemUrl(url: string): ParsedQueueUrl {
  const u = new URL(url);
  const folderId = u.searchParams.get("fid");
  if (!folderId) throw new Error(`queue-item URL is missing the 'fid' query param: ${url}`);
  const haystack = u.pathname + u.search;
  const txnMatch = URL_TXN_RE.exec(haystack);
  if (!txnMatch)
    throw new Error(`queue-item URL is missing a /transactions/<id>/details segment: ${url}`);
  const queueMatch = URL_QUEUE_RE.exec(haystack);
  return {
    txnId: Number(txnMatch[1]),
    folderId,
    queueId: queueMatch ? Number(queueMatch[1]) : 0,
  };
}
```
 - Delete `src/uipath/queue.test.ts` (its five `findOrderQueueItems` cases were re-pinned in Task 3).
 - `src/uipath/faults.ts`: delete the old `JobFault` interface and the `extractFault` function (with their comment blocks) — the only caller was the deleted `shapeJobDetail`; `uipathFault` (map.ts) replaced it. KEEP `EXCEPTION_TYPE`, `topError`, `extractTransactionOutcome`, `TransactionOutcome`, `formatFaultedJobIssue`, `buildFaultedJobIssue`. In `src/uipath/faults.test.ts` delete the `describe("extractFault", …)` block (and any import it alone needed). Then `bunx tsc --noEmit` and delete whatever imports it reports as unused (TS6133) in those two files.
- [ ] **Step 4: Run the tests**
Run: `bun test src/orchestrator/job-batch.test.ts src/server.test.ts src/uipath` — Expected: PASS.
Sweep for stragglers: `rg -n "extractFault|jobDeepLinkBase|shapeJobDetail|JobKeyResult" src` — Expected: no match in `src/server.ts`/`src/mcp`/`src/orchestrator`; the only `jobDeepLinkBase` hits are its definition and `jobDeepLink` (which composes it) in `src/uipath/uipath.ts` — keep it exported; nothing else imports it any more. `rg -n "jobKeys|\bjobKey\b|releaseKey|queueDefId|txnId" src --glob '!*.test.ts'` — Expected: no match in `src/server.ts`, `src/mcp/prompts.ts`, `src/uipath/queue.ts`; remaining hits are UiPath-internal OData names (`src/uipath/uipath.ts`, `src/uipath/actions.ts`, `src/uipath/faults.ts`) and `src/copilot/sweep.ts` (Task 14).
- [ ] **Step 5: Full gate** — `bun run typecheck && bun test && bunx biome check src` → all green
- [ ] **Step 6: Commit**
```bash
git add -A src
git commit -m "feat(orchestrator)!: route every orchestrator tool through backendFor(orchestrator)" -m "Breaking tool shapes (v2.0.0): every job/queue-level tool takes a required orchestrator and replies { orchestrator, env, ... } with normalized cores; jobKey(s)->jobId(s), releaseKey->processId, inputArguments->input, queueName/queueDefId->queue, queueDefinitionId->queueId, txnId->itemId (string), videoUrl->video, jobDeepLinkBase->per-job link. server.ts stays declarative: the job/queue logic lives in uipathBackend. Removes the dead UiPath-direct paths and re-syncs the header tool list (it omitted eight tools)."
```

## Phase 2: Apex config, client and doctor (Tasks 6–8)

### Task 6: Apex config section, `resolveApex()` and `ApexNotConfiguredError`

**Files:**
- Create: `src/apex/errors.ts` (`ApexNotConfiguredError`; Task 7 adds `ApexApiError` + `apexErrorDetail` to this same file)
- Modify: `src/config/config.ts` (header comment lines 3-6; import after line 22; new `ApexSchema` after the `UipathSchema` block that ends at line 122; `apex` key in `ConfigSchema` lines 132-136; `ApexConfig` + `resolveApex()` after `getUipath` at line 320)
- Modify: `src/config/config.test.ts` (imports lines 5-11; new `describe("resolveApex")` block inserted before `describe("live reload"` at line 219)
- Modify: `copilot-mcp.config.example.json` (`_note` line 2; new `apex` block between the `uipath` block, ends line 24, and `feedback`, line 25)
- Test: `src/config/config.test.ts`

**Interfaces:**
- Consumes: `ExpectedError` from `src/shared/expected-error.ts` (**created by Task 1**: the class moves out of `src/mcp/feedback.ts` so `config.ts` can throw an `ExpectedError` subclass without an import cycle `config.ts → apex/errors.ts → mcp/feedback.ts → config.ts`; do NOT import it from `mcp/feedback.js` anywhere); existing `loadConfig()` / `Env`.
- Produces (exact):
  - `src/apex/errors.ts`: `class ApexNotConfiguredError extends ExpectedError { readonly missing: string[]; readonly partial: boolean; constructor(missing: { field: string; envVar: string }[], configured: number) }` (`missing` = field names nobody supplied; `partial` = at least one of the three fields IS set).
  - `src/config/config.ts`: `interface ApexConfig` (contract shape: `apiUrl` (no trailing slash), `username`, `password`, `folderByEnv: Record<Env,string>`, `serverUrlByEnv: Partial<Record<Env,string>>`, `queueUrl` (`""` when unset), `sources: { apiUrl|username|password: "config" | "env" }`), `resolveApex(): ApexConfig`, `DEFAULT_APEX_FOLDERS: Record<Env, string>` (`{ prod: "Authorization", pre_prod: "Authorization Test" }`).
  - Config key `apex` (optional; `.passthrough()`): `apiUrl?` (URL), `username?`, `password?`, `folderByEnv?{prod?,pre_prod?}`, `serverUrlByEnv?{prod?,pre_prod?}`, `queueUrl?`.
- Semantics: per field the file wins, then `APEX_API_URL` / `APEX_USER_NAME` / `APEX_PASSWORD` (empty string = unset; never `APEX_URL`, never `APEX_API_URL_<n>`); env is read on every call, never cached. A config with no `apex` section still loads; only `resolveApex()` throws.

- [ ] **Step 1: Write the failing tests**

Apply this diff to `src/config/config.test.ts` (reuses the file's existing `FIXTURE` and temp-config pattern):

```diff
@@ -2,10 +2,14 @@
 import { mkdtempSync, rmSync, utimesSync, writeFileSync } from "node:fs";
 import { tmpdir } from "node:os";
 import { join } from "node:path";
+import { ApexNotConfiguredError } from "../apex/errors.js";
+import { ExpectedError } from "../shared/expected-error.js";
 import {
   getUipath,
+  loadConfig,
   onConfigReload,
   resetConfigCache,
+  resolveApex,
   resolveAuth,
   resolveSupport,
 } from "./config.js";
@@ -216,6 +220,154 @@
   });
 });
 
+describe("resolveApex", () => {
+  const ENV_VARS = [
+    "APEX_API_URL",
+    "APEX_USER_NAME",
+    "APEX_PASSWORD",
+    "APEX_URL",
+    "APEX_API_URL_1",
+  ];
+  let apexDir: string;
+  let apexConfigPath: string;
+  let prevConfig: string | undefined;
+  let prevEnv: Record<string, string | undefined>;
+
+  beforeEach(() => {
+    apexDir = mkdtempSync(join(tmpdir(), "copilot-mcp-apex-cfg-"));
+    apexConfigPath = join(apexDir, "config.json");
+    prevConfig = process.env["COPILOT_MCP_CONFIG"];
+    process.env["COPILOT_MCP_CONFIG"] = apexConfigPath;
+    prevEnv = Object.fromEntries(ENV_VARS.map((k) => [k, process.env[k]]));
+    for (const k of ENV_VARS) delete process.env[k];
+  });
+
+  afterEach(() => {
+    if (prevConfig === undefined) delete process.env["COPILOT_MCP_CONFIG"];
+    else process.env["COPILOT_MCP_CONFIG"] = prevConfig;
+    for (const k of ENV_VARS) {
+      const v = prevEnv[k];
+      if (v === undefined) delete process.env[k];
+      else process.env[k] = v;
+    }
+    resetConfigCache();
+    rmSync(apexDir, { recursive: true, force: true });
+  });
+
+  // `apex: undefined` writes a config with no apex section at all.
+  const withApex = (apex: Record<string, unknown> | undefined) => {
+    writeFileSync(apexConfigPath, JSON.stringify({ ...FIXTURE, ...(apex ? { apex } : {}) }));
+    resetConfigCache();
+  };
+
+  test("a config without an apex section still loads", () => {
+    withApex(undefined);
+    expect(loadConfig().apex).toBeUndefined();
+    expect(getUipath().bearer).toBe("test-bearer");
+  });
+
+  test("an invalid apex.apiUrl fails config validation naming the field", () => {
+    withApex({ apiUrl: "not a url" });
+    expect(() => loadConfig()).toThrow(/apex\.apiUrl must be a valid URL/);
+  });
+
+  test("the file wins per field and the env fills only what the file leaves out", () => {
+    withApex({ apiUrl: "https://file.example.com", username: "file-user" });
+    process.env["APEX_API_URL"] = "https://env.example.com";
+    process.env["APEX_USER_NAME"] = "env-user";
+    process.env["APEX_PASSWORD"] = "env-pw";
+    const apex = resolveApex();
+    expect(apex.apiUrl).toBe("https://file.example.com");
+    expect(apex.username).toBe("file-user");
+    expect(apex.password).toBe("env-pw");
+    expect(apex.sources).toEqual({ apiUrl: "config", username: "config", password: "env" });
+  });
+
+  test("env alone is enough, and a trailing slash on the url is stripped", () => {
+    withApex(undefined);
+    process.env["APEX_API_URL"] = "https://env.example.com//";
+    process.env["APEX_USER_NAME"] = "env-user";
+    process.env["APEX_PASSWORD"] = "env-pw";
+    const apex = resolveApex();
+    expect(apex.apiUrl).toBe("https://env.example.com");
+    expect(apex.sources).toEqual({ apiUrl: "env", username: "env", password: "env" });
+  });
+
+  test("an unconfigured field is an ApexNotConfiguredError naming exactly the missing ones", () => {
+    withApex({ apiUrl: "https://file.example.com" });
+    process.env["APEX_PASSWORD"] = "env-pw";
+    let err: unknown;
+    try {
+      resolveApex();
+    } catch (e) {
+      err = e;
+    }
+    expect(err).toBeInstanceOf(ApexNotConfiguredError);
+    expect(err).toBeInstanceOf(ExpectedError);
+    const message = (err as Error).message;
+    expect(message).toContain("missing username");
+    expect(message).not.toContain("apiUrl");
+    expect(message).not.toContain("password");
+    expect(message).toContain("apex.username");
+    expect(message).toContain("APEX_USER_NAME");
+    expect((err as ApexNotConfiguredError).missing).toEqual(["username"]);
+    expect((err as ApexNotConfiguredError).partial).toBe(true);
+  });
+
+  test("with nothing configured the error lists all three fields and both ways to supply them", () => {
+    withApex(undefined);
+    expect(() => resolveApex()).toThrow(
+      /missing apiUrl, username, password.*apex\.apiUrl.*APEX_API_URL, APEX_USER_NAME, APEX_PASSWORD/,
+    );
+    try {
+      resolveApex();
+    } catch (e) {
+      expect((e as ApexNotConfiguredError).partial).toBe(false);
+    }
+  });
+
+  test("an empty env var counts as unset, and the numbered/legacy names are never read", () => {
+    withApex({ username: "u", password: "p" });
+    process.env["APEX_API_URL"] = "";
+    process.env["APEX_API_URL_1"] = "https://numbered.example.com";
+    process.env["APEX_URL"] = "https://legacy.example.com";
+    expect(() => resolveApex()).toThrow(/missing apiUrl/);
+  });
+
+  test("reads the environment on every call, not once at load", () => {
+    withApex({ username: "u", password: "p" });
+    process.env["APEX_API_URL"] = "https://one.example.com";
+    expect(resolveApex().apiUrl).toBe("https://one.example.com");
+    process.env["APEX_API_URL"] = "https://two.example.com";
+    expect(resolveApex().apiUrl).toBe("https://two.example.com");
+  });
+
+  test("folders default per env and a partial override keeps the other default", () => {
+    const base = { apiUrl: "https://a.example.com", username: "u", password: "p" };
+    withApex(base);
+    expect(resolveApex().folderByEnv).toEqual({
+      prod: "Authorization",
+      pre_prod: "Authorization Test",
+    });
+    withApex({ ...base, folderByEnv: { pre_prod: "Sandbox" } });
+    expect(resolveApex().folderByEnv).toEqual({ prod: "Authorization", pre_prod: "Sandbox" });
+  });
+
+  test("write-guard settings pass through; unset ones stay absent / empty", () => {
+    const base = { apiUrl: "https://a.example.com", username: "u", password: "p" };
+    withApex(base);
+    expect(resolveApex().serverUrlByEnv).toEqual({});
+    expect(resolveApex().queueUrl).toBe("");
+    withApex({
+      ...base,
+      serverUrlByEnv: { pre_prod: "https://be.pre.example.com" },
+      queueUrl: "https://sqs.example.com/q",
+    });
+    expect(resolveApex().serverUrlByEnv).toEqual({ pre_prod: "https://be.pre.example.com" });
+    expect(resolveApex().queueUrl).toBe("https://sqs.example.com/q");
+  });
+});
+
 describe("live reload", () => {
   let liveDir: string;
   let liveConfigPath: string;
```

- [ ] **Step 2: Run it to verify it fails**

Run: `bun test src/config/config.test.ts` — Expected: FAIL (`Cannot find module '../apex/errors.js'` / `resolveApex` is not exported).

- [ ] **Step 3: Implement**

3a. Create `src/apex/errors.ts`:

```ts
// Apex domain errors. They extend ExpectedError: an unconfigured server or a structured
// HTTP answer from Apex is user/upstream state, not a bug in this server, so classify()
// (mcp/feedback.ts) treats them as `expected` by identity.

import { ExpectedError } from "../shared/expected-error.js";

export class ApexNotConfiguredError extends ExpectedError {
  // Names of the fields neither the file nor the environment supplied.
  readonly missing: string[];
  // true = some, but not all, of the fields are set (a half-finished setup, as opposed
  // to Apex simply not being used) — `doctor` flags only that case.
  readonly partial: boolean;

  constructor(missing: { field: string; envVar: string }[], configured: number) {
    super(
      `Apex is not configured — missing ${missing.map((m) => m.field).join(", ")}. ` +
        `Set ${missing.map((m) => `apex.${m.field}`).join(", ")} in the config file, or the ` +
        `environment variable${missing.length === 1 ? "" : "s"} ${missing.map((m) => m.envVar).join(", ")} ` +
        "(the file wins per field; env vars only fill what the file leaves out).",
    );
    this.name = "ApexNotConfiguredError";
    this.missing = missing.map((m) => m.field);
    this.partial = configured > 0;
  }
}

```

3b. Apply to `src/config/config.ts` (line numbers are the current ones; hunks carry enough context to locate):

```diff
@@ -1,9 +1,9 @@
 // Single-file, validated configuration for the copilot MCP server.
 //
-// One file holds BOTH the Copilot BE credentials and the UiPath Orchestrator
-// args: { copilot: {...}, uipath: {...} }. The path is taken from the
-// COPILOT_MCP_CONFIG env var, else `copilot-mcp.config.json` in the project root
-// (cwd), else the older `config.local.json` name for existing setups.
+// One file holds the Copilot BE credentials, the UiPath Orchestrator args and, optionally,
+// the Apex orchestrator account: { copilot: {...}, uipath: {...}, apex?: {...} }. The path
+// is taken from the COPILOT_MCP_CONFIG env var, else `copilot-mcp.config.json` in the
+// project root (cwd), else the older `config.local.json` name for existing setups.
 //
 // The config is live: loadConfig() cheaply stats the backing file(s) on every call
 // and re-reads/re-validates only when they changed, so editing the file while the
@@ -20,6 +20,7 @@
 import process from "node:process";
 import { fileURLToPath } from "node:url";
 import { z } from "zod";
+import { ApexNotConfiguredError } from "../apex/errors.js";
 
 // ---- Schemas -------------------------------------------------------------
 
@@ -121,6 +122,27 @@
     message: "uipath requires bearer, oauth, or both",
   });
 
+// Apex orchestrator. Every field is optional in the file: apiUrl/username/password fall
+// back to APEX_API_URL / APEX_USER_NAME / APEX_PASSWORD (see resolveApex), so a missing
+// section is valid and only Apex tools fail (with ApexNotConfiguredError).
+const ApexEnvMapSchema = z
+  .object({ prod: z.string().min(1).optional(), pre_prod: z.string().min(1).optional() })
+  .partial()
+  .optional();
+
+const ApexSchema = z
+  .object({
+    apiUrl: z.string().url("apex.apiUrl must be a valid URL").optional(),
+    username: z.string().min(1).optional(),
+    password: z.string().min(1).optional(),
+    // Folder NAME per env; resolved to the X-Folder-Id uuid at runtime. Defaults below.
+    folderByEnv: ApexEnvMapSchema,
+    // Write guard: specific_content.serverURL / queueUrl must equal these (fails closed unset).
+    serverUrlByEnv: ApexEnvMapSchema,
+    queueUrl: z.string().min(1).optional(),
+  })
+  .passthrough();
+
 // On-failure GitHub-issue suggestion (see feedback.ts). Optional; on by default.
 const FeedbackSchema = z
   .object({
@@ -132,6 +154,7 @@
 const ConfigSchema = z.object({
   copilot: CopilotSchema,
   uipath: UipathSchema,
+  apex: ApexSchema.optional(),
   feedback: FeedbackSchema.optional(),
 });
 
@@ -319,6 +342,71 @@
 
 export const getUipath = (): UipathConfig => loadConfig().uipath;
 
+export interface ApexConfig {
+  apiUrl: string; // no trailing slash
+  username: string;
+  password: string;
+  folderByEnv: Record<Env, string>;
+  serverUrlByEnv: Partial<Record<Env, string>>;
+  queueUrl: string; // "" when unset
+  sources: { apiUrl: ApexSource; username: ApexSource; password: ApexSource };
+}
+type ApexSource = "config" | "env";
+
+const APEX_ENV_VARS = {
+  apiUrl: "APEX_API_URL",
+  username: "APEX_USER_NAME",
+  password: "APEX_PASSWORD",
+} as const;
+export const DEFAULT_APEX_FOLDERS: Record<Env, string> = {
+  prod: "Authorization",
+  pre_prod: "Authorization Test",
+};
+
+// File value first, then the env var. Read per call (never cached) so a changed
+// environment — or a test — takes effect immediately. An empty env var counts as unset.
+function apexField(field: keyof typeof APEX_ENV_VARS, fromFile: string | undefined) {
+  if (fromFile) return { value: fromFile, source: "config" as const };
+  const fromEnv = process.env[APEX_ENV_VARS[field]];
+  return fromEnv ? { value: fromEnv, source: "env" as const } : undefined;
+}
+
+// The resolved Apex settings, or ApexNotConfiguredError naming every missing field.
+// Precedence is per field: the file wins; the environment only fills what it leaves out.
+export function resolveApex(): ApexConfig {
+  const apex = loadConfig().apex;
+  const found = {
+    apiUrl: apexField("apiUrl", apex?.apiUrl),
+    username: apexField("username", apex?.username),
+    password: apexField("password", apex?.password),
+  };
+  const { apiUrl, username, password } = found;
+  if (!(apiUrl && username && password)) {
+    const fields = Object.keys(APEX_ENV_VARS) as (keyof typeof APEX_ENV_VARS)[];
+    const missing = fields
+      .filter((field) => !found[field])
+      .map((field) => ({ field, envVar: APEX_ENV_VARS[field] }));
+    throw new ApexNotConfiguredError(missing, fields.length - missing.length);
+  }
+  const folders = apex?.folderByEnv;
+  const serverUrls = apex?.serverUrlByEnv;
+  return {
+    apiUrl: apiUrl.value.replace(/\/+$/, ""),
+    username: username.value,
+    password: password.value,
+    folderByEnv: {
+      prod: folders?.prod ?? DEFAULT_APEX_FOLDERS.prod,
+      pre_prod: folders?.pre_prod ?? DEFAULT_APEX_FOLDERS.pre_prod,
+    },
+    serverUrlByEnv: {
+      ...(serverUrls?.prod ? { prod: serverUrls.prod } : {}),
+      ...(serverUrls?.pre_prod ? { pre_prod: serverUrls.pre_prod } : {}),
+    },
+    queueUrl: apex?.queueUrl ?? "",
+    sources: { apiUrl: apiUrl.source, username: username.source, password: password.source },
+  };
+}
+
 // Names of the credential profiles defined in the config (copilot.profiles). Used to
 // drive prompt autocompletion; never throws (returns [] when the config is absent or
 // has no profiles) so it is safe to call from tool/prompt wiring and completions.
```

3c. `copilot-mcp.config.example.json`: append the sentence below to the end of the `_note` string on line 2 (before the closing quote), and insert the `apex` block between `uipath` and `feedback`. Apex fields may be left out of the file and supplied by environment variables, so the note says to delete placeholder fields that the environment should supply:

```diff
@@ -1,5 +1,5 @@
 {
-  "_note": "Copy to copilot-mcp.config.json (gitignored) and point COPILOT_MCP_CONFIG at it. copilot.profiles is the normal path (every Copilot tool takes a required profile arg). Prod now uses support-account auth for every shipped profile below: each prod entry is just { clinicUid } and the credentials come from copilot.support.prod; get the clinicUid from the list_clinics tool. Pre_prod stays on the existing direct per-account login shape. clinicUid wins if both are present. copilot.support is required for prod, and direct prod credentials are no longer shown in this example.",
+  "_note": "Copy to copilot-mcp.config.json (gitignored) and point COPILOT_MCP_CONFIG at it. copilot.profiles is the normal path (every Copilot tool takes a required profile arg). Prod now uses support-account auth for every shipped profile below: each prod entry is just { clinicUid } and the credentials come from copilot.support.prod; get the clinicUid from the list_clinics tool. Pre_prod stays on the existing direct per-account login shape. clinicUid wins if both are present. copilot.support is required for prod, and direct prod credentials are no longer shown in this example. The optional apex block configures the Apex orchestrator: apiUrl/username/password may instead come from the APEX_API_URL / APEX_USER_NAME / APEX_PASSWORD environment variables (the file wins per field, so delete any field you want the environment to supply); omit the whole block to leave Apex unused.",
   "copilot": {
     "sessionCache": true,
     "support": {
@@ -22,5 +22,13 @@
     "addQueueItemPath": "/odata/Queues/UiPathODataSvc.AddQueueItem",
     "serverUrlByEnv": { "prod": "https://prod-be-batch.ehrcopilotbe.com", "pre_prod": "https://pre-prod-be-batch.ehrcopilotbe.com" }
   },
+  "apex": {
+    "apiUrl": "https://orchestrator.apexmedicalai.com",
+    "username": "<your own Apex login>",
+    "password": "<your Apex password>",
+    "folderByEnv": { "prod": "Authorization", "pre_prod": "Authorization Test" },
+    "serverUrlByEnv": { "pre_prod": "https://pre-prod-be-batch.ehrcopilotbe.com" },
+    "queueUrl": "<SQS queue url used in the pre-prod SpecificContent>"
+  },
   "feedback": { "enabled": true, "repositoryUrl": "https://github.com/belal-elsabbagh-apex/copilot-mcp" }
 }
```

- [ ] **Step 4: Run tests** — `bun test src/config/config.test.ts` → PASS
- [ ] **Step 5: Full gate** — `bun run typecheck && bun test && bunx biome check src` → all green
- [ ] **Step 6: Commit**
```bash
git add src/apex/errors.ts src/config/config.ts src/config/config.test.ts copilot-mcp.config.example.json
git commit -m "feat(apex): optional apex config section with file-then-env resolution" -m "Apex credentials resolve per field from the config file first, then APEX_API_URL / APEX_USER_NAME / APEX_PASSWORD, read lazily on every call. Missing fields raise ApexNotConfiguredError naming exactly what to set and both ways to set it, so UiPath and Copilot tools are unaffected when Apex is unused."
```

---

### Task 7: Apex HTTP chokepoint `src/apex/client.ts`

**Files:**
- Create: `src/apex/client.ts`
- Create: `src/apex/test-support.ts` (shared fetch/config stub; **Tasks 9-17 reuse it**)
- Modify: `src/apex/errors.ts` (replace with the full final file below: adds `ApexApiError`, `apexErrorDetail`)
- Test: `src/apex/client.test.ts`

**Interfaces:**
- Consumes: `resolveApex`, `ApexConfig`, `Env`, `onConfigReload` (`src/config/config.ts`, Task 6); `ApexNotConfiguredError` (Task 6); `ExpectedError` (`src/shared/expected-error.ts`, Task 1); `sleep`, `rateLimitDelayMs` (`src/shared/http.ts`, Task 1); `prop`, `safeJsonParse` (`src/shared/util.ts`).
- Produces (exact; the contract's signatures, plus `apexLogin` and `apexErrorDetail`):
  - `src/apex/errors.ts`: `class ApexApiError extends ExpectedError { readonly status: number; readonly method: string; readonly path: string; constructor(method, path, status, detail: string) }` (message `Apex <METHOD> <path> -> <status>: <detail>`; `path` is relative to `/api/v1`); `apexErrorDetail(body: unknown, status: number): string` (string `detail` or pydantic `[{loc,msg}]` list, capped at 300 chars, 403 adds a permission hint).
  - `src/apex/client.ts`:
    - `apexRequest(env: Env, method: "GET"|"POST"|"PATCH"|"DELETE", path: string, opts?: { query?: Record<string,string>; body?: unknown; folder?: string | undefined }): Promise<unknown>` (parsed JSON, `null` for an empty body)
    - `apexPages<T>(env: Env, path: string, query: Record<string,string>, max: number, folder?: string): Promise<T[]>`
    - `apexDownload(env: Env, downloadUrl: string, opts?: { range?: string }): Promise<{ status: number; bytes: Uint8Array; contentRange: string | null }>`
    - `resolveFolderId(env: Env, folder?: string): Promise<{ id: string; name: string }>`
    - `apexDashboardLink(path: string): string`
    - `apexLogin(): Promise<void>` (log in or reuse the live session; used by `doctor`)
    - `_resetApexClient(): void` (test-only; also runs on config reload)
  - `src/apex/test-support.ts` (for every later Apex test): `APEX_URL`, `FOLDER_IDS` (keys `"Authorization"`, `"Authorization Test"`, `"Eligibility Check"`), `json(body, status = 200, headers = {})`, `fakeJwt(expSeconds?)`, `interface ApexCall { method; path /* after /api/v1, includes ?query */; headers /* lower-case keys */; body /* parsed JSON | undefined */ }`, `useApexStub(opts?: { apex?: Record<string,unknown> | null }): { calls: ApexCall[]; route(method, pathPrefix, handler: (c: ApexCall) => Response | Promise<Response>): void; callsTo(method, pathPrefix): ApexCall[] }`. `useApexStub` registers its own `beforeAll/afterAll/beforeEach/afterEach` (call it inside a `describe`): temp `COPILOT_MCP_CONFIG` with a configured `apex` section (`apex: null` = none, any object replaces the section), cleared `APEX_*` env vars, `resetConfigCache()` + `_resetApexClient()` + fetch restore. It auto-serves `POST /auth/login` (Set-Cookie JWT) and `GET /folders` (the three names above); the last registered matching route wins; a handler that throws is a network error; unmatched requests answer 404.
- Behavior (each pinned by a test below): login once via `Set-Cookie: access_token` (single-flighted; TTL = JWT `exp` − 60 s); 401 → one re-login + one retry; `X-Folder-Id` on every request (the folder listing itself is the only call without it); write gate (non-GET and `env !== "pre_prod"` → throw, or any non-GET with a `folder` override → throw) runs **before any fetch, including login**; GET retried 3 attempts on network errors and up to 3× on 429 (`Retry-After`), writes never retried; `apexDownload` only accepts `/api/v1/…` paths so the Bearer token can't be sent to another host. Retry waits reuse `sleep` (real timers: `Retry-After: "0"` in tests, network backoff is 50 ms × attempt, ~150 ms worst case).
- Note on size: `client.ts` is ~310 lines, over the ≲200 guideline, deliberately: spec §7/§10 require ONE module to own every Apex `fetch` (login included). The pure message formatting already lives in `errors.ts`.

- [ ] **Step 1: Write the failing tests**

`src/apex/test-support.ts`:

```ts
// Shared fixtures for tests that run the REAL Apex client against a stubbed fetch.
// Not a test file itself; imported by apex/*.test.ts and copilot/doctor.test.ts.
// useApexStub() installs hooks, so call it inside a describe (or at file top level).

import { afterAll, afterEach, beforeAll, beforeEach } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { resetConfigCache } from "../config/config.js";
import { _resetApexClient } from "./client.js";

export const APEX_URL = "https://apex.example.com";
const API_PREFIX = `${APEX_URL}/api/v1`;

export const FOLDER_IDS: Record<string, string> = {
  Authorization: "11111111-1111-1111-1111-111111111111",
  "Authorization Test": "22222222-2222-2222-2222-222222222222",
  "Eligibility Check": "33333333-3333-3333-3333-333333333333",
};

export interface ApexCall {
  method: string;
  path: string; // after /api/v1, query string included
  headers: Record<string, string>; // lower-cased keys
  body: unknown; // parsed JSON, undefined when none
}
type Handler = (call: ApexCall) => Response | Promise<Response>;

export const json = (body: unknown, status = 200, headers: Record<string, string> = {}): Response =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json", ...headers },
  });

const b64url = (v: unknown): string => Buffer.from(JSON.stringify(v)).toString("base64url");

// A structurally real (unsigned) JWT: the client only decodes the payload's `exp`.
export const fakeJwt = (exp = Math.floor(Date.now() / 1000) + 86_400): string =>
  `${b64url({ alg: "none" })}.${b64url({ sub: "u", exp })}.sig`;

const loginResponse = (jwt: string): Response =>
  json({ user: { username: "tester" } }, 200, {
    "Set-Cookie": `access_token=${jwt}; HttpOnly; Path=/; SameSite=Lax; Secure`,
  });

const foldersResponse = (): Response =>
  json({
    items: Object.entries(FOLDER_IDS).map(([name, id]) => ({ id, name })),
    total: 3,
    page: 1,
    page_size: 200,
    total_pages: 1,
  });

const baseConfig = (apex: Record<string, unknown> | null): string => {
  const creds = (n: string) => ({
    be: `https://be.${n}.example.com`,
    email: `${n}@x.com`,
    password: "pw",
  });
  return JSON.stringify({
    copilot: { sessionCache: false, prod: creds("prod"), pre_prod: creds("preprod") },
    uipath: { orchestratorUrl: "https://cloud.uipath.com/o/t/orchestrator_", bearer: "b" },
    ...(apex ? { apex } : {}),
  });
};

const APEX_ENV = ["APEX_API_URL", "APEX_USER_NAME", "APEX_PASSWORD"];

// `apex` = the whole config section (null: no apex section). Default: a fully configured one.
// Routes match by method + path prefix (after /api/v1); the LAST registered match wins, so a
// test overrides the default login/folders routes by registering its own. A handler that throws
// or rejects behaves like a network error. Unmatched requests answer 404.
export function useApexStub(opts: { apex?: Record<string, unknown> | null } = {}) {
  const apexSection =
    opts.apex === undefined
      ? {
          apiUrl: APEX_URL,
          username: "tester",
          password: "pw",
          serverUrlByEnv: { pre_prod: "https://be.pre.example" },
          queueUrl: "https://sqs.example/q",
        }
      : opts.apex;
  const realFetch = globalThis.fetch;
  const calls: ApexCall[] = [];
  let routes: { method: string; prefix: string; handler: Handler }[] = [];
  let dir: string;
  let prevConfig: string | undefined;
  const prevEnv: Record<string, string | undefined> = {};

  const route = (method: string, prefix: string, handler: Handler): void => {
    routes.push({ method, prefix, handler });
  };
  const callsTo = (method: string, prefix: string): ApexCall[] =>
    calls.filter((c) => c.method === method && c.path.startsWith(prefix));

  beforeAll(() => {
    dir = mkdtempSync(join(tmpdir(), "copilot-mcp-apex-"));
    const path = join(dir, "config.json");
    writeFileSync(path, baseConfig(apexSection));
    prevConfig = process.env["COPILOT_MCP_CONFIG"];
    process.env["COPILOT_MCP_CONFIG"] = path;
    for (const k of APEX_ENV) {
      prevEnv[k] = process.env[k];
      delete process.env[k];
    }
    resetConfigCache();
  });

  afterAll(() => {
    if (prevConfig === undefined) delete process.env["COPILOT_MCP_CONFIG"];
    else process.env["COPILOT_MCP_CONFIG"] = prevConfig;
    for (const k of APEX_ENV) {
      const v = prevEnv[k];
      if (v === undefined) delete process.env[k];
      else process.env[k] = v;
    }
    resetConfigCache();
    rmSync(dir, { recursive: true, force: true });
  });

  beforeEach(() => {
    calls.length = 0;
    routes = [];
    route("POST", "/auth/login", () => loginResponse(fakeJwt()));
    route("GET", "/folders", () => foldersResponse());
    _resetApexClient();
    globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
      const url = String(input);
      const headers: Record<string, string> = {};
      for (const [k, v] of new Headers(init?.headers).entries()) headers[k] = v;
      const call: ApexCall = {
        method: init?.method ?? "GET",
        path: url.startsWith(API_PREFIX) ? url.slice(API_PREFIX.length) : url,
        headers,
        body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
      };
      calls.push(call);
      const hit = [...routes]
        .reverse()
        .find((r) => r.method === call.method && call.path.startsWith(r.prefix));
      return hit
        ? hit.handler(call)
        : json({ detail: `no stub for ${call.method} ${call.path}` }, 404);
    }) as typeof fetch;
  });

  afterEach(() => {
    globalThis.fetch = realFetch;
    _resetApexClient();
  });

  return { calls, route, callsTo };
}
```

`src/apex/client.test.ts`:

```ts
import { describe, expect, test } from "bun:test";
import { ExpectedError } from "../shared/expected-error.js";
import {
  _resetApexClient,
  apexDashboardLink,
  apexDownload,
  apexPages,
  apexRequest,
  resolveFolderId,
} from "./client.js";
import { ApexApiError } from "./errors.js";
import { type ApexCall, FOLDER_IDS, fakeJwt, json, useApexStub } from "./test-support.js";

const bearerOf = (c: ApexCall | undefined): string | undefined => c?.headers["authorization"];
const params = (c: ApexCall | undefined): URLSearchParams =>
  new URL(`https://x${c?.path ?? ""}`).searchParams;
const caught = async (p: Promise<unknown>): Promise<unknown> =>
  p.then(
    () => undefined,
    (e) => e,
  );
const loginReturning = (...jwts: string[]) => {
  let n = 0;
  return () =>
    json({}, 200, {
      "Set-Cookie": `access_token=${jwts[Math.min(n++, jwts.length - 1)]}; HttpOnly`,
    });
};

describe("login and token", () => {
  const apex = useApexStub();

  test("logs in once with the configured account and sends the cookie JWT as Bearer", async () => {
    const jwt = fakeJwt();
    apex.route("POST", "/auth/login", loginReturning(jwt));
    apex.route("GET", "/jobs/", () => json({ id: "j1" }));
    await apexRequest("prod", "GET", "/jobs/j1");
    await apexRequest("prod", "GET", "/jobs/j2");
    const logins = apex.callsTo("POST", "/auth/login");
    expect(logins.length).toBe(1);
    expect(logins[0]?.body).toEqual({ username: "tester", password: "pw" });
    expect(apex.callsTo("GET", "/jobs/").map(bearerOf)).toEqual([`Bearer ${jwt}`, `Bearer ${jwt}`]);
  });

  test("a login with no access_token cookie is an ApexApiError, not a silent unauthenticated call", async () => {
    apex.route("POST", "/auth/login", () => json({ user: {} }, 200, { "Set-Cookie": "other=1" }));
    const err = await caught(apexRequest("prod", "GET", "/jobs/j1"));
    expect(err).toBeInstanceOf(ApexApiError);
    expect((err as ApexApiError).path).toBe("/auth/login");
    expect((err as ApexApiError).message).toMatch(/no access_token cookie/);
    expect(apex.callsTo("GET", "/jobs/").length).toBe(0);
  });

  test("a rejected login surfaces the server's detail", async () => {
    apex.route("POST", "/auth/login", () => json({ detail: "Invalid credentials" }, 401));
    const err = await caught(apexRequest("prod", "GET", "/jobs/j1"));
    expect((err as ApexApiError).status).toBe(401);
    expect((err as ApexApiError).message).toContain("Invalid credentials");
    expect(apex.callsTo("POST", "/auth/login").length).toBe(1);
  });

  test("concurrent first calls share one login and one folder lookup", async () => {
    apex.route("GET", "/jobs/", () => json({}));
    await Promise.all(Array.from({ length: 6 }, () => apexRequest("prod", "GET", "/jobs/j")));
    expect(apex.callsTo("POST", "/auth/login").length).toBe(1);
    expect(apex.callsTo("GET", "/folders").length).toBe(1);
  });

  test("a JWT expiring inside the 60s skew is not reused; a long-lived one is", async () => {
    apex.route("GET", "/jobs/", () => json({}));
    const soon = Math.floor(Date.now() / 1000) + 30;
    apex.route("POST", "/auth/login", loginReturning(fakeJwt(soon)));
    await apexRequest("prod", "GET", "/jobs/a");
    await apexRequest("prod", "GET", "/jobs/b");
    // never reused: every authenticated request (folders lookup + 2 reads) logged in afresh
    const logins = apex.callsTo("POST", "/auth/login").length;
    expect(logins).toBe(3);
    expect(apex.calls.length - logins).toBe(3);

    _resetApexClient();
    apex.calls.length = 0;
    apex.route("POST", "/auth/login", loginReturning(fakeJwt(soon + 3_600)));
    await apexRequest("prod", "GET", "/jobs/a");
    await apexRequest("prod", "GET", "/jobs/b");
    expect(apex.callsTo("POST", "/auth/login").length).toBe(1);
  });

  test("a 401 re-logs in exactly once and retries with the fresh token", async () => {
    const [old, fresh] = [fakeJwt(), fakeJwt(Math.floor(Date.now() / 1000) + 90_000)];
    apex.route("POST", "/auth/login", loginReturning(old, fresh));
    apex.route("GET", "/jobs/", (c) =>
      bearerOf(c) === `Bearer ${fresh}` ? json({ id: "j1" }) : json({ detail: "expired" }, 401),
    );
    expect(await apexRequest("prod", "GET", "/jobs/j1")).toEqual({ id: "j1" });
    expect(apex.callsTo("POST", "/auth/login").length).toBe(2);
    expect(apex.callsTo("GET", "/jobs/").length).toBe(2);
  });

  test("a second consecutive 401 is reported, not retried forever", async () => {
    apex.route("GET", "/jobs/", () => json({ detail: "nope" }, 401));
    const err = await caught(apexRequest("prod", "GET", "/jobs/j1"));
    expect((err as ApexApiError).status).toBe(401);
    expect(apex.callsTo("POST", "/auth/login").length).toBe(2);
    expect(apex.callsTo("GET", "/jobs/").length).toBe(2);
  });
});

describe("folder scoping", () => {
  const apex = useApexStub();
  const ok = () => json({});

  test("every request carries the env's folder id (prod default, pre_prod test folder)", async () => {
    apex.route("GET", "/jobs/", ok);
    apex.route("POST", "/queues/", ok);
    await apexRequest("prod", "GET", "/jobs/a");
    await apexRequest("pre_prod", "GET", "/jobs/a");
    await apexRequest("pre_prod", "POST", "/queues/q/items", { body: { reference: "r" } });
    const ids = [...apex.callsTo("GET", "/jobs/"), ...apex.callsTo("POST", "/queues/")].map(
      (c) => c.headers["x-folder-id"],
    );
    expect(ids).toEqual([
      FOLDER_IDS["Authorization"],
      FOLDER_IDS["Authorization Test"],
      FOLDER_IDS["Authorization Test"],
    ]);
  });

  test("a read-side folder override wins over the env default", async () => {
    apex.route("GET", "/jobs/", ok);
    await apexRequest("prod", "GET", "/jobs/a", { folder: "Eligibility Check" });
    expect(apex.callsTo("GET", "/jobs/")[0]?.headers["x-folder-id"]).toBe(
      FOLDER_IDS["Eligibility Check"],
    );
  });

  test("an unknown folder name lists the available ones and sends nothing", async () => {
    apex.route("GET", "/jobs/", ok);
    const err = await caught(apexRequest("prod", "GET", "/jobs/a", { folder: "Nope" }));
    expect(err).toBeInstanceOf(ExpectedError);
    expect((err as Error).message).toContain("'Nope' not found");
    expect((err as Error).message).toContain(
      "Authorization, Authorization Test, Eligibility Check",
    );
    expect(apex.callsTo("GET", "/jobs/").length).toBe(0);
  });

  test("the folder listing is fetched once and a failed fetch is not cached", async () => {
    let fail = true;
    apex.route("GET", "/folders", () =>
      fail
        ? json({ detail: "boom" }, 500)
        : json({ items: [{ id: FOLDER_IDS["Authorization"], name: "Authorization" }] }),
    );
    expect(await caught(resolveFolderId("prod"))).toBeInstanceOf(ApexApiError);
    fail = false;
    expect(await resolveFolderId("prod")).toEqual({
      id: FOLDER_IDS["Authorization"] as string,
      name: "Authorization",
    });
    await resolveFolderId("prod");
    expect(apex.callsTo("GET", "/folders").length).toBe(2);
  });
});

describe("write gate", () => {
  const apex = useApexStub();

  test.each([
    "POST",
    "PATCH",
    "DELETE",
  ] as const)("%s on prod is refused before any fetch", async (m) => {
    const err = await caught(apexRequest("prod", m, "/queues/q/items", { body: {} }));
    expect(err).toBeInstanceOf(ExpectedError);
    expect((err as Error).message).toMatch(/pre_prod-only/);
    expect(apex.calls.length).toBe(0);
  });

  test("a write with a folder override is refused before any fetch", async () => {
    const err = await caught(
      apexRequest("pre_prod", "POST", "/jobs", { body: {}, folder: "Authorization" }),
    );
    expect((err as Error).message).toMatch(/never take a folder override/);
    expect(apex.calls.length).toBe(0);
  });

  test("a pre_prod write posts the JSON body to the test folder", async () => {
    apex.route("POST", "/queues/", () => json({ id: "item-1" }, 201));
    expect(
      await apexRequest("pre_prod", "POST", "/queues/q/items", { body: { reference: "r" } }),
    ).toEqual({
      id: "item-1",
    });
    const post = apex.callsTo("POST", "/queues/")[0];
    expect(post?.body).toEqual({ reference: "r" });
    expect(post?.headers["content-type"]).toBe("application/json");
    expect(post?.headers["x-folder-id"]).toBe(FOLDER_IDS["Authorization Test"]);
  });

  test("an empty 2xx body (e.g. a 204) is null", async () => {
    apex.route("POST", "/jobs/", () => new Response(null, { status: 204 }));
    expect(await apexRequest("pre_prod", "POST", "/jobs/j1/cancel", { body: {} })).toBeNull();
  });
});

describe("retries", () => {
  const apex = useApexStub();
  const reset = (): never => {
    throw new TypeError("fetch failed");
  };

  test("a GET survives two connection resets (3 attempts)", async () => {
    let n = 0;
    apex.route("GET", "/jobs/", () => (++n < 3 ? reset() : json({ id: "j1" })));
    expect(await apexRequest("prod", "GET", "/jobs/j1")).toEqual({ id: "j1" });
    expect(n).toBe(3);
  });

  test("a GET that resets every time gives up after 3 attempts with the network error", async () => {
    apex.route("GET", "/jobs/", reset);
    expect(await caught(apexRequest("prod", "GET", "/jobs/j1"))).toBeInstanceOf(TypeError);
    expect(apex.callsTo("GET", "/jobs/").length).toBe(3);
  });

  test("a POST is never retried on a reset", async () => {
    apex.route("POST", "/queues/", reset);
    expect(
      await caught(apexRequest("pre_prod", "POST", "/queues/q/items", { body: {} })),
    ).toBeInstanceOf(TypeError);
    expect(apex.callsTo("POST", "/queues/").length).toBe(1);
  });

  test("a GET 429 waits Retry-After and retries; gives up after 3 retries", async () => {
    let n = 0;
    apex.route("GET", "/jobs/", () =>
      ++n === 1 ? json({ detail: "slow" }, 429, { "Retry-After": "0" }) : json({}),
    );
    await apexRequest("prod", "GET", "/jobs/j1");
    expect(n).toBe(2);

    apex.route("GET", "/jobs/", () => json({ detail: "slow" }, 429, { "Retry-After": "0" }));
    const err = await caught(apexRequest("prod", "GET", "/jobs/j1"));
    expect((err as ApexApiError).status).toBe(429);
    expect(apex.callsTo("GET", "/jobs/").length).toBe(2 + 4);
  });

  test("a write 429 is not retried", async () => {
    apex.route("POST", "/queues/", () => json({ detail: "slow" }, 429, { "Retry-After": "0" }));
    const err = await caught(apexRequest("pre_prod", "POST", "/queues/q/items", { body: {} }));
    expect((err as ApexApiError).status).toBe(429);
    expect(apex.callsTo("POST", "/queues/").length).toBe(1);
  });
});

describe("error messages", () => {
  const apex = useApexStub();
  const failWith = async (status: number, body: unknown): Promise<ApexApiError> => {
    apex.route("GET", "/jobs/", () =>
      typeof body === "string" ? new Response(body, { status }) : json(body, status),
    );
    return (await caught(apexRequest("prod", "GET", "/jobs/j1"))) as ApexApiError;
  };

  test("a string detail becomes the message, with status/method/path on the error", async () => {
    const e = await failWith(404, { detail: "Job not found" });
    expect(e).toBeInstanceOf(ExpectedError);
    expect([e.status, e.method, e.path]).toEqual([404, "GET", "/jobs/j1"]);
    expect(e.message).toBe("Apex GET /jobs/j1 -> 404: Job not found");
  });

  test("a pydantic detail list is flattened to loc: msg pairs", async () => {
    const e = await failWith(422, {
      detail: [
        {
          loc: ["query", "page_size"],
          msg: "ensure this value is less than or equal to 200",
          type: "x",
        },
        { loc: ["body", "reference"], msg: "field required", type: "y" },
      ],
    });
    expect(e.message).toContain(
      "query.page_size: ensure this value is less than or equal to 200; body.reference: field required",
    );
  });

  test("a long detail is capped at 300 characters", async () => {
    const e = await failWith(500, { detail: "x".repeat(1_000) });
    expect(e.message).toBe(`Apex GET /jobs/j1 -> 500: ${"x".repeat(300)}…`);
  });

  test("a 403 adds the permission hint; a non-JSON error body is still reported", async () => {
    expect((await failWith(403, { detail: "Forbidden" })).message).toMatch(
      /Forbidden — the Apex account lacks the role/,
    );
    expect((await failWith(502, "bad gateway")).message).toContain("bad gateway");
  });
});

describe("apexPages", () => {
  const apex = useApexStub();
  const page = (n: number, totalPages: number, size: number, rows: number) =>
    json({
      items: Array.from({ length: rows }, (_, i) => ({ id: `p${n}-${i}` })),
      total: totalPages * size,
      page: n,
      page_size: size,
      total_pages: totalPages,
    });

  test("clamps page_size to 200 and walks pages until total_pages", async () => {
    apex.route("GET", "/jobs", (c) => page(Number(params(c).get("page")), 3, 200, 200));
    const rows = await apexPages<{ id: string }>(
      "prod",
      "/jobs",
      { page_size: "500", status: "failed" },
      10_000,
    );
    expect(rows.length).toBe(600);
    const calls = apex.callsTo("GET", "/jobs");
    expect(calls.map((c) => params(c).get("page"))).toEqual(["1", "2", "3"]);
    expect(calls.every((c) => params(c).get("page_size") === "200")).toBe(true);
    expect(calls.every((c) => params(c).get("status") === "failed")).toBe(true);
  });

  test("stops at max mid-page and asks for no more rows than it needs", async () => {
    apex.route("GET", "/jobs", (c) => page(Number(params(c).get("page")), 9, 7, 7));
    const rows = await apexPages("prod", "/jobs", {}, 7);
    expect(rows.length).toBe(7);
    expect(apex.callsTo("GET", "/jobs").length).toBe(1);
    expect(params(apex.callsTo("GET", "/jobs")[0]).get("page_size")).toBe("7");
  });

  test("returns the rows of a bare-array route, truncated to max", async () => {
    apex.route("GET", "/jobs/j1/artifacts", () => json([{ id: "a" }, { id: "b" }, { id: "c" }]));
    expect(await apexPages("prod", "/jobs/j1/artifacts", {}, 2)).toEqual([
      { id: "a" },
      { id: "b" },
    ]);
    expect(apex.callsTo("GET", "/jobs/j1/artifacts").length).toBe(1);
  });

  test("an empty page ends the walk even if total_pages says more", async () => {
    apex.route("GET", "/jobs", (c) => page(Number(params(c).get("page")), 5, 200, 0));
    expect(await apexPages("prod", "/jobs", {}, 1_000)).toEqual([]);
    expect(apex.callsTo("GET", "/jobs").length).toBe(1);
  });
});

describe("apexDownload", () => {
  const apex = useApexStub();
  const URL_PATH = "/api/v1/jobs/j1/artifacts/a1";

  test("forwards Range, returns bytes + content-range, sends Bearer but no folder header", async () => {
    apex.route(
      "GET",
      "/jobs/j1/artifacts/a1",
      () =>
        new Response(new Uint8Array([1, 2, 3]), {
          status: 206,
          headers: { "content-range": "bytes 0-2/10" },
        }),
    );
    const r = await apexDownload("prod", URL_PATH, { range: "bytes=0-2" });
    expect(r).toEqual({
      status: 206,
      bytes: new Uint8Array([1, 2, 3]),
      contentRange: "bytes 0-2/10",
    });
    const call = apex.callsTo("GET", "/jobs/j1/artifacts/a1")[0];
    expect(call?.headers["range"]).toBe("bytes=0-2");
    expect(call?.headers["authorization"]).toMatch(/^Bearer /);
    expect(call?.headers["x-folder-id"]).toBeUndefined();
    expect(apex.callsTo("GET", "/folders").length).toBe(0);
  });

  test("a full 200 has no content-range; a non-2xx throws ApexApiError with a relative path", async () => {
    apex.route("GET", "/jobs/j1/artifacts/a1", () => new Response("hello", { status: 200 }));
    expect((await apexDownload("prod", URL_PATH)).contentRange).toBeNull();
    apex.route("GET", "/jobs/j1/artifacts/a1", () => json({ detail: "gone" }, 404));
    const err = await caught(apexDownload("prod", URL_PATH));
    expect((err as ApexApiError).path).toBe("/jobs/j1/artifacts/a1");
    expect((err as ApexApiError).status).toBe(404);
  });

  test.each([
    "https://evil.example.com/api/v1/x",
    "/jobs/j1/artifacts/a1",
    "/api/v1/../admin",
  ])("refuses %s without sending the Bearer token anywhere", async (url) => {
    expect(await caught(apexDownload("prod", url))).toBeInstanceOf(ExpectedError);
    expect(apex.calls.length).toBe(0);
  });
});

describe("apexDashboardLink", () => {
  const apex = useApexStub();
  test("joins the configured base URL and the path, making no request", () => {
    expect(apexDashboardLink("/jobs/j1")).toBe("https://apex.example.com/jobs/j1");
    expect(apex.calls.length).toBe(0);
  });
});

describe("apexDashboardLink when unconfigured", () => {
  useApexStub({ apex: null });
  test("is the empty string", () => {
    expect(apexDashboardLink("/jobs/j1")).toBe("");
  });
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `bun test src/apex/client.test.ts` — Expected: FAIL (`Cannot find module './client.js'`).

- [ ] **Step 3: Implement**

3a. Replace `src/apex/errors.ts` with:

```ts
// Apex domain errors. Both extend ExpectedError: an unconfigured server or a structured
// HTTP answer from Apex is user/upstream state, not a bug in this server, so classify()
// (mcp/feedback.ts) treats them as `expected` by identity.

import { ExpectedError } from "../shared/expected-error.js";
import { prop } from "../shared/util.js";

const DETAIL_CAP = 300;
const FORBIDDEN_HINT =
  " — the Apex account lacks the role for this action (e.g. jobs:download_artifacts, jobs:force_cancel); ask an Apex admin to grant it";

export class ApexNotConfiguredError extends ExpectedError {
  // Names of the fields neither the file nor the environment supplied.
  readonly missing: string[];
  // true = some, but not all, of the fields are set (a half-finished setup, as opposed
  // to Apex simply not being used) — `doctor` flags only that case.
  readonly partial: boolean;

  constructor(missing: { field: string; envVar: string }[], configured: number) {
    super(
      `Apex is not configured — missing ${missing.map((m) => m.field).join(", ")}. ` +
        `Set ${missing.map((m) => `apex.${m.field}`).join(", ")} in the config file, or the ` +
        `environment variable${missing.length === 1 ? "" : "s"} ${missing.map((m) => m.envVar).join(", ")} ` +
        "(the file wins per field; env vars only fill what the file leaves out).",
    );
    this.name = "ApexNotConfiguredError";
    this.missing = missing.map((m) => m.field);
    this.partial = configured > 0;
  }
}

// A non-2xx answer from the Apex API (or a login that returned no session cookie).
// `path` is relative to /api/v1 so messages and callers branch on `.status` / `.path`
// instead of regexing text. Message = Apex's own `detail`, already capped by the client.
export class ApexApiError extends ExpectedError {
  readonly status: number;
  readonly method: string;
  readonly path: string;

  constructor(method: string, path: string, status: number, detail: string) {
    super(`Apex ${method} ${path} -> ${status}: ${detail}`);
    this.name = "ApexApiError";
    this.status = status;
    this.method = method;
    this.path = path;
  }
}

// Message from an Apex error body: `detail` as a string, or pydantic's [{loc,msg}] list.
export function apexErrorDetail(body: unknown, status: number): string {
  const detail = prop(body, "detail");
  const text =
    typeof detail === "string"
      ? detail
      : Array.isArray(detail)
        ? detail.map(pydanticMessage).join("; ")
        : typeof body === "string"
          ? body
          : JSON.stringify(body);
  const capped = text.length > DETAIL_CAP ? `${text.slice(0, DETAIL_CAP)}…` : text;
  return status === 403 ? `${capped}${FORBIDDEN_HINT}` : capped;
}

function pydanticMessage(item: unknown): string {
  const msg = prop(item, "msg");
  const loc = prop(item, "loc");
  const where = Array.isArray(loc) ? loc.join(".") : "";
  const text = typeof msg === "string" ? msg : JSON.stringify(item);
  return where ? `${where}: ${text}` : text;
}
```

3b. Create `src/apex/client.ts`:

```ts
// The single Apex HTTP chokepoint — nothing else in the repo may fetch against Apex.
// Owns: login (JWT from the Set-Cookie, in-memory, single-flighted, refreshed before its
// own `exp`), folder-name -> X-Folder-Id resolution (sent on EVERY request: omitting it
// silently reads the user's default folder), the pre_prod-only write gate, retries
// (GETs only), and error shaping. Apex routes take paths relative to `/api/v1`.

import { type ApexConfig, type Env, onConfigReload, resolveApex } from "../config/config.js";
import { ExpectedError } from "../shared/expected-error.js";
import { rateLimitDelayMs, sleep } from "../shared/http.js";
import { prop, safeJsonParse } from "../shared/util.js";
import { ApexApiError, ApexNotConfiguredError, apexErrorDetail } from "./errors.js";

type Method = "GET" | "POST" | "PATCH" | "DELETE";

const API = "/api/v1";
const LOGIN_PATH = "/auth/login";
const MAX_PAGE_SIZE = 200; // above this the server answers 422
const GET_ATTEMPTS = 3; // network-error retries apply to GETs only: a retried write could double-post
const NETWORK_BACKOFF_MS = 50; // × attempt number
const MAX_RATE_LIMIT_RETRIES = 3;
const TOKEN_SKEW_MS = 60_000; // refresh this long before the JWT's own expiry
const FALLBACK_TOKEN_TTL_MS = 10 * 60_000; // when the JWT carries no readable exp

interface Token {
  value: string;
  expiresAt: number; // epoch ms
}

let token: Token | undefined;
let loginInFlight: Promise<string> | undefined;
let folderIds: Promise<Map<string, string>> | undefined;

// Test-only (and config-reload): forget the session token and the folder-id cache.
export function _resetApexClient(): void {
  token = undefined;
  loginInFlight = undefined;
  folderIds = undefined;
}

// A config edit may change the account, URL or folders — never keep a token/ids from the old one.
onConfigReload(_resetApexClient);

// `exp` (seconds) from the JWT payload, as epoch ms; undefined when not readable.
function jwtExpiryMs(jwt: string): number | undefined {
  const payload = jwt.split(".")[1];
  const exp = payload
    ? prop(safeJsonParse(Buffer.from(payload, "base64url").toString()), "exp")
    : undefined;
  return typeof exp === "number" ? exp * 1000 : undefined;
}

// Login's only useful output is the `access_token` Set-Cookie — the body holds user info.
async function login(cfg: ApexConfig): Promise<Token> {
  const res = await fetch(`${cfg.apiUrl}${API}${LOGIN_PATH}`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: JSON.stringify({ username: cfg.username, password: cfg.password }),
  });
  const text = await res.text();
  if (res.status >= 400) {
    throw new ApexApiError(
      "POST",
      LOGIN_PATH,
      res.status,
      apexErrorDetail(safeJsonParse(text), res.status),
    );
  }
  const cookie = res.headers.getSetCookie().find((c) => c.startsWith("access_token="));
  const value = cookie?.slice("access_token=".length).split(";")[0];
  if (!value) {
    throw new ApexApiError(
      "POST",
      LOGIN_PATH,
      res.status,
      "login succeeded but returned no access_token cookie",
    );
  }
  const expiry = jwtExpiryMs(value) ?? Date.now() + FALLBACK_TOKEN_TTL_MS;
  return { value, expiresAt: expiry - TOKEN_SKEW_MS };
}

// Concurrent callers share one login (the login route is rate-limited).
function bearerToken(cfg: ApexConfig): Promise<string> {
  if (token && token.expiresAt > Date.now()) return Promise.resolve(token.value);
  loginInFlight ??= login(cfg)
    .then((t) => {
      token = t;
      return t.value;
    })
    .finally(() => {
      loginInFlight = undefined;
    });
  return loginInFlight;
}

// One authenticated request. A 401 re-logins exactly once and retries (any method: the
// server rejected the credentials, so nothing ran). Only GETs retry on network errors or
// 429; writes surface the first failure.
async function transport(
  cfg: ApexConfig,
  method: Method,
  url: string,
  headers: Record<string, string>,
  body?: string,
): Promise<Response> {
  let relogged = false;
  let rateLimited = 0;
  let netFailures = 0;
  for (;;) {
    const used = await bearerToken(cfg);
    let res: Response;
    try {
      res = await fetch(url, {
        method,
        headers: { ...headers, Authorization: `Bearer ${used}` },
        ...(body !== undefined ? { body } : {}),
      });
    } catch (e) {
      netFailures++;
      if (method !== "GET" || netFailures >= GET_ATTEMPTS) throw e;
      await sleep(NETWORK_BACKOFF_MS * netFailures);
      continue;
    }
    if (res.status === 401 && !relogged) {
      relogged = true;
      if (token?.value === used) token = undefined; // a concurrent request may already have refreshed it
      continue;
    }
    if (res.status === 429 && method === "GET" && rateLimited < MAX_RATE_LIMIT_RETRIES) {
      rateLimited++;
      await sleep(rateLimitDelayMs(res.headers.get("Retry-After")));
      continue;
    }
    return res;
  }
}

// name -> uuid for every folder. Cached for the process; a failed fetch is not cached.
// ponytail: one page (200 folders) — walk total_pages if a deployment ever has more.
// This is the one call that cannot carry X-Folder-Id: it is how the ids are found.
function folderMap(cfg: ApexConfig): Promise<Map<string, string>> {
  folderIds ??= (async () => {
    const res = await transport(
      cfg,
      "GET",
      `${cfg.apiUrl}${API}/folders?page_size=${MAX_PAGE_SIZE}`,
      {
        Accept: "application/json",
      },
    );
    const body = safeJsonParse(await res.text());
    if (res.status >= 400) {
      throw new ApexApiError("GET", "/folders", res.status, apexErrorDetail(body, res.status));
    }
    const items = prop(body, "items");
    const rows = Array.isArray(items) ? items : [];
    const map = new Map<string, string>();
    for (const row of rows) {
      const id = prop(row, "id");
      const name = prop(row, "name");
      if (typeof id === "string" && typeof name === "string") map.set(name, id);
    }
    return map;
  })().catch((e: unknown) => {
    folderIds = undefined;
    throw e;
  });
  return folderIds;
}

async function lookupFolder(
  cfg: ApexConfig,
  env: Env,
  folder: string | undefined,
): Promise<{ id: string; name: string }> {
  const name = folder ?? cfg.folderByEnv[env];
  const ids = await folderMap(cfg);
  const id = ids.get(name);
  if (!id) {
    throw new ExpectedError(
      `Apex folder '${name}' not found (available: ${[...ids.keys()].join(", ") || "none"})`,
    );
  }
  return { id, name };
}

// The X-Folder-Id uuid for an env's configured folder, or for an explicit `folder` name.
export function resolveFolderId(env: Env, folder?: string): Promise<{ id: string; name: string }> {
  return lookupFolder(resolveApex(), env, folder);
}

// Prod and test folders share one deployment and one login, so this gate — checked
// before anything touches the network — is the only thing keeping writes out of prod.
function assertWritable(env: Env, method: Method, path: string, folder: string | undefined): void {
  if (method === "GET") return;
  if (env !== "pre_prod") {
    throw new ExpectedError(
      `Apex ${method} ${path} refused: writes are pre_prod-only (env=${env})`,
    );
  }
  if (folder) {
    throw new ExpectedError(`Apex ${method} ${path} refused: writes never take a folder override`);
  }
}

// One Apex JSON call, scoped to the env's folder (or `opts.folder`, reads only).
// Returns the parsed body (null when empty); throws ApexApiError on a non-2xx.
export async function apexRequest(
  env: Env,
  method: Method,
  path: string,
  opts: { query?: Record<string, string>; body?: unknown; folder?: string | undefined } = {},
): Promise<unknown> {
  assertWritable(env, method, path, opts.folder);
  const cfg = resolveApex();
  const { id } = await lookupFolder(cfg, env, opts.folder);
  const qs = new URLSearchParams(opts.query ?? {}).toString();
  const hasBody = opts.body !== undefined;
  const res = await transport(
    cfg,
    method,
    `${cfg.apiUrl}${API}${path}${qs ? `?${qs}` : ""}`,
    {
      Accept: "application/json",
      "X-Folder-Id": id,
      ...(hasBody ? { "Content-Type": "application/json" } : {}),
    },
    hasBody ? JSON.stringify(opts.body) : undefined,
  );
  const parsed = safeJsonParse(await res.text());
  if (res.status >= 400) {
    throw new ApexApiError(method, path, res.status, apexErrorDetail(parsed, res.status));
  }
  return parsed ?? null;
}

// Rows of a paged route. Walks `page` up to `total_pages` until `max` rows are collected;
// bare-array routes (workflow versions, review-queue, artifacts) come back as-is.
export async function apexPages<T>(
  env: Env,
  path: string,
  query: Record<string, string>,
  max: number,
  folder?: string,
): Promise<T[]> {
  const rows: T[] = [];
  const pageSize = Math.min(Number(query["page_size"] ?? MAX_PAGE_SIZE), MAX_PAGE_SIZE, max);
  for (let page = 1; rows.length < max; page++) {
    const body = await apexRequest(env, "GET", path, {
      query: { ...query, page: String(page), page_size: String(pageSize) },
      folder,
    });
    if (Array.isArray(body)) return body.slice(0, max) as T[];
    const items = prop(body, "items");
    if (Array.isArray(items)) rows.push(...(items as T[]));
    const totalPages = prop(body, "total_pages");
    if (
      !Array.isArray(items) ||
      items.length === 0 ||
      typeof totalPages !== "number" ||
      page >= totalPages
    )
      break;
  }
  return rows.slice(0, max);
}

// Log in now (or reuse the live session) without making a data call — what `doctor` probes.
export async function apexLogin(): Promise<void> {
  await bearerToken(resolveApex());
}

// Artifact bytes from a `download_url` exactly as the artifact listing returns it
// (`/api/v1/jobs/{id}/artifacts/{artifactId}`). Bearer only: downloads need no folder
// header. A Range read (`bytes=-65536`) gives a cheap tail of a large file. GET-only, so
// no write gate; `env` is accepted for call-site symmetry with the other client entry points.
export async function apexDownload(
  _env: Env,
  downloadUrl: string,
  opts: { range?: string } = {},
): Promise<{ status: number; bytes: Uint8Array; contentRange: string | null }> {
  // Only same-origin API paths: the Bearer token must never be sent to another host.
  if (!downloadUrl.startsWith(`${API}/`) || downloadUrl.includes("..")) {
    throw new ExpectedError(`Apex download refused: '${downloadUrl}' is not an ${API}/ path`);
  }
  const cfg = resolveApex();
  const path = downloadUrl.slice(API.length);
  const res = await transport(cfg, "GET", `${cfg.apiUrl}${downloadUrl}`, {
    ...(opts.range ? { Range: opts.range } : {}),
  });
  if (res.status >= 400) {
    const detail = apexErrorDetail(safeJsonParse(await res.text()), res.status);
    throw new ApexApiError("GET", path, res.status, detail);
  }
  return {
    status: res.status,
    bytes: new Uint8Array(await res.arrayBuffer()),
    contentRange: res.headers.get("content-range"),
  };
}

// Dashboard deep link (`/jobs/{id}` is served from the same base URL); "" when Apex is unconfigured.
export function apexDashboardLink(path: string): string {
  try {
    return `${resolveApex().apiUrl}${path}`;
  } catch (e) {
    if (e instanceof ApexNotConfiguredError) return "";
    throw e;
  }
}
```

- [ ] **Step 4: Run tests** — `bun test src/apex/client.test.ts` → PASS (37 tests)
- [ ] **Step 5: Full gate** — `bun run typecheck && bun test && bunx biome check src` → all green
- [ ] **Step 6: Commit**
```bash
git add src/apex/client.ts src/apex/client.test.ts src/apex/test-support.ts src/apex/errors.ts
git commit -m "feat(apex): single HTTP chokepoint with folder scoping and pre-prod write gate" -m "Every Apex call goes through apexRequest: cookie-JWT login (single-flight, expiry-aware, one re-login on 401), X-Folder-Id on every request because omitting it silently reads the default folder, and a gate that refuses any non-GET unless env is pre_prod before anything touches the network. GETs retry network resets and 429s; writes are never retried so they cannot double-post."
```

---

### Task 8: `doctor` Apex probes

**Decision (spec §11: "which config source was used, that login succeeds, and that both folders resolve"):** Apex is optional, so an Apex that is *not configured at all* must not make `report.ok` false: it yields one passing check `{name: "apex config", ok: true, detail: "not configured (optional) — …"}` and no requests are made. A *half-configured* Apex (some but not all of apiUrl/username/password) is a mistake, not a choice, so it yields one failing `apex config` check carrying the `ApexNotConfiguredError` message. When fully configured: `apex config` (which source supplied each field), `apex login`, and one `apex <env> folder` check per env (`folder '<name>' -> <uuid>`); any failure makes `report.ok` false, like UiPath. Apex probes never affect the Copilot/UiPath checks (each probe is independent).

**Files:**
- Modify: `src/copilot/doctor.ts` (header comment lines 1-5; imports lines 7-11; new exported `apexProbes()` after `ENVS` at line 41; `runDoctor` comment lines 43-46, `TOTAL_CHECKS` at line 63, extra `Promise.all` entries before line 110)
- Modify: `src/server.ts` (header comment line 33; `doctor` tool description lines 2357-2364; line numbers shift after Task 5, hunks carry context)
- Test: `src/copilot/doctor.test.ts` (new)

**Interfaces:**
- Consumes: `apexLogin`, `resolveFolderId` (Task 7), `ApexNotConfiguredError` (Task 6, `.partial`), `resolveApex` (Task 6), `useApexStub`, `json`, `FOLDER_IDS` (`src/apex/test-support.ts`, Task 7).
- Produces: `export function apexProbes(): (() => Promise<DoctorCheck>)[]` in `src/copilot/doctor.ts` (thunks so `runDoctor` counts them for progress; exported for tests). `DoctorReport`/`DoctorCheck` shapes unchanged; check names `apex config`, `apex login`, `apex prod folder`, `apex pre_prod folder`.

- [ ] **Step 1: Write the failing tests**

`src/copilot/doctor.test.ts`:

```ts
import { describe, expect, test } from "bun:test";
import { FOLDER_IDS, json, useApexStub } from "../apex/test-support.js";
import { apexProbes, type DoctorCheck } from "./doctor.js";

const runApexProbes = (): Promise<DoctorCheck[]> => Promise.all(apexProbes().map((run) => run()));
const byName = (checks: DoctorCheck[], name: string): DoctorCheck | undefined =>
  checks.find((c) => c.name === name);

describe("apexProbes: configured from the file", () => {
  const apex = useApexStub();

  test("reports the source, a working login, and each env's folder id", async () => {
    const checks = await runApexProbes();
    expect(checks.map((c) => [c.name, c.ok])).toEqual([
      ["apex config", true],
      ["apex login", true],
      ["apex prod folder", true],
      ["apex pre_prod folder", true],
    ]);
    expect(byName(checks, "apex config")?.detail).toBe(
      "apiUrl from config, username from config, password from config",
    );
    expect(byName(checks, "apex prod folder")?.detail).toBe(
      `folder 'Authorization' -> ${FOLDER_IDS["Authorization"]}`,
    );
    expect(byName(checks, "apex pre_prod folder")?.detail).toBe(
      `folder 'Authorization Test' -> ${FOLDER_IDS["Authorization Test"]}`,
    );
    expect(apex.callsTo("POST", "/auth/login").length).toBe(1); // checks share one session
  });

  test("a rejected login fails the login check (and the folder checks that need it), never throws", async () => {
    apex.route("POST", "/auth/login", () => json({ detail: "Invalid credentials" }, 401));
    const checks = await runApexProbes();
    expect(byName(checks, "apex config")?.ok).toBe(true);
    expect(byName(checks, "apex login")?.ok).toBe(false);
    expect(byName(checks, "apex login")?.detail).toContain("Invalid credentials");
    expect(byName(checks, "apex prod folder")?.ok).toBe(false);
  });
});

describe("apexProbes: a folder that does not exist", () => {
  useApexStub({
    apex: {
      apiUrl: "https://apex.example.com",
      username: "tester",
      password: "pw",
      folderByEnv: { pre_prod: "Missing Folder" },
    },
  });

  test("fails only that env, naming the folders that do exist", async () => {
    const checks = await runApexProbes();
    expect(byName(checks, "apex prod folder")?.ok).toBe(true);
    const bad = byName(checks, "apex pre_prod folder");
    expect(bad?.ok).toBe(false);
    expect(bad?.detail).toContain("'Missing Folder' not found");
    expect(bad?.detail).toContain("Authorization, Authorization Test, Eligibility Check");
  });
});

describe("apexProbes: configured from the environment", () => {
  useApexStub({ apex: { username: "file-user" } });

  test("says which fields came from the environment", async () => {
    process.env["APEX_API_URL"] = "https://apex.example.com";
    process.env["APEX_PASSWORD"] = "pw";
    try {
      const checks = await runApexProbes();
      expect(byName(checks, "apex config")?.detail).toBe(
        "apiUrl from env, username from config, password from env",
      );
      expect(byName(checks, "apex login")?.ok).toBe(true);
    } finally {
      delete process.env["APEX_API_URL"];
      delete process.env["APEX_PASSWORD"];
    }
  });
});

describe("apexProbes: not configured at all", () => {
  const apex = useApexStub({ apex: null });

  test("is one passing 'optional' check and makes no request", async () => {
    const checks = await runApexProbes();
    expect(checks).toEqual([
      {
        name: "apex config",
        target: "apex",
        ok: true,
        detail: expect.stringContaining("not configured (optional)"),
      },
    ]);
    expect(apex.calls.length).toBe(0);
  });
});

describe("apexProbes: half configured", () => {
  const apex = useApexStub({ apex: { username: "only-a-user" } });

  test("fails, naming what is missing, and makes no request", async () => {
    const checks = await runApexProbes();
    expect(checks.length).toBe(1);
    expect(checks[0]?.ok).toBe(false);
    expect(checks[0]?.detail).toContain("missing apiUrl, password");
    expect(apex.calls.length).toBe(0);
  });
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `bun test src/copilot/doctor.test.ts` — Expected: FAIL (`apexProbes` is not exported from `./doctor.js`).

- [ ] **Step 3: Implement**

```diff
@@ -1,11 +1,15 @@
 // doctor: probe the MCP server's connections to its external APIs and report what is
 // reachable. Read-only — it authenticates against the Copilot BE (prod + pre-prod),
 // fingerprints the clinic scope that session actually has, and makes one cheap
-// authenticated UiPath Orchestrator call per env. Intended for setup/onboarding
-// debugging ("are my creds + clinic + token + folders right?").
+// authenticated UiPath Orchestrator call per env, and — when configured — Apex: which
+// config source supplied the account, that login works, and that each env's folder
+// resolves to an id. Intended for setup/onboarding debugging ("are my creds + clinic +
+// token + folders right?").
 
+import { apexLogin, resolveFolderId as resolveApexFolder } from "../apex/client.js";
+import { ApexNotConfiguredError } from "../apex/errors.js";
 import type { Env } from "../config/config.js";
-import { getUipath, resolveAuth } from "../config/config.js";
+import { getUipath, resolveApex, resolveAuth } from "../config/config.js";
 import { prop, type StepProgress } from "../shared/util.js";
 import { listRecentJobs, resolveFolder } from "../uipath/uipath.js";
 import { connect } from "./session.js";
@@ -40,10 +44,56 @@
 
 const ENVS: readonly Env[] = ["prod", "pre_prod"];
 
+// Apex is optional, so the report treats "not configured" as fine: nothing is set at all =
+// one passing check saying so (UiPath/Copilot results are unaffected). A half-finished setup
+// (some but not all of apiUrl/username/password) is a failing check, since that is a mistake,
+// not a choice. Otherwise: config source, login, and one folder-resolution check per env.
+// Returned as thunks so runDoctor can count them for progress before running any.
+export function apexProbes(): (() => Promise<DoctorCheck>)[] {
+  let cfg: ReturnType<typeof resolveApex>;
+  try {
+    cfg = resolveApex();
+  } catch (e) {
+    if (!(e instanceof ApexNotConfiguredError)) throw e;
+    const check: DoctorCheck = e.partial
+      ? { name: "apex config", target: "apex", ok: false, detail: e.message }
+      : {
+          name: "apex config",
+          target: "apex",
+          ok: true,
+          detail:
+            "not configured (optional) — set the apex section of the config file, or APEX_API_URL / APEX_USER_NAME / APEX_PASSWORD, to enable Apex",
+        };
+    return [async () => check];
+  }
+  const { sources } = cfg;
+  return [
+    () =>
+      probe(
+        "apex config",
+        cfg.apiUrl,
+        async () =>
+          `apiUrl from ${sources.apiUrl}, username from ${sources.username}, password from ${sources.password}`,
+      ),
+    () =>
+      probe("apex login", cfg.apiUrl, async () => {
+        await apexLogin();
+        return `login OK as '${cfg.username}'`;
+      }),
+    ...ENVS.map(
+      (env) => () =>
+        probe(`apex ${env} folder`, cfg.apiUrl, async () => {
+          const folder = await resolveApexFolder(env);
+          return `folder '${folder.name}' -> ${folder.id}`;
+        }),
+    ),
+  ];
+}
+
 // Six independent probes: per env, an auth probe, a clinic-scope fingerprint, and an
-// Orchestrator round-trip. Each check is independent; one failure never aborts the
-// others, and config resolution happens inside the probes so one misconfigured env
-// fails one check instead of the whole report.
+// Orchestrator round-trip — plus the Apex probes (apexProbes). Each check is independent;
+// one failure never aborts the others, and config resolution happens inside the probes so
+// one misconfigured env fails one check instead of the whole report.
 export async function runDoctor(opts: {
   profile?: string | null;
   onProgress?: StepProgress;
@@ -60,7 +110,8 @@
     }
   };
 
-  const TOTAL_CHECKS = ENVS.length * 3;
+  const apex = apexProbes();
+  const TOTAL_CHECKS = ENVS.length * 3 + apex.length;
   let settled = 0;
   const track = (c: DoctorCheck): DoctorCheck => {
     settled++;
@@ -107,6 +158,7 @@
         } visible)`;
       }).then(track),
     ),
+    ...apex.map((run) => run().then(track)),
   ]);
 
   return { account, ok: checks.every((c) => c.ok), checks };
```

`src/server.ts` (doctor description + header comment; `server.test.ts` has no description assertions, only the `doctor` schema case at line 190, which is unchanged):

```diff
@@ -30,7 +30,7 @@
 //   - search_orders            filter orders by location/insurance/referredTo/type/auth/date range (read-only)
 //   - get_order_category_stats per-folder order counts for a given filter (read-only)
 //   - get_login_token          log into the Copilot BE for a profile/env and return the session JWT (read-only)
-//   - doctor                   probe the BE + UiPath connections and report what's reachable (read-only)
+//   - doctor                   probe the BE + UiPath + Apex connections and report what's reachable (read-only)
 //
 // See the copilot + copilot-order-mirror skills for the full flow and quirks.
 
@@ -2359,8 +2359,11 @@
       "for PROD and PRE-PROD, an auth check (Copilot BE login, reusing a cached session), a " +
       "clinic-scope fingerprint (GET /orders/locations, the cheap proof a support-account " +
       "session landed on the intended clinic and not just that login succeeded), and one cheap " +
-      "authenticated UiPath Orchestrator call per env/folder. READ-ONLY. Use it to debug setup " +
-      "(creds, clinic scope, UiPath token, folder access). Returns {account, ok, " +
+      "authenticated UiPath Orchestrator call per env/folder, and — when Apex is configured — " +
+      "which config source supplied the Apex account, that Apex login works, and that each env's " +
+      "folder resolves to an id (an unconfigured Apex is reported as optional, not a failure). " +
+      "READ-ONLY. Use it to debug setup (creds, clinic scope, UiPath token, Apex login, folder " +
+      "access). Returns {account, ok, " +
       "checks:[{name, target, ok, detail}]}.",
     inputSchema: {
       profile: z
```

- [ ] **Step 4: Run tests** — `bun test src/copilot/doctor.test.ts` → PASS (6 tests)
- [ ] **Step 5: Full gate** — `bun run typecheck && bun test && bunx biome check src` → all green
- [ ] **Step 6: Commit**
```bash
git add src/copilot/doctor.ts src/copilot/doctor.test.ts src/server.ts
git commit -m "feat(apex): doctor reports Apex config source, login and folder resolution" -m "Apex is optional, so an unconfigured Apex is a passing 'optional' check while a half-configured one fails; configured Apex gets config-source, login and per-env folder checks so a wrong password or missing folder shows up before a tool call does."
```

## Phase 3: Apex reads (Tasks 9–12)

> Executor notes (Phase 3): the contract surface below is extended in four additive ways, all used consistently from here on:
> 1. `downloadJobArtifacts(env, jobId, names, rootDir = defaultArtifactsRoot(), folder?: string | undefined)` — a 5th optional `folder` param. The tool takes `folder?` and the artifact listing needs the folder header outside the default folder (404 otherwise), but the contract signature has no way to carry it. Task 12 is the only caller.
> 2. `toApexItemCore(item, attempts, includeContent, queueName)` — a 4th `queueName: string` param. `QueueItemCore.queueName` is required but an Apex item row carries only `queue_id`; every caller already has the queue (list/find/pull).
> 3. Extra exported helpers other tasks may reuse (names are final): `src/apex/api.ts` `getQueue`, `isNotFound`; `src/apex/map.ts` `jobLabel`, `toApexJobAttempt(row): JobAttempt`, `attemptsFromItem(item): JobAttempt[]`, `toApexRunSummary(detail, parsed): ApexRunSummary`, `toApexItemStatus(QueueItemStatus): string`; `src/apex/backend.ts` `readJobLog(env, folder, jobId): Promise<ParsedJobLog | null>` (null = no `job.log` artifact listed; throws on HTTP failure) — Task 15 (`apexBuildFaultedJobIssue`) should call it instead of re-implementing the artifact → parse path.
> 4. New non-test support file `src/apex/test-rows.ts` (synthetic PHI-free row builders `jobRow/jobDetail/queueRow/itemRow/itemDetail/workflowRow/scheduleRow/artifactRow` + `JOB_ID/WORKFLOW_ID/QUEUE_ID/ITEM_ID`), created in Task 10 and reused by Tasks 11, 12 and any later Apex test (it imports only types from `api.ts`, so it never reaches the bundle).

Conventions used by every task below (all verified by running the exact code of this section in a scratch copy of the repo with minimal stand-ins for the Task 1/3/7 modules it consumes — `orchestrator/{types,backend,log-digest,log-semantics,faults,safety,portal}.ts`, `shared/expected-error.ts`, `apex/{errors,client,test-support}.ts`; the real ones are written by Tasks 1, 3, 6 and 7): `ExpectedError` is imported from `../shared/expected-error.js`; the fetch stub is Task 7's `src/apex/test-support.ts` (`useApexStub()`; **the last registered matching route prefix wins, so register a generic route before its more specific ones**; unmatched requests get a 404 `{detail}`); Biome may re-sort imports or re-wrap a long line when it first sees these files — run `bunx biome check --write src/apex` once before the gate and keep what it changes.

---

### Task 9: Apex `job.log` parser

**Files:**
- Create: `src/apex/job-log.ts`
- Test: `src/apex/job-log.test.ts`

**Interfaces:**
- Consumes: `JobLog` from `src/orchestrator/types.ts` (Task 1); `isRecord`, `safeJsonParse` from `src/shared/util.ts` (existing).
- Produces (contract names): `parseJobLog(text: string): ParsedJobLog`; `ParsedJobLog = { rows: ApexLogRow[]; footer: JobLogFooter | null; adapterResult: AdapterResult | null }`; `ApexLogRow extends JobLog { kind: LineKind; fields?: Record<string, unknown> }`; `LineKind = "header" | "footer" | "adapter" | "adapter-result" | "ndjson" | "traceback" | "plain"`; `JobLogFooter = { exitCode: number | null; timedOut: boolean; cancelled: boolean; suspended: boolean }`; `AdapterResult = { result: string; reason: string | null; exit: number | null; outcomes: string[] }`.

Decisions this task pins (each has a test): every non-blank line becomes one row, so header/adapter/footer lines are visible in `get_job_logs` exactly as written; NDJSON `level` maps `INFO|WARN|WARNING|ERROR|DEBUG` → `Info|Warn|Warn|Error|Debug`, an unknown level is kept verbatim capitalized (`NOTICE` → `Notice`), a missing/non-string level is `Info`; non-NDJSON, non-traceback lines are `Info`; **untimed rows inherit the previous row's `TimeStamp`, and leading untimed rows (header, early adapter lines) take the first timestamp that follows** (so no row has `""` unless the whole log has no timestamp, and `findLogStalls` never blames a gap on an untimed line); a Python traceback (the `Traceback (most recent call last):` line, its indented frames and the first unindented exception line) is `Level: "Error"` so the digest and `minLevel` filters catch crashes; a `{`-line that is not a JSON object with a string `message` is a plain line.

- [ ] **Step 1: Write the failing test**

Create `src/apex/job-log.test.ts`:
```ts
import { describe, expect, test } from "bun:test";
import { parseJobLog } from "./job-log.js";

const JOB = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee";
const HEADER = `=== apex job ${JOB} started (workflow=demo-portal runtime=python) ===`;
const FOOTER = `=== apex job ${JOB} finished: exit_code=21 timed_out=False cancelled=False suspended=False duration=9s ===`;
const nd = (o: Record<string, unknown>): string =>
  JSON.stringify({ v: 1, portal: "demo", timestamp: "2026-10-08T00:40:00.000Z", ...o });

// One of every line kind, in the order a failed run writes them.
const FULL = [
  HEADER,
  "[apex-adapter] claimed job, starting runtime",
  nd({ level: "INFO", message: "login ok" }),
  nd({
    level: "WARN",
    message: "slow page",
    timestamp: "2026-10-08T00:40:05.000Z",
    fields: { ms: 5200 },
  }),
  nd({ level: "ERROR", message: "row not found", timestamp: "2026-10-08T00:40:09.000Z" }),
  "a plain untyped line",
  "[trace] some other bracket line",
  "",
  "Traceback (most recent call last):",
  '  File "/app/main.py", line 10, in run',
  '    raise ValueError("boom")',
  "ValueError: boom",
  "[apex-adapter] result=FAILED reason=business_failure exit=21 outcomes=not_found,retry",
  FOOTER,
].join("\n");

describe("parseJobLog line kinds", () => {
  const parsed = parseJobLog(FULL);

  test("classifies every line, skipping blank ones", () => {
    expect(parsed.rows.map((r) => r.kind)).toEqual([
      "header",
      "adapter",
      "ndjson",
      "ndjson",
      "ndjson",
      "plain",
      "plain",
      "traceback",
      "traceback",
      "traceback",
      "traceback",
      "adapter-result",
      "footer",
    ]);
  });

  test("NDJSON rows map levels and keep message, timestamp and fields", () => {
    const [info, warn, error] = parsed.rows.filter((r) => r.kind === "ndjson");
    expect(info).toMatchObject({ Level: "Info", Message: "login ok" });
    expect(warn).toMatchObject({
      Level: "Warn",
      Message: "slow page",
      TimeStamp: "2026-10-08T00:40:05.000Z",
      fields: { ms: 5200 },
    });
    expect(error).toMatchObject({ Level: "Error", Message: "row not found" });
    expect(info?.fields).toBeUndefined();
  });

  test("non-NDJSON, non-traceback lines are Info and keep their text", () => {
    const plain = parsed.rows.filter((r) => r.kind === "plain");
    expect(plain.map((r) => [r.Level, r.Message])).toEqual([
      ["Info", "a plain untyped line"],
      ["Info", "[trace] some other bracket line"],
    ]);
  });

  test("a Python traceback is Error level through its exception line, and no further", () => {
    const tb = parsed.rows.filter((r) => r.kind === "traceback");
    expect(tb.every((r) => r.Level === "Error")).toBe(true);
    expect(tb.at(-1)?.Message).toBe("ValueError: boom");
    expect(parsed.rows.find((r) => r.kind === "adapter-result")?.Level).toBe("Info");
  });
});

describe("levels", () => {
  const level = (l: unknown): string | undefined =>
    parseJobLog(nd({ level: l, message: "m" })).rows[0]?.Level;

  test("known levels, case-insensitively", () => {
    expect(level("DEBUG")).toBe("Debug");
    expect(level("warning")).toBe("Warn");
    expect(level("error")).toBe("Error");
  });

  test("unknown levels are kept verbatim, capitalized; absent level is Info", () => {
    expect(level("NOTICE")).toBe("Notice");
    expect(level(undefined)).toBe("Info");
    expect(level(7)).toBe("Info");
  });
});

describe("timestamps", () => {
  test("untimed lines inherit the previous row's timestamp", () => {
    const text = [
      nd({ message: "a", timestamp: "2026-10-08T00:40:00.000Z" }),
      "[apex-adapter] midway note",
      nd({ message: "b", timestamp: "2026-10-08T00:41:00.000Z" }),
      FOOTER,
    ].join("\n");
    expect(parseJobLog(text).rows.map((r) => r.TimeStamp)).toEqual([
      "2026-10-08T00:40:00.000Z",
      "2026-10-08T00:40:00.000Z",
      "2026-10-08T00:41:00.000Z",
      "2026-10-08T00:41:00.000Z",
    ]);
  });

  test("leading untimed rows take the first timestamp that follows", () => {
    const rows = parseJobLog([HEADER, "[apex-adapter] boot", nd({ message: "a" })].join("\n")).rows;
    expect(rows.map((r) => r.TimeStamp)).toEqual(Array(3).fill("2026-10-08T00:40:00.000Z"));
  });

  test("a log with no timestamps keeps empty ones", () => {
    expect(parseJobLog(`${HEADER}\nplain`).rows.map((r) => r.TimeStamp)).toEqual(["", ""]);
  });
});

describe("footer", () => {
  test("parses exit code and flags", () => {
    expect(parseJobLog(FULL).footer).toEqual({
      exitCode: 21,
      timedOut: false,
      cancelled: false,
      suspended: false,
    });
  });

  test("True flags and exit_code=None", () => {
    const f = `=== apex job ${JOB} finished: exit_code=None timed_out=True cancelled=True suspended=True ===`;
    expect(parseJobLog(f).footer).toEqual({
      exitCode: null,
      timedOut: true,
      cancelled: true,
      suspended: true,
    });
  });

  test("a missing footer (killed run) is null, rows still parse", () => {
    const parsed = parseJobLog([HEADER, nd({ message: "a" })].join("\n"));
    expect(parsed.footer).toBeNull();
    expect(parsed.rows).toHaveLength(2);
  });
});

describe("adapter result", () => {
  test("parses result, reason, exit and outcomes", () => {
    expect(parseJobLog(FULL).adapterResult).toEqual({
      result: "FAILED",
      reason: "business_failure",
      exit: 21,
      outcomes: ["not_found", "retry"],
    });
  });

  test("`-` placeholders become null / []", () => {
    const line = "[apex-adapter] result=OK reason=- exit=- outcomes=-";
    expect(parseJobLog(line).adapterResult).toEqual({
      result: "OK",
      reason: null,
      exit: null,
      outcomes: [],
    });
  });

  test("an unparseable result line is a plain adapter row and no result", () => {
    const parsed = parseJobLog("[apex-adapter] result=OK");
    expect(parsed.adapterResult).toBeNull();
    expect(parsed.rows[0]?.kind).toBe("adapter");
  });
});

describe("malformed input", () => {
  test("broken NDJSON and JSON without a message are plain lines", () => {
    const text = ['{"v":1,"level":"ERROR","message":"cut off', '{"v":1,"level":"ERROR"}'].join(
      "\n",
    );
    const rows = parseJobLog(text).rows;
    expect(rows.map((r) => [r.kind, r.Level])).toEqual([
      ["plain", "Info"],
      ["plain", "Info"],
    ]);
  });

  test("a non-object `fields` is dropped", () => {
    expect(parseJobLog(nd({ message: "m", fields: "x" })).rows[0]?.fields).toBeUndefined();
  });

  test("empty text", () => {
    expect(parseJobLog("")).toEqual({ rows: [], footer: null, adapterResult: null });
  });

  test("CRLF line endings", () => {
    const parsed = parseJobLog(`${HEADER}\r\n${nd({ message: "a" })}\r\n${FOOTER}\r\n`);
    expect(parsed.rows.map((r) => r.kind)).toEqual(["header", "ndjson", "footer"]);
    expect(parsed.footer?.exitCode).toBe(21);
  });
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `bun test src/apex/job-log.test.ts` — Expected: FAIL (`Cannot find module './job-log.js'`).

- [ ] **Step 3: Implement**

Create `src/apex/job-log.ts`:
```ts
// Parser for Apex's per-job `job.log` artifact — the only log source (there is no
// RobotLogs API). The file interleaves the adapter's own lines, the bot's NDJSON log
// lines and raw Python crash output, so each line is classified and normalized to the
// shared JobLog row that the digest/filter code reads. Pure: no I/O.

import type { JobLog } from "../orchestrator/types.js";
import { isRecord, safeJsonParse } from "../shared/util.js";

export type LineKind =
  | "header"
  | "footer"
  | "adapter"
  | "adapter-result"
  | "ndjson"
  | "traceback"
  | "plain";

export interface ApexLogRow extends JobLog {
  kind: LineKind;
  fields?: Record<string, unknown>; // the bot's NDJSON `fields` object, when it sent one
}

// The footer is the log's true last line; it carries the run's own verdict flags.
export interface JobLogFooter {
  exitCode: number | null; // `None` in the log -> null
  timedOut: boolean;
  cancelled: boolean;
  suspended: boolean;
}

// `[apex-adapter] result=<R> reason=<reason|-> exit=<n> outcomes=<csv|->`
export interface AdapterResult {
  result: string;
  reason: string | null;
  exit: number | null;
  outcomes: string[];
}

export interface ParsedJobLog {
  rows: ApexLogRow[];
  footer: JobLogFooter | null;
  adapterResult: AdapterResult | null;
}

const HEADER_RE = /^=== apex job \S+ started\b/;
const FOOTER_RE = /^=== apex job \S+ finished:(.*)===$/;
const RESULT_RE = /^\[apex-adapter\] result=(\S+) reason=(.*?) exit=(\S+) outcomes=(\S*)$/;
const TRACEBACK_RE = /^Traceback \(most recent call last\):/;

const LEVELS: Record<string, string> = {
  INFO: "Info",
  WARN: "Warn",
  WARNING: "Warn",
  ERROR: "Error",
  DEBUG: "Debug",
};

// Unknown levels survive verbatim (capitalized) rather than being guessed at.
function levelOf(raw: unknown): string {
  if (typeof raw !== "string" || !raw) return "Info";
  const up = raw.toUpperCase();
  return LEVELS[up] ?? up.charAt(0) + up.slice(1).toLowerCase();
}

const flag = (body: string, key: string): boolean => new RegExp(`\\b${key}=True\\b`).test(body);

function footerOf(line: string): JobLogFooter | null {
  const body = FOOTER_RE.exec(line)?.[1];
  if (body === undefined) return null;
  const exit = /\bexit_code=(-?\d+)\b/.exec(body)?.[1];
  return {
    exitCode: exit === undefined ? null : Number(exit),
    timedOut: flag(body, "timed_out"),
    cancelled: flag(body, "cancelled"),
    suspended: flag(body, "suspended"),
  };
}

function adapterResultOf(line: string): AdapterResult | null {
  const m = RESULT_RE.exec(line);
  if (!m) return null;
  const [, result = "", reason = "-", exit = "-", outcomes = "-"] = m;
  return {
    result,
    reason: !reason || reason === "-" ? null : reason,
    exit: /^-?\d+$/.test(exit) ? Number(exit) : null,
    outcomes: outcomes === "-" ? [] : outcomes.split(",").filter(Boolean),
  };
}

// A bot NDJSON line: a JSON object with a string `message`. Anything else that merely
// starts with "{" (truncated, not JSON, a JSON dump without `message`) is a plain line.
function ndjsonRow(line: string): ApexLogRow | null {
  if (!line.startsWith("{")) return null;
  const obj = safeJsonParse(line);
  if (!isRecord(obj) || typeof obj["message"] !== "string") return null;
  const fields = obj["fields"];
  const stamp = obj["timestamp"];
  return {
    Level: levelOf(obj["level"]),
    Message: obj["message"],
    TimeStamp: typeof stamp === "string" ? stamp : "",
    kind: "ndjson",
    ...(isRecord(fields) ? { fields } : {}),
  };
}

const textRow = (kind: LineKind, line: string, Level = "Info"): ApexLogRow => ({
  Level,
  Message: line,
  TimeStamp: "",
  kind,
});

// Only NDJSON lines carry a timestamp. Untimed lines inherit the previous row's, so a
// gap between two timed rows is never blamed on an untimed line between them; leading
// untimed rows (header, early adapter lines) take the first timestamp that follows.
// A log with no timestamp at all keeps "".
function inheritTimestamps(rows: ApexLogRow[]): void {
  let last = rows.find((r) => r.TimeStamp)?.TimeStamp ?? "";
  for (const row of rows) {
    if (row.TimeStamp) last = row.TimeStamp;
    else row.TimeStamp = last;
  }
}

export function parseJobLog(text: string): ParsedJobLog {
  const rows: ApexLogRow[] = [];
  let footer: JobLogFooter | null = null;
  let adapterResult: AdapterResult | null = null;
  let inTraceback = false;

  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trimEnd();
    if (!line) continue;

    // A crash is reported at Error so the digest and minLevel filters catch it. The
    // block is the Traceback line, its indented frames, then one unindented exception line.
    if (inTraceback || TRACEBACK_RE.test(line)) {
      rows.push(textRow("traceback", line, "Error"));
      inTraceback = inTraceback ? /^\s/.test(line) : true;
      continue;
    }

    const parsedFooter = footerOf(line);
    if (parsedFooter) {
      footer = parsedFooter;
      rows.push(textRow("footer", line));
      continue;
    }
    const result = adapterResultOf(line);
    if (result) {
      adapterResult = result;
      rows.push(textRow("adapter-result", line));
      continue;
    }
    if (HEADER_RE.test(line)) rows.push(textRow("header", line));
    else if (line.startsWith("[apex-adapter]")) rows.push(textRow("adapter", line));
    else rows.push(ndjsonRow(line) ?? textRow("plain", line));
  }

  inheritTimestamps(rows);
  return { rows, footer, adapterResult };
}
```

- [ ] **Step 4: Run tests** — `bun test src/apex/job-log.test.ts` → PASS (19 tests).
- [ ] **Step 5: Full gate** — `bun run typecheck && bun test && bunx biome check src` → all green
- [ ] **Step 6: Commit**
```bash
git add src/apex/job-log.ts src/apex/job-log.test.ts
git commit -m "feat(apex): parse job.log artifacts into log rows, footer and adapter result" -m "Apex has no RobotLogs API: the only log source is one job.log per job that interleaves adapter lines, bot NDJSON and Python tracebacks. The pure parser normalizes it to the shared JobLog row so the generic digest/filter code applies unchanged."
```

---

### Task 10: Apex typed endpoints (`api.ts`) and pure mappers (`map.ts`)

**Files:**
- Create: `src/apex/api.ts` — raw row types + endpoint functions
- Create: `src/apex/map.ts` — label maps, terminal set, details types, mappers, `apexFault`
- Create: `src/apex/test-rows.ts` — synthetic row builders shared by the Apex tests
- Test: `src/apex/api.test.ts`, `src/apex/map.test.ts`

**Interfaces:**
- Consumes: `apexRequest`, `apexPages`, `apexDownload` (Task 7 `client.ts`); `ApexApiError` (`errors.ts`); `ExpectedError`; `useApexStub`, `json`, `FOLDER_IDS` (Task 7 `test-support.ts`); `JobCore`, `QueueItemCore`, `QueueCore`, `ProcessCore`, `TriggerCore`, `JobFault`, `JobAttempt`, `QueueItemStatus` (`orchestrator/types.ts`); `JobLogDigest` (`orchestrator/log-digest.ts`); `normalizeError` (`orchestrator/faults.ts`); `ParsedJobLog`, `AdapterResult` (Task 9).
- Produces (contract names): raw types `ApexJobRow`, `ApexJobDetail`, `ApexQueue`, `ApexQueueItem`, `ApexQueueItemDetail`, `ApexWorkflow`, `ApexSchedule`, `ApexArtifact`; filters `JobFilters`, `ItemFilters`; `listJobs(env, folder, f, max)`, `getJob(env, folder, id)`, `listWorkflows(env, folder)`, `listQueues(env, folder)`, `listQueueItems(env, folder, queueId, f, max)`, `getQueueItem(env, folder, queueId, itemId)`, `listSchedules(env, folder)`, `listArtifacts(env, folder, jobId)`, `downloadArtifact(env, a, range?)` — `folder` is `string | undefined`, a folder NAME override (undefined = the env's configured folder); `getJob`/`getQueueItem` return the row and throw `ApexApiError` on 404 (`isNotFound(e)` tells); plus `getQueue`, `isNotFound`. From `map.ts`: `APEX_JOB_LABELS`, `APEX_ITEM_LABELS` (`Readonly<Record<string,string>>`), `APEX_TERMINAL_STATUSES` (`ReadonlySet<string>`), `ApexJobDetails`, `ApexItemDetails`, `ApexRunSummary`, `ApexLogDigest = JobLogDigest & { run: ApexRunSummary }`, `toApexJobCore(row | detail, link)`, `toApexItemCore(item, attempts, includeContent, queueName)`, `toApexQueueCore`, `toApexProcessCore`, `toApexQueueTriggers(queues)`, `toApexScheduleTrigger`, `apexFault(job): JobFault | null`, plus `jobLabel`, `toApexJobAttempt`, `attemptsFromItem`, `toApexRunSummary`, `toApexItemStatus`.

Decisions this task pins (each has a test): filters are limited to the verified set and mapped camelCase → snake_case (`createdAfter` → `created_after`, …) with `sort_by=created_at&sort_dir=desc` always sent — Apex silently ignores unknown params and returns everything, so no free-form passthrough exists; `undefined` filters are omitted. Label maps are the dashboard bundle's verbatim (item `in_progress` → `InProgress`, UiPath's spelling). `neverStarted = terminal && started_at === null`. A list row's detail-only fields (`outcome`, `exitCode`, `needsReview`, `effectiveTimeoutSeconds`) are `null` = "not fetched", and `result`/`input` are absent; a detail row carries `result` (= `result_payload`) and `input` (= `input_payload` minus `token` and `callbackContext`). `apexFault`: `null` for `completed`; list row → `{message: null, signature: null, class: error_class}` when `error_class` is set, else `null`; detail → `null` while non-terminal, else `message = error_message ?? outcome_label ?? "Job ended in status <status>"`, `class = outcome ?? error_class`, `signature = "apex:" + normalizeError("<class ?? unclassified>: <message>")`. Item `details` also carries `memberId`/`member` (`MemberID`/`MemberFullName` from the content, `""` when absent or not a string — UiPath parity for triage), populated even when content is not requested. Item `exceptionType` is `business`/`application` or `null`; `specificContent` is `null` unless requested and then has `token` = `"[redacted]"`.

- [ ] **Step 1: Write the failing tests**

Create `src/apex/test-rows.ts` (synthetic builders; the tests below and Tasks 11/12 import them):
```ts
// Synthetic, PHI-free Apex row builders for tests (shapes mirror the live keys in api.ts).

import type {
  ApexArtifact,
  ApexJobDetail,
  ApexJobRow,
  ApexQueue,
  ApexQueueItem,
  ApexQueueItemDetail,
  ApexSchedule,
  ApexWorkflow,
} from "./api.js";

export const JOB_ID = "aaaaaaaa-0000-4000-8000-000000000001";
export const WORKFLOW_ID = "bbbbbbbb-0000-4000-8000-000000000001";
export const QUEUE_ID = "cccccccc-0000-4000-8000-000000000001";
export const ITEM_ID = "dddddddd-0000-4000-8000-000000000001";

export const jobRow = (o: Partial<ApexJobRow> = {}): ApexJobRow => ({
  id: JOB_ID,
  status: "completed",
  workflow_id: WORKFLOW_ID,
  folder_id: "22222222-2222-4222-8222-222222222222",
  source: "sqs",
  queue_item_id: ITEM_ID,
  queue_id: QUEUE_ID,
  queue_item_reference: "1000000001",
  workflow_name: "demo-auth-submit",
  queue_name: "DEMO auth submit queue",
  credential_name: "demo-cred",
  retry_of_job_id: null,
  attempt_number: 1,
  worker_id: "eeeeeeee-0000-4000-8000-000000000001",
  error_class: null,
  workflow_version: "1.0.2",
  callback_status: "sent",
  started_at: "2026-10-08T00:39:51.248748Z",
  completed_at: "2026-10-08T00:44:14.649417Z",
  created_at: "2026-10-08T00:39:49.044176Z",
  duration_seconds: 263.400669,
  ...o,
});

export const jobDetail = (o: Partial<ApexJobDetail> = {}): ApexJobDetail => ({
  ...jobRow(),
  input_payload: {
    orderUid: "order-1",
    token: "synthetic-token",
    callbackContext: { orderUid: "order-1" },
    retryCount: 0,
  },
  result_payload: { result: "approved" },
  error_message: null,
  exit_code: 0,
  outcome: null,
  outcome_label: null,
  needs_review: false,
  effective_timeout_seconds: 900,
  ...o,
});

export const queueRow = (o: Partial<ApexQueue> = {}): ApexQueue => ({
  id: QUEUE_ID,
  name: "DEMO auth submit queue",
  workflow_id: WORKFLOW_ID,
  is_active: true,
  auto_drain: true,
  max_concurrent_jobs: 1,
  max_retries: 0,
  enforce_unique_reference: false,
  ...o,
});

export const itemRow = (o: Partial<ApexQueueItem> = {}): ApexQueueItem => ({
  id: ITEM_ID,
  queue_id: QUEUE_ID,
  reference: "1000000001",
  specific_content: {
    orderUid: "order-1",
    token: "synthetic-token",
    IsApproved: true,
    MemberFirstName: "Jane",
    MemberLastName: "Doe",
  },
  match_value: "auto-1",
  priority: 20,
  status: "failed",
  exception_type: "business",
  retry_count: 0,
  parent_item_id: null,
  job_id: JOB_ID,
  created_at: "2026-10-08T00:29:26.164862Z",
  ended_at: "2026-10-08T00:44:14.655419Z",
  ...o,
});

export const itemDetail = (o: Partial<ApexQueueItemDetail> = {}): ApexQueueItemDetail => ({
  ...itemRow(),
  callback_status: "sent",
  lineage: [],
  ...o,
});

export const workflowRow = (o: Partial<ApexWorkflow> = {}): ApexWorkflow => ({
  id: WORKFLOW_ID,
  name: "demo-auth-submit",
  package_name: "demo-auth-submit",
  is_active: true,
  timeout_seconds: 900,
  max_attempts: 3,
  backoff_strategy: "exponential",
  artifact_policy: "on_failure",
  current_version: "1.0.2",
  ...o,
});

export const scheduleRow = (o: Partial<ApexSchedule> = {}): ApexSchedule => ({
  id: "ffffffff-0000-4000-8000-000000000001",
  workflow_id: WORKFLOW_ID,
  cron_expression: "0 6 * * *",
  timezone: "UTC",
  is_paused: false,
  ...o,
});

export const artifactRow = (o: Partial<ApexArtifact> = {}): ApexArtifact => {
  const id = o.id ?? "99999999-0000-4000-8000-000000000001";
  return {
    id,
    filename: "job.log",
    artifact_type: "logs",
    size_bytes: 100,
    download_url: `/api/v1/jobs/${JOB_ID}/artifacts/${id}`,
    ...o,
  };
};
```

Create `src/apex/map.test.ts`:
```ts
import { describe, expect, test } from "bun:test";
import { QUEUE_ITEM_STATUSES } from "../orchestrator/types.js";
import { parseJobLog } from "./job-log.js";
import {
  APEX_ITEM_LABELS,
  APEX_JOB_LABELS,
  APEX_TERMINAL_STATUSES,
  apexFault,
  attemptsFromItem,
  toApexItemCore,
  toApexItemStatus,
  toApexJobAttempt,
  toApexJobCore,
  toApexProcessCore,
  toApexQueueCore,
  toApexQueueTriggers,
  toApexRunSummary,
  toApexScheduleTrigger,
} from "./map.js";
import {
  itemDetail,
  itemRow,
  jobDetail,
  jobRow,
  queueRow,
  scheduleRow,
  workflowRow,
} from "./test-rows.js";

// The status lists the dashboard bundle's label maps (`Jr`, `$r`) enumerate.
const BUNDLE_JOB_STATUSES = [
  "queued",
  "claimed",
  "running",
  "completed",
  "failed",
  "cancelled",
  "cancelling",
  "lost",
  "timed_out",
  "suspended",
  "resumed",
];
const BUNDLE_ITEM_STATUSES = [
  "new",
  "in_progress",
  "successful",
  "failed",
  "retried",
  "abandoned",
  "deleted",
];

describe("label maps", () => {
  test("cover exactly the dashboard's job and item statuses", () => {
    expect(Object.keys(APEX_JOB_LABELS).sort()).toEqual([...BUNDLE_JOB_STATUSES].sort());
    expect(Object.keys(APEX_ITEM_LABELS).sort()).toEqual([...BUNDLE_ITEM_STATUSES].sort());
  });

  test("terminal statuses are exactly completed/failed/cancelled/lost/timed_out", () => {
    expect([...APEX_TERMINAL_STATUSES].sort()).toEqual(
      ["cancelled", "completed", "failed", "lost", "timed_out"].sort(),
    );
  });

  test("item labels are UiPath's QueueItemStatus spellings, and the wire map inverts them", () => {
    expect(Object.values(APEX_ITEM_LABELS).sort()).toEqual([...QUEUE_ITEM_STATUSES].sort());
    for (const [wire, label] of Object.entries(APEX_ITEM_LABELS)) {
      expect(toApexItemStatus(label as (typeof QUEUE_ITEM_STATUSES)[number])).toBe(wire);
    }
  });
});

describe("toApexJobCore", () => {
  test("every status maps to its label and terminal flag", () => {
    for (const status of BUNDLE_JOB_STATUSES) {
      const core = toApexJobCore(jobRow({ status }), "");
      expect(core.state).toBe(APEX_JOB_LABELS[status] ?? "");
      expect(core.rawState).toBe(status);
      expect(core.terminal).toBe(APEX_TERMINAL_STATUSES.has(status));
    }
  });

  test("an unknown status passes through verbatim and is not terminal", () => {
    const core = toApexJobCore(jobRow({ status: "paused_by_ops" }), "");
    expect(core).toMatchObject({ state: "paused_by_ops", terminal: false });
  });

  test("shapes ids, timing, retry fields and link", () => {
    const core = toApexJobCore(
      jobRow({ attempt_number: 2, retry_of_job_id: "prev-job" }),
      "https://x/jobs/1",
    );
    expect(core).toMatchObject({
      orchestrator: "apex",
      process: { id: jobRow().workflow_id, name: "demo-auth-submit", version: "1.0.2" },
      queueItem: {
        queueId: jobRow().queue_id,
        itemId: jobRow().queue_item_id,
        reference: "1000000001",
      },
      createdAt: "2026-10-08T00:39:49.044176Z",
      startedAt: "2026-10-08T00:39:51.248748Z",
      endedAt: "2026-10-08T00:44:14.649417Z",
      durationMs: 263401,
      attempt: 2,
      retryOfJobId: "prev-job",
      link: "https://x/jobs/1",
    });
  });

  test("a manual job (no queue item) has queueItem null", () => {
    const core = toApexJobCore(jobRow({ queue_item_id: null, source: "manual" }), "");
    expect(core.queueItem).toBeNull();
    expect(core.details.source).toBe("manual");
  });

  test("terminal + never started => neverStarted; a queued job with no start is not", () => {
    const reaped = jobRow({
      status: "failed",
      started_at: null,
      error_class: "missing_credential",
    });
    expect(toApexJobCore(reaped, "").details.neverStarted).toBe(true);
    expect(
      toApexJobCore(jobRow({ status: "queued", started_at: null }), "").details.neverStarted,
    ).toBe(false);
    expect(toApexJobCore(jobRow({ status: "failed" }), "").details.neverStarted).toBe(false);
  });

  test("a list row leaves detail-only fields null/absent; a detail fills them", () => {
    const row = toApexJobCore(jobRow({ status: "failed", error_class: "permanent" }), "");
    expect(row.details).toMatchObject({
      outcome: null,
      exitCode: null,
      needsReview: null,
      effectiveTimeoutSeconds: null,
      errorClass: "permanent",
    });
    expect("result" in row.details || "input" in row.details).toBe(false);

    const full = toApexJobCore(
      jobDetail({
        status: "failed",
        exit_code: 21,
        outcome: "business_failure",
        needs_review: true,
      }),
      "",
    );
    expect(full.details).toMatchObject({
      outcome: "business_failure",
      exitCode: 21,
      needsReview: true,
      effectiveTimeoutSeconds: 900,
      result: { result: "approved" },
    });
  });

  test("input drops token and callbackContext but keeps everything else", () => {
    const { details } = toApexJobCore(jobDetail(), "");
    expect(details.input).toEqual({ orderUid: "order-1", retryCount: 0 });
  });

  test("a null input_payload yields no input key", () => {
    expect("input" in toApexJobCore(jobDetail({ input_payload: null }), "").details).toBe(false);
  });

  test("durationMs is null when the job never ran", () => {
    expect(toApexJobCore(jobRow({ duration_seconds: null }), "").durationMs).toBeNull();
  });
});

describe("apexFault", () => {
  const failed = (o: Parameters<typeof jobDetail>[0]) => jobDetail({ status: "failed", ...o });

  test("null for completed and for a still-running detail", () => {
    expect(apexFault(jobDetail({ status: "completed", error_message: "ignored" }))).toBeNull();
    expect(apexFault(jobDetail({ status: "running", error_message: "x" }))).toBeNull();
  });

  test("message prefers error_message over outcome_label; class prefers outcome over error_class", () => {
    const fault = apexFault(
      failed({
        error_message: "referred provider not found",
        outcome_label: "Business failure",
        outcome: "business_failure",
        error_class: "permanent",
      }),
    );
    expect(fault).toMatchObject({
      message: "referred provider not found",
      class: "business_failure",
    });
  });

  test("falls back to outcome_label, error_class, then a status headline", () => {
    expect(
      apexFault(failed({ error_message: null, outcome_label: "Callback rejected" }))?.message,
    ).toBe("Callback rejected");
    expect(apexFault(failed({ outcome: null, error_class: "transient" }))?.class).toBe("transient");
    const bare = apexFault(failed({ error_message: null, outcome_label: null, error_class: null }));
    expect(bare).toMatchObject({ message: "Job ended in status failed", class: null });
    expect(bare?.signature).toBe("apex:unclassified: job ended in status failed");
  });

  test("signature is apex:-prefixed and stable across ids and timestamps", () => {
    const sig = (msg: string) => apexFault(failed({ outcome: "x", error_message: msg }))?.signature;
    const a = sig("job aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee failed at 2026-10-08T00:44:14Z");
    const b = sig("job 11111111-2222-3333-4444-555555555555 failed at 2026-10-09T09:00:00Z");
    expect(a).toStartWith("apex:");
    expect(a).toBe(b);
  });

  test("a list row carries only the class", () => {
    expect(apexFault(jobRow({ status: "failed", error_class: "needs_human" }))).toEqual({
      message: null,
      signature: null,
      class: "needs_human",
    });
    expect(apexFault(jobRow({ status: "failed", error_class: null }))).toBeNull();
    expect(apexFault(jobRow({ status: "completed", error_class: "permanent" }))).toBeNull();
  });
});

describe("toApexJobAttempt / toApexRunSummary", () => {
  test("attempt carries label and neverStarted", () => {
    expect(toApexJobAttempt(jobRow({ status: "failed", started_at: null }))).toEqual({
      orchestrator: "apex",
      id: jobRow().id,
      state: "Faulted",
      neverStarted: true,
    });
  });

  test("the log footer outranks the detail; the detail covers a missing footer", () => {
    const detail = jobDetail({
      status: "failed",
      exit_code: 1,
      outcome: "crash",
      error_class: "permanent",
    });
    const withFooter = parseJobLog(
      "=== apex job x finished: exit_code=21 timed_out=True cancelled=False suspended=False ===",
    );
    expect(toApexRunSummary(detail, withFooter)).toEqual({
      exitCode: 21,
      outcome: "crash",
      errorClass: "permanent",
      timedOut: true,
      cancelled: false,
      adapterResult: null,
    });
    const noFooter = toApexRunSummary({ ...detail, status: "timed_out" }, parseJobLog("plain"));
    expect(noFooter).toMatchObject({ exitCode: 1, timedOut: true, cancelled: false });
  });
});

describe("toApexItemCore", () => {
  test("labels the status, keeps the raw one, and extracts orderUid", () => {
    const core = toApexItemCore(
      itemRow({ status: "in_progress" }),
      [],
      false,
      "DEMO auth submit queue",
    );
    expect(core).toMatchObject({
      orchestrator: "apex",
      queueName: "DEMO auth submit queue",
      status: "InProgress",
      rawStatus: "in_progress",
      orderUid: "order-1",
      exceptionType: "business",
      createdAt: "2026-10-08T00:29:26.164862Z",
      endedAt: "2026-10-08T00:44:14.655419Z",
    });
  });

  test("retry fields land in details", () => {
    const core = toApexItemCore(
      itemRow({ retry_count: 2, parent_item_id: "parent-1", priority: 10, match_value: "auto-9" }),
      [],
      false,
      "q",
    );
    expect(core.details).toEqual({
      retryCount: 2,
      parentItemId: "parent-1",
      priority: 10,
      matchValue: "auto-9",
      memberId: "",
      member: "",
    });
  });

  test("memberId and member come from the content even when content is not requested", () => {
    const sc = { orderUid: "o", MemberID: "M-1", MemberFullName: "Doe, Jane", token: "t" };
    const core = toApexItemCore(itemRow({ specific_content: sc }), [], false, "q");
    expect(core.specificContent).toBeNull();
    expect(core.details).toMatchObject({ memberId: "M-1", member: "Doe, Jane" });
    const odd = toApexItemCore(itemRow({ specific_content: { MemberID: 7 } }), [], false, "q");
    expect(odd.details).toMatchObject({ memberId: "", member: "" });
  });

  test("content is null unless requested, and the token is redacted when it is", () => {
    expect(toApexItemCore(itemRow(), [], false, "q").specificContent).toBeNull();
    const sc = toApexItemCore(itemRow(), [], true, "q").specificContent;
    expect(sc?.["token"]).toBe("[redacted]");
    expect(sc?.["orderUid"]).toBe("order-1");
  });

  test("an unexpected exception_type and a missing orderUid degrade to null", () => {
    const core = toApexItemCore(
      itemRow({ exception_type: "weird", specific_content: { orderUid: 5 } }),
      [],
      false,
      "q",
    );
    expect(core).toMatchObject({ exceptionType: null, orderUid: null });
  });

  test("a detail adds callback status and a camelCased lineage with job state", () => {
    const core = toApexItemCore(
      itemDetail({
        callback_status: "failed",
        lineage: [
          {
            item_id: "i1",
            reference: "r1",
            status: "retried",
            exception_type: null,
            job: { id: "j1", status: "cancelled", error_class: "transient" },
          },
          { item_id: "i2", reference: "r2", status: "new", exception_type: null, job: null },
        ],
      }),
      [],
      false,
      "q",
    );
    expect(core.details.callbackStatus).toBe("failed");
    expect(core.details.lineage).toEqual([
      {
        itemId: "i1",
        reference: "r1",
        status: "Retried",
        exceptionType: null,
        jobId: "j1",
        jobStatus: "Stopped",
        errorClass: "transient",
      },
      {
        itemId: "i2",
        reference: "r2",
        status: "New",
        exceptionType: null,
        jobId: null,
        jobStatus: null,
        errorClass: null,
      },
    ]);
  });

  test("attemptsFromItem exposes only the latest job id, state unknown", () => {
    expect(attemptsFromItem(itemRow())).toEqual([
      {
        orchestrator: "apex",
        id: itemRow().job_id ?? "",
        state: null,
        neverStarted: null,
      },
    ]);
    expect(attemptsFromItem(itemRow({ job_id: null }))).toEqual([]);
  });
});

describe("queues, processes, triggers", () => {
  test("queue core", () => {
    expect(toApexQueueCore(queueRow({ is_active: false, max_retries: 3 }))).toMatchObject({
      name: "DEMO auth submit queue",
      processId: queueRow().workflow_id,
      active: false,
      details: {
        autoDrain: true,
        maxConcurrentJobs: 1,
        maxRetries: 3,
        enforceUniqueReference: false,
      },
    });
  });

  test("process core renames the workflow fields", () => {
    expect(toApexProcessCore(workflowRow())).toMatchObject({
      id: workflowRow().id,
      package: "demo-auth-submit",
      version: "1.0.2",
      active: true,
      details: {
        timeoutSeconds: 900,
        maxAttempts: 3,
        backoffStrategy: "exponential",
        artifactPolicy: "on_failure",
      },
    });
  });

  test("a queue trigger is enabled only when active AND auto-draining", () => {
    const [both, paused, noDrain] = toApexQueueTriggers([
      queueRow(),
      queueRow({ is_active: false }),
      queueRow({ auto_drain: false, max_concurrent_jobs: 4 }),
    ]);
    expect(both).toMatchObject({
      kind: "queue",
      enabled: true,
      cron: null,
      queueId: queueRow().id,
      processId: queueRow().workflow_id,
      details: { maxConcurrent: 1 },
    });
    expect(paused?.enabled).toBe(false);
    expect(noDrain).toMatchObject({ enabled: false, details: { maxConcurrent: 4 } });
  });

  test("a schedule becomes a time trigger; paused = disabled", () => {
    expect(toApexScheduleTrigger(scheduleRow())).toMatchObject({
      kind: "time",
      cron: "0 6 * * *",
      enabled: true,
      queueId: null,
      processId: scheduleRow().workflow_id,
    });
    expect(toApexScheduleTrigger(scheduleRow({ is_paused: true })).enabled).toBe(false);
  });
});
```

Create `src/apex/api.test.ts`:
```ts
import { describe, expect, test } from "bun:test";
import {
  downloadArtifact,
  getJob,
  isNotFound,
  listArtifacts,
  listJobs,
  listQueueItems,
} from "./api.js";
import { artifactRow, JOB_ID, jobRow } from "./test-rows.js";
import { FOLDER_IDS, json, useApexStub } from "./test-support.js";

const page = (items: unknown[]) =>
  json({ items, total: items.length, page: 1, page_size: 200, total_pages: 1 });
const query = (path: string) => Object.fromEntries(new URL(`https://x${path}`).searchParams);

describe("filters", () => {
  const stub = useApexStub();

  test("listJobs sends only the verified params, snake_cased, newest first by default", async () => {
    stub.route("GET", "/jobs", () => page([jobRow()]));
    await listJobs(
      "pre_prod",
      undefined,
      {
        status: "failed",
        queueId: "q1",
        workflowId: "w1",
        createdAfter: "2026-10-01T00:00:00Z",
        reference: "r1",
      },
      10,
    );
    const q = query(stub.callsTo("GET", "/jobs")[0]?.path ?? "");
    expect(q).toMatchObject({
      sort_by: "created_at",
      sort_dir: "desc",
      status: "failed",
      queue_id: "q1",
      workflow_id: "w1",
      created_after: "2026-10-01T00:00:00Z",
      reference: "r1",
    });
    expect(Object.keys(q).sort()).toEqual([
      "created_after",
      "page",
      "page_size",
      "queue_id",
      "reference",
      "sort_by",
      "sort_dir",
      "status",
      "workflow_id",
    ]);
  });

  test("undefined filters are omitted, not sent as the string 'undefined'", async () => {
    stub.route("GET", "/jobs", () => page([]));
    await listJobs("pre_prod", undefined, { status: undefined, reference: "r1" }, 10);
    const q = query(stub.callsTo("GET", "/jobs")[0]?.path ?? "");
    expect(q["reference"]).toBe("r1");
    expect("status" in q).toBe(false);
  });

  test("listQueueItems maps status / reference / search", async () => {
    stub.route("GET", "/queues/q1/items", () => page([]));
    await listQueueItems(
      "pre_prod",
      undefined,
      "q1",
      { status: "in_progress", search: "uid-1" },
      10,
    );
    const q = query(stub.callsTo("GET", "/queues/q1/items")[0]?.path ?? "");
    expect(q).toMatchObject({ status: "in_progress", search: "uid-1" });
    expect("reference" in q).toBe(false);
  });
});

describe("folder scoping and downloads", () => {
  const stub = useApexStub();

  test("a folder name override scopes the artifact listing (non-default folders 404 without it)", async () => {
    stub.route("GET", `/jobs/${JOB_ID}/artifacts`, () => json([artifactRow()]));
    await listArtifacts("pre_prod", "Authorization", JOB_ID);
    await listArtifacts("pre_prod", undefined, JOB_ID);
    const [override, byEnv] = stub.callsTo("GET", `/jobs/${JOB_ID}/artifacts`);
    expect(override?.headers["x-folder-id"]).toBe(FOLDER_IDS["Authorization"]);
    expect(byEnv?.headers["x-folder-id"]).toBe(FOLDER_IDS["Authorization Test"]);
  });

  test("a non-list artifact response is an error, not an empty list", async () => {
    stub.route("GET", `/jobs/${JOB_ID}/artifacts`, () => json({ items: [] }));
    await expect(listArtifacts("pre_prod", undefined, JOB_ID)).rejects.toThrow(/not a list/);
  });

  test("downloadArtifact forwards the Range header only when given", async () => {
    const a = artifactRow();
    stub.route(
      "GET",
      a.download_url.replace("/api/v1", ""),
      () => new Response("tail", { status: 206 }),
    );
    await downloadArtifact("pre_prod", a, "bytes=-4");
    await downloadArtifact("pre_prod", a);
    const [ranged, whole] = stub.callsTo("GET", a.download_url.replace("/api/v1", ""));
    expect(ranged?.headers["range"]).toBe("bytes=-4");
    expect(whole?.headers["range"]).toBeUndefined();
  });

  test("getJob 404 is a not-found ApexApiError; a 500 is not", async () => {
    stub.route("GET", "/jobs/gone", () => json({ detail: "Job not found" }, 404));
    stub.route("GET", "/jobs/boom", () => json({ detail: "kaput" }, 500));
    const gone = await getJob("pre_prod", undefined, "gone").catch((e: unknown) => e);
    const boom = await getJob("pre_prod", undefined, "boom").catch((e: unknown) => e);
    expect(isNotFound(gone)).toBe(true);
    expect(isNotFound(boom)).toBe(false);
    expect(isNotFound(new Error("x"))).toBe(false);
  });
});
```

- [ ] **Step 2: Run them to verify they fail**

Run: `bun test src/apex/map.test.ts src/apex/api.test.ts` — Expected: FAIL (`Cannot find module './map.js'` / `'./api.js'`).

- [ ] **Step 3: Implement**

Create `src/apex/api.ts`:
```ts
// Typed Apex endpoint functions over the client chokepoint. Raw row interfaces list
// exactly the live keys (verified 2026-10-11) the mappers read — nothing speculative —
// and filters are limited to the query params verified to filter server-side: Apex
// silently ignores unknown params and returns everything (e.g. `queue_item_id`).
// `folder` is a folder NAME override (undefined = the env's configured folder).

import type { Env } from "../config/config.js";
import { ExpectedError } from "../shared/expected-error.js";
import { apexDownload, apexPages, apexRequest } from "./client.js";
import { ApexApiError } from "./errors.js";

export interface ApexJobRow {
  id: string;
  status: string; // queued|claimed|running|completed|failed|cancelled|cancelling|lost|timed_out|suspended|resumed
  workflow_id: string;
  folder_id: string;
  source: string | null; // sqs | manual | schedule | job
  queue_item_id: string | null; // null on manual jobs
  queue_id: string | null;
  queue_item_reference: string | null;
  workflow_name: string;
  queue_name: string | null;
  credential_name: string | null;
  retry_of_job_id: string | null;
  attempt_number: number;
  worker_id: string | null;
  error_class: string | null; // permanent|transient|missing_credential|needs_human|credential
  workflow_version: string | null;
  callback_status: string | null;
  started_at: string | null; // null = never started (e.g. missing_credential reap)
  completed_at: string | null;
  created_at: string;
  duration_seconds: number | null;
}

// Detail-only keys: the list rows do not carry them.
export interface ApexJobDetail extends ApexJobRow {
  input_payload: Record<string, unknown> | null;
  result_payload: unknown;
  error_message: string | null;
  exit_code: number | null;
  outcome: string | null;
  outcome_label: string | null;
  needs_review: boolean;
  effective_timeout_seconds: number | null;
}

export interface ApexQueue {
  id: string;
  name: string;
  workflow_id: string | null;
  is_active: boolean;
  auto_drain: boolean;
  max_concurrent_jobs: number;
  max_retries: number;
  enforce_unique_reference: boolean;
}

export interface ApexQueueItem {
  id: string;
  queue_id: string;
  reference: string;
  specific_content: Record<string, unknown>;
  match_value: string | null;
  priority: number;
  status: string; // new|in_progress|successful|failed|retried|abandoned|deleted
  exception_type: string | null; // business | application
  retry_count: number;
  parent_item_id: string | null;
  job_id: string | null; // the latest job
  created_at: string;
  ended_at: string | null;
}

export interface ApexLineageRow {
  item_id: string;
  reference: string;
  status: string;
  exception_type: string | null;
  job: { id: string; status: string; error_class: string | null } | null;
}

export interface ApexQueueItemDetail extends ApexQueueItem {
  callback_status: string | null;
  lineage: ApexLineageRow[];
}

export interface ApexWorkflow {
  id: string;
  name: string;
  package_name: string | null;
  is_active: boolean;
  timeout_seconds: number;
  max_attempts: number;
  backoff_strategy: string;
  artifact_policy: string; // always | on_failure | never
  current_version: string | null;
}

export interface ApexSchedule {
  id: string;
  workflow_id: string;
  cron_expression: string;
  timezone: string;
  is_paused: boolean;
}

export interface ApexArtifact {
  id: string;
  filename: string; // "job.log", "trace.zip", "recording.mp4", …
  artifact_type: string; // logs | videos | other
  size_bytes: number;
  download_url: string; // "/api/v1/jobs/{id}/artifacts/{artifact_id}" — Bearer required
}

// ONLY these filter (queue_item_id / search / queue_item_reference are ignored by /jobs).
// `status` is the wire value (queued, failed, …), not the dashboard label.
export interface JobFilters {
  status?: string | undefined;
  queueId?: string | undefined;
  workflowId?: string | undefined;
  createdAfter?: string | undefined;
  reference?: string | undefined; // exact; this is how every attempt of one queue item is enumerated
}

// `status` is the wire value (new, in_progress, …); `search` is fuzzy over the content.
export interface ItemFilters {
  status?: string | undefined;
  reference?: string | undefined;
  search?: string | undefined;
}

const JOB_QUERY: Record<keyof JobFilters, string> = {
  status: "status",
  queueId: "queue_id",
  workflowId: "workflow_id",
  createdAfter: "created_after",
  reference: "reference",
};
const ITEM_QUERY: Record<keyof ItemFilters, string> = {
  status: "status",
  reference: "reference",
  search: "search",
};

function toQuery<F extends object>(f: F, keys: Record<keyof F, string>): Record<string, string> {
  const query: Record<string, string> = {};
  for (const k of Object.keys(keys) as (keyof F)[]) {
    const v = f[k];
    if (typeof v === "string") query[keys[k]] = v;
  }
  return query;
}

// The one 404 a caller may fold into "no such record"; any other status propagates.
export const isNotFound = (e: unknown): boolean => e instanceof ApexApiError && e.status === 404;

const seg = encodeURIComponent;
const BIG = 1000; // directory-sized lists (queues, workflows, schedules) per folder

// Newest first: Apex lists default to created_at desc, pinned here so a default change can't flip `top`.
export const listJobs = (
  env: Env,
  folder: string | undefined,
  f: JobFilters,
  max: number,
): Promise<ApexJobRow[]> =>
  apexPages<ApexJobRow>(
    env,
    "/jobs",
    { sort_by: "created_at", sort_dir: "desc", ...toQuery(f, JOB_QUERY) },
    max,
    folder,
  );

export async function getJob(
  env: Env,
  folder: string | undefined,
  id: string,
): Promise<ApexJobDetail> {
  return (await apexRequest(env, "GET", `/jobs/${seg(id)}`, { folder })) as ApexJobDetail;
}

export const listWorkflows = (env: Env, folder: string | undefined): Promise<ApexWorkflow[]> =>
  apexPages<ApexWorkflow>(env, "/workflows", {}, BIG, folder);

export const listQueues = (env: Env, folder: string | undefined): Promise<ApexQueue[]> =>
  apexPages<ApexQueue>(env, "/queues", {}, BIG, folder);

export async function getQueue(
  env: Env,
  folder: string | undefined,
  queueId: string,
): Promise<ApexQueue> {
  return (await apexRequest(env, "GET", `/queues/${seg(queueId)}`, { folder })) as ApexQueue;
}

// Items list newest first by default (live-verified); the server has no verified sort param.
export const listQueueItems = (
  env: Env,
  folder: string | undefined,
  queueId: string,
  f: ItemFilters,
  max: number,
): Promise<ApexQueueItem[]> =>
  apexPages<ApexQueueItem>(
    env,
    `/queues/${seg(queueId)}/items`,
    toQuery(f, ITEM_QUERY),
    max,
    folder,
  );

export async function getQueueItem(
  env: Env,
  folder: string | undefined,
  queueId: string,
  itemId: string,
): Promise<ApexQueueItemDetail> {
  const path = `/queues/${seg(queueId)}/items/${seg(itemId)}`;
  return (await apexRequest(env, "GET", path, { folder })) as ApexQueueItemDetail;
}

export const listSchedules = (env: Env, folder: string | undefined): Promise<ApexSchedule[]> =>
  apexPages<ApexSchedule>(env, "/schedules", {}, BIG, folder);

// Bare-array route; the listing needs the folder header outside the default folder.
export async function listArtifacts(
  env: Env,
  folder: string | undefined,
  jobId: string,
): Promise<ApexArtifact[]> {
  const rows = await apexRequest(env, "GET", `/jobs/${seg(jobId)}/artifacts`, { folder });
  if (!Array.isArray(rows)) throw new ExpectedError("Apex artifact listing was not a list");
  return rows as ApexArtifact[];
}

// Bearer only (no folder header); `range` is a Range header value, e.g. "bytes=-1048576".
export const downloadArtifact = (env: Env, a: ApexArtifact, range?: string) =>
  apexDownload(env, a.download_url, range ? { range } : undefined);
```

Create `src/apex/map.ts`:
```ts
// Pure Apex -> backend-neutral core mappers, the dashboard label maps (verbatim from the
// dashboard bundle) and the Apex fault. No I/O: the backend fetches, these shape.

import { normalizeError } from "../orchestrator/faults.js";
import type { JobLogDigest } from "../orchestrator/log-digest.js";
import type {
  JobAttempt,
  JobCore,
  JobFault,
  ProcessCore,
  QueueCore,
  QueueItemCore,
  QueueItemStatus,
  TriggerCore,
} from "../orchestrator/types.js";
import type {
  ApexJobDetail,
  ApexJobRow,
  ApexLineageRow,
  ApexQueue,
  ApexQueueItem,
  ApexQueueItemDetail,
  ApexSchedule,
  ApexWorkflow,
} from "./api.js";
import type { AdapterResult, ParsedJobLog } from "./job-log.js";

// Dashboard bundle label maps, verbatim. Item labels use UiPath's spelling (the
// dashboard itself shows "In Progress") because QueueItemStatus is UiPath's vocabulary.
export const APEX_JOB_LABELS: Readonly<Record<string, string>> = {
  queued: "Pending",
  claimed: "Claimed",
  running: "Running",
  completed: "Successful",
  failed: "Faulted",
  cancelled: "Stopped",
  cancelling: "Stopping",
  lost: "Lost",
  timed_out: "Timed Out",
  suspended: "Suspended",
  resumed: "Resumed",
};

export const APEX_ITEM_LABELS: Readonly<Record<string, string>> = {
  new: "New",
  in_progress: "InProgress",
  successful: "Successful",
  failed: "Failed",
  retried: "Retried",
  abandoned: "Abandoned",
  deleted: "Deleted",
};

export const APEX_TERMINAL_STATUSES: ReadonlySet<string> = new Set([
  "completed",
  "failed",
  "cancelled",
  "lost",
  "timed_out",
]);

const ITEM_WIRE: Record<QueueItemStatus, string> = {
  New: "new",
  InProgress: "in_progress",
  Successful: "successful",
  Failed: "failed",
  Retried: "retried",
  Abandoned: "abandoned",
  Deleted: "deleted",
};
export const toApexItemStatus = (s: QueueItemStatus): string => ITEM_WIRE[s];

export const jobLabel = (status: string): string => APEX_JOB_LABELS[status] ?? status;
const itemLabel = (status: string): string => APEX_ITEM_LABELS[status] ?? status;

// ---- jobs ---------------------------------------------------------------------

// Null on a list row means "not fetched" (list rows carry no outcome/exit code), never "absent".
export interface ApexJobDetails {
  errorClass: string | null;
  outcome: string | null;
  outcomeLabel: string | null;
  exitCode: number | null;
  needsReview: boolean | null;
  callbackStatus: string | null;
  credentialName: string | null;
  source: string | null;
  queueName: string | null;
  workerId: string | null;
  folderId: string;
  workflowId: string;
  neverStarted: boolean; // terminal and never claimed by a worker (e.g. missing_credential)
  effectiveTimeoutSeconds: number | null;
  result?: unknown; // result_payload (detail only)
  input?: Record<string, unknown>; // input_payload minus token/callbackContext (detail only)
}

export interface ApexRunSummary {
  exitCode: number | null;
  outcome: string | null;
  errorClass: string | null;
  timedOut: boolean;
  cancelled: boolean;
  adapterResult: AdapterResult | null;
}

export type ApexLogDigest = JobLogDigest & { run: ApexRunSummary };

const isDetail = (j: ApexJobRow | ApexJobDetail): j is ApexJobDetail => "error_message" in j;

const SECRET_INPUT_KEYS = new Set(["token", "callbackContext"]);
const withoutSecrets = (input: Record<string, unknown>): Record<string, unknown> =>
  Object.fromEntries(Object.entries(input).filter(([k]) => !SECRET_INPUT_KEYS.has(k)));

// message = error_message ?? outcome_label; class = outcome ?? error_class. null for a
// completed or still-running job. A list row has only error_class, so it carries the
// class with no message/signature (get_job fetches the detail that has them).
export function apexFault(job: ApexJobRow | ApexJobDetail): JobFault | null {
  if (job.status === "completed") return null;
  if (!isDetail(job)) {
    return job.error_class ? { message: null, signature: null, class: job.error_class } : null;
  }
  if (!APEX_TERMINAL_STATUSES.has(job.status)) return null;
  const message = job.error_message ?? job.outcome_label ?? `Job ended in status ${job.status}`;
  const cls = job.outcome ?? job.error_class;
  return {
    message,
    signature: `apex:${normalizeError(`${cls ?? "unclassified"}: ${message}`)}`,
    class: cls,
  };
}

const neverStarted = (j: ApexJobRow): boolean =>
  APEX_TERMINAL_STATUSES.has(j.status) && j.started_at === null;

export function toApexJobCore(
  row: ApexJobRow | ApexJobDetail,
  link: string,
): JobCore<ApexJobDetails> {
  const detail = isDetail(row) ? row : null;
  return {
    orchestrator: "apex",
    id: row.id,
    state: jobLabel(row.status),
    rawState: row.status,
    terminal: APEX_TERMINAL_STATUSES.has(row.status),
    process: { id: row.workflow_id, name: row.workflow_name, version: row.workflow_version },
    queueItem:
      row.queue_item_id && row.queue_id
        ? {
            queueId: row.queue_id,
            itemId: row.queue_item_id,
            reference: row.queue_item_reference ?? "",
          }
        : null,
    createdAt: row.created_at,
    startedAt: row.started_at,
    endedAt: row.completed_at,
    durationMs: row.duration_seconds === null ? null : Math.round(row.duration_seconds * 1000),
    attempt: row.attempt_number,
    retryOfJobId: row.retry_of_job_id,
    fault: apexFault(row),
    link,
    details: {
      errorClass: row.error_class,
      outcome: detail?.outcome ?? null,
      outcomeLabel: detail?.outcome_label ?? null,
      exitCode: detail?.exit_code ?? null,
      needsReview: detail?.needs_review ?? null,
      callbackStatus: row.callback_status,
      credentialName: row.credential_name,
      source: row.source,
      queueName: row.queue_name,
      workerId: row.worker_id,
      folderId: row.folder_id,
      workflowId: row.workflow_id,
      neverStarted: neverStarted(row),
      effectiveTimeoutSeconds: detail?.effective_timeout_seconds ?? null,
      ...(detail
        ? {
            result: detail.result_payload,
            ...(detail.input_payload ? { input: withoutSecrets(detail.input_payload) } : {}),
          }
        : {}),
    },
  };
}

export const toApexJobAttempt = (row: ApexJobRow | ApexJobDetail): JobAttempt => ({
  orchestrator: "apex",
  id: row.id,
  state: jobLabel(row.status),
  neverStarted: neverStarted(row),
});

// The footer is the run's own verdict; the detail fills in when the log has none.
export function toApexRunSummary(detail: ApexJobDetail, parsed: ParsedJobLog): ApexRunSummary {
  const { footer } = parsed;
  return {
    exitCode: footer ? footer.exitCode : detail.exit_code,
    outcome: detail.outcome,
    errorClass: detail.error_class,
    timedOut: footer ? footer.timedOut : detail.status === "timed_out",
    cancelled: footer ? footer.cancelled : detail.status === "cancelled",
    adapterResult: parsed.adapterResult,
  };
}

// ---- queue items --------------------------------------------------------------

export interface ApexLineageEntry {
  itemId: string;
  reference: string;
  status: string;
  exceptionType: string | null;
  jobId: string | null;
  jobStatus: string | null;
  errorClass: string | null;
}

export interface ApexItemDetails {
  retryCount: number;
  parentItemId: string | null;
  priority: number;
  matchValue: string | null;
  memberId: string; // PHI-light triage projection, as UiPath's list_queue_items; "" when absent
  member: string;
  callbackStatus?: string | null; // detail only
  lineage?: ApexLineageEntry[]; // detail only
}

const toLineage = (l: ApexLineageRow): ApexLineageEntry => ({
  itemId: l.item_id,
  reference: l.reference,
  status: itemLabel(l.status),
  exceptionType: l.exception_type,
  jobId: l.job?.id ?? null,
  jobStatus: l.job ? jobLabel(l.job.status) : null,
  errorClass: l.job?.error_class ?? null,
});

// The item names only its latest job; its state is unknown without fetching the job.
export const attemptsFromItem = (item: ApexQueueItem): JobAttempt[] =>
  item.job_id ? [{ orchestrator: "apex", id: item.job_id, state: null, neverStarted: null }] : [];

const str = (v: unknown): string => (typeof v === "string" ? v : "");

const redactToken = (sc: Record<string, unknown>): Record<string, unknown> =>
  Object.fromEntries(Object.entries(sc).map(([k, v]) => [k, k === "token" ? "[redacted]" : v]));

// `queueName` is the owning queue's name — an item row carries only the queue id.
export function toApexItemCore(
  item: ApexQueueItem | ApexQueueItemDetail,
  attempts: JobAttempt[],
  includeContent: boolean,
  queueName: string,
): QueueItemCore<ApexItemDetails> {
  const detail = "lineage" in item ? item : null;
  const orderUid = item.specific_content["orderUid"];
  return {
    orchestrator: "apex",
    queueId: item.queue_id,
    queueName,
    itemId: item.id,
    reference: item.reference,
    orderUid: typeof orderUid === "string" ? orderUid : null,
    status: itemLabel(item.status),
    rawStatus: item.status,
    exceptionType:
      item.exception_type === "business" || item.exception_type === "application"
        ? item.exception_type
        : null,
    createdAt: item.created_at,
    endedAt: item.ended_at,
    jobs: attempts,
    specificContent: includeContent ? redactToken(item.specific_content) : null,
    details: {
      retryCount: item.retry_count,
      parentItemId: item.parent_item_id,
      priority: item.priority,
      matchValue: item.match_value,
      memberId: str(item.specific_content["MemberID"]),
      member: str(item.specific_content["MemberFullName"]),
      ...(detail
        ? { callbackStatus: detail.callback_status, lineage: detail.lineage.map(toLineage) }
        : {}),
    },
  };
}

// ---- queues, processes, triggers ----------------------------------------------

export interface ApexQueueDetails {
  autoDrain: boolean;
  maxConcurrentJobs: number;
  maxRetries: number;
  enforceUniqueReference: boolean;
}

export const toApexQueueCore = (q: ApexQueue): QueueCore<ApexQueueDetails> => ({
  orchestrator: "apex",
  id: q.id,
  name: q.name,
  processId: q.workflow_id,
  active: q.is_active,
  details: {
    autoDrain: q.auto_drain,
    maxConcurrentJobs: q.max_concurrent_jobs,
    maxRetries: q.max_retries,
    enforceUniqueReference: q.enforce_unique_reference,
  },
});

export interface ApexProcessDetails {
  timeoutSeconds: number;
  maxAttempts: number;
  backoffStrategy: string;
  artifactPolicy: string;
}

export const toApexProcessCore = (w: ApexWorkflow): ProcessCore<ApexProcessDetails> => ({
  orchestrator: "apex",
  id: w.id,
  name: w.name,
  package: w.package_name,
  version: w.current_version,
  active: w.is_active,
  details: {
    timeoutSeconds: w.timeout_seconds,
    maxAttempts: w.max_attempts,
    backoffStrategy: w.backoff_strategy,
    artifactPolicy: w.artifact_policy,
  },
});

// Apex has no /triggers route (404). A queue that is active and auto-draining is the
// equivalent of an enabled UiPath queue trigger; max_concurrent_jobs is its maxJobsForActivation.
export const toApexQueueTriggers = (
  queues: ApexQueue[],
): TriggerCore<{ maxConcurrent: number }>[] =>
  queues.map((q) => ({
    orchestrator: "apex",
    id: q.id,
    name: q.name,
    kind: "queue",
    processId: q.workflow_id,
    queueId: q.id,
    enabled: q.is_active && q.auto_drain,
    cron: null,
    details: { maxConcurrent: q.max_concurrent_jobs },
  }));

export const toApexScheduleTrigger = (s: ApexSchedule): TriggerCore<{ timezone: string }> => ({
  orchestrator: "apex",
  id: s.id,
  name: `${s.cron_expression} (${s.timezone})`,
  kind: "time",
  processId: s.workflow_id,
  queueId: null,
  enabled: !s.is_paused,
  cron: s.cron_expression,
  details: { timezone: s.timezone },
});
```

- [ ] **Step 4: Run tests** — `bun test src/apex/map.test.ts src/apex/api.test.ts` → PASS (30 + 7 tests).
- [ ] **Step 5: Full gate** — `bunx biome check --write src/apex && bun run typecheck && bun test && bunx biome check src` → all green
- [ ] **Step 6: Commit**
```bash
git add src/apex/api.ts src/apex/map.ts src/apex/test-rows.ts src/apex/api.test.ts src/apex/map.test.ts
git commit -m "feat(apex): typed endpoints and pure core mappers" -m "Raw rows list only the live keys the mappers read; filters are the verified set because Apex silently ignores unknown query params. Mappers shape Apex jobs/items/queues/workflows into the backend-neutral cores, with the dashboard's own label maps and an apex:-prefixed fault signature."
```

---

### Task 11: Apex read functions (`backend.ts`)

**Files:**
- Create: `src/apex/backend.ts` — the nine read functions plus `readJobLog` (Tasks 15/16/17 append to this module; it is NOT assembled or registered here)
- Create: `src/apex/read-support.ts` — pure selection helpers used by `backend.ts`
- Test: `src/apex/read-support.test.ts`, `src/apex/backend.test.ts`

**Interfaces:**
- Consumes: Task 10 (`api.ts`, `map.ts`, `test-rows.ts`), Task 9 (`parseJobLog`), Task 7 (`resolveFolderId`, `apexDashboardLink`, `useApexStub`, `json`, `ApexCall`), `OrchestratorBackend` arg/result types (`orchestrator/backend.ts`), `digestLogs`/`truncate` (`orchestrator/log-digest.ts`), `isFailureLog` (`orchestrator/log-semantics.ts`), `guardQueueItemSafety` (`orchestrator/safety.ts`), `memberFilename` and `toPortalRef` from `src/orchestrator/portal.ts` (**created in Task 3; consumed here — do not move or redefine them**), `resolvePortal`/`normalizeQueueName` (`mcp/reference.ts`), `chunk`/`StepProgress` (`shared/util.ts`).
- Produces: `apexListJobs`, `apexGetJobs`, `apexGetJobLogs`, `apexListQueues`, `apexListQueueItems`, `apexFindOrderQueueItems`, `apexPullQueueItem`, `apexListProcesses`, `apexListTriggers` — each with exactly the signature of the same-named `OrchestratorBackend` method (`listJobs`, `getJobs`, `getJobLogs`, `listQueues`, `listQueueItems`, `findOrderQueueItems`, `pullQueueItem`, `listProcesses`, `listTriggers`); `readJobLog(env: Env, folder: string | undefined, jobId: string): Promise<ParsedJobLog | null>`; from `read-support.ts`: `pickQueue`, `filterLogRows`, `toLogRow`, `logsUnavailableBeforeFetch`, `ARTIFACT_POLICY_UNAVAILABLE`, `videoPart`, `mergeNewestFirst`, `withinSince`, `JOB_LOG`.

Behaviors pinned by the tests below:
- **`apexListJobs`**: `processName` → case-insensitive substring over `/workflows` → one `/jobs?workflow_id=<id>` per match (a verified filter), merged newest first and cut to `top`; no match → `[]` with no `/jobs` request; `since` → `created_after`; thin rows (`fault.class = error_class`, no detail fan-out).
- **`apexGetJobs`**: detail GETs run in parallel; a 404 is `found:false` in both modes; any other failure throws for a single id and lands on `entry.error` in a batch. `includeLogDigest` → artifact listing → `job.log` → `parseJobLog` → `digestLogs` + `run` (`ApexLogDigest`). `logsUnavailable.reason`: `running` (non-terminal, no artifact request), `never-started` (terminal with `started_at === null`, no artifact request), `artifact-policy` (terminal, started, no `job.log` listed). A failing log read is `logDigestError`; the job still returns.
- **`apexGetJobLogs`**: whole log fetched, then filtered client-side — `minLevel` `warn` ⇒ `Warn|Error|Fatal|Critical`, `error` ⇒ `Error|Fatal|Critical`; `contains` case-sensitive; `onlyFailures` via `isFailureLog`; `tail` last N of the matches; `totalMatching` = match count before `tail`, `truncated` always `false`; messages cut to 400 chars unless `fullMessages`; `includeRawFields` ⇒ `raw = fields ?? null`; `includeVideo` ⇒ `VideoRef` from the listing (`artifact` = the listed filename, e.g. `recording.mp4`) or `videoError`.
- **`apexListQueueItems`**: `queue` = portal key/alias → the queue named `<KEY|alias> auth <submit|sync> queue` (case-insensitive; active preferred on a repeated name), a queue id, or a full queue name; no match → `ExpectedError` listing the folder's queue names; `status` mapped to the Apex wire value (`InProgress` → `in_progress`); rows carry no `specificContent`, `jobs` = the item's latest job id with `state: null`.
- **`apexFindOrderQueueItems`**: all folder queues (including `Unrouted` and inactive ones), `items?search=<orderUid>` per queue at concurrency 6 (`chunk`), exact `specific_content.orderUid` match only, token `"[redacted]"`, `since` applied client-side on `created_at` (an empty window falls back to the unbounded matches plus a note, as UiPath does), newest first cut to `top`, per item `/jobs?reference=<item.reference>` filtered to that item's `queue_item_id` → `JobAttempt[]` newest first (fallback: the item's own `job_id`), `searchMode: "search"`, `scanned` = rows examined.
- **`apexPullQueueItem`**: needs `env` + `queueId` + `itemId` (`ExpectedError` otherwise, and for `url`/`folderId`); `IsApproved` forced `false` through `guardQueueItemSafety(…, "force")`, token kept; portal via `resolvePortal(queueName)`; `suggestedFilename` from `memberFilename`; a failed queue-name lookup is `queueNameError`, not a failed pull.
- **`apexListTriggers`**: queues → `kind:"queue"` triggers; schedules → `kind:"time"`; a `queueId` filter keeps that queue's trigger only and skips schedules.

- [ ] **Step 1: Write the failing tests**

Create `src/apex/read-support.test.ts`:
```ts
import { describe, expect, test } from "bun:test";
import type { ApexLogRow } from "./job-log.js";
import {
  filterLogRows,
  logsUnavailableBeforeFetch,
  mergeNewestFirst,
  pickQueue,
  toLogRow,
  videoPart,
  withinSince,
} from "./read-support.js";
import { artifactRow, jobRow, queueRow } from "./test-rows.js";

const row = (Level: string, Message: string, extra: Partial<ApexLogRow> = {}): ApexLogRow => ({
  Level,
  Message,
  TimeStamp: "t",
  kind: "ndjson",
  ...extra,
});

describe("pickQueue", () => {
  const queues = [
    queueRow({ id: "q-pmg", name: "PMG auth submit queue" }),
    queueRow({ id: "q-pmg-sync", name: "PMG auth sync queue" }),
    queueRow({ id: "q-spmg", name: "Spmg auth submit queue" }),
    queueRow({ id: "q-test", name: "CenCal shared-adapter test queue", is_active: false }),
    queueRow({ id: "q-unrouted", name: "Unrouted" }),
  ];

  test("portal key, case-insensitive, picks the submit or sync queue by kind", () => {
    expect(pickQueue(queues, "pmg", "submit")?.id).toBe("q-pmg");
    expect(pickQueue(queues, "PMG", "sync")?.id).toBe("q-pmg-sync");
  });

  test("a portal alias resolves to the key's queue", () => {
    expect(pickQueue(queues, "sdpmg", "submit")?.id).toBe("q-spmg");
  });

  test("a queue id or a full queue name matches directly", () => {
    expect(pickQueue(queues, "q-unrouted", "submit")?.id).toBe("q-unrouted");
    expect(pickQueue(queues, "cencal shared-adapter test queue", "submit")?.id).toBe("q-test");
  });

  test("an unknown portal, or a kind with no queue, is null", () => {
    expect(pickQueue(queues, "nope", "submit")).toBeNull();
    expect(pickQueue(queues, "spmg", "sync")).toBeNull();
  });

  test("prefers an active queue when the name repeats", () => {
    const dup = [
      queueRow({ id: "old", name: "PMG auth submit queue", is_active: false }),
      queueRow({ id: "new", name: "Pmg auth submit queue" }),
    ];
    expect(pickQueue(dup, "PMG", "submit")?.id).toBe("new");
  });
});

describe("filterLogRows", () => {
  const rows = [
    row("Info", "login ok"),
    row("Warn", "slow page"),
    row("Error", "row not found"),
    row("Debug", "dom dump"),
    row("Info", "request failed with 500"),
    row("Fatal", "kaput"),
  ];

  test("minLevel warn keeps Warn and above; error keeps Error and above", () => {
    expect(
      filterLogRows(rows, { minLevel: "warn", onlyFailures: false }).kept.map((r) => r.Level),
    ).toEqual(["Warn", "Error", "Fatal"]);
    expect(
      filterLogRows(rows, { minLevel: "error", onlyFailures: false }).kept.map((r) => r.Level),
    ).toEqual(["Error", "Fatal"]);
  });

  test("contains is case-sensitive", () => {
    expect(filterLogRows(rows, { contains: "page", onlyFailures: false }).kept).toHaveLength(1);
    expect(filterLogRows(rows, { contains: "PAGE", onlyFailures: false }).kept).toHaveLength(0);
  });

  test("onlyFailures catches failure wording at Info level", () => {
    const kept = filterLogRows(rows, { onlyFailures: true }).kept.map((r) => r.Message);
    expect(kept).toEqual(["row not found", "request failed with 500", "kaput"]);
  });

  test("tail keeps the last N matches but totalMatching counts them all", () => {
    const out = filterLogRows(rows, { minLevel: "warn", onlyFailures: false, tail: 2 });
    expect(out.kept.map((r) => r.Message)).toEqual(["row not found", "kaput"]);
    expect(out.totalMatching).toBe(3);
  });
});

describe("toLogRow", () => {
  const r = row("Info", "x".repeat(500), { fields: { ms: 1 } });

  test("truncates unless fullMessages; raw only when requested", () => {
    expect(toLogRow(r, false, false).Message).toHaveLength(401);
    expect(toLogRow(r, true, false).Message).toHaveLength(500);
    expect("raw" in toLogRow(r, false, false)).toBe(false);
  });

  test("raw is the NDJSON fields, or null when the row had none", () => {
    expect(toLogRow(r, false, true).raw).toEqual({ ms: 1 });
    expect(toLogRow(row("Info", "m"), false, true).raw).toBeNull();
  });
});

describe("logsUnavailableBeforeFetch", () => {
  test("a job in flight => running", () => {
    for (const status of ["queued", "claimed", "running", "cancelling", "suspended", "resumed"]) {
      expect(logsUnavailableBeforeFetch(jobRow({ status, started_at: null }))?.reason).toBe(
        "running",
      );
    }
  });

  test("terminal with no start => never-started (running wins for in-flight ones)", () => {
    const j = jobRow({ status: "failed", started_at: null, error_class: "missing_credential" });
    const out = logsUnavailableBeforeFetch(j);
    expect(out?.reason).toBe("never-started");
    expect(out?.detail).toContain("missing_credential");
  });

  test("terminal and started => nothing known up front", () => {
    expect(logsUnavailableBeforeFetch(jobRow({ status: "failed" }))).toBeNull();
    expect(logsUnavailableBeforeFetch(jobRow({ status: "timed_out" }))).toBeNull();
  });
});

describe("videoPart", () => {
  test("points at the videos artifact by filename", () => {
    const v = artifactRow({ filename: "recording.mp4", artifact_type: "videos", size_bytes: 9 });
    expect(videoPart([artifactRow(), v])).toEqual({
      video: {
        kind: "artifact",
        artifact: "recording.mp4",
        sizeBytes: 9,
        fetchWith: "get_job_artifacts",
      },
    });
  });

  test("no video => videoError", () => {
    expect(videoPart([artifactRow()]).videoError).toMatch(/no video/);
  });
});

describe("mergeNewestFirst / withinSince", () => {
  test("merges lists newest first and cuts to top", () => {
    const a = jobRow({ id: "a", created_at: "2026-10-08T00:00:03Z" });
    const b = jobRow({ id: "b", created_at: "2026-10-08T00:00:05Z" });
    const c = jobRow({ id: "c", created_at: "2026-10-08T00:00:04Z" });
    expect(mergeNewestFirst([[a], [b, c]], 2).map((j) => j.id)).toEqual(["b", "c"]);
  });

  const rows = [{ created_at: "2026-10-08T00:00:00Z" }, { created_at: "2026-10-01T00:00:00Z" }];

  test("keeps rows at or after since", () => {
    expect(withinSince(rows, "2026-10-05T00:00:00Z")).toEqual({
      rows: rows.slice(0, 1),
      note: null,
    });
  });

  test("an empty window falls back to the unbounded rows with a note", () => {
    const out = withinSince(rows, "2026-11-01T00:00:00Z");
    expect(out.rows).toEqual(rows);
    expect(out.note).toContain("without the time bound");
  });

  test("no rows and no since are passed through; a bad date is rejected", () => {
    expect(withinSince([], "2026-11-01T00:00:00Z")).toEqual({ rows: [], note: null });
    expect(withinSince(rows, undefined).rows).toEqual(rows);
    expect(() => withinSince(rows, "yesterday")).toThrow(/not a valid ISO date/);
  });
});
```

Create `src/apex/backend.test.ts`:
```ts
import { describe, expect, test } from "bun:test";
import { ExpectedError } from "../shared/expected-error.js";
import {
  apexFindOrderQueueItems,
  apexGetJobLogs,
  apexGetJobs,
  apexListJobs,
  apexListProcesses,
  apexListQueueItems,
  apexListQueues,
  apexListTriggers,
  apexPullQueueItem,
} from "./backend.js";
import {
  artifactRow,
  ITEM_ID,
  itemDetail,
  itemRow,
  JOB_ID,
  jobDetail,
  jobRow,
  QUEUE_ID,
  queueRow,
  scheduleRow,
  workflowRow,
} from "./test-rows.js";
import { type ApexCall, json, useApexStub } from "./test-support.js";

const page = (items: unknown[]) =>
  json({ items, total: items.length, page: 1, page_size: 200, total_pages: 1 });
const query = (c: ApexCall | undefined): Record<string, string> =>
  Object.fromEntries(new URL(`https://x${c?.path ?? ""}`).searchParams);

const T = "2026-10-08T00:40:00.000Z";
const nd = (o: Record<string, unknown>): string =>
  JSON.stringify({ v: 1, portal: "demo", timestamp: T, ...o });
const FOOTER = (exit: number) =>
  `=== apex job ${JOB_ID} finished: exit_code=${exit} timed_out=False cancelled=False suspended=False ===`;
const LOG = [
  `=== apex job ${JOB_ID} started (workflow=demo runtime=python) ===`,
  nd({ level: "INFO", message: "login ok" }),
  nd({ level: "WARN", message: "slow page", fields: { ms: 5200 } }),
  nd({ level: "ERROR", message: "row not found" }),
  nd({ level: "INFO", message: "x".repeat(500) }),
  "[apex-adapter] result=FAILED reason=business_failure exit=21 outcomes=-",
  FOOTER(21),
].join("\n");

const ART = artifactRow();
const artPath = (a = ART) => a.download_url.replace("/api/v1", "");

// Routes for one job: detail, artifact listing, and the job.log download. The more specific
// routes are registered last (the stub takes the last matching prefix).
function routeJob(
  stub: ReturnType<typeof useApexStub>,
  detail: ReturnType<typeof jobDetail>,
  artifacts: ReturnType<typeof artifactRow>[] = [ART],
  log = LOG,
): void {
  stub.route("GET", `/jobs/${detail.id}`, () => json(detail));
  stub.route("GET", `/jobs/${detail.id}/artifacts`, () => json(artifacts));
  stub.route("GET", artPath(), () => new Response(log, { status: 200 }));
}

describe("apexListJobs", () => {
  const stub = useApexStub();

  test("without processName: one newest-first request, since -> created_after, cores mapped", async () => {
    stub.route("GET", "/jobs", () =>
      page([jobRow({ status: "failed", error_class: "permanent" })]),
    );
    const out = await apexListJobs({ env: "pre_prod", since: "2026-10-01T00:00:00Z", top: 5 });
    const calls = stub.callsTo("GET", "/jobs");
    expect(calls).toHaveLength(1);
    expect(query(calls[0])).toMatchObject({
      created_after: "2026-10-01T00:00:00Z",
      sort_by: "created_at",
      sort_dir: "desc",
    });
    expect("workflow_id" in query(calls[0])).toBe(false);
    expect(out.folder).toBe("Authorization Test");
    expect(out.jobs[0]).toMatchObject({
      state: "Faulted",
      link: `https://apex.example.com/jobs/${JOB_ID}`,
      fault: { class: "permanent", message: null },
    });
  });

  test("processName matches workflows by case-insensitive substring and merges newest first", async () => {
    stub.route("GET", "/workflows", () =>
      page([
        workflowRow({ id: "wf-a", name: "demo-auth-submit" }),
        workflowRow({ id: "wf-b", name: "other-thing" }),
        workflowRow({ id: "wf-c", name: "DEMO-eligibility" }),
      ]),
    );
    stub.route("GET", "/jobs", (c) => {
      const wf = query(c)["workflow_id"];
      return page(
        wf === "wf-a"
          ? [
              jobRow({ id: "a2", created_at: "2026-10-08T00:00:05Z" }),
              jobRow({ id: "a1", created_at: "2026-10-08T00:00:01Z" }),
            ]
          : [jobRow({ id: "c1", created_at: "2026-10-08T00:00:03Z" })],
      );
    });
    const out = await apexListJobs({ env: "pre_prod", top: 2, processName: "DEMO" });
    expect(
      stub
        .callsTo("GET", "/jobs")
        .map((c) => query(c)["workflow_id"])
        .sort(),
    ).toEqual(["wf-a", "wf-c"]);
    expect(out.jobs.map((j) => j.id)).toEqual(["a2", "c1"]);
  });

  test("a processName that matches no workflow lists nothing and queries no jobs", async () => {
    stub.route("GET", "/workflows", () => page([workflowRow({ name: "demo-auth-submit" })]));
    const out = await apexListJobs({ env: "pre_prod", top: 5, processName: "zzz" });
    expect(out.jobs).toEqual([]);
    expect(stub.callsTo("GET", "/jobs")).toHaveLength(0);
  });
});

describe("apexGetJobs", () => {
  const stub = useApexStub();
  const args = {
    env: "pre_prod" as const,
    jobIds: [JOB_ID],
    batch: false,
    includeLogDigest: false,
  };

  test("a 404 is found:false, not an error", async () => {
    stub.route("GET", `/jobs/${JOB_ID}`, () => json({ detail: "Job not found" }, 404));
    const out = await apexGetJobs(args);
    expect(out.entries).toEqual([{ id: JOB_ID, found: false }]);
  });

  test("a non-404 failure throws for a single id but lands on the entry in a batch", async () => {
    stub.route("GET", "/jobs/bad", () => json({ detail: "kaput" }, 500));
    stub.route("GET", `/jobs/${JOB_ID}`, () => json(jobDetail()));
    await expect(apexGetJobs({ ...args, jobIds: ["bad"] })).rejects.toThrow(/kaput/);
    const out = await apexGetJobs({ ...args, jobIds: ["bad", JOB_ID], batch: true });
    expect(out.entries[0]).toMatchObject({
      id: "bad",
      found: false,
      error: expect.stringContaining("kaput"),
    });
    expect(out.entries[1]).toMatchObject({ id: JOB_ID, found: true });
  });

  test("includeLogDigest: digests the parsed job.log and summarizes the run from its footer", async () => {
    routeJob(stub, jobDetail({ status: "failed", outcome: "business_failure", exit_code: 21 }));
    const [entry] = (await apexGetJobs({ ...args, includeLogDigest: true })).entries;
    expect(entry?.logsUnavailable).toBeUndefined();
    expect(entry?.logDigest).toMatchObject({
      byLevel: { Info: 5, Warn: 1, Error: 1 },
      run: { exitCode: 21, outcome: "business_failure", timedOut: false, cancelled: false },
    });
    expect(entry?.logDigest?.failures.map((f) => f.message)).toContain("row not found");
    expect(entry?.job?.fault?.signature).toStartWith("apex:");
  });

  test("a running job has no log yet: reason running, no artifact request", async () => {
    stub.route("GET", `/jobs/${JOB_ID}`, () =>
      json(jobDetail({ status: "running", completed_at: null })),
    );
    const [entry] = (await apexGetJobs({ ...args, includeLogDigest: true })).entries;
    expect(entry?.logsUnavailable?.reason).toBe("running");
    expect(stub.callsTo("GET", `/jobs/${JOB_ID}/artifacts`)).toHaveLength(0);
  });

  test("a never-started job has no artifacts: reason never-started, no artifact request", async () => {
    const reaped = jobDetail({
      status: "failed",
      started_at: null,
      error_class: "missing_credential",
    });
    stub.route("GET", `/jobs/${JOB_ID}`, () => json(reaped));
    const [entry] = (await apexGetJobs({ ...args, includeLogDigest: true })).entries;
    expect(entry?.logsUnavailable?.reason).toBe("never-started");
    expect(entry?.job?.details).toMatchObject({ neverStarted: true });
    expect(stub.callsTo("GET", `/jobs/${JOB_ID}/artifacts`)).toHaveLength(0);
  });

  test("a finished job with no job.log artifact: reason artifact-policy", async () => {
    routeJob(stub, jobDetail(), [artifactRow({ filename: "bot.log", artifact_type: "other" })]);
    const [entry] = (await apexGetJobs({ ...args, includeLogDigest: true })).entries;
    expect(entry?.logsUnavailable?.reason).toBe("artifact-policy");
    expect(entry?.logDigest).toBeUndefined();
  });

  test("a failing log download is logDigestError and the job still returns", async () => {
    routeJob(stub, jobDetail());
    stub.route("GET", artPath(), () => json({ detail: "forbidden" }, 403));
    const [entry] = (await apexGetJobs({ ...args, includeLogDigest: true })).entries;
    expect(entry?.found).toBe(true);
    expect(entry?.logDigestError).toContain("forbidden");
    expect(entry?.logDigest).toBeUndefined();
  });

  test("progress is reported once per job", async () => {
    stub.route("GET", "/jobs/", () => json(jobDetail()));
    const seen: number[] = [];
    await apexGetJobs({ ...args, jobIds: ["j1", "j2", "j3"], batch: true }, (done) =>
      seen.push(done),
    );
    expect(seen.sort()).toEqual([1, 2, 3]);
  });
});

describe("apexGetJobLogs", () => {
  const stub = useApexStub();
  const base = {
    env: "pre_prod" as const,
    jobIds: [JOB_ID],
    batch: false,
    filter: { onlyFailures: false },
    fullMessages: false,
    includeRawFields: false,
    includeVideo: false,
  };

  test("returns every row of the whole log, truncating messages unless fullMessages", async () => {
    routeJob(stub, jobDetail({ status: "failed" }));
    const [entry] = (await apexGetJobLogs(base)).entries;
    expect(entry).toMatchObject({ jobId: JOB_ID, returned: 7, totalMatching: 7, truncated: false });
    const long = entry?.logs.find((l) => l.Message.startsWith("xxx"));
    expect(long?.Message).toHaveLength(401);
    const full = (await apexGetJobLogs({ ...base, fullMessages: true })).entries[0];
    expect(full?.logs.find((l) => l.Message.startsWith("xxx"))?.Message).toHaveLength(500);
  });

  test("minLevel, contains and tail narrow client-side; totalMatching is the pre-tail count", async () => {
    routeJob(stub, jobDetail({ status: "failed" }));
    const warn = (
      await apexGetJobLogs({ ...base, filter: { onlyFailures: false, minLevel: "warn" } })
    ).entries[0];
    expect(warn?.logs.map((l) => l.Level)).toEqual(["Warn", "Error"]);
    const err = (
      await apexGetJobLogs({ ...base, filter: { onlyFailures: false, minLevel: "error" } })
    ).entries[0];
    expect(err?.logs.map((l) => l.Message)).toEqual(["row not found"]);
    const tail = (
      await apexGetJobLogs({ ...base, filter: { onlyFailures: false, minLevel: "warn", tail: 1 } })
    ).entries[0];
    expect(tail).toMatchObject({ returned: 1, totalMatching: 2 });
    const has = (
      await apexGetJobLogs({ ...base, filter: { onlyFailures: false, contains: "slow" } })
    ).entries[0];
    expect(has?.logs.map((l) => l.Message)).toEqual(["slow page"]);
  });

  test("onlyFailures keeps error rows and failure wording (adapter result, traceback)", async () => {
    routeJob(stub, jobDetail({ status: "failed" }));
    const out = (await apexGetJobLogs({ ...base, filter: { onlyFailures: true } })).entries[0];
    const msgs = out?.logs.map((l) => l.Message) ?? [];
    expect(msgs).toContain("row not found");
    expect(msgs.some((m) => m.includes("result=FAILED"))).toBe(true);
    expect(msgs).not.toContain("login ok");
  });

  test("includeRawFields returns the NDJSON fields (null when none)", async () => {
    routeJob(stub, jobDetail({ status: "failed" }));
    const out = (await apexGetJobLogs({ ...base, includeRawFields: true })).entries[0];
    expect(out?.logs.find((l) => l.Message === "slow page")?.raw).toEqual({ ms: 5200 });
    expect(out?.logs.find((l) => l.Message === "login ok")?.raw).toBeNull();
  });

  test("includeVideo points at the recording artifact; absent => videoError", async () => {
    const video = artifactRow({
      id: "v1",
      filename: "recording.mp4",
      artifact_type: "videos",
      size_bytes: 77,
    });
    routeJob(stub, jobDetail({ status: "failed" }), [ART, video]);
    const withVideo = (await apexGetJobLogs({ ...base, includeVideo: true })).entries[0];
    expect(withVideo?.video).toEqual({
      kind: "artifact",
      artifact: "recording.mp4",
      sizeBytes: 77,
      fetchWith: "get_job_artifacts",
    });
    routeJob(stub, jobDetail({ status: "failed" }));
    const without = (await apexGetJobLogs({ ...base, includeVideo: true })).entries[0];
    expect(without?.video).toBeUndefined();
    expect(without?.videoError).toMatch(/no video/);
  });

  test("a running job returns no rows, reason running, and explains the video too", async () => {
    stub.route("GET", `/jobs/${JOB_ID}`, () =>
      json(jobDetail({ status: "running", completed_at: null })),
    );
    const out = (await apexGetJobLogs({ ...base, includeVideo: true })).entries[0];
    expect(out).toMatchObject({ logs: [], returned: 0, logsUnavailable: { reason: "running" } });
    expect(out?.videoError).toBeTruthy();
  });

  test("a missing job throws for a single id, lands on the entry in a batch", async () => {
    stub.route("GET", `/jobs/${JOB_ID}`, () => json({ detail: "Job not found" }, 404));
    await expect(apexGetJobLogs(base)).rejects.toThrow(/not found/i);
    const out = await apexGetJobLogs({ ...base, batch: true });
    expect(out.entries[0]).toMatchObject({
      jobId: JOB_ID,
      logs: [],
      error: expect.stringContaining("not found"),
    });
  });
});

describe("queues, processes, triggers", () => {
  const stub = useApexStub();

  test("listQueues filters by name (case-insensitive), keeps inactive queues, cuts to top", async () => {
    stub.route("GET", "/queues", () =>
      page([
        queueRow({ id: "q1", name: "PMG auth submit queue" }),
        queueRow({ id: "q2", name: "CenCal shared-adapter test queue", is_active: false }),
        queueRow({ id: "q3", name: "pmg auth sync queue" }),
      ]),
    );
    const all = await apexListQueues({ env: "pre_prod", top: 10, nameContains: "PMG" });
    expect(all.map((q) => q.id)).toEqual(["q1", "q3"]);
    expect(
      (await apexListQueues({ env: "pre_prod", top: 10, nameContains: "test" }))[0]?.active,
    ).toBe(false);
    expect(await apexListQueues({ env: "pre_prod", top: 1 })).toHaveLength(1);
  });

  test("listProcesses filters workflows by name and renames fields", async () => {
    stub.route("GET", "/workflows", () =>
      page([workflowRow({ name: "demo-auth-submit" }), workflowRow({ id: "w2", name: "other" })]),
    );
    const out = await apexListProcesses({ env: "pre_prod", top: 10, nameContains: "DEMO" });
    expect(out).toHaveLength(1);
    expect(out[0]).toMatchObject({ package: "demo-auth-submit", version: "1.0.2" });
  });

  test("listTriggers: queues as queue triggers plus schedules as time triggers", async () => {
    stub.route("GET", "/queues", () =>
      page([queueRow({ id: "q1" }), queueRow({ id: "q2", auto_drain: false })]),
    );
    stub.route("GET", "/schedules", () => page([scheduleRow()]));
    const out = await apexListTriggers({ env: "pre_prod", top: 10 });
    expect(out.map((t) => [t.kind, t.enabled])).toEqual([
      ["queue", true],
      ["queue", false],
      ["time", true],
    ]);
  });

  test("a queueId filter keeps that queue's trigger only and skips schedules", async () => {
    stub.route("GET", "/queues", () => page([queueRow({ id: "q1" }), queueRow({ id: "q2" })]));
    stub.route("GET", "/schedules", () => page([scheduleRow()]));
    const out = await apexListTriggers({ env: "pre_prod", top: 10, queueId: "q2" });
    expect(out.map((t) => t.id)).toEqual(["q2"]);
    expect(stub.callsTo("GET", "/schedules")).toHaveLength(0);
  });
});

describe("apexListQueueItems", () => {
  const stub = useApexStub();
  const queues = [
    queueRow({ id: "q-pmg", name: "PMG auth submit queue" }),
    queueRow({ id: "q-pmg-sync", name: "PMG auth sync queue" }),
  ];

  test("resolves the portal's queue, maps the status to its wire value, and omits content", async () => {
    stub.route("GET", "/queues?", () => page(queues));
    stub.route("GET", "/queues/q-pmg/items", () => page([itemRow({ status: "in_progress" })]));
    const out = await apexListQueueItems({
      env: "pre_prod",
      queue: "pmg",
      queueKind: "submit",
      status: "InProgress",
      top: 5,
    });
    expect(query(stub.callsTo("GET", "/queues/q-pmg/items")[0])["status"]).toBe("in_progress");
    expect(out).toMatchObject({ queueId: "q-pmg", queueName: "PMG auth submit queue" });
    expect(out.items[0]).toMatchObject({
      status: "InProgress",
      rawStatus: "in_progress",
      specificContent: null,
      jobs: [{ id: JOB_ID, state: null }],
    });
  });

  test("queueKind sync picks the sync queue; no status sends no status param", async () => {
    stub.route("GET", "/queues?", () => page(queues));
    stub.route("GET", "/queues/q-pmg-sync/items", () => page([]));
    await apexListQueueItems({ env: "pre_prod", queue: "PMG", queueKind: "sync", top: 5 });
    const call = stub.callsTo("GET", "/queues/q-pmg-sync/items")[0];
    expect("status" in query(call)).toBe(false);
  });

  test("an unknown queue is an ExpectedError listing what exists", async () => {
    stub.route("GET", "/queues?", () => page(queues));
    const err = await apexListQueueItems({
      env: "pre_prod",
      queue: "nope",
      queueKind: "submit",
      top: 5,
    }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ExpectedError);
    expect((err as Error).message).toContain("PMG auth submit queue");
  });
});

describe("apexFindOrderQueueItems", () => {
  const stub = useApexStub();
  const args = {
    env: "pre_prod" as const,
    orderUid: "order-1",
    top: 10,
    includeSpecificContent: true,
  };
  const exact = itemRow({ id: "i-new", reference: "R2", created_at: "2026-10-08T12:00:00Z" });
  const older = itemRow({ id: "i-old", reference: "R1", created_at: "2026-10-07T12:00:00Z" });
  const fuzzy = itemRow({
    id: "i-fuzzy",
    reference: "R9",
    specific_content: { orderUid: "order-10" },
  });

  function routeFind(queueCount = 2): void {
    const queues = Array.from({ length: queueCount }, (_, i) =>
      queueRow({ id: `q${i}`, name: i === 0 ? "PMG auth submit queue" : `Q${i}` }),
    );
    stub.route("GET", "/queues?", () => page(queues));
    stub.route("GET", "/queues/", () => page([])); // every other queue has no hits
    stub.route("GET", "/queues/q0/items", () => page([exact, fuzzy, older]));
    stub.route("GET", "/jobs", (c) => {
      const ref = query(c)["reference"];
      return page(
        ref === "R2"
          ? [
              jobRow({
                id: "j-retry",
                queue_item_id: "i-new",
                attempt_number: 2,
                status: "failed",
              }),
              jobRow({
                id: "j-first",
                queue_item_id: "i-new",
                attempt_number: 1,
                status: "cancelled",
              }),
              jobRow({ id: "j-other-item", queue_item_id: "someone-else" }),
            ]
          : [jobRow({ id: "j-old", queue_item_id: "i-old" })],
      );
    });
  }

  test("searches every queue, keeps only the exact orderUid, newest first, token redacted", async () => {
    routeFind(8);
    const out = await apexFindOrderQueueItems(args);
    for (let i = 0; i < 8; i++) {
      expect(query(stub.callsTo("GET", `/queues/q${i}/items`)[0])["search"]).toBe("order-1");
    }
    expect(out.items.map((i) => i.itemId)).toEqual(["i-new", "i-old"]);
    expect(out.items[0]?.specificContent?.["token"]).toBe("[redacted]");
    expect(out).toMatchObject({
      searchMode: "search",
      scanned: 3,
      notes: [],
      folder: "Authorization Test",
    });
    expect(out.items[0]?.queueName).toBe("PMG auth submit queue");
  });

  test("jobs[] are the item's own attempts from /jobs?reference, newest first", async () => {
    routeFind();
    const out = await apexFindOrderQueueItems(args);
    expect(out.items[0]?.jobs.map((j) => [j.id, j.state])).toEqual([
      ["j-retry", "Faulted"],
      ["j-first", "Stopped"],
    ]);
  });

  test("falls back to the item's latest job id when the reference listing has none of its jobs", async () => {
    routeFind();
    stub.route("GET", "/jobs", () => page([]));
    const out = await apexFindOrderQueueItems(args);
    expect(out.items[0]?.jobs).toEqual([
      { orchestrator: "apex", id: JOB_ID, state: null, neverStarted: null },
    ]);
  });

  test("includeSpecificContent=false drops the content", async () => {
    routeFind();
    const out = await apexFindOrderQueueItems({ ...args, includeSpecificContent: false });
    expect(out.items.every((i) => i.specificContent === null)).toBe(true);
  });

  test("top keeps the newest matches", async () => {
    routeFind();
    const out = await apexFindOrderQueueItems({ ...args, top: 1 });
    expect(out.items.map((i) => i.itemId)).toEqual(["i-new"]);
  });

  test("since bounds created_at; an empty window returns everything with a note", async () => {
    routeFind();
    const bounded = await apexFindOrderQueueItems({ ...args, since: "2026-10-08T00:00:00Z" });
    expect(bounded.items.map((i) => i.itemId)).toEqual(["i-new"]);
    expect(bounded.notes).toEqual([]);
    const empty = await apexFindOrderQueueItems({ ...args, since: "2026-11-01T00:00:00Z" });
    expect(empty.items).toHaveLength(2);
    expect(empty.notes[0]).toContain("without the time bound");
  });

  test("no exact match: empty items, scanned still counts the fuzzy rows, no job lookups", async () => {
    routeFind();
    const out = await apexFindOrderQueueItems({ ...args, orderUid: "order-1x" });
    expect(out).toMatchObject({ items: [], scanned: 3 });
    expect(stub.callsTo("GET", "/jobs")).toHaveLength(0);
  });
});

describe("apexPullQueueItem", () => {
  const stub = useApexStub();
  const args = { env: "pre_prod" as const, queueId: QUEUE_ID, itemId: ITEM_ID };

  test("forces IsApproved false, keeps the token, resolves the portal and a filename", async () => {
    stub.route("GET", `/queues/${QUEUE_ID}`, () =>
      json(queueRow({ name: "PMG auth submit queue" })),
    );
    stub.route("GET", `/queues/${QUEUE_ID}/items/${ITEM_ID}`, () =>
      json(itemDetail({ status: "failed" })),
    );
    const out = await apexPullQueueItem(args);
    expect(out).toMatchObject({
      env: "pre_prod",
      isApprovedForced: true,
      suggestedFilename: "jane-doe.yaml",
      portal: { matched: true, key: "PMG" },
      item: { status: "Failed", queueName: "PMG auth submit queue", orderUid: "order-1" },
    });
    expect(out.item.specificContent?.["IsApproved"]).toBe(false);
    expect(out.item.specificContent?.["token"]).toBe("synthetic-token");
    expect(out.queueNameError).toBeUndefined();
  });

  test("a failed queue-name lookup is queueNameError, not a failed pull", async () => {
    stub.route("GET", `/queues/${QUEUE_ID}`, () => json({ detail: "kaput" }, 500));
    stub.route("GET", `/queues/${QUEUE_ID}/items/${ITEM_ID}`, () => json(itemDetail()));
    const out = await apexPullQueueItem(args);
    expect(out.queueNameError).toContain("kaput");
    expect(out.portal.matched).toBe(false);
    expect(out.item.itemId).toBe(ITEM_ID);
  });

  test("requires env + queueId + itemId and rejects UiPath-only args", async () => {
    await expect(apexPullQueueItem({ env: "pre_prod", itemId: ITEM_ID })).rejects.toBeInstanceOf(
      ExpectedError,
    );
    await expect(apexPullQueueItem({ queueId: QUEUE_ID, itemId: ITEM_ID })).rejects.toBeInstanceOf(
      ExpectedError,
    );
    await expect(apexPullQueueItem({ ...args, url: "https://x/y" })).rejects.toBeInstanceOf(
      ExpectedError,
    );
    expect(stub.calls.filter((c) => c.path.startsWith("/queues"))).toHaveLength(0);
  });
});
```

- [ ] **Step 2: Run them to verify they fail**

Run: `bun test src/apex/read-support.test.ts src/apex/backend.test.ts` — Expected: FAIL (`Cannot find module './read-support.js'` / `'./backend.js'`).

- [ ] **Step 3: Implement**

Create `src/apex/read-support.ts`:
```ts
// Pure helpers for the Apex read functions in backend.ts: queue resolution, log row
// filtering/shaping, "why are there no logs" classification and window selection.

import { normalizeQueueName, resolvePortal } from "../mcp/reference.js";
import type {
  JobLogsEntry,
  LogFilter,
  LogRow,
  LogsUnavailable,
  QueueKind,
} from "../orchestrator/backend.js";
import { truncate } from "../orchestrator/log-digest.js";
import { isFailureLog } from "../orchestrator/log-semantics.js";
import { ExpectedError } from "../shared/expected-error.js";
import type { ApexArtifact, ApexJobRow, ApexQueue } from "./api.js";
import type { ApexLogRow } from "./job-log.js";
import { APEX_TERMINAL_STATUSES } from "./map.js";

export const JOB_LOG = "job.log";

// Matching is case-insensitive over the queue name: `<KEY> auth <kind> queue` for the
// portal key and each of its aliases, or the input as a whole name. A queue id wins outright.
export function pickQueue(queues: ApexQueue[], input: string, kind: QueueKind): ApexQueue | null {
  const byId = queues.find((q) => q.id === input);
  if (byId) return byId;
  const portal = resolvePortal(input);
  const keys = portal.matched ? [portal.key, ...portal.aliases] : [normalizeQueueName(input)];
  const wanted = new Set(
    [input, ...keys.map((k) => `${k} auth ${kind} queue`)].map((s) => s.toLowerCase()),
  );
  const hits = queues.filter((q) => wanted.has(q.name.toLowerCase()));
  return hits.find((q) => q.is_active) ?? hits[0] ?? null;
}

const MIN_LEVELS: Record<NonNullable<LogFilter["minLevel"]>, ReadonlySet<string>> = {
  warn: new Set(["Warn", "Error", "Fatal", "Critical"]),
  error: new Set(["Error", "Fatal", "Critical"]),
};

// minLevel, contains (case-sensitive) and onlyFailures narrow the rows; `tail` then keeps the
// last N of what matched. totalMatching is the match count before the tail cut.
export function filterLogRows(
  rows: ApexLogRow[],
  f: LogFilter,
): { kept: ApexLogRow[]; totalMatching: number } {
  const levels = f.minLevel ? MIN_LEVELS[f.minLevel] : null;
  const matched = rows.filter(
    (r) =>
      (!levels || levels.has(r.Level)) &&
      (!f.contains || r.Message.includes(f.contains)) &&
      (!f.onlyFailures || isFailureLog(r)),
  );
  return { kept: f.tail ? matched.slice(-f.tail) : matched, totalMatching: matched.length };
}

// `raw` carries the NDJSON `fields` object (null for rows that had none).
export const toLogRow = (r: ApexLogRow, fullMessages: boolean, includeRaw: boolean): LogRow => ({
  Level: r.Level,
  Message: fullMessages ? r.Message : truncate(r.Message),
  TimeStamp: r.TimeStamp,
  ...(includeRaw ? { raw: r.fields ?? null } : {}),
});

export const ARTIFACT_POLICY_UNAVAILABLE: LogsUnavailable = {
  reason: "artifact-policy",
  detail:
    "the job ended but no job.log artifact was kept (the Process's artifact_policy is on_failure or never)",
};

// Known without any artifact call: a job still in flight has no log yet (the live stream is
// dashboard-only SSE), and a job that never started has no artifacts at all.
export function logsUnavailableBeforeFetch(job: ApexJobRow): LogsUnavailable | null {
  if (!APEX_TERMINAL_STATUSES.has(job.status)) {
    return {
      reason: "running",
      detail: `job is ${job.status}; Apex writes job.log only once the run ends`,
    };
  }
  if (job.started_at === null) {
    return {
      reason: "never-started",
      detail: `job ended without ever starting (error_class=${job.error_class ?? "none"}); it has no artifacts`,
    };
  }
  return null;
}

export function videoPart(artifacts: ApexArtifact[]): Pick<JobLogsEntry, "video" | "videoError"> {
  const v = artifacts.find((a) => a.artifact_type === "videos");
  if (!v) return { videoError: "no video recording artifact on this job" };
  return {
    video: {
      kind: "artifact",
      artifact: v.filename,
      sizeBytes: v.size_bytes,
      fetchWith: "get_job_artifacts",
    },
  };
}

// Merge per-workflow job lists into one newest-first list cut to `top`.
export const mergeNewestFirst = (lists: ApexJobRow[][], top: number): ApexJobRow[] =>
  lists
    .flat()
    .sort((a, b) => b.created_at.localeCompare(a.created_at))
    .slice(0, top);

// `since` bounds created_at client-side (the items route has no verified date filter). When
// nothing falls inside the window the unbounded rows are returned with a note, as UiPath does.
export function withinSince<T extends { created_at: string }>(
  rows: T[],
  since: string | undefined,
): { rows: T[]; note: string | null } {
  if (!since) return { rows, note: null };
  const bound = Date.parse(since);
  if (!Number.isFinite(bound)) throw new ExpectedError(`since '${since}' is not a valid ISO date`);
  const inside = rows.filter((r) => Date.parse(r.created_at) >= bound);
  if (inside.length > 0 || rows.length === 0) return { rows: inside, note: null };
  return {
    rows,
    note: `no queue item matched within since=${since}; returned matches without the time bound.`,
  };
}
```

Create `src/apex/backend.ts`:
```ts
// Apex read functions: one per OrchestratorBackend read method (Task 17 assembles them into
// `apexBackend`). Fetching lives here; shaping is in map.ts and the pure selection logic in
// read-support.ts. Every call is folder-scoped by the client (api.ts `folder` = name override).

import type { Env } from "../config/config.js";
import { resolvePortal } from "../mcp/reference.js";
import type {
  FindOrderArgs,
  FindOrderResult,
  GetJobLogsArgs,
  GetJobLogsResult,
  GetJobsArgs,
  GetJobsResult,
  JobEntry,
  JobLogsEntry,
  ListJobsArgs,
  ListJobsResult,
  ListProcessesArgs,
  ListQueueItemsArgs,
  ListQueueItemsResult,
  ListQueuesArgs,
  ListTriggersArgs,
  PullQueueItemArgs,
  PullQueueItemResult,
  QueueKind,
} from "../orchestrator/backend.js";
import { digestLogs } from "../orchestrator/log-digest.js";
import { memberFilename, toPortalRef } from "../orchestrator/portal.js";
import { guardQueueItemSafety } from "../orchestrator/safety.js";
import type { ProcessCore, QueueCore, QueueItemCore, TriggerCore } from "../orchestrator/types.js";
import { ExpectedError } from "../shared/expected-error.js";
import { chunk, type StepProgress } from "../shared/util.js";
import {
  type ApexArtifact,
  type ApexJobDetail,
  type ApexQueue,
  type ApexQueueItem,
  downloadArtifact,
  getJob,
  getQueue,
  getQueueItem,
  isNotFound,
  type JobFilters,
  listArtifacts,
  listJobs,
  listQueueItems,
  listQueues,
  listSchedules,
  listWorkflows,
} from "./api.js";
import { apexDashboardLink, resolveFolderId } from "./client.js";
import { type ParsedJobLog, parseJobLog } from "./job-log.js";
import {
  type ApexLogDigest,
  attemptsFromItem,
  toApexItemCore,
  toApexItemStatus,
  toApexJobAttempt,
  toApexJobCore,
  toApexProcessCore,
  toApexQueueCore,
  toApexQueueTriggers,
  toApexRunSummary,
  toApexScheduleTrigger,
} from "./map.js";
import {
  ARTIFACT_POLICY_UNAVAILABLE,
  filterLogRows,
  JOB_LOG,
  logsUnavailableBeforeFetch,
  mergeNewestFirst,
  pickQueue,
  toLogRow,
  videoPart,
  withinSince,
} from "./read-support.js";

const FIND_CONCURRENCY = 6;
const SEARCH_PER_QUEUE_MAX = 200; // one page of fuzzy `search` hits per queue
const ATTEMPTS_PER_ITEM_MAX = 50; // max_attempts is 1–10
const MAX_LOG_BYTES = 5 * 1024 * 1024; // a larger job.log is read from its tail

const toMessage = (e: unknown): string => (e instanceof Error ? e.message : String(e));
const jobLink = (id: string): string => apexDashboardLink(`/jobs/${id}`);
const containsFold = (haystack: string, needle: string | undefined): boolean =>
  !needle || haystack.toLowerCase().includes(needle.toLowerCase());

// ---- jobs ---------------------------------------------------------------------

// processName is matched as a substring over the folder's workflows, then each match is
// listed by workflow_id (a verified filter) — one request per matching workflow.
async function workflowFilter(
  env: Env,
  folder: string | undefined,
  processName: string | undefined,
): Promise<(string | undefined)[]> {
  if (!processName) return [undefined];
  const workflows = await listWorkflows(env, folder);
  return workflows.filter((w) => containsFold(w.name, processName)).map((w) => w.id);
}

export async function apexListJobs(args: ListJobsArgs): Promise<ListJobsResult> {
  const { env, folder, since, top, processName } = args;
  const [resolved, workflowIds] = await Promise.all([
    resolveFolderId(env, folder),
    workflowFilter(env, folder, processName),
  ]);
  const base: JobFilters = since ? { createdAfter: since } : {};
  const lists = await Promise.all(
    workflowIds.map((workflowId) => listJobs(env, folder, { ...base, workflowId }, top)),
  );
  const jobs = mergeNewestFirst(lists, top).map((row) => toApexJobCore(row, jobLink(row.id)));
  return { folder: resolved.name, jobs };
}

async function readLogArtifact(env: Env, artifact: ApexArtifact): Promise<ParsedJobLog> {
  const range = artifact.size_bytes > MAX_LOG_BYTES ? `bytes=-${MAX_LOG_BYTES}` : undefined;
  const { bytes } = await downloadArtifact(env, artifact, range);
  return parseJobLog(new TextDecoder().decode(bytes));
}

// The job's parsed job.log, or null when no job.log artifact is listed. Throws on any HTTP failure.
export async function readJobLog(
  env: Env,
  folder: string | undefined,
  jobId: string,
): Promise<ParsedJobLog | null> {
  const artifact = (await listArtifacts(env, folder, jobId)).find((a) => a.filename === JOB_LOG);
  return artifact ? readLogArtifact(env, artifact) : null;
}

type DigestPart = Pick<JobEntry, "logDigest" | "logsUnavailable" | "logDigestError">;

// Best-effort, as UiPath's digest: a failure surfaces as logDigestError and never hides the job.
async function digestPart(
  env: Env,
  folder: string | undefined,
  detail: ApexJobDetail,
): Promise<DigestPart> {
  const unavailable = logsUnavailableBeforeFetch(detail);
  if (unavailable) return { logsUnavailable: unavailable };
  try {
    const parsed = await readJobLog(env, folder, detail.id);
    if (!parsed) return { logsUnavailable: ARTIFACT_POLICY_UNAVAILABLE };
    const logDigest: ApexLogDigest = {
      ...digestLogs(parsed.rows),
      run: toApexRunSummary(detail, parsed),
    };
    return { logDigest };
  } catch (e) {
    return { logDigestError: toMessage(e) };
  }
}

async function jobEntry(args: GetJobsArgs, id: string): Promise<JobEntry> {
  const { env, folder, batch, includeLogDigest } = args;
  let detail: ApexJobDetail;
  try {
    detail = await getJob(env, folder, id);
  } catch (e) {
    if (isNotFound(e)) return { id, found: false };
    if (batch) return { id, found: false, error: toMessage(e) };
    throw e;
  }
  const job = toApexJobCore(detail, jobLink(id));
  if (!includeLogDigest) return { id, found: true, job };
  return { id, found: true, job, ...(await digestPart(env, folder, detail)) };
}

export async function apexGetJobs(
  args: GetJobsArgs,
  onProgress?: StepProgress,
): Promise<GetJobsResult> {
  const resolved = await resolveFolderId(args.env, args.folder);
  let done = 0;
  const entries = await Promise.all(
    args.jobIds.map(async (id) => {
      try {
        return await jobEntry(args, id);
      } finally {
        done += 1;
        onProgress?.(done, args.jobIds.length, `diagnosed ${done}/${args.jobIds.length} job(s)`);
      }
    }),
  );
  return { folder: resolved.name, entries };
}

async function jobLogsEntry(args: GetJobLogsArgs, jobId: string): Promise<JobLogsEntry> {
  const { env, folder, filter, fullMessages, includeRawFields, includeVideo } = args;
  const detail = await getJob(env, folder, jobId);
  const empty: JobLogsEntry = { jobId, logs: [], returned: 0, truncated: false };

  const unavailable = logsUnavailableBeforeFetch(detail);
  if (unavailable) {
    return {
      ...empty,
      logsUnavailable: unavailable,
      ...(includeVideo ? { videoError: unavailable.detail } : {}),
    };
  }
  const artifacts = await listArtifacts(env, folder, jobId);
  const video = includeVideo ? videoPart(artifacts) : {};
  const logArtifact = artifacts.find((a) => a.filename === JOB_LOG);
  if (!logArtifact) return { ...empty, logsUnavailable: ARTIFACT_POLICY_UNAVAILABLE, ...video };

  const { rows } = await readLogArtifact(env, logArtifact);
  const { kept, totalMatching } = filterLogRows(rows, filter);
  return {
    ...empty,
    logs: kept.map((r) => toLogRow(r, fullMessages, includeRawFields)),
    returned: kept.length,
    totalMatching, // exact: the whole log is fetched, so truncated stays false
    ...video,
  };
}

export async function apexGetJobLogs(
  args: GetJobLogsArgs,
  onProgress?: StepProgress,
): Promise<GetJobLogsResult> {
  const resolved = await resolveFolderId(args.env, args.folder);
  let done = 0;
  const entries = await Promise.all(
    args.jobIds.map(async (jobId): Promise<JobLogsEntry> => {
      try {
        return await jobLogsEntry(args, jobId);
      } catch (e) {
        if (!args.batch) throw e;
        return { jobId, logs: [], returned: 0, truncated: false, error: toMessage(e) };
      } finally {
        done += 1;
        onProgress?.(
          done,
          args.jobIds.length,
          `fetched logs for ${done}/${args.jobIds.length} job(s)`,
        );
      }
    }),
  );
  return { folder: resolved.name, entries };
}

// ---- queues, queue items ------------------------------------------------------

export async function apexListQueues(args: ListQueuesArgs): Promise<QueueCore[]> {
  const queues = await listQueues(args.env, args.folder);
  return queues
    .filter((q) => containsFold(q.name, args.nameContains))
    .slice(0, args.top)
    .map(toApexQueueCore);
}

async function resolveQueue(env: Env, input: string, kind: QueueKind): Promise<ApexQueue> {
  const queues = await listQueues(env, undefined);
  const hit = pickQueue(queues, input, kind);
  if (hit) return hit;
  throw new ExpectedError(
    `no Apex ${kind} queue matches '${input}'. Queues in this folder: ${queues.map((q) => q.name).join(", ")}`,
  );
}

export async function apexListQueueItems(args: ListQueueItemsArgs): Promise<ListQueueItemsResult> {
  const { env, queue, queueKind, status, top } = args;
  const picked = await resolveQueue(env, queue, queueKind);
  const filters = status ? { status: toApexItemStatus(status) } : {};
  const rows = await listQueueItems(env, undefined, picked.id, filters, top);
  return {
    queueId: picked.id,
    queueName: picked.name,
    items: rows
      .slice(0, top)
      .map((r) => toApexItemCore(r, attemptsFromItem(r), false, picked.name)),
  };
}

interface Hit {
  queue: ApexQueue;
  item: ApexQueueItem;
}

// Every attempt of an item is a job with the item's reference; filter by queue_item_id because
// references are not unique (enforce_unique_reference is off). The item's own latest job id
// is the fallback when the reference listing finds none.
async function foundItem(
  env: Env,
  folder: string | undefined,
  { queue, item }: Hit,
  includeContent: boolean,
): Promise<QueueItemCore> {
  const jobs = await listJobs(env, folder, { reference: item.reference }, ATTEMPTS_PER_ITEM_MAX);
  const attempts = jobs.filter((j) => j.queue_item_id === item.id).map(toApexJobAttempt);
  return toApexItemCore(
    item,
    attempts.length > 0 ? attempts : attemptsFromItem(item),
    includeContent,
    queue.name,
  );
}

async function searchAllQueues(
  env: Env,
  folder: string | undefined,
  queues: ApexQueue[],
  needle: string,
): Promise<{ hits: Hit[]; scanned: number }> {
  const hits: Hit[] = [];
  let scanned = 0;
  for (const group of chunk(queues, FIND_CONCURRENCY)) {
    const found = await Promise.all(
      group.map(async (queue) => ({
        queue,
        rows: await listQueueItems(env, folder, queue.id, { search: needle }, SEARCH_PER_QUEUE_MAX),
      })),
    );
    for (const { queue, rows } of found) {
      scanned += rows.length;
      // `search` is fuzzy: only an exact specific_content.orderUid is the order's item.
      for (const item of rows) {
        if (item.specific_content["orderUid"] === needle) hits.push({ queue, item });
      }
    }
  }
  return { hits, scanned };
}

export async function apexFindOrderQueueItems(args: FindOrderArgs): Promise<FindOrderResult> {
  const { env, folder, since, top, includeSpecificContent } = args;
  const needle = args.orderUid.trim();
  const resolved = await resolveFolderId(env, folder);
  const queues = await listQueues(env, folder); // every queue, including Unrouted and inactive ones
  const { hits, scanned } = await searchAllQueues(env, folder, queues, needle);

  const windowed = withinSince(
    hits.map((h) => ({ ...h, created_at: h.item.created_at })),
    since,
  );
  const newest = windowed.rows
    .sort((a, b) => b.created_at.localeCompare(a.created_at))
    .slice(0, top);
  const items: QueueItemCore[] = [];
  for (const group of chunk(newest, FIND_CONCURRENCY)) {
    items.push(
      ...(await Promise.all(group.map((h) => foundItem(env, folder, h, includeSpecificContent)))),
    );
  }
  return {
    folder: resolved.name,
    searchMode: "search",
    scanned,
    items,
    notes: windowed.note ? [windowed.note] : [],
  };
}

async function queueNameOf(
  env: Env,
  queueId: string,
): Promise<{ queueName: string; queueNameError?: string }> {
  try {
    return { queueName: (await getQueue(env, undefined, queueId)).name };
  } catch (e) {
    return { queueName: "", queueNameError: toMessage(e) };
  }
}

export async function apexPullQueueItem(args: PullQueueItemArgs): Promise<PullQueueItemResult> {
  const { env, queueId, itemId } = args;
  if (args.url || args.folderId) {
    throw new ExpectedError(
      "Apex pull_queue_item takes env + queueId + itemId only (url and folderId are UiPath-only)",
    );
  }
  if (!(env && queueId && itemId)) {
    throw new ExpectedError(
      "Apex pull_queue_item needs env, queueId and itemId (from list_queue_items or find_order_queue_items)",
    );
  }
  const item = await getQueueItem(env, undefined, queueId, itemId);
  const { queueName, queueNameError } = await queueNameOf(env, queueId);
  // IsApproved is forced false so a pulled item can never submit a real auth when re-posted.
  const specificContent = guardQueueItemSafety(item.specific_content, "force").specificContent;
  return {
    env,
    item: { ...toApexItemCore(item, attemptsFromItem(item), false, queueName), specificContent },
    portal: toPortalRef(resolvePortal(queueName)),
    isApprovedForced: true,
    suggestedFilename: memberFilename(specificContent),
    ...(queueNameError ? { queueNameError } : {}),
  };
}

// ---- processes, triggers ------------------------------------------------------

export async function apexListProcesses(args: ListProcessesArgs): Promise<ProcessCore[]> {
  const workflows = await listWorkflows(args.env, args.folder);
  return workflows
    .filter((w) => containsFold(w.name, args.nameContains))
    .slice(0, args.top)
    .map(toApexProcessCore);
}

// Queues stand in for queue triggers (Apex has no /triggers route); cron schedules are the
// time triggers and are not queue-scoped, so a queueId filter returns queue triggers only.
export async function apexListTriggers(args: ListTriggersArgs): Promise<TriggerCore[]> {
  const { env, folder, queueId, top } = args;
  const queueTriggers = toApexQueueTriggers(await listQueues(env, folder)).filter(
    (t) => !queueId || t.queueId === queueId,
  );
  const timeTriggers = queueId ? [] : (await listSchedules(env, folder)).map(toApexScheduleTrigger);
  return [...queueTriggers, ...timeTriggers].slice(0, top);
}
```

`backend.ts` is above the ~200-line guideline on purpose: the contract homes all read functions in this one module and Tasks 15–17 add to it; the pure, separately tested logic already lives in `read-support.ts`/`map.ts`.

- [ ] **Step 4: Run tests** — `bun test src/apex/read-support.test.ts src/apex/backend.test.ts` → PASS (20 + 35 tests).
- [ ] **Step 5: Full gate** — `bunx biome check --write src/apex && bun run typecheck && bun test && bunx biome check src` → all green
- [ ] **Step 6: Commit**
```bash
git add src/apex/backend.ts src/apex/read-support.ts src/apex/read-support.test.ts src/apex/backend.test.ts
git commit -m "feat(apex): read functions for jobs, logs, queues, items, processes and triggers" -m "One function per OrchestratorBackend read method, tested against a route-matching fetch stub. Not registered yet (Task 17 assembles apexBackend): Apex logs are client-side filtered over the parsed job.log, order lookup is per-queue search with exact orderUid confirmation, and attempts are enumerated by item reference."
```

---

### Task 12: `get_job_artifacts` tool

**Files:**
- Create: `src/apex/artifacts.ts`
- Modify: `src/server.ts` — import, header tool list, new tool block (anchors by text; pre-Task-5 line numbers shown, they shift after Task 5)
- Modify: `src/server.test.ts` — `EXPECTED` + one schema case
- Test: `src/apex/artifacts.test.ts`

**Interfaces:**
- Consumes: `listArtifacts`, `downloadArtifact`, `ApexArtifact` (Task 10); `resolveFolderId` (Task 7); `useApexStub`, `json`, `FOLDER_IDS` (Task 7); `artifactRow`, `JOB_ID` (Task 10 `test-rows.ts`); in `server.ts`: `ok`, `toolError`, `VERSION`, `withHeartbeat`, `z`, and **`envArg` (defined once near the top of `server.ts` by Task 5)**.
- Produces (contract names): `defaultArtifactsRoot(): string` (= `~/.cache/copilot-mcp/artifacts`); `listJobArtifacts(env, jobId, folder?) → { folder, artifacts: {filename, type, sizeBytes}[] }`; `downloadJobArtifacts(env, jobId, names: string[] | "all", rootDir = defaultArtifactsRoot(), folder?: string | undefined) → { dir, files: {filename, path, sizeBytes}[] }`; tool `get_job_artifacts` (`env, jobId, folder?, download?: "all" | string[]`; annotations readOnly true, destructive false, idempotent true, openWorld true; not in `WRITE_TOOLS`, not in `CLOSED_WORLD_TOOLS`).

Behavior: listing without `download`; `download` fetches the named files (or `all`) sequentially into `<root>/<jobId>/` — directories `0700` (root and job dir), files `0600` — and returns paths + sizes, never bytes. An unknown name is an `ExpectedError` listing the available filenames, raised before anything is downloaded or created. A `jobId` that is not `[\w-]+` and a server-supplied filename that is not a bare name (`..`, `a/b`) are refused, so nothing can be written outside `<root>/<jobId>/`. Downloads send Bearer only (no folder header); the listing honours the `folder` override.

- [ ] **Step 1: Write the failing tests**

Create `src/apex/artifacts.test.ts`:
```ts
import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { existsSync, mkdtempSync, readFileSync, rmSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { ExpectedError } from "../shared/expected-error.js";
import { defaultArtifactsRoot, downloadJobArtifacts, listJobArtifacts } from "./artifacts.js";
import { artifactRow, JOB_ID } from "./test-rows.js";
import { FOLDER_IDS, json, useApexStub } from "./test-support.js";

const LOG = artifactRow({
  id: "a-log",
  filename: "job.log",
  artifact_type: "logs",
  size_bytes: 11,
});
const TRACE = artifactRow({
  id: "a-trace",
  filename: "trace.zip",
  artifact_type: "other",
  size_bytes: 4,
});
const body = (a: typeof LOG) => (a === LOG ? "log content" : "ZIP!");

describe("artifacts", () => {
  const stub = useApexStub();
  let root: string;

  beforeEach(() => {
    root = mkdtempSync(join(tmpdir(), "copilot-mcp-artifacts-"));
    stub.route("GET", `/jobs/${JOB_ID}/artifacts`, () => json([LOG, TRACE]));
    for (const a of [LOG, TRACE]) {
      stub.route("GET", a.download_url.replace("/api/v1", ""), () => new Response(body(a)));
    }
  });
  afterEach(() => rmSync(root, { recursive: true, force: true }));

  test("lists filename, type and size", async () => {
    expect(await listJobArtifacts("pre_prod", JOB_ID)).toEqual({
      folder: "Authorization Test",
      artifacts: [
        { filename: "job.log", type: "logs", sizeBytes: 11 },
        { filename: "trace.zip", type: "other", sizeBytes: 4 },
      ],
    });
  });

  test("the listing honours a folder override (non-default folders 404 without the header)", async () => {
    await listJobArtifacts("pre_prod", JOB_ID, "Authorization");
    expect(stub.callsTo("GET", `/jobs/${JOB_ID}/artifacts`)[0]?.headers["x-folder-id"]).toBe(
      FOLDER_IDS["Authorization"],
    );
  });

  test("downloads the named files into <root>/<jobId>/ and returns paths and sizes", async () => {
    const out = await downloadJobArtifacts("pre_prod", JOB_ID, ["trace.zip"], root);
    expect(out.dir).toBe(join(root, JOB_ID));
    expect(out.files).toEqual([
      { filename: "trace.zip", path: join(root, JOB_ID, "trace.zip"), sizeBytes: 4 },
    ]);
    expect(readFileSync(join(root, JOB_ID, "trace.zip"), "utf8")).toBe("ZIP!");
    expect(existsSync(join(root, JOB_ID, "job.log"))).toBe(false);
  });

  test("'all' downloads every listed artifact", async () => {
    const out = await downloadJobArtifacts("pre_prod", JOB_ID, "all", root);
    expect(out.files.map((f) => f.filename)).toEqual(["job.log", "trace.zip"]);
    expect(readFileSync(join(root, JOB_ID, "job.log"), "utf8")).toBe("log content");
  });

  test("the directory is private (0700) and files are owner-only (0600)", async () => {
    const out = await downloadJobArtifacts("pre_prod", JOB_ID, ["job.log"], root);
    expect(statSync(out.dir).mode & 0o777).toBe(0o700);
    expect(statSync(root).mode & 0o777).toBe(0o700);
    expect(statSync(join(out.dir, "job.log")).mode & 0o777).toBe(0o600);
  });

  test("an unknown name is an ExpectedError listing what exists, before any download", async () => {
    const err = await downloadJobArtifacts("pre_prod", JOB_ID, ["job.log", "nope.png"], root).catch(
      (e: unknown) => e,
    );
    expect(err).toBeInstanceOf(ExpectedError);
    expect((err as Error).message).toContain("'nope.png'");
    expect((err as Error).message).toContain("job.log, trace.zip");
    expect(stub.callsTo("GET", "/jobs/").some((c) => c.path.includes("/artifacts/"))).toBe(false);
    expect(existsSync(join(root, JOB_ID))).toBe(false);
  });

  test("a job id that is not a plain id is refused (no path traversal)", async () => {
    await expect(downloadJobArtifacts("pre_prod", "../../etc", "all", root)).rejects.toBeInstanceOf(
      ExpectedError,
    );
    expect(stub.callsTo("GET", "/jobs/")).toHaveLength(0);
  });

  test("a server-supplied filename containing a path is refused", async () => {
    const evil = artifactRow({ id: "a-evil", filename: "../escape.txt" });
    stub.route("GET", `/jobs/${JOB_ID}/artifacts`, () => json([evil]));
    await expect(downloadJobArtifacts("pre_prod", JOB_ID, "all", root)).rejects.toThrow(
      /path in its name/,
    );
    expect(existsSync(join(root, "escape.txt"))).toBe(false);
  });

  test("the default root is ~/.cache/copilot-mcp/artifacts", () => {
    expect(defaultArtifactsRoot().endsWith(join(".cache", "copilot-mcp", "artifacts"))).toBe(true);
  });
});
```

Modify `src/server.test.ts` — two hunks:

1. In `const EXPECTED = [ … ] as const;` add `"get_job_artifacts",` right after the `"get_job",` entry (currently line 66):
```diff
   "get_job",
+  "get_job_artifacts",
   "add_queue_item",
```
2. In `const cases` inside `describe("tool input schemas accept representative payloads", …)`, insert a case immediately before the `add_queue_item: {` case (currently line 215):
```ts
    get_job_artifacts: {
      valid: {
        env: "pre_prod",
        jobId: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
        download: ["job.log", "trace.zip"],
      },
      // `download` is "all" or a non-empty filename list — any other string is rejected
      invalid: {
        env: "pre_prod",
        jobId: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
        download: "everything",
      },
    },
```
`WRITE_TOOLS` and `CLOSED_WORLD_TOOLS` stay unchanged: the existing annotation tests then assert readOnly true / destructive false / idempotent true / openWorld true for the new tool.

- [ ] **Step 2: Run them to verify they fail**

Run: `bun test src/apex/artifacts.test.ts src/server.test.ts` — Expected: FAIL (`Cannot find module './artifacts.js'`; `registers exactly the expected tools` fails because `get_job_artifacts` is not registered).

- [ ] **Step 3: Implement**

Create `src/apex/artifacts.ts`:
```ts
// get_job_artifacts: list a job's artifacts, or download chosen ones to a private cache dir.
// Artifacts (screenshots, trace.zip, recordings, job.log) can contain PHI, so they are
// written only under ~/.cache/copilot-mcp/artifacts/<jobId>/ (dir 0700, files 0600) — never
// into a repo — and only the paths come back, not the bytes.

import { chmodSync, mkdirSync } from "node:fs";
import { writeFile } from "node:fs/promises";
import { homedir } from "node:os";
import { join } from "node:path";
import type { Env } from "../config/config.js";
import { ExpectedError } from "../shared/expected-error.js";
import { type ApexArtifact, downloadArtifact, listArtifacts } from "./api.js";
import { resolveFolderId } from "./client.js";

export interface ArtifactInfo {
  filename: string;
  type: string; // logs | videos | other
  sizeBytes: number;
}

export interface DownloadedArtifact {
  filename: string;
  path: string;
  sizeBytes: number;
}

export const defaultArtifactsRoot = (): string =>
  join(homedir(), ".cache", "copilot-mcp", "artifacts");

// The job id becomes a directory name, so it must not be able to climb out of the root.
const SAFE_JOB_ID = /^[\w-]+$/;
// Same for server-supplied filenames: a bare name only, never a path.
const isBareFilename = (name: string): boolean =>
  /^[^/\\]+$/.test(name) && name !== "." && name !== "..";

export async function listJobArtifacts(
  env: Env,
  jobId: string,
  folder?: string,
): Promise<{ folder: string; artifacts: ArtifactInfo[] }> {
  const [resolved, rows] = await Promise.all([
    resolveFolderId(env, folder),
    listArtifacts(env, folder, jobId),
  ]);
  return {
    folder: resolved.name,
    artifacts: rows.map((a) => ({
      filename: a.filename,
      type: a.artifact_type,
      sizeBytes: a.size_bytes,
    })),
  };
}

function choose(rows: ApexArtifact[], names: string[] | "all"): ApexArtifact[] {
  if (names === "all") return rows;
  const missing = names.filter((n) => !rows.some((a) => a.filename === n));
  if (missing.length > 0) {
    const available = rows.map((a) => a.filename).join(", ") || "(none)";
    throw new ExpectedError(
      `no artifact named ${missing.map((n) => `'${n}'`).join(", ")} on this job; available: ${available}`,
    );
  }
  return rows.filter((a) => names.includes(a.filename));
}

// `folder` is only the listing's folder-name override; downloads need Bearer, not a folder header.
export async function downloadJobArtifacts(
  env: Env,
  jobId: string,
  names: string[] | "all",
  rootDir: string = defaultArtifactsRoot(),
  folder?: string | undefined,
): Promise<{ dir: string; files: DownloadedArtifact[] }> {
  if (!SAFE_JOB_ID.test(jobId)) throw new ExpectedError(`'${jobId}' is not a valid job id`);
  const chosen = choose(await listArtifacts(env, folder, jobId), names);
  const unsafe = chosen.find((a) => !isBareFilename(a.filename));
  if (unsafe)
    throw new ExpectedError(`refusing artifact with a path in its name: '${unsafe.filename}'`);

  const dir = join(rootDir, jobId);
  for (const d of [rootDir, dir]) {
    mkdirSync(d, { recursive: true, mode: 0o700 });
    chmodSync(d, 0o700);
  }
  const files: DownloadedArtifact[] = [];
  for (const a of chosen) {
    const { bytes } = await downloadArtifact(env, a);
    const path = join(dir, a.filename);
    await writeFile(path, bytes, { mode: 0o600 });
    files.push({ filename: a.filename, path, sizeBytes: bytes.byteLength });
  }
  return { dir, files };
}
```

Modify `src/server.ts` — three hunks:

1. Header tool list (comment block at the top, currently lines 9–33): add one line after the `get_job` line (currently line 20; in Task 5 it reads `get_job … by jobId`):
```ts
//   - get_job_artifacts        list or download an Apex job's artifacts (read-only; files land under ~/.cache/copilot-mcp/artifacts)
```
2. Imports: add, as the first `./`-relative import, just before `import { configStatus, getFeedbackConfig, loadConfig, onConfigReload } from "./config/config.js";` (currently line 43):
```ts
import { downloadJobArtifacts, listJobArtifacts } from "./apex/artifacts.js";
```
3. Register the tool: insert this block immediately before the line `// ---- add_queue_item ---…` (currently line 1417), i.e. directly after the `get_job` registration:
```ts
// ---- get_job_artifacts -----------------------------------------------------
server.registerTool(
  "get_job_artifacts",
  {
    title: "List or download an Apex job's artifacts",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "List an Apex job's artifacts ({filename, type, sizeBytes}: job.log, bot.log, debug.json, " +
      "result.json, screenshots, trace.zip, recording.mp4), or download chosen ones with `download` " +
      '(artifact filenames, or "all") to ~/.cache/copilot-mcp/artifacts/<jobId>/ and get back the ' +
      "local paths + sizes. Apex-only (UiPath has no artifacts) — no `orchestrator` argument. " +
      "READ-ONLY on Apex; only local files are written. PHI WARNING: screenshots, trace.zip, " +
      "recordings and logs can contain member data — they are written OUTSIDE any repo with " +
      "owner-only permissions; never commit, attach or paste them. An unknown filename fails with " +
      "the list of available names. For parsed, filterable logs use get_job_logs instead; use this " +
      "for trace.zip, screenshots, recordings and raw files. Returns {env,folder,artifacts[]} " +
      "when listing, {env,dir,files:[{filename,path,sizeBytes}]} when downloading.",
    inputSchema: {
      env: envArg,
      jobId: z.string().min(8).describe("The Apex job id (from get_job / find_order_queue_items)"),
      folder: z
        .string()
        .optional()
        .describe("Explicit folder name override for the listing (wins over env)"),
      download: z
        .union([z.literal("all"), z.array(z.string().min(1)).min(1).max(50)])
        .optional()
        .describe(
          'Artifact filenames to download (e.g. ["trace.zip"]) or "all". Omit to only list.',
        ),
    },
  },
  async ({ env, jobId, folder, download }, extra) => {
    try {
      if (!download) {
        const listing = await withHeartbeat(extra, "Apex", listJobArtifacts(env, jobId, folder));
        return ok({ env, ...listing });
      }
      const downloaded = await withHeartbeat(
        extra,
        "Apex",
        downloadJobArtifacts(env, jobId, download, undefined, folder),
      );
      return ok({ env, ...downloaded });
    } catch (e) {
      return toolError("get_job_artifacts", e, VERSION);
    }
  },
);
```

- [ ] **Step 4: Run tests** — `bun test src/apex/artifacts.test.ts src/server.test.ts` → PASS (9 artifacts tests; server.test green including the new schema accept/reject pair and the four-hint assertions).
- [ ] **Step 5: Full gate** — `bunx biome check --write src/apex src/server.ts src/server.test.ts && bun run typecheck && bun test && bunx biome check src` → all green
- [ ] **Step 6: Commit**
```bash
git add src/apex/artifacts.ts src/apex/artifacts.test.ts src/server.ts src/server.test.ts
git commit -m "feat(apex): get_job_artifacts tool to list and download job artifacts" -m "Apex keeps trace.zip, screenshots, recordings and raw logs as per-job artifacts that only exist behind the Bearer-authenticated API. The tool lists them, or downloads chosen ones into ~/.cache/copilot-mcp/artifacts/<jobId>/ (0700, never inside a repo) and returns paths, since artifacts can contain PHI."
```

## Phase 4: Cross-backend tools and Apex payload (Tasks 13–16)

Conventions used by Tasks 13–15:

- `Orchestrator` already contains `"apex"` (Task 1), but `BACKENDS`/`backendFor`/the tool enums only cover `ENABLED_ORCHESTRATORS` (= `["uipath"]`, Task 5) until Task 17. Test fixtures that need a second backend name use `const APEX = "apex" as const;` (no casts needed); the code under test is name-agnostic and typed over `EnabledOrchestrator` where it takes the optional tool argument.
- Server hunks locate code by tool name, not line number, wherever Task 5 has already rewritten the block. Whole
  registration blocks are replaced, so the result does not depend on what exact text Task 5 left behind. Task 5's shared
  zod snippets `envArg` and `orchestratorArg` and the `ok` / `toolError` / `withHeartbeat` / `stepProgress` helpers are used as the contract defines them.
- If `bunx biome check src` reports only import/specifier ordering in a file this task touched, run
  `bunx biome check --write <file>` and re-run the gate; Biome's sort is authoritative.

---

### Task 13: `find_order_queue_items` across orchestrators

**Files:**
- Create: `src/orchestrator/find-order.ts`
- Test: `src/orchestrator/find-order.test.ts`
- Modify: `src/orchestrator/registry.ts` (append `searchableBackends`; covered by `selectBackends` tests, no separate test file)
- Modify: `src/server.ts` (replace the whole `find_order_queue_items` registration block, from the `// ---- find_order_queue_items` comment to the `);` before `// ---- Resources (static reference data)`; drop the now-unused import added by Task 5 for it, add the `find-order.js` import; header-comment bullet)
- Modify: `src/server.test.ts` (the `find_order_queue_items` entry of the schema `cases` map; currently lines 151-154 pre-Task-5)
- Modify: `src/mcp/prompts.ts` (`diagnose-order` steps 2–3, currently lines 43-48 pre-Task-5)

**Interfaces:**
- Consumes: `OrchestratorBackend`, `FindOrderArgs`, `FindOrderResult` (`src/orchestrator/backend.ts`); `QueueItemCore`, `Orchestrator` (`src/orchestrator/types.ts`); `BACKENDS`, `backendFor`, `EnabledOrchestrator` (`src/orchestrator/registry.ts`, Task 5); `ExpectedError` (`src/shared/expected-error.ts`, moved there by Task 1).
- Produces (later tasks rely on these exact names):
  - `export interface FindOrderAcrossResult { count: number; jobIds: Partial<Record<Orchestrator, string[]>>; items: QueueItemCore[]; searched: Orchestrator[]; backends: Partial<Record<Orchestrator, { folder: string; searchMode: string; scanned: number }>>; notes?: string[]; searchErrors?: Partial<Record<Orchestrator, string>> }`
  - `export function findOrderAcross(args: FindOrderArgs, backends: OrchestratorBackend[]): Promise<FindOrderAcrossResult>`
  - `export const searchableBackends = (): OrchestratorBackend[] => Object.values(BACKENDS);` in `src/orchestrator/registry.ts`: the backends an omitted `orchestrator` searches. Task 17 replaces its body with a filter that drops Apex when Apex is not configured, so a UiPath-only setup never sees an Apex "not configured" error on default searches (and `find_stuck_orders` never turns "found nowhere" into an `error:` verdict).
  - `export function selectBackends(orchestrator?: EnabledOrchestrator, folder?: string): OrchestratorBackend[]` — explicit orchestrator → `[backendFor(o)]`; omitted → `searchableBackends()`; `folder` without `orchestrator` → `ExpectedError` (Task 14 reuses it).

Design notes (decisions, so the reviewer does not have to infer them):
- Each backend runs in `Promise.allSettled`; a rejection OR a returned `searchError` lands in `searchErrors[backend]`. A returned `searchError` keeps that backend's partial `items`.
- The `since` retry-unbounded behavior is NOT repeated here: it already lives inside each backend's `findOrderQueueItems`.
- `top` stays a per-orchestrator cap; the merged list is not re-sliced, so no item a backend returned is silently dropped.
- `jobIds[backend]` is present for every backend that answered, even as `[]` (queued-not-picked-up is a real finding); it is absent for a backend that failed outright (`searchErrors` explains why). Ids are deduped per backend, newest item first, each item's attempts newest first.
- Unparseable `createdAt` sorts as the oldest instead of poisoning the comparator with `NaN`.

- [ ] **Step 1: Write the failing test**

````ts
// src/orchestrator/find-order.test.ts
import { describe, expect, test } from "bun:test";
import { ExpectedError } from "../shared/expected-error.js";
import type { FindOrderArgs, FindOrderResult, OrchestratorBackend } from "./backend.js";
import { findOrderAcross, selectBackends } from "./find-order.js";
import { searchableBackends } from "./registry.js";
import {
  type JobAttempt,
  type Orchestrator,
  type QueueItemCore,
} from "./types.js";

const APEX = "apex" as const;

const ARGS: FindOrderArgs = {
  env: "prod",
  orderUid: "order-0001",
  top: 50,
  includeSpecificContent: true,
};

const item = (
  orchestrator: Orchestrator,
  itemId: string,
  createdAt: string,
  jobIds: string[] = [],
): QueueItemCore => ({
  orchestrator,
  queueId: "q-1",
  queueName: "queue",
  itemId,
  reference: `ref-${itemId}`,
  orderUid: ARGS.orderUid,
  status: "New",
  rawStatus: "New",
  exceptionType: null,
  createdAt,
  endedAt: null,
  jobs: jobIds.map((id): JobAttempt => ({ orchestrator, id, state: null, neverStarted: null })),
  specificContent: null,
  details: {},
});

const result = (items: QueueItemCore[], over: Partial<FindOrderResult> = {}): FindOrderResult => ({
  folder: "Authorization",
  searchMode: "direct",
  scanned: items.length,
  items,
  notes: [],
  ...over,
});

const backend = (
  name: Orchestrator,
  find: (args: FindOrderArgs) => Promise<FindOrderResult>,
): OrchestratorBackend => ({ name, findOrderQueueItems: find }) as OrchestratorBackend;

describe("findOrderAcross", () => {
  test("merges every backend's items newest first; an unparseable createdAt sorts oldest", async () => {
    const out = await findOrderAcross(ARGS, [
      backend("uipath", async () =>
        result([
          item("uipath", "u3", "2026-07-03T00:00:00Z"),
          item("uipath", "u1", "2026-07-01T00:00:00Z"),
        ]),
      ),
      backend(APEX, async () =>
        result([
          item(APEX, "bad", "not-a-date"),
          item(APEX, "a2", "2026-07-02T00:00:00.123456+00:00"),
        ]),
      ),
    ]);
    expect(out.items.map((i) => i.itemId)).toEqual(["u3", "a2", "u1", "bad"]);
    expect(out.count).toBe(4);
    expect(out.searched).toEqual(["uipath", APEX]);
  });

  test("jobIds are per backend, deduped, newest item first; an answered backend with no jobs gets []", async () => {
    const out = await findOrderAcross(ARGS, [
      backend("uipath", async () =>
        result([
          item("uipath", "u2", "2026-07-02T00:00:00Z", ["j3", "j2"]),
          item("uipath", "u1", "2026-07-01T00:00:00Z", ["j2", "j1"]),
        ]),
      ),
      backend(APEX, async () => result([item(APEX, "a1", "2026-07-03T00:00:00Z")])),
    ]);
    expect(out.jobIds).toEqual({ uipath: ["j3", "j2", "j1"], [APEX]: [] });
  });

  test("one backend rejecting does not lose the other's items; it is reported, not thrown", async () => {
    const out = await findOrderAcross(ARGS, [
      backend("uipath", async () => result([item("uipath", "u1", "2026-07-01T00:00:00Z", ["j1"])])),
      backend(APEX, async () => {
        throw new Error("apex login failed");
      }),
    ]);
    expect(out.items.map((i) => i.itemId)).toEqual(["u1"]);
    expect(out.searchErrors).toEqual({ [APEX]: "apex login failed" });
    expect(out.searched).toEqual(["uipath", APEX]);
    expect(Object.keys(out.jobIds)).toEqual(["uipath"]);
    expect(Object.keys(out.backends)).toEqual(["uipath"]);
  });

  test("a searchError returned by a backend is reported and keeps that backend's partial items", async () => {
    const out = await findOrderAcross(ARGS, [
      backend(APEX, async () =>
        result([item(APEX, "a1", "2026-07-01T00:00:00Z", ["j1"])], { searchError: "queue 7 timed out" }),
      ),
    ]);
    expect(out.items.map((i) => i.itemId)).toEqual(["a1"]);
    expect(out.searchErrors).toEqual({ [APEX]: "queue 7 timed out" });
    expect(out.jobIds).toEqual({ [APEX]: ["j1"] });
  });

  test("every backend failing yields count 0 with an error per backend", async () => {
    const boom = (msg: string) => async (): Promise<FindOrderResult> => {
      throw new Error(msg);
    };
    const out = await findOrderAcross(ARGS, [
      backend("uipath", boom("uipath 503")),
      backend(APEX, boom("apex 401")),
    ]);
    expect(out.count).toBe(0);
    expect(out.searchErrors).toEqual({ uipath: "uipath 503", [APEX]: "apex 401" });
  });

  test("per-backend folder, searchMode, scanned and prefixed notes; notes/searchErrors omitted when empty", async () => {
    const out = await findOrderAcross(ARGS, [
      backend("uipath", async () =>
        result([], { folder: "Authorization", searchMode: "contains", scanned: 9, notes: ["slow"] }),
      ),
      backend(APEX, async () => result([], { folder: "Authorization Test", scanned: 2 })),
    ]);
    expect(out.backends).toEqual({
      uipath: { folder: "Authorization", searchMode: "contains", scanned: 9 },
      [APEX]: { folder: "Authorization Test", searchMode: "direct", scanned: 2 },
    });
    expect(out.notes).toEqual(["uipath: slow"]);
    expect(out.searchErrors).toBeUndefined();

    const quiet = await findOrderAcross(ARGS, [backend("uipath", async () => result([]))]);
    expect(quiet.notes).toBeUndefined();
  });
});

describe("selectBackends", () => {
  test("omitted orchestrator selects every registered backend", () => {
    expect(selectBackends().map((b) => b.name)).toEqual(searchableBackends().map((b) => b.name));
  });

  test("an explicit orchestrator selects only that backend, with or without a folder", () => {
    expect(selectBackends("uipath").map((b) => b.name)).toEqual(["uipath"]);
    expect(selectBackends("uipath", "Authorization").map((b) => b.name)).toEqual(["uipath"]);
  });

  test("a folder without an orchestrator is refused as an expected, user-actionable error", () => {
    expect(() => selectBackends(undefined, "Authorization")).toThrow(ExpectedError);
    expect(() => selectBackends(undefined, "Authorization")).toThrow(/orchestrator/);
  });
});
````

- [ ] **Step 2: Run it to verify it fails**
Run: `bun test src/orchestrator/find-order.test.ts` — Expected: FAIL (`Cannot find module './find-order.js'`).

- [ ] **Step 3: Implement**

`src/orchestrator/registry.ts` — append at the end of the file (after `backendFor`; no new imports, `OrchestratorBackend` is already imported there):

````ts
// What an omitted `orchestrator` searches (find_order_queue_items, find_stuck_orders).
export const searchableBackends = (): OrchestratorBackend[] => Object.values(BACKENDS);
````

New file:

````ts
// src/orchestrator/find-order.ts
// find_order_queue_items across orchestrators: ask every selected backend for the order's
// queue items in parallel, merge them newest first, and keep each backend's failure
// isolated. Routing is decided per portal outside this repo, so where the item actually
// sits is the truth. A backend's own `since` retry (unbounded when nothing matches) is
// that backend's business and is not repeated here.

import { ExpectedError } from "../shared/expected-error.js";
import type { FindOrderArgs, FindOrderResult, OrchestratorBackend } from "./backend.js";
import { backendFor, type EnabledOrchestrator, searchableBackends } from "./registry.js";
import type { Orchestrator, QueueItemCore } from "./types.js";

export interface FindOrderAcrossResult {
  count: number;
  jobIds: Partial<Record<Orchestrator, string[]>>; // per answering backend, newest first, deduped
  items: QueueItemCore[]; // merged, newest first by createdAt
  searched: Orchestrator[];
  backends: Partial<Record<Orchestrator, { folder: string; searchMode: string; scanned: number }>>;
  notes?: string[]; // each prefixed "<backend>: "
  searchErrors?: Partial<Record<Orchestrator, string>>;
}

// `folder` names one orchestrator's folder, so it only makes sense next to that orchestrator.
export function selectBackends(
  orchestrator?: EnabledOrchestrator,
  folder?: string,
): OrchestratorBackend[] {
  if (folder && !orchestrator) {
    throw new ExpectedError(
      "`folder` is a per-orchestrator override: pass `orchestrator` together with it, or omit " +
        "`folder` to search every orchestrator's default folder for the env.",
    );
  }
  return orchestrator ? [backendFor(orchestrator)] : searchableBackends();
}

// NaN would make the comparator inconsistent; an unreadable timestamp just sorts oldest.
const createdMs = (item: QueueItemCore): number => {
  const t = Date.parse(item.createdAt);
  return Number.isNaN(t) ? 0 : t;
};

// Items arrive newest first and each item's attempts newest first, so first-seen order
// is newest first; the Set drops the repeats a retried transaction produces.
const jobIdsOf = (items: QueueItemCore[]): string[] => [
  ...new Set(items.flatMap((i) => i.jobs.map((j) => j.id))),
];

export async function findOrderAcross(
  args: FindOrderArgs,
  backends: OrchestratorBackend[],
): Promise<FindOrderAcrossResult> {
  const settled = await Promise.allSettled(backends.map((b) => b.findOrderQueueItems(args)));
  const searched = backends.map((b) => b.name);

  const answers: Partial<Record<Orchestrator, FindOrderResult>> = {};
  const searchErrors: Partial<Record<Orchestrator, string>> = {};
  settled.forEach((r, i) => {
    const name = searched[i];
    if (!name) return;
    if (r.status === "rejected") {
      searchErrors[name] = r.reason instanceof Error ? r.reason.message : String(r.reason);
      return;
    }
    answers[name] = r.value;
    if (r.value.searchError) searchErrors[name] = r.value.searchError;
  });

  const items = searched
    .flatMap((name) => answers[name]?.items ?? [])
    .sort((a, b) => createdMs(b) - createdMs(a));
  const notes = searched.flatMap((name) => (answers[name]?.notes ?? []).map((n) => `${name}: ${n}`));

  const jobIds: FindOrderAcrossResult["jobIds"] = {};
  const answered: FindOrderAcrossResult["backends"] = {};
  for (const name of searched) {
    const answer = answers[name];
    if (!answer) continue;
    jobIds[name] = jobIdsOf(items.filter((i) => i.orchestrator === name));
    answered[name] = { folder: answer.folder, searchMode: answer.searchMode, scanned: answer.scanned };
  }

  return {
    count: items.length,
    jobIds,
    items,
    searched,
    backends: answered,
    ...(notes.length ? { notes } : {}),
    ...(Object.keys(searchErrors).length ? { searchErrors } : {}),
  };
}
````

`src/server.ts` — import (add next to the other `./orchestrator/…` imports; keep Biome's order; remove any now-unused `findOrderQueueItems`/`backendFor`-only import Task 5 left for this tool if `backendFor` is no longer used by anything else — it still is, by the other tools):

````ts
import { findOrderAcross, selectBackends } from "./orchestrator/find-order.js";
````

`src/server.ts` — replace the entire `find_order_queue_items` registration block (comment line `// ---- find_order_queue_items ---…` through the closing `);` that precedes `// ---- Resources (static reference data) ---…`) with:

````ts
// ---- find_order_queue_items ------------------------------------------------
server.registerTool(
  "find_order_queue_items",
  {
    title: "Find the queue item(s) a Copilot order flowed through",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Given a Copilot orderUid, find the orchestrator queue item(s) it flowed through and the " +
      "job ids that ran them — every order flows through a queue item carrying the orderUid and " +
      "its job(s), so this reaches faulted/still-running consumer jobs an OutputArguments scan " +
      "can't. Omit `orchestrator` to search EVERY orchestrator (uipath | apex) in parallel: routing " +
      "is decided per portal outside this repo, so where the item actually sits is the truth. Each " +
      "orchestrator fails independently into `searchErrors` without losing the others' items. " +
      "`folder` is accepted only together with an explicit `orchestrator`. READ-ONLY. The item's " +
      "full specificContent is returned deliberately, in full (member/clinical fields visible) — " +
      "the point is divergence analysis: comparing what Copilot actually sent against the order " +
      "record from get_order. Only the JWT `token` is redacted (a live credential, opaque, useless " +
      "for divergence). Feed `jobIds[<orchestrator>]` into get_job's jobIds with THAT orchestrator " +
      "and includeLogDigest=true to diagnose every run and retry in one call per orchestrator " +
      "(Apex lists every attempt of a retried item). `count === 0` with no `searchErrors` means the " +
      "order never reached any searched orchestrator; with `searchErrors` it is inconclusive for " +
      "the failed ones. `jobIds[o]: []` with items present means queued but not yet picked up by a " +
      "worker. Env picks each orchestrator's folder (see copilot://reference/orchestrators). " +
      "`since` narrows the queue-item search (each orchestrator retries unbounded if nothing " +
      "matches) — pass the order's creationDate from get_order. Returns {orderUid, env, count, " +
      "jobIds:{<orchestrator>:string[]}, items:QueueItemCore[] (newest first), searched, " +
      "backends:{<orchestrator>:{folder,searchMode,scanned}}, notes? (prefixed '<orchestrator>: '), " +
      "searchErrors?:{<orchestrator>:string}}.",
    inputSchema: {
      orderUid: z.string().min(8).describe("Copilot orderUid to correlate"),
      env: envArg,
      orchestrator: orchestratorArg
        .optional()
        .describe("Search only this orchestrator (uipath | apex); omit to search every orchestrator"),
      folder: z
        .string()
        .optional()
        .describe(
          "Explicit folder override for the one orchestrator named by `orchestrator` (wins " +
            "over env); refused without `orchestrator`",
        ),
      since: z
        .string()
        .optional()
        .describe(
          "ISO lower bound on the queue item's creation time — pass the order's creationDate " +
            "from get_order; each orchestrator retries without the bound if nothing matches",
        ),
      top: z
        .number()
        .int()
        .min(1)
        .max(500)
        .optional()
        .default(50)
        .describe("Max queue items per orchestrator"),
      includeSpecificContent: z.boolean().optional().default(true),
    },
  },
  async ({ orderUid, env, orchestrator, folder, since, top, includeSpecificContent }, extra) => {
    try {
      const found = await withHeartbeat(
        extra,
        "Orchestrator",
        findOrderAcross(
          { env, orderUid, folder, since, top, includeSpecificContent },
          selectBackends(orchestrator, folder),
        ),
      );
      return ok({ orderUid, env, ...found });
    } catch (e) {
      return toolError("find_order_queue_items", e, VERSION);
    }
  },
);
````

`src/server.ts` header comment: on the bullet line that starts with `//   - find_order_queue_items` (line 15 pre-Task-5; Task 5 may have reworded it), replace the whole line with:

````ts
//   - find_order_queue_items   find the queue item(s) + job ids an order flowed through, across every orchestrator (read-only)
````

`src/server.test.ts` — replace the `find_order_queue_items` entry in the `cases` map with (orchestrator omitted is now valid):

````ts
    find_order_queue_items: {
      valid: { orderUid: "abcdefgh", env: "prod" }, // orchestrator omitted = every orchestrator
      invalid: { orderUid: "abcdefgh" }, // env is required
    },
````

`src/mcp/prompts.ts` — in the `diagnose-order` prompt, replace steps 2 and 3 (the two template strings starting `` `2. Call find_order_queue_items `` and ending `…Read state, output, fault, logDigest per job.\n` + `` — Task 5 may have renamed `jobKeys`→`jobIds` inside them; replace the whole span) with:

````ts
          `2. Call find_order_queue_items with orderUid="${orderUid}"${envArg} and since=<that creationDate> — omit ` +
          `orchestrator to search every orchestrator — to find the queue item(s) and their jobIds (keyed per ` +
          `orchestrator). If count is 0 and searchErrors is absent, the order never reached any orchestrator — report and ` +
          `stop. If searchErrors names an orchestrator, that search failed: report it and retry rather than concluding the ` +
          `order never arrived. If items exist but an orchestrator's jobIds list is empty, the order is queued and not yet ` +
          `picked up by a worker — report the item statuses and stop.\n` +
          `3. For each orchestrator that has jobIds, call get_job with orchestrator=<that orchestrator>, env${env ? `="${env}"` : ""}, ` +
          `jobIds=<its ids from step 2> and includeLogDigest=true — ONE call per orchestrator covering the original run ` +
          `and every retry. Read state, fault, details, logDigest per job.\n` +
````

- [ ] **Step 4: Run tests** — `bun test src/orchestrator/find-order.test.ts src/server.test.ts src/mcp/prompts.test.ts` → PASS
- [ ] **Step 5: Full gate** — `bun run typecheck && bun test && bunx biome check src` → all green
- [ ] **Step 6: Commit**
```bash
git add src/orchestrator/find-order.ts src/orchestrator/find-order.test.ts src/orchestrator/registry.ts src/server.ts src/server.test.ts src/mcp/prompts.ts
git commit -m "feat(orchestrator)!: find_order_queue_items searches every orchestrator" -m "Routing is decided per portal outside this repo, so omitting orchestrator now queries every registered backend in parallel, merges items newest first, returns jobIds per orchestrator and isolates each backend's failure in searchErrors. folder is refused without an explicit orchestrator."
```

---

### Task 14: `find_stuck_orders` cross-check across orchestrators + `job-never-started`

**Files:**
- Modify: `src/copilot/sweep.ts` (lines 1-10 header/imports; 23-30 and 39 types; 86-116 verdict; 160-179 cross-check call; 184-210 `crossCheckUipath` → `crossCheckOrders`; all line numbers verified against the untouched tree, Task 5 does not touch this file)
- Modify: `src/copilot/sweep.test.ts` (lines 1-109 replaced; lines 110-139, the `queueSearchSince` describe, stay verbatim)
- Modify: `src/server.ts` (replace the whole `find_stuck_orders` registration block, `// ---- find_stuck_orders ---…` through the `);` before `// ---- diff_settings ---…`; Task 5 leaves it untouched, pre-Task-5 lines 1607-1696)
- Modify: `src/server.test.ts` (add a `find_stuck_orders` entry to the `cases` map)
- Modify: `src/mcp/prompts.ts` (`triage-stuck-orders` prompt, pre-Task-5 lines 169-189)

**Interfaces:**
- Consumes: `findOrderAcross`, `selectBackends`, `FindOrderAcrossResult` (Task 13); `QueueItemCore`, `JobAttempt`, `Orchestrator` (`types.ts`); `queueSearchSince` (unchanged, same file).
- Produces:
  - `export interface OrderVerdict { backend: Orchestrator | null; verdict: string; queueItemCount: number; queueItemStatus?: string | undefined; exceptionType?: "business" | "application" | undefined }`
  - `StuckOrder.orchestrator?: OrderVerdict | undefined` (replaces `StuckOrder.uipath`)
  - `FindStuckArgs.crossCheck?: boolean | undefined` (replaces `crossCheckUipath`) and `FindStuckArgs.orchestrator?: EnabledOrchestrator | undefined`
  - `export function crossCheckOrders(stuck: StuckOrder[], correlate: (orderUid: string) => Promise<QueueItemCore[]>, onProgress?: StepProgress): Promise<void>` (`correlate` must return items newest first)
  - `export function itemsOrThrow(found: FindOrderAcrossResult): QueueItemCore[]`

Design notes:
- **Correlation uses `findOrderAcross` (Task 13), not each backend's `findOrderQueueItems` directly.** `findOrderAcross` has no folder semantics of its own (the folder refusal lives in `selectBackends(orchestrator, folder)`, and the sweep passes no folder, so every backend uses its env default folder); it already runs the backends in parallel, merges newest first and isolates failures, so the sweep needs no second implementation of that.
- `includeSpecificContent: false`: the verdict only needs status/jobs, so no order content (PHI) is fetched into the sweep.
- Verdict rules, in priority order: `job-never-started` (the newest item's newest attempt has `neverStarted === true`) > `job-faulted` (any item `Failed`) > `job-running` (any `InProgress`) > `queued-not-picked-up` (a `New` item with no job attempt: UiPath's "no ExecutorJobKey", Apex's "no job yet") > `job-successful-order-stuck` (all `Successful`) > `job-found`. Only the NEWEST attempt of the NEWEST item decides `job-never-started`: an older reaped attempt that a retry replaced is history. `neverStarted: null` (backend can't tell) never triggers it. Status labels are the UiPath spelling by contract, identical to before.
- `orchestrator.backend` is the orchestrator holding the newest item (`null` when there is no item or the lookup failed).
- A backend that failed to search while nothing was found elsewhere is *unknown*, not `no-job`: `itemsOrThrow` throws so `crossCheckOrders` records `error: <backend>: <message>` for that order. When some item was found, the verdict stands on it.
- `ponytail:` the exception type reported is the first non-null across all items (as before); no per-attempt exception detail.

- [ ] **Step 1: Write the failing test**

Replace `src/copilot/sweep.test.ts` lines 1-109 with the following (keep lines 110-139, the `queueSearchSince` describe, byte for byte):

````ts
import { describe, expect, test } from "bun:test";
import type { FindOrderAcrossResult } from "../orchestrator/find-order.js";
import type { JobAttempt, Orchestrator, QueueItemCore } from "../orchestrator/types.js";
import { crossCheckOrders, itemsOrThrow, queueSearchSince, type StuckOrder } from "./sweep.js";

const APEX = "apex" as const;

const stuckOrder = (orderUid: string | undefined): StuckOrder => ({ orderUid, ageHours: null });

const attempt = (over: Partial<JobAttempt> = {}): JobAttempt => ({
  orchestrator: "uipath",
  id: "j1",
  state: null,
  neverStarted: null,
  ...over,
});

const qItem = (over: Partial<QueueItemCore> = {}): QueueItemCore => ({
  orchestrator: "uipath",
  queueId: "1",
  queueName: "queue",
  itemId: "1",
  reference: "ref",
  orderUid: null,
  status: "New",
  rawStatus: "New",
  exceptionType: null,
  createdAt: "2026-07-01T00:00:00Z",
  endedAt: null,
  jobs: [],
  specificContent: null,
  details: {},
  ...over,
});

describe("crossCheckOrders", () => {
  test("skips entries with no orderUid, leaving their orchestrator field untouched", async () => {
    const stuck = [stuckOrder(undefined)];
    await crossCheckOrders(stuck, async () => []);
    expect(stuck[0]?.orchestrator).toBeUndefined();
  });

  test("attaches a verdict per order from the injected correlate function", async () => {
    const stuck = [stuckOrder("u1"), stuckOrder("u2")];
    await crossCheckOrders(stuck, async (orderUid) =>
      orderUid === "u1" ? [qItem({ status: "Successful", jobs: [attempt()] })] : [],
    );
    expect(stuck[0]?.orchestrator?.verdict).toBe("job-successful-order-stuck");
    expect(stuck[1]?.orchestrator).toEqual({ backend: null, verdict: "no-job", queueItemCount: 0 });
  });

  test("a Failed queue item is job-faulted and surfaces the exception type", async () => {
    // The faulted consumer job is invisible to an OutputArguments scan; the queue
    // item's status is what makes it directly determinable.
    const stuck = [stuckOrder("u1")];
    await crossCheckOrders(stuck, async () => [
      qItem({ status: "Failed", exceptionType: "business", jobs: [attempt()] }),
    ]);
    expect(stuck[0]?.orchestrator).toEqual({
      backend: "uipath",
      verdict: "job-faulted",
      queueItemCount: 1,
      queueItemStatus: "Failed",
      exceptionType: "business",
    });
  });

  test("a New item with no job attempt is queued-not-picked-up; once an attempt exists it is not", async () => {
    const stuck = [stuckOrder("u1"), stuckOrder("u2")];
    await crossCheckOrders(stuck, async (orderUid) =>
      orderUid === "u1"
        ? [qItem({ status: "New" })]
        : [qItem({ status: "New", jobs: [attempt()] })],
    );
    expect(stuck[0]?.orchestrator?.verdict).toBe("queued-not-picked-up");
    expect(stuck[1]?.orchestrator?.verdict).toBe("job-found");
  });

  test("InProgress wins over an older Successful item (running)", async () => {
    const stuck = [stuckOrder("u1")];
    await crossCheckOrders(stuck, async () => [
      qItem({ status: "InProgress", jobs: [attempt({ id: "j2" })] }),
      qItem({ status: "Successful", jobs: [attempt({ id: "j1" })] }),
    ]);
    expect(stuck[0]?.orchestrator?.verdict).toBe("job-running");
  });

  test("a never-started newest attempt is job-never-started, ahead of job-faulted", async () => {
    const stuck = [stuckOrder("u1")];
    await crossCheckOrders(stuck, async () => [
      qItem({
        status: "Failed",
        jobs: [
          attempt({ id: "j2", state: "Faulted", neverStarted: true }),
          attempt({ id: "j1", state: "Faulted", neverStarted: false }),
        ],
      }),
    ]);
    expect(stuck[0]?.orchestrator?.verdict).toBe("job-never-started");
    expect(stuck[0]?.orchestrator?.queueItemStatus).toBe("Failed");
  });

  test("only the newest attempt of the newest item decides never-started", async () => {
    const stuck = [stuckOrder("older-item"), stuckOrder("older-attempt"), stuckOrder("unknown")];
    await crossCheckOrders(stuck, async (orderUid) => {
      if (orderUid === "older-item") {
        // newest item succeeded; the reaped attempt belongs to an older, failed item
        return [
          qItem({ status: "Successful", jobs: [attempt({ neverStarted: false })] }),
          qItem({ status: "Failed", jobs: [attempt({ id: "j0", neverStarted: true })] }),
        ];
      }
      if (orderUid === "older-attempt") {
        // an earlier attempt was reaped, the retry is running
        return [
          qItem({
            status: "InProgress",
            jobs: [attempt({ id: "j2", neverStarted: false }), attempt({ id: "j1", neverStarted: true })],
          }),
        ];
      }
      // neverStarted: null means "backend can't tell", never "never started"
      return [qItem({ status: "Failed", jobs: [attempt({ neverStarted: null })] })];
    });
    expect(stuck.map((s) => s.orchestrator?.verdict)).toEqual([
      "job-faulted",
      "job-running",
      "job-faulted",
    ]);
  });

  test("attributes the verdict to the orchestrator holding the newest item", async () => {
    const stuck = [stuckOrder("both"), stuckOrder("none")];
    await crossCheckOrders(stuck, async (orderUid) =>
      orderUid === "both"
        ? [
            qItem({ orchestrator: APEX, status: "InProgress", jobs: [attempt({ orchestrator: APEX })] }),
            qItem({ orchestrator: "uipath", status: "Failed", jobs: [attempt()] }),
          ]
        : [],
    );
    expect(stuck[0]?.orchestrator?.backend).toBe(APEX);
    expect(stuck[0]?.orchestrator?.queueItemCount).toBe(2);
    expect(stuck[1]?.orchestrator?.backend).toBeNull();
  });

  test("a rejected lookup attaches an error verdict instead of losing the batch", async () => {
    const stuck = [stuckOrder("u1"), stuckOrder("u2")];
    await crossCheckOrders(stuck, async (orderUid) => {
      if (orderUid === "u1") throw new Error("orchestrator timeout");
      return [];
    });
    expect(stuck[0]?.orchestrator).toEqual({
      backend: null,
      verdict: "error: orchestrator timeout",
      queueItemCount: 0,
    });
    // u2's lookup isn't blocked/lost by u1's rejection.
    expect(stuck[1]?.orchestrator?.verdict).toBe("no-job");
  });

  test("batches in groups of 10 — the 11th candidate isn't queried until batch 2", async () => {
    const stuck = Array.from({ length: 11 }, (_, i) => stuckOrder(`u${i}`));
    const seenAtCall: number[][] = [];
    let inFlight = 0;
    await crossCheckOrders(stuck, async () => {
      inFlight++;
      seenAtCall.push([inFlight]);
      await Promise.resolve();
      inFlight--;
      return [];
    });
    // First 10 all start concurrently (inFlight reaches 10) before the 11th starts.
    const maxConcurrent = Math.max(...seenAtCall.map(([n]) => n ?? 0));
    expect(maxConcurrent).toBe(10);
  });

  test("reports progress per completed batch, counting only real candidates", async () => {
    // 11 with a uid + 1 without: the uid-less one is never queried, so it's not in the total.
    const stuck = [
      ...Array.from({ length: 11 }, (_, i) => stuckOrder(`u${i}`)),
      stuckOrder(undefined),
    ];
    const steps: string[] = [];
    await crossCheckOrders(
      stuck,
      async () => [],
      (done, total) => steps.push(`${done}/${total}`),
    );
    expect(steps).toEqual(["10/11", "11/11"]);
  });
});

describe("itemsOrThrow", () => {
  const across = (
    items: QueueItemCore[],
    searchErrors?: Partial<Record<Orchestrator, string>>,
  ): FindOrderAcrossResult => ({
    count: items.length,
    jobIds: {},
    items,
    searched: ["uipath", APEX],
    backends: {},
    ...(searchErrors ? { searchErrors } : {}),
  });

  test("items found: the verdict stands on them even if another orchestrator's search failed", () => {
    const items = [qItem()];
    expect(itemsOrThrow(across(items, { [APEX]: "boom" }))).toBe(items);
  });

  test("nothing found and a search failed: unknown, not no-job — throws naming each failed orchestrator", () => {
    expect(() => itemsOrThrow(across([], { uipath: "503", [APEX]: "login failed" }))).toThrow(
      `uipath: 503; ${APEX}: login failed`,
    );
  });

  test("nothing found and nothing failed: an empty list (no-job)", () => {
    expect(itemsOrThrow(across([]))).toEqual([]);
  });
});
````

- [ ] **Step 2: Run it to verify it fails**
Run: `bun test src/copilot/sweep.test.ts` — Expected: FAIL (`crossCheckOrders` / `itemsOrThrow` are not exported by `./sweep.js`).

- [ ] **Step 3: Implement**

`src/copilot/sweep.ts` — **lines 1-10** become:

````ts
// find_stuck_orders: page recent orders in an env and flag the ones sitting in a
// non-terminal ("stuck") status, optionally correlating each to its queue item(s) on
// the orchestrator(s). Read-only. Reuses the order-filter scan (same shape as
// find_clone_candidates) and the cross-orchestrator queue-item search in
// orchestrator/find-order.ts.

import type { Env } from "../config/config.js";
import {
  type FindOrderAcrossResult,
  findOrderAcross,
  selectBackends,
} from "../orchestrator/find-order.js";
import type { EnabledOrchestrator } from "../orchestrator/registry.js";
import type { Orchestrator, QueueItemCore } from "../orchestrator/types.js";
import { chunk, type StepProgress } from "../shared/util.js";
import { type BeOrder, filterOrders, ORDER_MODE } from "./copilot-client.js";
import { connect } from "./session.js";
````

**Lines 16-31** (`StuckOrder`) become:

````ts
export interface OrderVerdict {
  backend: Orchestrator | null; // orchestrator holding the newest queue item; null = none found / lookup failed
  verdict: string;
  queueItemCount: number;
  queueItemStatus?: string | undefined; // PHI-safe: New/InProgress/Failed/Retried/Successful
  exceptionType?: "business" | "application" | undefined;
}

export interface StuckOrder {
  orderUid?: string | undefined;
  status?: string | undefined;
  type?: string | undefined;
  facility?: string | undefined;
  insurance?: string | undefined;
  ageHours: number | null;
  orchestrator?: OrderVerdict | undefined;
}
````

**Lines 33-43** (`FindStuckArgs`): replace line 39 `crossCheckUipath?: boolean | undefined;` with

````ts
  crossCheck?: boolean | undefined;
  orchestrator?: EnabledOrchestrator | undefined; // omitted = every orchestrator
````

**Lines 86-116** (`uipathVerdict`) become:

````ts
// Summarize an order's queue item(s) into a coarse verdict. The queue item is the
// source of truth for what happened to the order: its status directly determines
// faulted (Failed) / running (InProgress) / stuck-but-succeeded (Successful), and a
// New item with no job attempt means "queued, not yet picked up". Items arrive newest
// first (createdAt desc), so [0] is the latest item and its jobs[0] the latest attempt.
// Priority: never-started > faulted > running > queued-not-picked-up > successful-order-stuck.
// Only the latest attempt of the latest item decides never-started: an earlier reaped
// attempt that a retry replaced is history, not the order's current state.
const VERDICT_RULES: [verdict: string, matches: (items: QueueItemCore[]) => boolean][] = [
  ["job-never-started", (items) => items[0]?.jobs[0]?.neverStarted === true],
  ["job-faulted", (items) => items.some((q) => q.status === "Failed")],
  ["job-running", (items) => items.some((q) => q.status === "InProgress")],
  ["queued-not-picked-up", (items) => items.some((q) => q.status === "New" && q.jobs.length === 0)],
  ["job-successful-order-stuck", (items) => items.every((q) => q.status === "Successful")],
];

function orderVerdict(items: QueueItemCore[]): OrderVerdict {
  const newest = items[0];
  if (!newest) return { backend: null, verdict: "no-job", queueItemCount: 0 };
  const exceptionType = items.find((q) => q.exceptionType)?.exceptionType;
  return {
    backend: newest.orchestrator,
    verdict: VERDICT_RULES.find(([, matches]) => matches(items))?.[0] ?? "job-found",
    queueItemCount: items.length,
    queueItemStatus: newest.status,
    ...(exceptionType ? { exceptionType } : {}),
  };
}
````

**Lines 160-179** (the `if (args.crossCheckUipath) { … }` block) become:

````ts
  if (args.crossCheck) {
    const backends = selectBackends(args.orchestrator);
    const top = args.top ?? 50;
    // Offset past the page scan so progress stays monotonic across both phases. The
    // candidate count isn't known until the scan finishes, so the total grows here —
    // allowed (total is advisory), and better than reporting the cross-check silently.
    await crossCheckOrders(
      stuck,
      async (orderUid) =>
        itemsOrThrow(
          await findOrderAcross(
            {
              env,
              orderUid,
              since: queueSearchSince(args.since, creationDateByUid.get(orderUid)),
              top,
              includeSpecificContent: false,
            },
            backends,
          ),
        ),
      (done, total, label) => onProgress?.(scanPages + done, scanPages + total, label),
    );
  }
````

**Lines 184-210** (comment + `crossCheckUipath`) become:

````ts
// findOrderAcross isolates one orchestrator's failure into searchErrors. For the sweep an
// order with no items AND a failed search is unknown, not "no-job": throw so
// crossCheckOrders records it as that order's error verdict. With items found, the
// verdict stands on them.
export function itemsOrThrow(found: FindOrderAcrossResult): QueueItemCore[] {
  const failed = Object.entries(found.searchErrors ?? {});
  if (found.count === 0 && failed.length) {
    throw new Error(failed.map(([name, message]) => `${name}: ${message}`).join("; "));
  }
  return found.items;
}

// Bounded-concurrency batches (same chunk()+Promise.allSettled shape as uipath.ts's
// fetchJobsForKeys) instead of one Orchestrator lookup at a time — a real per-order
// failure lands on that order's orchestrator.verdict, never lost and never blocking the
// rest of the batch. `correlate` is injected so this is unit-testable without HTTP and
// must return the order's items newest first.
export async function crossCheckOrders(
  stuck: StuckOrder[],
  correlate: (orderUid: string) => Promise<QueueItemCore[]>,
  onProgress?: StepProgress,
): Promise<void> {
  const candidates = stuck.filter((s): s is StuckOrder & { orderUid: string } => !!s.orderUid);
  let checked = 0;
  for (const batch of chunk(candidates, 10)) {
    const results = await Promise.allSettled(batch.map((s) => correlate(s.orderUid)));
    batch.forEach((s, i) => {
      const r = results[i];
      s.orchestrator =
        r?.status === "fulfilled"
          ? orderVerdict(r.value)
          : {
              backend: null,
              verdict: `error: ${r?.reason instanceof Error ? r.reason.message : String(r?.reason)}`,
              queueItemCount: 0,
            };
    });
    checked += batch.length;
    onProgress?.(checked, candidates.length, `cross-checked ${checked} order(s) against orchestrators`);
  }
}
````

`src/server.ts` — import: `import { findStuckOrders } from "./copilot/sweep.js";` is unchanged. Replace the entire `find_stuck_orders` registration block (`// ---- find_stuck_orders ---…` through the `);` before `// ---- diff_settings ---…`) with:

````ts
// ---- find_stuck_orders ---------------------------------------------------
server.registerTool(
  "find_stuck_orders",
  {
    title: "Find stuck Copilot orders",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Scan recent orders in an env and flag the ones sitting in a non-terminal ('stuck') status " +
      "(default inProgress/incomplete/pending). Optionally (`crossCheck`) correlate each to its queue " +
      "item(s) on the orchestrator(s) — omit `orchestrator` to check every orchestrator, since where " +
      "the item actually sits is the truth — for a coarse verdict: no-job / job-never-started / " +
      "job-faulted / job-running / queued-not-picked-up / job-successful-order-stuck / job-found " +
      "(or 'error: …' when the lookup failed, which is NOT 'no-job'). Verdicts come from the newest " +
      "queue item's status and latest attempt (PHI-safe — member data / tokens are never surfaced). " +
      "job-never-started = the latest attempt was reaped before any worker ran it (e.g. a missing " +
      "credential on Apex): a platform/config problem, not order data. READ-ONLY. Returns {env, " +
      "scanned, statuses, found, stuck:[{orderUid,status,ageHours,orchestrator?:{backend," +
      "verdict,queueItemCount,queueItemStatus?,exceptionType?}}]} where `backend` is the " +
      "orchestrator holding the newest queue item (null when none / lookup failed).",
    inputSchema: {
      env: z.enum(["prod", "pre_prod"]).describe("Which env to scan (required)"),
      profile: z
        .string()
        .min(1)
        .describe("Credential profile / account name from config (required)"),
      scanPages: z
        .number()
        .int()
        .min(1)
        .max(20)
        .optional()
        .default(8)
        .describe("Pages of 50 recent orders to scan"),
      statuses: z
        .array(z.string())
        .optional()
        .describe("Override the 'stuck' status set (default inProgress/incomplete/pending)"),
      olderThanHours: z
        .number()
        .min(0)
        .optional()
        .describe(
          "Only flag orders at least this many hours old (needs a parseable date on the row)",
        ),
      crossCheck: z
        .boolean()
        .optional()
        .default(false)
        .describe("Correlate each stuck order to its queue item(s)/job(s) for a verdict"),
      orchestrator: orchestratorArg
        .optional()
        .describe(
          "With crossCheck: check only this orchestrator (uipath | apex); omit to check every orchestrator",
        ),
      since: z
        .string()
        .optional()
        .describe(
          "Explicit ISO floor for the queue-item search. Each order's own creationDate " +
            "already tightens its search to that date (queue items can't predate the order), so " +
            "this is only needed to push the floor even later, or when crossCheck is used " +
            "without a BE-sourced creationDate",
        ),
      top: z
        .number()
        .int()
        .min(1)
        .max(500)
        .optional()
        .default(50)
        .describe("Max queue items to consider per order, per orchestrator, in the cross-check"),
    },
  },
  async (
    { env, profile, scanPages, statuses, olderThanHours, crossCheck, orchestrator, since, top },
    extra,
  ) => {
    try {
      return ok(
        await findStuckOrders({
          env,
          profile: profile ?? null,
          scanPages,
          statuses,
          olderThanHours,
          crossCheck,
          orchestrator,
          since,
          top,
          onProgress: stepProgress(extra),
        }),
      );
    } catch (e) {
      return toolError("find_stuck_orders", e, VERSION);
    }
  },
);
````

`src/server.test.ts` — add this entry to the `cases` map (next to `find_order_queue_items`):

````ts
    find_stuck_orders: {
      valid: { env: "prod", profile: "ossm", crossCheck: true }, // orchestrator omitted = every orchestrator
      invalid: { env: "prod", profile: "ossm", orchestrator: "nope" }, // unknown orchestrator
    },
````

`src/mcp/prompts.ts` — replace the whole `triage-stuck-orders` prompt callback body's returned text (the `userText(...)` call inside `({ profile }) => { const p = …; return userText(…); }`, pre-Task-5 lines 169-189) with:

````ts
      return userText(
        `Triage stuck Copilot orders${p}.\n` +
          `1. Call find_stuck_orders${p} with crossCheck=true to list orders stuck in a non-terminal status; each ` +
          `carries orchestrator.{backend,verdict} (omit the orchestrator argument to check every orchestrator).\n` +
          `2. For the stuck orders whose orchestrator.verdict warrants a closer look (job-faulted, job-never-started, ` +
          `job-running, job-found, job-successful-order-stuck), call find_order_queue_items per order to get each ` +
          `order's jobIds per orchestrator, then diagnose them in ONE get_job call per orchestrator with ` +
          `orchestrator=<that orchestrator>, jobIds=[...] (up to 25 ids per call) and includeLogDigest=true to see ` +
          `whether each job failed, is still running, or never started. job-never-started means the latest attempt ` +
          `was reaped before a worker ran it (e.g. a missing credential) — a platform/config fix, not an order-data ` +
          `fix. An "error: …" verdict means the lookup failed, not that the order is fine — retry it.\n` +
          `3. Group them by root cause and recommend a remediation per group (retry, re-clone, fix data, escalate).\n` +
          `Do not take any write action without the user's explicit go-ahead.`,
      );
````

- [ ] **Step 4: Run tests** — `bun test src/copilot/sweep.test.ts src/server.test.ts src/mcp/prompts.test.ts` → PASS
- [ ] **Step 5: Full gate** — `bun run typecheck && bun test && bunx biome check src` → all green (`rg -n "crossCheckUipath|\.uipath\b" src` must show no remaining hits in `src/copilot` or `src/server.ts`)
- [ ] **Step 6: Commit**
```bash
git add src/copilot/sweep.ts src/copilot/sweep.test.ts src/server.ts src/server.test.ts src/mcp/prompts.ts
git commit -m "feat(orchestrator)!: find_stuck_orders cross-checks every orchestrator; job-never-started" -m "crossCheckUipath becomes crossCheck (optional orchestrator, omitted = all) and the uipath output field becomes orchestrator{backend, verdict, …}. Verdicts read the newest item and its latest attempt; a never-started latest attempt (Apex missing_credential reap) is its own verdict, and a failed search with nothing found is an error verdict rather than a false no-job."
```

---

### Task 15: Apex fault issue + `report-faulted-jobs` prompt

**Files:**
- Create: `src/apex/faults.ts`
- Test: `src/apex/faults.test.ts`
- Modify: `src/apex/backend.ts` (append `apexBuildFaultedJobIssue` + two private helpers; merge the listed imports into the existing import block)
- Test: `src/apex/backend-fault-issue.test.ts` (separate file so Task 11's `backend.test.ts` is not edited)
- Modify: `src/mcp/prompts.ts` (rename + rework the `report-faulted-uipath-jobs` prompt, pre-Task-5 lines 219-261; add the `ENABLED_ORCHESTRATORS` import if Task 5 has not)
- Modify: `src/mcp/prompts.test.ts` (line 20)
- Modify: `src/server.ts` (the prompt name inside the `build_faulted_job_issue` description, pre-Task-5 line 1097)
- Modify: `src/uipath/faults.ts` (header comment, line 3 pre-Task-1)

**Interfaces:**
- Consumes: `DEFAULT_REPO`, `FaultedJobIssue`, `faultSearchQuery(repo, signature)`, `firstLine`, `signatureMarker(signature)` (`src/orchestrator/faults.ts`, Task 1); `JobAttempt`, `JobCore`, `JobFault`, `JobLog` (`types.ts`); `FaultIssueArgs`, `FaultedJobIssueResult` (`backend.ts`); from `src/apex/api.ts` (Task 10): `getJob(env, folder: string | undefined, id): Promise<ApexJobDetail>` (throws `ApexApiError` on 404), `listJobs(env, folder, { reference }, max): Promise<ApexJobRow[]>` (newest first; `ApexJobRow.queue_item_id` / `queue_item_reference` are `string | null`); from `src/apex/map.ts` (Task 10): `ApexJobDetails`, `toApexJobCore(row, link)`, `toApexJobAttempt(row): JobAttempt`; `apexDashboardLink(path)` (`client.ts`, Task 7); `readJobLog(env, folder, jobId): Promise<ParsedJobLog | null>` (`apex/backend.ts`, Task 11: `null` = no `job.log` artifact listed, throws on HTTP failure); `ExpectedError`; test harness `src/apex/test-support.ts` (Task 7: `useApexStub`, `json`, `FOLDER_IDS`).
- Produces:
  - `src/apex/faults.ts`: `APEX_DEFAULT_LABELS`, `ApexFault`, `ApexFaultJob`, `ApexFaultOptions`, `formatApexFaultIssue(job: ApexFaultJob, rows: JobLog[], opts: ApexFaultOptions): FaultedJobIssue`. `ApexFaultJob` is `JobCore<Pick<ApexJobDetails, "errorClass" | "outcome" | "exitCode">> & { fault: ApexFault }`, i.e. any `JobCore<ApexJobDetails>` whose `fault` carries a message and signature (stricter than the contract's `JobCore<ApexJobDetails>` only on `fault`; Task 17 passes `apexBuildFaultedJobIssue` as `apexBackend.buildFaultedJobIssue`).
  - `src/apex/backend.ts`: `apexBuildFaultedJobIssue(args: FaultIssueArgs): Promise<FaultedJobIssueResult>`.
- The prompt is renamed `report-faulted-jobs` (argument `orchestrator`, required, completable from `ENABLED_ORCHESTRATORS`; Task 17 swaps it to `ORCHESTRATORS` along with every other `EnabledOrchestrator` usage).

Design notes:
- **Not-a-fault is an error, not a payload.** `apexFault` returns `null` for a completed/running job; building an issue for it would file a bug report about a non-fault, so `apexBuildFaultedJobIssue` throws an `ExpectedError` naming the job's state. For the same reason `found` is always `true` on return: a wrong id is the 404 `ApexApiError` from `getJob` (user-actionable, with the path), not a synthetic stub (UiPath's stub behavior is deliberately not copied).
- **Attempt chain** = `/jobs?reference=<item reference>` (the only verified item filter), narrowed by `queue_item_id` because two items may share a reference; with no reference the chain is just this job. Mapped by Task 10's `toApexJobAttempt`; shown oldest first.
- **Logs are best-effort**: any `readJobLog` failure becomes `logsError`; a missing `job.log` (never-started reaps and `artifact_policy` leave none) is not an error.
- **Log tail** = warn/error rows only, last 4000 characters (the tail, because the rows nearest the end explain the fault).
- The signature is exactly `job.fault.signature` (`apex:`-prefixed by `apexFault`, D7), so it cannot drift from the fault classification and is stable across job ids, GUIDs and timestamps.

- [ ] **Step 1: Write the failing tests**

````ts
// src/apex/faults.test.ts
import { describe, expect, test } from "bun:test";
import {
  DEFAULT_REPO,
  faultSearchQuery,
  signatureMarker,
} from "../orchestrator/faults.js";
import type { JobAttempt, JobLog } from "../orchestrator/types.js";
import { APEX_DEFAULT_LABELS, type ApexFaultJob, formatApexFaultIssue } from "./faults.js";

const APEX = "apex" as const;
const SIG = "apex:business_failure: portal rejected order <n> at <ts>";

const faultJob = (over: Partial<ApexFaultJob> = {}): ApexFaultJob => ({
  orchestrator: APEX,
  id: "job-2",
  state: "Faulted",
  rawState: "failed",
  terminal: true,
  process: { id: "wf-1", name: "optum-auth-submit", version: "1.4.2" },
  queueItem: { queueId: "q-1", itemId: "item-1", reference: "0123456789" },
  createdAt: "2026-10-11T09:59:00Z",
  startedAt: "2026-10-11T10:00:00Z",
  endedAt: "2026-10-11T10:01:00Z",
  durationMs: 60000,
  attempt: 2,
  retryOfJobId: "job-1",
  fault: {
    message: "Portal rejected order 998877 at 2026-10-11T10:00:09Z",
    signature: SIG,
    class: "business_failure",
  },
  link: "https://apex.example.com/jobs/job-2",
  details: { errorClass: "permanent", outcome: "business_failure", exitCode: 21 },
  ...over,
});

const attempts: JobAttempt[] = [
  { orchestrator: APEX, id: "job-2", state: "Faulted", neverStarted: false },
  { orchestrator: APEX, id: "job-1", state: "Stopped", neverStarted: false },
];

const row = (Level: string, Message: string): JobLog => ({
  Level,
  Message,
  TimeStamp: "2026-10-11T10:00:00Z",
});

const OPTS = { env: "prod" as const, attempts };

describe("formatApexFaultIssue", () => {
  test("titles by class + first message line, defaults repo/labels, carries the signature everywhere it must", () => {
    const issue = formatApexFaultIssue(faultJob(), [], OPTS);
    expect(issue.title).toBe(
      "[Apex Fault] business_failure: Portal rejected order 998877 at 2026-10-11T10:00:09Z (prod)",
    );
    expect(issue.repo).toBe(DEFAULT_REPO);
    expect(issue.labels).toEqual(APEX_DEFAULT_LABELS);
    expect(APEX_DEFAULT_LABELS).toEqual(["apex-fault", "bug"]);
    expect(issue.faultSignature).toBe(SIG);
    expect(issue.searchQuery).toBe(faultSearchQuery(DEFAULT_REPO, SIG));
    expect(issue.body).toContain(signatureMarker(SIG));
    expect(issue.body).toContain("optum-auth-submit@1.4.2");
    expect(issue.body).toContain("https://apex.example.com/jobs/job-2");
    expect(issue.recurrenceComment).toContain("job-2");
    expect(issue.recurrenceComment).toContain("prod");
  });

  test("a null fault class is titled 'unclassified'", () => {
    const issue = formatApexFaultIssue(
      faultJob({ fault: { message: "boom", signature: "apex:unclassified: boom", class: null } }),
      [],
      OPTS,
    );
    expect(issue.title).toBe("[Apex Fault] unclassified: boom (prod)");
  });

  test("the attempt chain is listed oldest first and marks the faulted job", () => {
    const { body } = formatApexFaultIssue(faultJob(), [], OPTS);
    expect(body).toContain("1. `job-1` — Stopped");
    expect(body).toContain("2. `job-2` — Faulted (this job)");
    expect(body.indexOf("1. `job-1`")).toBeLessThan(body.indexOf("2. `job-2`"));
  });

  test("a never-started attempt is flagged in the chain", () => {
    const { body } = formatApexFaultIssue(faultJob(), [], {
      env: "prod",
      attempts: [{ orchestrator: APEX, id: "job-2", state: "Faulted", neverStarted: true }],
    });
    expect(body).toContain("`job-2` — Faulted, never started (this job)");
  });

  test("exit code 0 is shown; null outcome/class/exit code are omitted", () => {
    const zero = formatApexFaultIssue(
      faultJob({ details: { errorClass: null, outcome: null, exitCode: 0 } }),
      [],
      OPTS,
    );
    expect(zero.body).toContain("Exit code");
    expect(zero.body).not.toContain("Outcome");
    expect(zero.body).not.toContain("Error class");

    const none = formatApexFaultIssue(
      faultJob({ details: { errorClass: null, outcome: null, exitCode: null } }),
      [],
      OPTS,
    );
    expect(none.body).not.toContain("Exit code");
  });

  test("a null process version prints the bare process name", () => {
    const { body } = formatApexFaultIssue(
      faultJob({ process: { id: "wf-1", name: "optum-auth-submit", version: null } }),
      [],
      OPTS,
    );
    expect(body).toContain("optum-auth-submit");
    expect(body).not.toContain("optum-auth-submit@");
  });

  test("custom repo and labels win; an empty labels array falls back to the default", () => {
    const custom = formatApexFaultIssue(faultJob(), [], {
      ...OPTS,
      repo: "acme/other",
      labels: ["triage"],
    });
    expect(custom.repo).toBe("acme/other");
    expect(custom.labels).toEqual(["triage"]);
    expect(custom.searchQuery).toBe(faultSearchQuery("acme/other", SIG));

    expect(formatApexFaultIssue(faultJob(), [], { ...OPTS, labels: [] }).labels).toEqual(
      APEX_DEFAULT_LABELS,
    );
  });

  test("signature, searchQuery and marker are stable across job ids, timestamps and message ids", () => {
    const a = formatApexFaultIssue(faultJob(), [], OPTS);
    const b = formatApexFaultIssue(
      faultJob({
        id: "job-9",
        createdAt: "2026-10-12T01:02:03Z",
        link: "https://apex.example.com/jobs/job-9",
        fault: {
          message: "Portal rejected order 112233 at 2026-10-12T01:02:09Z",
          signature: SIG,
          class: "business_failure",
        },
      }),
      [],
      { env: "prod", attempts: [] },
    );
    expect(b.faultSignature).toBe(a.faultSignature);
    expect(b.searchQuery).toBe(a.searchQuery);
    expect(b.body).toContain(signatureMarker(SIG));
    expect(b.body).not.toBe(a.body);
    expect(b.recurrenceComment).not.toBe(a.recurrenceComment);
  });

  test("the log tail keeps only warn/error rows and, past the cap, the newest ones", () => {
    const { body } = formatApexFaultIssue(
      faultJob(),
      [
        row("INFO", "login ok"),
        row("ERROR", `first-${"x".repeat(5000)}`),
        row("warning", "retrying selector"),
        row("ERROR", "last-row"),
      ],
      OPTS,
    );
    expect(body).not.toContain("login ok");
    expect(body).toContain("retrying selector");
    expect(body).toContain("last-row");
    expect(body).not.toContain("first-");
  });

  test("only INFO rows leave an empty tail, not a crash", () => {
    const { body } = formatApexFaultIssue(faultJob(), [row("INFO", "only info")], OPTS);
    expect(body).not.toContain("only info");
  });
});
````

````ts
// src/apex/backend-fault-issue.test.ts
import { describe, expect, test } from "bun:test";
import { ExpectedError } from "../shared/expected-error.js";
import { apexBuildFaultedJobIssue } from "./backend.js";
import { FOLDER_IDS, json, useApexStub } from "./test-support.js";

type Stub = ReturnType<typeof useApexStub>;

const JOB = "11111111-aaaa-bbbb-cccc-000000000001";
const PARENT = "11111111-aaaa-bbbb-cccc-000000000000";

const rawJob = (over: Record<string, unknown> = {}): Record<string, unknown> => ({
  id: JOB,
  status: "failed",
  workflow_id: "wf-1",
  folder_id: FOLDER_IDS["Authorization"],
  workflow_name: "optum-auth-submit",
  workflow_version: "1.4.2",
  queue_id: "q-1",
  queue_name: "OPTUM auth submit queue",
  queue_item_id: "item-1",
  queue_item_reference: "0123456789",
  match_key: "automationId",
  match_value: "auto-1",
  priority: "normal",
  source: "queue",
  idempotency_key: null,
  credential_id: "cred-1",
  credential_name: "svc-account",
  worker_id: "worker-1",
  retry_of_job_id: null,
  attempt_number: 1,
  error_class: "permanent",
  error_message: "Portal rejected order 998877 at 2026-10-11T10:00:09Z",
  exit_code: 21,
  outcome: "business_failure",
  outcome_label: "Business failure",
  needs_review: false,
  callback_type: null,
  callback_status: null,
  started_at: "2026-10-11T10:00:00Z",
  claimed_at: "2026-10-11T09:59:59Z",
  completed_at: "2026-10-11T10:01:00Z",
  next_eligible_at: null,
  trace_id: null,
  created_at: "2026-10-11T09:59:00Z",
  updated_at: "2026-10-11T10:01:00Z",
  duration_seconds: 60,
  effective_timeout_seconds: 900,
  input_payload: {},
  result_payload: null,
  ...over,
});

const page = (items: unknown[]) =>
  json({ items, total: items.length, page: 1, page_size: 200, total_pages: 1 });

// Routes match by path prefix and the latest registration wins, so the broad detail
// route goes first and the narrower artifacts/list routes override it.
const serveJob = (
  apex: Stub,
  id: string,
  detail: Record<string, unknown>,
  attempts: Record<string, unknown>[],
): void => {
  apex.route("GET", `/jobs/${id}`, () => json(detail));
  apex.route("GET", "/jobs?", () => page(attempts));
  apex.route("GET", `/jobs/${id}/artifacts`, () => json([]));
};

const LOG = [
  "=== apex job x started (workflow=optum-auth-submit runtime=python) ===",
  '{"v":1,"timestamp":"2026-10-11T10:00:01Z","level":"INFO","portal":"OPTUM","message":"login ok"}',
  '{"v":1,"timestamp":"2026-10-11T10:00:09Z","level":"ERROR","portal":"OPTUM","message":"selector not found"}',
  "[apex-adapter] result=FAILED reason=- exit=21 outcomes=-",
  "=== apex job x finished: exit_code=21 timed_out=False cancelled=False suspended=False ===",
].join("\n");

const serveLog = (apex: Stub, id: string, text: string): void => {
  apex.route("GET", `/jobs/${id}/artifacts`, () =>
    json([
      {
        id: "art-1",
        key: `jobs/${id}/job.log`,
        filename: "job.log",
        artifact_type: "logs",
        size_bytes: text.length,
        last_modified: "2026-10-11T10:01:00Z",
        content_type: "text/plain",
        download_url: `/api/v1/jobs/${id}/artifacts/art-1`,
      },
    ]),
  );
  apex.route("GET", `/jobs/${id}/artifacts/art-1`, () => new Response(text, { status: 200 }));
};

describe("apexBuildFaultedJobIssue", () => {
  const apex = useApexStub();

  test("builds the payload from the job, its attempt chain and the warn/error log rows", async () => {
    const detail = rawJob({ retry_of_job_id: PARENT, attempt_number: 2 });
    serveJob(apex, JOB, detail, [
      detail,
      rawJob({ id: PARENT, status: "cancelled", attempt_number: 1, error_class: null }),
      rawJob({ id: "other-item-job", queue_item_id: "item-2" }), // same reference, different item
    ]);
    serveLog(apex, JOB, LOG);

    const out = await apexBuildFaultedJobIssue({ env: "prod", jobId: JOB });

    expect(out.found).toBe(true);
    expect(out.logsError).toBeUndefined();
    expect(out.title).toBe(
      "[Apex Fault] business_failure: Portal rejected order 998877 at 2026-10-11T10:00:09Z (prod)",
    );
    expect(out.labels).toEqual(["apex-fault", "bug"]);
    expect(out.faultSignature.startsWith("apex:")).toBe(true);
    expect(out.body).toContain(`1. \`${PARENT}\``);
    expect(out.body).toContain(`2. \`${JOB}\``);
    expect(out.body).not.toContain("other-item-job");
    expect(out.body).toContain("selector not found");
    expect(out.body).not.toContain("login ok");
    expect(apex.callsTo("GET", "/jobs?")[0]?.path).toContain("reference=0123456789");
  });

  test("a log fetch failure surfaces as logsError and the issue is still built", async () => {
    serveJob(apex, JOB, rawJob(), [rawJob()]);
    apex.route("GET", `/jobs/${JOB}/artifacts`, () => json({ detail: "Not Found" }, 404));

    const out = await apexBuildFaultedJobIssue({ env: "prod", jobId: JOB });

    expect(out.found).toBe(true);
    expect(out.logsError).toBeTruthy();
    expect(out.title).toStartWith("[Apex Fault]");
  });

  test("a never-started job (no job.log artifact) is not a log error and is flagged in the chain", async () => {
    const reaped = rawJob({
      error_class: "missing_credential",
      error_message: "No credential bound for portal",
      outcome: null,
      outcome_label: null,
      exit_code: null,
      started_at: null,
      claimed_at: null,
      worker_id: null,
      credential_id: null,
    });
    serveJob(apex, JOB, reaped, [reaped]);

    const out = await apexBuildFaultedJobIssue({ env: "prod", jobId: JOB });

    expect(out.logsError).toBeUndefined();
    expect(out.title).toBe("[Apex Fault] missing_credential: No credential bound for portal (prod)");
    expect(out.body).toContain("never started");
  });

  test("a job with no queue item reference lists only itself and never queries /jobs", async () => {
    serveJob(apex, JOB, rawJob({ queue_item_reference: null, queue_item_id: null }), []);

    const out = await apexBuildFaultedJobIssue({ env: "prod", jobId: JOB });

    expect(apex.callsTo("GET", "/jobs?")).toHaveLength(0);
    expect(out.body).toContain(`1. \`${JOB}\``);
  });

  test("a job that did not fault is refused instead of filed as a bug", async () => {
    serveJob(
      apex,
      JOB,
      rawJob({
        status: "completed",
        error_class: null,
        error_message: null,
        exit_code: 0,
        outcome: "success",
        outcome_label: "Success",
      }),
      [],
    );

    await expect(apexBuildFaultedJobIssue({ env: "prod", jobId: JOB })).rejects.toBeInstanceOf(
      ExpectedError,
    );
  });

  test("the same fault on another job (new id, timestamps, message ids) keeps one signature", async () => {
    const other = "22222222-aaaa-bbbb-cccc-000000000002";
    serveJob(
      apex,
      JOB,
      rawJob({
        error_message:
          "Selector not found for order 998877 at 2026-10-11T10:00:09Z (job 1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d)",
      }),
      [],
    );
    const a = await apexBuildFaultedJobIssue({ env: "prod", jobId: JOB });

    serveJob(
      apex,
      other,
      rawJob({
        id: other,
        created_at: "2026-10-12T01:00:00Z",
        error_message:
          "Selector not found for order 112233 at 2026-10-12T01:00:09Z (job 9f8e7d6c-5b4a-3210-fedc-ba9876543210)",
      }),
      [],
    );
    const b = await apexBuildFaultedJobIssue({ env: "prod", jobId: other });

    expect(b.faultSignature).toBe(a.faultSignature);
    expect(b.searchQuery).toBe(a.searchQuery);
    expect(b.body).not.toBe(a.body);
  });
});
````

- [ ] **Step 2: Run them to verify they fail**
Run: `bun test src/apex/faults.test.ts src/apex/backend-fault-issue.test.ts` — Expected: FAIL (`./faults.js` does not exist; `apexBuildFaultedJobIssue` is not exported by `./backend.js`).

- [ ] **Step 3: Implement**

New file:

````ts
// src/apex/faults.ts
// Apex fault -> ready-to-post GitHub issue payload. Pure: fully determined by its inputs,
// posts nothing (the report-faulted-jobs prompt hands the payload to the host's GitHub MCP
// server, which creates the issue or comments on a matching one).

import type { Env } from "../config/config.js";
import {
  DEFAULT_REPO,
  type FaultedJobIssue,
  faultSearchQuery,
  firstLine,
  signatureMarker,
} from "../orchestrator/faults.js";
import type { JobAttempt, JobCore, JobLog } from "../orchestrator/types.js";
import type { ApexJobDetails } from "./map.js";

export const APEX_DEFAULT_LABELS = ["apex-fault", "bug"];

// apexFault's output for a faulted detail: message and signature are always set there.
export interface ApexFault {
  message: string;
  signature: string; // already "apex:"-prefixed and normalized by apexFault (D7)
  class: string | null;
}

export type ApexFaultJob = JobCore<Pick<ApexJobDetails, "errorClass" | "outcome" | "exitCode">> & {
  fault: ApexFault;
};

export interface ApexFaultOptions {
  env: Env;
  attempts: JobAttempt[]; // newest first, as /jobs lists them
  repo?: string | undefined;
  labels?: string[] | undefined;
}

const LOG_TAIL_CAP = 4000;
const SIGNIFICANT_LEVELS = new Set(["warn", "warning", "error", "critical", "fatal"]);

const fmtLog = (l: JobLog): string => `${l.TimeStamp} [${l.Level}] ${l.Message}`.trim();

// The cap keeps the tail: the rows nearest the end of the run explain the fault.
const logTail = (rows: JobLog[]): string =>
  rows
    .filter((l) => SIGNIFICANT_LEVELS.has(l.Level.toLowerCase()))
    .map(fmtLog)
    .join("\n")
    .slice(-LOG_TAIL_CAP);

const attemptLine = (a: JobAttempt, i: number, currentId: string): string =>
  `  ${i + 1}. \`${a.id}\` — ${a.state ?? "unknown"}${a.neverStarted ? ", never started" : ""}` +
  (a.id === currentId ? " (this job)" : "");

// Outcome / class / exit code are only known from the job detail; null means "not
// available", so it is left out rather than printed. Exit code 0 is a real value.
function classification(job: ApexFaultJob): string[] {
  const { outcome, errorClass, exitCode } = job.details;
  const entries: [string, string | number | null][] = [
    ["Outcome", outcome],
    ["Error class", errorClass],
    ["Exit code", exitCode],
  ];
  return entries.flatMap(([label, value]) => (value === null ? [] : [`- **${label}:** ${value}`]));
}

/**
 * Pure: build the GitHub issue payload for a faulted Apex job from its core, its
 * parsed log rows and its attempt chain.
 */
export function formatApexFaultIssue(
  job: ApexFaultJob,
  rows: JobLog[],
  opts: ApexFaultOptions,
): FaultedJobIssue {
  const repo = opts.repo ?? DEFAULT_REPO;
  const labels = opts.labels && opts.labels.length > 0 ? opts.labels : APEX_DEFAULT_LABELS;
  const { message, signature } = job.fault;
  const faultClass = job.fault.class ?? "unclassified";
  const process = job.process.version
    ? `${job.process.name}@${job.process.version}`
    : job.process.name;

  const body = [
    `**Apex job faulted** in \`${opts.env}\`.`,
    "",
    `- **Process:** ${process}`,
    `- **Job:** \`${job.id}\``,
    `- **State:** ${job.state} (\`${job.rawState}\`)`,
    ...classification(job),
    `- **Created:** ${job.createdAt}`,
    ...(job.endedAt ? [`- **Ended:** ${job.endedAt}`] : []),
    ...(job.link ? [`- **Dashboard:** ${job.link}`] : []),
    ...(opts.attempts.length > 0
      ? [
          "- **Attempts (oldest first):**",
          ...[...opts.attempts].reverse().map((a, i) => attemptLine(a, i, job.id)),
        ]
      : []),
    "",
    "**Error**",
    "```",
    message,
    "```",
    "",
    "<details><summary>Last warn/error log rows</summary>",
    "",
    "```",
    logTail(rows) || "(no warn/error log rows)",
    "```",
    "",
    "</details>",
    "",
    "_Filed by copilot-mcp `build_faulted_job_issue`. Recurrences of this fault are added as comments below._",
    signatureMarker(signature),
  ].join("\n");

  const recurrenceComment = [
    `Recurred in \`${opts.env}\` — same fault signature.`,
    "",
    `- **Job:** \`${job.id}\``,
    `- **Created:** ${job.createdAt}`,
    ...(job.link ? [`- **Dashboard:** ${job.link}`] : []),
    "",
    "```",
    firstLine(message),
    "```",
  ].join("\n");

  return {
    repo,
    title: `[Apex Fault] ${faultClass}: ${firstLine(message)} (${opts.env})`,
    body,
    labels,
    faultSignature: signature,
    searchQuery: faultSearchQuery(repo, signature),
    recurrenceComment,
  };
}
````

`src/apex/backend.ts` — merge these names into the file's existing import block (Task 11 already imports some of them; add only what is missing, do not duplicate):

````ts
import type { Env } from "../config/config.js";
import { ExpectedError } from "../shared/expected-error.js";
import type { FaultedJobIssueResult, FaultIssueArgs } from "../orchestrator/backend.js";
import type { JobAttempt, JobLog } from "../orchestrator/types.js";
import { getJob, listJobs } from "./api.js";
import { apexDashboardLink } from "./client.js";
import { formatApexFaultIssue } from "./faults.js";
import { toApexJobAttempt, toApexJobCore } from "./map.js";
// ApexJobDetail comes in as a type from "./api.js" alongside the functions above:
//   import { type ApexJobDetail, getJob, listJobs } from "./api.js";
// readJobLog is defined earlier in this file (Task 11).
````

Append to the end of `src/apex/backend.ts`:

````ts
// ---- build_faulted_job_issue ---------------------------------------------

const MAX_ATTEMPTS = 50;

// The attempts of one queue item, newest first: a retry is a NEW job on the same item, and
// /jobs?reference= is the only verified item filter. Two items can share a reference, so
// keep only this item's jobs.
async function jobAttempts(
  env: Env,
  folder: string | undefined,
  detail: ApexJobDetail,
): Promise<JobAttempt[]> {
  if (!detail.queue_item_reference) return [toApexJobAttempt(detail)];
  const rows = await listJobs(env, folder, { reference: detail.queue_item_reference }, MAX_ATTEMPTS);
  return rows.filter((r) => r.queue_item_id === detail.queue_item_id).map(toApexJobAttempt);
}

// Logs are a best-effort attachment: a failed fetch must not discard a valid job lookup
// (same convention as UiPath's buildFaultedJobIssue). A missing job.log is not a failure:
// never-started reaps and artifact_policy leave none.
async function bestEffortLogRows(
  env: Env,
  folder: string | undefined,
  jobId: string,
): Promise<{ rows: JobLog[]; error?: string }> {
  try {
    return { rows: (await readJobLog(env, folder, jobId))?.rows ?? [] };
  } catch (e) {
    return { rows: [], error: e instanceof Error ? e.message : String(e) };
  }
}

export async function apexBuildFaultedJobIssue(
  args: FaultIssueArgs,
): Promise<FaultedJobIssueResult> {
  const { env, jobId, folder } = args;
  const detail = await getJob(env, folder, jobId);
  const job = toApexJobCore(detail, apexDashboardLink(`/jobs/${detail.id}`));
  const { fault } = job;
  if (!fault?.message || !fault.signature) {
    throw new ExpectedError(
      `job ${jobId} is ${job.state}, not a fault — build_faulted_job_issue needs a failed, ` +
        "timed-out, lost or stopped job",
    );
  }
  const [attempts, logs] = await Promise.all([
    jobAttempts(env, folder, detail),
    bestEffortLogRows(env, folder, jobId),
  ]);
  const issue = formatApexFaultIssue(
    { ...job, fault: { ...fault, message: fault.message, signature: fault.signature } },
    logs.rows,
    { env, attempts, repo: args.repo, labels: args.labels },
  );
  return { ...issue, found: true, ...(logs.error ? { logsError: logs.error } : {}) };
}
````

`src/mcp/prompts.ts` — add to the imports (skip if Task 5 already added it; keep Biome's order):

````ts
import { ENABLED_ORCHESTRATORS } from "../orchestrator/registry.js";
````

Replace the whole `report-faulted-uipath-jobs` registration (from its `// ---- report-faulted-uipath-jobs ---…` comment through the `);` that closes `server.registerPrompt(`, i.e. everything before the final `}` of `registerPrompts`) with:

````ts
  // ---- report-faulted-jobs ------------------------------------------------
  server.registerPrompt(
    "report-faulted-jobs",
    {
      title: "Report faulted orchestrator jobs as GitHub issues",
      description:
        "Find faulted jobs on one orchestrator (uipath or apex; prod by default) and file each as a GitHub issue on the RPA repo, " +
        "commenting on an existing issue when the same fault is already filed. Requires a connected GitHub MCP server.",
      argsSchema: {
        orchestrator: completable(
          z.string().min(1).describe("Orchestrator to scan (uipath | apex) (required)"),
          () => [...ENABLED_ORCHESTRATORS],
        ),
        env: completable(
          z.string().optional().describe("Env to scan (prod | pre_prod); defaults to prod"),
          () => ENVS,
        ),
        since: z
          .string()
          .optional()
          .describe("ISO lower bound on job creation time (e.g. last 24h)"),
        top: z.string().optional().describe("Max recent jobs to scan (default 50)"),
        repo: z
          .string()
          .optional()
          .describe("Target repo owner/name (default Apex-Medical-AI-Inc/RPAPlaywright)"),
      },
    },
    ({ orchestrator, env, since, top, repo }) => {
      const e = env || "prod";
      const repoRef = repo || "Apex-Medical-AI-Inc/RPAPlaywright";
      const listArgs = [
        `orchestrator="${orchestrator}"`,
        `env="${e}"`,
        since ? `since="${since}"` : "",
        top ? `top=${top}` : "",
      ]
        .filter(Boolean)
        .join(", ");
      return userText(
        `File GitHub issues for faulted ${orchestrator} jobs in ${e} on ${repoRef}.\n` +
          `This requires a connected GitHub MCP server with write access to ${repoRef}; if none is available, stop and say so.\n` +
          `1. Call list_jobs with ${listArgs}, then keep only faulted jobs: state "Faulted" on either orchestrator, plus ` +
          `"Stopped" on uipath and "Timed Out" / "Lost" on apex. On apex a "Stopped" job is a cancellation or a retry ` +
          `parent superseded by a later attempt, not a fault — skip it; and when a Faulted job has a later attempt (another ` +
          `job whose retryOfJobId is its id), file only the final attempt, and only if it also faulted. If none remain, ` +
          `report that and stop.\n` +
          `2. For each faulted job, call build_faulted_job_issue with orchestrator="${orchestrator}", env="${e}" and that job's id as jobId${repo ? `, repo="${repo}"` : ""} to get the issue payload (it does NOT post to GitHub).\n` +
          `3. Using the GitHub MCP server, run the payload's searchQuery to find an existing OPEN issue for this fault (matched by faultSignature):\n` +
          `   - If one exists, add the payload's recurrenceComment to that issue's discussion (do NOT open a duplicate).\n` +
          `   - Otherwise create a new issue with the payload's title, body, and labels.\n` +
          `4. Report a per-job summary: faulted job id -> created issue (#/url) or commented on existing issue (#/url).\n` +
          `Only write to GitHub — never take any orchestrator write action.`,
      );
    },
  );
````

`src/mcp/prompts.test.ts` line 20 — replace `"report-faulted-uipath-jobs",` with `"report-faulted-jobs",` (alphabetical position is unchanged).

`src/server.ts` — in the `build_faulted_job_issue` description, replace the text

````ts
"alone) instead of failing the whole call. The caller (the report-faulted-uipath-jobs prompt) " +
````

with

````ts
"alone) instead of failing the whole call. The caller (the report-faulted-jobs prompt) " +
````

(pre-Task-5 line 1097; Task 5 leaves the prompt name as is, so the string is found verbatim). Also append, at the end of that tool's description string, nothing else: Task 5 already made it orchestrator-aware.

`src/uipath/faults.ts` — in the header comment (line 3 pre-Task-1), replace the token `report-faulted-uipath-jobs` with `report-faulted-jobs`:

````ts
// the `report-faulted-jobs` prompt hands the payload to the host's GitHub MCP
````

- [ ] **Step 4: Run tests** — `bun test src/apex/faults.test.ts src/apex/backend-fault-issue.test.ts src/mcp/prompts.test.ts src/server.test.ts` → PASS
- [ ] **Step 5: Full gate** — `bun run typecheck && bun test && bunx biome check src` → all green (`rg -n "report-faulted-uipath-jobs" src` must return nothing)
- [ ] **Step 6: Commit**
```bash
git add src/apex/faults.ts src/apex/faults.test.ts src/apex/backend.ts src/apex/backend-fault-issue.test.ts src/mcp/prompts.ts src/mcp/prompts.test.ts src/server.ts src/uipath/faults.ts
git commit -m "feat(apex): Apex fault issue payload and report-faulted-jobs prompt" -m "build_faulted_job_issue on Apex titles by outcome/error class, lists the attempt chain, attaches the last warn/error log rows (best-effort, logsError on failure) and dedupes on the apex:-prefixed fault signature. The prompt is renamed report-faulted-jobs and takes an orchestrator argument; it skips superseded Apex retry parents so only the final attempt is filed."
```

> **Executor notes for Tasks 16–17** (Task 16 closes Phase 4; Task 17 is Phase 5):
> 1. `src/orchestrator/safety.ts`'s refusal messages hard-code `uipath.serverUrlByEnv` / `uipath.queueUrl`. Task 17 fixes this : `QueueSafetyLimits` gains a required `configSection: "uipath" | "apex"` interpolated into those two messages (UiPath wording stays byte-identical); Task 17 touches `src/orchestrator/safety.ts`, `src/orchestrator/safety.test.ts` and `src/uipath/actions.ts`.
> 2. Task 17 verifies the Process exists with `listWorkflows(env, folder)` (the verified-live list route, Task 10 has no `getWorkflow`) instead of `GET /workflows/{id}`; same guarantee, no unverified endpoint.
> 3. Tests for `apexBuildQueueItemPayload` live in a NEW file `src/apex/backend-payload.test.ts` (Task 11's `backend.test.ts` is not mine to edit). All my Apex tests reuse Task 7's `src/apex/test-support.ts` (`useApexStub`, `json`, `FOLDER_IDS`, `APEX_URL`, `ApexCall`); `SERVER_URL`/`QUEUE_URL` in them equal the harness's default config (`https://be.pre.example`, `https://sqs.example/q`) — if Task 7 changes those defaults, change the two constants.
> 4. The Task 16 `backend.ts` hunks add imports; if Task 11/15 already import the same module (e.g. `resolveApex`, `ExpectedError`), merge into the existing import statement instead of duplicating.

### Task 16: Apex queue-item serializer

**Files:**
- Create: `src/apex/payload.ts` (`serializeApexContent`, `matchValueOf`)
- Create: `src/apex/payload.test.ts`
- Modify: `src/apex/backend.ts` (add imports + append `apexBuildQueueItemPayload`; the file already exists from Tasks 11/15)
- Create: `src/apex/backend-payload.test.ts`

**Interfaces:**
- Consumes: `QueueItemPayloadArgs`, `QueueItemPayload` (`src/orchestrator/backend.ts`); `guardQueueItemSafety` (`src/orchestrator/safety.ts`, Task 1); `resolveApex()` (`src/config/config.ts`, Task 6: `serverUrlByEnv: Partial<Record<Env,string>>`, `queueUrl: string`); canonical native content from `src/orchestrator/queue-item.ts` (Task 4). The fields that builder emits today (`src/uipath/queue-item.ts:138-203`): arrays `DescriptionOfService, Diagnoses, ServiceCode, Treatments, Units`; booleans `IsApproved, IsRetro, IsSameAsRequestingProvider, IsUrgent, addManualFacility, isSpeciality`; number `retryCount`; object `callbackContext`; null `eligibilityOtherPatientName`. The native-type map below also lists the spec §9 fields the builder does not emit yet (`Modifiers`, the two `ReferredTo*Count` numbers) so they are checked the moment they appear.
- Produces: `serializeApexContent(content: Record<string, unknown>): Record<string, unknown>`; `matchValueOf(specificContent: Record<string, unknown>): string` (reused by Task 17); `apexBuildQueueItemPayload(args: QueueItemPayloadArgs): QueueItemPayload`.

- [ ] **Step 1: Write the failing tests**

`src/apex/payload.test.ts`:
```ts
import { describe, expect, test } from "bun:test";
import { ExpectedError } from "../shared/expected-error.js";
import { matchValueOf, serializeApexContent } from "./payload.js";

const native = () => ({
  Units: ["1"],
  Diagnoses: ["M54.5"],
  Modifiers: [],
  Treatments: ["T1"],
  ServiceCode: ["99213"],
  DescriptionOfService: ["Office visit"],
  IsRetro: false,
  IsUrgent: true,
  IsApproved: false,
  isSpeciality: false,
  addManualFacility: false,
  IsSameAsRequestingProvider: false,
  retryCount: 0,
  ReferredToCompleteReferralsCount: 3,
  ReferredToSuccessfulReferralsCount: 2,
  callbackContext: { physicianId: 7, orderUid: "ord-1" },
  OpenReferralInfo: null,
  eligibilityOtherPatientName: null,
  ReferredToReferralDistribution: null,
  automationId: "auto-1",
  orderUid: "ord-1",
});

describe("serializeApexContent", () => {
  test("keeps every native type (no stringification) and passes unlisted fields through", () => {
    const content = native();
    const out = serializeApexContent(content);
    expect(out).toEqual(content);
    expect(out).not.toBe(content);
    expect(Array.isArray(out["Units"])).toBe(true);
    expect(out["callbackContext"]).toEqual({ physicianId: 7, orderUid: "ord-1" });
    expect(out["OpenReferralInfo"]).toBeNull();
  });

  test("accepts partial content: absent fields are not required", () => {
    expect(serializeApexContent({ orderUid: "ord-1" })).toEqual({ orderUid: "ord-1" });
  });

  test("rejects UiPath-style stringified arrays and objects", () => {
    expect(() => serializeApexContent({ ...native(), Diagnoses: '["M54.5"]' })).toThrow(
      /Diagnoses \(expected array, got string\)/,
    );
    expect(() =>
      serializeApexContent({ ...native(), callbackContext: '{"physicianId":7}' }),
    ).toThrow(/callbackContext \(expected object, got string\)/);
  });

  test("rejects stringified booleans and numbers", () => {
    expect(() => serializeApexContent({ ...native(), IsUrgent: "false" })).toThrow(/IsUrgent/);
    expect(() => serializeApexContent({ ...native(), retryCount: "0" })).toThrow(/retryCount/);
  });

  test("null is allowed only on the nullable fields", () => {
    expect(() => serializeApexContent({ ...native(), Units: null })).toThrow(
      /Units \(expected array, got null\)/,
    );
    expect(() => serializeApexContent({ ...native(), Units: undefined })).toThrow(/Units/);
  });

  test("reports every violation in one error", () => {
    expect(() => serializeApexContent({ ...native(), Units: "1", IsRetro: "no" })).toThrow(
      /Units.*IsRetro/,
    );
  });
});

describe("matchValueOf", () => {
  test("returns the automationId", () => {
    expect(matchValueOf({ automationId: "auto-1" })).toBe("auto-1");
  });

  test.each([{}, { automationId: "" }, { automationId: "   " }, { automationId: 5 }])(
    "refuses a missing/empty/non-string automationId: %j",
    (sc) => {
      expect(() => matchValueOf(sc)).toThrow(ExpectedError);
    },
  );
});
```

`src/apex/backend-payload.test.ts`:
```ts
import { describe, expect, spyOn, test } from "bun:test";
import { ExpectedError } from "../shared/expected-error.js";
import { apexBuildQueueItemPayload } from "./backend.js";
import { APEX_URL, useApexStub } from "./test-support.js";

// useApexStub's default apex config (only pre_prod has a serverUrlByEnv entry).
const SERVER_URL = "https://be.pre.example";
const QUEUE_URL = "https://sqs.example/q";

const content = () => ({
  orderUid: "ord-1",
  automationId: "auto-1",
  IsApproved: true,
  Units: ["1", "2"],
  Diagnoses: ["M54.5"],
  ServiceCode: ["99213"],
  DescriptionOfService: ["Office visit"],
  Treatments: [],
  IsRetro: false,
  IsUrgent: false,
  isSpeciality: true,
  addManualFacility: false,
  IsSameAsRequestingProvider: false,
  retryCount: 0,
  callbackContext: { physicianId: 7, orderUid: "ord-1" },
  eligibilityOtherPatientName: null,
});

interface Built {
  queueName: string;
  body: { specific_content: Record<string, unknown>; reference: string; match_value: string };
}

const args = (over: Partial<{ env: "prod" | "pre_prod"; content: Record<string, unknown> }> = {}) => ({
  env: "pre_prod" as const,
  queueName: "CENCAL auth submit queue",
  orderUid: "ord-1",
  content: content(),
  ...over,
});

describe("apexBuildQueueItemPayload", () => {
  useApexStub();

  test("keeps native types, forces IsApproved=false, and fills serverURL/queueUrl from config", () => {
    // The incoming content tries to point the robot at another host: config must win.
    const out = apexBuildQueueItemPayload(
      args({ content: { ...content(), serverURL: "https://evil.example.com" } }),
    );
    const sc = (out.payload as Built).body.specific_content;
    expect(sc["IsApproved"]).toBe(false);
    expect(sc["serverURL"]).toBe(SERVER_URL);
    expect(sc["queueUrl"]).toBe(QUEUE_URL);
    expect(sc["Units"]).toEqual(["1", "2"]);
    expect(sc["callbackContext"]).toEqual({ physicianId: 7, orderUid: "ord-1" });
    expect(sc["isSpeciality"]).toBe(true);
    expect(sc["retryCount"]).toBe(0);
    expect(sc["eligibilityOtherPatientName"]).toBeNull();
  });

  test("payload shape: reference is <orderUid>-<epoch seconds>, match_value is automationId", () => {
    const spy = spyOn(Date, "now").mockReturnValue(1_700_000_000_999);
    try {
      const out = apexBuildQueueItemPayload(args());
      const built = out.payload as Built;
      expect(built.queueName).toBe("CENCAL auth submit queue");
      expect(built.body.reference).toBe("ord-1-1700000000");
      expect(built.body.match_value).toBe("auto-1");
      expect(Object.keys(built).sort()).toEqual(["body", "queueName"]);
      expect(Object.keys(built.body).sort()).toEqual([
        "match_value",
        "reference",
        "specific_content",
      ]);
      expect(out.meta).toEqual({
        queueName: "CENCAL auth submit queue",
        env: "pre_prod",
        orderUid: "ord-1",
        note: "post with add_queue_item(orchestrator: apex)",
      });
      expect(out.notes.length).toBeGreaterThan(0);
    } finally {
      spy.mockRestore();
    }
  });

  test("does not mutate the caller's content", () => {
    const c = content();
    apexBuildQueueItemPayload(args({ content: c }));
    expect(c.IsApproved).toBe(true);
    expect("serverURL" in c).toBe(false);
  });

  test("an env without apex.serverUrlByEnv.<env> is an ExpectedError naming the key (no fallback)", () => {
    const build = () => apexBuildQueueItemPayload(args({ env: "prod" }));
    expect(build).toThrow(ExpectedError);
    expect(build).toThrow(/apex\.serverUrlByEnv\.prod/);
  });

  test("content without a usable automationId cannot be routed", () => {
    expect(() =>
      apexBuildQueueItemPayload(args({ content: { ...content(), automationId: "" } })),
    ).toThrow(/automationId/);
  });

  test("UiPath-style stringified content is rejected, not posted", () => {
    expect(() =>
      apexBuildQueueItemPayload(args({ content: { ...content(), Diagnoses: '["M54.5"]' } })),
    ).toThrow(/Diagnoses/);
  });
});

describe("apexBuildQueueItemPayload without apex.queueUrl", () => {
  useApexStub({
    apex: {
      apiUrl: APEX_URL,
      username: "tester",
      password: "pw",
      serverUrlByEnv: { pre_prod: SERVER_URL },
    },
  });

  test("is an ExpectedError naming the key", () => {
    const build = () => apexBuildQueueItemPayload(args());
    expect(build).toThrow(ExpectedError);
    expect(build).toThrow(/apex\.queueUrl/);
  });
});
```

- [ ] **Step 2: Run them to verify they fail**
Run: `bun test src/apex/payload.test.ts src/apex/backend-payload.test.ts` — Expected: FAIL (`./payload.js` does not exist; `apexBuildQueueItemPayload` is not exported from `./backend.js`).

- [ ] **Step 3: Implement**

New file `src/apex/payload.ts`:
```ts
// Apex queue-item serializer. Unlike UiPath's (which stringifies the array/object
// fields), Apex stores specific_content as native JSON — BE-built items carry real
// arrays, booleans, numbers and a callbackContext object — so the canonical content
// from orchestrator/queue-item.ts is kept as-is and checked against the native-type map
// (spec §9) instead of converted. A mismatch is a builder bug, never user input, so it
// throws a plain Error.

import { ExpectedError } from "../shared/expected-error.js";
import { isRecord } from "../shared/util.js";

type Kind = "array" | "boolean" | "number" | "object";

const IS_KIND: Record<Kind, (v: unknown) => boolean> = {
  array: Array.isArray,
  boolean: (v) => typeof v === "boolean",
  number: (v) => typeof v === "number",
  object: isRecord,
};

// OpenReferralInfo, eligibilityOtherPatientName and ReferredToReferralDistribution may be
// null, so they are not constrained here. Fields absent from the content are not required.
const NATIVE_FIELDS: Record<string, Kind> = {
  Units: "array",
  Diagnoses: "array",
  Modifiers: "array",
  Treatments: "array",
  ServiceCode: "array",
  DescriptionOfService: "array",
  IsRetro: "boolean",
  IsUrgent: "boolean",
  IsApproved: "boolean",
  isSpeciality: "boolean",
  addManualFacility: "boolean",
  IsSameAsRequestingProvider: "boolean",
  retryCount: "number",
  ReferredToCompleteReferralsCount: "number",
  ReferredToSuccessfulReferralsCount: "number",
  callbackContext: "object",
};

const typeName = (v: unknown): string =>
  v === null ? "null" : Array.isArray(v) ? "array" : typeof v;

export function serializeApexContent(content: Record<string, unknown>): Record<string, unknown> {
  const wrong = Object.entries(NATIVE_FIELDS)
    .filter(([key, kind]) => key in content && !IS_KIND[kind](content[key]))
    .map(([key, kind]) => `${key} (expected ${kind}, got ${typeName(content[key])})`);
  if (wrong.length) {
    throw new Error(
      `Apex queue-item content breaks the native-type map: ${wrong.join(", ")} — ` +
        "the canonical builder must emit native arrays/booleans/numbers/objects, not UiPath-style strings",
    );
  }
  return { ...content };
}

// Apex routes an item by match_value; BE-built items use match_key "automationId" with
// match_value == specific_content.automationId (verified live). Shared by build + add so
// both derive it identically.
export function matchValueOf(specificContent: Record<string, unknown>): string {
  const id = specificContent["automationId"];
  if (typeof id !== "string" || !id.trim()) {
    throw new ExpectedError(
      "specific_content.automationId is required — Apex routes the item by match_value = " +
        "automationId (build_queue_item sets it; pass automationId to override)",
    );
  }
  return id;
}
```

`src/apex/backend.ts` — add to the import block (alphabetical, merging into existing statements where the module is already imported):
```ts
import { resolveApex } from "../config/config.js";
import type { QueueItemPayload, QueueItemPayloadArgs } from "../orchestrator/backend.js";
import { guardQueueItemSafety } from "../orchestrator/safety.js";
import { ExpectedError } from "../shared/expected-error.js";
import { matchValueOf, serializeApexContent } from "./payload.js";
```
Append at the end of the file:
```ts
// ---- build_queue_item serializer -------------------------------------------

// Pure given config: native content + the configured callback host/queue, guarded
// (IsApproved forced false), shaped as the body add_queue_item posts. No silent fallback
// for serverURL/queueUrl — an item pointing a robot at the wrong host is worse than none.
export function apexBuildQueueItemPayload(args: QueueItemPayloadArgs): QueueItemPayload {
  const apex = resolveApex();
  const serverURL = apex.serverUrlByEnv[args.env];
  if (!serverURL) {
    throw new ExpectedError(
      `cannot build an Apex queue item for env '${args.env}': apex.serverUrlByEnv.${args.env} ` +
        "is not configured (it is the callback host the item's serverURL must carry)",
    );
  }
  if (!apex.queueUrl) {
    throw new ExpectedError(
      "cannot build an Apex queue item: apex.queueUrl is not configured " +
        "(the callback queue the item's queueUrl must carry)",
    );
  }
  const { specificContent, forced } = guardQueueItemSafety(
    { ...args.content, serverURL, queueUrl: apex.queueUrl },
    "force",
  );
  const content = serializeApexContent(specificContent);
  const reference = `${args.orderUid}-${Math.floor(Date.now() / 1000)}`;
  const notes = [
    `serverURL/queueUrl come from apex.serverUrlByEnv.${args.env} / apex.queueUrl; native types ` +
      "(arrays, booleans, numbers, callbackContext object) are kept as Apex stores them.",
    `To post: add_queue_item(orchestrator: "apex", env: "pre_prod", queue: "${args.queueName}", ` +
      "reference: payload.body.reference, specificContent: payload.body.specific_content). " +
      "match_value is re-derived from specificContent.automationId at post time.",
  ];
  if (forced.length) notes.unshift(`${forced.join(", ")} forced to false (test-safety guard).`);
  return {
    payload: {
      queueName: args.queueName,
      body: { specific_content: content, reference, match_value: matchValueOf(content) },
    },
    notes,
    meta: {
      queueName: args.queueName,
      env: args.env,
      orderUid: args.orderUid,
      note: "post with add_queue_item(orchestrator: apex)",
    },
  };
}
```

- [ ] **Step 4: Run tests** — `bun test src/apex/payload.test.ts src/apex/backend-payload.test.ts` → PASS
- [ ] **Step 5: Full gate** — `bun run typecheck && bun test && bunx biome check src` → all green (if Biome reformats the long `expect(() => apexBuildQueueItemPayload(...)).toThrow(` line in the test, run `bunx biome check --write src/apex` once and re-run the gate)
- [ ] **Step 6: Commit**
```bash
git add src/apex/payload.ts src/apex/payload.test.ts src/apex/backend.ts src/apex/backend-payload.test.ts
git commit -m "feat(apex): native-typed queue-item serializer and build payload" -m "Apex stores specific_content as native JSON (arrays/booleans/numbers/callbackContext object), unlike UiPath's stringified fields. serializeApexContent asserts the spec §9 native-type map instead of converting; apexBuildQueueItemPayload fills serverURL/queueUrl from apex config with no fallback, forces IsApproved=false, and emits {queueName, body:{specific_content, reference: <orderUid>-<epoch>, match_value: automationId}}."
```

---

---

## Phase 5: Apex writes and registration (Task 17)

### Task 17: Apex writes + register `apexBackend`

**Files:**
- Create: `src/apex/actions.ts`
- Create: `src/apex/actions.test.ts`
- Modify: `src/apex/backend.ts` (import the four writes; append `export const apexBackend`)
- Modify: `src/orchestrator/safety.ts` (`QueueSafetyLimits.configSection`; refusal messages name the caller's config section)
- Modify: `src/orchestrator/safety.test.ts` (typed limits gain `configSection`; one new test)
- Modify: `src/uipath/actions.ts` (`safetyLimits()` passes `configSection: "uipath"`)
- Modify: `src/orchestrator/registry.ts` (drop the `ENABLED_ORCHESTRATORS` gate, register `apex`, `searchableBackends` skips an unconfigured Apex)
- Create: `src/orchestrator/registry.test.ts`
- Modify (EnabledOrchestrator sweep, exact edits in Step 3): `src/server.ts`, `src/orchestrator/find-order.ts`, `src/copilot/sweep.ts`, `src/mcp/prompts.ts`
- Modify: `src/server.ts` (write-tool titles/descriptions/describe strings for Apex specifics; anchored by tool name, Task 5 owns the surrounding code)
- Modify: `src/server.test.ts` (append an Apex schema describe block)

**Interfaces:**
- Consumes: `AddQueueItemArgs/Result`, `DeleteQueueItemArgs/Result`, `StartJobArgs/Result`, `StartedJob`, `StopJobArgs/Result`, `StopStrategy`, `OrchestratorBackend` (`src/orchestrator/backend.ts`); `guardQueueItemSafety` (`src/orchestrator/safety.ts`); `resolveApex()`; `apexRequest`, `resolveFolderId`, `apexDashboardLink` (`src/apex/client.ts`, Task 7); `useApexStub`, `json`, `FOLDER_IDS`, `APEX_URL`, `ApexCall` (`src/apex/test-support.ts`, Task 7: routes match by method + path prefix after `/api/v1`, last registered wins, `calls` records every request incl. login/folders, headers lower-cased); `ApexApiError` (`src/apex/errors.ts`); `listQueues(env, folder)`, `getQueueItem(env, folder, queueId, itemId)`, `getJob(env, folder, id)`, `listWorkflows(env, folder)`, `ApexQueue` (`src/apex/api.ts`, Task 10: `folder: string | undefined`, raw snake_case rows, `getJob`/`getQueueItem` throw `ApexApiError` on 404); `APEX_JOB_LABELS`, `APEX_ITEM_LABELS: Readonly<Record<string,string>>`, `APEX_TERMINAL_STATUSES: ReadonlySet<string>` (`src/apex/map.ts`, Task 10); `matchValueOf` (Task 16); all `apex*` read/payload functions (Tasks 11/15/16).
- Produces: `addApexQueueItem`, `deleteApexQueueItem`, `startApexJob`, `stopApexJob`; `export const apexBackend: OrchestratorBackend`; `BACKENDS.apex` (the `ENABLED_ORCHESTRATORS` gate is deleted, so every tool now exposes `apex`). `ORCHESTRATORS` already contains `"apex"` from Task 1.

- [ ] **Step 1: Write the failing tests**

`src/apex/actions.test.ts` (runs the REAL `apexRequest` against Task 7's `useApexStub`, which also proves login + folder resolution; every refusal test asserts the stub saw ZERO calls, i.e. the refusal precedes all I/O):
```ts
import { describe, expect, test } from "bun:test";
import { ExpectedError } from "../shared/expected-error.js";
import { addApexQueueItem, deleteApexQueueItem, startApexJob, stopApexJob } from "./actions.js";
import { APEX_URL, type ApexCall, FOLDER_IDS, json, useApexStub } from "./test-support.js";

// useApexStub's default apex config: serverUrlByEnv.pre_prod + queueUrl, default folders.
const SERVER_URL = "https://be.pre.example";
const QUEUE_URL = "https://sqs.example/q";
const TEST_FOLDER_ID = FOLDER_IDS["Authorization Test"] as string;
const QUEUE_ID = "aaaaaaaa-1111-4111-8111-111111111111";
const ITEM_ID = "bbbbbbbb-2222-4222-8222-222222222222";
const JOB_ID = "cccccccc-3333-4333-8333-333333333333";
const WORKFLOW_ID = "dddddddd-4444-4444-8444-444444444444";

const paged = (items: unknown[]): Response =>
  json({ items, total: items.length, page: 1, page_size: 200, total_pages: 1 });

// Everything except login + folder resolution: what the action itself did.
const actionCalls = (calls: ApexCall[]): ApexCall[] =>
  calls.filter((c) => !c.path.startsWith("/auth/") && !c.path.startsWith("/folders"));
const writes = (calls: ApexCall[]): ApexCall[] =>
  actionCalls(calls).filter((c) => c.method !== "GET");
const sequence = (calls: ApexCall[]): string[] =>
  actionCalls(calls).map((c) => `${c.method} ${c.path.split("?")[0]}`);

const QUEUE = {
  id: QUEUE_ID,
  folder_id: TEST_FOLDER_ID,
  name: "CENCAL auth submit queue",
  workflow_id: WORKFLOW_ID,
  is_active: false,
};

// ---- addApexQueueItem -------------------------------------------------------

describe("addApexQueueItem", () => {
  const apex = useApexStub();
  const args = {
    env: "pre_prod" as const,
    queue: "cencal auth submit queue",
    reference: "ord-1-1700000000",
    priority: "Normal" as const,
    specificContent: {
      orderUid: "ord-1",
      automationId: "auto-1",
      IsApproved: true,
      serverURL: SERVER_URL,
      queueUrl: QUEUE_URL,
      Units: ["1"],
      callbackContext: { physicianId: 7 },
    },
  };
  const stubQueue = (): void => {
    apex.route("GET", "/queues?", () => paged([QUEUE]));
    apex.route("POST", `/queues/${QUEUE_ID}/items`, () => json({ id: ITEM_ID, status: "new" }, 201));
  };

  test("refuses env=prod in the domain, before any fetch", async () => {
    await expect(addApexQueueItem({ ...args, env: "prod" })).rejects.toThrow(/pre_prod-only/);
    expect(apex.calls.length).toBe(0);
  });

  test.each([
    ["a serverURL that is not the configured pre-prod host", { serverURL: "https://prod-be.example.com" }, /serverURL/],
    ["a queueUrl that is not the configured one", { queueUrl: "https://sqs.example/other" }, /queueUrl/],
    ["a <TO-FILL> placeholder", { Facility: "<TO-FILL>" }, /placeholder/],
  ])("guard violation blocks everything: %s", async (_name, patch, message) => {
    await expect(
      addApexQueueItem({ ...args, specificContent: { ...args.specificContent, ...patch } }),
    ).rejects.toThrow(message);
    expect(apex.calls.length).toBe(0);
  });

  test.each([{}, { automationId: "" }])(
    "refuses content without a usable automationId (%j) before any fetch",
    async (patch) => {
      const { automationId: _drop, ...rest } = args.specificContent;
      await expect(
        addApexQueueItem({ ...args, specificContent: { ...rest, ...patch } }),
      ).rejects.toThrow(ExpectedError);
      expect(apex.calls.length).toBe(0);
    },
  );

  test("posts the guarded native content with match_value = automationId, in the test folder", async () => {
    stubQueue();
    const out = await addApexQueueItem(args);
    expect(writes(apex.calls).length).toBe(1);
    const post = writes(apex.calls)[0];
    expect(post?.path).toBe(`/queues/${QUEUE_ID}/items`);
    expect(post?.headers["x-folder-id"]).toBe(TEST_FOLDER_ID);
    expect(post?.body).toEqual({
      specific_content: { ...args.specificContent, IsApproved: false },
      reference: "ord-1-1700000000",
      match_value: "auto-1",
    });
    expect(out).toEqual({
      env: "pre_prod",
      queueId: QUEUE_ID,
      queueName: "CENCAL auth submit queue",
      reference: "ord-1-1700000000",
      itemId: ITEM_ID,
      status: "New",
      forced: ["IsApproved"],
    });
  });

  test("resolves the queue by uuid, case-insensitively", async () => {
    stubQueue();
    const out = await addApexQueueItem({ ...args, queue: QUEUE_ID.toUpperCase() });
    expect(out.queueId).toBe(QUEUE_ID);
  });

  test("an unknown queue posts nothing", async () => {
    apex.route("GET", "/queues?", () => paged([QUEUE]));
    await expect(addApexQueueItem({ ...args, queue: "Nope" })).rejects.toThrow(/no queue/);
    expect(writes(apex.calls).length).toBe(0);
  });

  test("a name matching two queues (case differs) is refused, not guessed", async () => {
    apex.route("GET", "/queues?", () =>
      paged([
        QUEUE,
        { ...QUEUE, id: "eeeeeeee-5555-4555-8555-555555555555", name: "Cencal Auth Submit Queue" },
      ]),
    );
    await expect(addApexQueueItem(args)).rejects.toThrow(/matches 2 queues/);
    expect(writes(apex.calls).length).toBe(0);
  });
});

describe("addApexQueueItem without apex.serverUrlByEnv.pre_prod", () => {
  const apex = useApexStub({
    apex: { apiUrl: APEX_URL, username: "tester", password: "pw", queueUrl: QUEUE_URL },
  });

  test("fails closed, naming the apex config key, before any fetch", async () => {
    await expect(
      addApexQueueItem({
        env: "pre_prod",
        queue: "q",
        reference: "r",
        priority: "Normal",
        specificContent: { automationId: "auto-1", serverURL: SERVER_URL },
      }),
    ).rejects.toThrow(/apex\.serverUrlByEnv\.pre_prod/);
    expect(apex.calls.length).toBe(0);
  });
});

// ---- deleteApexQueueItem ----------------------------------------------------

describe("deleteApexQueueItem", () => {
  const apex = useApexStub();
  const itemPath = `/queues/${QUEUE_ID}/items/${ITEM_ID}`;

  test("refuses env=prod in the domain, before any fetch", async () => {
    await expect(
      deleteApexQueueItem({ env: "prod", itemId: ITEM_ID, queueId: QUEUE_ID }),
    ).rejects.toThrow(/pre_prod-only/);
    expect(apex.calls.length).toBe(0);
  });

  test("queueId is required, before any fetch", async () => {
    await expect(deleteApexQueueItem({ env: "pre_prod", itemId: ITEM_ID })).rejects.toThrow(
      /queueId is required/,
    );
    expect(apex.calls.length).toBe(0);
  });

  test.each([
    ["in_progress", "InProgress"],
    ["successful", "Successful"],
    ["failed", "Failed"],
    ["retried", "Retried"],
    ["abandoned", "Abandoned"],
    ["deleted", "Deleted"],
  ])("refuses a '%s' item (names the status) and posts nothing", async (status, label) => {
    apex.route("GET", itemPath, () => json({ id: ITEM_ID, status, reference: "ref-1" }));
    const err = await deleteApexQueueItem({
      env: "pre_prod",
      itemId: ITEM_ID,
      queueId: QUEUE_ID,
    }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ExpectedError);
    expect((err as Error).message).toContain(label);
    expect(writes(apex.calls).length).toBe(0);
  });

  test("deletes a 'new' item: fetch first, then POST :delete {}", async () => {
    apex.route("GET", itemPath, () => json({ id: ITEM_ID, status: "new", reference: "ref-1" }));
    apex.route("POST", `${itemPath}:delete`, () => json({}));
    const out = await deleteApexQueueItem({ env: "pre_prod", itemId: ITEM_ID, queueId: QUEUE_ID });
    expect(out).toEqual({
      env: "pre_prod",
      itemId: ITEM_ID,
      deleted: true,
      previousStatus: "New",
      reference: "ref-1",
    });
    expect(sequence(apex.calls)).toEqual([`GET ${itemPath}`, `POST ${itemPath}:delete`]);
    expect(writes(apex.calls)[0]?.body).toEqual({});
    expect(writes(apex.calls)[0]?.headers["x-folder-id"]).toBe(TEST_FOLDER_ID);
  });

  test("an item that cannot be fetched is never deleted", async () => {
    // no route stubbed -> 404 on the fetch-first GET
    await expect(
      deleteApexQueueItem({ env: "pre_prod", itemId: ITEM_ID, queueId: QUEUE_ID }),
    ).rejects.toThrow();
    expect(writes(apex.calls).length).toBe(0);
  });
});

// ---- startApexJob -----------------------------------------------------------

describe("startApexJob", () => {
  const apex = useApexStub();
  const args = {
    env: "pre_prod" as const,
    processId: WORKFLOW_ID,
    input: { dryRun: true },
    jobsCount: 3,
    matchValue: "auto-1",
  };
  const stubWorkflows = (): void =>
    apex.route("GET", "/workflows?", () =>
      paged([{ id: WORKFLOW_ID, name: "apex-auth-submit-cencal" }]),
    );

  test("refuses env=prod in the domain, before any fetch", async () => {
    await expect(startApexJob({ ...args, env: "prod" })).rejects.toThrow(/pre_prod-only/);
    expect(apex.calls.length).toBe(0);
  });

  test.each([undefined, "", "  "])("matchValue is required (%j), before any fetch", async (matchValue) => {
    const { matchValue: _drop, ...rest } = args;
    await expect(
      startApexJob(matchValue === undefined ? rest : { ...rest, matchValue }),
    ).rejects.toThrow(/matchValue is required/);
    expect(apex.calls.length).toBe(0);
  });

  test("an unknown Process posts nothing", async () => {
    apex.route("GET", "/workflows?", () => paged([]));
    await expect(startApexJob(args)).rejects.toThrow(/no Process/);
    expect(writes(apex.calls).length).toBe(0);
  });

  test("posts one sequential job per jobsCount with the documented body", async () => {
    stubWorkflows();
    let n = 0;
    apex.route("POST", "/jobs", () => {
      n += 1;
      return json({ id: `aaaaaaaa-0000-4000-8000-00000000000${n}`, status: "queued" }, 201);
    });
    const out = await startApexJob(args);
    expect(writes(apex.calls).length).toBe(3);
    for (const w of writes(apex.calls)) {
      expect(w.path).toBe("/jobs");
      expect(w.headers["x-folder-id"]).toBe(TEST_FOLDER_ID);
      expect(w.body).toEqual({
        workflow_id: WORKFLOW_ID,
        folder_id: TEST_FOLDER_ID,
        match_value: "auto-1",
        input_payload: { dryRun: true },
        bucket_id: null,
      });
    }
    expect(out.env).toBe("pre_prod");
    expect(out.jobs.map((j) => j.id)).toEqual([
      "aaaaaaaa-0000-4000-8000-000000000001",
      "aaaaaaaa-0000-4000-8000-000000000002",
      "aaaaaaaa-0000-4000-8000-000000000003",
    ]);
    expect(out.jobs[0]).toEqual({
      id: "aaaaaaaa-0000-4000-8000-000000000001",
      state: "Pending",
      processName: "apex-auth-submit-cencal",
      link: `${APEX_URL}/jobs/aaaaaaaa-0000-4000-8000-000000000001`,
    });
  });

  test("a failed POST is never retried and the already-started jobs are reported", async () => {
    stubWorkflows();
    let n = 0;
    apex.route("POST", "/jobs", () => {
      n += 1;
      return n === 1 ? json({ id: JOB_ID, status: "queued" }, 201) : json({ detail: "boom" }, 500);
    });
    const err = await startApexJob(args).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ExpectedError);
    expect((err as Error).message).toContain(JOB_ID);
    expect((err as Error).message).toContain("boom");
    expect(writes(apex.calls).length).toBe(2); // 1 ok + 1 failed: no retry, third never attempted
  });

  test("a failure of the first POST is not retried either", async () => {
    stubWorkflows();
    apex.route("POST", "/jobs", () => json({ detail: "boom" }, 500));
    await expect(startApexJob(args)).rejects.toThrow(/boom/);
    expect(writes(apex.calls).length).toBe(1);
  });
});

// ---- stopApexJob ------------------------------------------------------------

describe("stopApexJob", () => {
  const apex = useApexStub();
  const jobPath = `/jobs/${JOB_ID}`;
  const stop = (strategy: "SoftStop" | "Kill") =>
    stopApexJob({ env: "pre_prod", jobId: JOB_ID, strategy });

  test("refuses env=prod in the domain, before any fetch", async () => {
    await expect(stopApexJob({ env: "prod", jobId: JOB_ID, strategy: "Kill" })).rejects.toThrow(
      /pre_prod-only/,
    );
    expect(apex.calls.length).toBe(0);
  });

  test.each([
    ["completed", "Successful"],
    ["failed", "Faulted"],
    ["cancelled", "Stopped"],
    ["lost", "Lost"],
    ["timed_out", "Timed Out"],
  ])("refuses a terminal '%s' job (names the state) and posts nothing", async (status, label) => {
    apex.route("GET", jobPath, () => json({ id: JOB_ID, status }));
    const err = await stop("Kill").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ExpectedError);
    expect((err as Error).message).toContain(label);
    expect(writes(apex.calls).length).toBe(0);
  });

  test("SoftStop on a running job posts cancel {force:false}", async () => {
    apex.route("GET", jobPath, () => json({ id: JOB_ID, status: "running" }));
    apex.route("POST", `${jobPath}/cancel`, () => json({}));
    const out = await stop("SoftStop");
    expect(out).toEqual({ env: "pre_prod", jobId: JOB_ID, strategy: "SoftStop", stopped: true });
    expect(sequence(apex.calls)).toEqual([`GET ${jobPath}`, `POST ${jobPath}/cancel`]);
    expect(writes(apex.calls)[0]?.body).toEqual({ force: false });
    expect(writes(apex.calls)[0]?.headers["x-folder-id"]).toBe(TEST_FOLDER_ID);
  });

  test("Kill posts force_cancel with an empty body", async () => {
    apex.route("GET", jobPath, () => json({ id: JOB_ID, status: "cancelling" }));
    apex.route("POST", `${jobPath}/force_cancel`, () => json({}));
    const out = await stop("Kill");
    expect(out.strategy).toBe("Kill");
    expect(writes(apex.calls)[0]?.path).toBe(`${jobPath}/force_cancel`);
    expect(writes(apex.calls)[0]?.body).toEqual({});
  });

  test("a 403 on Kill becomes an ExpectedError that names jobs:force_cancel", async () => {
    apex.route("GET", jobPath, () => json({ id: JOB_ID, status: "running" }));
    apex.route("POST", `${jobPath}/force_cancel`, () => json({ detail: "forbidden" }, 403));
    const err = await stop("Kill").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ExpectedError);
    expect((err as Error).message).toContain("jobs:force_cancel");
    expect(writes(apex.calls).length).toBe(1);
  });

  test("a job that cannot be fetched is never cancelled", async () => {
    await expect(stop("SoftStop")).rejects.toThrow();
    expect(writes(apex.calls).length).toBe(0);
  });
});
```

Append to the END of `src/server.test.ts` (after the existing `tool input schemas accept representative payloads` block; `registered` is already in scope). Note Task 5 will already have converted the UiPath cases to the new arg names; these are additional, Apex-specific:
```ts
describe("orchestrator tools accept orchestrator: apex", () => {
  const ID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee";
  const apex = { env: "pre_prod", orchestrator: "apex" };
  const VALID: Record<string, Record<string, unknown>> = {
    list_jobs: { ...apex, processName: "CENCAL" },
    get_job: { ...apex, jobId: ID, includeLogDigest: true },
    get_job_logs: { ...apex, jobId: ID, tail: 50 },
    build_faulted_job_issue: { ...apex, jobId: ID },
    list_queues: { ...apex, nameContains: "auth" },
    list_queue_items: { ...apex, queue: "CENCAL", status: "New" },
    find_order_queue_items: { ...apex, orderUid: "abcdefgh" },
    pull_queue_item: { ...apex, queueId: ID, itemId: ID },
    list_processes: { ...apex },
    list_triggers: { ...apex, queueId: ID },
    build_queue_item: { ...apex, orderUid: "abcdefgh", profile: "ossm" },
    find_stuck_orders: { ...apex, profile: "ossm", crossCheck: true },
    add_queue_item: {
      ...apex,
      queue: "CENCAL auth submit queue",
      reference: "ord-1-1700000000",
      specificContent: { orderUid: "abcdefgh" },
    },
    delete_queue_item: { ...apex, itemId: ID, queueId: ID },
    start_job: { ...apex, processId: ID, matchValue: "auto-1" },
    stop_job: { ...apex, jobId: ID, strategy: "Kill" },
  };
  // orchestrator is optional on these two (omitted = every backend)
  const OPTIONAL_ORCHESTRATOR = new Set(["find_order_queue_items", "find_stuck_orders"]);
  const APEX_WRITES = ["add_queue_item", "delete_queue_item", "start_job", "stop_job"];

  for (const [name, valid] of Object.entries(VALID)) {
    test(`${name} accepts an apex payload`, () => {
      expect(registered[name]?.inputSchema?.safeParse(valid).success).toBe(true);
    });
    test(`${name} rejects an unknown orchestrator`, () => {
      const bad = { ...valid, orchestrator: "nope" };
      expect(registered[name]?.inputSchema?.safeParse(bad).success).toBe(false);
    });
    test(`${name} ${OPTIONAL_ORCHESTRATOR.has(name) ? "allows" : "requires"} orchestrator`, () => {
      const { orchestrator: _drop, ...rest } = valid;
      expect(registered[name]?.inputSchema?.safeParse(rest).success).toBe(
        OPTIONAL_ORCHESTRATOR.has(name),
      );
    });
  }

  for (const name of APEX_WRITES) {
    test(`${name} rejects env=prod for apex at the schema`, () => {
      const prod = { ...VALID[name], env: "prod" };
      expect(registered[name]?.inputSchema?.safeParse(prod).success).toBe(false);
    });
  }
});
```

`src/orchestrator/safety.test.ts` (Task 1 moved it verbatim from `src/uipath/safety.test.ts`) — the two typed limits constants gain the now-required field, then add one test inside `describe("guardQueueItemSafety — preProdPost mode", …)`:
```ts
// before
const LIMITS: QueueSafetyLimits = {
  preProdServerUrl: "https://pre-prod-be-batch.example.com",
  queueUrl: "https://sqs.example.com/dev-queue",
};

const NO_LIMITS: QueueSafetyLimits = { preProdServerUrl: "", queueUrl: "" };

// after
const LIMITS: QueueSafetyLimits = {
  preProdServerUrl: "https://pre-prod-be-batch.example.com",
  queueUrl: "https://sqs.example.com/dev-queue",
  configSection: "uipath",
};

const NO_LIMITS: QueueSafetyLimits = { preProdServerUrl: "", queueUrl: "", configSection: "uipath" };
```
```ts
  test("refusal messages name the calling backend's config section", () => {
    const apex: QueueSafetyLimits = {
      preProdServerUrl: "",
      queueUrl: "https://sqs.example.com/q",
      configSection: "apex",
    };
    expect(() =>
      guardQueueItemSafety({ serverURL: "https://x.example.com" }, "preProdPost", apex),
    ).toThrow(/apex\.serverUrlByEnv\.pre_prod/);
    expect(() =>
      guardQueueItemSafety({ queueUrl: "https://sqs.example.com/other" }, "preProdPost", apex),
    ).toThrow(/apex\.queueUrl/);
    // UiPath wording is unchanged
    expect(() =>
      guardQueueItemSafety({ serverURL: "https://x.example.com" }, "preProdPost", NO_LIMITS),
    ).toThrow(/uipath\.serverUrlByEnv\.pre_prod/);
  });
```

`src/orchestrator/registry.test.ts`:
```ts
import { describe, expect, test } from "bun:test";
import { useApexStub } from "../apex/test-support.js";
import { backendFor, searchableBackends } from "./registry.js";

const names = (): string[] => searchableBackends().map((b) => b.name);

describe("searchableBackends: Apex not configured at all", () => {
  useApexStub({ apex: null });

  test("is skipped from search-everything", () => {
    expect(names()).toEqual(["uipath"]);
  });

  test("an explicit backendFor('apex') still resolves", () => {
    expect(backendFor("apex").name).toBe("apex");
  });
});

describe("searchableBackends: Apex configured", () => {
  useApexStub();

  test("is searched", () => {
    expect(names()).toEqual(["uipath", "apex"]);
  });
});

describe("searchableBackends: Apex half-configured", () => {
  useApexStub({ apex: { apiUrl: "https://apex.example.com" } });

  test("is still searched so its ApexNotConfiguredError surfaces", () => {
    expect(names()).toEqual(["uipath", "apex"]);
  });
});
```

- [ ] **Step 2: Run them to verify they fail**
Run: `bun test src/apex/actions.test.ts src/server.test.ts src/orchestrator/safety.test.ts src/orchestrator/registry.test.ts` — Expected: FAIL (`./actions.js` does not exist; the safety messages still say `uipath.`; every `orchestrator: "apex"` payload is rejected because the tools still validate against `ENABLED_ORCHESTRATORS = ["uipath"]`).

- [ ] **Step 3: Implement**

`src/orchestrator/safety.ts` (four spots; the pure logic is otherwise untouched):
```ts
// before
export interface QueueSafetyLimits {
  preProdServerUrl: string; // config uipath.serverUrlByEnv?.pre_prod ?? ""
  queueUrl: string; // config uipath.queueUrl ?? ""
}
// after
export interface QueueSafetyLimits {
  preProdServerUrl: string; // config <configSection>.serverUrlByEnv?.pre_prod ?? ""
  queueUrl: string; // config <configSection>.queueUrl ?? ""
  configSection: "uipath" | "apex"; // names the caller's config keys in refusal messages
}

// before
  limits: QueueSafetyLimits = { preProdServerUrl: "", queueUrl: "" },
// after
  limits: QueueSafetyLimits = { preProdServerUrl: "", queueUrl: "", configSection: "uipath" },

// before
        "item carries a serverURL but uipath.serverUrlByEnv.pre_prod is not configured — " +
// after
        `item carries a serverURL but ${limits.configSection}.serverUrlByEnv.pre_prod is not configured — ` +

// before
        ? `queueUrl '${queueUrl}' is neither empty nor the configured uipath.queueUrl`
        : `queueUrl '${queueUrl}' must be empty (uipath.queueUrl is not configured)`,
// after
        ? `queueUrl '${queueUrl}' is neither empty nor the configured ${limits.configSection}.queueUrl`
        : `queueUrl '${queueUrl}' must be empty (${limits.configSection}.queueUrl is not configured)`,
```
(The first message's string literal changes from a plain string to a template literal; the text for UiPath stays byte-identical.)

`src/uipath/actions.ts` `safetyLimits()` (lines 36-42 today):
```ts
// before
  return {
    preProdServerUrl: cfg.serverUrlByEnv?.pre_prod ?? "",
    queueUrl: cfg.queueUrl ?? "",
  };
// after
  return {
    preProdServerUrl: cfg.serverUrlByEnv?.pre_prod ?? "",
    queueUrl: cfg.queueUrl ?? "",
    configSection: "uipath",
  };
```

New file `src/apex/actions.ts`:
```ts
// Apex WRITES — the only module that mutates Apex state. Everything here is pre_prod-only
// (the 'Authorization Test' folder): every function asserts env === "pre_prod" BEFORE any
// I/O, beneath the tools' schema-level z.literal("pre_prod") and the apexRequest
// chokepoint's own non-GET gate. Prod and test folders share one deployment and one login,
// so the folder is the only boundary (repo invariant).
//
// The same four operations as uipath/actions.ts — enqueue a guarded item, delete a New
// item, start jobs, stop a job — Apex-shaped: queues and Processes are resolved by
// name/id at call time (nothing hardcoded), and every refusal (guard violation, non-new
// item, terminal job) happens before the write is issued. Writes are never retried.

import { type Env, resolveApex } from "../config/config.js";
import type {
  AddQueueItemArgs,
  AddQueueItemResult,
  DeleteQueueItemArgs,
  DeleteQueueItemResult,
  StartedJob,
  StartJobArgs,
  StartJobResult,
  StopJobArgs,
  StopJobResult,
  StopStrategy,
} from "../orchestrator/backend.js";
import { guardQueueItemSafety } from "../orchestrator/safety.js";
import { ExpectedError } from "../shared/expected-error.js";
import { prop } from "../shared/util.js";
import { type ApexQueue, getJob, getQueueItem, listQueues, listWorkflows } from "./api.js";
import { apexDashboardLink, apexRequest, resolveFolderId } from "./client.js";
import { ApexApiError } from "./errors.js";
import { APEX_ITEM_LABELS, APEX_JOB_LABELS, APEX_TERMINAL_STATUSES } from "./map.js";
import { matchValueOf } from "./payload.js";

const PRE_PROD = "pre_prod" as const;

function assertPreProd(env: Env): void {
  if (env !== PRE_PROD) {
    throw new ExpectedError(
      `Apex writes are pre_prod-only (Authorization Test folder) — refusing env '${env}'`,
    );
  }
}

const str = (v: unknown): string => (typeof v === "string" ? v : "");
const label = (labels: Readonly<Record<string, string>>, raw: string): string => labels[raw] ?? raw;

function required(name: string, v: string | undefined): string {
  const t = v?.trim();
  if (!t) throw new ExpectedError(`${name} is required`);
  return t;
}

// ---- add_queue_item ---------------------------------------------------------

// By uuid or exact name, both case-insensitive. Ambiguity is refused, never guessed:
// queue names in the wild differ only by case (`Cencal` / `CENCAL`).
async function resolveQueue(queue: string): Promise<ApexQueue> {
  const wanted = queue.trim().toLowerCase();
  const matches = (await listQueues(PRE_PROD, undefined)).filter(
    (q) => q.id.toLowerCase() === wanted || q.name.toLowerCase() === wanted,
  );
  const [only, ...extra] = matches;
  if (!only) {
    throw new ExpectedError(`no queue named or with id '${queue}' in the pre_prod folder — see list_queues`);
  }
  if (extra.length) {
    throw new ExpectedError(
      `'${queue}' matches ${matches.length} queues (${matches.map((q) => q.id).join(", ")}) — pass the queue uuid`,
    );
  }
  return only;
}

// POST one item to a pre-prod queue. The payload passes the test-safety guard first
// (IsApproved forced false; serverURL/queueUrl pinned to the configured pre-prod values;
// <TO-FILL> placeholders rejected). Priority is the queue's default (`args.priority` is
// UiPath-only). Every check that needs no I/O runs before the queue lookup.
export async function addApexQueueItem(args: AddQueueItemArgs): Promise<AddQueueItemResult> {
  assertPreProd(args.env);
  const reference = required("reference", args.reference);
  const apex = resolveApex();
  const guarded = guardQueueItemSafety(args.specificContent, "preProdPost", {
    preProdServerUrl: apex.serverUrlByEnv.pre_prod ?? "",
    queueUrl: apex.queueUrl,
    configSection: "apex",
  });
  const matchValue = matchValueOf(guarded.specificContent);
  const queue = await resolveQueue(required("queue", args.queue));
  const created = await apexRequest(PRE_PROD, "POST", `/queues/${encodeURIComponent(queue.id)}/items`, {
    body: { specific_content: guarded.specificContent, reference, match_value: matchValue },
  });
  const status = str(prop(created, "status"));
  return {
    env: args.env,
    queueId: queue.id,
    queueName: queue.name,
    reference,
    itemId: str(prop(created, "id")),
    status: label(APEX_ITEM_LABELS, status),
    forced: guarded.forced,
  };
}

// ---- delete_queue_item ------------------------------------------------------

// Fetch-first: only a never-picked-up ('new') item may go — anything else is execution
// history (and possibly a running job's transaction). The dashboard also offers delete
// for failed/abandoned items; parity with UiPath and evidence preservation keep this strict.
export async function deleteApexQueueItem(args: DeleteQueueItemArgs): Promise<DeleteQueueItemResult> {
  assertPreProd(args.env);
  const itemId = required("itemId", args.itemId);
  const queueId = args.queueId?.trim();
  if (!queueId) {
    throw new ExpectedError(
      "queueId is required for Apex (from list_queue_items / find_order_queue_items output)",
    );
  }
  const item = await getQueueItem(PRE_PROD, undefined, queueId, itemId);
  if (item.status !== "new") {
    throw new ExpectedError(
      `refusing to delete queue item ${itemId}: status is '${label(APEX_ITEM_LABELS, item.status)}' ` +
        "(only 'New' items may be deleted)",
    );
  }
  await apexRequest(
    PRE_PROD,
    "POST",
    `/queues/${encodeURIComponent(queueId)}/items/${encodeURIComponent(itemId)}:delete`,
    { body: {} },
  );
  return { env: args.env, itemId, deleted: true, previousStatus: "New", reference: item.reference };
}

// ---- start_job --------------------------------------------------------------

// Start `jobsCount` jobs for a pre-prod Process. match_value picks the credential the job
// runs under, so it is required (no safe default). Sequential and NEVER retried: a retried
// POST could double-start a real portal login. A mid-loop failure reports the jobs that
// were already started, since they keep running.
export async function startApexJob(args: StartJobArgs): Promise<StartJobResult> {
  assertPreProd(args.env);
  const processId = required("processId", args.processId);
  const matchValue = args.matchValue?.trim();
  if (!matchValue) {
    throw new ExpectedError(
      "matchValue is required for Apex start_job — the credential match value the job runs " +
        "under (for auth-submit portals, the account's automationId; see a queue item's match_value)",
    );
  }
  const wanted = processId.toLowerCase();
  const workflow = (await listWorkflows(PRE_PROD, undefined)).find((w) => w.id.toLowerCase() === wanted);
  if (!workflow) {
    throw new ExpectedError(`no Process with id '${processId}' in the pre_prod folder — see list_processes`);
  }
  const folder = await resolveFolderId(PRE_PROD);
  const jobs: StartedJob[] = [];
  for (let i = 0; i < args.jobsCount; i++) {
    try {
      const created = await apexRequest(PRE_PROD, "POST", "/jobs", {
        body: {
          workflow_id: workflow.id,
          folder_id: folder.id,
          match_value: matchValue,
          input_payload: args.input,
          bucket_id: null,
        },
      });
      const id = str(prop(created, "id"));
      jobs.push({
        id,
        state: label(APEX_JOB_LABELS, str(prop(created, "status"))),
        processName: workflow.name,
        link: id ? apexDashboardLink(`/jobs/${id}`) : "",
      });
    } catch (e) {
      if (!jobs.length) throw e;
      throw new ExpectedError(
        `start_job failed on job ${jobs.length + 1} of ${args.jobsCount} (not retried); already started: ` +
          `${jobs.map((j) => j.id).join(", ")} — ${e instanceof Error ? e.message : String(e)}`,
      );
    }
  }
  return { env: args.env, jobs };
}

// ---- stop_job ---------------------------------------------------------------

const STOP_ROUTES: Record<
  StopStrategy,
  { action: string; body: Record<string, unknown>; forbidden: string }
> = {
  SoftStop: {
    action: "cancel",
    body: { force: false },
    forbidden: "this account may lack permission to cancel jobs in the Authorization Test folder",
  },
  Kill: {
    action: "force_cancel",
    body: {},
    forbidden: "force_cancel needs the 'jobs:force_cancel' permission, which this account may lack — use SoftStop instead",
  },
};

// Fetch-first: a job already in a terminal state (completed/failed/cancelled/lost/timed_out)
// has nothing left to stop, so it is refused before any write. The job is read scoped to the
// pre-prod folder.
export async function stopApexJob(args: StopJobArgs): Promise<StopJobResult> {
  assertPreProd(args.env);
  const jobId = required("jobId", args.jobId);
  const job = await getJob(PRE_PROD, undefined, jobId);
  if (APEX_TERMINAL_STATUSES.has(job.status)) {
    throw new ExpectedError(
      `refusing to stop job '${jobId}': state is already '${label(APEX_JOB_LABELS, job.status)}' (terminal)`,
    );
  }
  const route = STOP_ROUTES[args.strategy];
  try {
    await apexRequest(PRE_PROD, "POST", `/jobs/${encodeURIComponent(jobId)}/${route.action}`, {
      body: route.body,
    });
  } catch (e) {
    if (e instanceof ApexApiError && e.status === 403) {
      throw new ExpectedError(`Apex refused ${args.strategy} for job '${jobId}' (403): ${route.forbidden}`);
    }
    throw e;
  }
  return { env: args.env, jobId, strategy: args.strategy, stopped: true };
}
```

`src/apex/backend.ts` — add to the import block (sorted):
```ts
import type { OrchestratorBackend } from "../orchestrator/backend.js";
import { addApexQueueItem, deleteApexQueueItem, startApexJob, stopApexJob } from "./actions.js";
```
(merge `OrchestratorBackend` into the existing `import type { … } from "../orchestrator/backend.js"` from Tasks 11/16.) Append at the end of the file:
```ts
// ---- the assembled backend --------------------------------------------------

export const apexBackend: OrchestratorBackend = {
  name: "apex",
  listJobs: apexListJobs,
  getJobs: apexGetJobs,
  getJobLogs: apexGetJobLogs,
  listQueues: apexListQueues,
  listQueueItems: apexListQueueItems,
  findOrderQueueItems: apexFindOrderQueueItems,
  pullQueueItem: apexPullQueueItem,
  listProcesses: apexListProcesses,
  listTriggers: apexListTriggers,
  buildFaultedJobIssue: apexBuildFaultedJobIssue,
  buildQueueItemPayload: apexBuildQueueItemPayload,
  addQueueItem: addApexQueueItem,
  deleteQueueItem: deleteApexQueueItem,
  startJob: startApexJob,
  stopJob: stopApexJob,
};
```

**Registry + the `EnabledOrchestrator` sweep.** Delete the gate and register Apex; `searchableBackends` (added by Task 13) keeps an Apex that is not configured at all out of "search every backend" while a half-configured Apex still runs (so its error surfaces) and an explicit `backendFor("apex")` always runs. Replace the whole of `src/orchestrator/registry.ts` (Tasks 5 + 13 content, plus apex):
```ts
// Which backend serves each orchestrator value. Retiring a backend = delete its directory,
// its entry here and its ORCHESTRATORS value.

import { apexBackend } from "../apex/backend.js";
import { ApexNotConfiguredError } from "../apex/errors.js";
import { resolveApex } from "../config/config.js";
import { uipathBackend } from "../uipath/backend.js";
import type { OrchestratorBackend } from "./backend.js";
import type { Orchestrator } from "./types.js";

export const BACKENDS: Record<Orchestrator, OrchestratorBackend> = {
  uipath: uipathBackend,
  apex: apexBackend,
};

export const backendFor = (o: Orchestrator): OrchestratorBackend => BACKENDS[o];

// A wholly unconfigured Apex is skipped by "search everything" (UiPath-only setups stay
// quiet); a half-configured one is searched so its ApexNotConfiguredError surfaces.
const apexAbsent = (): boolean => {
  try {
    resolveApex();
    return false;
  } catch (e) {
    return e instanceof ApexNotConfiguredError && !e.partial;
  }
};

export const searchableBackends = (): OrchestratorBackend[] =>
  Object.values(BACKENDS).filter((b) => b.name !== "apex" || !apexAbsent());
```
(No import cycle: registry → apex/backend, apex/errors, config/config, uipath/backend; nothing under those imports registry.)

Then make the sweep explicit. Every site (cross-checked against plan-s2 Task 5 and plan-s5 Tasks 13–15); each edit is exactly:

1. `src/orchestrator/registry.ts` — done above (`ENABLED_ORCHESTRATORS`, `EnabledOrchestrator` deleted; `BACKENDS`/`backendFor` retyped to `Orchestrator`).
2. `src/server.ts`:
```ts
// before
import { backendFor, ENABLED_ORCHESTRATORS } from "./orchestrator/registry.js";
import { QUEUE_ITEM_STATUSES } from "./orchestrator/types.js";
// after
import { backendFor } from "./orchestrator/registry.js";
import { ORCHESTRATORS, QUEUE_ITEM_STATUSES } from "./orchestrator/types.js";

// before
const orchestratorArg = z
  .enum(ENABLED_ORCHESTRATORS)
  .describe(`Which orchestrator: ${ENABLED_ORCHESTRATORS.join(" | ")} (required)`);
// after
const orchestratorArg = z
  .enum(ORCHESTRATORS)
  .describe(`Which orchestrator: ${ORCHESTRATORS.join(" | ")} (required)`);
```
   (If Task 13 added further names to the registry import line, e.g. `searchableBackends`, keep them; only `ENABLED_ORCHESTRATORS` goes.)
3. `src/orchestrator/find-order.ts`:
```ts
// before
import { backendFor, type EnabledOrchestrator, searchableBackends } from "./registry.js";
// after
import { backendFor, searchableBackends } from "./registry.js";

// before
  orchestrator?: EnabledOrchestrator,
// after
  orchestrator?: Orchestrator,
```
   (`Orchestrator` is already imported there from `./types.js`. Task 13 leaves `selectBackends` using `searchableBackends()`, so the only registry-import change is dropping the `type EnabledOrchestrator` specifier.)
4. `src/orchestrator/find-order.test.ts` — Task 13 already compares against `searchableBackends()` and never imports `ENABLED_ORCHESTRATORS`, so nothing changes here; the verification command below confirms it.
5. `src/copilot/sweep.ts`:
```ts
// before
import type { EnabledOrchestrator } from "../orchestrator/registry.js";
import type { Orchestrator, QueueItemCore } from "../orchestrator/types.js";
// after
import type { Orchestrator, QueueItemCore } from "../orchestrator/types.js";

// before
  orchestrator?: EnabledOrchestrator | undefined; // omitted = every orchestrator
// after
  orchestrator?: Orchestrator | undefined; // omitted = every orchestrator
```
6. `src/mcp/prompts.ts` (`report-faulted-jobs` orchestrator completion list):
```ts
// before
import { ENABLED_ORCHESTRATORS } from "../orchestrator/registry.js";
// after (merge into prompts.ts's existing ../orchestrator/types.js import if it has one; else add it in sorted position)
import { ORCHESTRATORS } from "../orchestrator/types.js";

// before
          () => [...ENABLED_ORCHESTRATORS],
// after
          () => [...ORCHESTRATORS],
```
Verification: `rg -n "EnabledOrchestrator|ENABLED_ORCHESTRATORS" src` must hit nothing.

`src/server.ts` — Task 5 owns the four write registrations; locate each by tool name and replace ONLY the properties below (everything else, handlers and annotations included, stays). Do not touch line numbers; they moved in Task 5.

1. The shared `writeEnvArg` const (defined once near `orchestratorArg`):
```ts
const writeEnvArg = z
  .literal("pre_prod")
  .describe(
    "Writes are pre_prod-only (UiPath: the 'Authorization Dev Clone' folder; Apex: the " +
      "'Authorization Test' folder); 'prod' is rejected by the schema (required)",
  );
```
2. `add_queue_item`: `title`, `description`, and the `queue` property:
```ts
    title: "Post a queue item to a pre-prod queue",
    description:
      "POST one item to a queue in the pre-prod test folder (pre_prod ONLY — the schema rejects " +
      "prod; UiPath: 'Authorization Dev Clone', Apex: 'Authorization Test'). Every payload passes " +
      "the test-safety guard first: IsApproved is forced false; a non-empty serverURL/queueUrl " +
      "must match the config's pre-prod values (the chosen orchestrator's section); <TO-FILL> " +
      "placeholders are rejected. `queue`: UiPath = the exact queue Name; Apex = the queue name " +
      "(exact, case-insensitive) or uuid, resolved in the Authorization Test folder (see " +
      "list_queues). Apex derives match_value from specificContent.automationId (required) and " +
      "uses the queue's default priority (`priority` is ignored). To post a build_queue_item " +
      "result: UiPath — payload.itemData.Name as queue, .Reference as reference, .SpecificContent " +
      "as specificContent; Apex — meta.queueName as queue, payload.body.reference as reference, " +
      "payload.body.specific_content as specificContent. When enqueueing many items, pace ~1/s. " +
      "Returns {orchestrator, env, queueId, queueName, reference, itemId, status, forced}.",
```
```ts
      queue: z
        .string()
        .min(1)
        .describe(
          "UiPath: exact queue Name in the dev-clone folder. Apex: queue name or uuid in the " +
            "Authorization Test folder (see list_queues)",
        ),
```
3. `delete_queue_item`: `title`, `description`, and the `queueId` property:
```ts
    title: "Delete a New queue item from the pre-prod folder",
    description:
      "Delete one queue item from the pre-prod test folder (pre_prod ONLY — the schema rejects " +
      "prod). Fetch-first: refuses unless the item's current status is New (Apex 'new'), so " +
      "InProgress/Successful/Failed history is never destroyed. `queueId` is REQUIRED for Apex " +
      "(from list_queue_items / find_order_queue_items) and ignored by UiPath. Use to clear stale " +
      "test items before re-enqueueing after a re-mint. Returns " +
      "{orchestrator, env, itemId, deleted, previousStatus, reference}.",
```
```ts
      queueId: z
        .string()
        .min(1)
        .optional()
        .describe("Queue uuid the item sits in — REQUIRED for Apex, ignored by UiPath"),
```
4. `start_job`: `title`, `description`, and the `matchValue` property:
```ts
    title: "Start a job in the pre-prod test folder",
    description:
      "Start job(s) for a Process in the pre-prod test folder (pre_prod ONLY — the schema " +
      "rejects prod). Get processId from list_processes. UiPath: PIN RACE — a job resolves its " +
      "package version at START, so verify the release's version via list_processes right " +
      "before starting (repinning is manual; this MCP never repins). Apex: matchValue is " +
      "REQUIRED (the credential match value the job runs under — for auth-submit portals the " +
      "account's automationId); the job runs the Process's current_version at dispatch; the " +
      "jobsCount POSTs are sequential and never retried, and a mid-loop failure names the jobs " +
      "already started. A started job performs a real (dry-run) portal login. Poll with get_job. " +
      "Returns {orchestrator, env, jobs:[{id,state,processName,link}]}.",
```
```ts
      matchValue: z
        .string()
        .min(1)
        .optional()
        .describe(
          "Apex: REQUIRED credential match value (the account's automationId for auth-submit " +
            "portals). UiPath: ignored",
        ),
```
5. `stop_job`: `title`, `description`:
```ts
    title: "Stop or kill a running job in the pre-prod folder",
    description:
      "Stop (SoftStop, graceful) or Kill (immediate) a job in the pre-prod test folder " +
      "(pre_prod ONLY — the schema rejects prod). Takes the job id from start_job / list_jobs / " +
      "get_job; it is resolved scoped to the pre-prod folder, so a prod job's id resolves to " +
      "nothing. Fetch-first: refuses a job already in a terminal state (UiPath Successful/" +
      "Faulted/Stopped; Apex completed/failed/cancelled/lost/timed_out). Apex Kill uses " +
      "force_cancel, which needs the 'jobs:force_cancel' permission (a 403 is reported as such). " +
      "Returns {orchestrator, env, jobId, strategy, stopped}.",
```

- [ ] **Step 4: Run tests** — `bun test src/apex/actions.test.ts src/server.test.ts src/orchestrator/safety.test.ts src/orchestrator/registry.test.ts` → PASS
- [ ] **Step 5: Full gate** — `bun run typecheck && bun test && bunx biome check src` → all green. Two things to expect: (a) `bunx biome check --write src/apex src/orchestrator` may reflow the long lines above (Biome's line width) — re-run the gate after; (b) registering `apex` means "search everything" now goes through `searchableBackends()`, which skips an unconfigured Apex, so Task 13/14 tests that run without Apex config stay UiPath-only; a test that deliberately configures Apex (via `useApexStub`) will see both backends.
- [ ] **Step 6: Commit**
```bash
git add src/apex/actions.ts src/apex/actions.test.ts src/apex/backend.ts src/orchestrator/safety.ts src/orchestrator/safety.test.ts src/uipath/actions.ts src/orchestrator/registry.ts src/orchestrator/registry.test.ts src/server.ts src/server.test.ts
git add src/orchestrator/find-order.ts src/copilot/sweep.ts src/mcp/prompts.ts
git commit -m "feat(apex)!: pre-prod writes and register the apex orchestrator" -m "src/apex/actions.ts is the only module that mutates Apex: add (guarded, queue by name/uuid), delete (fetch-first, New only, queueId required), start (matchValue required, sequential, never retried) and stop (refuses terminal states incl. lost/timed_out; 403 on Kill names jobs:force_cancel). Every function refuses non-pre_prod before any I/O. The shared guard's refusal messages now name the caller's config section (uipath|apex). apexBackend is assembled and registered, ORCHESTRATORS gains \"apex\", and the write-tool descriptions document the Apex specifics."
```

### Notes for Task 20 (live checklist additions from Tasks 16–17)

- Prove the stop-job folder scope: with a job id that lives in the prod `Authorization` folder, `stop_job(orchestrator: apex, env: pre_prod)` must fail at the fetch-first `GET /jobs/{id}` (404 via the test-folder `X-Folder-Id`) and issue no POST. Tasks 16–17 assume this; it is unverified live.
- Unverified server preconditions to observe, not assume: `POST …/items/{id}:delete` on a `new` item (expected 200/204 with an empty body; `apexRequest` returns `null` for an empty body and Task 17 does not read the response), and whether `force_cancel` is permitted for the user (a 403 must surface as the `jobs:force_cancel` ExpectedError).
- `add_queue_item` live: use the inactive `CenCal shared-adapter test queue` in `Authorization Test`; check the created item's `match_key`/`match_value` equal `automationId`, `reference` is `<orderUid>-<epoch>`, and `specific_content` arrays/booleans/`callbackContext` are native (compare with a BE-built item).

---

## Phase 6: Reference, docs, release and live verification (Tasks 18–20)

### Task 18: `copilot://reference/orchestrators` resource, safety/config entries, server instructions

**Files:**
- Create: `src/mcp/orchestrator-reference.ts` (leaf module holding `ORCHESTRATOR_REFERENCE`; only `server.ts` imports it, and nothing under `src/uipath` or `src/apex` may)
- Modify: `src/mcp/reference.ts` (`SAFETY_RULES` lines 530-539; `CONFIG_GUIDE` lines 546-626 — no import or structural change)
- Modify: `src/server.ts` (import blocks, baseline lines 68-87; `buildInstructions()`, baseline lines 163-187; the `uipath-folders` resource, baseline lines 631-637). Line numbers are the pre-plan baseline — Tasks 5/11/12/17 shift them; locate every hunk by its quoted anchor text.
- Test: none. `reference.ts` has no tests for these structures (`rg -n "SAFETY_RULES|CONFIG_GUIDE|UIPATH_FOLDERS" src --glob '*.test.ts'` is empty; `server.test.ts` only pins tools/schemas, not resources), and wording tests are banned. Verification is the typecheck/test gate.

**Interfaces:**
- Consumes: `UIPATH_FOLDERS` (`src/mcp/reference.ts`, unchanged), `DEFAULT_APEX_FOLDERS: Record<Env, string>` (`src/config/config.ts`, Task 6), `APEX_JOB_LABELS`, `APEX_ITEM_LABELS` (`Record<string, string>`), `APEX_TERMINAL_STATUSES` (`ReadonlySet<string>`) from `src/apex/map.ts` (Task 10), `UIPATH_TERMINAL_STATES` (`ReadonlySet<string>`) from `src/uipath/map.ts` (Task 2).
- Produces: `export const ORCHESTRATOR_REFERENCE` in `src/mcp/orchestrator-reference.ts` (JSON-serializable, served as `copilot://reference/orchestrators`). It lives in its own leaf module on purpose: `uipath/map.ts` → `uipath.ts` → `mcp/reference.ts` already exists, so `reference.ts` importing `uipath/map.ts` would create a load-order (TDZ) cycle; `UIPATH_FOLDERS` stays exported (uipath code uses it via `folderIdFor` & co). `SAFETY_RULES` / `CONFIG_GUIDE` keep their shapes (`{rules: string[]}` / `{howItLoads, steps, template, fields, rules}`).

- [ ] **Step 1: Write the failing test** — none (see Files).

- [ ] **Step 2: Implement**

2a. Create `src/mcp/orchestrator-reference.ts` (whole file):

```ts
// Orchestrator reference (per backend, per env), served as copilot://reference/orchestrators.
// Hand-maintained facts, plus the label maps and terminal sets imported from the code that
// applies them so the resource cannot drift. Leaf module: only server.ts imports it (the
// mappers it imports reach mcp/reference.ts, so reference.ts must not import them).

import { APEX_ITEM_LABELS, APEX_JOB_LABELS, APEX_TERMINAL_STATUSES } from "../apex/map.js";
import { DEFAULT_APEX_FOLDERS } from "../config/config.js";
import { UIPATH_TERMINAL_STATES } from "../uipath/map.js";
import { UIPATH_FOLDERS } from "./reference.js";

const APEX_FOLDER_ID =
  "resolved at runtime (GET /folders by name, cached per process) — Apex ids are never hardcoded";

export const ORCHESTRATOR_REFERENCE = {
  uipath: {
    folders: UIPATH_FOLDERS,
    writeRule:
      "pre_prod only (the dev-clone folder, which skips all BE notifications). The MCP never repins releases or creates queues.",
  },
  apex: {
    folders: {
      prod: { name: DEFAULT_APEX_FOLDERS.prod, id: APEX_FOLDER_ID },
      pre_prod: { name: DEFAULT_APEX_FOLDERS.pre_prod, id: APEX_FOLDER_ID },
    },
    folderNote:
      "Names are the defaults; apex.folderByEnv overrides them. Both folders live on ONE Apex deployment with ONE login, so the folder is the only boundary: every request carries X-Folder-Id. Unlike UiPath's dev clone, the folder has no dry-run gate of its own — an item only fires real BE calls if its serverURL is the prod BE and IsApproved is true, which this MCP's guard prevents on every write (IsApproved forced false, serverURL pinned to the configured pre-prod value).",
    writeRule:
      "pre_prod ('Authorization Test') only: schema literal + domain assert + the HTTP chokepoint refusing any non-GET unless env=pre_prod. delete_queue_item refuses anything but status New; stop_job refuses terminal jobs. The MCP never deploys, pins, or creates Processes/queues, and never touches Apex credentials, assets or Action Center.",
  },
  stateLabels: {
    uipathJobState: "UiPath job State passes through unchanged (Pending, Running, Successful, Faulted, Stopped, …).",
    apexJobStatusToState: APEX_JOB_LABELS,
    apexItemStatusToStatus: APEX_ITEM_LABELS,
  },
  terminal: {
    uipathJobStates: [...UIPATH_TERMINAL_STATES],
    apexJobStatuses: [...APEX_TERMINAL_STATUSES],
  },
  notes: [
    "One transaction maps to MANY jobs on Apex: a retry is a NEW job on the SAME queue item (retryOfJobId, attempt on each job; the parent ends cancelled), so a queue item's jobs[] is newest-first and a verdict must use the latest attempt. UiPath retries are new queue items, so its jobs[] holds at most one.",
    "Apex logs exist only after the run and only per the Process's artifact_policy: the live log is dashboard-only SSE, so get_job / get_job_logs on a running job report logsUnavailable.reason='running', and a finished job whose policy skipped the artifact (on_failure for a success, never) reports 'artifact-policy'. Use get_job_artifacts for the files themselves (job.log, trace.zip, screenshots, recording).",
    "Never-started jobs: a job that ended failed with started_at null (typically error_class missing_credential, reaped before any worker claimed it) has no logs and no artifacts — logsUnavailable.reason='never-started', details.neverStarted=true, and find_stuck_orders reports verdict 'job-never-started'. Fix the credential binding; re-running does not help.",
  ],
} as const;

2b. `src/mcp/reference.ts` — `SAFETY_RULES` (anchor: the two rules starting `"Dry-run gate:` and `"This MCP never writes to prod:`; then append two rules before the closing `],`). Replace lines 535-537 and add after line 537:

Old:
```ts
    "Dry-run gate: the 'Authorization Dev Clone' folder (434039) skips all BE notifications; only 'Authorization' (231517) fires real BE calls.",
    "This MCP never writes to prod: queue-item POSTs/deletes and job starts are pre_prod-only (dev clone 434039, schema-enforced) and it never repins releases.",
    "Every posted queue item passes the test-safety guard: IsApproved forced false, serverURL/queueUrl pinned to the configured pre-prod values, <TO-FILL> placeholders rejected.",
```
New:
```ts
    "UiPath dry-run gate: the 'Authorization Dev Clone' folder (434039) skips all BE notifications; only 'Authorization' (231517) fires real BE calls.",
    "This MCP never writes to prod: queue-item POSTs/deletes and job starts/stops are pre_prod-only (UiPath dev clone 434039, Apex 'Authorization Test'; schema-enforced) and it never repins releases or deploys.",
    "Every posted queue item (either orchestrator) passes the test-safety guard: IsApproved forced false, serverURL/queueUrl pinned to the configured pre-prod values, <TO-FILL> placeholders rejected.",
    "Apex prod and test folders share one deployment and one login, so the folder is the only boundary: the Apex client always sends X-Folder-Id and refuses any non-GET request unless env=pre_prod, before any network call.",
    "get_job_artifacts writes only under ~/.cache/copilot-mcp/artifacts/ (directory mode 0700), never inside a repo. Treat everything downloaded (job.log adapter lines, result_payload, screenshots, trace.zip) as PHI: never commit, paste or summarize its contents.",
```

2c. `src/mcp/reference.ts` — `CONFIG_GUIDE`:

(i) `steps` — replace line 556 and add a step after it.

Old:
```ts
    "4. For any env using support accounts, call the list_clinics tool (env=prod|pre_prod) to get the clinicUid of each clinic and fill copilot.profiles.<name>.<env>.clinicUid; then call the doctor tool (profile=<one of copilot.profiles>) to verify auth + clinic scope + UiPath.",
```
New:
```ts
    "4. For any env using support accounts, call the list_clinics tool (env=prod|pre_prod) to get the clinicUid of each clinic and fill copilot.profiles.<name>.<env>.clinicUid; then call the doctor tool (profile=<one of copilot.profiles>) to verify auth + clinic scope + UiPath + Apex.",
    "5. Apex is optional: add the `apex` block (or export APEX_API_URL / APEX_USER_NAME / APEX_PASSWORD) only if you need orchestrator=apex. With neither configured, every orchestrator=apex call fails with 'Apex is not configured' while UiPath and Copilot tools keep working.",
```

(ii) `template` — add an `apex` block after the `uipath` block (after the line `      folderPathByEnv: { prod: "Authorization", pre_prod: "Authorization Dev Clone" },` and its closing `    },` at 591):

Old:
```ts
      folderPathByEnv: { prod: "Authorization", pre_prod: "Authorization Dev Clone" },
    },
  },
  fields: {
```
New:
```ts
      folderPathByEnv: { prod: "Authorization", pre_prod: "Authorization Dev Clone" },
    },
    apex: {
      apiUrl:
        "https://orchestrator.apexmedicalai.com (the load-balancer host, never a CloudFront host) (optional if $APEX_API_URL is set)",
      username:
        "<ASK-USER: the user's own Apex login — Apex audits writes per user> (optional if $APEX_USER_NAME is set)",
      password: "<ASK-USER: that user's Apex password> (optional if $APEX_PASSWORD is set)",
      folderByEnv: { prod: "Authorization", pre_prod: "Authorization Test" },
      serverUrlByEnv: { pre_prod: "https://pre-prod-be-batch.ehrcopilotbe.com" },
      queueUrl:
        "<ASK-USER: the SQS queue URL carried by BE-built Apex items (specific_content.queueUrl)>",
    },
  },
  fields: {
```

(iii) `fields` — add entries after the `"uipath.queueUrl / uipath.addQueueItemPath / uipath.serverUrlByEnv"` entry (ends line 616) and before `feedback:`:

```ts
    apex: "Optional block enabling orchestrator=apex (the Apex orchestrator replacing UiPath portal by portal). Any of apiUrl / username / password the file leaves out is read from APEX_API_URL / APEX_USER_NAME / APEX_PASSWORD (file wins per field). The env fallback only works when the MCP host passes its environment through (CLI hosts and bunx do); MCPB installs only pass COPILOT_MCP_CONFIG, so they MUST put Apex in the file. Numbered APEX_API_URL_<n> and APEX_URL are not read. No source configured = Apex calls fail with 'Apex is not configured'; UiPath/Copilot are unaffected.",
    "apex.apiUrl":
      "Apex orchestrator base URL. Use the LB host (https://orchestrator.apexmedicalai.com), NEVER a CloudFront host — CloudFront caps request bodies at 8 KB, which truncates queue-item and job writes.",
    "apex.username / apex.password":
      "The user's OWN Apex login (Apex audits writes per user — no shared bot account). The session JWT (24 h) is cached in memory only, never on disk.",
    "apex.folderByEnv":
      "Folder NAME per env; optional, defaults prod='Authorization', pre_prod='Authorization Test'. Names resolve to folder UUIDs at runtime (cached per process) — never put ids here. Every Apex request is scoped by X-Folder-Id; a tool's `folder` arg overrides it for READS only.",
    "apex.serverUrlByEnv / apex.queueUrl":
      "Write guard limits, needed for build_queue_item / add_queue_item on Apex: specific_content.serverURL must equal serverUrlByEnv.pre_prod and a non-empty specific_content.queueUrl must equal queueUrl. Unset = writes are refused (fails closed). Deliberately duplicated with the uipath keys; deleted together with UiPath at retirement.",
```

(iv) `rules` — add after the rule ending `refuse prod at both the schema and domain layer.` (line 623):

```ts
    "Apex credentials are the operator's own login: never reuse another person's, never echo them, and never point apex.apiUrl at a CloudFront host.",
```

2d. `src/server.ts` — imports. Old (baseline lines 77-87):
```ts
import {
  ACCOUNT_AUTOMATION_IDS,
  CONFIG_GUIDE,
  ORDER_LIFECYCLE,
  PORTALS,
  type PortalEntry,
  QUEUE_ITEM_SCHEMA,
  RESULT_CONTRACT,
  SAFETY_RULES,
  UIPATH_FOLDERS,
} from "./mcp/reference.js";
```
New (`UIPATH_FOLDERS` was used only by the resource below, so it leaves this import):
```ts
import {
  ACCOUNT_AUTOMATION_IDS,
  CONFIG_GUIDE,
  ORDER_LIFECYCLE,
  PORTALS,
  type PortalEntry,
  QUEUE_ITEM_SCHEMA,
  RESULT_CONTRACT,
  SAFETY_RULES,
} from "./mcp/reference.js";
```
and add, between the existing `./mcp/notify.js` import block and `import { registerPrompts } from "./mcp/prompts.js";` (Biome's sorted position):
```ts
import { ORCHESTRATOR_REFERENCE } from "./mcp/orchestrator-reference.js";
```

2e. `src/server.ts` — resource. Old (baseline 631-637):
```ts
jsonResource(
  "uipath-folders",
  "copilot://reference/uipath-folders",
  "UiPath folders by env",
  "Per-env UiPath folder name, OrganizationUnitId, FolderKey, and whether it fires real BE calls.",
  UIPATH_FOLDERS,
);
```
New (old resource is deleted, not aliased):
```ts
jsonResource(
  "orchestrators",
  "copilot://reference/orchestrators",
  "Orchestrators (UiPath + Apex) by env",
  "Per orchestrator and env: folder name + ids (UiPath: OrganizationUnitId/FolderKey/real-BE-calls; Apex: resolved at runtime), the write rule, job-state / queue-item-status label maps, terminal sets, and notes on many-jobs-per-transaction, Apex log availability, and never-started jobs.",
  ORCHESTRATOR_REFERENCE,
);
```

2f. `src/server.ts` — `buildInstructions()`. Replace the `base` string's first statement and the PROD paragraph and the last line (anchors: `"EHR Copilot order operations + UiPath Orchestrator execution analysis. " +` … `"never work around a pre-prod-only refusal — it is the design, not a bug.\n\n" +` and `"Stable facts (portals, folders, …` ).

Old:
```ts
    "EHR Copilot order operations + UiPath Orchestrator execution analysis. " +
    "Every Copilot tool requires an explicit env (prod | pre_prod) and a profile from the config — never assume either.\n\n" +
    "PROD IS READ-ONLY BY DESIGN. This server cannot mutate prod: every write path — order minting/deletes, " +
    "settings sync, queue-item posts/deletes, job starts — targets pre-prod / the UiPath dev-clone folder only, " +
    "enforced at the tool schema AND asserted again in the domain layer. Posted queue items additionally pass a " +
    "safety guard (IsApproved forced false, callback URLs pinned to pre-prod). Reading prod is normal and safe; " +
    "never work around a pre-prod-only refusal — it is the design, not a bug.\n\n" +
```
New:
```ts
    "EHR Copilot order operations + orchestrator (UiPath and Apex) execution analysis. " +
    "Every Copilot tool requires an explicit env (prod | pre_prod) and a profile from the config — never assume either. " +
    "Every job- or queue-level orchestrator tool also requires an explicit `orchestrator` (uipath | apex); only " +
    "find_order_queue_items and find_stuck_orders may omit it (omitted = every configured backend), and " +
    "get_job_artifacts is Apex-only. An order can run on either orchestrator and which one is decided outside this " +
    "server, so look where the item actually sits. Job ids are GUIDs on both — pass them with the right `orchestrator`.\n\n" +
    "PROD IS READ-ONLY BY DESIGN. This server cannot mutate prod: every write path — order minting/deletes, " +
    "settings sync, queue-item posts/deletes, job starts/stops — targets pre-prod only (the UiPath dev-clone folder, " +
    "the Apex 'Authorization Test' folder), enforced at the tool schema AND asserted again in the domain layer " +
    "(Apex also at its HTTP chokepoint). Posted queue items additionally pass a safety guard (IsApproved forced " +
    "false, callback URLs pinned to pre-prod). Reading prod is normal and safe; " +
    "never work around a pre-prod-only refusal — it is the design, not a bug.\n\n" +
```
and Old: `    "Stable facts (portals, folders, schemas, safety rules, config guide) are resources under copilot://.";` New:
```ts
    "Stable facts (portals, orchestrator folders/labels, schemas, safety rules, config guide) are resources under copilot://.";
```

- [ ] **Step 3: Full gate** — `bun run typecheck && bun test && bunx biome check src` → all green
- [ ] **Step 4: Commit**
```bash
git add src/mcp/orchestrator-reference.ts src/mcp/reference.ts src/server.ts
git commit -m "feat(mcp): orchestrators reference resource, Apex safety rules and config guide" -m "Replaces copilot://reference/uipath-folders with copilot://reference/orchestrators (folders, write rule, label maps, terminal sets, behaviour notes per backend) so agents learn both orchestrators from one resource. safety-rules and config-guide document the Apex folder boundary, the apex config block with its env fallback and the LB-host-never-CloudFront rule; the server instructions now describe the orchestrator argument."
```

---

### Task 19: README, CLAUDE.md, rules, CHANGELOG and the v2.0.0 version bump

**Files:**
- Modify: `README.md` (apply the hunks bottom-up so the quoted line numbers stay valid: 164, 148, 103-108, 87, 80, 35-61, 30-31, 9-14)
- Modify: `CLAUDE.md` (lines 7-8, 32, 35-37, 43-47, 74-79, 88-95, 129-141, new bullets)
- Modify: `.claude/rules/env-profile-required.md`, `.claude/rules/uipath-writes-guarded.md`, `.claude/rules/tool-response-contract.md`, `.claude/rules/README.md`, `.claude/workflows/invariant-compliance-sweep.js` (line 16); Create: `.claude/rules/apex-writes-guarded.md` — CLAUDE.md says the rule files must stay in sync with its invariants
- Modify: `CHANGELOG.md` (insert before line 7, `## [1.33.1] - 2026-09-09`)
- Modify: `package.json` (lines 3, 5), `manifest.json` (lines 5, 6, 7, 31, 38; tools block), `src/server.ts` (`const VERSION`, baseline line 156)
- Test: none (docs + version strings; `bun run typecheck && bun test` prove nothing broke, Step 3 proves the manifest is still valid JSON and byte-identical elsewhere).

**Interfaces:**
- Consumes: the final tool surface from the contract's "Tool surface after the plan" table and the D9 rename list; Task 6's `apex` config block; Task 12's `get_job_artifacts`; Task 15's prompt `report-faulted-jobs`; Task 18's resource.
- Produces: version string `2.0.0` in three places. The `vX.Y.Z` tag is NOT created here — the caller tags only after Task 20 passes.

- [ ] **Step 1: README.md** — apply bottom-up.

**1a. After line 162 (end of the `feedback` json fence), before `## Local development` (line 164)** — insert:

````markdown
## Migrating from v1.x (v2.0.0 breaking changes)

v2.0.0 puts UiPath and Apex behind one tool surface, so argument names and output shapes became
backend-neutral. Skills/prompts that call the old names must be updated.

| v1.x | v2.0.0 |
|------|--------|
| (no such arg) | `orchestrator: "uipath" \| "apex"` — **required** on every job/queue-level tool; optional on `find_order_queue_items` / `find_stuck_orders` (omitted = every registered backend) |
| `jobKey` / `jobKeys` (`get_job`, `get_job_logs`) | `jobId` / `jobIds` |
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

````

**1b. After line 148 (`logging notification when it reloads.`)**, before the config-guide paragraph (line 150) — insert:

````markdown
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
  hosts and `bunx` do); **MCPB installs only pass `COPILOT_MCP_CONFIG`, so they must use the file.**
- With neither source configured, every `orchestrator: "apex"` call fails with "Apex is not
  configured"; UiPath and Copilot tools are unaffected. `doctor` reports which source was used.

`get_job_artifacts` downloads Apex artifacts to `~/.cache/copilot-mcp/artifacts/<jobId>/`
(directory mode `0700`, never inside a repo). Treat them as PHI.

````

**1c. Replace lines 103-108** (the `report-faulted-uipath-jobs` paragraph):
```markdown
`report-faulted-jobs` finds faulted jobs on the chosen `orchestrator` (`uipath` or `apex`; prod by default) and files each as a
GitHub issue on `Apex-Medical-AI-Inc/RPAPlaywright` — creating one issue per distinct fault
and commenting on the existing issue when the same fault recurs. Apex fault signatures are
`apex:`-prefixed so they never collide with UiPath ones. This server holds **no
GitHub credentials**: the prompt drives a **GitHub MCP server connected in the host** to do
the actual search/create/comment, so that server must be connected with write access to the
repo.
```

**1d. Replace line 87** `` `triage-stuck-orders`, and `report-faulted-uipath-jobs`. `` with `` `triage-stuck-orders`, and `report-faulted-jobs`. ``

**1e. Replace line 80** (the `doctor` row) — keep the existing text and append before the closing ` |`: ` When Apex is configured it adds an Apex check: which config source supplied the credentials (file or env), that login succeeds, and that both folders resolve to ids.`

**1f. Replace lines 35-61** (both UiPath sections) with:

````markdown
### Orchestrator (UiPath + Apex) — reads

All READ-ONLY. Every tool takes `env` and `orchestrator` (`uipath` | `apex`); results come back as `{ orchestrator, env, … }` with backend-neutral cores (`JobCore`, `QueueItemCore`, `QueueCore`, `ProcessCore`, `TriggerCore`) and backend-specific fields under `details`. Job `state` uses UiPath's vocabulary on both backends (Apex statuses are mapped; `rawState` keeps the original). One Apex transaction can have **many** jobs (a retry is a new job on the same queue item), so queue items carry `jobs[]`, newest first.

| Tool | Args | What it does |
|------|------|--------------|
| `list_jobs` | `env`, `orchestrator` | List the most recent jobs in a folder, newest first, no order correlation (`since`, `processName`, `top`, `folder`). Apex rows are thin (`fault.class` = `error_class`); use `get_job` for detail. |
| `get_job` | `env`, `orchestrator` | Fetch one (`jobId`) or up to 25 (`jobIds`) jobs — the light poll target after `start_job`. `includeLogDigest:true` adds a structured fault + condensed log digest; on Apex a missing log yields `logsUnavailable.reason` = `running` \| `never-started` \| `artifact-policy` instead of a digest. |
| `get_job_logs` | `env`, `orchestrator` | Fetch a job's logs (oldest first, capped 500), batchable via `jobIds` (up to 25) and filterable (`minLevel`, `contains`, `onlyFailures`, `tail`); messages truncate to 400 chars unless `fullMessages:true`; `includeRawFields` adds each row's structured fields; `includeVideo` adds a `video` reference. |
| `get_job_artifacts` | `env` (**no `orchestrator`: Apex-only**) | List a job's artifacts (`filename`, `type`, `sizeBytes`) and optionally `download` named files (or `"all"`) to `~/.cache/copilot-mcp/artifacts/<jobId>/` (mode `0700`) — `job.log`, `trace.zip`, screenshots, recording. PHI: never commit or paste them. |
| `find_order_queue_items` | `env`, `orchestrator?` | Find the queue item(s) an order flowed through and their job ids. `orchestrator` omitted = search every registered backend (each fails independently into `searchErrors`); `folder` is only accepted with an explicit `orchestrator`. Output `jobIds: { uipath?: […], apex?: […] }` feeds straight into `get_job`'s `jobIds`. |
| `list_queue_items` | `env`, `orchestrator` | Browse a portal's queue for triage — `queue` is a portal key/alias or a queue id, optionally filtered by `status`. |
| `list_queues` | `env`, `orchestrator` | List queues in a folder — use to discover a queue id (pre-prod ids differ from the prod portal registry) or the exact queue name for `add_queue_item`. |
| `list_processes` | `env`, `orchestrator` | List processes (UiPath releases / Apex workflows) — `id` is the `processId` `start_job` takes; check `version` (UiPath: `isLatestVersion`) and, on Apex, `details.artifactPolicy`. |
| `list_triggers` | `env`, `orchestrator` | List triggers (queue + time). Apex has no triggers API, so queue triggers are synthesized from queues (`enabled` = active and auto-drain) and time triggers come from schedules. |
| `pull_queue_item` | `orchestrator`, `env` (optional if `url` given) | Fetch one queue item (UiPath: Orchestrator `url` or `itemId`; Apex: `queueId` + `itemId`) and return its content as a ready-to-run test payload. `IsApproved` is always forced `false`. |
| `build_faulted_job_issue` | `env`, `orchestrator` | Read a faulted job + its logs and BUILD a ready-to-post GitHub issue payload (`title`/`body`/`labels`/`faultSignature`/`searchQuery`/`recurrenceComment`). Posts nothing — the caller hands the payload to a GitHub MCP server. |

### Orchestrator (UiPath + Apex) — writes (pre-prod only)

`env` is a schema-enforced literal `"pre_prod"` on every tool below — `"prod"` is rejected before
any HTTP call (UiPath: the dev-clone folder; Apex: the `Authorization Test` folder), and every posted payload passes a safety guard.

| Tool | Args | What it does |
|------|------|--------------|
| `add_queue_item` | `env`, `orchestrator` | POST one item to a pre-prod queue (`queue`, `reference`, `specificContent`). `IsApproved` is forced `false`; a configured `serverURL`/`queueUrl` must match pre-prod; `<TO-FILL>` placeholders are rejected. Pace ~1/s when enqueueing many. |
| `delete_queue_item` | `env`, `orchestrator` | Delete one queue item (`itemId`; Apex also `queueId`). Fetch-first: refuses unless the item's current status is `New`, so run history is never destroyed. |
| `start_job` | `env`, `orchestrator` | Start job(s) for a process (`processId`, `input`, `jobsCount` 1–5; Apex also requires `matchValue`). The job runs the process's pinned/current version as resolved at dispatch — re-check `list_processes` right before starting if others are publishing. |
| `stop_job` | `env`, `orchestrator` | Stop (`SoftStop`) or kill (`Kill`) a job. Fetch-first: refuses a job already in a terminal state. |
````

**1g. Replace lines 30-31** (`find_stuck_orders`, `build_queue_item` rows):
```markdown
| `find_stuck_orders` | `env`, `profile`, `orchestrator?` | Scan recent orders in an env and flag ones sitting in a non-terminal status (default `inProgress`/`incomplete`/`pending`). Optional `crossCheck` correlates each to its queue item(s) and job(s) on UiPath and/or Apex (`orchestrator` omitted = both) for a coarse verdict using the latest attempt; adds `job-never-started` for an Apex job that failed before any worker claimed it. Each stuck order carries `orchestrator: { backend, verdict, … }`. |
| `build_queue_item` | `env`, `profile`, `orchestrator` | Fetch an order and BUILD the queue-item request payload for the chosen orchestrator — never POSTs. `IsApproved` is always `false`. UiPath stringifies array/object fields as before; Apex keeps native types. Pair with `add_queue_item` to actually post it. |
```

**1h. Replace lines 9-14** (the tools intro):
```markdown
33 tools, grouped below by domain. **Args** lists only the arguments relevant to auth/scope
(`env`, `profile`, `orchestrator`) — see each tool's own schema for the rest. Every Copilot tool requires an
explicit `profile`; most also require an explicit `env` (`prod` | `pre_prod`) — neither is ever
defaulted. The one exception is `list_clinics`, which takes `env` but no `profile`: it's the
discovery tool that produces the profile → clinicUid mapping, so requiring a profile would be
circular. Orchestrator tools also take `env` but no `profile` (orchestrators auth globally, not per-account), plus an
`orchestrator` (`uipath` | `apex`) that is **required and never defaulted** on every job- or queue-level tool. The carve-outs:
`find_order_queue_items` and `find_stuck_orders` (omitted = every registered backend) and the Apex-only `get_job_artifacts` (no `orchestrator` arg).
```
Also in the same file, line 3-5 intro stays. Update line 112 `The server reads one validated config holding both the Copilot BE creds and the UiPath` / line 113 `args. Two ways to provide it:` → `The server reads one validated config holding the Copilot BE creds and the UiPath / Apex` + `args. Two ways to provide it:`.

- [ ] **Step 2: CLAUDE.md** (apply bottom-up; verified baseline lines)

2a. Replace lines 129-141 (the UiPath-writes invariant) with these FOUR invariants (UiPath one adjusted, three new ones from spec §10):
```markdown
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
- **Apex has one HTTP chokepoint and always scopes to a folder.** `apex/client.ts`'s `apexRequest`
  is the only Apex `fetch` (login, `apexDownload` and paging all go through it). It always sends
  `X-Folder-Id` (omitting it silently reads the user's default folder), sends only verified query
  params (unknown ones are silently ignored by the server), and refuses any request other than GET
  unless `env === "pre_prod"` — before any network call. Writes are never auto-retried (a retry could double-post).
- **Apex writes are test-folder-only and guarded.** Mirrors the UiPath invariant: schema
  `z.literal("pre_prod")`, domain assert, the chokepoint gate, `guardQueueItemSafety`, and fetch-first
  refusal (`delete_queue_item` only on `new`; `stop_job` refuses every terminal status including `lost`/`timed_out`).
  `apex/actions.ts` is the ONLY module that mutates Apex. Prod and test folders share one deployment and one login, so
  the folder is the only boundary — a bug that drops the gate could write to prod.
- **`orchestrator` is required on job- and queue-level tools, never defaulted.** The carve-outs are
  `find_order_queue_items` and `find_stuck_orders` (omitted = every registered backend) and the Apex-only
  `get_job_artifacts` (no `orchestrator` arg). **The MCP never deploys, pins, creates Processes or queues, or touches
  Apex credentials, assets or Action Center.** Artifacts download only under `~/.cache/copilot-mcp/artifacts/`
  (mode `0700`), never inside a repo.
```

2b. Replace lines 88-95 (the UiPath-only tool sentence inside the first invariant, from `the profile → clinicUid mapping (requiring a profile would be circular). UiPath-only tools` to `one of `bearer`/`oauth` is required by the config schema. Still never default `env`.`):
```markdown
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
```

2c. Replace lines 74-79 (the faults bullet):
```markdown
- **`orchestrator/faults.ts` + `uipath/faults.ts` + `apex/faults.ts`** (`build_faulted_job_issue` + the
  `report-faulted-jobs` prompt): turn a faulted job into a ready-to-post GitHub issue payload but **post nothing** —
  this server holds no GitHub credentials. The prompt drives a **GitHub MCP server connected in the host** to do the
  search/create/comment. Dedupe is by **fault signature** (`normalizeError` strips GUIDs/ids/timestamps so
  recurrences group into one issue, not by per-run job id). UiPath signatures are byte-identical to the pre-2.0
  ones; Apex signatures are `apex:`-prefixed so the two never collide. The pure `formatFaultedJobIssue` /
  `formatApexFaultIssue` / `normalizeError` are unit-tested; keep them pure.
```

2d. Insert after line 73 (end of the `mcp/notify.ts` bullet) the orchestrator-layer bullets:
```markdown
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
```

2e. Lines 43-47 (hybrid + two systems): replace `UiPath folder ids` (line 43) with `orchestrator folders`, and replace lines 46-47 with:
```markdown
- **Three external systems**: Copilot BE via `copilot/copilot-client.ts` (`makeClient`/`login`/`req`),
  UiPath Orchestrator via `uipath/uipath.ts`, and the Apex orchestrator via `apex/client.ts`. Each Copilot tenant
  exists per-env (`prod` / `pre_prod`); both orchestrators sit behind `orchestrator/backend.ts`.
```

2f. Replace lines 32, 35-37 (module list):
```markdown
- `src/config/` — `config.ts` (validated config loading; `resolveApex()` for the optional `apex` block).
```
and
```markdown
- `src/orchestrator/` — backend-neutral orchestrator contract + cross-backend tools (see below).
- `src/uipath/` — UiPath backend: `uipath.ts`, `auth.ts`, `queue.ts`, `map.ts`, `payload.ts`, `backend.ts`, `faults.ts`, `actions.ts`.
- `src/apex/` — Apex backend (see below).
- `src/mcp/` — MCP-protocol concerns: `prompts.ts`, `notify.ts`, `feedback.ts`, `reference.ts`.
- `src/shared/` — cross-cutting helpers: `util.ts`, `http.ts` (`sleep`, `rateLimitDelayMs`), `expected-error.ts`.
```

2g. Lines 7-8: `order operations + UiPath Orchestrator execution analysis.` → `order operations + orchestrator (UiPath and Apex) execution analysis.`

- [ ] **Step 3: `.claude/rules/`** (kept in sync with CLAUDE.md)

3a. `env-profile-required.md`, replace lines 9-13 (`UiPath-only tools (…` through `…circular).`):
```markdown
Orchestrator tools (`list_jobs`, `get_job`, `get_job_logs`, `get_job_artifacts`, `build_faulted_job_issue`,
`list_queues`, `list_queue_items`, `find_order_queue_items`, `pull_queue_item`, `list_processes`,
`list_triggers`, `add_queue_item`, `delete_queue_item`, `start_job`, `stop_job`) are one documented
exception: they take `env` but no `profile`, because orchestrators authenticate globally (UiPath via
`uipath.oauth`/`uipath.bearer`, Apex via the one `apex` login), not per-profile creds. They also take a
required, never-defaulted `orchestrator` (`uipath` | `apex`); the only carve-outs are
`find_order_queue_items` / `find_stuck_orders` (omitted = every backend) and the Apex-only
`get_job_artifacts`. `list_clinics`
is the other profile exception: it takes `env` but no `profile`, because it's the discovery tool that produces the
profile → clinicUid mapping (requiring a profile would be circular).
```
3b. `uipath-writes-guarded.md`: front-matter `files:` line → `files: src/uipath/actions.ts, src/orchestrator/safety.ts`; replace every `src/uipath/safety.ts` with `src/orchestrator/safety.ts` (two occurrences: the front matter and the paragraph "Every posted `SpecificContent` passes `guardQueueItemSafety` (`src/uipath/safety.ts`, …"); and in "Orchestrator state" → "UiPath Orchestrator state".
3c. `tool-response-contract.md`: in the violation signature replace `` `uipath/actions.ts`) `` with `` `uipath/actions.ts` or `apex/actions.ts`) ``.
3d. Create `.claude/rules/apex-writes-guarded.md`:
```markdown
---
id: apex-writes-guarded
files: src/apex/client.ts, src/apex/actions.ts, src/orchestrator/safety.ts
---

`src/apex/client.ts`'s `apexRequest` is the ONLY Apex `fetch`: login, paging and artifact downloads
(`apexDownload`) all go through it. It ALWAYS sends `X-Folder-Id` (omitting it silently reads the
user's default folder), sends only verified query params, and refuses any method other than GET
unless `env === "pre_prod"` — before any network call. Writes are never auto-retried.

`src/apex/actions.ts` is the ONLY module that mutates Apex (`add_queue_item`, `delete_queue_item`,
`start_job`, `stop_job`). All four take `env: z.literal("pre_prod")` at the schema layer AND assert
`env === "pre_prod"` in the domain function. Every posted `specific_content` passes
`guardQueueItemSafety` (IsApproved forced false; non-empty `serverURL`/`queueUrl` must match the
configured pre-prod values, failing closed when unset; `<TO-FILL>` rejected). `delete_queue_item` is
fetch-first and refuses unless the item's status is `new`; `stop_job` is fetch-first and refuses every
terminal status (`completed`, `failed`, `cancelled`, `lost`, `timed_out`). Writes never take a
`folder` override. The MCP never deploys, pins, creates Processes/queues, or touches Apex
credentials, assets or Action Center.

**Why:** the Apex prod and test folders share one deployment and one login, so the folder is the
only boundary; a dropped gate could write to prod.

**Violation signature:** a `fetch(` against Apex outside `apex/client.ts`; a non-GET path that
doesn't pass through the `pre_prod` gate; a request without `X-Folder-Id`; an Apex write outside
`apex/actions.ts`; a write endpoint that accepts a `folder` argument; a retry loop around a
POST; the gate test (`apex/client.test.ts`) not running before `fetch`.
```
3e. `.claude/rules/README.md`: after the `uipath-writes-guarded.md` list item add `- [apex-writes-guarded.md](apex-writes-guarded.md)`.
3f. `.claude/workflows/invariant-compliance-sweep.js` line 16: after it insert `  { id: 'apex-writes-guarded', file: '.claude/rules/apex-writes-guarded.md' },`.

- [ ] **Step 4: CHANGELOG.md** — insert before line 7 (`## [1.33.1] - 2026-09-09`):

````markdown
## [2.0.0] - 2026-10-11

One orchestrator tool surface backed by UiPath **and** Apex (the orchestrator replacing UiPath portal
by portal). Cloud-EHR decides where an order runs, so the tools look at both. Retiring UiPath later is
deleting `src/uipath/` and one enum value — no further tool renames.

### Added

- **Apex orchestrator backend** (`src/apex/`): reads (jobs, logs + digest, queues, queue items, processes,
  triggers, order → item → job correlation, fault → GitHub-issue payload) and pre-prod-only writes
  (`add_queue_item`, `delete_queue_item`, `start_job`, `stop_job`) behind the same tools as UiPath. One HTTP
  chokepoint that always sends `X-Folder-Id` and refuses any non-GET unless `env=pre_prod`.
- **`orchestrator` argument** (`uipath` | `apex`) on every job/queue-level tool.
- **`get_job_artifacts`** (Apex-only): list a job's artifacts and download `job.log`, `trace.zip`, … to
  `~/.cache/copilot-mcp/artifacts/<jobId>/` (dir mode `0700`, never inside a repo).
- **`apex` config block** (`apiUrl` = LB host, never CloudFront; `username`/`password`; optional `folderByEnv`;
  `serverUrlByEnv`/`queueUrl` write-guard limits) with an env fallback: `APEX_API_URL` / `APEX_USER_NAME` /
  `APEX_PASSWORD` for any field the file omits. MCPB installs only pass `COPILOT_MCP_CONFIG` and must use the file.
- `doctor` Apex check (config source file|env, login, both folders resolve).
- `find_stuck_orders` verdict `job-never-started` (Apex job failed before any worker claimed it) and
  cross-backend correlation using the latest attempt.
- Resource `copilot://reference/orchestrators`; Apex entries in `safety-rules` and `config-guide`.

### Changed

- **BREAKING — argument renames:** `jobKey`/`jobKeys` → `jobId`/`jobIds` (`get_job`, `get_job_logs`);
  `releaseKey` → `processId` and `inputArguments` → `input` (`start_job`); `queueName` / `queueDefId`
  (`list_queue_items`) and `queueName` (`add_queue_item`) → `queue`; `queueDefinitionId` (`list_triggers`) →
  `queueId`; `txnId` → `itemId` (`delete_queue_item`, `pull_queue_item`; Apex also takes `queueId`);
  `crossCheckUipath` → `crossCheck` (`find_stuck_orders`).
- **BREAKING — `orchestrator` is required** on `list_jobs`, `get_job`, `get_job_logs`, `build_faulted_job_issue`,
  `list_queues`, `list_queue_items`, `pull_queue_item`, `list_processes`, `list_triggers`, `build_queue_item`,
  `add_queue_item`, `delete_queue_item`, `start_job`, `stop_job`; optional on `find_order_queue_items` and
  `find_stuck_orders` (omitted = every registered backend; `folder` only with an explicit `orchestrator`).
- **BREAKING — output shapes:** every orchestrator tool returns `{ orchestrator, env, … }`; jobs, queue items,
  queues, processes and triggers are backend-neutral cores with backend-specific fields under `details`
  (`get_job.job` is a `JobCore`); `find_order_queue_items` `jobKeys` → `jobIds: { uipath?, apex? }`;
  `find_stuck_orders` per-order `uipath` → `orchestrator`; `get_job_logs` `videoUrl` → `video` (a url on UiPath, an
  artifact reference on Apex); `jobDeepLinkBase` is gone — every job carries its own `link`. UiPath `state`
  values and UiPath fault signatures are unchanged; Apex statuses map onto the same vocabulary (`rawState`
  keeps the original).
- **BREAKING —** prompt `report-faulted-uipath-jobs` → `report-faulted-jobs` (adds `orchestrator`); other prompts
  that call changed tools use the new argument names. Resource `copilot://reference/uipath-folders` →
  `copilot://reference/orchestrators`.
- Generic code moved out of `src/uipath/` into `src/orchestrator/` (log digest/semantics, safety guard, fault
  normalization, canonical queue-item content) and `src/shared/` (`rateLimitDelayMs`, `ExpectedError`).
  UiPath `build_queue_item` output is unchanged.
- The `server.ts` header comment's tool list is re-synced with the registered tools.

### Removed

- The old UiPath-only argument names and output fields listed above (no aliases),
  `copilot://reference/uipath-folders`, and the `report-faulted-uipath-jobs` prompt name.

````

- [ ] **Step 5: Version bump + manifest** — one exact-string script: `package.json`, `manifest.json` and `src/server.ts` are edited by exact replacement (no JSON parse/stringify, so formatting noise is impossible); each anchor must match exactly once or the script throws before writing anything.
```bash
bun -e '
import { readFileSync, writeFileSync } from "node:fs";
const edit = (file, pairs) => {
  let s = readFileSync(file, "utf8");
  for (const [a, b] of pairs) {
    const n = s.split(a).length - 1;
    if (n !== 1) throw new Error(`${file}: anchor matched ${n}x: ${a}`);
    s = s.replace(a, () => b);
  }
  writeFileSync(file, s);
};
edit("src/server.ts", [[`const VERSION = "1.33.1";`, `const VERSION = "2.0.0";`]]);
edit("package.json", [
  [`"version": "1.33.1"`, `"version": "2.0.0"`],
  [`and UiPath Orchestrator execution analysis"`, `and UiPath + Apex Orchestrator execution analysis"`],
]);
edit("manifest.json", [
  [`"version": "1.33.1"`, `"version": "2.0.0"`],
  [`queue-item build, and UiPath Orchestrator execution analysis"`, `queue-item build, and UiPath + Apex Orchestrator execution analysis"`],
  [`Exposes EHR Copilot order operations and UiPath Orchestrator execution analysis as MCP tools`, `Exposes EHR Copilot order operations and UiPath + Apex Orchestrator execution analysis as MCP tools`],
  [`building/posting UiPath queue items against a pre-prod dev clone`, `building/posting queue items against the pre-prod UiPath dev clone or Apex Authorization Test folder`],
  [`Copilot BE credentials per profile and UiPath Orchestrator credentials`, `Copilot BE credentials per profile and UiPath and/or Apex Orchestrator credentials`],
  [`(Copilot BE creds per profile + UiPath Orchestrator creds). See copilot-mcp.config.example.json bundled alongside this manifest for the exact shape.`, `(Copilot BE creds per profile + UiPath and/or Apex Orchestrator creds; Apex must be in this file — MCPB installs pass no other environment). See copilot-mcp.config.example.json bundled alongside this manifest for the exact shape.`],
  [`    "uipath",\n`, `    "uipath",\n    "apex",\n`],
  [`"Build UiPath AddQueueItem request from an order"`, `"Build a queue-item payload (UiPath or Apex) from an order"`],
  [`"Find the UiPath queue item(s) a Copilot order flowed through"`, `"Find the queue item(s) and job(s) a Copilot order flowed through (UiPath and/or Apex)"`],
  [`"Pull a UiPath queue item as a test payload"`, `"Pull a UiPath or Apex queue item as a test payload"`],
  [`"Browse a UiPath queue"`, `"Browse a UiPath or Apex queue"`],
  [`"List recent UiPath Orchestrator jobs"`, `"List recent orchestrator jobs (UiPath or Apex)"`],
  [`"Get a UiPath job\x27s robot logs"`, `"Get a job\x27s robot logs (UiPath or Apex)"`],
  [`"Build a GitHub issue payload for a faulted UiPath job"`, `"Build a GitHub issue payload for a faulted job (UiPath or Apex)"`],
  [`"List UiPath queue definitions in a folder"`, `"List queues in an orchestrator folder (UiPath or Apex)"`],
  [`"List UiPath releases (processes) in a folder"`, `"List processes in an orchestrator folder (UiPath releases or Apex workflows)"`],
  [`"List UiPath triggers (queue + time) in a folder"`, `"List triggers (queue + time) in an orchestrator folder"`],
  [`      "description": "Get a single UiPath job\x27s state by Key"\n    },\n`, `      "description": "Get job(s) by id (UiPath or Apex)"\n    },\n    {\n      "name": "get_job_artifacts",\n      "description": "List and download an Apex job\x27s artifacts (job.log, trace.zip, recording)"\n    },\n`],
  [`"Post a queue item to a dev-clone queue"`, `"Post a queue item to a pre-prod queue (UiPath dev clone or Apex Authorization Test)"`],
  [`"Delete a New queue item from the dev clone"`, `"Delete a New queue item from a pre-prod queue (UiPath dev clone or Apex Authorization Test)"`],
  [`"Start a UiPath job on the dev clone"`, `"Start a job in the pre-prod folder (UiPath dev clone or Apex Authorization Test)"`],
  [`"Stop or kill a running UiPath job on the dev clone"`, `"Stop or kill a running job in the pre-prod folder (UiPath dev clone or Apex Authorization Test)"`],
]);
'
git diff --stat package.json manifest.json src/server.ts
```
Expected: no throw; `git diff --stat` shows `package.json | 4 +-`, `manifest.json` ≈ 30 lines changed, `src/server.ts | 2 +-` (the Task 18/5 edits are already committed). If an anchor throws "matched 0x", a previous task already reworded that string — print the live text (`rg -n '<fragment>' manifest.json`) and adjust only that pair.

- [ ] **Step 6: Verify** 
```bash
bun -e 'const m=JSON.parse(await Bun.file("manifest.json").text()); const p=JSON.parse(await Bun.file("package.json").text()); console.log(m.version, p.version, m.tools.length, m.tools.some(t=>t.name==="get_job_artifacts"))'
rg -n "1\.33\.1" package.json manifest.json src/server.ts
bunx mcpb validate manifest.json
bun run typecheck && bun test && bunx biome check src
```
Expected: `2.0.0 2.0.0 33 true` (manifest has 33 entries: it already lacked `list_clinics`, so tool count = 32 existing manifest entries + `get_job_artifacts`; if the printed count is 32, `list_clinics` is absent as before — that is a pre-existing gap, not touched here); `rg` prints nothing; `mcpb validate` passes; gate green.

- [ ] **Step 7: Commit**
```bash
git add README.md CLAUDE.md CHANGELOG.md package.json manifest.json src/server.ts .claude/rules .claude/workflows/invariant-compliance-sweep.js
git commit -m "chore(release): v2.0.0 — docs, changelog, version bump for the Apex orchestrator" -m "README migration table + Apex config, CLAUDE.md architecture and the Apex invariants (one chokepoint, test-folder-only guarded writes, required orchestrator arg, no deploy/pin), rule files kept in sync, CHANGELOG 2.0.0 with every breaking rename and output change. Version is bumped in package.json, manifest.json and server.ts; the v2.0.0 tag is pushed only after the Task 20 live verification passes."
```

---

### Task 20: Live verification (spec §13) — executable checklist

**Files:**
- Create (outside the repo, throwaway): `$V/helpers.sh`, `$V/mcp-call.mjs` under `~/.cache/copilot-mcp/verify/<timestamp>/` (dir mode `0700`; every PHI-bearing tool output lands here, not in chat and not in the repo)
- Modify: none in the repo. Test: none (this task is the live acceptance run). Commit: none — the only artifact is the final report (template at the end), posted to the user.

**Interfaces:**
- Consumes: the finished server (`dist/server.js` built from this branch after Task 19), a working config (`COPILOT_MCP_CONFIG`, with `apex` set in the file or via `APEX_API_URL`/`APEX_USER_NAME`/`APEX_PASSWORD` exported in the shell that runs these steps), the skill `trace-preprod-be-submit-to-apex-queue` (for the go-ahead gate only), tag `v1.33.1` (UiPath baseline).
- Produces: a pass/fail report; on all-pass the caller may `git tag v2.0.0 && git push origin v2.0.0`.

**Rules for the whole run (read first):**
1. **GET-only until Step V8.** The only writes before the go-ahead gate are the add→delete probe on the inactive `CenCal shared-adapter test queue` and refusal probes that cannot change state.
2. **PHI hygiene.** Pre-prod orders are clones of prod data: treat every `specificContent`, `job.log`, `result_payload`, screenshot, `trace.zip` and order/member field as PHI. Never `cat`/paste/summarise them. All calls go through `call` (below), which writes the full JSON to `$V/<label>.json` and prints only the exit code and byte size; you then print **ids, statuses, booleans, counts and key names** with `jq`. Report `orderUid`s truncated to 8 chars. Never print the config, tokens, or `$V/*.json` wholesale. Delete `$V` at the end (V11).
3. **Stop on surprise.** A step marked **STOP-IF** that is violated means: do not continue to later writes; record FAIL and report.
4. If the host already exposes this build's tools as MCP tools you may call them directly for non-PHI-bearing checks; the expected observations are identical. The helper is the default because it can also run the v1.33.1 baseline and keeps PHI out of the transcript.
5. jq paths use recursive descent (`..`) where the exact nesting of the envelope is incidental, so a harmless re-nesting does not break an assertion; the *observation* is what must hold.

- [ ] **V0: Build, smoke, helpers**
```bash
export REPO=/home/belal/projects/copilot-mcp
export COPILOT_MCP_CONFIG=${COPILOT_MCP_CONFIG:?set to the config file the host uses}
export V=$HOME/.cache/copilot-mcp/verify/$(date +%Y%m%d-%H%M%S); mkdir -p -m 700 "$V"
cd "$REPO" && git status --porcelain && git rev-parse --short HEAD     # expect: empty status; record the sha
bun run typecheck && bun test && bunx biome check src                  # expect: green
bun run build && ls -l dist/server.js                                  # expect: built, executable
```
Helper script (copy exactly):
```bash
cat > "$V/mcp-call.mjs" <<'EOF'
// usage: bun mcp-call.mjs <server.js> list | read <uri> | <tool> ['<json args>']
const [serverJs, mode, raw] = process.argv.slice(2);
const dir = `${process.env.REPO}/node_modules/@modelcontextprotocol/sdk/dist/esm/client`;
const { Client } = await import(`${dir}/index.js`);
const { StdioClientTransport } = await import(`${dir}/stdio.js`);
const env = Object.fromEntries(Object.entries(process.env).filter(([, v]) => v !== undefined));
const client = new Client({ name: "verify", version: "0" });
await client.connect(new StdioClientTransport({ command: "node", args: [serverJs], env }));
let out;
let failed = false;
try {
  if (mode === "list") {
    out = { tools: (await client.listTools()).tools, resources: (await client.listResources()).resources };
  } else if (mode === "read") {
    out = JSON.parse((await client.readResource({ uri: raw })).contents[0].text);
  } else {
    const r = await client.callTool({ name: mode, arguments: JSON.parse(raw ?? "{}") });
    failed = r.isError === true;
    const t = r.content?.[0]?.text ?? "";
    try { out = JSON.parse(t); } catch { out = { text: t }; }
  }
} catch (e) {
  failed = true;
  out = { protocolError: String(e?.message ?? e) };
}
process.stdout.write(JSON.stringify(out));
await client.close();
process.exit(failed ? 1 : 0);
EOF
cat > "$V/helpers.sh" <<'EOF'
# source this in every shell call; needs REPO, V, COPILOT_MCP_CONFIG (+ APEX_* if Apex is env-configured)
SERVER=${SERVER:-$REPO/dist/server.js}
call() { # call <label> <tool|list|read> ['<json args | uri>']  -> $V/<label>.json
  local args=${3:-}; [ -z "$args" ] && args='{}'
  bun "$V/mcp-call.mjs" "$SERVER" "$2" "$args" > "$V/$1.json"; local rc=$?
  echo "$1 exit=$rc bytes=$(wc -c < "$V/$1.json")"; return 0
}
EOF
source "$V/helpers.sh"
```
Raw stdio smoke (`node dist/server.js` starts and lists tools):
```bash
{ printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' '{"jsonrpc":"2.0","method":"notifications/initialized"}' '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' '{"jsonrpc":"2.0","id":3,"method":"resources/list"}'; sleep 4; } | timeout 30 node "$REPO/dist/server.js" > "$V/smoke.jsonl"; echo "server exit=$?"
jq -s -c '{version:(.[]|select(.id==1)|.result.serverInfo.version), tools:(.[]|select(.id==2)|.result.tools|length), hasArtifacts:(.[]|select(.id==2)|.result.tools|any(.name=="get_job_artifacts")), orchestratorProp:(.[]|select(.id==2)|.result.tools|map(select(.inputSchema.properties.orchestrator))|length), orchestratorRequired:(.[]|select(.id==2)|.result.tools|map(select(.inputSchema.required//[]|index("orchestrator")))|length), orchEnum:([.[]|select(.id==2)|.result.tools[]|.inputSchema.properties.orchestrator.enum]|map(select(.!=null))|unique), writeEnvConst:([.[]|select(.id==2)|.result.tools[]|select(.name|IN("add_queue_item","delete_queue_item","start_job","stop_job"))|(.inputSchema.properties.env|(.const // .enum[0]))]|unique), resources:(.[]|select(.id==3)|.result.resources|map(.uri)|map(select(test("orchestrators|uipath-folders"))))}' "$V/smoke.jsonl"
```
Expected (server exit 0 after stdin closes): `version:"2.0.0"`, `tools:33`, `hasArtifacts:true`, `orchestratorProp:16`, `orchestratorRequired:14` (the 2 optional ones are `find_order_queue_items`, `find_stuck_orders`), `orchEnum:[["uipath","apex"]]`, `writeEnvConst:["pre_prod"]`, `resources:["copilot://reference/orchestrators"]` (no `uipath-folders`). **STOP-IF** any differs.

- [ ] **V1: Resource + doctor**
```bash
call v1-res read 'copilot://reference/orchestrators'
jq -c '{apexFolders:(.apex.folders|map_values(.name)), uipathFolders:(.uipath.folders|map_values(.name)), jobMap:(.stateLabels.apexJobStatusToState|length), term:.terminal}' "$V/v1-res.json"
jq -r '.copilot.profiles|keys[]' "$COPILOT_MCP_CONFIG"          # profile NAMES only; pick any one as <PROFILE>
call v1-doctor doctor '{"profile":"<PROFILE>"}'
jq . "$V/v1-doctor.json"                                        # doctor output is status-only; safe to read
```
Expected: `apexFolders {"prod":"Authorization","pre_prod":"Authorization Test"}`, UiPath names `Authorization` / `Authorization Dev Clone`, `jobMap 11`. Doctor: Copilot + UiPath checks as in v1.33.1, plus an Apex check that is OK with the source (`file` or `env`, matching how you configured it), login OK, and both folders resolved (names above, UUID ids). **STOP-IF** Apex login fails — nothing later can pass.

- [ ] **V2: Apex discovery, both folders (GET-only)**
```bash
for E in pre_prod prod; do
  call v2-queues-$E list_queues "{\"env\":\"$E\",\"orchestrator\":\"apex\",\"top\":200}"
  call v2-procs-$E  list_processes "{\"env\":\"$E\",\"orchestrator\":\"apex\",\"top\":200}"
  call v2-trig-$E   list_triggers "{\"env\":\"$E\",\"orchestrator\":\"apex\",\"top\":200}"
  jq -c "{env:\"$E\", folder:(.folder//null), queues:([..|objects|select(has(\"active\") and has(\"name\"))]|length), inactive:([..|objects|select(has(\"active\") and has(\"name\") and .active==false)|.name])}" "$V/v2-queues-$E.json"
done
export TEST_QUEUE_ID=$(jq -r '[..|objects|select(.name=="CenCal shared-adapter test queue")][0].id' "$V/v2-queues-pre_prod.json"); echo "TEST_QUEUE_ID=$TEST_QUEUE_ID"
jq -c '[..|objects|select(.name=="CenCal shared-adapter test queue")|{active,orchestrator,name}]' "$V/v2-queues-pre_prod.json"
jq -c '[..|objects|select(has("package") and has("details"))|.details.artifactPolicy]|group_by(.)|map({p:.[0],n:length})' "$V/v2-procs-pre_prod.json"
Q=$(jq '[..|objects|select(has("active") and has("name"))]|length' "$V/v2-queues-pre_prod.json")
jq -c --argjson q "$Q" '{queueTriggers:([..|objects|select(.kind=="queue")]|length), expected:$q, timeTriggers:([..|objects|select(.kind=="time")]|length)}' "$V/v2-trig-pre_prod.json"
call v2-trig-one list_triggers "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"queueId\":\"$TEST_QUEUE_ID\"}"
jq -c '[..|objects|select(.kind=="queue")|{queueId,enabled}]' "$V/v2-trig-one.json"
```
Expected: pre_prod `folder` = `Authorization Test`, prod `folder` = `Authorization`; queue names look like `<PORTAL KEY> auth submit queue` (+ `Unrouted`, casing varies); `CenCal shared-adapter test queue` exists in `pre_prod` with `active:false` and its id is non-empty; every process has `details.artifactPolicy` ∈ `always|on_failure|never`; queue-kind trigger count equals the queue count; `list_triggers` with `queueId=TEST_QUEUE_ID` returns exactly that one queue trigger with `enabled:false`. **STOP-IF** the test queue is missing or `active` is not `false` — do not run V8.

- [ ] **V3: Filters actually filter (count checks)**
```bash
# 3a items: status filter on the busiest portal queue
call v3-items-all list_queue_items '{"env":"pre_prod","orchestrator":"apex","queue":"CENCAL","top":200}'
jq -c '[..|objects|select(has("rawStatus"))]|{n:length, byStatus:(group_by(.status)|map({s:.[0].status,n:length}))}' "$V/v3-items-all.json"
```
Pick a `<STATUS>` present in `byStatus` that is NOT all items (e.g. `Successful`); if only one status exists in the queue, try `queue:"PMG"` or another portal key; if no queue has ≥2 statuses mark V3a INCONCLUSIVE (not PASS).
```bash
call v3-items-st list_queue_items '{"env":"pre_prod","orchestrator":"apex","queue":"CENCAL","status":"<STATUS>","top":200}'
jq -c --arg s "<STATUS>" '[..|objects|select(has("rawStatus"))]|{n:length, allMatch:(all(.status==$s))}' "$V/v3-items-st.json"
```
Expected: `allMatch:true` and `n` equals the `<STATUS>` count from `byStatus` (when the unfiltered total is < 200; if it hit 200, `n ≤ 200` and `allMatch:true`). Also assert the label mapping: every `status` ∈ `New|InProgress|Successful|Failed|Retried|Abandoned|Deleted`.
```bash
# 3b jobs: workflow_id filter (via processName) + created_after (via since)
call v3-jobs-all list_jobs '{"env":"pre_prod","orchestrator":"apex","top":500}'
jq -c '[..|objects|select(has("rawState"))]|{n:length, byProc:(group_by(.process.name)|map({p:.[0].process.name,n:length})|sort_by(-.n)|.[0:6])}' "$V/v3-jobs-all.json"
```
Pick `<SUBSTR>` = a distinctive lowercase fragment of a process name that is not all jobs.
```bash
call v3-jobs-proc list_jobs '{"env":"pre_prod","orchestrator":"apex","top":500,"processName":"<SUBSTR>"}'
jq -c --arg s "<SUBSTR>" '[..|objects|select(has("rawState"))]|{n:length, allMatch:(all(.process.name|ascii_downcase|contains($s)))}' "$V/v3-jobs-proc.json"
jq -c --arg s "<SUBSTR>" '[..|objects|select(has("rawState"))|select(.process.name|ascii_downcase|contains($s))]|length' "$V/v3-jobs-all.json"
MID=$(jq -r '[..|objects|select(has("rawState"))|.createdAt]|sort|.[length/2|floor]' "$V/v3-jobs-all.json"); echo "MID=$MID"
call v3-jobs-since list_jobs "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"top\":500,\"since\":\"$MID\"}"
jq -c --arg m "$MID" '[..|objects|select(has("rawState"))]|{n:length, allAfter:(all(.createdAt>=$m))}' "$V/v3-jobs-since.json"
```
Expected: `processName` result `allMatch:true`, `n` equals the matching count from `v3-jobs-all` (if `v3-jobs-all` n < 500) and is strictly less than the unfiltered `n`; `since` result `allAfter:true`, about half the rows. Also assert the job label map live over `v3-jobs-all`:
```bash
jq -c '[..|objects|select(has("rawState"))]|map({r:.rawState,s:.state,t:.terminal})|unique' "$V/v3-jobs-all.json"
```
Expected pairs only from: `queued→Pending, claimed→Claimed, running→Running, completed→Successful, failed→Faulted, cancelled→Stopped, cancelling→Stopping, lost→Lost, timed_out→Timed Out, suspended→Suspended, resumed→Resumed`; `terminal:true` exactly for `completed, failed, cancelled, lost, timed_out`.

- [ ] **V4: `find_order_queue_items` across backends**
Pick `<ORDER_UID>`: an order with several Apex items in `Authorization Test`:
```bash
jq -r '[..|objects|select(has("rawStatus") and .orderUid!=null)]|group_by(.orderUid)|map({o:.[0].orderUid,n:length})|sort_by(-.n)|.[0]|"\(.o[0:8])… items=\(.n)"' "$V/v3-items-all.json"
export ORDER_UID=$(jq -r '[..|objects|select(has("rawStatus") and .orderUid!=null)]|group_by(.orderUid)|sort_by(-length)|.[0][0].orderUid' "$V/v3-items-all.json")
```
(≥2 items = retried/requeued order; if the best is 1 item, record "multi-item path not exercised live".)
```bash
call v4-apex find_order_queue_items "{\"env\":\"pre_prod\",\"orderUid\":\"$ORDER_UID\",\"orchestrator\":\"apex\",\"includeSpecificContent\":true}"
jq -c --arg o "$ORDER_UID" '[..|objects|select(has("rawStatus"))] as $i | {items:($i|length), exactOrder:($i|all(.orderUid==$o)), newestFirst:(($i|map(.createdAt))==($i|map(.createdAt)|sort|reverse)), jobsPerItem:($i|map(.jobs|length)), jobsNewestFirst:($i|all((.jobs|map(.id))|length>=0)), tokensRedacted:([..|objects|select(has("token"))|.token]|all(.=="[redacted]")), hasContent:($i|all(.specificContent!=null))}' "$V/v4-apex.json"
jq -c '.jobIds' "$V/v4-apex.json"
call v4-both find_order_queue_items "{\"env\":\"pre_prod\",\"orderUid\":\"$ORDER_UID\"}"
jq -c '{jobIds:.jobIds, apexItems:([..|objects|select(has("rawStatus") and .orchestrator=="apex")]|length), searchErrors:(.searchErrors//null)}' "$V/v4-both.json"
call v4-none find_order_queue_items '{"env":"pre_prod","orderUid":"00000000-0000-0000-0000-000000000000","orchestrator":"apex"}'
jq -c '{items:([..|objects|select(has("rawStatus"))]|length), isError:(.protocolError//null)}' "$V/v4-none.json"
call v4-folder-bad find_order_queue_items "{\"env\":\"pre_prod\",\"orderUid\":\"$ORDER_UID\",\"folder\":\"Authorization\"}"; head -c 300 "$V/v4-folder-bad.json"
```
Expected: `exactOrder:true` (exact `orderUid` match, no fuzzy neighbours), `newestFirst:true`, `tokensRedacted:true`, `hasContent:true`; items with retries have `jobsPerItem` ≥ 2 and each `jobs[]` newest first (spot-check `attempt` descending with `jq '[..|objects|select(has("rawState"))|.attempt]'`); `jobIds.apex` equals the union of all `jobs[].id` (compare with `jq`); `v4-both` returns the same Apex items and a `jobIds` object keyed by backend, with a UiPath failure (if any) isolated in `searchErrors.uipath` rather than failing the call; `v4-none` → `items:0` and no error; `v4-folder-bad` (folder without an explicit orchestrator) fails with a message saying `folder` needs an explicit `orchestrator`.

- [ ] **V5: `get_job` with digest — failed, never-started, running, artifact-policy**
```bash
JOBS='[..|objects|select(has("rawState"))]'
export FAILED_ID=$(jq -r "$JOBS|map(select(.rawState==\"failed\" and .startedAt!=null))|.[0].id" "$V/v3-jobs-all.json")
export NEVER_ID=$(jq -r "$JOBS|map(select(.rawState==\"failed\" and .startedAt==null))|.[0].id" "$V/v3-jobs-all.json")
export RUNNING_ID=$(jq -r "$JOBS|map(select(.rawState|IN(\"running\",\"claimed\")))|.[0].id" "$V/v3-jobs-all.json")
echo "failed=$FAILED_ID never=$NEVER_ID running=$RUNNING_ID"
```
`null` = none in the newest 500 jobs → widen with `since`/`env:"prod"` + `folder`-less (prod Authorization folder is read-only safe), or record "no live sample" for that class. For the artifact-policy case pick a `completed` job whose process (V2) has `artifactPolicy` `on_failure` or `never`.
```bash
call v5-failed get_job "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"jobId\":\"$FAILED_ID\",\"includeLogDigest\":true}"
jq -c '..|objects|select(has("rawState"))|{state,rawState,terminal,attempt,retryOf:.retryOfJobId,fault,linkEndsWithId:(.link|endswith("/jobs/'"$FAILED_ID"'")),neverStarted:.details.neverStarted,errorClass:.details.errorClass}' "$V/v5-failed.json" | head -c 600
jq -c '[..|objects|select(has("byLevel"))|{keys:keys, run:.run}]|.[0]' "$V/v5-failed.json"
jq -c '[..|objects|select(has("logsUnavailable"))|.logsUnavailable]' "$V/v5-failed.json"
```
Expected (failed, started): `state:"Faulted"`, `terminal:true`, `fault.signature` starts with `apex:`, `fault.class` non-null, `fault.message` non-null; digest keys include `byLevel`, `failures`, `stalls` and `run` has `exitCode, outcome, errorClass, timedOut, cancelled`; `link` ends `/jobs/<id>`. If the job's policy skipped the log instead, `logsUnavailable.reason` is `artifact-policy` and no digest — acceptable only if the process policy explains it.
```bash
call v5-never get_job "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"jobId\":\"$NEVER_ID\",\"includeLogDigest\":true}"
jq -c '[..|objects|select(has("rawState"))|{startedAt,neverStarted:.details.neverStarted,class:.fault.class}], [..|objects|select(has("logsUnavailable"))|.logsUnavailable.reason], ([..|objects|select(has("byLevel"))]|length)' "$V/v5-never.json"
call v5-batch get_job "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"jobIds\":[\"$FAILED_ID\",\"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee\"],\"includeLogDigest\":false}"
jq -c '[..|objects|select(has("found"))|{found,hasError:(.error!=null)}]' "$V/v5-batch.json"
```
Expected (never-started): `startedAt:null`, `neverStarted:true`, `fault.class` usually `missing_credential`, `logsUnavailable.reason:"never-started"`, digest count `0`, no error. Batch: the call succeeds; entry 1 `found:true`, entry 2 `found:false` or carrying its own `error` — one bad id never fails the batch. If `RUNNING_ID` exists: same call → `logsUnavailable.reason:"running"`, `terminal:false`.

- [ ] **V6: `get_job_logs` filters (on `$FAILED_ID`)**
```bash
LROWS='[..|objects|select(has("Level") and has("Message"))]'
call v6-all  get_job_logs "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"jobId\":\"$FAILED_ID\"}"
call v6-warn get_job_logs "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"jobId\":\"$FAILED_ID\",\"minLevel\":\"warn\"}"
call v6-fail get_job_logs "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"jobId\":\"$FAILED_ID\",\"onlyFailures\":true}"
call v6-cont get_job_logs "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"jobId\":\"$FAILED_ID\",\"contains\":\"adapter\"}"
call v6-tail get_job_logs "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"jobId\":\"$FAILED_ID\",\"tail\":5}"
call v6-raw  get_job_logs "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"jobId\":\"$FAILED_ID\",\"includeRawFields\":true}"
call v6-vid  get_job_logs "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"jobId\":\"$FAILED_ID\",\"includeVideo\":true}"
call v6-full get_job_logs "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"jobId\":\"$FAILED_ID\",\"fullMessages\":true}"
for f in all warn fail cont tail; do echo -n "$f: "; jq -c "$LROWS|{n:length, levels:(map(.Level|ascii_upcase)|unique), maxMsg:(map(.Message|length)|max)}" "$V/v6-$f.json"; done
jq -c "$LROWS|{adapterOnly:(all(.Message|ascii_downcase|contains(\"adapter\")))}" "$V/v6-cont.json"
jq -c "{withRaw:($LROWS|map(select(.raw!=null))|length), plainNoRaw:($LROWS|map(select(.raw==null))|length)}" "$V/v6-raw.json"
jq -c "{noRawWhenOff:($LROWS|all(.raw==null)), maxFull:($LROWS|map(.Message|length)|max)}" "$V/v6-all.json"; jq -c "[$LROWS|.[]|.Message|length]|max" "$V/v6-full.json"
jq -c '[..|objects|select(has("video") or has("videoError"))|{video:(.video|if .==null then null else {kind,artifact,sizeBytes,fetchWith} end),videoError}]' "$V/v6-vid.json"
```
Expected: `warn` levels ⊆ {WARN, ERROR} (no INFO) and `n` ≤ `all.n`; `fail` ⊆ `all`; `cont` `adapterOnly:true`; `tail` `n ≤ 5`; `raw` has ≥1 row with `raw` (NDJSON rows that had `fields`) and plain rows without; default (`all`) `maxMsg` ≤ ~403 (400 + ellipsis) while `fullMessages` max ≥ it (equal only if no message is long); `video` is `{kind:"artifact", artifact:"…recording.mp4", sizeBytes:n, fetchWith:"get_job_artifacts"}` when the job has a recording, else absent with no error. Rows never contain a field value you print — only counts/levels.

- [ ] **V7: `get_job_artifacts` (list + download + path discipline)**
```bash
call v7-list get_job_artifacts "{\"env\":\"pre_prod\",\"jobId\":\"$FAILED_ID\"}"
jq -c '[..|objects|select(has("filename"))|{filename,type,sizeBytes}]' "$V/v7-list.json"
call v7-dl get_job_artifacts "{\"env\":\"pre_prod\",\"jobId\":\"$FAILED_ID\",\"download\":[\"job.log\",\"trace.zip\"]}"
jq -c '[..|objects|select(has("path"))|{filename,sizeBytes,path}]' "$V/v7-dl.json"
D=$HOME/.cache/copilot-mcp/artifacts/$FAILED_ID
stat -c '%a %n' "$D" "$D/job.log" "$D/trace.zip"; stat -c '%s %n' "$D/job.log" "$D/trace.zip"
head -n1 "$D/job.log" | cut -c1-14; unzip -tq "$D/trace.zip"
git -C "$REPO" status --porcelain                                  # expect empty: nothing written inside the repo
call v7-bad get_job_artifacts "{\"env\":\"pre_prod\",\"jobId\":\"$FAILED_ID\",\"download\":[\"../../escape.txt\"]}"; jq -c '{err:(.protocolError//.text//null)|tostring|.[0:160]}' "$V/v7-bad.json"; ls "$HOME/.cache/copilot-mcp/artifacts/" | head
call v7-never get_job_artifacts "{\"env\":\"pre_prod\",\"jobId\":\"$NEVER_ID\"}"; jq -c '[..|objects|select(has("filename"))]|length' "$V/v7-never.json"
```
Expected: listing rows `{filename,type,sizeBytes}` with `type` ∈ `logs|videos|other` and `job.log` present (`pre_prod` = `Authorization Test` is a **non-default folder**, so a successful listing proves `X-Folder-Id` is sent — without it the API 404s); downloaded paths under `~/.cache/copilot-mcp/artifacts/<jobId>/`; dir mode `700`; file sizes equal the listed `sizeBytes`; first line starts `=== apex job `; `unzip -tq` says `No errors detected`; repo `git status` empty; `../../escape.txt` fails (not in the listing / invalid name) and creates nothing outside the artifacts dir; the never-started job lists `0` artifacts. Optionally `download:"all"` on a job whose listed total is < 5 MB.

- [ ] **V8: Safe write probe on the inactive test queue (add → verify idle → delete)**
Preconditions from V2 (`TEST_QUEUE_ID` set, `active:false`). Pick any `<AUTOMATION_ID>` from the static resource (not PHI):
```bash
call v8-auto read 'copilot://reference/account-automation-ids'; export AUTOMATION_ID=$(jq -r 'to_entries[0].value' "$V/v8-auto.json")
call v8-before list_queue_items "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"queue\":\"$TEST_QUEUE_ID\",\"top\":200}"
N0=$(jq '[..|objects|select(has("rawStatus"))]|length' "$V/v8-before.json"); echo "N0=$N0"
jq -c '[..|objects|select(has("rawStatus"))|.status]|group_by(.)|map({s:.[0],n:length})' "$V/v8-before.json"
call v8-jobs0 list_jobs '{"env":"pre_prod","orchestrator":"apex","top":500}'; J0=$(jq '[..|objects|select(has("rawState"))]|length' "$V/v8-jobs0.json"); echo "J0=$J0"
```
8.1 Refusals that cannot change state (each must fail, and `N0` must not change):
```bash
call v8-prod add_queue_item "{\"env\":\"prod\",\"orchestrator\":\"apex\",\"queue\":\"$TEST_QUEUE_ID\",\"reference\":\"verify-x\",\"specificContent\":{\"orderUid\":\"verify-x\",\"automationId\":\"$AUTOMATION_ID\"}}"; jq -c '{err:(.protocolError//.text//null)|tostring|.[0:140]}' "$V/v8-prod.json"
call v8-url add_queue_item "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"queue\":\"$TEST_QUEUE_ID\",\"reference\":\"verify-x\",\"specificContent\":{\"orderUid\":\"verify-x\",\"automationId\":\"$AUTOMATION_ID\",\"serverURL\":\"https://prod-be-batch.ehrcopilotbe.com\"}}"; jq -c '{err:(.protocolError//.text//null)|tostring|.[0:140]}' "$V/v8-url.json"
call v8-fill add_queue_item "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"queue\":\"$TEST_QUEUE_ID\",\"reference\":\"verify-x\",\"specificContent\":{\"orderUid\":\"verify-x\",\"automationId\":\"<TO-FILL>\"}}"; jq -c '{err:(.protocolError//.text//null)|tostring|.[0:140]}' "$V/v8-fill.json"
call v8-stopprod stop_job '{"env":"prod","orchestrator":"apex","jobId":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}'; jq -c '{err:(.protocolError//.text//null)|tostring|.[0:140]}' "$V/v8-stopprod.json"
```
Expected: all four FAIL — `prod` rejected by the schema (literal `pre_prod`) before any HTTP; the prod `serverURL` rejected by the safety guard; `<TO-FILL>` rejected; `stop_job` prod rejected by the schema.
8.2 The probe itself (note `IsApproved:true` on purpose — it must come back forced `false`):
```bash
export EPOCH=$(date +%s); export T0=$(date -u +%Y-%m-%dT%H:%M:%SZ)
call v8-add add_queue_item "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"queue\":\"$TEST_QUEUE_ID\",\"reference\":\"verify-probe-$EPOCH\",\"specificContent\":{\"orderUid\":\"verify-probe-$EPOCH\",\"automationId\":\"$AUTOMATION_ID\",\"IsApproved\":true}}"
jq -c '{queueName,status,reference,forced,itemId}' "$V/v8-add.json"
export PROBE_ID=$(jq -r '.. | objects | select(has("itemId")) | .itemId' "$V/v8-add.json" | head -1); echo "PROBE_ID=$PROBE_ID"
```
**From here, `delete_queue_item` for `$PROBE_ID` MUST run even if a check fails (V8.5).** Expected: `queueName:"CenCal shared-adapter test queue"`, `status:"New"`, `reference:"verify-probe-<epoch>"`, `forced` mentions `IsApproved`, `itemId` non-empty.
8.3 It sits there, idle:
```bash
call v8-pull pull_queue_item "{\"orchestrator\":\"apex\",\"env\":\"pre_prod\",\"queueId\":\"$TEST_QUEUE_ID\",\"itemId\":\"$PROBE_ID\"}"
jq -c '{isApprovedForced, storedIsApproved:.item.specificContent.IsApproved, status:.item.status}' "$V/v8-pull.json"
sleep 30
call v8-after list_queue_items "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"queue\":\"$TEST_QUEUE_ID\",\"top\":200}"
jq -c --arg id "$PROBE_ID" '[..|objects|select(has("rawStatus") and .itemId==$id)|{status,jobs:(.jobs|length)}]' "$V/v8-after.json"
call v8-jobs1 list_jobs "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"top\":500,\"since\":\"$T0\"}"
jq -c --arg id "$PROBE_ID" '[..|objects|select(has("rawState"))]|{jobsSinceT0:length, forProbe:(map(select(.queueItem.itemId==$id))|length)}' "$V/v8-jobs1.json"
call v8-find find_order_queue_items "{\"env\":\"pre_prod\",\"orderUid\":\"verify-probe-$EPOCH\",\"orchestrator\":\"apex\"}"
jq -c '[..|objects|select(has("rawStatus"))|{status,jobs:(.jobs|length)}]' "$V/v8-find.json"
```
Expected: `isApprovedForced:true`, `storedIsApproved:false` (the guard forced it before the POST); after 30 s the item is still `New` with `jobs:0`; `jobsSinceT0:0` and `forProbe:0` (nothing was dispatched — `is_active:false`); `find_order_queue_items` finds exactly the probe item by `search`+exact-orderUid with `jobs:0`. **STOP-IF** the item left `New` or a job exists for it → delete is no longer allowed (not `New`): do NOT retry anything, report FAIL with the ids.
8.4 Delete and prove it is gone and the guard refuses a second delete:
```bash
call v8-del delete_queue_item "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"itemId\":\"$PROBE_ID\",\"queueId\":\"$TEST_QUEUE_ID\"}"
jq -c '{deleted,previousStatus,reference}' "$V/v8-del.json"
call v8-del2 delete_queue_item "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"itemId\":\"$PROBE_ID\",\"queueId\":\"$TEST_QUEUE_ID\"}"; jq -c '{err:(.protocolError//.text//null)|tostring|.[0:160]}' "$V/v8-del2.json"
call v8-end list_queue_items "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"queue\":\"$TEST_QUEUE_ID\",\"status\":\"New\",\"top\":200}"
jq -c --arg id "$PROBE_ID" '[..|objects|select(has("rawStatus"))]|{newNow:length, probeStillNew:(map(select(.itemId==$id))|length)}' "$V/v8-end.json"
call v8-jobs2 list_jobs '{"env":"pre_prod","orchestrator":"apex","top":500}'; echo "J2=$(jq '[..|objects|select(has("rawState"))]|length' "$V/v8-jobs2.json") (J0=$J0)"
```
8.5 Expected: `deleted:true`, `previousStatus:"New"`; the second delete is REFUSED (status is no longer `New`); `probeStillNew:0`; `newNow` equals the `New` count recorded before the probe; no new jobs (`J2` equals `J0` unless unrelated traffic landed — compare ids not just counts if they differ). Safe to run without the go-ahead: `stop_job` on a **terminal** job refuses before any POST:
```bash
export TERM_ID=$(jq -r '[..|objects|select(has("rawState") and .terminal==true)]|.[0].id' "$V/v3-jobs-all.json")
call v8-stopterm stop_job "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"jobId\":\"$TERM_ID\",\"strategy\":\"SoftStop\"}"; jq -c '{err:(.protocolError//.text//null)|tostring|.[0:160]}' "$V/v8-stopterm.json"
```
Expected: refused with a message naming the terminal state. Re-fetch (`get_job`) the same id and confirm `rawState` is unchanged.
Cross-folder id: a job id from the prod `Authorization` folder must not resolve under the `Authorization Test` folder header, so `stop_job` must fail cleanly (a not-found-style `ApexApiError`, no bug-report block) without touching the job:
```bash
call v8-prodjobs list_jobs '{"env":"prod","orchestrator":"apex","top":50}'
PROD_JOB=$(jq -r '[..|objects|select(has("rawState") and .terminal==true)]|.[0].id' "$V/v8-prodjobs.json")
call v8-stopxf stop_job "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"jobId\":\"$PROD_JOB\",\"strategy\":\"SoftStop\"}"; jq -c '{err:(.protocolError//.text//null)|tostring|.[0:160], reportIssue:(.reportIssue!=null)}' "$V/v8-stopxf.json"
call v8-getxf get_job "{\"env\":\"prod\",\"orchestrator\":\"apex\",\"jobId\":\"$PROD_JOB\"}"; jq -c '[..|objects|select(has("rawState"))|.rawState]' "$V/v8-getxf.json"
```
Expected: `stop_job` refused with a not-found/does-not-resolve message (`reportIssue:false`); the follow-up prod read shows the same `rawState` as before (unchanged).

- [ ] **V9: GO-AHEAD GATE — `start_job` / `stop_job` live (needs the user)**
`start_job` and a `SoftStop`/`Kill` against a live job cause a real (dry-run) portal login. **Do not call either until the user answers this in the conversation, naming the order.** Post exactly:
> Ready for the live start/stop probe on Apex `Authorization Test`. It dispatches ONE real dry-run portal login (a shared credential is used; IsApproved stays false, pre-prod BE). Which pre-prod order/queue item should I run it on, which process (`list_processes` id) and `matchValue`, and may I also `SoftStop` it mid-run (and try `Kill`, which may 403)? I will not start anything until you confirm.

If the user declines or does not answer: mark V9 **NOT RUN (gate)** — never fail or skip silently, and the release may still proceed at the user's discretion. If approved, first read `skill://trace-preprod-be-submit-to-apex-queue` and complete its shared-credential preflight, then:
```bash
call v9-start start_job "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"processId\":\"<WORKFLOW_ID from list_processes, chosen by the user>\",\"matchValue\":\"<MATCH_VALUE chosen by the user>\",\"input\":<INPUT_JSON chosen by the user>,\"jobsCount\":1}"
jq -c '..|objects|select(has("processName"))|{id,state,processName,linkEndsWithId:(.link|endswith("/jobs/"+.id))}' "$V/v9-start.json"
export LIVE_JOB=$(jq -r '..|objects|select(has("processName"))|.id' "$V/v9-start.json"|head -1)
for i in $(seq 1 30); do call v9-poll get_job "{\"env\":\"pre_prod\",\"orchestrator\":\"apex\",\"jobId\":\"$LIVE_JOB\"}" >/dev/null; S=$(jq -r '..|objects|select(has("rawState"))|"\(.rawState) \(.terminal)"' "$V/v9-poll.json"|head -1); echo "$i $S"; case "$S" in *true) break;; esac; sleep 10; done
```
Expected: `state` `Pending` then `Running`, `terminal:false` until a terminal status; the dashboard `link` resolves; then `get_job` with `includeLogDigest:true` yields a digest only if `artifact_policy` allows it (`logsUnavailable` otherwise); the job runs the Process's `current_version` at dispatch. If the user approved `stop_job`: start a second job, `stop_job` with `SoftStop` while non-terminal → `stopped:true`, then `get_job` shows `Stopping`→`Stopped` (`cancelled`); `Kill` → success or a clear permission error (403 mentioning `jobs:force_cancel` is an acceptable outcome — record it). Never start more than the approved count.

- [ ] **V10: UiPath regression vs v1.33.1 (prod + pre_prod, read-only)**
Build the baseline outside the repo (no worktree, no git state change):
```bash
mkdir -p "$V/v1331" && git -C "$REPO" archive v1.33.1 | tar -x -C "$V/v1331"
(cd "$V/v1331" && bun install --frozen-lockfile --ignore-scripts && bun run build) && ls -l "$V/v1331/dist/server.js"
```
`SERVER` selects the build for `call`; v1 labels get a `b-` prefix. Same config, same moment:
```bash
for E in prod pre_prod; do
  SERVER=$V/v1331/dist/server.js call b-jobs-$E list_jobs "{\"env\":\"$E\",\"top\":50}"
  call n-jobs-$E list_jobs "{\"env\":\"$E\",\"orchestrator\":\"uipath\",\"top\":50}"
  jq -n --slurpfile b "$V/b-jobs-$E.json" --slurpfile n "$V/n-jobs-$E.json" '
    ($b[0].jobs|map({k:.key,s:.state,p:.processName,v:.processVersion})) as $B |
    ([$n[0]|..|objects|select(has("rawState"))]|map({k:.id,s:.state,p:.process.name,v:(.process.version//"")})) as $N |
    ($B|map(.k) as $ks | $N|map(select(.k|IN($ks[])))) as $common |
    {env:"'"$E"'", base:($B|length), new:($N|length), common:($common|length),
     stateMismatch:([$common[] as $c | ($B[]|select(.k==$c.k)) as $o | select($o.s!=$c.s or $o.p!=$c.p or $o.v!=$c.v)|$c.k]|length)}'
done
```
Expected per env: `common` ≥ 1 (jobs finishing between the two calls can differ in `state`: re-query those keys with `get_job` before counting them as mismatches) and **`stateMismatch:0`** — UiPath `state` values identical to v1.33.1. Then:
```bash
# faulted job: signature byte-identical, digest identical
KEY=$(jq -r '[..|objects|select(has("rawState") and .state=="Faulted")][0].id' "$V/n-jobs-prod.json"); echo "key=$KEY"
SERVER=$V/v1331/dist/server.js call b-job get_job "{\"env\":\"prod\",\"jobKey\":\"$KEY\",\"includeLogDigest\":true}"
call n-job get_job "{\"env\":\"prod\",\"orchestrator\":\"uipath\",\"jobId\":\"$KEY\",\"includeLogDigest\":true}"
jq -c '{b:(.job.fault.signature)}' "$V/b-job.json"; jq -c '{n:([..|objects|select(has("signature"))|.signature][0])}' "$V/n-job.json"
diff <(jq -S '..|objects|select(has("byLevel"))|del(.transactionOutcome)' "$V/b-job.json") <(jq -S '..|objects|select(has("byLevel"))|del(.transactionOutcome)' "$V/n-job.json") && echo "digest identical"
jq -c '..|objects|select(has("transactionOutcome"))|.transactionOutcome' "$V/b-job.json" | head -1; jq -c '..|objects|select(has("transactionOutcome"))|.transactionOutcome' "$V/n-job.json" | head -1
# discovery reads: same ids/names
for E in prod pre_prod; do
  SERVER=$V/v1331/dist/server.js call b-q-$E list_queues "{\"env\":\"$E\"}"; call n-q-$E list_queues "{\"env\":\"$E\",\"orchestrator\":\"uipath\"}"
  diff <(jq -S '[.queues[]|{id:(.id|tostring),name}]|sort_by(.id)' "$V/b-q-$E.json") <(jq -S '[..|objects|select(has("active") and has("name") and has("processId"))|{id:(.id|tostring),name}]|sort_by(.id)' "$V/n-q-$E.json") && echo "$E queues identical"
  SERVER=$V/v1331/dist/server.js call b-p-$E list_processes "{\"env\":\"$E\"}"; call n-p-$E list_processes "{\"env\":\"$E\",\"orchestrator\":\"uipath\"}"
  diff <(jq -S '[.processes[]|{id:.key,name,v:.processVersion}]|sort_by(.id)' "$V/b-p-$E.json") <(jq -S '[..|objects|select(has("package") and has("details"))|{id,name,v:(.version//"")}]|sort_by(.id)' "$V/n-p-$E.json") && echo "$E processes identical"
done
# queue items by status (prod, portal alias) and order correlation
SERVER=$V/v1331/dist/server.js call b-items list_queue_items '{"env":"prod","queueName":"CENCAL","top":50}'
call n-items list_queue_items '{"env":"prod","orchestrator":"uipath","queue":"CENCAL","top":50}'
jq -S '[.items[]|{id:(.id|tostring),status}]|sort_by(.id)' "$V/b-items.json" > "$V/b-items.norm"
jq -S '[..|objects|select(has("rawStatus"))|{id:.itemId,status}]|sort_by(.id)' "$V/n-items.json" > "$V/n-items.norm"
diff "$V/b-items.norm" "$V/n-items.norm" && echo "items identical"
```
Expected: signatures equal (D7: byte-identical), `digest identical`, same `transactionOutcome` (or both null), queues/processes `identical`, items `identical` modulo traffic between the two calls (inspect any diff; a differing `status` on an item that moved `New→InProgress` is traffic, a differing status vocabulary is a FAIL). For an order on UiPath (pick a recent prod order uid from `find_stuck_orders` or from `n-items`' `reference`/`orderUid`, uid redacted in the report), compare `find_order_queue_items` v1 `{env,orderUid}` against v2 `{env,orderUid,orchestrator:"uipath"}`: same item count, same `status`es, v1 `jobKeys` == v2 `jobIds.uipath`. Also `build_queue_item` (UiPath, pre_prod, any pre-prod order + profile): `diff` of v1 vs v2 `orchestrator:"uipath"` outputs must show only the added `{orchestrator}` envelope key (the payload body deep-equal — the Task 4 golden test already pins this offline).

- [ ] **V11: Cross-backend stuck-order sweep + cleanup**
```bash
call v11-stuck find_stuck_orders '{"env":"pre_prod","profile":"<PROFILE>","scanPages":2,"crossCheck":true}'
jq -c '[..|objects|select(has("orchestrator") and has("verdict"))|{backend:.orchestrator.backend? // .backend,verdict}]|group_by(.verdict)|map({v:.[0].verdict,n:length})' "$V/v11-stuck.json"
```
Expected: the sweep completes with `crossCheck` across both backends; each flagged order has `orchestrator{backend,verdict,…}`; verdicts ⊆ `no-job|job-faulted|job-running|queued-not-picked-up|job-successful-order-stuck|job-never-started`; if V5 found a never-started Apex job whose order is stuck, `job-never-started` appears for it. (If the pre-prod scan finds no stuck orders, record "no sample" — the verdict logic is unit-tested.)
Cleanup — remove all PHI-bearing artifacts this run created and confirm the repo is untouched:
```bash
rm -rf "$HOME/.cache/copilot-mcp/artifacts/$FAILED_ID" "$HOME/.cache/copilot-mcp/artifacts/$NEVER_ID" "$V"
git -C "$REPO" status --porcelain      # expect empty
```

**Final report template** (post to the user; ids truncated to 8 chars, no payload content):
```markdown
## Apex orchestrator live verification — v2.0.0 @ <sha>
Config source: <file|env> · folders: Authorization (<id8>) / Authorization Test (<id8>)

| # | Check | Result | Evidence (ids/counts only) |
|---|-------|--------|----------------------------|
| V0 | build + `node dist/server.js` smoke (33 tools, schemas) | PASS/FAIL | version 2.0.0; tools 33; orchestrator required on 14, optional on 2 |
| V1 | `orchestrators` resource + doctor (Apex login, folders) | | |
| V2 | list_queues/processes/triggers, both folders | | queues N/N; test queue inactive; trigger counts |
| V3 | filters filter: items `status`, jobs `workflow_id`, `since`; label map | | n filtered vs n total |
| V4 | find_order_queue_items apex / both / unknown / folder-misuse | | items N, jobs per item […] |
| V5 | get_job digest: failed / never-started / batch (/ running, artifact-policy) | | reasons observed |
| V6 | get_job_logs filters, raw fields, video ref | | row counts per filter |
| V7 | get_job_artifacts list/download/path discipline | | sizes match; dir 700; trace.zip ok; repo clean |
| V8 | test-queue probe: refusals, add → idle → delete, guard forced IsApproved=false, no job, second delete refused, terminal-job and cross-folder stop refused | | probe `<id8>`, N0→N0 |
| V9 | start_job/stop_job live | PASS / FAIL / **NOT RUN (gate: user declined/absent)** | |
| V10 | UiPath regression vs v1.33.1 (state, signature, digest, queues, processes, items) | | stateMismatch 0/0 |
| V11 | cross-backend find_stuck_orders | | verdict counts |

Not exercised live (unit-tested only): single-backend failure isolation in find_order_queue_items, Apex 429 handling, `force_cancel` permission, `:delete` server-side preconditions.
Deviations / failures: <list each with the step id and the exact observation>
PHI: all outputs stayed under ~/.cache/copilot-mcp/verify (removed); artifacts removed; nothing inside the repo.
Recommendation: <tag v2.0.0 | fix X first>
```

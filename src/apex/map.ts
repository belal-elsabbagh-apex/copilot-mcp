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

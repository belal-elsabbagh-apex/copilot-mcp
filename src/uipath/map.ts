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

// Parsing for RobotLogs.RawMessage: the full JSON payload UiPath's NLog-based robot
// logger emits per log row, of which `Message` (the field the rest of this server
// already reads) is just one key. RawMessage carries everything else the robot
// logged for that row — but only the fields relevant to that specific log event,
// and only when the caller opted into fetching it (JobLogFilter.includeRawFields:
// benchmarked live at ~4-6x the byte size of every already-selected row — a bigger
// cost than a mere doubling — so it's never fetched by default).
//
// Ground-truthed live (2026-09-07, this org's Orchestrator, both envs, across every
// RPAPlaywright/AuthSubmit process) rather than assumed from docs:
//   - transactionId/queueName/processingExceptionType/processingExceptionReason/
//     transactionExecutionTime/queueItemPriority/queueItemReviewStatus appear ONLY
//     on the "Transaction Started"/"Transaction Ended" log rows — an ordinary Info
//     line has none of them.
//   - totalExecutionTimeInSeconds/totalExecutionTime appear ONLY on the final
//     "<process name> execution ended" row.
//   - transactionStatus is present but USELESS: every job observed (Successful and
//     Faulted alike, across every distinct process) logs the *unresolved* .NET
//     binding literally, e.g. `System.Activities.InArgument\`1[UiPath.Core.ProcessingStatus]`,
//     never the actual enum value — a platform/activity-package quirk, not specific
//     to one automation. This module deliberately does NOT surface it; use
//     processingExceptionType instead — it IS populated correctly.
//   - activityInfo (which Studio activity was executing/faulted) only appears when
//     the process has Debugging-level logging enabled — not observed on any job in
//     this org's live traffic, so treat it as opportunistic, not load-bearing.
//   - Custom fields added via Studio's "Add Log Fields" activity show up as ordinary
//     top-level RawMessage keys (e.g. this org's `healingAgentConfig: {...}`),
//     attached only to the specific log row(s) emitted after the activity ran,
//     under whatever name the process author chose — there is no way to know them
//     ahead of time, so they are collected into `custom` verbatim instead of
//     enumerated.
//   - RawMessage is not guaranteed to be valid JSON (UiPath's own NLog target format
//     embeds it inside "${timestamp} ${loglevel} ${message}" in some configurations,
//     per community reports) — parse defensively, never assume success.

import { isRecord, safeJsonParse } from "../shared/util.js";
import type { JobLog } from "./uipath.js";

// Every key this module surfaces as a typed field below; anything else lands in
// `custom` unchanged. `transactionStatus` is intentionally excluded — see module doc.
const KNOWN_KEYS: Record<string, true> = {
  message: true,
  level: true,
  logType: true,
  timeStamp: true,
  fingerprint: true,
  windowsIdentity: true,
  machineName: true,
  fileName: true,
  jobId: true,
  robotName: true,
  machineId: true,
  userKey: true,
  processName: true,
  processVersion: true,
  organizationUnitId: true,
  initiatedBy: true,
  businessOperationId: true,
  transactionId: true,
  transactionState: true,
  transactionStatus: true,
  queueName: true,
  processingExceptionType: true,
  processingExceptionReason: true,
  transactionExecutionTime: true,
  queueItemPriority: true,
  queueItemReviewStatus: true,
  totalExecutionTimeInSeconds: true,
  totalExecutionTime: true,
  activityInfo: true,
};

export interface RawMessageFields {
  transactionId: string | null;
  transactionState: string | null; // "Started" | "Ended" — a phase marker, NOT an outcome
  queueName: string | null;
  processingExceptionType: string | null; // e.g. "BusinessException" — the queue's own retry classification
  processingExceptionReason: string | null;
  transactionExecutionTimeSec: number | null;
  totalExecutionTimeInSeconds: number | null;
  queueItemPriority: string | null;
  // As logged at the time this row was written — a snapshot, not live queue-item
  // state. Re-check via find_order_queue_items/get_queue_item for the current value.
  queueItemReviewStatus: string | null;
  businessOperationId: string | null;
  // Debugging-log only; rarely present — see module doc. Shape (DisplayName/State/
  // Activity/Arguments per UiPath's docs) is intentionally left unvalidated since it
  // has never been observed live here.
  activityInfo: unknown;
  // Any RawMessage key outside the known schema above — Add Log Fields activity
  // output, verbatim, under whatever name the process author chose.
  custom: Record<string, unknown>;
}

// Parses one log row's RawMessage. Returns null when the field is absent, not an
// object, or not valid JSON — all three are normal (most rows carry none of this,
// and RawMessage isn't guaranteed parseable), never an error condition.
export function parseRawMessage(log: JobLog): RawMessageFields | null {
  if (!log.RawMessage) return null;
  const parsed = safeJsonParse(log.RawMessage);
  if (!isRecord(parsed)) return null;

  const custom: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(parsed)) {
    if (!Object.hasOwn(KNOWN_KEYS, key)) custom[key] = value;
  }

  const str = (v: unknown): string | null => (typeof v === "string" && v.length > 0 ? v : null);
  const num = (v: unknown): number | null => (typeof v === "number" ? v : null);

  return {
    transactionId: str(parsed["transactionId"]),
    transactionState: str(parsed["transactionState"]),
    queueName: str(parsed["queueName"]),
    processingExceptionType: str(parsed["processingExceptionType"]),
    processingExceptionReason: str(parsed["processingExceptionReason"]),
    transactionExecutionTimeSec: num(parsed["transactionExecutionTime"]),
    totalExecutionTimeInSeconds: num(parsed["totalExecutionTimeInSeconds"]),
    queueItemPriority: str(parsed["queueItemPriority"]),
    queueItemReviewStatus: str(parsed["queueItemReviewStatus"]),
    businessOperationId: str(parsed["businessOperationId"]),
    activityInfo: parsed["activityInfo"] ?? null,
    custom,
  };
}

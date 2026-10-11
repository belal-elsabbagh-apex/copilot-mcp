// build_faulted_job_issue: turn a faulted UiPath job into a ready-to-post GitHub
// issue payload — WITHOUT touching GitHub. This server holds no GitHub credentials;
// the `report-faulted-uipath-jobs` prompt hands the payload to the host's GitHub MCP
// server, which creates the issue (or comments on a matching one). Keeping the
// formatting here makes it consistent and lets the pure formatter be unit-tested.

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

export interface FormatOptions {
  env: Env;
  jobKey: string;
  repo?: string | undefined;
  labels?: string[] | undefined;
  deepLink?: string | undefined;
}

// The headline error: the first Error-level log, else the last non-empty log line,
// else a synthetic line from the job State.
export function topError(job: UiPathJob, logs: JobLog[]): string {
  const firstErr = logs.find((l) => l.Level?.toLowerCase() === "error" && l.Message?.trim());
  if (firstErr) return firstErr.Message.trim();
  const lastLine = [...logs].reverse().find((l) => l.Message?.trim());
  if (lastLine) return lastLine.Message.trim();
  return `Job ended in state ${job.State ?? "Faulted"}`;
}

// A dotted .NET-style type ending in Exception/Error, e.g. System.NullReferenceException.
export const EXCEPTION_TYPE = /\b(?:[A-Za-z_]\w*\.)*[A-Z]\w*(?:Exception|Error)\b/;

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

const fmtLog = (l: JobLog): string =>
  `${l.TimeStamp ?? ""} [${l.Level ?? ""}] ${l.Message ?? ""}`.trim();

/**
 * Pure: build the GitHub issue payload for a faulted job from its detail + logs.
 * Does not call GitHub or UiPath — fully determined by its inputs.
 */
export function formatFaultedJobIssue(
  job: UiPathJob,
  logs: UiPathLogRow[],
  opts: FormatOptions,
): FaultedJobIssue {
  const repo = opts.repo ?? DEFAULT_REPO;
  const labels = opts.labels && opts.labels.length > 0 ? opts.labels : DEFAULT_LABELS;
  const deepLink = opts.deepLink ?? "";
  const error = topError(job, logs);
  const signature = normalizeError(error);
  const title = `[UiPath Fault] ${firstLine(error)} (${opts.env})`;

  const errorLogs = logs.filter((l) => l.Level?.toLowerCase() === "error");
  const logTail = (errorLogs.length > 0 ? errorLogs : logs.slice(-10))
    .map(fmtLog)
    .join("\n")
    .slice(0, 4000);

  // Only populated when the caller fetched RawMessage (buildFaultedJobIssue always
  // does); a formatFaultedJobIssue caller that didn't gets no queue-exception line,
  // never a crash — extractTransactionOutcome degrades to null on plain JobLog[].
  const queueException = extractTransactionOutcome(logs);

  const body = [
    `**UiPath job faulted** in \`${opts.env}\`.`,
    "",
    `- **State:** ${job.State ?? "Faulted"}`,
    `- **Job Id:** ${job.Id ?? "(unknown)"}`,
    `- **Job Key:** \`${opts.jobKey}\``,
    `- **Created:** ${job.CreationTime ?? "(unknown)"}`,
    ...(job.EndTime ? [`- **Ended:** ${job.EndTime}`] : []),
    ...(deepLink ? [`- **Orchestrator:** ${deepLink}`] : []),
    ...(queueException?.processingExceptionType
      ? [
          `- **Queue exception:** ${queueException.processingExceptionType}` +
            (queueException.processingExceptionReason
              ? ` — ${queueException.processingExceptionReason}`
              : ""),
        ]
      : []),
    "",
    "**Error**",
    "```",
    error,
    "```",
    "",
    "<details><summary>Log tail</summary>",
    "",
    "```",
    logTail || "(no logs)",
    "```",
    "",
    "</details>",
    "",
    "_Filed by copilot-mcp `build_faulted_job_issue`. Recurrences of this fault are added as comments below._",
    signatureMarker(signature),
  ].join("\n");

  const searchQuery = faultSearchQuery(repo, signature);

  const recurrenceComment = [
    `Recurred in \`${opts.env}\` — same fault signature.`,
    "",
    `- **Job Key:** \`${opts.jobKey}\``,
    `- **Created:** ${job.CreationTime ?? "(unknown)"}`,
    ...(deepLink ? [`- **Orchestrator:** ${deepLink}`] : []),
    "",
    "```",
    firstLine(error),
    "```",
  ].join("\n");

  return { repo, title, body, labels, faultSignature: signature, searchQuery, recurrenceComment };
}

export interface BuildOptions {
  folder?: string | undefined;
  repo?: string | undefined;
  labels?: string[] | undefined;
}

/**
 * Fetch a faulted job (by Key) + its robot logs from UiPath and format the issue
 * payload. `found` reports whether the job Key actually resolved (the payload is
 * still produced from a synthetic stub if not, so the caller can decide).
 */
export async function buildFaultedJobIssue(
  env: Env,
  jobKey: string,
  opts: BuildOptions = {},
): Promise<FaultedJobIssue & { found: boolean; logsError?: string }> {
  const folder = resolveFolder(env, opts.folder);
  const job = await fetchJobByKey(jobKey, folder);
  // Logs are a best-effort attachment — a transient logs-fetch failure must not
  // discard an otherwise-valid job lookup, same convention as analyze.ts.
  let logs: UiPathLogRow[] = [];
  let logsError: string | undefined;
  try {
    logs = await fetchJobLogs(jobKey, folder, { includeRawFields: true });
  } catch (e) {
    logsError = e instanceof Error ? e.message : String(e);
  }
  const resolved: UiPathJob = job ?? { Key: jobKey, State: "Faulted" };
  const issue = formatFaultedJobIssue(resolved, logs, {
    env,
    jobKey,
    repo: opts.repo,
    labels: opts.labels,
    deepLink: jobDeepLink(jobKey),
  });
  return { ...issue, found: job !== null, ...(logsError ? { logsError } : {}) };
}

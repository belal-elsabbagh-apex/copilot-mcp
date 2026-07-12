// Condensed, failure-focused view of a job's robot logs: consecutive-duplicate
// collapsing (retry loops), stall detection (largest timestamp gaps), and a
// structured fault extracted from the headline error. Pure — analyze_order_execution
// attaches the digest instead of the raw (up to 500-line) log dump; the complete
// logs stay one get_job_logs call away.

import { isFailureLog } from "../copilot/output-analysis.js";
import { normalizeError, topError } from "./faults.js";
import type { JobLog, UiPathJob } from "./uipath.js";

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

export interface JobFault {
  message: string; // headline error (first Error-level log, else last line, else state)
  signature: string; // normalizeError — same dedupe key as build_faulted_job_issue
  exceptionType: string | null; // e.g. System.NullReferenceException, parsed from the message
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
// Logs must be oldest-first (the order fetchJobLogs returns them).
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

// A dotted .NET-style type ending in Exception/Error, e.g. System.NullReferenceException.
const EXCEPTION_TYPE = /\b(?:[A-Za-z_]\w*\.)*[A-Z]\w*(?:Exception|Error)\b/;

export function extractFault(job: UiPathJob, logs: JobLog[]): JobFault {
  const message = topError(job, logs);
  return {
    message: truncate(message),
    signature: normalizeError(message),
    exceptionType: message.match(EXCEPTION_TYPE)?.[0] ?? null,
  };
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

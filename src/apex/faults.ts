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

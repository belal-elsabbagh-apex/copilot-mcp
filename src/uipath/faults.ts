// build_faulted_job_issue: turn a faulted UiPath job into a ready-to-post GitHub
// issue payload — WITHOUT touching GitHub. This server holds no GitHub credentials;
// the `report-faulted-uipath-jobs` prompt hands the payload to the host's GitHub MCP
// server, which creates the issue (or comments on a matching one). Keeping the
// formatting here makes it consistent and lets the pure formatter be unit-tested.

import type { Env } from "../config/config.js";
import {
  fetchJobByKey,
  fetchJobLogs,
  type JobLog,
  jobDeepLink,
  resolveFolder,
  type UiPathJob,
} from "./uipath.js";

// Default target repo (the RPA automation project). Override per call via opts.repo.
export const DEFAULT_REPO = "Apex-Medical-AI-Inc/RPAPlaywright";
export const DEFAULT_LABELS = ["uipath-fault", "bug"];

export interface FaultedJobIssue {
  repo: string;
  title: string;
  body: string;
  labels: string[];
  faultSignature: string; // stable, normalized error — the dedupe key
  searchQuery: string; // GitHub query to find an existing issue for this fault
  recurrenceComment: string; // post this on the existing issue when one is found
}

export interface FormatOptions {
  env: Env;
  jobKey: string;
  repo?: string | undefined;
  labels?: string[] | undefined;
  deepLink?: string | undefined;
}

const firstLine = (s: string): string => (s.split("\n")[0] ?? s).trim().slice(0, 120);

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

// The headline error: the first Error-level log, else the last non-empty log line,
// else a synthetic line from the job State.
export function topError(job: UiPathJob, logs: JobLog[]): string {
  const firstErr = logs.find((l) => l.Level?.toLowerCase() === "error" && l.Message?.trim());
  if (firstErr) return firstErr.Message.trim();
  const lastLine = [...logs].reverse().find((l) => l.Message?.trim());
  if (lastLine) return lastLine.Message.trim();
  return `Job ended in state ${job.State ?? "Faulted"}`;
}

const fmtLog = (l: JobLog): string =>
  `${l.TimeStamp ?? ""} [${l.Level ?? ""}] ${l.Message ?? ""}`.trim();

/**
 * Pure: build the GitHub issue payload for a faulted job from its detail + logs.
 * Does not call GitHub or UiPath — fully determined by its inputs.
 */
export function formatFaultedJobIssue(
  job: UiPathJob,
  logs: JobLog[],
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

  const body = [
    `**UiPath job faulted** in \`${opts.env}\`.`,
    "",
    `- **State:** ${job.State ?? "Faulted"}`,
    `- **Job Id:** ${job.Id ?? "(unknown)"}`,
    `- **Job Key:** \`${opts.jobKey}\``,
    `- **Created:** ${job.CreationTime ?? "(unknown)"}`,
    ...(job.EndTime ? [`- **Ended:** ${job.EndTime}`] : []),
    ...(deepLink ? [`- **Orchestrator:** ${deepLink}`] : []),
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
    `<!-- fault-signature: ${signature} -->`,
  ].join("\n");

  const searchQuery = `repo:${repo} is:issue is:open in:body "fault-signature: ${signature}"`;

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
  let logs: JobLog[] = [];
  let logsError: string | undefined;
  try {
    logs = await fetchJobLogs(jobKey, folder);
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

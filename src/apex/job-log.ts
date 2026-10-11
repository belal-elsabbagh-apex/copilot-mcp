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

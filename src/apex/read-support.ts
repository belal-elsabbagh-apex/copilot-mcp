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

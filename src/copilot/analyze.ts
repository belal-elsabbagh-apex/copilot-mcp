// Orchestration: trace a Copilot orderUid to its UiPath Orchestrator job(s) and
// diagnose the run. Correlation is queue-item based: every order flows through a
// queue item that carries the orderUid + the ExecutorJobKey of the job that ran it,
// so this reaches faulted/still-running consumer jobs an OutputArguments scan can't.
// Ported from copilot-doctor src/jobMatcher.ts.

import { isRecord, msBetween, type StepProgress } from "../shared/util.js";
import {
  digestLogs,
  extractFault,
  type JobFault,
  type JobLogDigest,
} from "../uipath/log-digest.js";
import {
  type Env,
  type FolderScope,
  fetchJobLogs,
  fetchJobsForKeys,
  fetchJobVideoUrl,
  type JobLog,
  jobDeepLink,
  type QueueItemMatch,
  type QueueSearchMode,
  resolveFolder,
  resolveOrgUnitId,
  searchQueueItemsByOrderId,
  type UiPathJob,
} from "../uipath/uipath.js";
import { analyzeOutput, type OutputComment } from "./output-analysis.js";
import { normalizeOutput, type OutputSchemaId } from "./output-schema.js";

export type { Env };

export interface JobAnalysis {
  id: string | undefined;
  key: string | undefined;
  state: string | undefined;
  processName: string | undefined;
  verdict: string;
  creationTime: string | undefined;
  endTime: string | undefined;
  durationMs: number | null; // creationTime -> endTime
  gapSincePreviousJobMs: number | null; // previous (older) run's end -> this run's start
  orchestratorUrl: string;
  schema: OutputSchemaId;
  result: string | null;
  output: Record<string, unknown>;
  analysis: OutputComment[];
  fault: JobFault | null; // structured headline error for non-SUCCESS jobs
  videoUrl?: string | null;
  videoError?: string; // includeVideo was requested but the fetch itself failed — best-effort
  logDigest?: JobLogDigest; // condensed logs — the raw dump stays in get_job_logs
  logsError?: string; // includeLogs was requested but the fetch itself failed — best-effort
}

export interface AnalyzeResult {
  orderUid: string;
  env: Env;
  folder: string | undefined;
  matched: boolean;
  jobCount: number;
  queueItemsScanned: number; // raw queue items examined before client-side confirm
  summary: { latestState: string | null; verdict: string; reasons: string[] };
  jobs: JobAnalysis[];
  searchMode: QueueSearchMode;
  queueItemSignals?: QueueItemMatch[]; // PHI-safe status/exception hints per correlated item
  notes?: string[];
  searchError?: string; // the queue-item search failed entirely
}

export interface AnalyzeOptions {
  env?: Env | undefined;
  folder?: string | undefined;
  since?: string | undefined; // bounds the recent-scan fallback when the OData filter is rejected
  top?: number | undefined;
  includeLogs?: boolean | undefined;
  includeVideo?: boolean | undefined; // default false — the video fetch is an extra round-trip
  onProgress?: StepProgress | undefined;
}

// out_Result (flat schema) gives an explicit verdict; otherwise infer from job
// State. UiPath States: Successful / Faulted / Stopped / etc.
function jobVerdict(state: string | undefined, output: Record<string, unknown>): string {
  const r = output["out_Result"];
  if (typeof r === "string") return r.toUpperCase();
  const s = (state ?? "").toLowerCase();
  if (s === "successful") return "SUCCESS";
  if (s === "faulted" || s === "stopped") return "FAILURE";
  return state ?? "UNKNOWN";
}

const parseOutput = (oa: string | undefined): Record<string, unknown> => {
  try {
    const v: unknown = JSON.parse(oa ?? "{}");
    return isRecord(v) ? v : {};
  } catch {
    return {};
  }
};

// The order → job correlation, folded into analyzeOrderExecution.
interface Correlation {
  jobs: UiPathJob[]; // executor jobs resolved from the queue item(s), deduped by Key
  signals: QueueItemMatch[]; // PHI-safe queue-item hints (status/exception/retry)
  scanned: number;
  searchMode: QueueSearchMode;
  notes: string[];
  searchError: string | undefined; // set when the recent-scan fallback ran
}

// Resolve the order's queue item(s) → their ExecutorJobKey → the full job(s). A New,
// not-yet-picked-up item has no ExecutorJobKey and so yields no job (correctly — no
// run has happened yet), but it still surfaces as a signal. `logFolder` is the
// folder-path scope the job lookups use; `scope` is the org-unit scope the QueueItems
// endpoint requires. Propagates a total search failure to the caller (→ SEARCH_FAILED).
async function correlate(
  orderUid: string,
  scope: FolderScope,
  logFolder: string | undefined,
  top: number,
  since: string | undefined,
): Promise<Correlation> {
  const q = await searchQueueItemsByOrderId(orderUid, scope, top, since);
  const jobKeys = [...new Set(q.matches.map((m) => m.executorJobKey).filter((k) => k))];
  const notes = [...q.notes];
  let jobs: UiPathJob[] = [];
  if (jobKeys.length) {
    const looked = await fetchJobsForKeys(jobKeys, logFolder);
    jobs = Object.values(looked)
      .map((l) => l.job)
      .filter((j): j is UiPathJob => j !== null);
    const failed = Object.values(looked).filter((l) => l.error).length;
    if (failed) notes.push(`${failed} executor job(s) failed to load from their queue-item key.`);
  }
  return {
    jobs,
    signals: q.matches,
    scanned: q.scanned,
    searchMode: q.searchMode,
    notes,
    searchError: q.searchError,
  };
}

// Hydrate one job into its diagnosis (state, verdict, normalized output, analysis
// comments, structured fault, and optionally a condensed log digest + video).
async function toJobAnalysis(
  job: UiPathJob,
  scope: string | undefined,
  includeLogs: boolean,
  includeVideo: boolean,
): Promise<JobAnalysis> {
  const output = parseOutput(job.OutputArguments);
  const norm = normalizeOutput(output);

  // Both are per-job, best-effort attachments inside a potentially large multi-job
  // report — a real fetch failure here must not fail the whole analysis, but it
  // must not vanish either, so it's attached to this job as logsError/videoError.
  let logs: JobLog[] = [];
  let logsError: string | undefined;
  if (includeLogs) {
    try {
      logs = await fetchJobLogs(job.Key ?? "", scope);
    } catch (e) {
      logsError = e instanceof Error ? e.message : String(e);
    }
  }
  let videoUrl = "";
  let videoError: string | undefined;
  if (includeVideo) {
    try {
      videoUrl = await fetchJobVideoUrl(job.Key ?? "", scope);
    } catch (e) {
      videoError = e instanceof Error ? e.message : String(e);
    }
  }

  const resultRaw = output["out_Result"];
  const verdict = jobVerdict(job.State, output);
  return {
    id: job.Id,
    key: job.Key,
    state: job.State,
    processName: job.ReleaseName,
    verdict,
    creationTime: job.CreationTime,
    endTime: job.EndTime,
    durationMs: msBetween(job.CreationTime, job.EndTime),
    gapSincePreviousJobMs: null, // filled once all jobs are sorted
    orchestratorUrl: jobDeepLink(job.Key ?? ""),
    schema: norm.schema,
    result: typeof resultRaw === "string" ? resultRaw : null,
    output: Object.fromEntries(norm.fields), // token/callbackContext already stripped
    analysis: analyzeOutput(output, logs),
    fault: verdict === "SUCCESS" ? null : extractFault(job, logs),
    ...(includeVideo ? { videoUrl: videoUrl || null } : {}),
    ...(videoError ? { videoError } : {}),
    ...(includeLogs ? { logDigest: digestLogs(logs) } : {}),
    ...(logsError ? { logsError } : {}),
  };
}

// The summary verdict when no job resolved: distinguish "queued, not yet picked up"
// (a queue item exists but has no ExecutorJobKey) from a plain no-match.
function noJobSummary(signals: QueueItemMatch[]): AnalyzeResult["summary"] {
  if (!signals.length) return { latestState: null, verdict: "NO_MATCH", reasons: [] };
  const verdict = signals.every((s) => !s.executorJobKey)
    ? "QUEUED_NOT_PICKED_UP"
    : "NO_JOB_RESOLVED";
  const reasons = signals.map((s) => `queue item ${s.reference || s.id}: status=${s.status}`);
  return { latestState: null, verdict, reasons };
}

// Analyze how `orderUid` executed on the Orchestrator. Read-only.
export async function analyzeOrderExecution(
  orderUid: string,
  {
    env = "prod",
    folder,
    since,
    top = 50,
    includeLogs = true,
    includeVideo = false,
    onProgress,
  }: AnalyzeOptions = {},
): Promise<AnalyzeResult> {
  const scope = resolveFolder(env, folder);
  const qScope: FolderScope = { orgUnitId: resolveOrgUnitId(env), folderPath: scope ?? "" };

  let corr: Correlation;
  try {
    corr = await correlate(orderUid, qScope, scope, top, since);
  } catch (e) {
    const searchError = e instanceof Error ? e.message : String(e);
    return {
      orderUid,
      env,
      folder: scope,
      matched: false,
      jobCount: 0,
      queueItemsScanned: 0,
      summary: { latestState: null, verdict: "SEARCH_FAILED", reasons: [searchError] },
      jobs: [],
      searchMode: "contains",
      searchError,
    };
  }

  const jobs: JobAnalysis[] = [];
  // One job's analysis is up to three sequential Orchestrator calls (details, logs,
  // video) — with includeLogs/includeVideo on, a multi-retry order is a long wait.
  for (const [i, job] of corr.jobs.entries()) {
    jobs.push(await toJobAnalysis(job, scope, includeLogs, includeVideo));
    onProgress?.(i + 1, corr.jobs.length, `analyzed job ${job.Key ?? "(no key)"}`);
  }

  // Newest first (search ordered desc, but key lookups can reorder).
  jobs.sort((a, b) => (b.creationTime ?? "").localeCompare(a.creationTime ?? ""));
  // Retry cadence: how long after the previous (older) run's end each run started.
  for (let i = 0; i < jobs.length; i++) {
    const cur = jobs[i];
    const prev = jobs[i + 1]; // older neighbour
    if (!(cur && prev)) continue;
    cur.gapSincePreviousJobMs = msBetween(prev.endTime ?? prev.creationTime, cur.creationTime);
  }
  const latest = jobs[0];

  return {
    orderUid,
    env,
    folder: scope,
    matched: jobs.length > 0,
    jobCount: jobs.length,
    queueItemsScanned: corr.scanned,
    summary: latest
      ? {
          latestState: latest.state ?? null,
          verdict: latest.verdict,
          reasons: latest.analysis.map((a) => a.message),
        }
      : noJobSummary(corr.signals),
    jobs,
    searchMode: corr.searchMode,
    ...(corr.signals.length ? { queueItemSignals: corr.signals } : {}),
    ...(corr.notes.length ? { notes: corr.notes } : {}),
    ...(corr.searchError ? { searchError: corr.searchError } : {}),
  };
}

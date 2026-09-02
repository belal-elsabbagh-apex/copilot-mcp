// Orchestration: trace a Copilot orderUid to its UiPath Orchestrator job(s) and
// diagnose the run. Correlation is queue-item based: every order flows through a
// queue item that carries the orderUid + the ExecutorJobKey of the job that ran it,
// so this reaches faulted/still-running consumer jobs an OutputArguments scan can't.
// Ported from copilot-doctor src/jobMatcher.ts.

import { chunk, isRecord, msBetween, type StepProgress } from "../shared/util.js";
import {
  digestLogs,
  extractFault,
  type JobFault,
  type JobLogDigest,
} from "../uipath/log-digest.js";
import {
  type Env,
  type FolderScope,
  fetchJobByKey,
  fetchJobLogs,
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
  since?: string | undefined; // bounds the primary queue-item search (retried unbounded when nothing matches) and the recent-scan fallback
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
  signals: QueueItemMatch[]; // PHI-safe queue-item hints
  jobKeys: string[]; // deduped ExecutorJobKeys, in queue-item order
  scanned: number;
  searchMode: QueueSearchMode;
  notes: string[];
  searchError: string | undefined; // set when the recent-scan fallback ran
}

// Resolve the order's queue item(s) → their ExecutorJobKey(s) — search only, no job
// lookups (those are keyed by ExecutorJobKey and happen per-key in the hydration wave
// below, since detail/logs/video all need only the Key this search already produced).
// A New, not-yet-picked-up item has no ExecutorJobKey and so yields no key (correctly —
// no run has happened yet), but it still surfaces as a signal. `since` narrows the
// primary search; a single-order diagnostic favors recall, so a bounded search that
// matches nothing is retried once, unbounded (never done in the bulk sweep path).
// Propagates a total search failure to the caller (→ SEARCH_FAILED).
async function correlate(
  orderUid: string,
  scope: FolderScope,
  top: number,
  since: string | undefined,
): Promise<Correlation> {
  let q = await searchQueueItemsByOrderId(orderUid, scope, top, since);
  const notes = [...q.notes];
  if (since && q.matches.length === 0) {
    q = await searchQueueItemsByOrderId(orderUid, scope, top, undefined);
    notes.push(...q.notes);
    notes.push(`no queue item matched within since=${since}; retried without the time bound.`);
  }
  const jobKeys = [...new Set(q.matches.map((m) => m.executorJobKey).filter((k) => k))];
  return {
    signals: q.matches,
    jobKeys,
    scanned: q.scanned,
    searchMode: q.searchMode,
    notes,
    searchError: q.searchError,
  };
}

// One job key's hydration outcome: detail, robot logs, and video, each independently
// best-effort (a real failure lands on its own *Error field, never lost, never fails
// the others or the whole call).
interface JobFetch {
  job: UiPathJob | null;
  jobError: string | undefined;
  logs: JobLog[];
  logsError: string | undefined;
  videoUrl: string;
  videoError: string | undefined;
}

// Detail, robot logs and video are all keyed by the Key the queue item already gave us, so
// they go out together: the logs leg (up to 500 rows, the call's biggest payload) no longer
// waits for the detail response. allSettled — each leg's failure stays on its own field.
async function fetchJobBundle(
  key: string,
  scope: string | undefined,
  includeLogs: boolean,
  includeVideo: boolean,
): Promise<JobFetch> {
  const errMsg = (e: unknown): string => (e instanceof Error ? e.message : String(e));
  const [jobResult, logsResult, videoResult] = await Promise.allSettled([
    fetchJobByKey(key, scope),
    includeLogs ? fetchJobLogs(key, scope) : Promise.resolve<JobLog[]>([]),
    includeVideo ? fetchJobVideoUrl(key, scope) : Promise.resolve(""),
  ]);
  return {
    job: jobResult.status === "fulfilled" ? jobResult.value : null,
    jobError: jobResult.status === "rejected" ? errMsg(jobResult.reason) : undefined,
    logs: logsResult.status === "fulfilled" ? logsResult.value : [],
    logsError: logsResult.status === "rejected" ? errMsg(logsResult.reason) : undefined,
    videoUrl: videoResult.status === "fulfilled" ? videoResult.value : "",
    videoError: videoResult.status === "rejected" ? errMsg(videoResult.reason) : undefined,
  };
}

// Hydrate one job into its diagnosis (state, verdict, normalized output, analysis
// comments, structured fault, and optionally a condensed log digest + video). Pure —
// `fetched` already carries whatever toJobAnalysis needs, so this does no I/O.
function toJobAnalysis(
  job: UiPathJob,
  fetched: JobFetch,
  includeLogs: boolean,
  includeVideo: boolean,
): JobAnalysis {
  const output = parseOutput(job.OutputArguments);
  const norm = normalizeOutput(output);
  const { logs, logsError, videoUrl, videoError } = fetched;

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
    corr = await correlate(orderUid, qScope, top, since);
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
  // Detail, robot logs and video are fetched together per key (fetchJobBundle), so a
  // multi-retry order's jobs no longer pay for each job's up-to-three Orchestrator round
  // trips serially. Chunks of 5, not 10: each key now costs up to 3 concurrent calls
  // (≤15 in flight, the same order as the previous per-job concurrency of 10 × 1 call).
  let analyzed = 0;
  let failedJobLookups = 0;
  for (const batch of chunk(corr.jobKeys, 5)) {
    await Promise.all(
      batch.map(async (key) => {
        const bundle = await fetchJobBundle(key, scope, includeLogs, includeVideo);
        if (bundle.jobError !== undefined) failedJobLookups++;
        if (bundle.job) jobs.push(toJobAnalysis(bundle.job, bundle, includeLogs, includeVideo));
        onProgress?.(++analyzed, corr.jobKeys.length, `analyzed job ${key}`);
      }),
    );
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
  const notes = [...corr.notes];
  if (failedJobLookups) {
    notes.push(`${failedJobLookups} executor job(s) failed to load from their queue-item key.`);
  }

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
    ...(notes.length ? { notes } : {}),
    ...(corr.searchError ? { searchError: corr.searchError } : {}),
  };
}

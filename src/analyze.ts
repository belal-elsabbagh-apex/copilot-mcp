// Orchestration: trace a Copilot orderUid to its UiPath Orchestrator job(s) and
// diagnose the run. Ported from copilot-doctor src/jobMatcher.ts, extended with a
// recent-scan fallback for when Orchestrator rejects the OutputArguments filter.

import { analyzeOutput, type OutputComment } from "./output-analysis.js";
import { normalizeOutput, type OutputSchemaId } from "./output-schema.js";
import {
  confirmJobsForOrder,
  type Env,
  fetchJobLogs,
  fetchJobVideoUrl,
  type JobLog,
  jobDeepLink,
  listRecentJobs,
  resolveFolder,
  searchJobsByOrderId,
  type UiPathJob,
} from "./uipath.js";
import { isRecord } from "./util.js";

export type { Env };

export interface JobAnalysis {
  id: string | undefined;
  key: string | undefined;
  state: string | undefined;
  verdict: string;
  creationTime: string | undefined;
  endTime: string | undefined;
  orchestratorUrl: string;
  schema: OutputSchemaId;
  result: string | null;
  output: Record<string, unknown>;
  analysis: OutputComment[];
  videoUrl?: string | null;
  logs?: JobLog[];
}

export interface AnalyzeResult {
  orderUid: string;
  env: Env;
  folder: string | undefined;
  matched: boolean;
  jobCount: number;
  candidatesScanned: number;
  summary: { latestState: string | null; verdict: string; reasons: string[] };
  jobs: JobAnalysis[];
  searchMode: "contains" | "recent-scan";
  notes?: string[];
  searchError?: string;
}

export interface AnalyzeOptions {
  env?: Env | undefined;
  folder?: string | undefined;
  since?: string | undefined;
  top?: number | undefined;
  includeLogs?: boolean | undefined;
  includeVideo?: boolean | undefined; // default false — the video fetch is an extra round-trip
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

type SearchMode = "contains" | "recent-scan";
interface Acquired {
  candidates: UiPathJob[];
  searchMode: SearchMode;
  notes: string[];
  searchError: string | undefined;
  fatal: string | undefined; // set when BOTH the contains scan and the fallback fail
}

// Find candidate jobs for an order. Tier 1: server-side `contains(OutputArguments,
// uid)` — broad (matches any job regardless of recency) and cheap when UiPath
// accepts it. But Orchestrator intermittently rejects OutputArguments filtering
// (400 "Invalid OData query options" / 500). Tier 2 on failure: a bounded recent-
// jobs scan (CreationTime filter is reliable) confirmed client-side.
async function acquireCandidates(
  orderUid: string,
  since: string | undefined,
  top: number,
  scope: string | undefined,
): Promise<Acquired> {
  try {
    const candidates = await searchJobsByOrderId(orderUid, since, top, scope);
    return {
      candidates,
      searchMode: "contains",
      notes: [],
      searchError: undefined,
      fatal: undefined,
    };
  } catch (e) {
    const searchError = e instanceof Error ? e.message : String(e);
    try {
      const candidates = await listRecentJobs(since, top, scope);
      const tail = since
        ? ` since ${since}.`
        : ". Pass `since` (the order's run date) and/or raise `top` if not found.";
      const note = `OutputArguments filter rejected by Orchestrator; fell back to scanning the ${candidates.length} most-recent job(s)${tail}`;
      return {
        candidates,
        searchMode: "recent-scan",
        notes: [note],
        searchError,
        fatal: undefined,
      };
    } catch (e2) {
      const message = e2 instanceof Error ? e2.message : String(e2);
      return {
        candidates: [],
        searchMode: "recent-scan",
        notes: [],
        searchError,
        fatal: `${searchError} | fallback: ${message}`,
      };
    }
  }
}

// Hydrate one confirmed job into its diagnosis (state, verdict, normalized output,
// analysis comments, and optionally logs + video).
async function toJobAnalysis(
  job: UiPathJob,
  scope: string | undefined,
  includeLogs: boolean,
  includeVideo: boolean,
): Promise<JobAnalysis> {
  const output = parseOutput(job.OutputArguments);
  const norm = normalizeOutput(output);
  const logs = includeLogs ? await fetchJobLogs(job.Key ?? "", scope) : [];
  const videoUrl = includeVideo ? await fetchJobVideoUrl(job.Key ?? "", scope) : "";
  const resultRaw = output["out_Result"];
  return {
    id: job.Id,
    key: job.Key,
    state: job.State,
    verdict: jobVerdict(job.State, output),
    creationTime: job.CreationTime,
    endTime: job.EndTime,
    orchestratorUrl: jobDeepLink(job.Key ?? ""),
    schema: norm.schema,
    result: typeof resultRaw === "string" ? resultRaw : null,
    output: Object.fromEntries(norm.fields), // token/callbackContext already stripped
    analysis: analyzeOutput(output, logs),
    ...(includeVideo ? { videoUrl: videoUrl || null } : {}),
    ...(includeLogs ? { logs } : {}),
  };
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
  }: AnalyzeOptions = {},
): Promise<AnalyzeResult> {
  const scope = resolveFolder(env, folder);
  const acq = await acquireCandidates(orderUid, since, top, scope);
  if (acq.fatal !== undefined) {
    return {
      orderUid,
      env,
      folder: scope,
      matched: false,
      jobCount: 0,
      candidatesScanned: 0,
      summary: { latestState: null, verdict: "SEARCH_FAILED", reasons: [acq.fatal] },
      jobs: [],
      searchMode: acq.searchMode,
      searchError: acq.fatal,
    };
  }

  const confirmed = await confirmJobsForOrder(acq.candidates, orderUid, scope);
  const jobs: JobAnalysis[] = [];
  for (const job of confirmed)
    jobs.push(await toJobAnalysis(job, scope, includeLogs, includeVideo));

  // Newest first (search ordered desc, but confirm batches can reorder).
  jobs.sort((a, b) => (b.creationTime ?? "").localeCompare(a.creationTime ?? ""));
  const latest = jobs[0];

  return {
    orderUid,
    env,
    folder: scope,
    matched: jobs.length > 0,
    jobCount: jobs.length,
    candidatesScanned: acq.candidates.length,
    summary: latest
      ? {
          latestState: latest.state ?? null,
          verdict: latest.verdict,
          reasons: latest.analysis.map((a) => a.message),
        }
      : { latestState: null, verdict: "NO_MATCH", reasons: [] },
    jobs,
    searchMode: acq.searchMode,
    ...(acq.notes.length ? { notes: acq.notes } : {}),
    ...(acq.searchError ? { searchError: acq.searchError } : {}),
  };
}

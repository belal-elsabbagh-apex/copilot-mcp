// Apex read functions: one per OrchestratorBackend read method (Task 17 assembles them into
// `apexBackend`). Fetching lives here; shaping is in map.ts and the pure selection logic in
// read-support.ts. Every call is folder-scoped by the client (api.ts `folder` = name override).

import { type Env, resolveApex } from "../config/config.js";
import { resolvePortal } from "../mcp/reference.js";
import type {
  FaultedJobIssueResult,
  FaultIssueArgs,
  FindOrderArgs,
  FindOrderResult,
  GetJobLogsArgs,
  GetJobLogsResult,
  GetJobsArgs,
  GetJobsResult,
  JobEntry,
  JobLogsEntry,
  ListJobsArgs,
  ListJobsResult,
  ListProcessesArgs,
  ListQueueItemsArgs,
  ListQueueItemsResult,
  ListQueuesArgs,
  ListTriggersArgs,
  PullQueueItemArgs,
  PullQueueItemResult,
  QueueItemPayload,
  QueueItemPayloadArgs,
  QueueKind,
} from "../orchestrator/backend.js";
import { digestLogs } from "../orchestrator/log-digest.js";
import { memberFilename, toPortalRef } from "../orchestrator/portal.js";
import { guardQueueItemSafety } from "../orchestrator/safety.js";
import type {
  JobAttempt,
  JobLog,
  ProcessCore,
  QueueCore,
  QueueItemCore,
  TriggerCore,
} from "../orchestrator/types.js";
import { ExpectedError } from "../shared/expected-error.js";
import { chunk, type StepProgress } from "../shared/util.js";
import {
  type ApexArtifact,
  type ApexJobDetail,
  type ApexQueue,
  type ApexQueueItem,
  downloadArtifact,
  getJob,
  getQueue,
  getQueueItem,
  isNotFound,
  type JobFilters,
  listArtifacts,
  listJobs,
  listQueueItems,
  listQueues,
  listSchedules,
  listWorkflows,
} from "./api.js";
import { apexDashboardLink, resolveFolderId } from "./client.js";
import { formatApexFaultIssue } from "./faults.js";
import { type ParsedJobLog, parseJobLog } from "./job-log.js";
import {
  type ApexLogDigest,
  attemptsFromItem,
  toApexItemCore,
  toApexItemStatus,
  toApexJobAttempt,
  toApexJobCore,
  toApexProcessCore,
  toApexQueueCore,
  toApexQueueTriggers,
  toApexRunSummary,
  toApexScheduleTrigger,
} from "./map.js";
import { matchValueOf, serializeApexContent } from "./payload.js";
import {
  ARTIFACT_POLICY_UNAVAILABLE,
  filterLogRows,
  JOB_LOG,
  logsUnavailableBeforeFetch,
  mergeNewestFirst,
  pickQueue,
  toLogRow,
  videoPart,
  withinSince,
} from "./read-support.js";

const FIND_CONCURRENCY = 6;
const SEARCH_PER_QUEUE_MAX = 200; // one page of fuzzy `search` hits per queue
const ATTEMPTS_PER_ITEM_MAX = 50; // max_attempts is 1–10
const MAX_LOG_BYTES = 5 * 1024 * 1024; // a larger job.log is read from its tail

const toMessage = (e: unknown): string => (e instanceof Error ? e.message : String(e));
const jobLink = (id: string): string => apexDashboardLink(`/jobs/${id}`);
const containsFold = (haystack: string, needle: string | undefined): boolean =>
  !needle || haystack.toLowerCase().includes(needle.toLowerCase());

// ---- jobs ---------------------------------------------------------------------

// processName is matched as a substring over the folder's workflows, then each match is
// listed by workflow_id (a verified filter) — one request per matching workflow.
async function workflowFilter(
  env: Env,
  folder: string | undefined,
  processName: string | undefined,
): Promise<(string | undefined)[]> {
  if (!processName) return [undefined];
  const workflows = await listWorkflows(env, folder);
  return workflows.filter((w) => containsFold(w.name, processName)).map((w) => w.id);
}

export async function apexListJobs(args: ListJobsArgs): Promise<ListJobsResult> {
  const { env, folder, since, top, processName } = args;
  const [resolved, workflowIds] = await Promise.all([
    resolveFolderId(env, folder),
    workflowFilter(env, folder, processName),
  ]);
  const base: JobFilters = since ? { createdAfter: since } : {};
  const lists = await Promise.all(
    workflowIds.map((workflowId) => listJobs(env, folder, { ...base, workflowId }, top)),
  );
  const jobs = mergeNewestFirst(lists, top).map((row) => toApexJobCore(row, jobLink(row.id)));
  return { folder: resolved.name, jobs };
}

async function readLogArtifact(env: Env, artifact: ApexArtifact): Promise<ParsedJobLog> {
  const range = artifact.size_bytes > MAX_LOG_BYTES ? `bytes=-${MAX_LOG_BYTES}` : undefined;
  const { bytes } = await downloadArtifact(env, artifact, range);
  return parseJobLog(new TextDecoder().decode(bytes));
}

// The job's parsed job.log, or null when no job.log artifact is listed. Throws on any HTTP failure.
export async function readJobLog(
  env: Env,
  folder: string | undefined,
  jobId: string,
): Promise<ParsedJobLog | null> {
  const artifact = (await listArtifacts(env, folder, jobId)).find((a) => a.filename === JOB_LOG);
  return artifact ? readLogArtifact(env, artifact) : null;
}

type DigestPart = Pick<JobEntry, "logDigest" | "logsUnavailable" | "logDigestError">;

// Best-effort, as UiPath's digest: a failure surfaces as logDigestError and never hides the job.
async function digestPart(
  env: Env,
  folder: string | undefined,
  detail: ApexJobDetail,
): Promise<DigestPart> {
  const unavailable = logsUnavailableBeforeFetch(detail);
  if (unavailable) return { logsUnavailable: unavailable };
  try {
    const parsed = await readJobLog(env, folder, detail.id);
    if (!parsed) return { logsUnavailable: ARTIFACT_POLICY_UNAVAILABLE };
    const logDigest: ApexLogDigest = {
      ...digestLogs(parsed.rows),
      run: toApexRunSummary(detail, parsed),
    };
    return { logDigest };
  } catch (e) {
    return { logDigestError: toMessage(e) };
  }
}

async function jobEntry(args: GetJobsArgs, id: string): Promise<JobEntry> {
  const { env, folder, batch, includeLogDigest } = args;
  let detail: ApexJobDetail;
  try {
    detail = await getJob(env, folder, id);
  } catch (e) {
    if (isNotFound(e)) return { id, found: false };
    if (batch) return { id, found: false, error: toMessage(e) };
    throw e;
  }
  const job = toApexJobCore(detail, jobLink(id));
  if (!includeLogDigest) return { id, found: true, job };
  return { id, found: true, job, ...(await digestPart(env, folder, detail)) };
}

export async function apexGetJobs(
  args: GetJobsArgs,
  onProgress?: StepProgress,
): Promise<GetJobsResult> {
  const resolved = await resolveFolderId(args.env, args.folder);
  let done = 0;
  const entries = await Promise.all(
    args.jobIds.map(async (id) => {
      try {
        return await jobEntry(args, id);
      } finally {
        done += 1;
        onProgress?.(done, args.jobIds.length, `diagnosed ${done}/${args.jobIds.length} job(s)`);
      }
    }),
  );
  return { folder: resolved.name, entries };
}

async function jobLogsEntry(args: GetJobLogsArgs, jobId: string): Promise<JobLogsEntry> {
  const { env, folder, filter, fullMessages, includeRawFields, includeVideo } = args;
  const detail = await getJob(env, folder, jobId);
  const empty: JobLogsEntry = { jobId, logs: [], returned: 0, truncated: false };

  const unavailable = logsUnavailableBeforeFetch(detail);
  if (unavailable) {
    return {
      ...empty,
      logsUnavailable: unavailable,
      ...(includeVideo ? { videoError: unavailable.detail } : {}),
    };
  }
  const artifacts = await listArtifacts(env, folder, jobId);
  const video = includeVideo ? videoPart(artifacts) : {};
  const logArtifact = artifacts.find((a) => a.filename === JOB_LOG);
  if (!logArtifact) return { ...empty, logsUnavailable: ARTIFACT_POLICY_UNAVAILABLE, ...video };

  const { rows } = await readLogArtifact(env, logArtifact);
  const { kept, totalMatching } = filterLogRows(rows, filter);
  return {
    ...empty,
    logs: kept.map((r) => toLogRow(r, fullMessages, includeRawFields)),
    returned: kept.length,
    totalMatching, // exact: the whole log is fetched, so truncated stays false
    ...video,
  };
}

export async function apexGetJobLogs(
  args: GetJobLogsArgs,
  onProgress?: StepProgress,
): Promise<GetJobLogsResult> {
  const resolved = await resolveFolderId(args.env, args.folder);
  let done = 0;
  const entries = await Promise.all(
    args.jobIds.map(async (jobId): Promise<JobLogsEntry> => {
      try {
        return await jobLogsEntry(args, jobId);
      } catch (e) {
        if (!args.batch) throw e;
        return { jobId, logs: [], returned: 0, truncated: false, error: toMessage(e) };
      } finally {
        done += 1;
        onProgress?.(
          done,
          args.jobIds.length,
          `fetched logs for ${done}/${args.jobIds.length} job(s)`,
        );
      }
    }),
  );
  return { folder: resolved.name, entries };
}

// ---- queues, queue items ------------------------------------------------------

export async function apexListQueues(args: ListQueuesArgs): Promise<QueueCore[]> {
  const queues = await listQueues(args.env, args.folder);
  return queues
    .filter((q) => containsFold(q.name, args.nameContains))
    .slice(0, args.top)
    .map(toApexQueueCore);
}

async function resolveQueue(env: Env, input: string, kind: QueueKind): Promise<ApexQueue> {
  const queues = await listQueues(env, undefined);
  const hit = pickQueue(queues, input, kind);
  if (hit) return hit;
  throw new ExpectedError(
    `no Apex ${kind} queue matches '${input}'. Queues in this folder: ${queues.map((q) => q.name).join(", ")}`,
  );
}

export async function apexListQueueItems(args: ListQueueItemsArgs): Promise<ListQueueItemsResult> {
  const { env, queue, queueKind, status, top } = args;
  const picked = await resolveQueue(env, queue, queueKind);
  const filters = status ? { status: toApexItemStatus(status) } : {};
  const rows = await listQueueItems(env, undefined, picked.id, filters, top);
  return {
    queueId: picked.id,
    queueName: picked.name,
    items: rows
      .slice(0, top)
      .map((r) => toApexItemCore(r, attemptsFromItem(r), false, picked.name)),
  };
}

interface Hit {
  queue: ApexQueue;
  item: ApexQueueItem;
}

// Every attempt of an item is a job with the item's reference; filter by queue_item_id because
// references are not unique (enforce_unique_reference is off). The item's own latest job id
// is the fallback when the reference listing finds none.
async function foundItem(
  env: Env,
  folder: string | undefined,
  { queue, item }: Hit,
  includeContent: boolean,
): Promise<QueueItemCore> {
  const jobs = await listJobs(env, folder, { reference: item.reference }, ATTEMPTS_PER_ITEM_MAX);
  const attempts = jobs.filter((j) => j.queue_item_id === item.id).map(toApexJobAttempt);
  return toApexItemCore(
    item,
    attempts.length > 0 ? attempts : attemptsFromItem(item),
    includeContent,
    queue.name,
  );
}

async function searchAllQueues(
  env: Env,
  folder: string | undefined,
  queues: ApexQueue[],
  needle: string,
): Promise<{ hits: Hit[]; scanned: number }> {
  const hits: Hit[] = [];
  let scanned = 0;
  for (const group of chunk(queues, FIND_CONCURRENCY)) {
    const found = await Promise.all(
      group.map(async (queue) => ({
        queue,
        rows: await listQueueItems(env, folder, queue.id, { search: needle }, SEARCH_PER_QUEUE_MAX),
      })),
    );
    for (const { queue, rows } of found) {
      scanned += rows.length;
      // `search` is fuzzy: only an exact specific_content.orderUid is the order's item.
      for (const item of rows) {
        if (item.specific_content["orderUid"] === needle) hits.push({ queue, item });
      }
    }
  }
  return { hits, scanned };
}

export async function apexFindOrderQueueItems(args: FindOrderArgs): Promise<FindOrderResult> {
  const { env, folder, since, top, includeSpecificContent } = args;
  const needle = args.orderUid.trim();
  const resolved = await resolveFolderId(env, folder);
  const queues = await listQueues(env, folder); // every queue, including Unrouted and inactive ones
  const { hits, scanned } = await searchAllQueues(env, folder, queues, needle);

  const windowed = withinSince(
    hits.map((h) => ({ ...h, created_at: h.item.created_at })),
    since,
  );
  const newest = windowed.rows
    .sort((a, b) => b.created_at.localeCompare(a.created_at))
    .slice(0, top);
  const items: QueueItemCore[] = [];
  for (const group of chunk(newest, FIND_CONCURRENCY)) {
    items.push(
      ...(await Promise.all(group.map((h) => foundItem(env, folder, h, includeSpecificContent)))),
    );
  }
  return {
    folder: resolved.name,
    searchMode: "search",
    scanned,
    items,
    notes: windowed.note ? [windowed.note] : [],
  };
}

async function queueNameOf(
  env: Env,
  queueId: string,
): Promise<{ queueName: string; queueNameError?: string }> {
  try {
    return { queueName: (await getQueue(env, undefined, queueId)).name };
  } catch (e) {
    return { queueName: "", queueNameError: toMessage(e) };
  }
}

export async function apexPullQueueItem(args: PullQueueItemArgs): Promise<PullQueueItemResult> {
  const { env, queueId, itemId } = args;
  if (args.url || args.folderId) {
    throw new ExpectedError(
      "Apex pull_queue_item takes env + queueId + itemId only (url and folderId are UiPath-only)",
    );
  }
  if (!(env && queueId && itemId)) {
    throw new ExpectedError(
      "Apex pull_queue_item needs env, queueId and itemId (from list_queue_items or find_order_queue_items)",
    );
  }
  const item = await getQueueItem(env, undefined, queueId, itemId);
  const { queueName, queueNameError } = await queueNameOf(env, queueId);
  // IsApproved is forced false so a pulled item can never submit a real auth when re-posted.
  const specificContent = guardQueueItemSafety(item.specific_content, "force").specificContent;
  return {
    env,
    item: { ...toApexItemCore(item, attemptsFromItem(item), false, queueName), specificContent },
    portal: toPortalRef(resolvePortal(queueName)),
    isApprovedForced: true,
    suggestedFilename: memberFilename(specificContent),
    ...(queueNameError ? { queueNameError } : {}),
  };
}

// ---- processes, triggers ------------------------------------------------------

export async function apexListProcesses(args: ListProcessesArgs): Promise<ProcessCore[]> {
  const workflows = await listWorkflows(args.env, args.folder);
  return workflows
    .filter((w) => containsFold(w.name, args.nameContains))
    .slice(0, args.top)
    .map(toApexProcessCore);
}

// Queues stand in for queue triggers (Apex has no /triggers route); cron schedules are the
// time triggers and are not queue-scoped, so a queueId filter returns queue triggers only.
export async function apexListTriggers(args: ListTriggersArgs): Promise<TriggerCore[]> {
  const { env, folder, queueId, top } = args;
  const queueTriggers = toApexQueueTriggers(await listQueues(env, folder)).filter(
    (t) => !queueId || t.queueId === queueId,
  );
  const timeTriggers = queueId ? [] : (await listSchedules(env, folder)).map(toApexScheduleTrigger);
  return [...queueTriggers, ...timeTriggers].slice(0, top);
}

// ---- build_faulted_job_issue ---------------------------------------------

const MAX_ATTEMPTS = 50;

// The attempts of one queue item, newest first: a retry is a NEW job on the same item, and
// /jobs?reference= is the only verified item filter. Two items can share a reference, so
// keep only this item's jobs.
async function jobAttempts(
  env: Env,
  folder: string | undefined,
  detail: ApexJobDetail,
): Promise<JobAttempt[]> {
  if (!detail.queue_item_reference) return [toApexJobAttempt(detail)];
  const rows = await listJobs(
    env,
    folder,
    { reference: detail.queue_item_reference },
    MAX_ATTEMPTS,
  );
  return rows.filter((r) => r.queue_item_id === detail.queue_item_id).map(toApexJobAttempt);
}

// Logs are a best-effort attachment: a failed fetch must not discard a valid job lookup
// (same convention as UiPath's buildFaultedJobIssue). A missing job.log is not a failure:
// never-started reaps and artifact_policy leave none.
async function bestEffortLogRows(
  env: Env,
  folder: string | undefined,
  jobId: string,
): Promise<{ rows: JobLog[]; error?: string }> {
  try {
    return { rows: (await readJobLog(env, folder, jobId))?.rows ?? [] };
  } catch (e) {
    return { rows: [], error: e instanceof Error ? e.message : String(e) };
  }
}

export async function apexBuildFaultedJobIssue(
  args: FaultIssueArgs,
): Promise<FaultedJobIssueResult> {
  const { env, jobId, folder } = args;
  const detail = await getJob(env, folder, jobId);
  const job = toApexJobCore(detail, apexDashboardLink(`/jobs/${detail.id}`));
  const { fault } = job;
  if (!fault?.message || !fault.signature) {
    throw new ExpectedError(
      `job ${jobId} is ${job.state}, not a fault — build_faulted_job_issue needs a failed, ` +
        "timed-out, lost or stopped job",
    );
  }
  const [attempts, logs] = await Promise.all([
    jobAttempts(env, folder, detail),
    bestEffortLogRows(env, folder, jobId),
  ]);
  const issue = formatApexFaultIssue(
    { ...job, fault: { ...fault, message: fault.message, signature: fault.signature } },
    logs.rows,
    { env, attempts, repo: args.repo, labels: args.labels },
  );
  return { ...issue, found: true, ...(logs.error ? { logsError: logs.error } : {}) };
}

// ---- build_queue_item serializer -------------------------------------------

// Pure given config: native content + the configured callback host/queue, guarded
// (IsApproved forced false), shaped as the body add_queue_item posts. No silent fallback
// for serverURL/queueUrl — an item pointing a robot at the wrong host is worse than none.
export function apexBuildQueueItemPayload(args: QueueItemPayloadArgs): QueueItemPayload {
  const apex = resolveApex();
  const serverURL = apex.serverUrlByEnv[args.env];
  if (!serverURL) {
    throw new ExpectedError(
      `cannot build an Apex queue item for env '${args.env}': apex.serverUrlByEnv.${args.env} ` +
        "is not configured (it is the callback host the item's serverURL must carry)",
    );
  }
  if (!apex.queueUrl) {
    throw new ExpectedError(
      "cannot build an Apex queue item: apex.queueUrl is not configured " +
        "(the callback queue the item's queueUrl must carry)",
    );
  }
  const { specificContent, forced } = guardQueueItemSafety(
    { ...args.content, serverURL, queueUrl: apex.queueUrl },
    "force",
  );
  const content = serializeApexContent(specificContent);
  const reference = `${args.orderUid}-${Math.floor(Date.now() / 1000)}`;
  const notes = [
    `serverURL/queueUrl come from apex.serverUrlByEnv.${args.env} / apex.queueUrl; native types ` +
      "(arrays, booleans, numbers, callbackContext object) are kept as Apex stores them.",
    `To post: add_queue_item(orchestrator: "apex", env: "pre_prod", queue: "${args.queueName}", ` +
      "reference: payload.body.reference, specificContent: payload.body.specific_content). " +
      "match_value is re-derived from specificContent.automationId at post time.",
  ];
  if (forced.length) notes.unshift(`${forced.join(", ")} forced to false (test-safety guard).`);
  return {
    payload: {
      queueName: args.queueName,
      body: { specific_content: content, reference, match_value: matchValueOf(content) },
    },
    notes,
    meta: {
      queueName: args.queueName,
      env: args.env,
      orderUid: args.orderUid,
      note: "post with add_queue_item(orchestrator: apex)",
    },
  };
}

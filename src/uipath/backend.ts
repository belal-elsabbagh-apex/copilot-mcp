// UiPath implementation of the orchestrator read surface (OrchestratorBackend, see
// orchestrator/backend.ts): the handler logic that used to live in server.ts, returning
// the backend-neutral cores. Task 4 adds the write/payload functions and assembles
// `uipathBackend` at the bottom.

import type { Env } from "../config/config.js";
import { normalizeOutput } from "../copilot/output-schema.js";
import {
  envForFolderId,
  folderNameForId,
  normalizeQueueName,
  PORTALS,
  resolvePortal,
  resolvePortalByDefId,
} from "../mcp/reference.js";
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
  LogRow,
  PullQueueItemArgs,
  PullQueueItemResult,
  QueueKind,
} from "../orchestrator/backend.js";
import { truncate } from "../orchestrator/log-digest.js";
import { memberFilename, toPortalRef } from "../orchestrator/portal.js";
import { guardQueueItemSafety } from "../orchestrator/safety.js";
import type { ProcessCore, QueueCore, TriggerCore } from "../orchestrator/types.js";
import { ExpectedError } from "../shared/expected-error.js";
import type { StepProgress } from "../shared/util.js";
import { buildFaultedJobIssue } from "./faults.js";
import {
  toJobCore,
  toProcessCore,
  toQueueCore,
  toQueueItemCoreFromItem,
  toQueueItemCoreFromMatch,
  toTriggerCore,
  toUiPathLogDigest,
  uipathFault,
} from "./map.js";
import { parseQueueItemUrl } from "./queue.js";
import { parseRawMessage } from "./raw-message.js";
import {
  type FolderScope,
  fetchJobLogs,
  fetchJobLogsForKeys,
  fetchJobsForKeys,
  fetchJobVideoUrl,
  getQueueDefinitionName,
  getQueueItem,
  type JobLogFilter,
  type JobLookup,
  jobDeepLink,
  listQueueDefinitions,
  listQueueItems,
  listRecentJobs,
  listReleases,
  listTriggers,
  resolveFolder,
  resolveOrgUnitId,
  scopeForEnv,
  searchQueueItemsByOrderId,
  type UiPathJob,
} from "./uipath.js";

const toMessage = (e: unknown): string => (e instanceof Error ? e.message : String(e));

// Queue/process/trigger/item reads: the env's org-unit id always applies; an explicit
// `folder` overrides only the folder-path header (as find_order_queue_items always did).
const scopeFor = (env: Env, folder?: string): FolderScope => ({
  orgUnitId: resolveOrgUnitId(env),
  folderPath: resolveFolder(env, folder) ?? "",
});

// ---- jobs ------------------------------------------------------------------

export async function uipathListJobs(args: ListJobsArgs): Promise<ListJobsResult> {
  const folder = resolveFolder(args.env, args.folder);
  const jobs = await listRecentJobs(args.since, args.top, folder, args.processName);
  return {
    folder: folder ?? "",
    jobs: jobs.map((j) => toJobCore(j, { link: jobDeepLink(j.Key ?? "") })),
  };
}

// The job's parsed OutputArguments with token/callbackContext stripped (normalizeOutput);
// unparseable output degrades to its first 500 characters; none at all is null.
function parseJobOutput(job: UiPathJob): unknown {
  if (!job.OutputArguments) return null;
  try {
    const parsed = JSON.parse(job.OutputArguments) as Record<string, unknown>;
    return Object.fromEntries(normalizeOutput(parsed).fields);
  } catch {
    return job.OutputArguments.slice(0, 500);
  }
}

// One id's entry. A single explicit id fails like any other tool error; a batch keeps going
// and attaches the failure to just this entry. With includeLogDigest the logs are a
// best-effort attachment: a fetch failure lands on logDigestError and leaves fault null
// (a fault synthesized from zero logs would be a state-only signature that mis-dedupes).
async function jobEntry(
  id: string,
  lookup: JobLookup,
  args: GetJobsArgs,
  folder: string | undefined,
): Promise<JobEntry> {
  if (lookup.error && !args.batch) throw lookup.error;
  if (lookup.error) return { id, found: false, error: toMessage(lookup.error) };
  const { job } = lookup;
  if (!job) return { id, found: false };
  const core = toJobCore(job, { link: jobDeepLink(id), output: parseJobOutput(job) });
  if (!args.includeLogDigest) return { id, found: true, job: core };
  try {
    const logs = await fetchJobLogs(id, folder, { includeRawFields: true });
    const fault = job.State === "Successful" ? null : uipathFault(job, logs);
    return { id, found: true, job: { ...core, fault }, logDigest: toUiPathLogDigest(logs) };
  } catch (e) {
    return { id, found: true, job: core, logDigestError: toMessage(e) };
  }
}

export async function uipathGetJobs(
  args: GetJobsArgs,
  onProgress?: StepProgress,
): Promise<GetJobsResult> {
  const folder = resolveFolder(args.env, args.folder);
  const byId = await fetchJobsForKeys(args.jobIds, folder);
  // With includeLogDigest a 25-id batch is 50 Orchestrator calls — count them off as
  // each id finishes rather than going quiet for the whole batch.
  let done = 0;
  const entries = await Promise.all(
    args.jobIds.map(async (id) => {
      try {
        return await jobEntry(id, byId[id] ?? { job: null }, args, folder);
      } finally {
        done++;
        onProgress?.(done, args.jobIds.length, `diagnosed ${done}/${args.jobIds.length} job(s)`);
      }
    }),
  );
  return { folder: folder ?? "", entries };
}

export async function uipathGetJobLogs(
  args: GetJobLogsArgs,
  onProgress?: StepProgress,
): Promise<GetJobLogsResult> {
  const folder = resolveFolder(args.env, args.folder);
  const { minLevel, contains, onlyFailures, tail } = args.filter;
  const filter: JobLogFilter = {
    onlyFailures,
    includeRawFields: args.includeRawFields,
    ...(minLevel !== undefined ? { minLevel } : {}),
    ...(contains !== undefined ? { contains } : {}),
    ...(tail !== undefined ? { tail } : {}),
  };
  const byId = await fetchJobLogsForKeys(args.jobIds, folder, filter, onProgress);
  const entries = await Promise.all(
    args.jobIds.map(async (id): Promise<JobLogsEntry> => {
      const result = byId[id] ?? { logs: [], totalMatching: null };
      if (result.error && !args.batch) throw result.error;
      const { logs, totalMatching } = result;
      // RawMessage (the raw JSON text) never leaks into the output — only the parsed
      // `raw` object does, and only when includeRawFields was requested.
      const rows: LogRow[] = logs.map((l) => ({
        Level: l.Level,
        Message: args.fullMessages ? l.Message : truncate(l.Message),
        TimeStamp: l.TimeStamp,
        ...(args.includeRawFields ? { raw: parseRawMessage(l) } : {}),
      }));
      const entry: JobLogsEntry = {
        jobId: id,
        logs: rows,
        returned: rows.length,
        // truncated = the 500-row fetch window didn't cover every matching row
        // (a requested `tail` shorter than the total is not truncation).
        truncated: totalMatching !== null && totalMatching > 500,
        ...(totalMatching !== null ? { totalMatching } : {}),
        ...(result.error ? { error: toMessage(result.error) } : {}),
      };
      // Video is a secondary attachment, always best-effort — never fails the primary
      // logs result, single id or batch alike. No recording at all means no `video`.
      if (args.includeVideo) {
        try {
          const url = await fetchJobVideoUrl(id, folder);
          if (url) entry.video = { kind: "url", url };
        } catch (e) {
          entry.videoError = toMessage(e);
        }
      }
      return entry;
    }),
  );
  return { folder: folder ?? "", entries };
}

// ---- queues ----------------------------------------------------------------

export async function uipathListQueues(args: ListQueuesArgs): Promise<QueueCore[]> {
  const defs = await listQueueDefinitions(
    scopeFor(args.env, args.folder),
    args.nameContains ?? "",
    args.top,
  );
  return defs.map(toQueueCore);
}

// A numeric id is a QueueDefinitionId as-is; anything else is a portal key/alias whose
// submit/sync id comes from the static registry (prod ids only — pass the numeric id for
// dev-clone queues). `name` is the portal key when the registry knows the queue, else "".
function resolveQueue(queue: string, kind: QueueKind): { id: number; name: string } {
  if (/^\d+$/.test(queue)) {
    const id = Number(queue);
    return { id, name: resolvePortalByDefId(id).key };
  }
  const portal = resolvePortal(queue);
  if (!portal.matched) {
    const known = PORTALS.map((p) => p.key).join(", ");
    throw new ExpectedError(
      `unknown queue '${queue}'. Pass a numeric queue id or one of: ${known}`,
    );
  }
  const id = kind === "sync" ? portal.syncQueueDefId : portal.submitQueueDefId;
  if (!id) {
    throw new ExpectedError(
      `portal '${portal.key}' has no known ${kind} queue id — pass the numeric queue id`,
    );
  }
  return { id, name: portal.key };
}

// Browse a queue, newest first (PHI-light: no specificContent in the projection).
export async function uipathListQueueItems(
  args: ListQueueItemsArgs,
): Promise<ListQueueItemsResult> {
  const { id, name } = resolveQueue(args.queue, args.queueKind);
  const rows = await listQueueItems(id, scopeFor(args.env), args.status ?? "", args.top);
  return {
    queueId: String(id),
    queueName: name || normalizeQueueName(rows[0]?.name ?? ""),
    items: rows.map((r) =>
      toQueueItemCoreFromItem(r, { queueName: r.name, specificContent: null }),
    ),
  };
}

// Resolve orderUid -> the UiPath queue item(s) it flowed through. When a `since`-bounded
// search finds nothing, retry once without the bound (the item may predate it).
export async function uipathFindOrderQueueItems(args: FindOrderArgs): Promise<FindOrderResult> {
  const scope = scopeFor(args.env, args.folder);
  let q = await searchQueueItemsByOrderId(args.orderUid, scope, args.top, args.since);
  const notes = [...q.notes];
  if (args.since && q.matches.length === 0) {
    q = await searchQueueItemsByOrderId(args.orderUid, scope, args.top, undefined);
    notes.push(...q.notes);
    notes.push(`no queue item matched within since=${args.since}; retried without the time bound.`);
  }
  const items = q.matches.map((m) => {
    const core = toQueueItemCoreFromMatch(m, {
      queueName: resolvePortalByDefId(m.queueDefinitionId).key,
    });
    return args.includeSpecificContent ? core : { ...core, specificContent: null };
  });
  return {
    folder: scope.folderPath,
    searchMode: q.searchMode,
    scanned: q.scanned,
    items,
    notes,
    ...(q.searchError ? { searchError: q.searchError } : {}),
  };
}

// Where to pull from: an Orchestrator URL (fid + item id parsed out; env derived from the
// fid) or an explicit numeric itemId scoped by env (folderId omitted = the env default).
function resolvePull(args: PullQueueItemArgs): { itemId: number; env: Env; scope: FolderScope } {
  if (args.url) {
    const parsed = parseQueueItemUrl(args.url);
    return {
      itemId: parsed.txnId,
      env: envForFolderId(parsed.folderId),
      // Query by the URL's actual fid (correct even for folders we don't map).
      scope: { orgUnitId: parsed.folderId, folderPath: folderNameForId(parsed.folderId) },
    };
  }
  if (!args.env) {
    throw new ExpectedError("env is required when pulling by itemId (no url to derive it from)");
  }
  const itemId = Number(args.itemId);
  if (!Number.isInteger(itemId) || itemId <= 0) {
    throw new ExpectedError("UiPath pull_queue_item needs a url or a positive numeric itemId");
  }
  const scope: FolderScope = args.folderId
    ? { orgUnitId: args.folderId, folderPath: folderNameForId(args.folderId) }
    : scopeForEnv(args.env);
  return { itemId, env: args.env, scope };
}

// Fetch one queue item as a ready-to-run test payload.
// CRITICAL: IsApproved is forced false so a test run can never submit a real auth.
export async function uipathPullQueueItem(args: PullQueueItemArgs): Promise<PullQueueItemResult> {
  const { itemId, env, scope } = resolvePull(args);
  const item = await getQueueItem(itemId, scope);

  let queueName = normalizeQueueName(item.name);
  // Secondary "try harder" lookup — best-effort: a real failure here shouldn't fail the
  // whole pull when item.name is already a usable fallback, but it must still surface.
  let queueNameError: string | undefined;
  if ((!queueName || !resolvePortal(queueName).matched) && item.queueDefinitionId) {
    try {
      const resolved = await getQueueDefinitionName(item.queueDefinitionId, scope);
      if (resolved) queueName = normalizeQueueName(resolved);
    } catch (e) {
      queueNameError = toMessage(e);
    }
  }
  const specificContent = guardQueueItemSafety(item.specificContent, "force").specificContent;
  return {
    env,
    item: toQueueItemCoreFromItem(item, { queueName, specificContent }),
    portal: toPortalRef(resolvePortal(queueName)),
    isApprovedForced: true,
    suggestedFilename: memberFilename(specificContent),
    ...(queueNameError ? { queueNameError } : {}),
  };
}

// ---- processes / triggers --------------------------------------------------

export async function uipathListProcesses(args: ListProcessesArgs): Promise<ProcessCore[]> {
  const releases = await listReleases(
    scopeFor(args.env, args.folder),
    args.nameContains ?? "",
    args.top,
  );
  return releases.map(toProcessCore);
}

// ProcessSchedules has no server-side queue filter: `top` bounds the fetch, then the
// queueId filter applies to what came back.
export async function uipathListTriggers(args: ListTriggersArgs): Promise<TriggerCore[]> {
  const triggers = await listTriggers(scopeFor(args.env, args.folder), args.top);
  const cores = triggers.map(toTriggerCore);
  return args.queueId ? cores.filter((t) => t.queueId === args.queueId) : cores;
}

// ---- faulted-job issue -----------------------------------------------------

export function uipathBuildFaultedJobIssue(args: FaultIssueArgs): Promise<FaultedJobIssueResult> {
  return buildFaultedJobIssue(args.env, args.jobId, {
    folder: args.folder,
    repo: args.repo,
    labels: args.labels,
  });
}

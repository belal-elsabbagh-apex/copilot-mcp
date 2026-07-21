// UiPath Orchestrator WRITES — the only module that mutates Orchestrator state.
// Everything here is pre_prod-only (the dev clone, folder 434039): each function
// asserts env === "pre_prod" as defense in depth beneath the tools' schema-level
// z.literal("pre_prod"). Prod is never written to (repo invariant).
//
// Extracted from the dev-clone-e2e-testing skill's per-portal scripts into atomic,
// portal-agnostic operations: enqueue an item (with the shared test-safety guard),
// delete a stale New item, and start a job for a release.

import { type Env, getUipath } from "../config/config.js";
import { isRecord } from "../shared/util.js";
import { guardQueueItemSafety, type QueueSafetyLimits } from "./safety.js";
import {
  fetchJobByKey,
  getQueueItem,
  jobDeepLink,
  type QueueItem,
  resolveFolder,
  scopeForEnv,
  toQueueItem,
  type UiPathJob,
  uipathDelete,
  uipathPost,
} from "./uipath.js";

const ADD_QUEUE_ITEM_PATH = "/odata/Queues/UiPathODataSvc.AddQueueItem";
const START_JOBS_PATH = "/odata/Jobs/UiPath.Server.Configuration.OData.StartJobs";

function assertPreProd(env: Env): void {
  if (env !== "pre_prod") {
    throw new Error(`UiPath writes are pre_prod-only (dev clone) — refusing env '${env}'`);
  }
}

// The configured safe values the guard pins posted items to.
function safetyLimits(): QueueSafetyLimits {
  const cfg = getUipath();
  return {
    preProdServerUrl: cfg.serverUrlByEnv?.pre_prod ?? "",
    queueUrl: cfg.queueUrl ?? "",
  };
}

// ---- add_queue_item --------------------------------------------------------

export type QueueItemPriority = "Low" | "Normal" | "High";

export interface AddQueueItemArgs {
  env: Env; // asserted === "pre_prod"
  queueName: string;
  reference: string;
  priority: QueueItemPriority;
  specificContent: Record<string, unknown>;
}

export interface AddQueueItemResult {
  env: Env;
  queueName: string;
  reference: string;
  itemId: number; // id of the created item (0 if the response omitted it)
  status: string; // "New" on success
  forced: string[]; // fields the safety guard overrode, e.g. ["IsApproved"]
}

// Pure body builder (unit-tested without HTTP). `specificContent` must already be guarded.
export function buildAddQueueItemBody(
  queueName: string,
  reference: string,
  priority: QueueItemPriority,
  specificContent: Record<string, unknown>,
): unknown {
  return {
    itemData: {
      Name: queueName,
      Priority: priority,
      Reference: reference,
      SpecificContent: specificContent,
    },
  };
}

// POST one item to a dev-clone queue. Every payload passes the test-safety guard
// first (IsApproved forced false; serverURL/queueUrl pinned to the configured
// pre-prod values; <TO-FILL> placeholders rejected).
export async function addQueueItem(args: AddQueueItemArgs): Promise<AddQueueItemResult> {
  assertPreProd(args.env);
  if (!args.queueName.trim()) throw new Error("queueName is required");
  if (!args.reference.trim()) throw new Error("reference is required");
  const guarded = guardQueueItemSafety(args.specificContent, "preProdPost", safetyLimits());
  const body = buildAddQueueItemBody(
    args.queueName,
    args.reference,
    args.priority,
    guarded.specificContent,
  );
  const path = getUipath().addQueueItemPath || ADD_QUEUE_ITEM_PATH;
  const item = toQueueItem(await uipathPost(path, body, scopeForEnv("pre_prod")));
  return {
    env: args.env,
    queueName: args.queueName,
    reference: args.reference,
    itemId: item.id,
    status: item.status,
    forced: guarded.forced,
  };
}

// ---- delete_queue_item -----------------------------------------------------

// Only never-picked-up items may be deleted — an InProgress/Successful/Failed item
// is execution history (and possibly a running robot's transaction).
export function assertDeletable(item: QueueItem): void {
  if (item.status !== "New") {
    throw new Error(
      `refusing to delete queue item ${item.id}: status is '${item.status}' ` +
        "(only 'New' items may be deleted)",
    );
  }
}

export interface DeleteQueueItemResult {
  env: Env;
  itemId: number;
  deleted: true;
  previousStatus: "New";
  reference: string;
}

// Fetch-first delete: read the item, refuse unless Status is "New", then DELETE.
export async function deleteQueueItem(itemId: number, env: Env): Promise<DeleteQueueItemResult> {
  assertPreProd(env);
  const scope = scopeForEnv("pre_prod");
  const item = await getQueueItem(itemId, scope);
  assertDeletable(item);
  await uipathDelete(`/odata/QueueItems(${itemId})`, scope);
  return { env, itemId, deleted: true, previousStatus: "New", reference: item.reference };
}

// ---- start_job -------------------------------------------------------------

export interface StartJobArgs {
  env: Env; // asserted === "pre_prod"
  releaseKey: string; // Release GUID Key (from listReleases / list_processes)
  inputArguments: Record<string, unknown>; // {} = none
  jobsCount: number;
}

export interface StartedJob {
  id: number;
  key: string;
  state: string;
  releaseName: string;
  deepLink: string;
}

export interface StartJobResult {
  env: Env;
  jobs: StartedJob[];
}

// Pure body builder. InputArguments is the JSON-stringified map (Orchestrator's
// wire format) and is omitted entirely when empty.
export function buildStartJobBody(args: StartJobArgs): unknown {
  const hasArgs = Object.keys(args.inputArguments).length > 0;
  return {
    startInfo: {
      ReleaseKey: args.releaseKey,
      Strategy: "ModernJobsCount",
      JobsCount: args.jobsCount,
      ...(hasArgs ? { InputArguments: JSON.stringify(args.inputArguments) } : {}),
    },
  };
}

// Start job(s) for a dev-clone release. NOTE the pin race: a job resolves its
// package version at START (not creation) — verify the release's ProcessVersion
// via listReleases right before starting.
export async function startJob(args: StartJobArgs): Promise<StartJobResult> {
  assertPreProd(args.env);
  if (!args.releaseKey.trim()) throw new Error("releaseKey is required");
  const data = await uipathPost(START_JOBS_PATH, buildStartJobBody(args), scopeForEnv("pre_prod"));
  const rows = isRecord(data) && Array.isArray(data["value"]) ? data["value"] : [];
  const jobs: StartedJob[] = rows.map((raw: unknown) => {
    const r = isRecord(raw) ? raw : {};
    const key = typeof r["Key"] === "string" ? r["Key"] : "";
    return {
      id: typeof r["Id"] === "number" ? r["Id"] : 0,
      key,
      state: typeof r["State"] === "string" ? r["State"] : "",
      releaseName: typeof r["ReleaseName"] === "string" ? r["ReleaseName"] : "",
      deepLink: key ? jobDeepLink(key) : "",
    };
  });
  return { env: args.env, jobs };
}

// ---- stop_job ---------------------------------------------------------------

export type StopJobStrategy = "SoftStop" | "Kill";

export interface StopJobArgs {
  env: Env; // asserted === "pre_prod"
  jobKey: string; // GUID Key (from start_job / list_jobs / get_job)
  strategy: StopJobStrategy;
}

export interface StopJobResult {
  env: Env;
  jobId: number;
  jobKey: string;
  strategy: StopJobStrategy;
  stopped: true;
}

// A job already in one of these states has nothing left to stop.
const TERMINAL_JOB_STATES = new Set(["Successful", "Faulted", "Stopped"]);

export function assertStoppable(job: UiPathJob): void {
  if (job.State && TERMINAL_JOB_STATES.has(job.State)) {
    throw new Error(
      `refusing to stop job '${job.Key}': state is already '${job.State}' (terminal)`,
    );
  }
}

// Stop (SoftStop) or kill a dev-clone job. StopJob addresses Orchestrator jobs by
// numeric Id, not the GUID Key the rest of this API uses, so resolve the Key
// first — scoped to the pre-prod folder, so a prod job's Key resolves to nothing
// here. Fetch-first, like deleteQueueItem: refuses (no HTTP write issued) once the
// job is already in a terminal state.
export async function stopJob(args: StopJobArgs): Promise<StopJobResult> {
  assertPreProd(args.env);
  const jobKey = args.jobKey.trim();
  if (!jobKey) throw new Error("jobKey is required");
  const job = await fetchJobByKey(jobKey, resolveFolder("pre_prod"));
  if (!job?.Id) throw new Error(`no dev-clone job found for key '${jobKey}'`);
  assertStoppable(job);
  await uipathPost(
    `/odata/Jobs(${job.Id})/UiPath.Server.Configuration.OData.StopJob`,
    { strategy: args.strategy },
    scopeForEnv("pre_prod"),
  );
  return { env: args.env, jobId: Number(job.Id), jobKey, strategy: args.strategy, stopped: true };
}

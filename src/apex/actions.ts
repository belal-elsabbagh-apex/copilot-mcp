// Apex WRITES — the only module that mutates Apex state. Everything here is pre_prod-only
// (the 'Authorization Test' folder): every function asserts env === "pre_prod" BEFORE any
// I/O, beneath the tools' schema-level z.literal("pre_prod") and the apexRequest
// chokepoint's own non-GET gate. Prod and test folders share one deployment and one login,
// so the folder is the only boundary (repo invariant).
//
// The same four operations as uipath/actions.ts — enqueue a guarded item, delete a New
// item, start jobs, stop a job — Apex-shaped: queues and Processes are resolved by
// name/id at call time (nothing hardcoded), and every refusal (guard violation, non-new
// item, terminal job) happens before the write is issued. Writes are never retried.

import { type Env, resolveApex } from "../config/config.js";
import type {
  AddQueueItemArgs,
  AddQueueItemResult,
  DeleteQueueItemArgs,
  DeleteQueueItemResult,
  StartedJob,
  StartJobArgs,
  StartJobResult,
  StopJobArgs,
  StopJobResult,
  StopStrategy,
} from "../orchestrator/backend.js";
import { guardQueueItemSafety } from "../orchestrator/safety.js";
import { ExpectedError } from "../shared/expected-error.js";
import { prop } from "../shared/util.js";
import { type ApexQueue, getJob, getQueueItem, listQueues, listWorkflows } from "./api.js";
import { apexDashboardLink, apexRequest, resolveFolderId } from "./client.js";
import { ApexApiError } from "./errors.js";
import { APEX_ITEM_LABELS, APEX_JOB_LABELS, APEX_TERMINAL_STATUSES } from "./map.js";
import { matchValueOf } from "./payload.js";

const PRE_PROD = "pre_prod" as const;

function assertPreProd(env: Env): void {
  if (env !== PRE_PROD) {
    throw new ExpectedError(
      `Apex writes are pre_prod-only (Authorization Test folder) — refusing env '${env}'`,
    );
  }
}

const str = (v: unknown): string => (typeof v === "string" ? v : "");
const label = (labels: Readonly<Record<string, string>>, raw: string): string => labels[raw] ?? raw;

function required(name: string, v: string | undefined): string {
  const t = v?.trim();
  if (!t) throw new ExpectedError(`${name} is required`);
  return t;
}

// ---- add_queue_item ---------------------------------------------------------

// By uuid or exact name, both case-insensitive. Ambiguity is refused, never guessed:
// queue names in the wild differ only by case (`Cencal` / `CENCAL`).
async function resolveQueue(queue: string): Promise<ApexQueue> {
  const wanted = queue.trim().toLowerCase();
  const matches = (await listQueues(PRE_PROD, undefined)).filter(
    (q) => q.id.toLowerCase() === wanted || q.name.toLowerCase() === wanted,
  );
  const [only, ...extra] = matches;
  if (!only) {
    throw new ExpectedError(
      `no queue named or with id '${queue}' in the pre_prod folder — see list_queues`,
    );
  }
  if (extra.length) {
    throw new ExpectedError(
      `'${queue}' matches ${matches.length} queues (${matches.map((q) => q.id).join(", ")}) — pass the queue uuid`,
    );
  }
  return only;
}

// POST one item to a pre-prod queue. The payload passes the test-safety guard first
// (IsApproved forced false; serverURL/queueUrl pinned to the configured pre-prod values;
// <TO-FILL> placeholders rejected). Priority is the queue's default (`args.priority` is
// UiPath-only). Every check that needs no I/O runs before the queue lookup.
export async function addApexQueueItem(args: AddQueueItemArgs): Promise<AddQueueItemResult> {
  assertPreProd(args.env);
  const reference = required("reference", args.reference);
  const apex = resolveApex();
  const guarded = guardQueueItemSafety(args.specificContent, "preProdPost", {
    preProdServerUrl: apex.serverUrlByEnv.pre_prod ?? "",
    queueUrl: apex.queueUrl,
    configSection: "apex",
  });
  const matchValue = matchValueOf(guarded.specificContent);
  const queue = await resolveQueue(required("queue", args.queue));
  const created = await apexRequest(
    PRE_PROD,
    "POST",
    `/queues/${encodeURIComponent(queue.id)}/items`,
    {
      body: { specific_content: guarded.specificContent, reference, match_value: matchValue },
    },
  );
  const status = str(prop(created, "status"));
  return {
    env: args.env,
    queueId: queue.id,
    queueName: queue.name,
    reference,
    itemId: str(prop(created, "id")),
    status: label(APEX_ITEM_LABELS, status),
    forced: guarded.forced,
  };
}

// ---- delete_queue_item ------------------------------------------------------

// Fetch-first: only a never-picked-up ('new') item may go — anything else is execution
// history (and possibly a running job's transaction). The dashboard also offers delete
// for failed/abandoned items; parity with UiPath and evidence preservation keep this strict.
// queueId must be a queue in the pre-prod folder's own listing (client-side folder proof).
export async function deleteApexQueueItem(
  args: DeleteQueueItemArgs,
): Promise<DeleteQueueItemResult> {
  assertPreProd(args.env);
  const itemId = required("itemId", args.itemId);
  const queueId = args.queueId?.trim();
  if (!queueId) {
    throw new ExpectedError(
      "queueId is required for Apex (from list_queue_items / find_order_queue_items output)",
    );
  }
  const wanted = queueId.toLowerCase();
  if (!(await listQueues(PRE_PROD, undefined)).some((q) => q.id.toLowerCase() === wanted)) {
    throw new ExpectedError(
      `queueId '${queueId}' not found in the pre_prod folder — see list_queues; nothing deleted`,
    );
  }
  const item = await getQueueItem(PRE_PROD, undefined, queueId, itemId);
  if (item.status !== "new") {
    throw new ExpectedError(
      `refusing to delete queue item ${itemId}: status is '${label(APEX_ITEM_LABELS, item.status)}' ` +
        "(only 'New' items may be deleted)",
    );
  }
  await apexRequest(
    PRE_PROD,
    "POST",
    `/queues/${encodeURIComponent(queueId)}/items/${encodeURIComponent(itemId)}:delete`,
    { body: {} },
  );
  return { env: args.env, itemId, deleted: true, previousStatus: "New", reference: item.reference };
}

// ---- start_job --------------------------------------------------------------

// Start `jobsCount` jobs for a pre-prod Process. match_value picks the credential the job
// runs under, so it is required (no safe default). Sequential and NEVER retried: a retried
// POST could double-start a real portal login. A mid-loop failure reports the jobs that
// were already started, since they keep running.
export async function startApexJob(args: StartJobArgs): Promise<StartJobResult> {
  assertPreProd(args.env);
  const processId = required("processId", args.processId);
  const matchValue = args.matchValue?.trim();
  if (!matchValue) {
    throw new ExpectedError(
      "matchValue is required for Apex start_job — the credential match value the job runs " +
        "under (for auth-submit portals, the account's automationId; see a queue item's match_value)",
    );
  }
  const wanted = processId.toLowerCase();
  const workflow = (await listWorkflows(PRE_PROD, undefined)).find(
    (w) => w.id.toLowerCase() === wanted,
  );
  if (!workflow) {
    throw new ExpectedError(
      `no Process with id '${processId}' in the pre_prod folder — see list_processes`,
    );
  }
  const folder = await resolveFolderId(PRE_PROD);
  const jobs: StartedJob[] = [];
  for (let i = 0; i < args.jobsCount; i++) {
    try {
      const created = await apexRequest(PRE_PROD, "POST", "/jobs", {
        body: {
          workflow_id: workflow.id,
          folder_id: folder.id,
          match_value: matchValue,
          input_payload: args.input,
          bucket_id: null,
        },
      });
      const id = str(prop(created, "id"));
      jobs.push({
        id,
        state: label(APEX_JOB_LABELS, str(prop(created, "status"))),
        processName: workflow.name,
        link: id ? apexDashboardLink(`/jobs/${id}`) : "",
      });
    } catch (e) {
      if (!jobs.length) throw e;
      throw new ExpectedError(
        `start_job failed on job ${jobs.length + 1} of ${args.jobsCount} (not retried); already started: ` +
          `${jobs.map((j) => j.id).join(", ")} — ${e instanceof Error ? e.message : String(e)}`,
      );
    }
  }
  return { env: args.env, jobs };
}

// ---- stop_job ---------------------------------------------------------------

const STOP_ROUTES: Record<
  StopStrategy,
  { action: string; body: Record<string, unknown>; forbidden: string }
> = {
  SoftStop: {
    action: "cancel",
    body: { force: false },
    forbidden: "this account may lack permission to cancel jobs in the Authorization Test folder",
  },
  Kill: {
    action: "force_cancel",
    body: {},
    forbidden:
      "force_cancel needs the 'jobs:force_cancel' permission, which this account may lack — use SoftStop instead",
  },
};

// Fetch-first: a job already in a terminal state (completed/failed/cancelled/lost/timed_out)
// has nothing left to stop, so it is refused before any write. The job is read scoped to the
// pre-prod folder, and its own folder_id must equal the pre-prod folder's (client-side proof:
// by-id routes are not verified to be folder-scoped server-side).
export async function stopApexJob(args: StopJobArgs): Promise<StopJobResult> {
  assertPreProd(args.env);
  const jobId = required("jobId", args.jobId);
  const job = await getJob(PRE_PROD, undefined, jobId);
  const folder = await resolveFolderId(PRE_PROD);
  if (job.folder_id !== folder.id) {
    throw new ExpectedError(
      `refusing to stop job '${jobId}': it is not in the pre_prod folder ('${folder.name}') — nothing was stopped`,
    );
  }
  if (APEX_TERMINAL_STATUSES.has(job.status)) {
    throw new ExpectedError(
      `refusing to stop job '${jobId}': state is already '${label(APEX_JOB_LABELS, job.status)}' (terminal)`,
    );
  }
  const route = STOP_ROUTES[args.strategy];
  try {
    await apexRequest(PRE_PROD, "POST", `/jobs/${encodeURIComponent(jobId)}/${route.action}`, {
      body: route.body,
    });
  } catch (e) {
    if (e instanceof ApexApiError && e.status === 403) {
      throw new ExpectedError(
        `Apex refused ${args.strategy} for job '${jobId}' (403): ${route.forbidden}`,
      );
    }
    throw e;
  }
  return { env: args.env, jobId, strategy: args.strategy, stopped: true };
}

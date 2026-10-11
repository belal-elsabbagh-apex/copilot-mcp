// Typed Apex endpoint functions over the client chokepoint. Raw row interfaces list
// exactly the live keys (verified 2026-10-11) the mappers read — nothing speculative —
// and filters are limited to the query params verified to filter server-side: Apex
// silently ignores unknown params and returns everything (e.g. `queue_item_id`).
// `folder` is a folder NAME override (undefined = the env's configured folder).

import type { Env } from "../config/config.js";
import { ExpectedError } from "../shared/expected-error.js";
import { apexDownload, apexPages, apexRequest } from "./client.js";
import { ApexApiError } from "./errors.js";

export interface ApexJobRow {
  id: string;
  status: string; // queued|claimed|running|completed|failed|cancelled|cancelling|lost|timed_out|suspended|resumed
  workflow_id: string;
  folder_id: string;
  source: string | null; // sqs | manual | schedule | job
  queue_item_id: string | null; // null on manual jobs
  queue_id: string | null;
  queue_item_reference: string | null;
  workflow_name: string;
  queue_name: string | null;
  credential_name: string | null;
  retry_of_job_id: string | null;
  attempt_number: number;
  worker_id: string | null;
  error_class: string | null; // permanent|transient|missing_credential|needs_human|credential
  workflow_version: string | null;
  callback_status: string | null;
  started_at: string | null; // null = never started (e.g. missing_credential reap)
  completed_at: string | null;
  created_at: string;
  duration_seconds: number | null;
}

// Detail-only keys: the list rows do not carry them.
export interface ApexJobDetail extends ApexJobRow {
  input_payload: Record<string, unknown> | null;
  result_payload: unknown;
  error_message: string | null;
  exit_code: number | null;
  outcome: string | null;
  outcome_label: string | null;
  needs_review: boolean;
  effective_timeout_seconds: number | null;
}

export interface ApexQueue {
  id: string;
  name: string;
  workflow_id: string | null;
  is_active: boolean;
  auto_drain: boolean;
  max_concurrent_jobs: number;
  max_retries: number;
  enforce_unique_reference: boolean;
}

export interface ApexQueueItem {
  id: string;
  queue_id: string;
  reference: string;
  specific_content: Record<string, unknown>;
  match_value: string | null;
  priority: number;
  status: string; // new|in_progress|successful|failed|retried|abandoned|deleted
  exception_type: string | null; // business | application
  retry_count: number;
  parent_item_id: string | null;
  job_id: string | null; // the latest job
  created_at: string;
  ended_at: string | null;
}

export interface ApexLineageRow {
  item_id: string;
  reference: string;
  status: string;
  exception_type: string | null;
  job: { id: string; status: string; error_class: string | null } | null;
}

export interface ApexQueueItemDetail extends ApexQueueItem {
  callback_status: string | null;
  lineage: ApexLineageRow[];
}

export interface ApexWorkflow {
  id: string;
  name: string;
  package_name: string | null;
  is_active: boolean;
  timeout_seconds: number;
  max_attempts: number;
  backoff_strategy: string;
  artifact_policy: string; // always | on_failure | never
  current_version: string | null;
}

export interface ApexSchedule {
  id: string;
  workflow_id: string;
  cron_expression: string;
  timezone: string;
  is_paused: boolean;
}

export interface ApexArtifact {
  id: string;
  filename: string; // "job.log", "trace.zip", "recording.mp4", …
  artifact_type: string; // logs | videos | other
  size_bytes: number;
  download_url: string; // "/api/v1/jobs/{id}/artifacts/{artifact_id}" — Bearer required
}

// ONLY these filter (queue_item_id / search / queue_item_reference are ignored by /jobs).
// `status` is the wire value (queued, failed, …), not the dashboard label.
export interface JobFilters {
  status?: string | undefined;
  queueId?: string | undefined;
  workflowId?: string | undefined;
  createdAfter?: string | undefined;
  reference?: string | undefined; // exact; this is how every attempt of one queue item is enumerated
}

// `status` is the wire value (new, in_progress, …); `search` is fuzzy over the content.
export interface ItemFilters {
  status?: string | undefined;
  reference?: string | undefined;
  search?: string | undefined;
}

const JOB_QUERY: Record<keyof JobFilters, string> = {
  status: "status",
  queueId: "queue_id",
  workflowId: "workflow_id",
  createdAfter: "created_after",
  reference: "reference",
};
const ITEM_QUERY: Record<keyof ItemFilters, string> = {
  status: "status",
  reference: "reference",
  search: "search",
};

function toQuery<F extends object>(f: F, keys: Record<keyof F, string>): Record<string, string> {
  const query: Record<string, string> = {};
  for (const k of Object.keys(keys) as (keyof F)[]) {
    const v = f[k];
    if (typeof v === "string") query[keys[k]] = v;
  }
  return query;
}

// The one 404 a caller may fold into "no such record"; any other status propagates.
export const isNotFound = (e: unknown): boolean => e instanceof ApexApiError && e.status === 404;

const seg = encodeURIComponent;
const BIG = 1000; // directory-sized lists (queues, workflows, schedules) per folder

// Newest first: Apex lists default to created_at desc, pinned here so a default change can't flip `top`.
export const listJobs = (
  env: Env,
  folder: string | undefined,
  f: JobFilters,
  max: number,
): Promise<ApexJobRow[]> =>
  apexPages<ApexJobRow>(
    env,
    "/jobs",
    { sort_by: "created_at", sort_dir: "desc", ...toQuery(f, JOB_QUERY) },
    max,
    folder,
  );

export async function getJob(
  env: Env,
  folder: string | undefined,
  id: string,
): Promise<ApexJobDetail> {
  return (await apexRequest(env, "GET", `/jobs/${seg(id)}`, { folder })) as ApexJobDetail;
}

export const listWorkflows = (env: Env, folder: string | undefined): Promise<ApexWorkflow[]> =>
  apexPages<ApexWorkflow>(env, "/workflows", {}, BIG, folder);

export const listQueues = (env: Env, folder: string | undefined): Promise<ApexQueue[]> =>
  apexPages<ApexQueue>(env, "/queues", {}, BIG, folder);

export async function getQueue(
  env: Env,
  folder: string | undefined,
  queueId: string,
): Promise<ApexQueue> {
  return (await apexRequest(env, "GET", `/queues/${seg(queueId)}`, { folder })) as ApexQueue;
}

// Items list newest first by default (live-verified); the server has no verified sort param.
export const listQueueItems = (
  env: Env,
  folder: string | undefined,
  queueId: string,
  f: ItemFilters,
  max: number,
): Promise<ApexQueueItem[]> =>
  apexPages<ApexQueueItem>(
    env,
    `/queues/${seg(queueId)}/items`,
    toQuery(f, ITEM_QUERY),
    max,
    folder,
  );

export async function getQueueItem(
  env: Env,
  folder: string | undefined,
  queueId: string,
  itemId: string,
): Promise<ApexQueueItemDetail> {
  const path = `/queues/${seg(queueId)}/items/${seg(itemId)}`;
  return (await apexRequest(env, "GET", path, { folder })) as ApexQueueItemDetail;
}

export const listSchedules = (env: Env, folder: string | undefined): Promise<ApexSchedule[]> =>
  apexPages<ApexSchedule>(env, "/schedules", {}, BIG, folder);

// Bare-array route; the listing needs the folder header outside the default folder.
export async function listArtifacts(
  env: Env,
  folder: string | undefined,
  jobId: string,
): Promise<ApexArtifact[]> {
  const rows = await apexRequest(env, "GET", `/jobs/${seg(jobId)}/artifacts`, { folder });
  if (!Array.isArray(rows)) throw new ExpectedError("Apex artifact listing was not a list");
  return rows as ApexArtifact[];
}

// Bearer only (no folder header); `range` is a Range header value, e.g. "bytes=-1048576".
export const downloadArtifact = (env: Env, a: ApexArtifact, range?: string) =>
  apexDownload(env, a.download_url, range ? { range } : undefined);

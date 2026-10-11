// Synthetic, PHI-free Apex row builders for tests (shapes mirror the live keys in api.ts).

import type {
  ApexArtifact,
  ApexJobDetail,
  ApexJobRow,
  ApexQueue,
  ApexQueueItem,
  ApexQueueItemDetail,
  ApexSchedule,
  ApexWorkflow,
} from "./api.js";

export const JOB_ID = "aaaaaaaa-0000-4000-8000-000000000001";
export const WORKFLOW_ID = "bbbbbbbb-0000-4000-8000-000000000001";
export const QUEUE_ID = "cccccccc-0000-4000-8000-000000000001";
export const ITEM_ID = "dddddddd-0000-4000-8000-000000000001";

export const jobRow = (o: Partial<ApexJobRow> = {}): ApexJobRow => ({
  id: JOB_ID,
  status: "completed",
  workflow_id: WORKFLOW_ID,
  folder_id: "22222222-2222-4222-8222-222222222222",
  source: "sqs",
  queue_item_id: ITEM_ID,
  queue_id: QUEUE_ID,
  queue_item_reference: "1000000001",
  workflow_name: "demo-auth-submit",
  queue_name: "DEMO auth submit queue",
  credential_name: "demo-cred",
  retry_of_job_id: null,
  attempt_number: 1,
  worker_id: "eeeeeeee-0000-4000-8000-000000000001",
  error_class: null,
  workflow_version: "1.0.2",
  callback_status: "sent",
  started_at: "2026-10-08T00:39:51.248748Z",
  completed_at: "2026-10-08T00:44:14.649417Z",
  created_at: "2026-10-08T00:39:49.044176Z",
  duration_seconds: 263.400669,
  ...o,
});

export const jobDetail = (o: Partial<ApexJobDetail> = {}): ApexJobDetail => ({
  ...jobRow(),
  input_payload: {
    orderUid: "order-1",
    token: "synthetic-token",
    callbackContext: { orderUid: "order-1" },
    retryCount: 0,
  },
  result_payload: { result: "approved" },
  error_message: null,
  exit_code: 0,
  outcome: null,
  outcome_label: null,
  needs_review: false,
  effective_timeout_seconds: 900,
  ...o,
});

export const queueRow = (o: Partial<ApexQueue> = {}): ApexQueue => ({
  id: QUEUE_ID,
  name: "DEMO auth submit queue",
  workflow_id: WORKFLOW_ID,
  is_active: true,
  auto_drain: true,
  max_concurrent_jobs: 1,
  max_retries: 0,
  enforce_unique_reference: false,
  ...o,
});

export const itemRow = (o: Partial<ApexQueueItem> = {}): ApexQueueItem => ({
  id: ITEM_ID,
  queue_id: QUEUE_ID,
  reference: "1000000001",
  specific_content: {
    orderUid: "order-1",
    token: "synthetic-token",
    IsApproved: true,
    MemberFirstName: "Jane",
    MemberLastName: "Doe",
  },
  match_value: "auto-1",
  priority: 20,
  status: "failed",
  exception_type: "business",
  retry_count: 0,
  parent_item_id: null,
  job_id: JOB_ID,
  created_at: "2026-10-08T00:29:26.164862Z",
  ended_at: "2026-10-08T00:44:14.655419Z",
  ...o,
});

export const itemDetail = (o: Partial<ApexQueueItemDetail> = {}): ApexQueueItemDetail => ({
  ...itemRow(),
  callback_status: "sent",
  lineage: [],
  ...o,
});

export const workflowRow = (o: Partial<ApexWorkflow> = {}): ApexWorkflow => ({
  id: WORKFLOW_ID,
  name: "demo-auth-submit",
  package_name: "demo-auth-submit",
  is_active: true,
  timeout_seconds: 900,
  max_attempts: 3,
  backoff_strategy: "exponential",
  artifact_policy: "on_failure",
  current_version: "1.0.2",
  ...o,
});

export const scheduleRow = (o: Partial<ApexSchedule> = {}): ApexSchedule => ({
  id: "ffffffff-0000-4000-8000-000000000001",
  workflow_id: WORKFLOW_ID,
  cron_expression: "0 6 * * *",
  timezone: "UTC",
  is_paused: false,
  ...o,
});

export const artifactRow = (o: Partial<ApexArtifact> = {}): ApexArtifact => {
  const id = o.id ?? "99999999-0000-4000-8000-000000000001";
  return {
    id,
    filename: "job.log",
    artifact_type: "logs",
    size_bytes: 100,
    download_url: `/api/v1/jobs/${JOB_ID}/artifacts/${id}`,
    ...o,
  };
};

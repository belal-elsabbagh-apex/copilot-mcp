// The one interface every orchestrator backend implements. server.ts dispatches each
// orchestrator tool through backendFor(orchestrator) (registry.ts) and wraps the result
// as { orchestrator, env, ...result }, so backends never shape tool envelopes themselves.

import type { Env } from "../config/config.js";
import type { StepProgress } from "../shared/util.js";
import type { FaultedJobIssue } from "./faults.js";
import type { JobLogDigest } from "./log-digest.js";
import type {
  JobCore,
  Orchestrator,
  PortalRef,
  ProcessCore,
  QueueCore,
  QueueItemCore,
  QueueItemStatus,
  TriggerCore,
} from "./types.js";

export interface ListJobsArgs {
  env: Env;
  folder?: string | undefined;
  since?: string | undefined;
  top: number;
  processName?: string | undefined;
}
export interface ListJobsResult {
  folder: string;
  jobs: JobCore[];
}

export type LogsUnavailableReason = "running" | "never-started" | "artifact-policy";
export interface LogsUnavailable {
  reason: LogsUnavailableReason;
  detail: string;
}

// batch=false: a lookup failure of the single id throws (fails the tool call);
// batch=true: it lands on that entry's `error` and the rest still return.
export interface GetJobsArgs {
  env: Env;
  jobIds: string[];
  batch: boolean;
  folder?: string | undefined;
  includeLogDigest: boolean;
}
export interface JobEntry {
  id: string;
  found: boolean;
  job?: JobCore;
  logDigest?: JobLogDigest; // backends return a subtype (UiPath +transactionOutcome, Apex +run)
  logsUnavailable?: LogsUnavailable;
  logDigestError?: string;
  error?: string;
}
export interface GetJobsResult {
  folder: string;
  entries: JobEntry[];
}

export interface LogFilter {
  minLevel?: "warn" | "error" | undefined;
  contains?: string | undefined;
  onlyFailures: boolean;
  tail?: number | undefined;
}
export interface GetJobLogsArgs {
  env: Env;
  jobIds: string[];
  batch: boolean;
  folder?: string | undefined;
  filter: LogFilter;
  fullMessages: boolean;
  includeRawFields: boolean;
  includeVideo: boolean;
}
export interface LogRow {
  Level: string;
  Message: string;
  TimeStamp: string;
  raw?: object | null; // only when includeRawFields: UiPath RawMessage fields | Apex NDJSON `fields`
}
export type VideoRef =
  | { kind: "url"; url: string }
  | { kind: "artifact"; artifact: string; sizeBytes: number; fetchWith: "get_job_artifacts" };
export interface JobLogsEntry {
  jobId: string;
  logs: LogRow[];
  returned: number;
  totalMatching?: number;
  truncated: boolean;
  logsUnavailable?: LogsUnavailable;
  error?: string;
  video?: VideoRef;
  videoError?: string;
}
export interface GetJobLogsResult {
  folder: string;
  entries: JobLogsEntry[];
}

export interface ListQueuesArgs {
  env: Env;
  folder?: string | undefined;
  nameContains?: string | undefined;
  top: number;
}

export type QueueKind = "submit" | "sync";
// `queue` is a portal key/alias (e.g. "PMG") or the backend's own queue id.
export interface ListQueueItemsArgs {
  env: Env;
  queue: string;
  queueKind: QueueKind;
  status?: QueueItemStatus | undefined;
  top: number;
}
export interface ListQueueItemsResult {
  queueId: string;
  queueName: string;
  items: QueueItemCore[];
}

export interface FindOrderArgs {
  env: Env;
  orderUid: string;
  folder?: string | undefined;
  since?: string | undefined;
  top: number;
  includeSpecificContent: boolean;
}
export interface FindOrderResult {
  folder: string;
  searchMode: string;
  scanned: number;
  items: QueueItemCore[]; // newest first; specificContent token value "[redacted]"
  notes: string[];
  searchError?: string;
}

// UiPath: url, or itemId (+ env, optional folderId). Apex: env + queueId + itemId.
export interface PullQueueItemArgs {
  env?: Env | undefined;
  url?: string | undefined;
  queueId?: string | undefined;
  itemId?: string | undefined;
  folderId?: string | undefined;
}
export interface PullQueueItemResult {
  env: Env;
  item: QueueItemCore; // specificContent present, IsApproved forced false
  portal: PortalRef;
  isApprovedForced: true;
  suggestedFilename: string;
  queueNameError?: string;
}

export interface ListProcessesArgs {
  env: Env;
  folder?: string | undefined;
  nameContains?: string | undefined;
  top: number;
}
export interface ListTriggersArgs {
  env: Env;
  folder?: string | undefined;
  queueId?: string | undefined;
  top: number;
}

export interface FaultIssueArgs {
  env: Env;
  jobId: string;
  folder?: string | undefined;
  repo?: string | undefined;
  labels?: string[] | undefined;
}
export type FaultedJobIssueResult = FaultedJobIssue & { found: boolean; logsError?: string };

// `content` is the canonical native-typed SpecificContent from orchestrator/queue-item.ts,
// WITHOUT serverURL/queueUrl; the backend fills those from its own config and serializes.
export interface QueueItemPayloadArgs {
  env: Env;
  queueName: string;
  orderUid: string;
  content: Record<string, unknown>;
  beBase?: string | undefined; // the Copilot BE the order was read from; UiPath serverURL fallback
}
export interface QueueItemPayload {
  payload: unknown;
  notes: string[];
  meta: Record<string, unknown>;
}

export type QueueItemPriority = "Low" | "Normal" | "High";
export interface AddQueueItemArgs {
  env: Env;
  queue: string; // UiPath: exact queue Name; Apex: queue name or uuid
  reference: string;
  specificContent: Record<string, unknown>;
  priority: QueueItemPriority; // UiPath only; Apex uses the queue's default
}
export interface AddQueueItemResult {
  env: Env;
  queueId: string;
  queueName: string;
  reference: string;
  itemId: string;
  status: string;
  forced: string[];
}
export interface DeleteQueueItemArgs {
  env: Env;
  itemId: string;
  queueId?: string | undefined; // required by Apex
}
export interface DeleteQueueItemResult {
  env: Env;
  itemId: string;
  deleted: true;
  previousStatus: string;
  reference: string;
}
export interface StartJobArgs {
  env: Env;
  processId: string;
  input: Record<string, unknown>;
  jobsCount: number;
  matchValue?: string | undefined; // required by Apex
}
export interface StartedJob {
  id: string;
  state: string;
  processName: string;
  link: string;
}
export interface StartJobResult {
  env: Env;
  jobs: StartedJob[];
}
export type StopStrategy = "SoftStop" | "Kill";
export interface StopJobArgs {
  env: Env;
  jobId: string;
  strategy: StopStrategy;
}
export interface StopJobResult {
  env: Env;
  jobId: string;
  strategy: StopStrategy;
  stopped: true;
}

export interface OrchestratorBackend {
  readonly name: Orchestrator;
  listJobs(args: ListJobsArgs): Promise<ListJobsResult>;
  getJobs(args: GetJobsArgs, onProgress?: StepProgress): Promise<GetJobsResult>;
  getJobLogs(args: GetJobLogsArgs, onProgress?: StepProgress): Promise<GetJobLogsResult>;
  listQueues(args: ListQueuesArgs): Promise<QueueCore[]>;
  listQueueItems(args: ListQueueItemsArgs): Promise<ListQueueItemsResult>;
  findOrderQueueItems(args: FindOrderArgs): Promise<FindOrderResult>;
  pullQueueItem(args: PullQueueItemArgs): Promise<PullQueueItemResult>;
  listProcesses(args: ListProcessesArgs): Promise<ProcessCore[]>;
  listTriggers(args: ListTriggersArgs): Promise<TriggerCore[]>;
  buildFaultedJobIssue(args: FaultIssueArgs): Promise<FaultedJobIssueResult>;
  buildQueueItemPayload(args: QueueItemPayloadArgs): QueueItemPayload;
  addQueueItem(args: AddQueueItemArgs): Promise<AddQueueItemResult>;
  deleteQueueItem(args: DeleteQueueItemArgs): Promise<DeleteQueueItemResult>;
  startJob(args: StartJobArgs): Promise<StartJobResult>;
  stopJob(args: StopJobArgs): Promise<StopJobResult>;
}

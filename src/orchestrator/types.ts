// Backend-neutral orchestrator contract: the normalized shapes every orchestrator tool
// returns, whichever backend served the call. Backend-specific fields live under each
// core's `details`, never at the top level, so agents read one vocabulary across backends.

export const ORCHESTRATORS = ["uipath", "apex"] as const;
export type Orchestrator = (typeof ORCHESTRATORS)[number];

// One log row as the shared digest/filter code reads it. Backends extend it
// (UiPath adds RawMessage, Apex adds fields); shared code reads only these three.
export interface JobLog {
  Level: string;
  Message: string;
  TimeStamp: string;
}

// `class` is the backend's failure classification: UiPath = the exception type parsed
// from the headline error; Apex = the Process outcome, else error_class. null fields
// mean "not available from this read", never "no fault".
export interface JobFault {
  message: string | null;
  signature: string | null;
  class: string | null;
}

// One execution attempt of a transaction, as known from the queue-item read alone.
// state/neverStarted are null when the backend can't tell without fetching the job.
export interface JobAttempt {
  orchestrator: Orchestrator;
  id: string;
  state: string | null;
  neverStarted: boolean | null;
}

export interface JobCore<D extends object = object> {
  orchestrator: Orchestrator;
  id: string; // UiPath job Key (GUID) | Apex job uuid
  state: string; // dashboard label (UiPath State vocabulary)
  rawState: string; // the backend's own status string, verbatim
  terminal: boolean;
  process: { id: string | null; name: string; version: string | null };
  queueItem: { queueId: string; itemId: string; reference: string } | null;
  createdAt: string;
  startedAt: string | null;
  endedAt: string | null;
  durationMs: number | null;
  attempt: number | null;
  retryOfJobId: string | null;
  fault: JobFault | null;
  link: string; // dashboard deep link ("" when unknown)
  details: D;
}

// Queue-item statuses in UiPath's spelling; Apex statuses are mapped onto these.
export const QUEUE_ITEM_STATUSES = [
  "New",
  "InProgress",
  "Successful",
  "Failed",
  "Retried",
  "Abandoned",
  "Deleted",
] as const;
export type QueueItemStatus = (typeof QUEUE_ITEM_STATUSES)[number];

export interface QueueItemCore<D extends object = object> {
  orchestrator: Orchestrator;
  queueId: string;
  queueName: string;
  itemId: string;
  reference: string;
  orderUid: string | null;
  status: string; // QueueItemStatus spelling when the backend value maps, else verbatim
  rawStatus: string;
  exceptionType: "business" | "application" | null;
  createdAt: string;
  endedAt: string | null;
  jobs: JobAttempt[]; // newest first; at most one on UiPath
  specificContent: Record<string, unknown> | null; // null = not part of this projection
  details: D;
}

export interface QueueCore<D extends object = object> {
  orchestrator: Orchestrator;
  id: string;
  name: string;
  processId: string | null;
  active: boolean;
  details: D;
}

export interface ProcessCore<D extends object = object> {
  orchestrator: Orchestrator;
  id: string; // what start_job takes as processId (UiPath release Key | Apex workflow uuid)
  name: string;
  package: string | null;
  version: string | null;
  active: boolean;
  details: D;
}

export interface TriggerCore<D extends object = object> {
  orchestrator: Orchestrator;
  id: string;
  name: string;
  kind: "queue" | "time";
  processId: string | null;
  queueId: string | null;
  enabled: boolean;
  cron: string | null;
  details: D;
}

// A portal reference exposed on a pull result. matched=false carries empty sentinels.
export interface PortalRef {
  matched: boolean;
  key: string;
  portalDir: string;
  buildArtifact: string;
  family: string;
}

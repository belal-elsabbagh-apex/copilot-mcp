// UiPath Orchestrator OData client: job search/detail/confirm, robot logs, video,
// deep links, and per-env folder resolution. Ported from copilot-doctor src/api.ts
// (chrome background-proxy removed — direct fetch with the bearer from config.ts).

import { type Env, getUipath } from "../config/config.js";
import { isFailureLog } from "../copilot/output-analysis.js";
import { outputMatchesOrder } from "../copilot/output-schema.js";
import { folderIdFor } from "../mcp/reference.js";
import { chunk, isRecord } from "../shared/util.js";
import { resolveBearerToken } from "./auth.js";

export type { Env };

export interface UiPathJob {
  Id?: string;
  Key?: string;
  State?: string;
  ReleaseName?: string;
  CreationTime?: string;
  EndTime?: string;
  OutputArguments?: string;
  InputArguments?: string;
  RobotName?: string; // from $expand=Robot($select=Name) — the robot that ran/is running the job
  ProcessVersion?: string; // from $expand=Release($select=ProcessVersion) — the pinned package version
}

const numField = (v: unknown, k: string): number => {
  const r = isRecord(v) ? v[k] : undefined;
  return typeof r === "number" ? r : 0;
};
const strField = (v: unknown, k: string): string => {
  const r = isRecord(v) ? v[k] : undefined;
  return typeof r === "string" ? r : "";
};
const recField = (v: unknown, k: string): Record<string, unknown> => {
  const r = isRecord(v) ? v[k] : undefined;
  return isRecord(r) ? r : {};
};

// Flatten the $expand=Robot($select=Name),Release($select=ProcessVersion) nav objects
// onto the flat UiPathJob shape; every other field passes through unchanged.
export const toUiPathJob = (raw: unknown): UiPathJob => {
  const robotName = strField(recField(raw, "Robot"), "Name");
  const processVersion = strField(recField(raw, "Release"), "ProcessVersion");
  return {
    ...(raw as UiPathJob),
    ...(robotName ? { RobotName: robotName } : {}),
    ...(processVersion ? { ProcessVersion: processVersion } : {}),
  };
};

// Nav properties folded into every Jobs query below — confirmed via live $metadata
// that Robot/Release are valid Jobs nav properties; nested $select keeps the
// expanded payload down to just the two fields we flatten in toUiPathJob.
const JOB_EXPAND = "Robot($select=Name),Release($select=ProcessVersion)";

export interface JobLog {
  Level: string;
  Message: string;
  TimeStamp: string;
}

// orchestratorUrl is like https://cloud.uipath.com/{org}/{tenant}/orchestrator_
// — already org/tenant-scoped, so OData paths append directly.
const base = (): string => getUipath().orchestratorUrl.replace(/\/$/, "");

const orgTenant = (): { org: string; tenant: string } => {
  const m = base().match(/cloud\.uipath\.com\/([^/]+)\/([^/]+)\/orchestrator_/);
  return { org: m?.[1] ?? "", tenant: m?.[2] ?? "" };
};

// Known UiPath folders by env (same org/tenant) — used when the config doesn't
// override them via folderPathByEnv. Override per call with an explicit `folder`.
const FOLDER_DEFAULTS: Record<Env, string> = {
  prod: "Authorization",
  pre_prod: "Authorization Dev Clone",
};

// Resolve which Orchestrator folder to scope the query to:
//   explicit folder  >  config.folderPathByEnv[env]  >  known default  >  legacy folderPath
export function resolveFolder(env?: Env, folder?: string): string | undefined {
  if (folder) return folder;
  const cfg = getUipath();
  if (env) return cfg.folderPathByEnv?.[env] ?? FOLDER_DEFAULTS[env];
  return cfg.folderPath;
}

const odataValues = <T>(data: unknown): T[] =>
  isRecord(data) && Array.isArray(data["value"]) ? (data["value"] as T[]) : [];

type HttpMethod = "GET" | "POST" | "DELETE";

interface UipathRequestOpts {
  params?: Record<string, string>; // become the OData querystring
  body?: unknown; // JSON.stringify'd; adds Content-Type: application/json
  folder?: string | undefined; // X-UIPATH-FolderPath (falls back to the legacy config folderPath)
  orgUnitId?: string | undefined; // X-UIPATH-OrganizationUnitId (numeric folder id)
}

// One Orchestrator OData/REST call. `folder` scopes the request to a UiPath folder;
// `orgUnitId` is sent as X-UIPATH-OrganizationUnitId — the QueueItems/QueueDefinitions
// endpoints are addressed by org-unit id, while Jobs accept the folder-path header.
// Sending both is harmless. Returns parsed JSON (null for an empty body, e.g. a
// DELETE 204), or throws with method + status + body snippet on a non-2xx.
async function uipathRequest(
  method: HttpMethod,
  path: string,
  opts: UipathRequestOpts = {},
): Promise<unknown> {
  const cfg = getUipath();
  const qs = new URLSearchParams(opts.params ?? {}).toString();
  const url = base() + path + (qs ? `?${qs}` : "");
  const folderPath = opts.folder ?? cfg.folderPath;
  const hasBody = opts.body !== undefined;
  const token = await resolveBearerToken(cfg);
  const res = await fetch(url, {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      Accept: "application/json",
      // Folder scoping — copilot-doctor uses the folder-path header. Jobs live in
      // a per-env folder (prod "Authorization" vs dev "Authorization Dev Clone").
      ...(folderPath ? { "X-UIPATH-FolderPath": folderPath } : {}),
      ...(opts.orgUnitId ? { "X-UIPATH-OrganizationUnitId": opts.orgUnitId } : {}),
      ...(hasBody ? { "Content-Type": "application/json" } : {}),
    },
    ...(hasBody ? { body: JSON.stringify(opts.body) } : {}),
  });
  const text = await res.text();
  if (res.status >= 400) {
    throw new Error(`UiPath ${method} ${path} -> ${res.status}: ${text.slice(0, 300)}`);
  }
  if (!text) return null;
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

async function uipathGet(
  path: string,
  params: Record<string, string> = {},
  folder?: string,
  orgUnitId = "",
): Promise<unknown> {
  return uipathRequest("GET", path, { params, folder, orgUnitId });
}

// Orchestrator writes always carry an explicit FolderScope — unlike the legacy GET
// helpers there is no folder fallback, so a write can never land in an implicit folder.
export async function uipathPost(
  path: string,
  body: unknown,
  scope: FolderScope,
): Promise<unknown> {
  return uipathRequest("POST", path, {
    body,
    folder: scope.folderPath,
    orgUnitId: scope.orgUnitId,
  });
}

export async function uipathDelete(path: string, scope: FolderScope): Promise<unknown> {
  return uipathRequest("DELETE", path, { folder: scope.folderPath, orgUnitId: scope.orgUnitId });
}

// All jobs whose OutputArguments contains `orderId`, newest first (capped at
// `top`). The Jobs collection nulls OutputArguments, so the match is a
// server-side OData `contains`; per-job OutputArguments is loaded on demand.
export async function searchJobsByOrderId(
  orderId: string,
  since?: string,
  top = 50,
  folder?: string,
): Promise<UiPathJob[]> {
  const needle = orderId.trim();
  if (!needle) return [];
  const escaped = needle.replace(/'/g, "''"); // OData single-quote escaping
  let filter = `contains(OutputArguments, '${escaped}')`;
  if (since) filter = `CreationTime gt ${new Date(since).toISOString()} and ${filter}`;
  const data = await uipathGet(
    "/odata/Jobs",
    {
      $filter: filter,
      $orderby: "CreationTime desc",
      $top: String(top),
      $select: "Id,Key,State,ReleaseName,CreationTime",
      $expand: JOB_EXPAND,
    },
    folder,
  );
  return odataValues<unknown>(data).map(toUiPathJob);
}

// Recent jobs in a folder, newest first — bounded by `since`/`top`, with NO
// OutputArguments filter. Used as the reliable fallback when the `contains`
// substring scan is rejected (UiPath intermittently 400/500s on OutputArguments
// filtering). Filtering on CreationTime is indexed and supported.
export async function listRecentJobs(
  since: string | undefined,
  top: number,
  folder?: string,
  processName?: string,
): Promise<UiPathJob[]> {
  const params: Record<string, string> = {
    $orderby: "CreationTime desc",
    $top: String(top),
    $select: "Id,Key,State,ReleaseName,CreationTime,EndTime",
    $expand: JOB_EXPAND,
  };
  const clauses: string[] = [];
  if (since) clauses.push(`CreationTime gt ${new Date(since).toISOString()}`);
  const needle = processName?.trim();
  if (needle) clauses.push(`contains(ReleaseName, '${needle.replace(/'/g, "''")}')`);
  if (clauses.length) params["$filter"] = clauses.join(" and ");
  return odataValues<unknown>(await uipathGet("/odata/Jobs", params, folder)).map(toUiPathJob);
}

// Single job detail — the single-job endpoint returns OutputArguments where the
// collection omits it (confirmed: the collection nulls it even with an explicit
// $select, regardless of folder/permissions — a platform limitation, not a bug here).
async function fetchJobDetailsById(jobId: string, folder?: string): Promise<UiPathJob | null> {
  if (!jobId) return null;
  try {
    return toUiPathJob(
      await uipathGet(
        `/odata/Jobs(${jobId})`,
        {
          $select: "Id,Key,State,ReleaseName,CreationTime,EndTime,OutputArguments,InputArguments",
          $expand: JOB_EXPAND,
        },
        folder,
      ),
    );
  } catch {
    return null;
  }
}

// Single job by its GUID Key (the collection is keyed by numeric Id, so a Key
// lookup needs an OData $filter). Returns the job incl. OutputArguments, or null
// if no job matches. Used by build_faulted_job_issue, which is given only a Key.
export async function fetchJobByKey(jobKey: string, folder?: string): Promise<UiPathJob | null> {
  const key = jobKey.trim();
  if (!key) return null;
  try {
    const data = await uipathGet(
      "/odata/Jobs",
      {
        $filter: `Key eq ${key}`,
        $top: "1",
        $select: "Id,Key,State,ReleaseName,CreationTime,EndTime,OutputArguments,InputArguments",
        $expand: JOB_EXPAND,
      },
      folder,
    );
    const job = odataValues<unknown>(data)[0];
    return job !== undefined ? toUiPathJob(job) : null;
  } catch {
    return null;
  }
}

// Fetch several jobs by GUID Key in one MCP round-trip. Each key still costs its
// own HTTP call — confirmed live that the Jobs collection endpoint DOES support
// `Key in (...)`/`or` correctly, but only the single-entity endpoint ever returns
// OutputArguments (collection nulls it regardless of $select), so a collection
// batch call can't replace this when detail is needed. Bounded concurrency (chunks
// of 10, matching confirmJobsForOrder) keeps a large batch from hammering Orchestrator.
export async function fetchJobsForKeys(
  jobKeys: string[],
  folder?: string,
): Promise<Record<string, UiPathJob | null>> {
  const out: Record<string, UiPathJob | null> = {};
  for (const batch of chunk(jobKeys, 10)) {
    const results = await Promise.all(batch.map((key) => fetchJobByKey(key, folder)));
    batch.forEach((key, i) => {
      out[key] = results[i] ?? null;
    });
  }
  return out;
}

// Narrow search candidates to jobs whose normalized output truly belongs to
// `orderId` (the `contains` filter can match incidental substrings). Loads each
// candidate's OutputArguments in batches of 10.
export async function confirmJobsForOrder(
  jobs: UiPathJob[],
  orderId: string,
  folder?: string,
): Promise<UiPathJob[]> {
  const confirmed: UiPathJob[] = [];
  for (const batch of chunk(jobs, 10)) {
    const details = await Promise.allSettled(
      batch.map((job) => fetchJobDetailsById(job.Id ?? "", folder)),
    );
    for (const result of details) {
      const full = result.status === "fulfilled" ? result.value : null;
      if (!full?.OutputArguments) continue;
      try {
        if (outputMatchesOrder(JSON.parse(full.OutputArguments), orderId)) confirmed.push(full);
      } catch {
        // Unparseable OutputArguments — not a confirmable match.
      }
    }
  }
  return confirmed;
}

// Optional narrowing for job-log fetches. All fields default off — an empty
// filter returns the full (capped 500) log set, oldest first, as before.
export interface JobLogFilter {
  minLevel?: "warn" | "error"; // at-or-above; pushed into the OData $filter
  contains?: string; // server-side substring match on Message (case-sensitive)
  onlyFailures?: boolean; // semantic post-filter — see isFailureLog
  tail?: number; // keep only the last N logs after all other filters
}

const LOG_LEVELS_AT_LEAST: Record<"warn" | "error", string[]> = {
  warn: ["Warn", "Error", "Fatal"],
  error: ["Error", "Fatal"],
};

// OData query for a job's RobotLogs under `filter`. Level/contains narrow
// server-side so the 500-row cap spends its budget on matching rows. `tail`
// flips the fetch to newest-first so the window covers the END of a >500-row
// job; refineJobLogs restores oldest-first. With onlyFailures the tail cut
// happens client-side (after the semantic filter), so keep $top at 500.
export function jobLogQueryParams(
  jobKey: string,
  filter: JobLogFilter = {},
): Record<string, string> {
  const clauses = [`JobKey eq ${jobKey}`];
  if (filter.minLevel) {
    const ors = LOG_LEVELS_AT_LEAST[filter.minLevel].map((l) => `Level eq '${l}'`).join(" or ");
    clauses.push(`(${ors})`);
  }
  if (filter.contains) {
    clauses.push(`contains(Message, '${filter.contains.replace(/'/g, "''")}')`); // OData single-quote escaping
  }
  const newestFirst = filter.tail !== undefined;
  const top = filter.tail !== undefined && !filter.onlyFailures ? Math.min(filter.tail, 500) : 500;
  return {
    $filter: clauses.join(" and "),
    $orderby: `TimeStamp ${newestFirst ? "desc" : "asc"}`,
    $top: String(top),
    $select: "Level,Message,TimeStamp",
    $count: "true",
  };
}

// Client-side refinement matching jobLogQueryParams: restore oldest-first order
// (a tail fetch comes back newest-first), apply the semantic failure filter,
// then keep the last `tail` rows.
export function refineJobLogs(logs: JobLog[], filter: JobLogFilter = {}): JobLog[] {
  let out = filter.tail !== undefined ? [...logs].reverse() : logs;
  if (filter.onlyFailures) out = out.filter(isFailureLog);
  if (filter.tail !== undefined) out = out.slice(-filter.tail);
  return out;
}

export interface JobLogResult {
  logs: JobLog[];
  // Rows matching the server-side filters (@odata.count), before the 500 cap
  // and before onlyFailures/tail. null when Orchestrator omits the count.
  totalMatching: number | null;
}

// Robot execution logs for a single job, oldest first. RobotLogs is keyed by the
// job's GUID `Key` (not its numeric Id).
export async function fetchFilteredJobLogs(
  jobKey: string,
  folder?: string,
  filter: JobLogFilter = {},
): Promise<JobLogResult> {
  if (!jobKey) return { logs: [], totalMatching: null };
  try {
    const data = await uipathGet("/odata/RobotLogs", jobLogQueryParams(jobKey, filter), folder);
    const count = isRecord(data) ? data["@odata.count"] : undefined;
    return {
      logs: refineJobLogs(odataValues<JobLog>(data), filter),
      totalMatching: typeof count === "number" ? count : null,
    };
  } catch {
    return { logs: [], totalMatching: null };
  }
}

export async function fetchJobLogs(jobKey: string, folder?: string): Promise<JobLog[]> {
  return (await fetchFilteredJobLogs(jobKey, folder)).logs;
}

// Fetch logs for several jobs in one MCP round-trip, same `filter` applied to
// each. RobotLogs has NO way to combine multiple JobKeys server-side — confirmed
// live that both `JobKey in (...)` and `JobKey eq X or JobKey eq Y` silently
// return wrong/empty results rather than erroring, so one HTTP call per key is a
// hard platform limit. What this collapses is MCP round-trips, not HTTP calls:
// bounded concurrency (chunks of 10) instead of one tool call per job.
export async function fetchJobLogsForKeys(
  jobKeys: string[],
  folder?: string,
  filter: JobLogFilter = {},
): Promise<Record<string, JobLogResult>> {
  const out: Record<string, JobLogResult> = {};
  for (const batch of chunk(jobKeys, 10)) {
    const results = await Promise.all(
      batch.map((key) => fetchFilteredJobLogs(key, folder, filter)),
    );
    batch.forEach((key, i) => {
      out[key] = results[i] ?? { logs: [], totalMatching: null };
    });
  }
  return out;
}

// Video recording uri for a job (the entry whose uri points at recording.webm).
export async function fetchJobVideoUrl(jobKey: string, folder?: string): Promise<string> {
  if (!jobKey) return "";
  try {
    const data = await uipathGet(`/api/VideoRecording/jobs/${jobKey}/read`, {}, folder);
    if (Array.isArray(data)) {
      for (const entry of data) {
        if (
          isRecord(entry) &&
          typeof entry["uri"] === "string" &&
          entry["uri"].includes("recording.webm")
        ) {
          return entry["uri"];
        }
      }
    }
    return "";
  } catch {
    return "";
  }
}

export const jobDeepLink = (jobKey: string): string => {
  const { org, tenant } = orgTenant();
  if (!(org && tenant && jobKey)) return "";
  return `https://cloud.uipath.com/${org}/${tenant}/orchestrator_/jobs(sidepanel:sidepanel/jobs/${jobKey}/details)`;
};

// ---- Queue items ----------------------------------------------------------

// Where a queue/job request is scoped. Both headers are sent: the QueueItems and
// QueueDefinitions endpoints are addressed by org-unit id; folderPath ("" omits the
// header) covers endpoints that want the folder name instead.
export interface FolderScope {
  orgUnitId: string;
  folderPath: string;
}

// A queue item normalized into a TOTAL shape (no optionals): raw OData fields are
// read defensively into "" / 0 / {} so downstream code never branches on undefined.
export interface QueueItem {
  id: number;
  status: string;
  reference: string;
  creationTime: string;
  retryNumber: number;
  queueDefinitionId: number;
  name: string; // queue display name — from $expand=QueueDefinition (live), else the item's own possibly-stale Name
  robotName: string; // from $expand=Robot($select=Name); "" if unassigned
  specificContent: Record<string, unknown>;
}

// Nav properties folded into every QueueItems query below — confirmed via live
// $metadata that QueueDefinition/Robot are valid QueueItems nav properties. MUST
// stay a bare expand (no nested $select): confirmed live that a nested $select
// inside $expand on THIS endpoint silently nulls that entity — e.g.
// `Robot($select=Name)` returns Robot:null even when the item has a robot — while
// the identical nested-$select pattern works fine on Jobs (see JOB_EXPAND). toQueueItem
// only reads .Name off the full object, so the larger payload costs nothing downstream.
const QUEUE_ITEM_EXPAND = "QueueDefinition,Robot";

export const toQueueItem = (raw: unknown): QueueItem => ({
  id: numField(raw, "Id"),
  status: strField(raw, "Status"),
  reference: strField(raw, "Reference"),
  creationTime: strField(raw, "CreationTime"),
  retryNumber: numField(raw, "RetryNumber"),
  queueDefinitionId: numField(raw, "QueueDefinitionId"),
  name: strField(recField(raw, "QueueDefinition"), "Name") || strField(raw, "Name"),
  robotName: strField(recField(raw, "Robot"), "Name"),
  specificContent: recField(raw, "SpecificContent"),
});

// Numeric OrganizationUnitId for an env: config.folderIdByEnv > the bundled
// reference default (prod 231517 / pre_prod 434039).
export const resolveOrgUnitId = (env: Env): string =>
  getUipath().folderIdByEnv?.[env] ?? folderIdFor(env);

// The folder scope for an env (both headers populated).
export const scopeForEnv = (env: Env): FolderScope => ({
  orgUnitId: resolveOrgUnitId(env),
  folderPath: resolveFolder(env) ?? "",
});

// Single queue item by transaction/item id. Throws on a non-2xx.
export async function getQueueItem(itemId: number, scope: FolderScope): Promise<QueueItem> {
  return toQueueItem(
    await uipathGet(
      `/odata/QueueItems(${itemId})`,
      { $expand: QUEUE_ITEM_EXPAND },
      scope.folderPath,
      scope.orgUnitId,
    ),
  );
}

// Canonical queue display name for a QueueDefinition id. Returns "" if unresolved —
// the item's own Name field is sometimes empty/stale, so this is the source of truth.
export async function getQueueDefinitionName(
  queueDefId: number,
  scope: FolderScope,
): Promise<string> {
  try {
    const data = await uipathGet(
      `/odata/QueueDefinitions(${queueDefId})`,
      { $select: "Name" },
      scope.folderPath,
      scope.orgUnitId,
    );
    return strField(data, "Name");
  } catch {
    return "";
  }
}

// ---- Queues / releases / triggers (discovery reads) ------------------------

// A queue definition (the queue itself, not its items), normalized total.
export interface QueueDefinition {
  id: number;
  name: string;
  description: string;
  creationTime: string;
}

// Queue definitions in a folder, sorted by name. `nameContains` "" = no filter.
// Dev-clone queue ids differ from the prod ids in the static PORTALS registry, so
// this is how pre_prod queueDefIds are discovered.
export async function listQueueDefinitions(
  scope: FolderScope,
  nameContains: string,
  top: number,
): Promise<QueueDefinition[]> {
  const params: Record<string, string> = {
    $orderby: "Name asc",
    $top: String(top),
    $select: "Id,Name,Description,CreationTime",
  };
  const needle = nameContains.trim();
  if (needle) params["$filter"] = `contains(Name, '${needle.replace(/'/g, "''")}')`;
  const data = await uipathGet(
    "/odata/QueueDefinitions",
    params,
    scope.folderPath,
    scope.orgUnitId,
  );
  return odataValues<unknown>(data).map((raw) => ({
    id: numField(raw, "Id"),
    name: strField(raw, "Name"),
    description: strField(raw, "Description"),
    creationTime: strField(raw, "CreationTime"),
  }));
}

// A release ("process" in the Orchestrator UI) — the startable unit a job runs.
// ProcessVersion is the package version the release is pinned to (a job resolves
// it at START time, so verify the pin here before start_job).
export interface UiPathRelease {
  id: number;
  key: string; // GUID — what StartJobs takes as ReleaseKey
  name: string;
  processKey: string; // package id
  processVersion: string;
  isLatestVersion: boolean;
}

// Releases (processes) in a folder, sorted by name. `nameContains` "" = no filter.
export async function listReleases(
  scope: FolderScope,
  nameContains: string,
  top: number,
): Promise<UiPathRelease[]> {
  const params: Record<string, string> = {
    $orderby: "Name asc",
    $top: String(top),
    $select: "Id,Key,Name,ProcessKey,ProcessVersion,IsLatestVersion",
  };
  const needle = nameContains.trim();
  if (needle) params["$filter"] = `contains(Name, '${needle.replace(/'/g, "''")}')`;
  const data = await uipathGet("/odata/Releases", params, scope.folderPath, scope.orgUnitId);
  return odataValues<unknown>(data).map((raw) => {
    const r = isRecord(raw) ? raw : {};
    return {
      id: numField(raw, "Id"),
      key: strField(raw, "Key"),
      name: strField(raw, "Name"),
      processKey: strField(raw, "ProcessKey"),
      processVersion: strField(raw, "ProcessVersion"),
      isLatestVersion: r["IsLatestVersion"] === true,
    };
  });
}

// A trigger (ProcessSchedules row). Queue triggers carry a non-zero
// queueDefinitionId; time triggers have 0/"" sentinels there.
export interface UiPathTrigger {
  id: number;
  name: string;
  enabled: boolean;
  releaseId: number;
  releaseKey: string;
  releaseName: string;
  queueDefinitionId: number;
  queueDefinitionName: string;
  itemsActivationThreshold: number;
  maxJobsForActivation: number;
}

// Triggers in a folder. There is no separate queue-trigger collection in the OData
// API — queue triggers are ProcessSchedules rows with a QueueDefinitionId. This is
// the "does the queue trigger's ReleaseId point at MY dev release?" check.
export async function listTriggers(scope: FolderScope, top: number): Promise<UiPathTrigger[]> {
  const data = await uipathGet(
    "/odata/ProcessSchedules",
    {
      $top: String(top),
      // ProcessScheduleDto already denormalizes ReleaseName/QueueDefinitionName as
      // plain columns (confirmed via $metadata — no $expand needed/available for
      // Release/QueueDefinition here); $select just trims the rest of the row
      // (MachineRobots, Tags, cron fields, …) that this shape doesn't use.
      $select:
        "Id,Name,Enabled,ReleaseId,ReleaseKey,ReleaseName,QueueDefinitionId," +
        "QueueDefinitionName,ItemsActivationThreshold,MaxJobsForActivation",
    },
    scope.folderPath,
    scope.orgUnitId,
  );
  return odataValues<unknown>(data).map((raw) => {
    const r = isRecord(raw) ? raw : {};
    return {
      id: numField(raw, "Id"),
      name: strField(raw, "Name"),
      enabled: r["Enabled"] === true,
      releaseId: numField(raw, "ReleaseId"),
      releaseKey: strField(raw, "ReleaseKey"),
      releaseName: strField(raw, "ReleaseName"),
      queueDefinitionId: numField(raw, "QueueDefinitionId"),
      queueDefinitionName: strField(raw, "QueueDefinitionName"),
      itemsActivationThreshold: numField(raw, "ItemsActivationThreshold"),
      maxJobsForActivation: numField(raw, "MaxJobsForActivation"),
    };
  });
}

// Queue items for a queue, newest first. Filtered by QueueDefinitionId and, when
// `status` is non-empty, Status. Capped at `top`.
export async function listQueueItems(
  queueDefId: number,
  scope: FolderScope,
  status: string,
  top: number,
): Promise<QueueItem[]> {
  const filters = [`QueueDefinitionId eq ${queueDefId}`];
  if (status) filters.push(`Status eq '${status.replace(/'/g, "''")}'`);
  const data = await uipathGet(
    "/odata/QueueItems",
    {
      $filter: filters.join(" and "),
      $orderby: "CreationTime desc",
      $top: String(top),
      $select: "Id,Status,Reference,CreationTime,RetryNumber,QueueDefinitionId,SpecificContent",
      $expand: QUEUE_ITEM_EXPAND,
    },
    scope.folderPath,
    scope.orgUnitId,
  );
  return odataValues<unknown>(data).map(toQueueItem);
}

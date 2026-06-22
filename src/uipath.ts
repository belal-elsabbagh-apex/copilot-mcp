// UiPath Orchestrator OData client: job search/detail/confirm, robot logs, video,
// deep links, and per-env folder resolution. Ported from copilot-doctor src/api.ts
// (chrome background-proxy removed — direct fetch with the bearer from config.ts).

import { type Env, getUipath } from "./config.js";
import { outputMatchesOrder } from "./output-schema.js";
import { folderIdFor } from "./reference.js";
import { isRecord } from "./util.js";

export type { Env };

export interface UiPathJob {
  Id?: string;
  Key?: string;
  State?: string;
  CreationTime?: string;
  EndTime?: string;
  OutputArguments?: string;
  InputArguments?: string;
}

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

// One Orchestrator OData/REST GET. `params` become the OData querystring; `folder`
// scopes the request to a UiPath folder (falls back to the legacy config folderPath).
// `orgUnitId` (numeric folder id) is sent as X-UIPATH-OrganizationUnitId — the
// QueueItems/QueueDefinitions endpoints are addressed by org-unit id, while Jobs
// accept the folder-path header. Sending both is harmless.
// Returns parsed JSON, or throws with status + body snippet on a non-2xx.
async function uipathGet(
  path: string,
  params: Record<string, string> = {},
  folder?: string,
  orgUnitId = "",
): Promise<unknown> {
  const cfg = getUipath();
  const qs = new URLSearchParams(params).toString();
  const url = base() + path + (qs ? `?${qs}` : "");
  const folderPath = folder ?? cfg.folderPath;
  const res = await fetch(url, {
    headers: {
      Authorization: `Bearer ${cfg.bearer}`,
      Accept: "application/json",
      // Folder scoping — copilot-doctor uses the folder-path header. Jobs live in
      // a per-env folder (prod "Authorization" vs dev "Authorization Dev Clone").
      ...(folderPath ? { "X-UIPATH-FolderPath": folderPath } : {}),
      ...(orgUnitId ? { "X-UIPATH-OrganizationUnitId": orgUnitId } : {}),
    },
  });
  const text = await res.text();
  if (res.status >= 400) {
    throw new Error(`UiPath GET ${path} -> ${res.status}: ${text.slice(0, 300)}`);
  }
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
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
      $select: "Id,Key,State,CreationTime",
    },
    folder,
  );
  return odataValues<UiPathJob>(data);
}

// Recent jobs in a folder, newest first — bounded by `since`/`top`, with NO
// OutputArguments filter. Used as the reliable fallback when the `contains`
// substring scan is rejected (UiPath intermittently 400/500s on OutputArguments
// filtering). Filtering on CreationTime is indexed and supported.
export async function listRecentJobs(
  since: string | undefined,
  top: number,
  folder?: string,
): Promise<UiPathJob[]> {
  const params: Record<string, string> = {
    $orderby: "CreationTime desc",
    $top: String(top),
    $select: "Id,Key,State,CreationTime",
  };
  if (since) params["$filter"] = `CreationTime gt ${new Date(since).toISOString()}`;
  return odataValues<UiPathJob>(await uipathGet("/odata/Jobs", params, folder));
}

// Single job detail — the single-job endpoint returns OutputArguments where the
// collection omits it.
async function fetchJobDetailsById(jobId: string, folder?: string): Promise<UiPathJob | null> {
  if (!jobId) return null;
  try {
    return (await uipathGet(
      `/odata/Jobs(${jobId})`,
      { $select: "Id,Key,State,CreationTime,EndTime,OutputArguments,InputArguments" },
      folder,
    )) as UiPathJob;
  } catch {
    return null;
  }
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
  for (let i = 0; i < jobs.length; i += 10) {
    const batch = jobs.slice(i, i + 10);
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

// Robot execution logs for a single job, oldest first. RobotLogs is keyed by the
// job's GUID `Key` (not its numeric Id).
export async function fetchJobLogs(jobKey: string, folder?: string): Promise<JobLog[]> {
  if (!jobKey) return [];
  try {
    const data = await uipathGet(
      "/odata/RobotLogs",
      {
        $filter: `JobKey eq ${jobKey}`,
        $orderby: "TimeStamp asc",
        $top: "200",
        $select: "Level,Message,TimeStamp",
      },
      folder,
    );
    return odataValues<JobLog>(data);
  } catch {
    return [];
  }
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
  name: string; // queue display name (can be "" / stale — see QueueDefinitions)
  specificContent: Record<string, unknown>;
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

const toQueueItem = (raw: unknown): QueueItem => ({
  id: numField(raw, "Id"),
  status: strField(raw, "Status"),
  reference: strField(raw, "Reference"),
  creationTime: strField(raw, "CreationTime"),
  retryNumber: numField(raw, "RetryNumber"),
  queueDefinitionId: numField(raw, "QueueDefinitionId"),
  name: strField(raw, "Name"),
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
    await uipathGet(`/odata/QueueItems(${itemId})`, {}, scope.folderPath, scope.orgUnitId),
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
      $select:
        "Id,Status,Reference,CreationTime,RetryNumber,QueueDefinitionId,Name,SpecificContent",
    },
    scope.folderPath,
    scope.orgUnitId,
  );
  return odataValues<unknown>(data).map(toQueueItem);
}

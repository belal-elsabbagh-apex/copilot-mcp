// UiPath queue-item operations: pull one item as a ready-to-run test payload, and
// browse a queue for triage. Read-only — ported from the optum
// .planning/pull_queue_item_by_id.py script (same URL parsing + the IsApproved=false
// safety rule), reusing the OData client in uipath.ts.
//
// All types here are TOTAL — no optionals/nullables. Inputs arrive as discriminated
// unions; missing data is normalized to "" / 0 sentinels at the boundary.

import type { Env } from "./config.js";
import {
  envForFolderId,
  folderNameForId,
  normalizeQueueName,
  PORTALS,
  resolvePortal,
  resolvePortalByDefId,
} from "./reference.js";
import {
  type FolderScope,
  getQueueDefinitionName,
  getQueueItem,
  listQueueItems,
  type QueueItem,
  resolveOrgUnitId,
  scopeForEnv,
} from "./uipath.js";

const URL_TXN_RE = /\/transactions\/(\d+)\/details/;
const URL_QUEUE_RE = /\/queues\/(\d+)\/transactions/;

export interface ParsedQueueUrl {
  txnId: number;
  folderId: string;
  queueId: number; // 0 if the URL had no /queues/<id>/ segment
}

// Pull (txnId, folderId, queueId) out of an Orchestrator queue-item URL. The txn id
// lives only in the matrix-parameter `(sidepanel:.../transactions/<id>/details)`
// path segment, and the folder id only in the `fid` query param. Throws if either
// required part is absent.
export function parseQueueItemUrl(url: string): ParsedQueueUrl {
  const u = new URL(url);
  const folderId = u.searchParams.get("fid");
  if (!folderId) throw new Error(`queue-item URL is missing the 'fid' query param: ${url}`);
  const haystack = u.pathname + u.search;
  const txnMatch = URL_TXN_RE.exec(haystack);
  if (!txnMatch)
    throw new Error(`queue-item URL is missing a /transactions/<id>/details segment: ${url}`);
  const queueMatch = URL_QUEUE_RE.exec(haystack);
  return {
    txnId: Number(txnMatch[1]),
    folderId,
    queueId: queueMatch ? Number(queueMatch[1]) : 0,
  };
}

// A portal reference exposed on a pull result. matched=false carries empty sentinels.
export interface PortalRef {
  matched: boolean;
  key: string;
  portalDir: string;
  buildArtifact: string;
  family: string;
}

// Build a PHI-light filename slug from a queue item's member name.
function memberFilename(sc: Record<string, unknown>): string {
  const s = (v: unknown): string => (typeof v === "string" ? v.trim().toLowerCase() : "");
  const first = s(sc["MemberFirstName"]);
  const last = s(sc["MemberLastName"]);
  let slug = `${first}-${last}`.replace(/^-|-$/g, "");
  if (!slug) slug = s(sc["MemberFullName"]) || "unknown";
  slug = slug.replace(/,/g, "").replace(/\s+/g, "-");
  return `${slug}.yaml`;
}

// Where to pull a queue item from: either an Orchestrator URL (fid + txn id parsed
// out) or an explicit transaction id scoped by env (folderId "" = use the env default).
export type PullQueueItemArgs =
  | { source: "url"; url: string }
  | { source: "txn"; txnId: number; env: Env; folderId: string };

export interface PullQueueItemResult {
  item: { id: number; status: string; creationTime: string; retryNumber: number };
  env: Env;
  queueName: string;
  queueDefinitionId: number;
  portal: PortalRef;
  isApprovedForced: true;
  suggestedFilename: string;
  specificContent: Record<string, unknown>;
}

// Resolve the (txnId, env, scope) to query from either input variant.
function resolvePull(args: PullQueueItemArgs): { txnId: number; env: Env; scope: FolderScope } {
  if (args.source === "url") {
    const parsed = parseQueueItemUrl(args.url);
    return {
      txnId: parsed.txnId,
      env: envForFolderId(parsed.folderId),
      // Query by the URL's actual fid (correct even for folders we don't map).
      scope: { orgUnitId: parsed.folderId, folderPath: folderNameForId(parsed.folderId) },
    };
  }
  if (!args.txnId) throw new Error("a transaction id is required");
  const scope: FolderScope = args.folderId
    ? { orgUnitId: args.folderId, folderPath: folderNameForId(args.folderId) }
    : scopeForEnv(args.env);
  // folderId override without a known name still needs a valid org-unit id.
  if (args.folderId && !scope.orgUnitId) scope.orgUnitId = resolveOrgUnitId(args.env);
  return { txnId: args.txnId, env: args.env, scope };
}

// Fetch a single queue item and shape it into a ready-to-run test payload.
// CRITICAL: IsApproved is forced false so a test run can never submit a real auth.
export async function pullQueueItem(args: PullQueueItemArgs): Promise<PullQueueItemResult> {
  const { txnId, env, scope } = resolvePull(args);
  const item = await getQueueItem(txnId, scope);

  let queueName = normalizeQueueName(item.name);
  if ((!queueName || !resolvePortal(queueName).matched) && item.queueDefinitionId) {
    const resolved = await getQueueDefinitionName(item.queueDefinitionId, scope);
    if (resolved) queueName = normalizeQueueName(resolved);
  }
  const portal = resolvePortal(queueName);

  const specificContent: Record<string, unknown> = { ...item.specificContent, IsApproved: false };

  return {
    item: {
      id: item.id,
      status: item.status,
      creationTime: item.creationTime,
      retryNumber: item.retryNumber,
    },
    env,
    queueName,
    queueDefinitionId: item.queueDefinitionId,
    portal: {
      matched: portal.matched,
      key: portal.key,
      portalDir: portal.portalDir,
      buildArtifact: portal.buildArtifact,
      family: portal.family,
    },
    isApprovedForced: true,
    suggestedFilename: memberFilename(specificContent),
    specificContent,
  };
}

// ---- list_queue_items -----------------------------------------------------

export type QueueKind = "submit" | "sync";

export interface ListQueueArgs {
  queueName: string; // "" when using queueDefId
  queueDefId: number; // 0 when using queueName
  queueKind: QueueKind;
  env: Env;
  status: string; // "" = no filter
  top: number;
}

export interface ListQueueRow {
  id: number;
  status: string;
  reference: string;
  creationTime: string;
  retryNumber: number;
  memberId: string;
  member: string;
}

export interface ListQueueResult {
  env: Env;
  queueName: string;
  queueDefinitionId: number;
  count: number;
  items: ListQueueRow[];
}

// Resolve the QueueDefinitionId to browse, from an explicit id or a portal/queue name.
function resolveQueueDefId(args: ListQueueArgs): { id: number; queueName: string } {
  if (args.queueDefId) {
    return { id: args.queueDefId, queueName: resolvePortalByDefId(args.queueDefId).key };
  }
  if (!args.queueName) throw new Error("provide either `queueDefId` or `queueName`");
  const portal = resolvePortal(args.queueName);
  if (!portal.matched) {
    throw new Error(
      `unknown queue '${args.queueName}'. Known: ${PORTALS.map((p) => p.key).join(", ")}`,
    );
  }
  const id = args.queueKind === "sync" ? portal.syncQueueDefId : portal.submitQueueDefId;
  if (!id) {
    throw new Error(
      `portal '${portal.key}' has no known ${args.queueKind} queue id — pass queueDefId`,
    );
  }
  return { id, queueName: portal.key };
}

const str = (v: unknown): string => (typeof v === "string" ? v : "");

// Browse a queue (PHI-light projection — id/status/reference + member identifier).
export async function listQueue(args: ListQueueArgs): Promise<ListQueueResult> {
  const { id, queueName } = resolveQueueDefId(args);
  const rows: QueueItem[] = await listQueueItems(id, scopeForEnv(args.env), args.status, args.top);
  return {
    env: args.env,
    queueName,
    queueDefinitionId: id,
    count: rows.length,
    items: rows.map((r) => ({
      id: r.id,
      status: r.status,
      reference: r.reference,
      creationTime: r.creationTime,
      retryNumber: r.retryNumber,
      memberId: str(r.specificContent["MemberID"]),
      member: str(r.specificContent["MemberFullName"]),
    })),
  };
}

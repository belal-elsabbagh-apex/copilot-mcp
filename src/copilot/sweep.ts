// find_stuck_orders: page recent orders in an env and flag the ones sitting in a
// non-terminal ("stuck") status, optionally correlating each to its UiPath queue
// item(s). Read-only. Reuses the order-filter scan (same shape as
// find_clone_candidates) and the queue-item correlation in uipath.ts.

import { type Env, resolveCreds } from "../config/config.js";
import { chunk, type StepProgress } from "../shared/util.js";
import { type QueueItemMatch, scopeForEnv, searchQueueItemsByOrderId } from "../uipath/uipath.js";
import { type BeOrder, filterOrders, login, makeClient, ORDER_MODE } from "./copilot-client.js";

// Statuses considered "stuck" by default: submitted-but-not-finished, or never
// completed. Terminal/healthy statuses (e.g. forReview, completed) are excluded.
export const DEFAULT_STUCK_STATUSES = ["inProgress", "incomplete", "pending"];

export interface StuckOrder {
  orderUid?: string | undefined;
  status?: string | undefined;
  type?: string | undefined;
  facility?: string | undefined;
  insurance?: string | undefined;
  ageHours: number | null;
  uipath?:
    | {
        verdict: string;
        queueItemCount: number;
        queueItemStatus?: string | undefined; // PHI-safe: New/InProgress/Failed/Retried/Successful
        processingExceptionType?: string | undefined; // e.g. BusinessException
      }
    | undefined;
}

export interface FindStuckArgs {
  env: Env;
  profile?: string | null | undefined;
  scanPages?: number | undefined;
  statuses?: string[] | undefined;
  olderThanHours?: number | undefined;
  crossCheckUipath?: boolean | undefined;
  since?: string | undefined;
  top?: number | undefined;
  onProgress?: StepProgress | undefined;
}

export interface FindStuckResult {
  env: Env;
  scanned: number;
  statuses: string[];
  found: number;
  stuck: StuckOrder[];
}

// Best-effort age in hours from whatever timestamp the row carries. null if none parse.
function ageHours(row: BeOrder): number | null {
  for (const v of [row.orderDate, row.encounterDate, row.appointmentDate]) {
    if (!v) continue;
    const t = Date.parse(v);
    if (!Number.isNaN(t)) return Math.round((Date.now() - t) / 3_600_000);
  }
  return null;
}

// The order's own BE creationDate is a hard floor for its queue item's CreationTime —
// UiPath cannot queue an order before Copilot creates it. Narrowing the per-order search
// to (creationDate - margin) is therefore a free, correctness-preserving tightening (it
// can never cause a missed match, unlike ageHours()'s clinical dates, which can sit
// arbitrarily far from when the order was actually queued) — it turns an otherwise
// unbounded contains(SpecificData, uid) scan into one bounded to the relevant window,
// without an extra Orchestrator round trip (creationDate is already in the page scan).
// An explicit caller `since` is a floor too, so the tighter (later) of the two wins —
// this never loosens a bound the caller deliberately set.
const CREATION_TO_QUEUE_MARGIN_MS = 24 * 3_600_000; // clock skew / BE -> queue processing lag
export function queueSearchSince(
  callerSince: string | undefined,
  orderCreationDate: string | undefined,
): string | undefined {
  const callerMs = callerSince ? Date.parse(callerSince) : Number.NaN;
  const orderMs = orderCreationDate ? Date.parse(orderCreationDate) : Number.NaN;
  const floors = [
    Number.isFinite(callerMs) ? callerMs : undefined,
    Number.isFinite(orderMs) ? orderMs - CREATION_TO_QUEUE_MARGIN_MS : undefined,
  ].filter((v): v is number => v !== undefined);
  return floors.length ? new Date(Math.max(...floors)).toISOString() : undefined;
}

// Summarize an order's queue item(s) into a coarse verdict. The queue item is the
// source of truth for what happened to the order: its Status directly determines
// faulted (Failed) / running (InProgress) / stuck-but-succeeded (Successful), and a
// New item with no ExecutorJobKey means "queued, not yet picked up" by a robot.
// Statuses are read newest-first (CreationTime desc), so [0] is the latest.
// Priority: faulted > running > queued-not-picked-up > successful-order-stuck.
function uipathVerdict(queueItems: QueueItemMatch[]): NonNullable<StuckOrder["uipath"]> {
  if (!queueItems.length) return { verdict: "no-job", queueItemCount: 0 };
  const statuses = queueItems.map((q) => q.status);
  const processingExceptionType = queueItems.find(
    (q) => q.processingExceptionType,
  )?.processingExceptionType;

  const faulted = statuses.some((s) => s === "Failed");
  const running = statuses.some((s) => s === "InProgress");
  const queuedNotPicked = queueItems.some((q) => q.status === "New" && !q.executorJobKey);
  const allSuccessful = statuses.every((s) => s === "Successful");

  let verdict = "job-found";
  if (faulted) verdict = "job-faulted";
  else if (running) verdict = "job-running";
  else if (queuedNotPicked) verdict = "queued-not-picked-up";
  else if (allSuccessful) verdict = "job-successful-order-stuck";

  return {
    verdict,
    queueItemCount: queueItems.length,
    ...(statuses[0] ? { queueItemStatus: statuses[0] } : {}),
    ...(processingExceptionType ? { processingExceptionType } : {}),
  };
}

export async function findStuckOrders(args: FindStuckArgs): Promise<FindStuckResult> {
  const env = args.env;
  const scanPages = args.scanPages ?? 8;
  const statuses = (args.statuses ?? DEFAULT_STUCK_STATUSES).map((s) => s.toLowerCase());
  const olderThanHours = args.olderThanHours ?? 0;

  const creds = resolveCreds(args.profile ?? null)[env];
  const client = makeClient(creds.be, env);
  await login(client, creds.email, creds.password);

  const onProgress = args.onProgress;
  const stuck: StuckOrder[] = [];
  const creationDateByUid = new Map<string, string | undefined>(); // internal-only: not part of StuckOrder's public shape
  let scanned = 0;
  for (let page = 0; page < scanPages; page++) {
    const { rows } = await filterOrders(client, {
      pageSize: 50,
      pageNumber: page,
      type: "Outbound Referral",
      orderMode: ORDER_MODE,
    });
    if (!rows.length) break;
    for (const o of rows) {
      scanned++;
      if (!statuses.includes((o.status ?? "").toLowerCase())) continue;
      const age = ageHours(o);
      if (olderThanHours > 0 && (age === null || age < olderThanHours)) continue;
      stuck.push({
        orderUid: o.orderUid,
        status: o.status,
        type: o.orderType?.name,
        facility: o.referredFacility?.name,
        insurance: o.insurance?.name,
        ageHours: age,
      });
      if (o.orderUid) creationDateByUid.set(o.orderUid, o.creationDate);
    }
    onProgress?.(
      page + 1,
      scanPages,
      `page ${page + 1}: ${scanned} scanned, ${stuck.length} stuck`,
    );
  }

  if (args.crossCheckUipath) {
    const scope = scopeForEnv(env);
    const top = args.top ?? 50;
    // Offset past the page scan so progress stays monotonic across both phases. The
    // candidate count isn't known until the scan finishes, so the total grows here —
    // allowed (total is advisory), and better than reporting the cross-check silently.
    await crossCheckUipath(
      stuck,
      async (orderUid) =>
        (
          await searchQueueItemsByOrderId(
            orderUid,
            scope,
            top,
            queueSearchSince(args.since, creationDateByUid.get(orderUid)),
          )
        ).matches,
      (done, total, label) => onProgress?.(scanPages + done, scanPages + total, label),
    );
  }

  return { env, scanned, statuses, found: stuck.length, stuck };
}

// Bounded-concurrency batches (same chunk()+Promise.allSettled shape as uipath.ts's
// fetchJobsForKeys) instead of one Orchestrator lookup at a time — a real per-order
// failure lands on that order's uipath.verdict, never lost and never blocking the
// rest of the batch. `correlate` is injected so this is unit-testable without HTTP.
export async function crossCheckUipath(
  stuck: StuckOrder[],
  correlate: (orderUid: string) => Promise<QueueItemMatch[]>,
  onProgress?: StepProgress,
): Promise<void> {
  const candidates = stuck.filter((s): s is StuckOrder & { orderUid: string } => !!s.orderUid);
  let checked = 0;
  for (const batch of chunk(candidates, 10)) {
    const results = await Promise.allSettled(batch.map((s) => correlate(s.orderUid)));
    batch.forEach((s, i) => {
      const r = results[i];
      s.uipath =
        r?.status === "fulfilled"
          ? uipathVerdict(r.value)
          : {
              verdict: `error: ${r?.reason instanceof Error ? r.reason.message : String(r?.reason)}`,
              queueItemCount: 0,
            };
    });
    checked += batch.length;
    onProgress?.(checked, candidates.length, `cross-checked ${checked} order(s) against UiPath`);
  }
}

// find_stuck_orders: page recent orders in an env and flag the ones sitting in a
// non-terminal ("stuck") status, optionally correlating each to its queue item(s) on
// the orchestrator(s). Read-only. Reuses the order-filter scan (same shape as
// find_clone_candidates) and the cross-orchestrator queue-item search in
// orchestrator/find-order.ts.

import type { Env } from "../config/config.js";
import {
  type FindOrderAcrossResult,
  findOrderAcross,
  selectBackends,
} from "../orchestrator/find-order.js";
import type { EnabledOrchestrator } from "../orchestrator/registry.js";
import type { Orchestrator, QueueItemCore } from "../orchestrator/types.js";
import { chunk, type StepProgress } from "../shared/util.js";
import { type BeOrder, filterOrders, ORDER_MODE } from "./copilot-client.js";
import { connect } from "./session.js";

// Statuses considered "stuck" by default: submitted-but-not-finished, or never
// completed. Terminal/healthy statuses (e.g. forReview, completed) are excluded.
export const DEFAULT_STUCK_STATUSES = ["inProgress", "incomplete", "pending"];

export interface OrderVerdict {
  backend: Orchestrator | null; // orchestrator holding the newest queue item; null = none found / lookup failed
  verdict: string;
  queueItemCount: number;
  queueItemStatus?: string | undefined; // PHI-safe: New/InProgress/Failed/Retried/Successful
  exceptionType?: "business" | "application" | undefined;
}

export interface StuckOrder {
  orderUid?: string | undefined;
  status?: string | undefined;
  type?: string | undefined;
  facility?: string | undefined;
  insurance?: string | undefined;
  ageHours: number | null;
  orchestrator?: OrderVerdict | undefined;
}

export interface FindStuckArgs {
  env: Env;
  profile?: string | null | undefined;
  scanPages?: number | undefined;
  statuses?: string[] | undefined;
  olderThanHours?: number | undefined;
  crossCheck?: boolean | undefined;
  orchestrator?: EnabledOrchestrator | undefined; // omitted = every orchestrator
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
// source of truth for what happened to the order: its status directly determines
// faulted (Failed) / running (InProgress) / stuck-but-succeeded (Successful), and a
// New item with no job attempt means "queued, not yet picked up". Items arrive newest
// first (createdAt desc), so [0] is the latest item and its jobs[0] the latest attempt.
// Priority: never-started > faulted > running > queued-not-picked-up > successful-order-stuck.
// Only the latest attempt of the latest item decides never-started: an earlier reaped
// attempt that a retry replaced is history, not the order's current state.
const VERDICT_RULES: [verdict: string, matches: (items: QueueItemCore[]) => boolean][] = [
  ["job-never-started", (items) => items[0]?.jobs[0]?.neverStarted === true],
  ["job-faulted", (items) => items.some((q) => q.status === "Failed")],
  ["job-running", (items) => items.some((q) => q.status === "InProgress")],
  ["queued-not-picked-up", (items) => items.some((q) => q.status === "New" && q.jobs.length === 0)],
  ["job-successful-order-stuck", (items) => items.every((q) => q.status === "Successful")],
];

function orderVerdict(items: QueueItemCore[]): OrderVerdict {
  const newest = items[0];
  if (!newest) return { backend: null, verdict: "no-job", queueItemCount: 0 };
  const exceptionType = items.find((q) => q.exceptionType)?.exceptionType;
  return {
    backend: newest.orchestrator,
    verdict: VERDICT_RULES.find(([, matches]) => matches(items))?.[0] ?? "job-found",
    queueItemCount: items.length,
    queueItemStatus: newest.status,
    ...(exceptionType ? { exceptionType } : {}),
  };
}

export async function findStuckOrders(args: FindStuckArgs): Promise<FindStuckResult> {
  const env = args.env;
  const scanPages = args.scanPages ?? 8;
  const statuses = (args.statuses ?? DEFAULT_STUCK_STATUSES).map((s) => s.toLowerCase());
  const olderThanHours = args.olderThanHours ?? 0;

  const { client } = await connect(env, args.profile);

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

  if (args.crossCheck) {
    const backends = selectBackends(args.orchestrator);
    const top = args.top ?? 50;
    // Offset past the page scan so progress stays monotonic across both phases. The
    // candidate count isn't known until the scan finishes, so the total grows here —
    // allowed (total is advisory), and better than reporting the cross-check silently.
    await crossCheckOrders(
      stuck,
      async (orderUid) =>
        itemsOrThrow(
          await findOrderAcross(
            {
              env,
              orderUid,
              since: queueSearchSince(args.since, creationDateByUid.get(orderUid)),
              top,
              includeSpecificContent: false,
            },
            backends,
          ),
        ),
      (done, total, label) => onProgress?.(scanPages + done, scanPages + total, label),
    );
  }

  return { env, scanned, statuses, found: stuck.length, stuck };
}

// findOrderAcross isolates one orchestrator's failure into searchErrors. For the sweep an
// order with no items AND a failed search is unknown, not "no-job": throw so
// crossCheckOrders records it as that order's error verdict. With items found, the
// verdict stands on them.
export function itemsOrThrow(found: FindOrderAcrossResult): QueueItemCore[] {
  const failed = Object.entries(found.searchErrors ?? {});
  if (found.count === 0 && failed.length) {
    throw new Error(failed.map(([name, message]) => `${name}: ${message}`).join("; "));
  }
  return found.items;
}

// Bounded-concurrency batches (same chunk()+Promise.allSettled shape as uipath.ts's
// fetchJobsForKeys) instead of one Orchestrator lookup at a time — a real per-order
// failure lands on that order's orchestrator.verdict, never lost and never blocking the
// rest of the batch. `correlate` is injected so this is unit-testable without HTTP and
// must return the order's items newest first.
export async function crossCheckOrders(
  stuck: StuckOrder[],
  correlate: (orderUid: string) => Promise<QueueItemCore[]>,
  onProgress?: StepProgress,
): Promise<void> {
  const candidates = stuck.filter((s): s is StuckOrder & { orderUid: string } => !!s.orderUid);
  let checked = 0;
  for (const batch of chunk(candidates, 10)) {
    const results = await Promise.allSettled(batch.map((s) => correlate(s.orderUid)));
    batch.forEach((s, i) => {
      const r = results[i];
      s.orchestrator =
        r?.status === "fulfilled"
          ? orderVerdict(r.value)
          : {
              backend: null,
              verdict: `error: ${r?.reason instanceof Error ? r.reason.message : String(r?.reason)}`,
              queueItemCount: 0,
            };
    });
    checked += batch.length;
    onProgress?.(
      checked,
      candidates.length,
      `cross-checked ${checked} order(s) against orchestrators`,
    );
  }
}

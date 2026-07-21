// find_stuck_orders: page recent orders in an env and flag the ones sitting in a
// non-terminal ("stuck") status, optionally correlating each to its UiPath job(s).
// Read-only. Reuses the order-filter scan (same shape as find_clone_candidates) and
// the Orchestrator search in uipath.ts.

import { type Env, resolveCreds } from "../config/config.js";
import { chunk } from "../shared/util.js";
import { resolveFolder, searchJobsByOrderId, type UiPathJob } from "../uipath/uipath.js";
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
        latestState?: string | undefined;
        jobCount: number;
        processNames?: string[] | undefined;
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

// Summarize a list of candidate jobs into a coarse verdict. These are substring
// matches on OutputArguments (not confirmed), so the verdict is a heuristic hint.
function uipathVerdict(jobs: UiPathJob[]): {
  verdict: string;
  latestState?: string | undefined;
  jobCount: number;
  processNames?: string[] | undefined;
} {
  if (!jobs.length) return { verdict: "no-job", jobCount: 0 };
  const states = jobs.map((j) => j.State ?? "");
  const latestState = states[0];
  const processNames = [...new Set(jobs.map((j) => j.ReleaseName).filter((n): n is string => !!n))];
  let verdict = "job-found";
  if (states.some((s) => s === "Faulted" || s === "Stopped")) verdict = "job-faulted";
  else if (states.some((s) => s === "Running" || s === "Pending")) verdict = "job-running";
  else if (states.every((s) => s === "Successful")) verdict = "job-successful-order-stuck";
  return { verdict, latestState, jobCount: jobs.length, processNames };
}

export async function findStuckOrders(args: FindStuckArgs): Promise<FindStuckResult> {
  const env = args.env;
  const scanPages = args.scanPages ?? 8;
  const statuses = (args.statuses ?? DEFAULT_STUCK_STATUSES).map((s) => s.toLowerCase());
  const olderThanHours = args.olderThanHours ?? 0;

  const creds = resolveCreds(args.profile ?? null)[env];
  const client = makeClient(creds.be, env);
  await login(client, creds.email, creds.password);

  const stuck: StuckOrder[] = [];
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
    }
  }

  if (args.crossCheckUipath) {
    const folder = resolveFolder(env);
    await crossCheckUipath(stuck, (orderUid) =>
      searchJobsByOrderId(orderUid, args.since, args.top ?? 50, folder),
    );
  }

  return { env, scanned, statuses, found: stuck.length, stuck };
}

// Bounded-concurrency batches (same chunk()+Promise.allSettled shape as uipath.ts's
// fetchJobsForKeys) instead of one Orchestrator lookup at a time — a real per-order
// failure lands on that order's uipath.verdict, never lost and never blocking the
// rest of the batch. `search` is injected so this is unit-testable without HTTP.
export async function crossCheckUipath(
  stuck: StuckOrder[],
  search: (orderUid: string) => Promise<UiPathJob[]>,
): Promise<void> {
  const candidates = stuck.filter((s): s is StuckOrder & { orderUid: string } => !!s.orderUid);
  for (const batch of chunk(candidates, 10)) {
    const results = await Promise.allSettled(batch.map((s) => search(s.orderUid)));
    batch.forEach((s, i) => {
      const r = results[i];
      s.uipath =
        r?.status === "fulfilled"
          ? uipathVerdict(r.value)
          : {
              verdict: `error: ${r?.reason instanceof Error ? r.reason.message : String(r?.reason)}`,
              jobCount: 0,
            };
    });
  }
}

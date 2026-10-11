// src/orchestrator/find-order.ts
// find_order_queue_items across orchestrators: ask every selected backend for the order's
// queue items in parallel, merge them newest first, and keep each backend's failure
// isolated. Routing is decided per portal outside this repo, so where the item actually
// sits is the truth. A backend's own `since` retry (unbounded when nothing matches) is
// that backend's business and is not repeated here.

import { ExpectedError } from "../shared/expected-error.js";
import type { FindOrderArgs, FindOrderResult, OrchestratorBackend } from "./backend.js";
import { backendFor, searchableBackends } from "./registry.js";
import type { Orchestrator, QueueItemCore } from "./types.js";

export interface FindOrderAcrossResult {
  count: number;
  jobIds: Partial<Record<Orchestrator, string[]>>; // per answering backend, newest first, deduped
  items: QueueItemCore[]; // merged, newest first by createdAt
  searched: Orchestrator[];
  backends: Partial<Record<Orchestrator, { folder: string; searchMode: string; scanned: number }>>;
  notes?: string[]; // each prefixed "<backend>: "
  searchErrors?: Partial<Record<Orchestrator, string>>;
}

// `folder` names one orchestrator's folder, so it only makes sense next to that orchestrator.
export function selectBackends(
  orchestrator?: Orchestrator,
  folder?: string,
): OrchestratorBackend[] {
  if (folder && !orchestrator) {
    throw new ExpectedError(
      "`folder` is a per-orchestrator override: pass `orchestrator` together with it, or omit " +
        "`folder` to search every orchestrator's default folder for the env.",
    );
  }
  return orchestrator ? [backendFor(orchestrator)] : searchableBackends();
}

// NaN would make the comparator inconsistent; an unreadable timestamp just sorts oldest.
const createdMs = (item: QueueItemCore): number => {
  const t = Date.parse(item.createdAt);
  return Number.isNaN(t) ? 0 : t;
};

// Items arrive newest first and each item's attempts newest first, so first-seen order
// is newest first; the Set drops the repeats a retried transaction produces.
const jobIdsOf = (items: QueueItemCore[]): string[] => [
  ...new Set(items.flatMap((i) => i.jobs.map((j) => j.id))),
];

export async function findOrderAcross(
  args: FindOrderArgs,
  backends: OrchestratorBackend[],
): Promise<FindOrderAcrossResult> {
  const settled = await Promise.allSettled(backends.map((b) => b.findOrderQueueItems(args)));
  const searched = backends.map((b) => b.name);

  const answers: Partial<Record<Orchestrator, FindOrderResult>> = {};
  const searchErrors: Partial<Record<Orchestrator, string>> = {};
  settled.forEach((r, i) => {
    const name = searched[i];
    if (!name) return;
    if (r.status === "rejected") {
      searchErrors[name] = r.reason instanceof Error ? r.reason.message : String(r.reason);
      return;
    }
    answers[name] = r.value;
    if (r.value.searchError) searchErrors[name] = r.value.searchError;
  });

  const items = searched
    .flatMap((name) => answers[name]?.items ?? [])
    .sort((a, b) => createdMs(b) - createdMs(a));
  const notes = searched.flatMap((name) =>
    (answers[name]?.notes ?? []).map((n) => `${name}: ${n}`),
  );

  const jobIds: FindOrderAcrossResult["jobIds"] = {};
  const answered: FindOrderAcrossResult["backends"] = {};
  for (const name of searched) {
    const answer = answers[name];
    if (!answer) continue;
    jobIds[name] = jobIdsOf(items.filter((i) => i.orchestrator === name));
    answered[name] = {
      folder: answer.folder,
      searchMode: answer.searchMode,
      scanned: answer.scanned,
    };
  }

  return {
    count: items.length,
    jobIds,
    items,
    searched,
    backends: answered,
    ...(notes.length ? { notes } : {}),
    ...(Object.keys(searchErrors).length ? { searchErrors } : {}),
  };
}

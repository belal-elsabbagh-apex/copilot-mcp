// Action ids + apply selection (pure; unit-tested). Generic over any SyncAction — no
// section-specific logic lives here.

import { ExpectedError } from "../../mcp/feedback.js";
import { asArray } from "./internal.js";
import type { ApplyFilter, PlannedActionSummary, PlannedSyncAction, SyncAction } from "./types.js";

// Kept as a representative ExpectedError for feedback classification tests; no longer thrown.
export class NotImplementedError extends ExpectedError {
  constructor(message: string) {
    super(message);
    this.name = "NotImplementedError";
  }
}

// Stable, human-readable id for one planned action, derived from its semantic fields —
// stable across plan runs as long as the underlying drift is the same. Colons inside
// names are fine: ids are opaque match tokens, never parsed back apart.
export const actionId = (a: Pick<SyncAction, "section" | "op" | "typeName" | "itemName">): string =>
  `${a.section}:${a.op}:${a.typeName}:${a.itemName}`;

// Assign ids in plan order. Two actions with identical semantic fields (duplicate item
// names under one type) get a deterministic "#2"/"#3" suffix so every id stays unique.
export function assignActionIds(actions: SyncAction[]): PlannedSyncAction[] {
  const seen = new Map<string, number>();
  return actions.map((a) => {
    const base = actionId(a);
    const n = (seen.get(base) ?? 0) + 1;
    seen.set(base, n);
    return { ...a, id: n === 1 ? base : `${base}#${n}` };
  });
}

// The apply selection filter: exactly ONE of actionIds / all=true is required — there is
// deliberately no default "apply everything" (breadth must be an explicit input).
export const assertApplyFilter = (filter: ApplyFilter): void => {
  const wantAll = filter.all === true;
  const hasIds = (filter.actionIds?.length ?? 0) > 0;
  if (wantAll === hasIds)
    throw new ExpectedError(
      "pass exactly one of actionIds (from plan_settings_sync, reviewed with the user) or all=true — refusing to apply without an explicit selection",
    );
};

// Split the planned actions into the selected subset and the rest; ids that match no
// planned action are returned so the caller can surface state drift since planning.
export function selectPlannedActions(
  actions: readonly PlannedSyncAction[],
  filter: ApplyFilter,
): { selected: PlannedSyncAction[]; notSelected: PlannedSyncAction[]; unmatchedIds: string[] } {
  assertApplyFilter(filter);
  if (filter.all === true) return { selected: [...actions], notSelected: [], unmatchedIds: [] };

  const want = new Set(filter.actionIds);
  const selected: PlannedSyncAction[] = [];
  const notSelected: PlannedSyncAction[] = [];
  for (const a of actions) (want.has(a.id) ? selected : notSelected).push(a);
  const have = new Set(actions.map((a) => a.id));
  return { selected, notSelected, unmatchedIds: [...want].filter((id) => !have.has(id)) };
}

// One-line description of an action's request body so the plan output can omit the
// (potentially large) body itself.
export function summarizeActionBody(a: SyncAction): string {
  const count = (field: string): number => asArray(a.body[field]).length;
  if (a.itemKind === "speciality") {
    const facs = count("referredFacilities");
    const provs = count("referredProviders");
    return a.op === "create"
      ? `create speciality '${a.itemName}' (${facs} facilities, ${provs} providers)`
      : `merge speciality '${a.itemName}' -> ${facs} facilities, ${provs} providers (existing + prod-only additions)`;
  }
  return (
    `create order '${a.itemName}' (${count("CPTCodes")} CPTs, ${count("facilitiesUids")} facilities, ` +
    `${count("authSubCategoryUids")} auth + ${count("referralSubCategoryUids")} referral sub-categories)`
  );
}

// Plan-output shape for one action: everything needed to review + select it, with the
// request body summarized (includeBodies=true for spot-checking a scoped plan).
export function toActionSummary(
  a: PlannedSyncAction,
  includeBodies: boolean,
): PlannedActionSummary {
  return {
    id: a.id,
    section: a.section,
    op: a.op,
    itemKind: a.itemKind,
    typeName: a.typeName,
    itemName: a.itemName,
    method: a.method,
    path: a.path,
    summary: summarizeActionBody(a),
    ...(a.warnings ? { warnings: a.warnings } : {}),
    ...(includeBodies ? { body: a.body } : {}),
  };
}

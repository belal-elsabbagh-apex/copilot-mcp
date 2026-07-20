// Settings sync (plan/apply) — the write-side counterpart to diffSettings. ADDITIVELY copy
// settings that exist in PROD but are missing in PRE-PROD into pre-prod. Additive only —
// never overwrites or deletes existing pre-prod settings; pre-prod only. Split into two
// operations:
//   - planSettingsSyncOp: read-only — compute the planned actions, each with a stable id.
//   - applySettingsSync:  re-plans server-side (never accepts bodies from the caller) and
//                         executes only the actions the caller selected by id / all=true.
//
// Section dispatch is driven by each catalog section's own `sync?: SectionSyncer` — a section
// with no sync capability is reported under skippedSections automatically. Adding a new
// syncable domain means writing its SectionSyncer and attaching it to the relevant section(s);
// this file needs no changes.

import { resolveCreds } from "../../config/config.js";
import { assertPreProdClient, type HttpClient, login, makeClient } from "../copilot-client.js";
import { selectSections } from "./catalog.js";
import {
  assertApplyFilter,
  assignActionIds,
  selectPlannedActions,
  toActionSummary,
} from "./sync-actions.js";
import { buildPayerMaps } from "./sync-payers.js";
import type {
  ApplySettingsSyncOpts,
  ApplySettingsSyncResult,
  PayerLinkFinding,
  PlannedSyncAction,
  PlanSettingsSyncOpts,
  PlanSettingsSyncResult,
  SectionSyncer,
  SyncAction,
  SyncSkip,
} from "./types.js";

// The crawled + planned state shared by plan and apply: apply re-runs this exact
// computation rather than accepting bodies from the caller.
interface SyncPlanContext {
  account: string;
  prodBase: string;
  preProdBase: string;
  actions: PlannedSyncAction[];
  skipped: SyncSkip[];
  skippedSections: { key: string; reason: string }[];
  payerLinkFindings: PayerLinkFinding[];
  pre: HttpClient | null; // logged-in pre-prod client; null when nothing syncable was selected (no login done)
}

async function buildSyncPlan(opts: {
  profile?: string | null;
  sections?: string[];
  tags?: string[];
  emr?: string;
}): Promise<SyncPlanContext> {
  // Validate + resolve the selection up front (unknown tag/section fails fast, no login).
  const chosen = selectSections(opts.sections, opts.tags, opts.emr);

  // Collect the distinct syncers referenced by the chosen sections — several sections can
  // share one syncer (e.g. specialties/referred-providers/referred-facilities all point at
  // the same specialities syncer), so it only runs once no matter how many of them are chosen.
  const syncers = new Map<string, SectionSyncer>();
  for (const s of chosen) if (s.sync) syncers.set(s.sync.domain, s.sync);
  const skippedSections = chosen
    .filter((s) => !s.sync)
    .map((s) => ({ key: s.key, reason: "no write mapping yet — additive sync not implemented" }));

  const creds = resolveCreds(opts.profile ?? null);
  const ctx: SyncPlanContext = {
    account: opts.profile ?? "(default)",
    prodBase: creds.prod.be,
    preProdBase: creds.pre_prod.be,
    actions: [],
    skipped: [],
    skippedSections,
    payerLinkFindings: [],
    pre: null,
  };
  if (!syncers.size) return ctx; // nothing syncable selected — report skipped only

  const prod = makeClient(creds.prod.be, "prod");
  const pre = makeClient(creds.pre_prod.be, "pre_prod");
  await Promise.all([
    login(prod, creds.prod.email, creds.prod.password),
    login(pre, creds.pre_prod.email, creds.pre_prod.password),
  ]);

  // Every syncer remaps payer references prod->pre, so build the payer maps once and share them.
  const { prodToPre: payerMap, prodNameByUid, preNameByUid } = await buildPayerMaps(prod, pre);
  const actions: SyncAction[] = [];
  const skipped: SyncSkip[] = [];
  const payerLinkFindings: PayerLinkFinding[] = [];

  for (const syncer of syncers.values()) {
    const r = await syncer.plan({
      prod,
      pre,
      payerMap,
      prodPayerNameByUid: prodNameByUid,
      prePayerNameByUid: preNameByUid,
    });
    actions.push(...r.actions);
    skipped.push(...r.skipped);
    if (r.payerLinkFindings) payerLinkFindings.push(...r.payerLinkFindings);
  }

  ctx.actions = assignActionIds(actions);
  ctx.skipped = skipped;
  ctx.payerLinkFindings = payerLinkFindings;
  ctx.pre = pre;
  return ctx;
}

// Compute — without writing — the additive actions that would sync prod -> pre-prod for
// the selected sections. Read-only; applySettingsSync executes a reviewed selection.
export async function planSettingsSyncOp(
  opts: PlanSettingsSyncOpts,
): Promise<PlanSettingsSyncResult> {
  const ctx = await buildSyncPlan(opts);
  return {
    account: ctx.account,
    prodBase: ctx.prodBase,
    preProdBase: ctx.preProdBase,
    actionCount: ctx.actions.length,
    actions: ctx.actions.map((a) => toActionSummary(a, opts.includeBodies ?? false)),
    skipped: ctx.skipped,
    skippedSections: ctx.skippedSections,
    payerLinkFindings: ctx.payerLinkFindings,
  };
}

// Re-plan server-side (same computation + scoping args as planSettingsSyncOp — bodies are
// never accepted from the caller) and execute only the selected actions against PRE-PROD.
// Additive only. Requires actionIds XOR all=true; a stale id (state drifted since planning)
// matches nothing and lands in unmatchedIds instead of executing something unreviewed.
export async function applySettingsSync(
  opts: ApplySettingsSyncOpts,
): Promise<ApplySettingsSyncResult> {
  // Reject a missing/ambiguous selection BEFORE any login so the bad call is free.
  assertApplyFilter(opts);

  const ctx = await buildSyncPlan(opts);
  const { selected, notSelected, unmatchedIds } = selectPlannedActions(ctx.actions, opts);

  // Execute against PRE-PROD only. Additive: POST new specialities/orders / PUT
  // existing-plus-additions (specialities merge only).
  const executed: ApplySettingsSyncResult["executed"] = [];
  if (selected.length && ctx.pre) assertPreProdClient(ctx.pre, "apply_settings_sync");
  for (const a of selected) {
    if (!ctx.pre) break; // unreachable: actions imply a logged-in pre client
    const r = await ctx.pre.req(a.method, a.path, { json: a.body });
    const okWrite = r.status < 400;
    opts.onWrite?.(
      `apply_settings_sync ${a.op} ${a.itemKind} '${a.itemName}' (type '${a.typeName}')`,
      { id: a.id, method: a.method, path: a.path, status: r.status, ok: okWrite },
    );
    executed.push({
      id: a.id,
      op: a.op,
      itemKind: a.itemKind,
      typeName: a.typeName,
      itemName: a.itemName,
      method: a.method,
      path: a.path,
      status: r.status,
      ok: okWrite,
      ...(a.warnings ? { warnings: a.warnings } : {}),
    });
  }

  return {
    account: ctx.account,
    prodBase: ctx.prodBase,
    preProdBase: ctx.preProdBase,
    plannedCount: ctx.actions.length,
    executed,
    notSelected: notSelected.map((a) => ({
      id: a.id,
      op: a.op,
      typeName: a.typeName,
      itemName: a.itemName,
    })),
    unmatchedIds,
    skipped: ctx.skipped,
    skippedSections: ctx.skippedSections,
  };
}

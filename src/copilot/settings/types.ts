// Shared types for the settings module: the catalog entry shape every section module
// implements, the pluggable sync interface, and the request/result shapes for the five
// public tool entry points (diff_settings / get_settings / plan_settings_sync /
// apply_settings_sync / list_setting_sections).

import type { HttpClient } from "../copilot-client.js";

// ---- catalog entry ---------------------------------------------------------

export interface SettingsSection {
  key: string; // stable id used in the `sections` param + output
  label: string;
  group: string; // top-level grouping for coarse filtering / listing
  path: string; // GET path (query string included where the UI sends one)
  kind: "object" | "list";
  envelope?: string; // dot-path to the payload inside the response (e.g. "data")
  matchKey?: string; // list: field to match items across envs; omit => content-set diff
  ignore?: readonly string[]; // extra fields to strip before diffing (beyond global noise)
  derive?: (client: HttpClient) => Promise<unknown[]>; // custom fetch (overrides path); used for crawled sections
  sync?: SectionSyncer; // additive write capability; omit => reported under skippedSections
}

// ---- pluggable sync ---------------------------------------------------------

export interface SyncPlanCtx {
  prod: HttpClient;
  pre: HttpClient;
  payerMap: ReadonlyMap<string, string>;
  prodPayerNameByUid: ReadonlyMap<string, string>;
  prePayerNameByUid: ReadonlyMap<string, string>;
}

// A domain's additive write plan. `domain` is a stable identity used to de-dupe when
// several catalog sections share one syncer (e.g. specialties / referred-providers /
// referred-facilities all point at the same specialities syncer).
export interface SectionSyncer {
  domain: string;
  plan(ctx: SyncPlanCtx): Promise<{
    actions: SyncAction[];
    skipped: SyncSkip[];
    payerLinkFindings?: PayerLinkFinding[];
  }>;
}

// ---- diff engine result shapes ----------------------------------------------

export interface FieldDiff {
  path: string;
  prod: unknown;
  preprod: unknown;
}

export interface ListDiff {
  onlyInProd: unknown[];
  onlyInPreProd: unknown[];
  changed: { key: string; diffs: FieldDiff[] }[];
}

export interface SectionResult {
  key: string;
  label: string;
  kind: "object" | "list";
  equal: boolean;
  diffs?: FieldDiff[];
  onlyInProd?: unknown[];
  onlyInPreProd?: unknown[];
  changed?: { key: string; diffs: FieldDiff[] }[];
  error?: string;
}

// ---- list_setting_sections ---------------------------------------------------

export interface SettingSectionInfo {
  key: string;
  label: string;
  group: string;
  kind: "object" | "list";
  derived: boolean; // crawled (heavier) vs a plain single GET
  matchKey?: string; // list sections: the field items are matched/scoped by (omitted => content-set diff)
}

// ---- diff_settings -----------------------------------------------------------

export interface DiffSettingsOpts {
  profile?: string | null;
  sections?: string[];
  groups?: string[];
  emr?: string;
  includeUnchanged?: boolean;
}

export interface DiffSettingsResult {
  account: string;
  prodBase: string;
  preProdBase: string;
  sectionsCompared: number;
  sectionsWithDiffs: number;
  sections: SectionResult[];
}

// ---- get_settings --------------------------------------------------------------

export interface GetSettingsOpts {
  env: "prod" | "pre_prod";
  profile?: string | null;
  sections?: string[];
  groups?: string[];
  emr?: string;
  normalized?: boolean; // default true = stripped like diff_settings; false = raw
}

export interface GetSettingsSection {
  key: string;
  label: string;
  group: string;
  kind: "object" | "list";
  count?: number; // list sections: row count
  data?: unknown;
  error?: string; // per-section fetch failure (other sections still return)
}

export interface GetSettingsResult {
  account: string;
  env: "prod" | "pre_prod";
  base: string;
  sectionsFetched: number;
  sections: GetSettingsSection[];
}

// ---- sync actions --------------------------------------------------------------

export interface SyncAction {
  section: string;
  op: "create" | "merge";
  itemKind: "speciality" | "order";
  typeName: string;
  itemName: string; // speciality name or order name, depending on itemKind
  method: "POST" | "PUT";
  path: string;
  body: Record<string, unknown>;
  warnings?: string[];
}

export interface SyncSkip {
  typeName: string;
  specialityName?: string;
  reason: string;
}

export type PlannedSyncAction = SyncAction & { id: string };

// The apply selection filter: exactly ONE of actionIds / all=true is required — there is
// deliberately no default "apply everything".
export interface ApplyFilter {
  actionIds?: string[];
  all?: boolean;
}

export interface PlannedActionSummary {
  id: string;
  section: string;
  op: SyncAction["op"];
  itemKind: SyncAction["itemKind"];
  typeName: string;
  itemName: string;
  method: SyncAction["method"];
  path: string;
  summary: string;
  warnings?: string[];
  body?: Record<string, unknown>;
}

// ---- payer-link audit ------------------------------------------------------------

export interface PayerLinkFinding {
  typeName: string;
  specialityName: string;
  itemKind: "facility" | "provider";
  itemName: string;
  extraInPreProd: string[]; // payer NAMEs linked in pre-prod but not in prod
  missingInPreProd: string[]; // payer NAMEs linked in prod but not in pre-prod
  orphanedProdPayerUids: string[]; // prod's own payerUids that resolve to no payer name in prod
  orphanedPreProdPayerUids: string[]; // pre-prod's own payerUids that resolve to no payer name in pre-prod
}

// ---- plan_settings_sync ----------------------------------------------------------

export interface PlanSettingsSyncOpts {
  profile?: string | null;
  sections?: string[];
  groups?: string[];
  emr?: string;
  includeBodies?: boolean;
}

export interface PlanSettingsSyncResult {
  account: string;
  prodBase: string;
  preProdBase: string;
  actionCount: number;
  actions: PlannedActionSummary[];
  skipped: SyncSkip[];
  skippedSections: { key: string; reason: string }[];
  // Payer-link drift/orphans on facilities/providers that already exist (matched by name) in
  // both envs — audit-only, never turned into a write action.
  payerLinkFindings: PayerLinkFinding[];
}

// ---- apply_settings_sync ----------------------------------------------------------

export interface ExecutedAction {
  id: string;
  op: SyncAction["op"];
  itemKind: SyncAction["itemKind"];
  typeName: string;
  itemName: string;
  method: string;
  path: string;
  status: number;
  ok: boolean;
  warnings?: string[];
}

export interface ApplySettingsSyncOpts extends ApplyFilter {
  profile?: string | null;
  sections?: string[];
  groups?: string[];
  emr?: string;
  // Optional audit hook; the server wires this to mcpLog(warning) so every live write is logged.
  onWrite?: (message: string, data?: Record<string, unknown>) => void;
}

export interface ApplySettingsSyncResult {
  account: string;
  prodBase: string;
  preProdBase: string;
  plannedCount: number;
  executed: ExecutedAction[];
  notSelected: { id: string; op: SyncAction["op"]; typeName: string; itemName: string }[];
  unmatchedIds: string[];
  skipped: SyncSkip[];
  skippedSections: { key: string; reason: string }[];
}

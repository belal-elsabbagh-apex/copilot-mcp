// Settings diff: compare an account's EHR Copilot settings across prod and pre-prod.
//
// The BE exposes per-tenant settings under plain GET /api/v1/settings/* endpoints
// (see SETTINGS_CATALOG, assembled in ./catalog.ts from ./sections/*). We log into both envs
// for one account, fetch every section, and report a NORMALIZED diff. Read-only — never
// writes to either env.
//
// The hard part is cross-env noise: UIDs, dummy emails, CDN hosts, and timestamps differ
// between prod and pre-prod, so a naive deep diff flags every one of them. We therefore
// strip env-specific fields (stripNoise) and match list items on a SEMANTIC key (name),
// never on UID. See ./diff-engine.ts.
//
// Settings sync (plan/apply) is the write-side counterpart — see ./sync.ts and its
// per-domain SectionSyncer implementations (./specialities.ts, ./orders.ts).

export * from "./catalog.js";
export * from "./diff-engine.js";
export * from "./orders.js";
export * from "./read.js";
export * from "./specialities.js";
export * from "./sync.js";
export * from "./sync-actions.js";
export * from "./sync-payers.js";
export * from "./types.js";

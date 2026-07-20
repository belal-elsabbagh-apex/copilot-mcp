// Small generic helpers shared across settings/* modules — never part of the public API
// (not re-exported by index.ts).

import { isNoiseKey } from "./diff-engine.js";

export const asArray = (v: unknown): unknown[] => (Array.isArray(v) ? v : []);

export const toMessage = (e: unknown): string => (e instanceof Error ? e.message : String(e));

// Drop own-record UIDs + timestamps (the BE assigns fresh ones on create). Shared by both
// the specialities and orders write-body builders.
export const stripOwnUids = (rec: Record<string, unknown>): Record<string, unknown> => {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(rec)) if (!isNoiseKey(k)) out[k] = v;
  return out;
};

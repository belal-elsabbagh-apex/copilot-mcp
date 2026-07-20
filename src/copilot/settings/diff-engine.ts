// Normalization + diff (pure; unit-tested).
//
// The hard part of settings.ts is cross-env noise: UIDs, dummy emails, CDN hosts, and
// timestamps differ between prod and pre-prod, so a naive deep diff flags every one of
// them. We therefore strip env-specific fields (stripNoise) and match list items on a
// SEMANTIC key (name), never on UID.

import { isRecord, prop } from "../../shared/util.js";
import type { FieldDiff, ListDiff, SettingsSection } from "./types.js";

// Stable JSON of an already-normalized value (object keys + arrays are sorted by
// stripNoise, so JSON.stringify is canonical). undefined -> "undefined" so a present
// key with value X never collators with an absent key.
export const canonical = (v: unknown): string => JSON.stringify(v) ?? "undefined";

// Fields that differ across envs by construction and carry no semantic meaning.
// specialityName is the parent speciality's name, echoed onto every facility/provider row by
// the GET — verified (via a captured HAR of the Settings UI's "edit specialty" PUT) that the
// real UI never resends it. source is a GET-only field the create/merge write schema rejects
// outright (verified live: POST .../specialities 400s with "referredFacilities[0].source is
// not allowed" until it is stripped).
export const NOISE_KEYS = new Set([
  "createdAt",
  "updatedAt",
  "updatedBy",
  "specialityName",
  "source",
]);
export const isNoiseKey = (k: string): boolean => NOISE_KEYS.has(k) || /uid$/i.test(k);

// Recursively drop env-specific fields and sort keys/arrays so two semantically equal
// values normalize to byte-identical JSON regardless of UID/timestamp/order noise.
export function stripNoise(value: unknown, ignore: ReadonlySet<string>): unknown {
  if (Array.isArray(value)) {
    const mapped = value.map((v) => stripNoise(v, ignore));
    return [...mapped].sort((a, b) => {
      const [ca, cb] = [canonical(a), canonical(b)];
      return ca < cb ? -1 : ca > cb ? 1 : 0;
    });
  }
  if (isRecord(value)) {
    const out: Record<string, unknown> = {};
    for (const k of Object.keys(value).sort()) {
      if (isNoiseKey(k) || ignore.has(k)) continue;
      out[k] = stripNoise(value[k], ignore);
    }
    return out;
  }
  return value;
}

// Deep-walk two NORMALIZED values into flat path-level diffs. Records recurse key by
// key; arrays/primitives/type-mismatches are reported whole at their path.
export function diffObjects(a: unknown, b: unknown, base = ""): FieldDiff[] {
  if (canonical(a) === canonical(b)) return [];
  if (isRecord(a) && isRecord(b)) {
    const keys = new Set([...Object.keys(a), ...Object.keys(b)]);
    const out: FieldDiff[] = [];
    for (const k of [...keys].sort()) {
      out.push(...diffObjects(a[k], b[k], base ? `${base}.${k}` : k));
    }
    return out;
  }
  return [{ path: base || "(root)", prod: a, preprod: b }];
}

const keyOf = (item: unknown, matchKey: string): string => {
  const v = prop(item, matchKey);
  return v === undefined || v === null ? canonical(item) : String(v);
};

// Diff two lists after normalizing each element. With a matchKey, items are paired by
// that field and field-diffed; without one, items are compared as a content set.
export function diffList(
  prodArr: unknown[],
  preArr: unknown[],
  section: Pick<SettingsSection, "matchKey" | "ignore">,
): ListDiff {
  const ignore = new Set(section.ignore ?? []);
  const pN = prodArr.map((x) => stripNoise(x, ignore));
  const qN = preArr.map((x) => stripNoise(x, ignore));

  if (section.matchKey) {
    const mk = section.matchKey;
    const pMap = new Map(pN.map((it) => [keyOf(it, mk), it]));
    const qMap = new Map(qN.map((it) => [keyOf(it, mk), it]));
    const onlyInProd: unknown[] = [];
    const onlyInPreProd: unknown[] = [];
    const changed: { key: string; diffs: FieldDiff[] }[] = [];
    for (const [k, it] of pMap) {
      if (!qMap.has(k)) onlyInProd.push(it);
      else {
        const diffs = diffObjects(it, qMap.get(k));
        if (diffs.length) changed.push({ key: k, diffs });
      }
    }
    for (const [k, it] of qMap) if (!pMap.has(k)) onlyInPreProd.push(it);
    return { onlyInProd, onlyInPreProd, changed };
  }

  const pSet = new Map(pN.map((x) => [canonical(x), x]));
  const qSet = new Map(qN.map((x) => [canonical(x), x]));
  return {
    onlyInProd: [...pSet].filter(([c]) => !qSet.has(c)).map(([, x]) => x),
    onlyInPreProd: [...qSet].filter(([c]) => !pSet.has(c)).map(([, x]) => x),
    changed: [],
  };
}

// stripNoise with a section's per-section ignore set — the same normalization the diff
// applies to each side. Exposed pure so get_settings' normalized=true output is
// byte-comparable with what diff_settings compared.
export const normalizeSectionValue = (
  section: Pick<SettingsSection, "ignore">,
  data: unknown,
): unknown => stripNoise(data, new Set(section.ignore ?? []));

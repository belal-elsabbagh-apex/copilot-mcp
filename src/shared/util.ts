// Shared tiny helpers.

export const isRecord = (v: unknown): v is Record<string, unknown> =>
  typeof v === "object" && v !== null && !Array.isArray(v);

// Safely read a string-keyed property off an unknown value (undefined if absent
// or not an object). Centralizes the "narrow then index" dance so call sites stay
// cast-free under noUncheckedIndexedAccess / noPropertyAccessFromIndexSignature.
export const prop = (v: unknown, key: string): unknown => (isRecord(v) ? v[key] : undefined);

// Pull the rows out of the common API envelope `{ data: T[] }`, or [] when the
// shape isn't as expected. Returns unknown[] — narrow/cast per call site.
export const envelopeRows = (v: unknown): unknown[] => {
  const rows = prop(v, "data");
  return Array.isArray(rows) ? rows : [];
};

// Read a property only when it's a string (else undefined). Hardens the many
// `(x as { k?: string }).k` envelope reads into a real runtime check.
export const stringProp = (v: unknown, key: string): string | undefined => {
  const got = prop(v, key);
  return typeof got === "string" ? got : undefined;
};

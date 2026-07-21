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

// Milliseconds between two ISO timestamps, null when either is missing/unparseable.
export function msBetween(start: string | undefined, end: string | undefined): number | null {
  if (!(start && end)) return null;
  const a = Date.parse(start);
  const b = Date.parse(end);
  return Number.isFinite(a) && Number.isFinite(b) ? b - a : null;
}

// Parse text as JSON; fall back to the raw string when it isn't valid JSON ("" ->
// undefined). Centralizes the JSON-or-raw-text fallback needed wherever an HTTP
// response body has to be read defensively.
export function safeJsonParse(text: string): unknown {
  if (!text) return undefined;
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

// Split into fixed-size groups (last group may be smaller). Used to bound fan-out
// concurrency when a batch of independent requests replaces a per-item MCP call.
export function chunk<T>(items: T[], size: number): T[][] {
  const out: T[][] = [];
  for (let i = 0; i < items.length; i += size) out.push(items.slice(i, i + size));
  return out;
}

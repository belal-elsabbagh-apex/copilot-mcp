// Settings diff: compare an account's EHR Copilot settings across prod and pre-prod.
//
// The BE exposes per-tenant settings under plain GET /api/v1/settings/* endpoints
// (see SETTINGS_CATALOG). We log into both envs for one account, fetch every section,
// and report a NORMALIZED diff. Read-only — never writes to either env.
//
// The hard part is cross-env noise: UIDs, dummy emails, CDN hosts, and timestamps differ
// between prod and pre-prod, so a naive deep diff flags every one of them. We therefore
// strip env-specific fields (stripNoise) and match list items on a SEMANTIC key (name),
// never on UID.

import { resolveCreds } from "./config.js";
import { type HttpClient, login, makeClient } from "./copilot-client.js";
import { envelopeRows, isRecord, prop, stringProp } from "./util.js";

// ---- Section catalog (derived from the captured HAR) ----------------------

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
}

// ---- Derived sections: crawl order types -> specialities ------------------
// "Specialties", their "referred (referring) providers", and "referred facilities" are
// not standalone endpoints — they are nested inside each outbound order type's
// /specialities response. We crawl every order type once per env and slice the three
// lists out of the result. The crawl is memoized per client so the three derived
// sections share a single pass.

interface SpecialtyCrawl {
  specialties: { type: string; name: string }[];
  providers: Record<string, unknown>[];
  facilities: Record<string, unknown>[];
}

const asArray = (v: unknown): unknown[] => (Array.isArray(v) ? v : []);
const crawlCache = new WeakMap<HttpClient, Promise<SpecialtyCrawl>>();

function crawlSpecialities(client: HttpClient): Promise<SpecialtyCrawl> {
  const cached = crawlCache.get(client);
  if (cached) return cached;
  const run = (async (): Promise<SpecialtyCrawl> => {
    const typesRes = await client.req("GET", "/api/v1/settings/orders/outbound/types");
    if (typesRes.status >= 400)
      throw new Error(`GET /settings/orders/outbound/types -> ${typesRes.status}`);
    const out: SpecialtyCrawl = { specialties: [], providers: [], facilities: [] };
    for (const t of asArray(typesRes.data)) {
      const typeUid = stringProp(t, "typeUid");
      const typeName = stringProp(t, "name") ?? "";
      if (!typeUid) continue;
      const r = await client.req(
        "GET",
        `/api/v1/settings/orders/outbound/types/${typeUid}/specialities`,
      );
      if (r.status >= 400) throw new Error(`GET specialities(${typeName}) -> ${r.status}`);
      for (const s of envelopeRows(r.data)) {
        out.specialties.push({ type: typeName, name: stringProp(s, "name") ?? "" });
        for (const p of asArray(prop(s, "referredProviders")))
          if (isRecord(p)) out.providers.push(p);
        for (const f of asArray(prop(s, "referredFacilities")))
          if (isRecord(f)) out.facilities.push(f);
      }
    }
    return out;
  })();
  crawlCache.set(client, run);
  return run;
}

const deriveSpecialties = async (c: HttpClient): Promise<unknown[]> =>
  (await crawlSpecialities(c)).specialties;
const deriveReferredProviders = async (c: HttpClient): Promise<unknown[]> =>
  (await crawlSpecialities(c)).providers;
const deriveReferredFacilities = async (c: HttpClient): Promise<unknown[]> =>
  (await crawlSpecialities(c)).facilities;

export const SETTINGS_CATALOG: readonly SettingsSection[] = [
  {
    key: "file-manager",
    label: "File manager settings",
    group: "file-manager",
    path: "/api/v1/settings/fileManager",
    kind: "object",
    envelope: "data.fileManagerSettings",
  },
  {
    key: "orders-outbound",
    label: "Outbound order settings",
    group: "orders",
    path: "/api/v1/settings/orders/outbound",
    kind: "object",
  },
  {
    key: "orders-outbound-types",
    label: "Outbound order types",
    group: "orders",
    path: "/api/v1/settings/orders/outbound/types",
    kind: "list",
    matchKey: "name",
  },
  {
    key: "auto-finish-document-rules",
    label: "Auto-finish document rules",
    group: "documents",
    path: "/api/v1/settings/auto-finish-document-rules",
    kind: "object",
  },
  {
    key: "eligibility-visibility",
    label: "Eligibility visibility",
    group: "eligibility",
    path: "/api/v1/settings/eligibility/eligibilityVisibility",
    kind: "object",
  },
  {
    key: "locations",
    label: "Locations",
    group: "locations",
    path: "/api/v1/settings/locations",
    kind: "list",
    matchKey: "name",
    ignore: ["email"], // dummy locations carry a randomized per-env email
  },
  {
    key: "location-groups",
    label: "Location groups",
    group: "locations",
    path: "/api/v1/settings/locations/groups",
    kind: "list",
    matchKey: "name",
  },
  {
    key: "location-regions",
    label: "Location regions",
    group: "locations",
    path: "/api/v1/settings/locations/regions",
    kind: "list",
    matchKey: "name",
  },
  {
    key: "location-fax-info",
    label: "Location fax info",
    group: "locations",
    path: "/api/v1/settings/locationFaxInfo",
    kind: "list",
    matchKey: "location",
    ignore: ["clinicLogo"], // points at the per-env CDN host
  },
  {
    key: "clinic-payers",
    label: "Clinic payers",
    group: "payers",
    path: "/api/v1/settings/clinic-payers",
    kind: "list",
    envelope: "data",
    matchKey: "Name",
    ignore: ["providerId"],
  },
  {
    key: "document-routing-rules",
    label: "Document routing rules",
    group: "documents",
    path: "/api/v1/settings/document-routing-rules?pageSize=200&pageNumber=0",
    kind: "list",
    envelope: "data",
    // no stable cross-env key -> content-set diff
  },
  {
    key: "document-reviewing-rules-auto",
    label: "Document reviewing rules (Auto Review)",
    group: "documents",
    path: "/api/v1/settings/document-reviewing-rules/Auto%20Review",
    kind: "list",
    envelope: "data",
  },
  {
    key: "document-reviewing-rules-manual",
    label: "Document reviewing rules (Manual Review)",
    group: "documents",
    path: "/api/v1/settings/document-reviewing-rules/Manual%20Review",
    kind: "list",
    envelope: "data",
  },
  {
    key: "referring-entities",
    label: "Referring entities",
    group: "providers",
    path: "/api/v1/settings/referring-entities",
    kind: "list",
    envelope: "data",
    matchKey: "name",
  },
  {
    key: "rendering-providers",
    label: "Rendering providers (clinic)",
    group: "providers",
    path: "/api/v1/clinic/ehr/providers",
    kind: "list",
    matchKey: "name",
  },
  {
    key: "specialties",
    label: "Specialties (across order types)",
    group: "orders",
    path: "/api/v1/settings/orders/outbound/types/*/specialities", // crawled (see derive)
    kind: "list",
    matchKey: "name",
    derive: deriveSpecialties,
  },
  {
    key: "referred-providers",
    label: "Referred (referring) providers",
    group: "providers",
    path: "/api/v1/settings/orders/outbound/types/*/specialities", // crawled (see derive)
    kind: "list",
    matchKey: "name",
    derive: deriveReferredProviders,
  },
  {
    key: "referred-facilities",
    label: "Referred facilities",
    group: "providers",
    path: "/api/v1/settings/orders/outbound/types/*/specialities", // crawled (see derive)
    kind: "list",
    matchKey: "name",
    derive: deriveReferredFacilities,
  },
];

// emrDetailsSettings is keyed by the account's EMR type (e.g. NEXTGEN), so it is
// opt-in: only included when the caller passes `emr`.
const emrSection = (emr: string): SettingsSection => ({
  key: "emr-details",
  label: `EMR details (${emr})`,
  group: "emr",
  path: `/api/v1/settings/emrDetailsSettings/${encodeURIComponent(emr)}`,
  kind: "object",
});

// Distinct top-level groups, in catalog order.
export const settingGroups = (): string[] => [...new Set(SETTINGS_CATALOG.map((s) => s.group))];

// ---- Normalization + diff (pure; unit-tested) -----------------------------

// Stable JSON of an already-normalized value (object keys + arrays are sorted by
// stripNoise, so JSON.stringify is canonical). undefined -> "undefined" so a present
// key with value X never collators with an absent key.
const canonical = (v: unknown): string => JSON.stringify(v) ?? "undefined";

// Fields that differ across envs by construction and carry no semantic meaning.
const NOISE_KEYS = new Set(["createdAt", "updatedAt", "updatedBy"]);
const isNoiseKey = (k: string): boolean => NOISE_KEYS.has(k) || /uid$/i.test(k);

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

export interface FieldDiff {
  path: string;
  prod: unknown;
  preprod: unknown;
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

export interface ListDiff {
  onlyInProd: unknown[];
  onlyInPreProd: unknown[];
  changed: { key: string; diffs: FieldDiff[] }[];
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

// ---- Orchestration --------------------------------------------------------

const getEnvelope = (data: unknown, envelope?: string): unknown => {
  if (!envelope) return data;
  let cur = data;
  for (const part of envelope.split(".")) cur = prop(cur, part);
  return cur;
};

async function fetchSection(client: HttpClient, section: SettingsSection): Promise<unknown> {
  if (section.derive) return section.derive(client);
  const r = await client.req("GET", section.path);
  if (r.status >= 400)
    throw new Error(`GET ${section.path} -> ${r.status}: ${r.text.slice(0, 200)}`);
  return getEnvelope(r.data, section.envelope);
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

function diffSection(
  section: SettingsSection,
  prodData: unknown,
  preData: unknown,
): Omit<SectionResult, "key" | "label"> {
  if (section.kind === "object") {
    const ignore = new Set(section.ignore ?? []);
    const diffs = diffObjects(stripNoise(prodData, ignore), stripNoise(preData, ignore));
    return { kind: "object", equal: diffs.length === 0, diffs };
  }
  const ld = diffList(
    Array.isArray(prodData) ? prodData : [],
    Array.isArray(preData) ? preData : [],
    section,
  );
  const equal = !ld.onlyInProd.length && !ld.onlyInPreProd.length && !ld.changed.length;
  return { kind: "list", equal, ...ld };
}

function catalogWithEmr(emr: string | undefined): SettingsSection[] {
  return emr ? [...SETTINGS_CATALOG, emrSection(emr)] : [...SETTINGS_CATALOG];
}

// Pick sections from the catalog, narrowing by top-level group and/or exact key. Both
// filters apply together (AND). Unknown groups/keys throw a clear, value-listing error.
function selectSections(
  keys: string[] | undefined,
  groups: string[] | undefined,
  emr: string | undefined,
): SettingsSection[] {
  let picked = catalogWithEmr(emr);
  if (groups?.length) {
    const valid = new Set(picked.map((s) => s.group));
    const missing = groups.filter((g) => !valid.has(g));
    if (missing.length)
      throw new Error(`unknown group(s): ${missing.join(", ")}. Known: ${[...valid].join(", ")}`);
    const want = new Set(groups);
    picked = picked.filter((s) => want.has(s.group));
  }
  if (keys?.length) {
    const missing = keys.filter((k) => !catalogWithEmr(emr).some((s) => s.key === k));
    if (missing.length)
      throw new Error(
        `unknown section(s): ${missing.join(", ")}. Known: ${catalogWithEmr(emr)
          .map((s) => s.key)
          .join(", ")}` + (emr ? "" : " (pass `emr` to include emr-details)"),
      );
    const want = new Set(keys);
    picked = picked.filter((s) => want.has(s.key));
  }
  return picked;
}

// ---- Section listing (companion tool — pure, no network) ------------------

export interface SettingSectionInfo {
  key: string;
  label: string;
  group: string;
  kind: "object" | "list";
  derived: boolean; // crawled (heavier) vs a plain single GET
}

// List the catalog sections diff_settings can compare, optionally filtered to one group.
// Pure: reads the static catalog, no creds/network.
export function listSettingSections(opts: { group?: string; emr?: string }): {
  groups: string[];
  sections: SettingSectionInfo[];
} {
  const all = catalogWithEmr(opts.emr);
  if (opts.group && !all.some((s) => s.group === opts.group))
    throw new Error(
      `unknown group: ${opts.group}. Known: ${[...new Set(all.map((s) => s.group))].join(", ")}`,
    );
  const sections = all
    .filter((s) => !opts.group || s.group === opts.group)
    .map((s) => ({
      key: s.key,
      label: s.label,
      group: s.group,
      kind: s.kind,
      derived: !!s.derive,
    }));
  return { groups: [...new Set(all.map((s) => s.group))], sections };
}

const toMessage = (e: unknown): string => (e instanceof Error ? e.message : String(e));

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

// Log into both envs for the account, fetch every selected settings section, and
// return the normalized prod<->pre-prod diff.
export async function diffSettings(opts: DiffSettingsOpts): Promise<DiffSettingsResult> {
  // Select first so an unknown group/section fails fast (no needless login).
  const chosen = selectSections(opts.sections, opts.groups, opts.emr);

  const creds = resolveCreds(opts.profile ?? null);
  const prod = makeClient(creds.prod.be);
  const pre = makeClient(creds.pre_prod.be);
  await Promise.all([
    login(prod, creds.prod.email, creds.prod.password),
    login(pre, creds.pre_prod.email, creds.pre_prod.password),
  ]);

  const results: SectionResult[] = [];
  let withDiffs = 0;

  for (const section of chosen) {
    let result: SectionResult;
    try {
      const [pData, qData] = await Promise.all([
        fetchSection(prod, section),
        fetchSection(pre, section),
      ]);
      result = { key: section.key, label: section.label, ...diffSection(section, pData, qData) };
    } catch (e) {
      result = {
        key: section.key,
        label: section.label,
        kind: section.kind,
        equal: false,
        error: toMessage(e),
      };
    }
    if (!result.equal) withDiffs++;
    results.push(result);
  }

  return {
    account: opts.profile ?? "(default)",
    prodBase: creds.prod.be,
    preProdBase: creds.pre_prod.be,
    sectionsCompared: chosen.length,
    sectionsWithDiffs: withDiffs,
    sections: opts.includeUnchanged ? results : results.filter((s) => !s.equal),
  };
}

// ---- sync_settings (stub) -------------------------------------------------
// The write-side counterpart to diffSettings: ADDITIVELY copy settings items that exist
// in PROD but are missing in PRE-PROD into pre-prod (i.e. the diffList `onlyInProd` items
// per selected section). Additive only — it must never overwrite or delete existing
// pre-prod settings, and leaves items that merely differ (the `changed` set) untouched.
// Deliberately NOT implemented yet — creating items in a tenant needs the per-section
// write endpoints mapped and a dry-run/safety design first. Registered + thrown so the
// tool is discoverable and its intent is documented, while never silently doing nothing.

export class NotImplementedError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "NotImplementedError";
  }
}

export interface SyncSettingsOpts {
  profile?: string | null;
  sections?: string[];
  groups?: string[];
  emr?: string;
  dryRun?: boolean;
}

export function syncSettings(_opts: SyncSettingsOpts): Promise<never> {
  return Promise.reject(
    new NotImplementedError(
      "sync_settings is not implemented yet (stub). It will ADDITIVELY add settings items that " +
        "exist in PROD but are missing in PRE-PROD (the diff_settings `onlyInProd` items) into " +
        "pre-prod — additive only (never overwrites or deletes), pre-prod only, dry-run by default. " +
        "For now use diff_settings to inspect what is missing and add it by hand.",
    ),
  );
}

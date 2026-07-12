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

import { resolveCreds } from "../config/config.js";
import { ExpectedError } from "../mcp/feedback.js";
import { envelopeRows, isRecord, prop, stringProp } from "../shared/util.js";
import { assertPreProdClient, type HttpClient, login, makeClient } from "./copilot-client.js";

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

// ---- Derived section: crawl order types -> order names --------------------
// Each outbound order type carries a paged list of "order names" at
// /types/{uid}/names. They are not a standalone endpoint, so — like specialties — we
// crawl every type once per env and flatten the rows for the diff. Memoized per client.

interface OrderTypeRef {
  typeUid: string;
  name: string;
}

// The order-type list, reduced to {typeUid, name}. Shared by the name crawls.
async function listOrderTypes(client: HttpClient): Promise<OrderTypeRef[]> {
  const r = await client.req("GET", "/api/v1/settings/orders/outbound/types");
  if (r.status >= 400) throw new Error(`GET /settings/orders/outbound/types -> ${r.status}`);
  const out: OrderTypeRef[] = [];
  for (const t of asArray(r.data)) {
    const typeUid = stringProp(t, "typeUid");
    if (typeUid) out.push({ typeUid, name: stringProp(t, "name") ?? "" });
  }
  return out;
}

// The UI always sends this page/sort query; 200 is well above the per-type name count.
const ORDER_NAMES_QS = "?pageSize=200&pageNumber=0&search=&sortAlphabitically=true";

// Order-name rows for one type (envelope `{ data: [...] }`).
async function fetchOrderNames(
  client: HttpClient,
  typeUid: string,
): Promise<Record<string, unknown>[]> {
  const r = await client.req(
    "GET",
    `/api/v1/settings/orders/outbound/types/${typeUid}/names${ORDER_NAMES_QS}`,
  );
  if (r.status >= 400) throw new Error(`GET names(${typeUid}) -> ${r.status}`);
  return envelopeRows(r.data).filter(isRecord);
}

const orderNamesCache = new WeakMap<HttpClient, Promise<Record<string, unknown>[]>>();
function crawlOrderNamesFlat(client: HttpClient): Promise<Record<string, unknown>[]> {
  const cached = orderNamesCache.get(client);
  if (cached) return cached;
  const run = (async (): Promise<Record<string, unknown>[]> => {
    const rows: Record<string, unknown>[] = [];
    for (const t of await listOrderTypes(client))
      rows.push(...(await fetchOrderNames(client, t.typeUid)));
    return rows;
  })();
  orderNamesCache.set(client, run);
  return run;
}

const deriveOrderNames = async (c: HttpClient): Promise<unknown[]> => crawlOrderNamesFlat(c);

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
  {
    key: "orders",
    label: "Orders (across order types)",
    group: "orders",
    path: "/api/v1/settings/orders/outbound/types/*/names", // crawled (see derive)
    kind: "list",
    matchKey: "name",
    derive: deriveOrderNames,
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
  matchKey?: string; // list sections: the field items are matched/scoped by (omitted => content-set diff)
}

// List the catalog sections diff_settings can compare, optionally narrowed by top-level
// group and/or an explicit set of section keys (both applied as AND). Pure: reads the
// static catalog, no creds/network. Unknown groups/keys throw a clear, value-listing error.
export function listSettingSections(opts: { group?: string; sections?: string[]; emr?: string }): {
  groups: string[];
  sections: SettingSectionInfo[];
} {
  const all = catalogWithEmr(opts.emr);
  const allGroups = [...new Set(all.map((s) => s.group))];
  if (opts.group && !allGroups.includes(opts.group))
    throw new Error(`unknown group: ${opts.group}. Known: ${allGroups.join(", ")}`);
  if (opts.sections?.length) {
    const known = new Set(all.map((s) => s.key));
    const missing = opts.sections.filter((k) => !known.has(k));
    if (missing.length)
      throw new Error(
        `unknown section(s): ${missing.join(", ")}. Known: ${[...known].join(", ")}` +
          (opts.emr ? "" : " (pass `emr` to include emr-details)"),
      );
  }
  const want = opts.sections?.length ? new Set(opts.sections) : undefined;
  const sections = all
    .filter((s) => (!opts.group || s.group === opts.group) && (!want || want.has(s.key)))
    .map((s) => ({
      key: s.key,
      label: s.label,
      group: s.group,
      kind: s.kind,
      derived: !!s.derive,
      ...(s.matchKey ? { matchKey: s.matchKey } : {}),
    }));
  return { groups: allGroups, sections };
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
  const prod = makeClient(creds.prod.be, "prod");
  const pre = makeClient(creds.pre_prod.be, "pre_prod");
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

// ---- get_settings (single-env read) ----------------------------------------

// stripNoise with a section's per-section ignore set — the same normalization the diff
// applies to each side. Exposed pure so get_settings' normalized=true output is
// byte-comparable with what diff_settings compared.
export const normalizeSectionValue = (
  section: Pick<SettingsSection, "ignore">,
  data: unknown,
): unknown => stripNoise(data, new Set(section.ignore ?? []));

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

// Fetch the selected settings sections from ONE env — the single-env counterpart to
// diffSettings. Read-only. Stripped by default (same noise-stripping as diffSettings);
// normalized=false returns the raw payload with real UIDs/timestamps visible.
export async function getSettings(opts: GetSettingsOpts): Promise<GetSettingsResult> {
  // Select first so an unknown group/section fails fast (no needless login).
  const chosen = selectSections(opts.sections, opts.groups, opts.emr);

  const creds = resolveCreds(opts.profile ?? null)[opts.env];
  const client = makeClient(creds.be, opts.env);
  await login(client, creds.email, creds.password);

  const sections: GetSettingsSection[] = [];
  for (const section of chosen) {
    const head = { key: section.key, label: section.label, group: section.group };
    try {
      const raw = await fetchSection(client, section);
      const data = opts.normalized ? normalizeSectionValue(section, raw) : raw;
      sections.push({
        ...head,
        kind: section.kind,
        ...(Array.isArray(data) ? { count: data.length } : {}),
        data,
      });
    } catch (e) {
      sections.push({ ...head, kind: section.kind, error: toMessage(e) });
    }
  }

  return {
    account: opts.profile ?? "(default)",
    env: opts.env,
    base: creds.be,
    sectionsFetched: chosen.length,
    sections,
  };
}

// ---- settings sync (plan/apply) --------------------------------------------
// The write-side counterpart to diffSettings: ADDITIVELY copy settings that exist in PROD
// but are missing in PRE-PROD into pre-prod. Additive only — never overwrites or deletes
// existing pre-prod settings; pre-prod only. Split into two operations:
//   - planSettingsSyncOp: read-only — compute the planned actions, each with a stable id.
//   - applySettingsSync:  re-plans server-side (never accepts bodies from the caller) and
//                         executes only the actions the caller selected by id / all=true.
//
// Scope: the outbound order-type SPECIALITIES domain (the resource behind the three derived
// diff sections `specialties` / `referred-providers` / `referred-facilities`). Two additive
// ops, both verified against captured HARs:
//   - create: a prod-only speciality (with its facilities/providers) ->
//             POST /api/v1/settings/orders/outbound/types/{preTypeUid}/specialities
//   - merge:  a speciality present in both, with prod-only facilities/providers ->
//             PUT  /api/v1/settings/specialities/{preSpecialityUid}  (full-replace body, so we
//             send the EXISTING pre-prod speciality + only the prod-only additions)
// Other catalog sections have no verified write endpoint yet and are reported as skipped.
//
// Crucial: referredFacilities[].payersProviderId[].payerUid is a real foreign key to a payer,
// and payer UIDs differ per env — so we REMAP payerUid prod->pre (payers matched by name),
// never strip it. Own-record UIDs (specialityUid, referredFacilityUid, timestamps) ARE
// stripped on create (the BE assigns fresh ones).

// Kept as a representative ExpectedError for feedback classification tests; no longer thrown.
export class NotImplementedError extends ExpectedError {
  constructor(message: string) {
    super(message);
    this.name = "NotImplementedError";
  }
}

// ---- rich speciality crawl (separate from the diff crawl, which flattens away context) ----

interface CrawledType {
  typeUid: string;
  name: string;
  specialities: Record<string, unknown>[]; // full speciality objects (referredFacilities, etc.)
}
export interface SpecialityTree {
  types: CrawledType[];
}

async function crawlSpecialityTree(client: HttpClient): Promise<SpecialityTree> {
  const typesRes = await client.req("GET", "/api/v1/settings/orders/outbound/types");
  if (typesRes.status >= 400)
    throw new Error(`GET /settings/orders/outbound/types -> ${typesRes.status}`);
  const types: CrawledType[] = [];
  for (const t of asArray(typesRes.data)) {
    const typeUid = stringProp(t, "typeUid");
    const name = stringProp(t, "name") ?? "";
    if (!typeUid) continue;
    const r = await client.req(
      "GET",
      `/api/v1/settings/orders/outbound/types/${typeUid}/specialities`,
    );
    if (r.status >= 400) throw new Error(`GET specialities(${name}) -> ${r.status}`);
    types.push({ typeUid, name, specialities: envelopeRows(r.data).filter(isRecord) });
  }
  return { types };
}

// ---- payer prod->pre UID remap --------------------------------------------

// Candidate keys for a payer's own UID in the clinic-payers list (exact field unconfirmed —
// fall back to any *Uid key). A wrong/empty map drops payer links with a warning rather than
// linking to the wrong payer, so the miss is visible in dry-run output.
const PAYER_UID_KEYS = ["payerUid", "uid"];
const payerUidOf = (p: unknown): string | undefined => {
  for (const k of PAYER_UID_KEYS) {
    const v = stringProp(p, k);
    if (v) return v;
  }
  if (isRecord(p))
    for (const k of Object.keys(p))
      if (/uid$/i.test(k)) {
        const v = stringProp(p, k);
        if (v) return v;
      }
  return undefined;
};

// name -> own payerUid for one env (source: clinic-payers, envelope `data`, key `Name`).
async function fetchPayerUidsByName(client: HttpClient): Promise<Map<string, string>> {
  const r = await client.req("GET", "/api/v1/settings/clinic-payers");
  if (r.status >= 400) throw new Error(`GET /settings/clinic-payers -> ${r.status}`);
  const out = new Map<string, string>();
  for (const p of asArray(getEnvelope(r.data, "data"))) {
    const name = stringProp(p, "Name");
    const uid = payerUidOf(p);
    if (name && uid) out.set(name, uid);
  }
  return out;
}

// prod payerUid -> pre-prod payerUid, by matching payers across envs on name.
async function buildPayerMap(prod: HttpClient, pre: HttpClient): Promise<Map<string, string>> {
  const [prodByName, preByName] = await Promise.all([
    fetchPayerUidsByName(prod),
    fetchPayerUidsByName(pre),
  ]);
  const map = new Map<string, string>();
  for (const [name, prodUid] of prodByName) {
    const preUid = preByName.get(name);
    if (preUid) map.set(prodUid, preUid);
  }
  return map;
}

// Rewrite each link's payerUid prod->pre; drop (and report) links with no pre-prod match.
export function remapPayerLinks(
  links: unknown,
  payerMap: ReadonlyMap<string, string>,
): { links: Record<string, unknown>[]; dropped: string[] } {
  const out: Record<string, unknown>[] = [];
  const dropped: string[] = [];
  for (const l of asArray(links)) {
    if (!isRecord(l)) continue;
    const prodUid = stringProp(l, "payerUid");
    if (!prodUid) {
      out.push({ ...l }); // link carries no payer ref — keep verbatim
      continue;
    }
    const preUid = payerMap.get(prodUid);
    if (!preUid) {
      dropped.push(prodUid);
      continue;
    }
    out.push({ ...l, payerUid: preUid });
  }
  return { links: out, dropped };
}

// ---- create/merge body builders (pure; unit-tested) -----------------------

// Drop own-record UIDs + timestamps (the BE assigns fresh ones on create).
const stripOwnUids = (rec: Record<string, unknown>): Record<string, unknown> => {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(rec)) if (!isNoiseKey(k)) out[k] = v;
  return out;
};

// Clean a prod facility/provider for writing into pre-prod: strip its own UIDs and remap the
// nested payersProviderId links prod->pre.
export function cleanReferralForWrite(
  item: unknown,
  payerMap: ReadonlyMap<string, string>,
): { item: Record<string, unknown>; droppedPayers: string[] } {
  const base = isRecord(item) ? stripOwnUids(item) : {};
  let droppedPayers: string[] = [];
  if (Object.hasOwn(base, "payersProviderId")) {
    const { links, dropped } = remapPayerLinks(base["payersProviderId"], payerMap);
    base["payersProviderId"] = links;
    droppedPayers = dropped;
  }
  return { item: base, droppedPayers };
}

const cleanMany = (
  items: unknown[],
  payerMap: ReadonlyMap<string, string>,
): { items: Record<string, unknown>[]; droppedPayers: string[] } => {
  const out: Record<string, unknown>[] = [];
  const droppedPayers: string[] = [];
  for (const it of items) {
    const c = cleanReferralForWrite(it, payerMap);
    out.push(c.item);
    droppedPayers.push(...c.droppedPayers);
  }
  return { items: out, droppedPayers };
};

// POST body to create a whole prod-only speciality under a pre-prod type. Matches the verified
// HAR shape: { name, referredFacilities, referredProviders? }.
export function buildSpecialityCreateBody(
  prodSpeciality: unknown,
  payerMap: ReadonlyMap<string, string>,
): { body: Record<string, unknown>; droppedPayers: string[] } {
  const facs = cleanMany(asArray(prop(prodSpeciality, "referredFacilities")), payerMap);
  const provsRaw = asArray(prop(prodSpeciality, "referredProviders"));
  const provs = cleanMany(provsRaw, payerMap);
  const body: Record<string, unknown> = {
    name: stringProp(prodSpeciality, "name") ?? "",
    referredFacilities: facs.items,
  };
  if (provsRaw.length) body["referredProviders"] = provs.items;
  return { body, droppedPayers: [...facs.droppedPayers, ...provs.droppedPayers] };
}

// PUT body to additively merge prod-only facilities/providers into an EXISTING pre-prod
// speciality. PUT is full-replace, so we keep the existing pre-prod facilities/providers
// verbatim (their referredFacilityUid + already-valid pre-prod payer links) and append only
// the cleaned, payer-remapped prod-only additions.
export function buildMergedSpecialityBody(
  preSpeciality: unknown,
  prodOnlyFacilities: unknown[],
  prodOnlyProviders: unknown[],
  payerMap: ReadonlyMap<string, string>,
): { body: Record<string, unknown>; droppedPayers: string[] } {
  const existingFacs = asArray(prop(preSpeciality, "referredFacilities")).filter(isRecord);
  const existingProvs = asArray(prop(preSpeciality, "referredProviders")).filter(isRecord);
  const newFacs = cleanMany(prodOnlyFacilities, payerMap);
  const newProvs = cleanMany(prodOnlyProviders, payerMap);
  const body: Record<string, unknown> = {
    name: stringProp(preSpeciality, "name") ?? "",
    referredFacilities: [...existingFacs, ...newFacs.items],
  };
  if (existingProvs.length || newProvs.items.length)
    body["referredProviders"] = [...existingProvs, ...newProvs.items];
  return { body, droppedPayers: [...newFacs.droppedPayers, ...newProvs.droppedPayers] };
}

// ---- plan (pure over crawled trees + payer map; unit-tested) --------------

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

const referralName = (x: unknown): string => stringProp(x, "name") ?? "";
const missingByName = (prodArr: unknown[], preArr: unknown[]): unknown[] => {
  const have = new Set(preArr.map(referralName));
  return prodArr.filter((x) => !have.has(referralName(x)));
};
const payerWarning = (dropped: string[]): string =>
  `${dropped.length} payer link(s) dropped (no matching pre-prod payer): ${[...new Set(dropped)].join(", ")}`;

// Match order types by name, then specialities by name; emit create actions for prod-only
// specialities and merge actions for specialities whose facilities/providers are a superset in
// prod. Pure — no network. Types/specialities that can't be placed are reported in `skipped`.
export function planSpecialitySync(
  prodTree: SpecialityTree,
  preTree: SpecialityTree,
  payerMap: ReadonlyMap<string, string>,
): { actions: SyncAction[]; skipped: SyncSkip[] } {
  const preTypeByName = new Map(preTree.types.map((t) => [t.name, t]));
  const actions: SyncAction[] = [];
  const skipped: SyncSkip[] = [];

  for (const pType of prodTree.types) {
    const qType = preTypeByName.get(pType.name);
    if (!qType) {
      skipped.push({
        typeName: pType.name,
        reason: "order type missing in pre-prod (order types are not auto-created)",
      });
      continue;
    }
    const preSpecByName = new Map(qType.specialities.map((s) => [referralName(s), s]));

    for (const pSpec of pType.specialities) {
      const sName = referralName(pSpec);
      const qSpec = preSpecByName.get(sName);

      if (!qSpec) {
        const { body, droppedPayers } = buildSpecialityCreateBody(pSpec, payerMap);
        actions.push({
          section: "specialties",
          op: "create",
          itemKind: "speciality",
          typeName: pType.name,
          itemName: sName,
          method: "POST",
          path: `/api/v1/settings/orders/outbound/types/${qType.typeUid}/specialities`,
          body,
          ...(droppedPayers.length ? { warnings: [payerWarning(droppedPayers)] } : {}),
        });
        continue;
      }

      const missFacs = missingByName(
        asArray(prop(pSpec, "referredFacilities")),
        asArray(prop(qSpec, "referredFacilities")),
      );
      const missProvs = missingByName(
        asArray(prop(pSpec, "referredProviders")),
        asArray(prop(qSpec, "referredProviders")),
      );
      if (!missFacs.length && !missProvs.length) continue; // already in sync

      const preSpecUid = stringProp(qSpec, "specialityUid");
      if (!preSpecUid) {
        skipped.push({
          typeName: pType.name,
          specialityName: sName,
          reason: "speciality has no specialityUid in pre-prod (cannot target the update endpoint)",
        });
        continue;
      }
      const { body, droppedPayers } = buildMergedSpecialityBody(
        qSpec,
        missFacs,
        missProvs,
        payerMap,
      );
      actions.push({
        section: "specialties",
        op: "merge",
        itemKind: "speciality",
        typeName: pType.name,
        itemName: sName,
        method: "PUT",
        path: `/api/v1/settings/specialities/${preSpecUid}`,
        body,
        ...(droppedPayers.length ? { warnings: [payerWarning(droppedPayers)] } : {}),
      });
    }
  }
  return { actions, skipped };
}

// ---- orders domain (create prod-only orders in pre-prod) ------------------
// The write-side counterpart to the `orders` diff section (the UI's "Orders" section). Each
// outbound order type holds a list of orders; each order references facilities, auth/referral sub-categories,
// and per-CPT payers. The create endpoint (verified against a captured HAR) is
//   POST /api/v1/settings/orders/outbound/types/{preTypeUid}/names
// with a body of UID *references* (facilitiesUids, authSubCategoryUids, referralSubCategoryUids,
// CPTCodes[].payers map). Those UIDs are per-env, so — exactly like payer links in the specialities
// syncer — we REMAP every reference prod->pre by NAME and DROP (with a warning) any that has no
// pre-prod match, rather than copy a prod UID that would dangle or point at the wrong entity.
// Create-only: order names present in both envs are left untouched (there is no verified
// name-update endpoint, and this tool never overwrites).

export interface OrderNameType {
  typeUid: string;
  name: string;
  names: Record<string, unknown>[];
}
export interface OrderNameTree {
  types: OrderNameType[];
}

async function crawlOrderNameTree(client: HttpClient): Promise<OrderNameTree> {
  const types: OrderNameType[] = [];
  for (const t of await listOrderTypes(client))
    types.push({
      typeUid: t.typeUid,
      name: t.name,
      names: await fetchOrderNames(client, t.typeUid),
    });
  return { types };
}

// The pre-prod name->UID lookups needed to remap one order type's references prod->pre.
export interface PreTypeRefs {
  facilityUidByName: ReadonlyMap<string, string>;
  authSubCatUidByName: ReadonlyMap<string, string>;
  referralSubCatUidByName: ReadonlyMap<string, string>;
}

// Pre-prod facilities for a type: name -> referredFacilityUid (verified endpoint; the fullest
// source, since it includes facilities not yet referenced by any name).
async function fetchFacilityUidByName(
  client: HttpClient,
  typeUid: string,
): Promise<Map<string, string>> {
  const r = await client.req("GET", `/api/v1/settings/specialities/facilities?typeUid=${typeUid}`);
  if (r.status >= 400) throw new Error(`GET facilities(${typeUid}) -> ${r.status}`);
  const rows = Array.isArray(r.data) ? r.data : envelopeRows(r.data);
  const m = new Map<string, string>();
  for (const f of rows) {
    const name = stringProp(f, "name");
    const uid = stringProp(f, "referredFacilityUid");
    if (name && uid) m.set(name, uid);
  }
  return m;
}

// Sub-categories have no list endpoint, so harvest name -> subCategoryUid from the sub-category
// objects embedded in the (pre-prod) crawled name rows of one type.
function harvestSubCatUidByName(
  names: Record<string, unknown>[],
  field: string,
): Map<string, string> {
  const m = new Map<string, string>();
  for (const n of names)
    for (const s of asArray(prop(n, field))) {
      const name = stringProp(s, "subCategoryName");
      const uid = stringProp(s, "subCategoryUid");
      if (name && uid) m.set(name, uid);
    }
  return m;
}

// Build the per-type pre-prod ref maps, keyed by order-type NAME (the cross-env match key).
async function buildPreTypeRefs(
  pre: HttpClient,
  preTree: OrderNameTree,
): Promise<Map<string, PreTypeRefs>> {
  const out = new Map<string, PreTypeRefs>();
  for (const t of preTree.types) {
    out.set(t.name, {
      facilityUidByName: await fetchFacilityUidByName(pre, t.typeUid),
      authSubCatUidByName: harvestSubCatUidByName(t.names, "authSubCategories"),
      referralSubCatUidByName: harvestSubCatUidByName(t.names, "referralSubCategories"),
    });
  }
  return out;
}

// Rewrite a CPT's payers map prod->pre: remap both the map key and each entry's inner payerUid,
// dropping (and reporting, by payer name) any payer with no pre-prod match.
export function remapPayersMap(
  payers: unknown,
  payerMap: ReadonlyMap<string, string>,
): { payers: Record<string, unknown>; dropped: string[] } {
  const out: Record<string, unknown> = {};
  const dropped: string[] = [];
  if (isRecord(payers))
    for (const [prodUid, val] of Object.entries(payers)) {
      const preUid = payerMap.get(prodUid);
      if (!preUid) {
        dropped.push(stringProp(val, "Name") ?? prodUid);
        continue;
      }
      out[preUid] = isRecord(val) ? { ...val, payerUid: preUid } : val;
    }
  return { payers: out, dropped };
}

// Resolve a list of named reference objects to their pre-prod UIDs by name; unmatched names are
// dropped and returned so the caller can warn.
export function remapNamedRefs(
  items: unknown,
  nameField: string,
  byName: ReadonlyMap<string, string>,
): { uids: string[]; dropped: string[] } {
  const uids: string[] = [];
  const dropped: string[] = [];
  for (const it of asArray(items)) {
    const name = stringProp(it, nameField);
    if (!name) continue;
    const uid = byName.get(name);
    if (uid) uids.push(uid);
    else dropped.push(name);
  }
  return { uids, dropped };
}

// One CPT cleaned for the create body: drop its own codeUid, remap the payers map prod->pre.
function cleanCptForWrite(
  cpt: unknown,
  payerMap: ReadonlyMap<string, string>,
): { cpt: Record<string, unknown>; dropped: string[] } {
  const base = isRecord(cpt) ? stripOwnUids(cpt) : {};
  if (Object.hasOwn(base, "payers")) {
    const { payers, dropped } = remapPayersMap(base["payers"], payerMap);
    base["payers"] = payers;
    return { cpt: base, dropped };
  }
  return { cpt: base, dropped: [] };
}

// POST body to create a whole prod-only order name under a pre-prod type. Matches the verified HAR
// shape: { name, CPTCodes, facilitiesUids, authSubCategoryUids, referralSubCategoryUids,
// mandatoryICDCodes }. Every UID reference is remapped prod->pre by name; unmatched refs/payers
// are dropped and reported.
export function buildOrderNameCreateBody(
  prodRow: unknown,
  refs: PreTypeRefs,
  payerMap: ReadonlyMap<string, string>,
): { body: Record<string, unknown>; droppedPayers: string[]; droppedRefs: string[] } {
  const cpts: Record<string, unknown>[] = [];
  const droppedPayers: string[] = [];
  for (const c of asArray(prop(prodRow, "outboundReferralOrderTypeNameCPTCodes"))) {
    const r = cleanCptForWrite(c, payerMap);
    cpts.push(r.cpt);
    droppedPayers.push(...r.dropped);
  }
  const fac = remapNamedRefs(prop(prodRow, "referredFacilities"), "name", refs.facilityUidByName);
  const auth = remapNamedRefs(
    prop(prodRow, "authSubCategories"),
    "subCategoryName",
    refs.authSubCatUidByName,
  );
  const ref = remapNamedRefs(
    prop(prodRow, "referralSubCategories"),
    "subCategoryName",
    refs.referralSubCatUidByName,
  );
  const droppedRefs = [
    ...fac.dropped.map((n) => `facility '${n}'`),
    ...auth.dropped.map((n) => `auth sub-category '${n}'`),
    ...ref.dropped.map((n) => `referral sub-category '${n}'`),
  ];
  const body: Record<string, unknown> = {
    name: stringProp(prodRow, "name") ?? "",
    CPTCodes: cpts,
    facilitiesUids: fac.uids,
    authSubCategoryUids: auth.uids,
    referralSubCategoryUids: ref.uids,
    mandatoryICDCodes: asArray(prop(prodRow, "mandatoryICDCodes")),
  };
  return { body, droppedPayers, droppedRefs };
}

const nameOf = (x: unknown): string => stringProp(x, "name") ?? "";
const refWarning = (dropped: string[]): string =>
  `${dropped.length} reference(s) dropped (no matching pre-prod entity by name): ${[...new Set(dropped)].join(", ")}`;

// Match order types by name, then order names by name; emit a create action for each prod-only
// order name. Pure — the per-type pre-prod ref maps and payer map are supplied by the caller.
// Existing (both-env) names are left untouched; types missing in pre-prod are reported in `skipped`.
export function planOrderNameSync(
  prodTree: OrderNameTree,
  preTree: OrderNameTree,
  preRefsByType: ReadonlyMap<string, PreTypeRefs>,
  payerMap: ReadonlyMap<string, string>,
): { actions: SyncAction[]; skipped: SyncSkip[] } {
  const preTypeByName = new Map(preTree.types.map((t) => [t.name, t]));
  const emptyRefs: PreTypeRefs = {
    facilityUidByName: new Map(),
    authSubCatUidByName: new Map(),
    referralSubCatUidByName: new Map(),
  };
  const actions: SyncAction[] = [];
  const skipped: SyncSkip[] = [];

  for (const pType of prodTree.types) {
    const qType = preTypeByName.get(pType.name);
    if (!qType) {
      skipped.push({
        typeName: pType.name,
        reason: "order type missing in pre-prod (order types are not auto-created)",
      });
      continue;
    }
    const refs = preRefsByType.get(pType.name) ?? emptyRefs;
    const preNames = new Set(qType.names.map(nameOf));
    for (const pName of pType.names) {
      const nm = nameOf(pName);
      if (preNames.has(nm)) continue; // present in both — create-only, leave untouched
      const { body, droppedPayers, droppedRefs } = buildOrderNameCreateBody(pName, refs, payerMap);
      const warnings = [
        ...(droppedPayers.length ? [payerWarning(droppedPayers)] : []),
        ...(droppedRefs.length ? [refWarning(droppedRefs)] : []),
      ];
      actions.push({
        section: "orders",
        op: "create",
        itemKind: "order",
        typeName: pType.name,
        itemName: nm,
        method: "POST",
        path: `/api/v1/settings/orders/outbound/types/${qType.typeUid}/names`,
        body,
        ...(warnings.length ? { warnings } : {}),
      });
    }
  }
  return { actions, skipped };
}

// ---- action ids + apply selection (pure; unit-tested) ----------------------

// Stable, human-readable id for one planned action, derived from its semantic fields —
// stable across plan runs as long as the underlying drift is the same. Colons inside
// names are fine: ids are opaque match tokens, never parsed back apart.
export const actionId = (a: Pick<SyncAction, "section" | "op" | "typeName" | "itemName">): string =>
  `${a.section}:${a.op}:${a.typeName}:${a.itemName}`;

export type PlannedSyncAction = SyncAction & { id: string };

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
export interface ApplyFilter {
  actionIds?: string[];
  all?: boolean;
}

const assertApplyFilter = (filter: ApplyFilter): void => {
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
  const count = (field: string): number => asArray(prop(a.body, field)).length;
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

// ---- orchestration --------------------------------------------------------

// Catalog section keys whose additive write is handled by the specialities syncer.
const SPECIALITY_SYNC_SECTIONS = ["specialties", "referred-providers", "referred-facilities"];
// Catalog section key handled by the orders syncer.
const ORDER_NAME_SYNC_SECTIONS = ["orders"];

// The crawled + planned state shared by plan and apply: apply re-runs this exact
// computation rather than accepting bodies from the caller.
interface SyncPlanContext {
  account: string;
  prodBase: string;
  preProdBase: string;
  actions: PlannedSyncAction[];
  skipped: SyncSkip[];
  skippedSections: { key: string; reason: string }[];
  pre: HttpClient | null; // logged-in pre-prod client; null when nothing syncable was selected (no login done)
}

async function buildSyncPlan(opts: {
  profile?: string | null;
  sections?: string[];
  groups?: string[];
  emr?: string;
}): Promise<SyncPlanContext> {
  // Validate + resolve the selection up front (unknown group/section fails fast, no login).
  const chosen = selectSections(opts.sections, opts.groups, opts.emr);
  const chosenKeys = new Set(chosen.map((s) => s.key));
  const runSpeciality = SPECIALITY_SYNC_SECTIONS.some((k) => chosenKeys.has(k));
  const runOrderNames = ORDER_NAME_SYNC_SECTIONS.some((k) => chosenKeys.has(k));

  const covered = new Set([
    ...(runSpeciality ? SPECIALITY_SYNC_SECTIONS : []),
    ...(runOrderNames ? ORDER_NAME_SYNC_SECTIONS : []),
  ]);
  const skippedSections = chosen
    .filter((s) => !covered.has(s.key))
    .map((s) => ({ key: s.key, reason: "no write mapping yet — additive sync not implemented" }));

  const creds = resolveCreds(opts.profile ?? null);
  const ctx: SyncPlanContext = {
    account: opts.profile ?? "(default)",
    prodBase: creds.prod.be,
    preProdBase: creds.pre_prod.be,
    actions: [],
    skipped: [],
    skippedSections,
    pre: null,
  };
  if (!runSpeciality && !runOrderNames) return ctx; // nothing syncable selected — report skipped only

  const prod = makeClient(creds.prod.be, "prod");
  const pre = makeClient(creds.pre_prod.be, "pre_prod");
  await Promise.all([
    login(prod, creds.prod.email, creds.prod.password),
    login(pre, creds.pre_prod.email, creds.pre_prod.password),
  ]);

  // Both domains remap payer references prod->pre, so build the payer map once and share it.
  const payerMap = await buildPayerMap(prod, pre);
  const actions: SyncAction[] = [];
  const skipped: SyncSkip[] = [];

  if (runSpeciality) {
    const [prodTree, preTree] = await Promise.all([
      crawlSpecialityTree(prod),
      crawlSpecialityTree(pre),
    ]);
    const s = planSpecialitySync(prodTree, preTree, payerMap);
    actions.push(...s.actions);
    skipped.push(...s.skipped);
  }
  if (runOrderNames) {
    const [prodTree, preTree] = await Promise.all([
      crawlOrderNameTree(prod),
      crawlOrderNameTree(pre),
    ]);
    const preRefsByType = await buildPreTypeRefs(pre, preTree);
    const o = planOrderNameSync(prodTree, preTree, preRefsByType, payerMap);
    actions.push(...o.actions);
    skipped.push(...o.skipped);
  }

  ctx.actions = assignActionIds(actions);
  ctx.skipped = skipped;
  ctx.pre = pre;
  return ctx;
}

// ---- plan_settings_sync ----------------------------------------------------

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
  };
}

// ---- apply_settings_sync ---------------------------------------------------

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
  const executed: ExecutedAction[] = [];
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

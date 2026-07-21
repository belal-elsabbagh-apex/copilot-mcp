// Specialities domain: crawl order types -> specialities, and the additive write/sync plan
// for the `specialties` / `referred-providers` / `referred-facilities` catalog sections.
//
// "Specialties", their "referred (referring) providers", and "referred facilities" are not
// standalone endpoints — they are nested inside each outbound order type's /specialities
// response. We crawl every order type once per env and slice the three lists out of the
// result. The crawl is memoized per client so the three derived sections share a single pass.
//
// Sync side: two additive ops, both verified against captured HARs:
//   - create: a prod-only speciality (with its facilities/providers) ->
//             POST /api/v1/settings/orders/outbound/types/{preTypeUid}/specialities
//   - merge:  a speciality present in both, with prod-only facilities/providers ->
//             PUT  /api/v1/settings/specialities/{preSpecialityUid}  (full-replace body, so we
//             send the EXISTING pre-prod speciality + only the prod-only additions)
//
// Crucial: referredFacilities[].payersProviderId[].payerUid is a real foreign key to a payer,
// and payer UIDs differ per env — so we REMAP payerUid prod->pre (payers matched by name),
// never strip it. Own-record UIDs (specialityUid, referredFacilityUid, timestamps) ARE
// stripped on create (the BE assigns fresh ones).

import { envelopeRows, isRecord, prop, stringProp } from "../../shared/util.js";
import type { HttpClient } from "../copilot-client.js";
import { NOISE_KEYS } from "./diff-engine.js";
import { asArray, stripOwnUids } from "./internal.js";
import type { PayerLinkFinding, SectionSyncer, SyncAction, SyncSkip } from "./types.js";

// ---- flattened crawl, used by the diff-path derived sections ---------------

export interface SpecialtyCrawl {
  specialties: { type: string; name: string }[];
  providers: Record<string, unknown>[];
  facilities: Record<string, unknown>[];
}

const crawlCache = new WeakMap<HttpClient, Promise<SpecialtyCrawl>>();

export function crawlSpecialities(client: HttpClient): Promise<SpecialtyCrawl> {
  const cached = crawlCache.get(client);
  if (cached) return cached;
  const run = (async (): Promise<SpecialtyCrawl> => {
    const typesRes = await client.req("GET", "/api/v1/settings/orders/outbound/types");
    if (typesRes.status >= 400)
      throw new Error(`GET /settings/orders/outbound/types -> ${typesRes.status}`);
    const validTypes = asArray(typesRes.data)
      .map((t) => ({ typeUid: stringProp(t, "typeUid"), typeName: stringProp(t, "name") ?? "" }))
      .filter((t): t is { typeUid: string; typeName: string } => !!t.typeUid);
    // Every type's specialities GET is independent of every other type's — fetch them
    // concurrently instead of paying each round-trip in turn.
    const perType = await Promise.all(
      validTypes.map(async ({ typeUid, typeName }) => {
        const r = await client.req(
          "GET",
          `/api/v1/settings/orders/outbound/types/${typeUid}/specialities`,
        );
        if (r.status >= 400) throw new Error(`GET specialities(${typeName}) -> ${r.status}`);
        return { typeName, rows: envelopeRows(r.data) };
      }),
    );
    const out: SpecialtyCrawl = { specialties: [], providers: [], facilities: [] };
    for (const { typeName, rows } of perType) {
      for (const s of rows) {
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

export const deriveSpecialties = async (c: HttpClient): Promise<unknown[]> =>
  (await crawlSpecialities(c)).specialties;
export const deriveReferredProviders = async (c: HttpClient): Promise<unknown[]> =>
  (await crawlSpecialities(c)).providers;
export const deriveReferredFacilities = async (c: HttpClient): Promise<unknown[]> =>
  (await crawlSpecialities(c)).facilities;

// ---- rich crawl, used by the sync-path planner (keeps full nested context) ----

export interface CrawledType {
  typeUid: string;
  name: string;
  specialities: Record<string, unknown>[]; // full speciality objects (referredFacilities, etc.)
}
export interface SpecialityTree {
  types: CrawledType[];
}

export async function crawlSpecialityTree(client: HttpClient): Promise<SpecialityTree> {
  const typesRes = await client.req("GET", "/api/v1/settings/orders/outbound/types");
  if (typesRes.status >= 400)
    throw new Error(`GET /settings/orders/outbound/types -> ${typesRes.status}`);
  const validTypes = asArray(typesRes.data)
    .map((t) => ({ typeUid: stringProp(t, "typeUid"), name: stringProp(t, "name") ?? "" }))
    .filter((t): t is { typeUid: string; name: string } => !!t.typeUid);
  // Same independence as crawlSpecialities — fetch every type's specialities concurrently.
  const types = await Promise.all(
    validTypes.map(async ({ typeUid, name }): Promise<CrawledType> => {
      const r = await client.req(
        "GET",
        `/api/v1/settings/orders/outbound/types/${typeUid}/specialities`,
      );
      if (r.status >= 400) throw new Error(`GET specialities(${name}) -> ${r.status}`);
      return { typeUid, name, specialities: envelopeRows(r.data).filter(isRecord) };
    }),
  );
  return { types };
}

// ---- payer-link remap (own to this domain) ---------------------------------

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

// ---- create/merge body builders (pure; unit-tested) -------------------------

// The parent speciality's own uid, echoed onto every facility/provider row by the GET (like
// specialityName in NOISE_KEYS) — must be dropped, but unlike NOISE_KEYS this can't be matched
// generically: the row's OWN uid (referredFacilityUid / referredProviderUid) also ends in "Uid"
// and must survive.
const ECHOED_PARENT_UID_KEY = "specialityUid";

// Existing pre-prod facility/provider rows are kept as-is on a merge PUT (never treated as new,
// so their own uid must survive) but still carry echo fields off the GET response
// (specialityUid/specialityName/createdAt/updatedAt) that the real UI does not resend (verified
// via a captured HAR of the "edit specialty" PUT). Strip only those.
const stripEchoedFields = (item: unknown): Record<string, unknown> => {
  if (!isRecord(item)) return {};
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(item))
    if (!NOISE_KEYS.has(k) && k !== ECHOED_PARENT_UID_KEY) out[k] = v;
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
  const existingFacs = asArray(prop(preSpeciality, "referredFacilities"))
    .filter(isRecord)
    .map(stripEchoedFields);
  const existingProvs = asArray(prop(preSpeciality, "referredProviders"))
    .filter(isRecord)
    .map(stripEchoedFields);
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

const referralName = (x: unknown): string => stringProp(x, "name") ?? "";
// The BE has used both casings for this field across records (see BeFacility in
// copilot-client.ts) — try both.
const npiOf = (x: unknown): string | undefined => stringProp(x, "NPI") ?? stringProp(x, "npi");
const payerWarning = (dropped: string[]): string =>
  `${dropped.length} payer link(s) dropped (no matching pre-prod payer): ${[...new Set(dropped)].join(", ")}`;

// One pre-prod facility/provider record, indexed by NPI regardless of which order type or
// speciality it lives under.
interface PreNpiEntry {
  typeName: string;
  specialityName: string;
  name: string;
}

// Every facility/provider NPI already present ANYWHERE in pre-prod — across every order type
// and speciality, not just the one being merged. A prod-only-by-name "addition" whose NPI shows
// up here is the same real-world record under a different name or casing, not a genuinely new
// one (issue #3: an all-caps pre-prod duplicate within the SAME speciality, and a same-NPI
// record living under a completely different speciality elsewhere in the tenant, both slipped
// past exact-string name matching and 400'd the BE's duplicate-NPI constraint).
function buildPreNpiIndex(preTree: SpecialityTree): Map<string, PreNpiEntry> {
  const index = new Map<string, PreNpiEntry>();
  for (const t of preTree.types) {
    for (const s of t.specialities) {
      const specialityName = referralName(s);
      for (const item of [
        ...asArray(prop(s, "referredFacilities")),
        ...asArray(prop(s, "referredProviders")),
      ]) {
        const npi = npiOf(item);
        if (npi && !index.has(npi))
          index.set(npi, { typeName: t.name, specialityName, name: referralName(item) });
      }
    }
  }
  return index;
}

// Prod items missing from pre-prod BY NAME, minus any whose NPI already exists somewhere in
// pre-prod (a same-record duplicate hiding behind a name/casing difference) — those are
// dropped and reported via `warnings` instead of sent, since the write would 400 on the
// duplicate NPI otherwise.
function missingByName(
  prodArr: unknown[],
  preArr: unknown[],
  npiIndex: ReadonlyMap<string, PreNpiEntry>,
): { items: unknown[]; warnings: string[] } {
  const have = new Set(preArr.map(referralName));
  const items: unknown[] = [];
  const warnings: string[] = [];
  for (const x of prodArr) {
    if (have.has(referralName(x))) continue;
    const npi = npiOf(x);
    const existing = npi ? npiIndex.get(npi) : undefined;
    if (existing) {
      warnings.push(
        `'${referralName(x)}' (NPI ${npi}) skipped — already in pre-prod as '${existing.name}' ` +
          `under ${existing.typeName} / ${existing.specialityName}`,
      );
      continue;
    }
    items.push(x);
  }
  return { items, warnings };
}

// Match order types by name, then specialities by name; emit create actions for prod-only
// specialities and merge actions for specialities whose facilities/providers are a superset in
// prod. Pure — no network. Types/specialities that can't be placed are reported in `skipped`.
export function planSpecialitySync(
  prodTree: SpecialityTree,
  preTree: SpecialityTree,
  payerMap: ReadonlyMap<string, string>,
): { actions: SyncAction[]; skipped: SyncSkip[] } {
  const preTypeByName = new Map(preTree.types.map((t) => [t.name, t]));
  const npiIndex = buildPreNpiIndex(preTree);
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
        npiIndex,
      );
      const missProvs = missingByName(
        asArray(prop(pSpec, "referredProviders")),
        asArray(prop(qSpec, "referredProviders")),
        npiIndex,
      );
      const npiWarnings = [...missFacs.warnings, ...missProvs.warnings];
      if (!missFacs.items.length && !missProvs.items.length) {
        if (npiWarnings.length)
          skipped.push({
            typeName: pType.name,
            specialityName: sName,
            reason: `nothing left to merge after dropping NPI duplicates: ${npiWarnings.join("; ")}`,
          });
        continue; // already in sync (or everything missing was an NPI duplicate)
      }

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
        missFacs.items,
        missProvs.items,
        payerMap,
      );
      const warnings = [
        ...(droppedPayers.length ? [payerWarning(droppedPayers)] : []),
        ...npiWarnings,
      ];
      actions.push({
        section: "specialties",
        op: "merge",
        itemKind: "speciality",
        typeName: pType.name,
        itemName: sName,
        method: "PUT",
        path: `/api/v1/settings/specialities/${preSpecUid}`,
        body,
        ...(warnings.length ? { warnings } : {}),
      });
    }
  }
  return { actions, skipped };
}

// ---- payer-link audit (read-only; facilities/providers that already match by name) --------
// planSpecialitySync only ever ADDS prod-only facilities/providers — a facility/provider that
// already exists (matched by name) in both envs is never looked at again, so payer-link drift
// on shared records is invisible to sync (issue #4). Reconciling an EXISTING record's
// payersProviderId would mean MODIFYING pre-prod settings, not just adding to them — outside
// sync's additive-only contract — so this is audit-only: it reports findings, plan_settings_sync
// surfaces them, nothing here writes anything.

// Payer names linked to one facility/provider row (resolved via the row's OWN env's payer
// list — payer uids are per-env and never expected to match across envs, only their names are
// comparable), plus any payerUid that doesn't resolve to a name at all in that env (a dangling
// reference).
function payerLinkNames(
  item: unknown,
  payerNameByUid: ReadonlyMap<string, string>,
): { names: string[]; orphaned: string[] } {
  const names: string[] = [];
  const orphaned: string[] = [];
  for (const l of asArray(prop(item, "payersProviderId"))) {
    const uid = stringProp(l, "payerUid");
    if (!uid) continue;
    const name = payerNameByUid.get(uid);
    if (name) names.push(name);
    else orphaned.push(uid);
  }
  return { names, orphaned };
}

const setDiff = (a: readonly string[], b: readonly string[]): string[] => {
  const have = new Set(b);
  return [...new Set(a)].filter((x) => !have.has(x));
};

// Audit payer links on facilities/providers that exist (matched by name) in BOTH envs' crawled
// speciality trees — exactly the records planSpecialitySync's additive logic never inspects.
// Pure over the same crawled trees + per-env payerUid->name maps buildSyncPlan already builds
// for the specialities domain (no extra requests). Types/specialities not present in both envs
// are skipped here too — planSpecialitySync already reports those via `skipped`.
export function auditPayerLinks(
  prodTree: SpecialityTree,
  preTree: SpecialityTree,
  prodPayerNameByUid: ReadonlyMap<string, string>,
  prePayerNameByUid: ReadonlyMap<string, string>,
): PayerLinkFinding[] {
  const preTypeByName = new Map(preTree.types.map((t) => [t.name, t]));
  const findings: PayerLinkFinding[] = [];
  const kinds = [
    { field: "referredFacilities", itemKind: "facility" as const },
    { field: "referredProviders", itemKind: "provider" as const },
  ];

  for (const pType of prodTree.types) {
    const qType = preTypeByName.get(pType.name);
    if (!qType) continue;
    const preSpecByName = new Map(qType.specialities.map((s) => [referralName(s), s]));

    for (const pSpec of pType.specialities) {
      const qSpec = preSpecByName.get(referralName(pSpec));
      if (!qSpec) continue;

      for (const { field, itemKind } of kinds) {
        const preItemByName = new Map(asArray(prop(qSpec, field)).map((x) => [referralName(x), x]));
        for (const pItem of asArray(prop(pSpec, field))) {
          const itemName = referralName(pItem);
          const qItem = preItemByName.get(itemName);
          if (!qItem) continue; // not shared -- planSpecialitySync's create/merge covers it

          const prodLinks = payerLinkNames(pItem, prodPayerNameByUid);
          const preLinks = payerLinkNames(qItem, prePayerNameByUid);
          const extraInPreProd = setDiff(preLinks.names, prodLinks.names);
          const missingInPreProd = setDiff(prodLinks.names, preLinks.names);
          if (
            !extraInPreProd.length &&
            !missingInPreProd.length &&
            !prodLinks.orphaned.length &&
            !preLinks.orphaned.length
          )
            continue;

          findings.push({
            typeName: pType.name,
            specialityName: referralName(pSpec),
            itemKind,
            itemName,
            extraInPreProd,
            missingInPreProd,
            orphanedProdPayerUids: prodLinks.orphaned,
            orphanedPreProdPayerUids: preLinks.orphaned,
          });
        }
      }
    }
  }
  return findings;
}

// ---- pluggable syncer --------------------------------------------------------

export const specialitiesSyncer: SectionSyncer = {
  domain: "specialities",
  async plan(ctx) {
    const [prodTree, preTree] = await Promise.all([
      crawlSpecialityTree(ctx.prod),
      crawlSpecialityTree(ctx.pre),
    ]);
    const { actions, skipped } = planSpecialitySync(prodTree, preTree, ctx.payerMap);
    const payerLinkFindings = auditPayerLinks(
      prodTree,
      preTree,
      ctx.prodPayerNameByUid,
      ctx.prePayerNameByUid,
    );
    return { actions, skipped, payerLinkFindings };
  },
};

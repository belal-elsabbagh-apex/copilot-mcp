// Orders domain: crawl order types -> order names, and the additive write/sync plan for the
// `orders` catalog section (create prod-only orders in pre-prod).
//
// Each outbound order type carries a paged list of "order names" at /types/{uid}/names. They
// are not a standalone endpoint, so — like specialities — we crawl every type once per env and
// flatten the rows for the diff. Memoized per client.
//
// Sync side: each outbound order type holds a list of orders; each order references
// facilities, auth/referral sub-categories, and per-CPT payers. The create endpoint (verified
// against a captured HAR) is
//   POST /api/v1/settings/orders/outbound/types/{preTypeUid}/names
// with a body of UID *references* (facilitiesUids, authSubCategoryUids, referralSubCategoryUids,
// CPTCodes[].payers map). Those UIDs are per-env, so — exactly like payer links in the
// specialities domain — we REMAP every reference prod->pre by NAME and DROP (with a warning) any
// that has no pre-prod match, rather than copy a prod UID that would dangle or point at the wrong
// entity. Create-only: order names present in both envs are left untouched (there is no verified
// name-update endpoint, and this tool never overwrites).

import { envelopeRows, isRecord, prop, stringProp } from "../../shared/util.js";
import type { HttpClient } from "../copilot-client.js";
import { asArray, stripOwnUids } from "./internal.js";
import type { SectionSyncer, SyncAction, SyncSkip } from "./types.js";

export interface OrderTypeRef {
  typeUid: string;
  name: string;
}

// The order-type list, reduced to {typeUid, name}. Shared by the name crawls.
export async function listOrderTypes(client: HttpClient): Promise<OrderTypeRef[]> {
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
export async function fetchOrderNames(
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

// ---- flattened crawl, used by the diff-path derived section ----------------

const orderNamesCache = new WeakMap<HttpClient, Promise<Record<string, unknown>[]>>();
function crawlOrderNamesFlat(client: HttpClient): Promise<Record<string, unknown>[]> {
  const cached = orderNamesCache.get(client);
  if (cached) return cached;
  const run = (async (): Promise<Record<string, unknown>[]> => {
    const types = await listOrderTypes(client);
    // Every type's names GET is independent of every other type's — fetch them
    // concurrently instead of paying each round-trip in turn.
    const perType = await Promise.all(types.map((t) => fetchOrderNames(client, t.typeUid)));
    return perType.flat();
  })();
  orderNamesCache.set(client, run);
  return run;
}

export const deriveOrderNames = async (c: HttpClient): Promise<unknown[]> => crawlOrderNamesFlat(c);

// ---- rich crawl, used by the sync-path planner -------------------------------

export interface OrderNameType {
  typeUid: string;
  name: string;
  names: Record<string, unknown>[];
}
export interface OrderNameTree {
  types: OrderNameType[];
}

export async function crawlOrderNameTree(client: HttpClient): Promise<OrderNameTree> {
  const orderTypes = await listOrderTypes(client);
  // Same independence as crawlOrderNamesFlat — fetch every type's names concurrently.
  const types = await Promise.all(
    orderTypes.map(
      async (t): Promise<OrderNameType> => ({
        typeUid: t.typeUid,
        name: t.name,
        names: await fetchOrderNames(client, t.typeUid),
      }),
    ),
  );
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
  const rows = Array.isArray(r.data) ? r.data : asArray(prop(r.data, "data"));
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
  // Same independence again — each type's facility lookup is its own GET.
  const entries = await Promise.all(
    preTree.types.map(
      async (t): Promise<[string, PreTypeRefs]> => [
        t.name,
        {
          facilityUidByName: await fetchFacilityUidByName(pre, t.typeUid),
          authSubCatUidByName: harvestSubCatUidByName(t.names, "authSubCategories"),
          referralSubCatUidByName: harvestSubCatUidByName(t.names, "referralSubCategories"),
        },
      ],
    ),
  );
  return new Map(entries);
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
const payerWarning = (dropped: string[]): string =>
  `${dropped.length} payer link(s) dropped (no matching pre-prod payer): ${[...new Set(dropped)].join(", ")}`;
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

// ---- pluggable syncer --------------------------------------------------------

export const ordersSyncer: SectionSyncer = {
  domain: "orders",
  async plan(ctx) {
    const [prodTree, preTree] = await Promise.all([
      crawlOrderNameTree(ctx.prod),
      crawlOrderNameTree(ctx.pre),
    ]);
    const preRefsByType = await buildPreTypeRefs(ctx.pre, preTree);
    const { actions, skipped } = planOrderNameSync(prodTree, preTree, preRefsByType, ctx.payerMap);
    return { actions, skipped };
  },
};

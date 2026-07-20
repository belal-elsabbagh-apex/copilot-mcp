// Shared payer prod->pre UID map — built once and passed to every SectionSyncer, since both
// the specialities and orders domains need to remap payer references prod->pre.

import { stringProp } from "../../shared/util.js";
import type { HttpClient } from "../copilot-client.js";
import { asArray } from "./internal.js";
import { getEnvelope } from "./read.js";

// Candidate keys for a payer's own UID in the clinic-payers list (exact field unconfirmed —
// fall back to any *Uid key). A wrong/empty map drops payer links with a warning rather than
// linking to the wrong payer, so the miss is visible in dry-run output.
const PAYER_UID_KEYS = ["payerUid", "uid"];
const payerUidOf = (p: unknown): string | undefined => {
  for (const k of PAYER_UID_KEYS) {
    const v = stringProp(p, k);
    if (v) return v;
  }
  if (typeof p === "object" && p !== null)
    for (const k of Object.keys(p))
      if (/uid$/i.test(k)) {
        const v = stringProp(p, k);
        if (v) return v;
      }
  return undefined;
};

// name -> own payerUid AND uid -> name for one env (source: clinic-payers, envelope `data`, key
// `Name`) — one fetch, both directions: buildPayerMaps needs uidByName (to link prod payerUid ->
// pre payerUid by name); auditPayerLinks needs each env's OWN nameByUid (to resolve a facility's
// payer links back to a name for cross-env diffing, and to spot a uid that resolves to nothing).
async function fetchPayerMaps(
  client: HttpClient,
): Promise<{ uidByName: Map<string, string>; nameByUid: Map<string, string> }> {
  const r = await client.req("GET", "/api/v1/settings/clinic-payers");
  if (r.status >= 400) throw new Error(`GET /settings/clinic-payers -> ${r.status}`);
  const uidByName = new Map<string, string>();
  const nameByUid = new Map<string, string>();
  for (const p of asArray(getEnvelope(r.data, "data"))) {
    const name = stringProp(p, "Name");
    const uid = payerUidOf(p);
    if (name && uid) {
      uidByName.set(name, uid);
      nameByUid.set(uid, name);
    }
  }
  return { uidByName, nameByUid };
}

// prod payerUid -> pre-prod payerUid (by matching payers across envs on name), plus each env's
// own uid -> name map (needed to resolve/audit payer links directly, not just remap them).
export async function buildPayerMaps(
  prod: HttpClient,
  pre: HttpClient,
): Promise<{
  prodToPre: Map<string, string>;
  prodNameByUid: ReadonlyMap<string, string>;
  preNameByUid: ReadonlyMap<string, string>;
}> {
  const [prodMaps, preMaps] = await Promise.all([fetchPayerMaps(prod), fetchPayerMaps(pre)]);
  const prodToPre = new Map<string, string>();
  for (const [name, prodUid] of prodMaps.uidByName) {
    const preUid = preMaps.uidByName.get(name);
    if (preUid) prodToPre.set(prodUid, preUid);
  }
  return { prodToPre, prodNameByUid: prodMaps.nameByUid, preNameByUid: preMaps.nameByUid };
}

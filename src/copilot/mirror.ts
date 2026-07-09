// EHR Copilot order mirror — prod -> pre-prod. Ported natively from the legacy
// `.planning/copy_order_prod_to_preprod.mjs` (logic unchanged; config + types only).
//
// Extract prod order via /orders/filter, replay all PUTs into a fresh pre-prod draft,
// /note/upload (dummy PDF), ICDCodes, /process retry loop until forReview, post-forReview
// corrections (requiredAuthorization then placeOfService LAST), optional /submit.

import { mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import process from "node:process";
import type { ResolvedCreds } from "../config/config.js";
import { getOverrides, type OrderOverride } from "../config/config.js";
import { envelopeRows, prop, stringProp } from "../shared/util.js";
import {
  assertPreProdClient,
  type BeFacility,
  type BeOrder,
  type BeSpeciality,
  type HttpClient,
  login,
  makeClient,
  type OrderVerify,
  pad,
  toMDY,
  verify,
} from "./copilot-client.js";

export interface MirrorResult {
  prodUid: string;
  newUid?: string;
  submitted?: boolean;
  verify?: OrderVerify | null;
}

const sleep = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms));
const todayMDY = (): string => {
  const d = new Date();
  return `${pad(d.getMonth() + 1)}/${pad(d.getDate())}/${d.getFullYear()}`;
};

// Shift a date forward by `months` if it's already in the past (keeps clones future-dated).
function shiftIfPast(mdy: string, months = 3): string {
  const [mm, dd, yy] = mdy.split("/").map(Number);
  const d = new Date(yy ?? 0, (mm ?? 1) - 1, dd ?? 1);
  if (d < new Date()) {
    d.setMonth(d.getMonth() + months);
    return `${pad(d.getMonth() + 1)}/${pad(d.getDate())}/${d.getFullYear()}`;
  }
  return mdy;
}

function normPhone(s: string | null | undefined): string {
  const d = String(s ?? "").replace(/\D/g, "");
  if (d.length === 10) return `(${d.slice(0, 3)}) ${d.slice(3, 6)}-${d.slice(6)}`;
  if (d.length === 11 && d[0] === "1") return `(${d.slice(1, 4)}) ${d.slice(4, 7)}-${d.slice(7)}`;
  return "(555) 555-5555";
}

// Minimal valid single-page PDF with correct xref offsets (dummy auth note).
function buildDummyPdf(): Buffer {
  const stream = "BT /F1 18 Tf 20 60 Td (Dummy auth note - preprod clone) Tj ET";
  const objs = [
    "<< /Type /Catalog /Pages 2 0 R >>",
    "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 144] >>",
    "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
    "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
    `<< /Length ${stream.length} >>\nstream\n${stream}\nendstream`,
  ];
  let pdf = "%PDF-1.4\n";
  const offsets: number[] = [];
  objs.forEach((bodyText, i) => {
    offsets[i] = Buffer.byteLength(pdf, "latin1");
    pdf += `${i + 1} 0 obj\n${bodyText}\nendobj\n`;
  });
  const xrefStart = Buffer.byteLength(pdf, "latin1");
  pdf += `xref\n0 ${objs.length + 1}\n0000000000 65535 f \n`;
  offsets.forEach((off) => {
    pdf += `${String(off).padStart(10, "0")} 00000 n \n`;
  });
  pdf += `trailer\n<< /Root 1 0 R /Size ${objs.length + 1} >>\nstartxref\n${xrefStart}\n%%EOF\n`;
  return Buffer.from(pdf, "latin1");
}

type PutFn = (body: Record<string, unknown>, label: string) => Promise<unknown>;

function mkPut(c: HttpClient, uid: string): PutFn {
  return async function put(body, label) {
    let r = await c.req("PUT", `/api/v1/orders/${uid}`, { json: body });
    if (r.status === 403 && /missing_token/.test(r.text)) {
      await c.req("GET", "/api/v1/physician/refresh");
      r = await c.req("PUT", `/api/v1/orders/${uid}`, { json: body });
    }
    if (r.status >= 400)
      throw new Error(`PUT ${label} failed ${r.status}: ${r.text.slice(0, 300)}`);
    console.log(`  PUT ${label}: ok`);
    return r.data;
  };
}

interface ProcResult {
  msg?: string;
  [k: string]: unknown;
}
async function postProcess(c: HttpClient, uid: string): Promise<ProcResult> {
  const r = await c.req("POST", `/api/v1/orders/${uid}/process`);
  // already past forReview -> 400 E6001 "status must be in drafted/new/incomplete/pending" = success
  if (r.status === 400 && /E6001/.test(r.text) && /(drafted|incomplete|pending)/i.test(r.text))
    return { msg: "Order already at forReview status" };
  if (r.status >= 400) throw new Error(`/process failed ${r.status}: ${r.text.slice(0, 300)}`);
  return (r.data ?? {}) as ProcResult;
}
async function processUntilForReview(
  c: HttpClient,
  uid: string,
  attempts = 6,
  delayMs = 5000,
): Promise<ProcResult> {
  let body: ProcResult = {};
  for (let i = 0; i < attempts; i++) {
    body = await postProcess(c, uid);
    console.log(`  /process [${i}]: ${body.msg ?? JSON.stringify(body).slice(0, 120)}`);
    if (/forreview/i.test(body.msg ?? "")) return body;
    if (i < attempts - 1) await sleep(delayMs);
  }
  return body;
}

export interface FacilityRemap {
  referredFacilityUid?: string | undefined;
  specialityUid?: string | undefined;
  placeOfService?: string | undefined;
  name?: string | undefined;
  npi?: string | undefined;
}

// Faithful cross-env facility remap: the same facility is often re-keyed with a
// different referredFacilityUid in pre-prod. Look it up by NPI (then name) under
// the order type's specialities and return the pre-prod uid + its speciality uid.
async function resolvePreprodFacility(
  pre: HttpClient,
  typeUid: string | undefined,
  prodFac: BeFacility,
): Promise<FacilityRemap | null> {
  const npi = prodFac.NPI ?? prodFac.npi;
  const name = prodFac.name;
  if (!typeUid || (!npi && !name)) return null;
  let r = await pre.req("GET", `/api/v1/settings/orders/outbound/types/${typeUid}/specialities`);
  // 403 cookie race: refresh physician token once and retry (mirrors note/upload + submit guards).
  if (r.status === 403 && /missing_token/.test(r.text ?? "")) {
    await pre.req("GET", "/api/v1/physician/refresh");
    r = await pre.req("GET", `/api/v1/settings/orders/outbound/types/${typeUid}/specialities`);
  }
  // Defensive: the endpoint may return {data:[...]}, a bare array, or (on error) a
  // non-iterable object/string. Only iterate a real array — otherwise no remap.
  // Endpoint returns either { data: [...] } or a bare [...]; both -> the speciality list.
  const raw = r.data;
  const spd = (Array.isArray(raw) ? raw : envelopeRows(raw)) as BeSpeciality[];
  for (const s of spd) {
    for (const f of s.referredFacilities ?? []) {
      const fnpi = f.NPI ?? f.npi;
      const npiMatch = npi && fnpi && String(fnpi) === String(npi);
      const nameMatch = !npi && name && (f.name ?? "").toLowerCase() === name.toLowerCase();
      if (npiMatch || nameMatch)
        return {
          referredFacilityUid: f.referredFacilityUid,
          specialityUid: s.specialityUid,
          placeOfService: f.placeOfService,
          name: f.name,
          npi: fnpi,
        };
    }
  }
  return null;
}

// Best-effort debug dump of the extracted prod order (never breaks the clone).
function dumpProdOrder(prodUid: string, src: BeOrder): void {
  try {
    const dir = process.env["COPILOT_MCP_DEBUG_DIR"] ?? tmpdir();
    mkdirSync(dir, { recursive: true });
    writeFileSync(join(dir, `prod-order-${prodUid}.json`), JSON.stringify(src, null, 2));
  } catch {
    // debug artifact only — ignore failures
  }
}

// ---- Mint spec --------------------------------------------------------------

// Everything the pre-prod mint engine needs, as plain data — no reference to a
// prod source order. TOTAL shape ("" / null / [] sentinels) so both producers
// (specFromProdOrder for the clone, create_preprod_order's explicit payload)
// normalize at the boundary.
export interface MintSpec {
  patientName: string;
  patientBirthDate: string; // MM/DD/YYYY, "" = unset
  patientPhoneNumber: string; // raw; normalized at PUT time ("" -> dummy)
  insuranceName: string;
  insuranceMemberId: string;
  location: string; // "" = skip
  typeUid: string; // order type uid ("" = omitted — /process will stall)
  specialityUid: string | null;
  referredFacilityUid: string; // "" = skip
  referredProviderUid: string; // "" = skip (referral orders; PUT after facility —
  // must come after orderNames, which reset it)
  clinicProviderUid: string; // "" = leave to FE default
  orderNamesUids: string[]; // auto-seed CPTs on the BE
  cptCodes: { code: string; units: unknown; description: string; treatments: unknown }[]; // [] = rely on orderNames auto-seed
  uploadAuth: boolean;
  uploadFax: boolean;
  retro: boolean;
  authorization: Record<string, unknown> | null;
  appointmentDate: string; // MM/DD/YYYY, "" = none
  icdCodes: { code: string; description: string }[];
  placeOfService: string; // "" = none; applied LAST, post-forReview
}

// Derive the mint spec for a prod order: override > facility remap > prod value.
// Pure — the (network) facility remap happens before this and is passed in.
export function specFromProdOrder(
  src: BeOrder,
  ov: OrderOverride,
  facMap: FacilityRemap | null,
): MintSpec {
  // sent-by only maps when an override provides a valid pre-prod uid, or the prod
  // uid is carried as-is when no facility override forces a different account.
  const clinicProviderUid =
    ov.clinicProviderUid ?? (ov.referredFacilityUid ? null : src.clinicProvider?.clinicProviderUid);
  return {
    patientName: src.patient?.patientName ?? "",
    patientBirthDate: toMDY(src.patient?.patientBirthDate) ?? "",
    patientPhoneNumber: src.patient?.patientPhoneNumber ?? "",
    insuranceName: src.insurance?.name ?? "",
    insuranceMemberId: src.insurance?.memberId ?? "",
    location: src.location ?? "",
    typeUid: ov.typeUid ?? src.orderType?.typeUid ?? "",
    specialityUid: ov.specialityUid ?? facMap?.specialityUid ?? null,
    referredFacilityUid:
      ov.referredFacilityUid ??
      facMap?.referredFacilityUid ??
      src.referredFacility?.referredFacilityUid ??
      "",
    referredProviderUid: "", // the mirror never re-links a referred provider (facility-based orders)
    clinicProviderUid: clinicProviderUid ?? "",
    orderNamesUids:
      ov.orderNamesUids ??
      (src.orderNames ?? []).map((n) => n.nameUid).filter((u): u is string => !!u),
    cptCodes: ov.injectCPTs
      ? (src.CPTCodes ?? []).map((c) => ({
          code: c.code ?? "",
          units: c.units,
          description: c.description ?? "",
          treatments: c.treatments ?? null,
        }))
      : [],
    uploadAuth: !!src.uploadAuth,
    uploadFax: !!src.uploadFax,
    retro: !!src.retro,
    authorization: src.authorization ? { ...src.authorization, sendReferralAfterAuth: true } : null,
    appointmentDate: src.appointmentDate ? shiftIfPast(toMDY(src.appointmentDate) ?? "") : "",
    icdCodes: (src.ICDCodes ?? []).map((i) => ({
      code: i.code ?? "",
      description: i.description ?? "",
    })),
    placeOfService: ov.placeOfService ?? src.placeOfService ?? facMap?.placeOfService ?? "",
  };
}

// ---- Mint engine -------------------------------------------------------------

export interface MintResult {
  newUid: string;
  submitted: boolean;
  verify: OrderVerify | null;
}

// Create a pre-prod draft and drive it to forReview from a MintSpec, preserving
// the sequence invariants: order names auto-seed CPTs and RESET speciality/
// referredProvider (names after type, speciality/provider after names);
// placeOfService is applied LAST, post-forReview; /process runs an E6001-tolerant
// retry loop (async note OCR blocks it). `pre` must already be logged in to the
// pre-prod tenant. Callers other than the mirror should pass submit:false —
// hand-minted test orders stop at forReview.
export async function mintPreprodOrder(
  pre: HttpClient,
  spec: MintSpec,
  opts: { submit: boolean },
): Promise<MintResult> {
  assertPreProdClient(pre, "mintPreprodOrder");
  // "" sentinels -> omitted keys, so the wire bodies match the legacy mirror exactly.
  const orUndef = (s: string): string | undefined => s || undefined;

  // Step 1: empty draft
  const draftRes = await pre.req("POST", "/api/v1/orders");
  if (draftRes.status >= 400)
    throw new Error(`create draft failed ${draftRes.status}: ${draftRes.text.slice(0, 300)}`);
  const newUid = stringProp(prop(draftRes.data, "order"), "orderUid");
  if (!newUid) throw new Error(`create draft returned no orderUid: ${draftRes.text.slice(0, 200)}`);
  console.log(`  new draft: ${newUid}`);
  const put = mkPut(pre, newUid);

  // Step 2: patient
  await put(
    {
      emrPatientId: "",
      patientName: orUndef(spec.patientName),
      patientBirthDate: spec.patientBirthDate || null,
    },
    "patient",
  );
  // Step 3: phone (always non-empty)
  await put({ patientPhoneNumber: normPhone(spec.patientPhoneNumber) }, "phone");
  // Step 4: insurance
  await put(
    { insurance: { name: orUndef(spec.insuranceName), memberId: orUndef(spec.insuranceMemberId) } },
    "insurance",
  );
  // Step 5: location
  if (spec.location) await put({ location: spec.location }, "location");
  // Step 6: order type + speciality (orderDate = today)
  await put(
    {
      typeUid: orUndef(spec.typeUid),
      specialityUid: spec.specialityUid,
      referredProviderUid: null,
      referredFacilityUid: null,
      orderDate: todayMDY(),
    },
    "type",
  );
  // Step 7: order names (auto-seeds CPTs; RESETS speciality/provider — hence re-sent here)
  await put(
    {
      orderNamesUids: spec.orderNamesUids,
      specialityUid: spec.specialityUid,
      referredProviderUid: null,
      referredFacilityUid: null,
    },
    "orderNames",
  );
  // Step 8: sent by — only when a valid pre-prod uid is known (prod's may not exist)
  if (spec.clinicProviderUid) await put({ clinicProviderUid: spec.clinicProviderUid }, "sentBy");
  else console.log("  (sentBy: left to FE default — clinicProvider not mapped)");
  // Step 9: facility (auto-sets POS + fax)
  if (spec.referredFacilityUid)
    await put({ referredFacilityUid: spec.referredFacilityUid }, "facility");
  // Step 9a: referred provider (referral orders) — after names/speciality, which reset it
  if (spec.referredProviderUid)
    await put({ referredProviderUid: spec.referredProviderUid }, "referredProvider");
  // Step 9b: best-effort CPT injection — "Other" orderName won't auto-seed PT CPTs
  if (spec.cptCodes.length) {
    const cpts = spec.cptCodes.map((c) => ({ ...c, evidences: [] }));
    try {
      await put({ CPTCodes: cpts }, "CPTCodes (injected)");
    } catch (e) {
      console.warn(`  !! CPT injection failed (continuing): ${(e as Error).message}`);
    }
  }
  // Step 10: yes/no flags
  await put({ uploadAuth: spec.uploadAuth }, "uploadAuth");
  await put({ uploadFax: spec.uploadFax }, "uploadFax");
  await put({ retro: spec.retro }, "retro");
  if (spec.authorization) await put({ authorization: spec.authorization }, "authorization");
  // Step 11: direct processing
  await put({ directProcessingEnabled: true }, "directProcessingEnabled");
  // Step 12: first /process (usually incomplete: appointmentDate missing)
  let proc = await postProcess(pre, newUid);
  console.log(`  /process (1st): ${proc.msg}`);
  // Step 14: appointment date
  if (spec.appointmentDate) {
    await put({ appointmentDate: spec.appointmentDate }, "appointmentDate");
    if (/incomplete/i.test(proc.msg ?? "")) {
      proc = await postProcess(pre, newUid);
      console.log(`  /process (after appt): ${proc.msg}`);
    }
  }
  // Step 15: note upload (dummy PDF, async OCR on BE)
  const fd = new FormData();
  fd.append("pdfFile", new Blob([buildDummyPdf()], { type: "application/pdf" }), "note.pdf");
  let up = await pre.req("POST", `/api/v1/orders/${newUid}/note/upload`, { form: fd });
  if (up.status === 403 && /missing_token/.test(up.text)) {
    await pre.req("GET", "/api/v1/physician/refresh");
    up = await pre.req("POST", `/api/v1/orders/${newUid}/note/upload`, { form: fd });
  }
  if (up.status >= 400)
    throw new Error(`/note/upload failed ${up.status}: ${up.text.slice(0, 300)}`);
  console.log("  /note/upload: ok");
  // Step 16: ICDs (manual; OCR can't read dummy PDF)
  const icds = spec.icdCodes.map((i) => ({ ...i, evidences: [] }));
  if (icds.length) await put({ ICDCodes: icds }, "ICDCodes");
  // Final /process retry loop until forReview (note upload async-blocks it)
  proc = await processUntilForReview(pre, newUid);
  if (!/forreview/i.test(proc.msg ?? ""))
    console.warn(`  !! order did not reach forReview: ${proc.msg}`);
  // Step 13: post-forReview corrections — requiredAuthorization FIRST, placeOfService LAST.
  await put({ requiredAuthorization: true }, "requiredAuthorization (final)");
  if (spec.placeOfService)
    await put({ placeOfService: spec.placeOfService }, "placeOfService (final, must be last)");

  // Step 17: submit (opt-in — only the mirror ever passes true)
  if (opts.submit) {
    let r = await pre.req("POST", `/api/v1/orders/${newUid}/submit`, {
      json: { actionsHistory: ["submit_action"] },
    });
    if (r.status === 403 && /missing_token/.test(r.text)) {
      await pre.req("GET", "/api/v1/physician/refresh");
      r = await pre.req("POST", `/api/v1/orders/${newUid}/submit`, {
        json: { actionsHistory: ["submit_action"] },
      });
    }
    if (r.status >= 400) throw new Error(`/submit failed ${r.status}: ${r.text.slice(0, 300)}`);
    console.log(`  /submit: ${JSON.stringify(r.data)}`);
    await sleep(1500);
  }
  const v = await verify(pre, newUid);
  console.log(`  verify: ${JSON.stringify(v, null, 2)}`);
  return { newUid, submitted: opts.submit, verify: v };
}

// ---- Mirror (prod -> pre-prod clone) ------------------------------------------

export async function mirrorOne(
  prodUid: string,
  opts: { submit: boolean; creds: ResolvedCreds },
): Promise<MirrorResult> {
  const { submit, creds } = opts;
  const ov: OrderOverride = getOverrides()[prodUid] ?? {};
  console.log(
    `\n=== mirror ${prodUid}${submit ? " (+submit)" : ""}${Object.keys(ov).length ? " [overrides]" : ""} ===`,
  );
  const prod = makeClient(creds.prod.be, "prod");
  const pre = makeClient(creds.pre_prod.be, "pre_prod");
  await login(prod, creds.prod.email, creds.prod.password);
  const src = await fetchProd(prod, prodUid);
  dumpProdOrder(prodUid, src);
  console.log(`  extracted prod order: ${src.patient?.patientName} / ${src.orderType?.name}`);

  await login(pre, creds.pre_prod.email, creds.pre_prod.password);

  // Faithful cross-env facility remap (network) before the pure spec derivation:
  // prefer an explicit override, else NPI/name lookup, else prod's uid as-is.
  let facMap: FacilityRemap | null = null;
  if (!ov.referredFacilityUid && src.referredFacility) {
    const typeUid = ov.typeUid ?? src.orderType?.typeUid;
    facMap = await resolvePreprodFacility(pre, typeUid, src.referredFacility);
    if (facMap)
      console.log(
        `  facility remapped (NPI ${facMap.npi}): ${facMap.name} ${src.referredFacility.referredFacilityUid} -> ${facMap.referredFacilityUid}`,
      );
    else console.log("  (no pre-prod facility match by NPI/name — will try prod uid, may 404)");
  }

  const spec = specFromProdOrder(src, ov, facMap);
  const minted = await mintPreprodOrder(pre, spec, { submit });
  console.log(`\n  prod_uid:     ${prodUid}\n  pre_prod_uid: ${minted.newUid}`);
  return { prodUid, newUid: minted.newUid, submitted: minted.submitted, verify: minted.verify };
}

// Extract a prod order (same /orders/filter call as fetchOrder, kept distinct for the clearer error).
async function fetchProd(c: HttpClient, uid: string): Promise<BeOrder> {
  const r = await c.req("POST", "/api/v1/orders/filter", {
    json: {
      searchUid: uid,
      pageSize: 30,
      pageNumber: 0,
      type: "Outbound Referral",
      orderMode: '["orders_only_mode","pcp_notes_mode"]',
    },
  });
  if (r.status >= 400) throw new Error(`extract failed ${r.status}: ${r.text.slice(0, 300)}`);
  const o = envelopeRows(r.data)[0] as BeOrder | undefined;
  if (!o) throw new Error(`prod order ${uid} not found`);
  return o;
}

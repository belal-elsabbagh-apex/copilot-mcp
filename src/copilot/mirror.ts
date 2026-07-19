// EHR Copilot pre-prod order mint engine. Ported natively from the legacy
// `.planning/copy_order_prod_to_preprod.mjs` (logic unchanged; config + types only).
//
// Create an empty pre-prod draft, replay all PUTs, /note/upload (dummy PDF), ICDCodes,
// /process retry loop until forReview, post-forReview corrections (requiredAuthorization
// then placeOfService LAST). Never submits — see copilot-client.ts's submitOrder for that.

import { prop, stringProp } from "../shared/util.js";
import {
  assertPreProdClient,
  type HttpClient,
  type OrderVerify,
  pad,
  verify,
} from "./copilot-client.js";

const sleep = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms));
const todayMDY = (): string => {
  const d = new Date();
  return `${pad(d.getMonth() + 1)}/${pad(d.getDate())}/${d.getFullYear()}`;
};

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

// ---- Mint spec --------------------------------------------------------------

// Everything the pre-prod mint engine needs, as plain data. TOTAL shape
// ("" / null / [] sentinels) so create_preprod_order's explicit payload normalizes
// at the boundary.
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

// ---- Mint engine -------------------------------------------------------------

export interface MintResult {
  newUid: string;
  verify: OrderVerify | null;
  // The last /process response's message — set even when the order didn't reach
  // forReview, so a caller can see WHY (e.g. a missing reference) instead of just
  // an unexplained non-forReview status.
  processMessage: string | undefined;
}

// Create a pre-prod draft and drive it to forReview from a MintSpec, preserving
// the sequence invariants: order names auto-seed CPTs and RESET speciality/
// referredProvider (names after type, speciality/provider after names);
// placeOfService is applied LAST, post-forReview; /process runs an E6001-tolerant
// retry loop (async note OCR blocks it). `pre` must already be logged in to the
// pre-prod tenant. Always stops at forReview — never submits; see
// copilot-client.ts's submitOrder for that as a separate, explicit step.
export async function mintPreprodOrder(pre: HttpClient, spec: MintSpec): Promise<MintResult> {
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

  const v = await verify(pre, newUid);
  console.log(`  verify: ${JSON.stringify(v, null, 2)}`);
  return { newUid, verify: v, processMessage: proc.msg };
}

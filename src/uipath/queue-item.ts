// Build a UiPath AddQueueItem request from an EHR Copilot order (prod or pre-prod).
// Ported natively from `.planning/build_queue_item.mjs` (logic unchanged; config + types only).
// Fetch the order via /orders/filter, log in to capture the BE callback JWT + physicianId,
// map order details -> SpecificContent, return { payload, notes, meta }. The caller submits it
// themselves with their own bearer token — this module never embeds a live credential in output.
// BUILD ONLY — never POSTs. IsApproved is ALWAYS false (see repo CLAUDE.md rule).

import { randomUUID } from "node:crypto";
import type { Env, UipathConfig } from "../config/config.js";
import { getUipath, resolveCreds } from "../config/config.js";
import {
  type BeOrder,
  fetchOrder,
  makeClient,
  toMDY as toMDYStrict,
} from "../copilot/copilot-client.js";
import { prop, stringProp } from "../shared/util.js";
import { guardQueueItemSafety } from "./safety.js";

// Non-throwing MM/DD/YYYY normalizer (queue payloads prefer "" over an error) — the
// date-parsing rules themselves live once, in copilot-client.ts's toMDY.
export function toMDY(input: string | null | undefined): string {
  try {
    return toMDYStrict(input) ?? "";
  } catch {
    return "";
  }
}

export const decodeJwtId = (t: string | undefined): string | number | null => {
  try {
    const seg = (t ?? "").split(".")[1] ?? "";
    const payload: unknown = JSON.parse(Buffer.from(seg, "base64").toString());
    const id = prop(payload, "id");
    return typeof id === "string" || typeof id === "number" ? id : null;
  } catch {
    return null;
  }
};

// "Last, First M" -> { last:"LAST", first:"FIRST" } (first token after comma, uppercased)
export function splitName(full: string | undefined): { last: string; first: string } {
  const s = String(full ?? "").trim();
  const ci = s.indexOf(",");
  if (ci < 0) return { last: s.toUpperCase(), first: "" };
  const last = s.slice(0, ci).trim().toUpperCase();
  const first = (
    s
      .slice(ci + 1)
      .trim()
      .split(/\s+/)[0] ?? ""
  ).toUpperCase();
  return { last, first };
}

// Require the queue-build-specific UiPath fields (only needed by this tool).
function requireQueueFields(
  u: UipathConfig,
): asserts u is UipathConfig &
  Required<Pick<UipathConfig, "queueUrl" | "addQueueItemPath" | "folderPath">> {
  const missing = (["queueUrl", "addQueueItemPath", "folderPath"] as const).filter((k) => !u[k]);
  if (missing.length)
    throw new Error(
      `uipath config is missing fields required for build_queue_item: ${missing.join(", ")}. ` +
        "Add them to your COPILOT_MCP_CONFIG uipath block (see copilot-mcp.config.example.json).",
    );
}

export interface QueueItemResult {
  payload: unknown;
  notes: string[];
  meta: Record<string, unknown>;
}

export async function buildQueueItem(
  orderUid: string,
  opts: { profile?: string | null; env: Env }, // env is required — never defaulted (repo invariant)
): Promise<QueueItemResult> {
  const profile = opts.profile ?? null;
  const env: Env = opts.env;
  const uipath = getUipath();
  requireQueueFields(uipath);

  const creds = resolveCreds(profile);
  const envCfg = creds[env];
  if (!envCfg) throw new Error(`env '${env}' not in profile (expected 'prod' or 'pre_prod')`);
  const client = makeClient(envCfg.be, env);
  // login captures the BE JWT (used as SpecificContent.token) and sets the cookie jar
  const lr = await client.req("POST", "/api/v1/copilot/physician/login", {
    json: { email: envCfg.email, password: envCfg.password },
  });
  if (lr.status >= 400) throw new Error(`login failed ${lr.status}: ${lr.text.slice(0, 200)}`);
  const token = stringProp(lr.data, "token");
  const physicianId = decodeJwtId(token);

  const o = await fetchOrder(client, orderUid);
  const notes: string[] = [];

  const form: NonNullable<BeOrder["referralFormJSON"]> = o.referralFormJSON ?? {};
  const from: Record<string, unknown> = form.from ?? {};
  const to: Record<string, unknown> = form.to ?? {};
  const fac = o.referredFacility ?? {};
  const cpts = o.CPTCodes ?? [];
  const icds = o.ICDCodes ?? [];
  const member = splitName(o.patient?.patientName);
  const insuranceName = o.insurance?.name ?? "";

  const dos = toMDY(o.encounterDate) || toMDY(o.appointmentDate) || toMDY(o.orderDate);
  if (!o.encounterDate)
    notes.push(
      "DOS: order had no encounterDate; fell back to appointment/order date (may be empty).",
    );
  if (cpts.some((c) => /[:;]/.test(c.description ?? "")))
    notes.push(
      "DescriptionOfService uses the order's full CPT descriptions (the BE's short-form descriptions are not in the order JSON).",
    );
  if (!token)
    notes.push(
      "WARNING: login returned no token — SpecificContent.token is empty; the bot's BE callbacks will fail.",
    );

  const s = (v: unknown): string => (v == null ? "" : String(v));
  const fromName = s(from["name"]);
  const built = {
    DOS: dos,
    DescriptionOfService: JSON.stringify(cpts.map((c) => c.description ?? "")),
    Diagnoses: JSON.stringify(icds.map((i) => i.code)),
    Facility: fac.name || s(to["name"]),
    IsApproved: false, // HARD RULE — never true
    IsRetro: !!o.retro,
    IsSameAsRequestingProvider: false,
    IsUrgent: String(o.urgency ?? "").toLowerCase() === "urgent",
    Location: o.location ?? "",
    MemberDOB: toMDY(o.patient?.patientBirthDate),
    MemberFax: "",
    MemberFirstName: member.first,
    MemberFullName: o.patient?.patientName ?? "",
    MemberID: o.insurance?.memberId ?? "",
    MemberLastName: member.last,
    MemberPhone: s(form.patient?.phoneNumber) || o.patient?.patientPhoneNumber || "",
    OfficeComments: o.comments ?? "",
    ProviderAddress: s(from["address"]),
    ProviderCity: "",
    ProviderFax: s(from["faxNumber"]),
    ProviderFirstName: splitName(fromName).first || fromName,
    ProviderFullName: fromName,
    ProviderID: "",
    ProviderLastName:
      splitName(fromName).last && fromName.includes(",") ? splitName(fromName).last : "",
    ProviderMail: envCfg.email ?? "",
    ProviderNPI: s(from["NPI"]),
    ProviderPhone: s(from["phoneNumber"]),
    ProviderZipCode: "",
    ReferredToAddress: s(to["address"]) || fac.address || "",
    ReferredToFax: s(to["faxNumber"]) || fac.faxNumber || "",
    ReferredToNPI: s(to["NPI"]) || fac.NPI || "",
    ReferredToPhone: s(to["phoneNumber"]) || fac.phone || "",
    ReferredToProviderID: "",
    ServiceCode: JSON.stringify(cpts.map((c) => c.code)),
    Specialty: o.speciality?.name || s(to["speciality"]),
    Treatments: JSON.stringify(cpts.flatMap((c) => c.treatments ?? [])),
    Units: JSON.stringify(cpts.map((c) => String(c.units ?? ""))),
    appointmentDate: toMDY(o.appointmentDate),
    automationId: randomUUID(),
    callbackContext: JSON.stringify({ physicianId, orderUid }),
    isSpeciality: !!(fac.isSpeciality ?? false),
    orderUid,
    placeOfService: o.placeOfService || fac.placeOfService || "",
    queueUrl: uipath.queueUrl,
    retryCount: 0,
    serverURL: uipath.serverUrlByEnv?.[env] ?? envCfg.be,
    token: token ?? "",
  };
  // Single enforcement point for the IsApproved=false rule (the literal above documents it).
  const specific = guardQueueItemSafety(built, "force").specificContent;

  const payload = {
    itemData: {
      Name: `${insuranceName} auth submit queue`,
      Priority: "Normal",
      Reference: `${orderUid}-${Math.floor(Date.now() / 1000)}`,
      SpecificContent: specific,
    },
  };

  const postUrl = uipath.orchestratorUrl.replace(/\/$/, "") + uipath.addQueueItemPath;
  notes.push(
    "To submit: POST payload.itemData as JSON to meta.postUrl with headers " +
      "'Authorization: Bearer <your uipath.bearer>' and " +
      `'X-UIPATH-FolderPath: ${uipath.folderPath}'. Never done automatically by this tool.`,
  );

  return {
    payload,
    notes,
    meta: {
      profile: profile ?? "(default)",
      env,
      physicianId,
      queueName: payload.itemData.Name,
      orderStatus: o.status,
      postUrl,
      folderPath: uipath.folderPath,
    },
  };
}

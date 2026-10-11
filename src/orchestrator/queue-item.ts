// Canonical Copilot-order -> queue-item content, shared by every orchestrator backend.
// Fetches the order and logs in to capture the BE callback JWT + physicianId, then maps the
// order onto SpecificContent in NATIVE types (arrays stay arrays, callbackContext is an
// object) and WITHOUT serverURL/queueUrl — each backend's serializer fills those from its
// own config and shapes the wire format. BUILD ONLY — never POSTs. IsApproved is ALWAYS
// false (see repo CLAUDE.md rule); backends re-assert it through guardQueueItemSafety.

import { randomUUID } from "node:crypto";
import type { Env } from "../config/config.js";
import { type BeOrder, fetchOrder, toMDY as toMDYStrict } from "../copilot/copilot-client.js";
import { clinicOwnerEmail, connect } from "../copilot/session.js";
import { ACCOUNT_AUTOMATION_IDS } from "../mcp/reference.js";
import { prop } from "../shared/util.js";

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

// An explicit automationId always wins; otherwise resolve it from `profile` (the account name
// from config, e.g. "kafri"/"sclc"/"ossm") against the known account table, matched
// case-insensitively. Returns undefined when neither source has one.
export function resolveAutomationId(
  explicit: string | null | undefined,
  profile: string | null | undefined,
): string | undefined {
  return explicit?.trim() || ACCOUNT_AUTOMATION_IDS[(profile ?? "").toUpperCase()];
}

export interface QueueItemContent {
  content: Record<string, unknown>; // native-typed SpecificContent, no serverURL/queueUrl
  queueName: string;
  notes: string[];
  meta: Record<string, unknown>;
}

// `beBase` is the Copilot BE the order was read from: the serverURL fallback for backends
// whose config pins no server URL for this env.
export interface BuiltQueueItem extends QueueItemContent {
  beBase: string;
}

// Everything buildQueueItemContent reads from the outside world, so assembly stays pure.
export interface AssembleInput {
  order: BeOrder;
  orderUid: string;
  profile: string | null;
  env: Env;
  automationId: string | undefined; // already resolved: explicit > account table
  token: string | undefined;
  physicianId: string | number | null;
  supportMode: boolean;
  providerMail: string;
}

const s = (v: unknown): string => (v == null ? "" : String(v));

export function assembleQueueItemContent(input: AssembleInput): QueueItemContent {
  const { order: o, orderUid, profile, env, automationId, token, physicianId } = input;
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
  if (!automationId)
    notes.push(
      `WARNING: no automationId was supplied and none is known for account '${profile ?? "(none)"}' ` +
        "— SpecificContent.automationId is a placeholder random UUID, not the account's real " +
        "automation id. Pass the correct value via the automationId argument before submitting.",
    );
  if (input.supportMode)
    notes.push(
      `physicianId ${physicianId} is the support account's user id — under support-account auth the JWT 'id' claim is the support user in both pre- and post-switch tokens; callbackContext carries it as-is.`,
    );

  const fromName = s(from["name"]);
  const queueName = `${insuranceName} auth submit queue`;
  const content = {
    ClinicNPI: s(from["NPI"]),
    DOS: dos,
    DescriptionOfService: cpts.map((c) => c.description ?? ""),
    Diagnoses: icds.map((i) => i.code),
    Facility: fac.name || s(to["name"]),
    IsApproved: false, // HARD RULE — never true
    IsRetro: !!o.retro,
    IsSameAsRequestingProvider: false,
    IsUrgent: String(o.urgency ?? "").toLowerCase() === "urgent",
    LinkedFacility: "",
    LinkedFacilityAddress: "",
    LinkedFacilityNPI: "",
    LinkedFacilityOtherName: "",
    LinkedFacilityType: "",
    LinkedPlaceOfService: "",
    LinkedProviderFullName: "",
    LinkedProviderID: "",
    LinkedProviderNPI: "",
    LinkedProviderType: "",
    LinkedSpecialty: "",
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
    ProviderMail: input.providerMail,
    ProviderNPI: s(from["NPI"]),
    ProviderPhone: s(from["phoneNumber"]),
    ProviderZipCode: "",
    ReferredToAddress: s(to["address"]) || fac.address || "",
    ReferredToFax: s(to["faxNumber"]) || fac.faxNumber || "",
    ReferredToNPI: s(to["NPI"]) || fac.NPI || "",
    ReferredToOtherName: "",
    ReferredToPhone: s(to["phoneNumber"]) || fac.phone || "",
    ReferredToProviderID: "",
    ReferredToType: fac.type || "provider",
    ServiceCode: cpts.map((c) => c.code),
    Specialty: o.speciality?.name || s(to["speciality"]),
    Treatments: cpts.flatMap((c) => c.treatments ?? []),
    Units: cpts.map((c) => String(c.units ?? "")),
    addManualFacility: false,
    appointmentDate: toMDY(o.appointmentDate),
    automationId: automationId || randomUUID(),
    callbackContext: { physicianId, orderUid },
    eligibilityOtherPatientName: null,
    isSpeciality: !!(fac.isSpeciality ?? false),
    orderUid,
    placeOfService: o.placeOfService || fac.placeOfService || "",
    retryCount: 0,
    token: token ?? "",
  };

  return {
    content,
    queueName,
    notes,
    meta: { profile: profile ?? "(default)", env, physicianId, queueName, orderStatus: o.status },
  };
}

export async function buildQueueItemContent(
  orderUid: string,
  opts: { profile?: string | null; env: Env; automationId?: string | null }, // env is required — never defaulted (repo invariant)
): Promise<BuiltQueueItem> {
  const profile = opts.profile ?? null;
  const session = await connect(opts.env, profile);
  const { client, token } = session;
  const providerMail =
    session.mode === "support"
      ? ((await clinicOwnerEmail(opts.env, session.clinicUid as string)) ?? "")
      : session.email;

  const built = assembleQueueItemContent({
    order: await fetchOrder(client, orderUid),
    orderUid,
    profile,
    env: opts.env,
    automationId: resolveAutomationId(opts.automationId, profile),
    token,
    physicianId: decodeJwtId(token),
    supportMode: session.mode === "support",
    providerMail,
  });
  return { ...built, beBase: client.base };
}

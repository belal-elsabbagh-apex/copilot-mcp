// Apex queue-item serializer. Unlike UiPath's (which stringifies the array/object
// fields), Apex stores specific_content as native JSON — BE-built items carry real
// arrays, booleans, numbers and a callbackContext object — so the canonical content
// from orchestrator/queue-item.ts is kept as-is and checked against the native-type map
// (spec §9) instead of converted. A mismatch is a builder bug, never user input, so it
// throws a plain Error.

import { ExpectedError } from "../shared/expected-error.js";
import { isRecord } from "../shared/util.js";

type Kind = "array" | "boolean" | "number" | "object";

const IS_KIND: Record<Kind, (v: unknown) => boolean> = {
  array: Array.isArray,
  boolean: (v) => typeof v === "boolean",
  number: (v) => typeof v === "number",
  object: isRecord,
};

// OpenReferralInfo, eligibilityOtherPatientName and ReferredToReferralDistribution may be
// null, so they are not constrained here. Fields absent from the content are not required.
const NATIVE_FIELDS: Record<string, Kind> = {
  Units: "array",
  Diagnoses: "array",
  Modifiers: "array",
  Treatments: "array",
  ServiceCode: "array",
  DescriptionOfService: "array",
  IsRetro: "boolean",
  IsUrgent: "boolean",
  IsApproved: "boolean",
  isSpeciality: "boolean",
  addManualFacility: "boolean",
  IsSameAsRequestingProvider: "boolean",
  retryCount: "number",
  ReferredToCompleteReferralsCount: "number",
  ReferredToSuccessfulReferralsCount: "number",
  callbackContext: "object",
};

const typeName = (v: unknown): string =>
  v === null ? "null" : Array.isArray(v) ? "array" : typeof v;

export function serializeApexContent(content: Record<string, unknown>): Record<string, unknown> {
  const wrong = Object.entries(NATIVE_FIELDS)
    .filter(([key, kind]) => key in content && !IS_KIND[kind](content[key]))
    .map(([key, kind]) => `${key} (expected ${kind}, got ${typeName(content[key])})`);
  if (wrong.length) {
    throw new Error(
      `Apex queue-item content breaks the native-type map: ${wrong.join(", ")} — ` +
        "the canonical builder must emit native arrays/booleans/numbers/objects, not UiPath-style strings",
    );
  }
  return { ...content };
}

// Apex routes an item by match_value; BE-built items use match_key "automationId" with
// match_value == specific_content.automationId (verified live). Shared by build + add so
// both derive it identically.
export function matchValueOf(specificContent: Record<string, unknown>): string {
  const id = specificContent["automationId"];
  if (typeof id !== "string" || !id.trim()) {
    throw new ExpectedError(
      "specific_content.automationId is required — Apex routes the item by match_value = " +
        "automationId (build_queue_item sets it; pass automationId to override)",
    );
  }
  return id;
}

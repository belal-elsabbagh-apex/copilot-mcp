import { describe, expect, test } from "bun:test";
import { ExpectedError } from "../shared/expected-error.js";
import { matchValueOf, serializeApexContent } from "./payload.js";

const native = () => ({
  Units: ["1"],
  Diagnoses: ["M54.5"],
  Modifiers: [],
  Treatments: ["T1"],
  ServiceCode: ["99213"],
  DescriptionOfService: ["Office visit"],
  IsRetro: false,
  IsUrgent: true,
  IsApproved: false,
  isSpeciality: false,
  addManualFacility: false,
  IsSameAsRequestingProvider: false,
  retryCount: 0,
  ReferredToCompleteReferralsCount: 3,
  ReferredToSuccessfulReferralsCount: 2,
  callbackContext: { physicianId: 7, orderUid: "ord-1" },
  OpenReferralInfo: null,
  eligibilityOtherPatientName: null,
  ReferredToReferralDistribution: null,
  automationId: "auto-1",
  orderUid: "ord-1",
});

describe("serializeApexContent", () => {
  test("keeps every native type (no stringification) and passes unlisted fields through", () => {
    const content = native();
    const out = serializeApexContent(content);
    expect(out).toEqual(content);
    expect(out).not.toBe(content);
    expect(Array.isArray(out["Units"])).toBe(true);
    expect(out["callbackContext"]).toEqual({ physicianId: 7, orderUid: "ord-1" });
    expect(out["OpenReferralInfo"]).toBeNull();
  });

  test("accepts partial content: absent fields are not required", () => {
    expect(serializeApexContent({ orderUid: "ord-1" })).toEqual({ orderUid: "ord-1" });
  });

  test("rejects UiPath-style stringified arrays and objects", () => {
    expect(() => serializeApexContent({ ...native(), Diagnoses: '["M54.5"]' })).toThrow(
      /Diagnoses \(expected array, got string\)/,
    );
    expect(() =>
      serializeApexContent({ ...native(), callbackContext: '{"physicianId":7}' }),
    ).toThrow(/callbackContext \(expected object, got string\)/);
  });

  test("rejects stringified booleans and numbers", () => {
    expect(() => serializeApexContent({ ...native(), IsUrgent: "false" })).toThrow(/IsUrgent/);
    expect(() => serializeApexContent({ ...native(), retryCount: "0" })).toThrow(/retryCount/);
  });

  test("null is allowed only on the nullable fields", () => {
    expect(() => serializeApexContent({ ...native(), Units: null })).toThrow(
      /Units \(expected array, got null\)/,
    );
    expect(() => serializeApexContent({ ...native(), Units: undefined })).toThrow(/Units/);
  });

  test("reports every violation in one error", () => {
    expect(() => serializeApexContent({ ...native(), Units: "1", IsRetro: "no" })).toThrow(
      /Units.*IsRetro/,
    );
  });
});

describe("matchValueOf", () => {
  test("returns the automationId", () => {
    expect(matchValueOf({ automationId: "auto-1" })).toBe("auto-1");
  });

  test.each([
    {},
    { automationId: "" },
    { automationId: "   " },
    { automationId: 5 },
  ])("refuses a missing/empty/non-string automationId: %j", (sc) => {
    expect(() => matchValueOf(sc)).toThrow(ExpectedError);
  });
});

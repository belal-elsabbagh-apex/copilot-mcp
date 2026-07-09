import { describe, expect, test } from "bun:test";
import type { Env, OrderOverride } from "../config/config.js";
import {
  assertPreProdClient,
  type BeOrder,
  type HttpClient,
  makeClient,
} from "./copilot-client.js";
import { type FacilityRemap, mintPreprodOrder, specFromProdOrder } from "./mirror.js";

const SRC: BeOrder = {
  orderUid: "prod-1",
  patient: {
    patientName: "DOE, JOHN",
    patientBirthDate: "1980-01-02",
    patientPhoneNumber: "5551234567",
  },
  insurance: { name: "Acme Health", memberId: "M123" },
  orderType: { typeUid: "type-prod", name: "Outbound Referral" },
  referredFacility: { referredFacilityUid: "fac-prod", name: "Imaging Center", NPI: "1234567890" },
  clinicProvider: { clinicProviderUid: "clinic-prod" },
  orderNames: [{ nameUid: "name-1" }, {}, { nameUid: "name-2" }],
  ICDCodes: [{ code: "M54.5", description: "Low back pain" }, { code: "R51" }],
  CPTCodes: [{ code: "72148", units: 1, description: "MRI lumbar", treatments: null }],
  location: "Main St",
  uploadAuth: true,
  retro: false,
  authorization: { authNumber: "A1" },
  placeOfService: "Office",
};

const FAC_MAP: FacilityRemap = {
  referredFacilityUid: "fac-pre",
  specialityUid: "spec-pre",
  placeOfService: "Outpatient",
  name: "Imaging Center",
  npi: "1234567890",
};

describe("specFromProdOrder", () => {
  test("maps a plain prod order with sentinels and filtered nameUids", () => {
    const spec = specFromProdOrder(SRC, {}, null);
    expect(spec.patientName).toBe("DOE, JOHN");
    expect(spec.patientBirthDate).toBe("01/02/1980");
    expect(spec.insuranceName).toBe("Acme Health");
    expect(spec.typeUid).toBe("type-prod");
    expect(spec.specialityUid).toBeNull();
    expect(spec.referredFacilityUid).toBe("fac-prod"); // no remap -> prod uid as-is
    expect(spec.referredProviderUid).toBe(""); // mirror never re-links a referred provider
    expect(spec.clinicProviderUid).toBe("clinic-prod");
    expect(spec.orderNamesUids).toEqual(["name-1", "name-2"]); // undefined nameUid dropped
    expect(spec.cptCodes).toEqual([]); // no injectCPTs override
    expect(spec.uploadAuth).toBe(true);
    expect(spec.uploadFax).toBe(false);
    expect(spec.authorization).toEqual({ authNumber: "A1", sendReferralAfterAuth: true });
    expect(spec.appointmentDate).toBe(""); // none on the order
    expect(spec.icdCodes).toEqual([
      { code: "M54.5", description: "Low back pain" },
      { code: "R51", description: "" },
    ]);
    expect(spec.placeOfService).toBe("Office");
  });

  test("facility remap wins over the prod uids when no override is set", () => {
    const spec = specFromProdOrder(SRC, {}, FAC_MAP);
    expect(spec.referredFacilityUid).toBe("fac-pre");
    expect(spec.specialityUid).toBe("spec-pre");
    expect(spec.placeOfService).toBe("Office"); // prod POS still beats the remap fallback
  });

  test("explicit overrides win over both remap and prod values", () => {
    const ov: OrderOverride = {
      typeUid: "type-ov",
      specialityUid: "spec-ov",
      referredFacilityUid: "fac-ov",
      orderNamesUids: ["name-ov"],
      placeOfService: "Telehealth",
    };
    const spec = specFromProdOrder(SRC, ov, FAC_MAP);
    expect(spec.typeUid).toBe("type-ov");
    expect(spec.specialityUid).toBe("spec-ov");
    expect(spec.referredFacilityUid).toBe("fac-ov");
    expect(spec.orderNamesUids).toEqual(["name-ov"]);
    expect(spec.placeOfService).toBe("Telehealth");
  });

  test("a facility override drops prod's clinicProvider (different account)", () => {
    const spec = specFromProdOrder(SRC, { referredFacilityUid: "fac-ov" }, null);
    expect(spec.clinicProviderUid).toBe("");
  });

  test("injectCPTs gates the CPT payload", () => {
    const spec = specFromProdOrder(SRC, { injectCPTs: true }, null);
    expect(spec.cptCodes).toEqual([
      { code: "72148", units: 1, description: "MRI lumbar", treatments: null },
    ]);
  });

  test("a past appointmentDate is shifted forward", () => {
    const spec = specFromProdOrder({ ...SRC, appointmentDate: "01/15/2020" }, {}, null);
    expect(spec.appointmentDate).toBe("04/15/2020"); // +3 months, single shift
  });

  test("an empty order collapses to sentinels", () => {
    const spec = specFromProdOrder({}, {}, null);
    expect(spec.patientName).toBe("");
    expect(spec.patientBirthDate).toBe("");
    expect(spec.typeUid).toBe("");
    expect(spec.referredFacilityUid).toBe("");
    expect(spec.orderNamesUids).toEqual([]);
    expect(spec.authorization).toBeNull();
    expect(spec.icdCodes).toEqual([]);
  });
});

// A client whose req must never fire — proves the env guard rejects before any HTTP.
const stubClient = (env?: Env): HttpClient => ({
  base: "http://stub.invalid",
  ...(env ? { env } : {}),
  req: () => {
    throw new Error("unexpected HTTP call — the env guard should have thrown first");
  },
});

describe("pre-prod write guard", () => {
  test("assertPreProdClient accepts only a pre_prod-tagged client", () => {
    expect(() => assertPreProdClient(stubClient("pre_prod"), "test")).not.toThrow();
    expect(() => assertPreProdClient(stubClient("prod"), "test")).toThrow(/pre_prod-only/);
    expect(() => assertPreProdClient(stubClient(), "test")).toThrow(/untagged/);
  });

  test("makeClient tags the client with its env", () => {
    expect(makeClient("http://stub.invalid", "pre_prod").env).toBe("pre_prod");
    expect(makeClient("http://stub.invalid", "prod").env).toBe("prod");
    expect(makeClient("http://stub.invalid").env).toBeUndefined();
  });

  test("mintPreprodOrder refuses a prod or untagged client before any HTTP", async () => {
    const spec = specFromProdOrder(SRC, {}, null);
    await expect(mintPreprodOrder(stubClient("prod"), spec, { submit: false })).rejects.toThrow(
      /pre_prod-only/,
    );
    await expect(mintPreprodOrder(stubClient(), spec, { submit: false })).rejects.toThrow(
      /pre_prod-only/,
    );
  });
});

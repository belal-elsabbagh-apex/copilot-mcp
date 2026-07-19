import { describe, expect, test } from "bun:test";
import type { Env } from "../config/config.js";
import { assertPreProdClient, type HttpClient, makeClient } from "./copilot-client.js";
import { type MintSpec, mintPreprodOrder } from "./mirror.js";

const SPEC: MintSpec = {
  patientName: "DOE, JOHN",
  patientBirthDate: "01/02/1980",
  patientPhoneNumber: "5551234567",
  insuranceName: "Acme Health",
  insuranceMemberId: "M123",
  location: "Main St",
  typeUid: "type-1",
  specialityUid: null,
  referredFacilityUid: "fac-1",
  referredProviderUid: "",
  clinicProviderUid: "",
  orderNamesUids: ["name-1"],
  cptCodes: [],
  uploadAuth: false,
  uploadFax: false,
  retro: false,
  authorization: null,
  appointmentDate: "",
  icdCodes: [],
  placeOfService: "",
};

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
    await expect(mintPreprodOrder(stubClient("prod"), SPEC)).rejects.toThrow(/pre_prod-only/);
    await expect(mintPreprodOrder(stubClient(), SPEC)).rejects.toThrow(/pre_prod-only/);
  });
});

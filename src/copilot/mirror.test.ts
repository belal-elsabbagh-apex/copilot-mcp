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

// A client that answers every call in the mint sequence with a generic success —
// draft creation, every PUT, /process (always "forReview" so the final retry loop
// returns on its first attempt, no real sleep), /note/upload, and /orders/filter
// (verify's lookup) all resolve on the first try.
function happyClient(): HttpClient {
  return {
    base: "http://stub.invalid",
    env: "pre_prod",
    req: async (_method: string, path: string) => {
      if (path === "/api/v1/orders")
        return { status: 200, data: { order: { orderUid: "new-uid" } }, text: "" };
      if (path.endsWith("/process")) return { status: 200, data: { msg: "forReview" }, text: "" };
      if (path === "/api/v1/orders/filter")
        return {
          status: 200,
          data: { data: [{ orderUid: "new-uid", status: "forReview" }] },
          text: "",
        };
      return { status: 200, data: {}, text: "" }; // PUTs, note/upload
    },
  };
}

describe("mintPreprodOrder progress reporting", () => {
  test("calls onProgress at each milestone, in addition to the ported console.log calls", async () => {
    const messages: string[] = [];
    const result = await mintPreprodOrder(happyClient(), SPEC, (m) => messages.push(m));
    expect(result.newUid).toBe("new-uid");
    expect(messages).toContain("new draft: new-uid");
    expect(messages).toContain("PUT patient: ok");
    expect(messages).toContain("PUT orderNames: ok");
    expect(messages.some((m) => m.startsWith("/process"))).toBe(true);
    expect(messages).toContain("/note/upload: ok");
    expect(messages).toContain("verify: done");
    // SPEC.clinicProviderUid is "" — sentBy is left to the FE default.
    expect(messages).toContain("(sentBy: left to FE default — clinicProvider not mapped)");
  });

  test("onProgress defaults to a no-op when the caller doesn't pass one", async () => {
    await expect(mintPreprodOrder(happyClient(), SPEC)).resolves.toMatchObject({
      newUid: "new-uid",
    });
  });
});

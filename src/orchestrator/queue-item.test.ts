import { describe, expect, test } from "bun:test";
import type { BeOrder } from "../copilot/copilot-client.js";
import {
  type AssembleInput,
  assembleQueueItemContent,
  decodeJwtId,
  resolveAutomationId,
  splitName,
  toMDY,
} from "./queue-item.js";

describe("toMDY", () => {
  test("zero-pads US M/D/YYYY", () => {
    expect(toMDY("1/2/2024")).toBe("01/02/2024");
    expect(toMDY("12/31/2024")).toBe("12/31/2024");
  });
  test("converts ISO YYYY-MM-DD to MM/DD/YYYY", () => {
    expect(toMDY("2024-03-04")).toBe("03/04/2024");
  });
  test("returns '' for empty/nullish/unparseable input", () => {
    expect(toMDY("")).toBe("");
    expect(toMDY(null)).toBe("");
    expect(toMDY(undefined)).toBe("");
    expect(toMDY("not a date")).toBe("");
  });
});

describe("splitName", () => {
  test("splits 'Last, First M' and uppercases", () => {
    expect(splitName("Doe, John A")).toEqual({ last: "DOE", first: "JOHN" });
  });
  test("treats a comma-less name as the last name", () => {
    expect(splitName("Madonna")).toEqual({ last: "MADONNA", first: "" });
  });
  test("handles undefined", () => {
    expect(splitName(undefined)).toEqual({ last: "", first: "" });
  });
});

describe("resolveAutomationId", () => {
  test("an explicit automationId always wins over the account table", () => {
    expect(resolveAutomationId("explicit-id", "kafri")).toBe("explicit-id");
  });
  test("resolves from a known account (profile), case-insensitively", () => {
    expect(resolveAutomationId(undefined, "kafri")).toBe("c741d791-3e3b-4102-b95c-f94549949c9a");
    expect(resolveAutomationId(undefined, "SCLC")).toBe("85619cc6-5e25-4219-ad12-fc9877aaf841");
  });
  test("returns undefined for an unknown/missing account and no explicit id", () => {
    expect(resolveAutomationId(undefined, "unknown-account")).toBeUndefined();
    expect(resolveAutomationId(undefined, null)).toBeUndefined();
    expect(resolveAutomationId("", null)).toBeUndefined();
  });
});

describe("decodeJwtId", () => {
  test("extracts a numeric id from a JWT payload", () => {
    const payload = Buffer.from(JSON.stringify({ id: 42 })).toString("base64");
    expect(decodeJwtId(`header.${payload}.sig`)).toBe(42);
  });
  test("extracts a string id", () => {
    const payload = Buffer.from(JSON.stringify({ id: "abc" })).toString("base64");
    expect(decodeJwtId(`header.${payload}.sig`)).toBe("abc");
  });
  test("returns null for malformed tokens or missing id", () => {
    expect(decodeJwtId(undefined)).toBeNull();
    expect(decodeJwtId("garbage")).toBeNull();
    const noId = Buffer.from(JSON.stringify({ sub: "x" })).toString("base64");
    expect(decodeJwtId(`h.${noId}.s`)).toBeNull();
  });
});

describe("assembleQueueItemContent", () => {
  const UID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee";
  const input = (order: BeOrder, over: Partial<AssembleInput> = {}): AssembleInput => ({
    order,
    orderUid: UID,
    profile: "ossm",
    env: "pre_prod",
    automationId: "auto-1",
    token: "h.p.s",
    physicianId: 7,
    supportMode: false,
    providerMail: "owner@example.com",
    ...over,
  });

  test("emits native types and leaves the backend-owned URL fields out", () => {
    const { content } = assembleQueueItemContent(
      input({
        ICDCodes: [{ code: "I77.9" }],
        CPTCodes: [{ code: "99213", description: "Visit", units: 2, treatments: ["ECG"] }],
      }),
    );
    expect(content["Diagnoses"]).toEqual(["I77.9"]);
    expect(content["Units"]).toEqual(["2"]);
    expect(content["Treatments"]).toEqual(["ECG"]);
    expect(content["callbackContext"]).toEqual({ physicianId: 7, orderUid: UID });
    expect(content["IsApproved"]).toBe(false);
    expect("serverURL" in content).toBe(false);
    expect("queueUrl" in content).toBe(false);
  });

  test("DOS prefers encounterDate, then appointmentDate, then orderDate; the fallback note appears only without an encounterDate", () => {
    const withEncounter = assembleQueueItemContent(
      input({ encounterDate: "1/2/2026", appointmentDate: "3/4/2026", orderDate: "5/6/2026" }),
    );
    expect(withEncounter.content["DOS"]).toBe("01/02/2026");
    expect(withEncounter.notes.some((n) => n.startsWith("DOS:"))).toBe(false);

    const fallback = assembleQueueItemContent(
      input({ appointmentDate: "3/4/2026", orderDate: "5/6/2026" }),
    );
    expect(fallback.content["DOS"]).toBe("03/04/2026");
    expect(fallback.notes.some((n) => n.startsWith("DOS:"))).toBe(true);
    expect(assembleQueueItemContent(input({ orderDate: "5/6/2026" })).content["DOS"]).toBe(
      "05/06/2026",
    );
  });

  test("referral-form values win over the referred facility, which fills the gaps", () => {
    const { content } = assembleQueueItemContent(
      input({
        referredFacility: {
          NPI: "FAC-NPI",
          address: "Facility Rd",
          phone: "555-0001",
          type: "facility",
        },
        referralFormJSON: { to: { NPI: "FORM-NPI" } },
      }),
    );
    expect(content["ReferredToNPI"]).toBe("FORM-NPI");
    expect(content["ReferredToAddress"]).toBe("Facility Rd");
    expect(content["ReferredToPhone"]).toBe("555-0001");
    expect(content["ReferredToType"]).toBe("facility");
  });

  test("ProviderLastName is empty when the provider name has no comma", () => {
    const { content } = assembleQueueItemContent(
      input({ referralFormJSON: { from: { name: "Clinic Group" } } }),
    );
    expect(content["ProviderFirstName"]).toBe("Clinic Group");
    expect(content["ProviderLastName"]).toBe("");
  });

  test("an unresolved automationId becomes a placeholder UUID with a warning; a resolved one does not", () => {
    const unresolved = assembleQueueItemContent(input({}, { automationId: undefined }));
    expect(unresolved.content["automationId"]).toMatch(/^[0-9a-f-]{36}$/);
    expect(unresolved.notes.some((n) => n.includes("no automationId was supplied"))).toBe(true);

    const resolved = assembleQueueItemContent(input({}));
    expect(resolved.content["automationId"]).toBe("auto-1");
    expect(resolved.notes.some((n) => n.includes("no automationId"))).toBe(false);
  });

  test("a missing token is flagged and emitted as an empty string", () => {
    const out = assembleQueueItemContent(input({}, { token: undefined }));
    expect(out.content["token"]).toBe("");
    expect(out.notes.some((n) => n.includes("login returned no token"))).toBe(true);
  });

  test("support-account auth adds the physicianId note; meta carries the queue name", () => {
    const out = assembleQueueItemContent(
      input({ insurance: { name: "Acme Health" }, status: "inProgress" }, { supportMode: true }),
    );
    expect(out.notes.some((n) => n.startsWith("physicianId 7 is the support account"))).toBe(true);
    expect(out.queueName).toBe("Acme Health auth submit queue");
    expect(out.meta).toEqual({
      profile: "ossm",
      env: "pre_prod",
      physicianId: 7,
      queueName: "Acme Health auth submit queue",
      orderStatus: "inProgress",
    });
  });
});

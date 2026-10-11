import { describe, expect, spyOn, test } from "bun:test";
import { ExpectedError } from "../shared/expected-error.js";
import { apexBuildQueueItemPayload } from "./backend.js";
import { APEX_URL, useApexStub } from "./test-support.js";

// useApexStub's default apex config (only pre_prod has a serverUrlByEnv entry).
const SERVER_URL = "https://be.pre.example";
const QUEUE_URL = "https://sqs.example/q";

const content = () => ({
  orderUid: "ord-1",
  automationId: "auto-1",
  IsApproved: true,
  Units: ["1", "2"],
  Diagnoses: ["M54.5"],
  ServiceCode: ["99213"],
  DescriptionOfService: ["Office visit"],
  Treatments: [],
  IsRetro: false,
  IsUrgent: false,
  isSpeciality: true,
  addManualFacility: false,
  IsSameAsRequestingProvider: false,
  retryCount: 0,
  callbackContext: { physicianId: 7, orderUid: "ord-1" },
  eligibilityOtherPatientName: null,
});

interface Built {
  queueName: string;
  body: { specific_content: Record<string, unknown>; reference: string; match_value: string };
}

const args = (
  over: Partial<{ env: "prod" | "pre_prod"; content: Record<string, unknown> }> = {},
) => ({
  env: "pre_prod" as const,
  queueName: "CENCAL auth submit queue",
  orderUid: "ord-1",
  content: content(),
  ...over,
});

describe("apexBuildQueueItemPayload", () => {
  useApexStub();

  test("keeps native types, forces IsApproved=false, and fills serverURL/queueUrl from config", () => {
    // The incoming content tries to point the robot at another host: config must win.
    const out = apexBuildQueueItemPayload(
      args({ content: { ...content(), serverURL: "https://evil.example.com" } }),
    );
    const sc = (out.payload as Built).body.specific_content;
    expect(sc["IsApproved"]).toBe(false);
    expect(sc["serverURL"]).toBe(SERVER_URL);
    expect(sc["queueUrl"]).toBe(QUEUE_URL);
    expect(sc["Units"]).toEqual(["1", "2"]);
    expect(sc["callbackContext"]).toEqual({ physicianId: 7, orderUid: "ord-1" });
    expect(sc["isSpeciality"]).toBe(true);
    expect(sc["retryCount"]).toBe(0);
    expect(sc["eligibilityOtherPatientName"]).toBeNull();
  });

  test("payload shape: reference is <orderUid>-<epoch seconds>, match_value is automationId", () => {
    const spy = spyOn(Date, "now").mockReturnValue(1_700_000_000_999);
    try {
      const out = apexBuildQueueItemPayload(args());
      const built = out.payload as Built;
      expect(built.queueName).toBe("CENCAL auth submit queue");
      expect(built.body.reference).toBe("ord-1-1700000000");
      expect(built.body.match_value).toBe("auto-1");
      expect(Object.keys(built).sort()).toEqual(["body", "queueName"]);
      expect(Object.keys(built.body).sort()).toEqual([
        "match_value",
        "reference",
        "specific_content",
      ]);
      expect(out.meta).toEqual({
        queueName: "CENCAL auth submit queue",
        env: "pre_prod",
        orderUid: "ord-1",
        note: "post with add_queue_item(orchestrator: apex)",
      });
      expect(out.notes.length).toBeGreaterThan(0);
    } finally {
      spy.mockRestore();
    }
  });

  test("does not mutate the caller's content", () => {
    const c = content();
    apexBuildQueueItemPayload(args({ content: c }));
    expect(c.IsApproved).toBe(true);
    expect("serverURL" in c).toBe(false);
  });

  test("an env without apex.serverUrlByEnv.<env> is an ExpectedError naming the key (no fallback)", () => {
    const build = () => apexBuildQueueItemPayload(args({ env: "prod" }));
    expect(build).toThrow(ExpectedError);
    expect(build).toThrow(/apex\.serverUrlByEnv\.prod/);
  });

  test("content without a usable automationId cannot be routed", () => {
    expect(() =>
      apexBuildQueueItemPayload(args({ content: { ...content(), automationId: "" } })),
    ).toThrow(/automationId/);
  });

  test("UiPath-style stringified content is rejected, not posted", () => {
    expect(() =>
      apexBuildQueueItemPayload(args({ content: { ...content(), Diagnoses: '["M54.5"]' } })),
    ).toThrow(/Diagnoses/);
  });
});

describe("apexBuildQueueItemPayload without apex.queueUrl", () => {
  useApexStub({
    apex: {
      apiUrl: APEX_URL,
      username: "tester",
      password: "pw",
      serverUrlByEnv: { pre_prod: SERVER_URL },
    },
  });

  test("is an ExpectedError naming the key", () => {
    const build = () => apexBuildQueueItemPayload(args());
    expect(build).toThrow(ExpectedError);
    expect(build).toThrow(/apex\.queueUrl/);
  });
});

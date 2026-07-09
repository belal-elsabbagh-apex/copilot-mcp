import { describe, expect, test } from "bun:test";
import { guardQueueItemSafety, type QueueSafetyLimits } from "./safety.js";

const LIMITS: QueueSafetyLimits = {
  preProdServerUrl: "https://pre-prod-be-batch.example.com",
  queueUrl: "https://sqs.example.com/dev-queue",
};

const NO_LIMITS: QueueSafetyLimits = { preProdServerUrl: "", queueUrl: "" };

describe("guardQueueItemSafety — force mode", () => {
  test("forces IsApproved=false and reports it in forced", () => {
    const out = guardQueueItemSafety({ IsApproved: true, orderUid: "u1" }, "force", NO_LIMITS);
    expect(out.specificContent["IsApproved"]).toBe(false);
    expect(out.forced).toEqual(["IsApproved"]);
  });

  test("missing IsApproved counts as forced", () => {
    const out = guardQueueItemSafety({ orderUid: "u1" }, "force", NO_LIMITS);
    expect(out.specificContent["IsApproved"]).toBe(false);
    expect(out.forced).toEqual(["IsApproved"]);
  });

  test("already-false IsApproved is not reported as forced", () => {
    const out = guardQueueItemSafety({ IsApproved: false }, "force", NO_LIMITS);
    expect(out.specificContent["IsApproved"]).toBe(false);
    expect(out.forced).toEqual([]);
  });

  test("never mutates the input", () => {
    const sc = { IsApproved: true, orderUid: "u1" };
    guardQueueItemSafety(sc, "force", NO_LIMITS);
    expect(sc.IsApproved).toBe(true);
  });

  test("output matches the legacy inline-spread result", () => {
    const sc = { MemberID: "m1", IsApproved: true, serverURL: "https://prod.example.com" };
    const out = guardQueueItemSafety(sc, "force", NO_LIMITS);
    expect(out.specificContent).toEqual({ ...sc, IsApproved: false });
  });

  test("force mode does not validate serverURL/queueUrl/placeholders", () => {
    const sc = {
      serverURL: "https://prod.example.com",
      queueUrl: "https://sqs.example.com/prod",
      orderUid: "<TO-FILL>",
    };
    expect(() => guardQueueItemSafety(sc, "force", LIMITS)).not.toThrow();
  });
});

describe("guardQueueItemSafety — preProdPost mode", () => {
  test("accepts a valid build_queue_item-shaped payload", () => {
    const out = guardQueueItemSafety(
      {
        IsApproved: false,
        orderUid: "abc-123",
        serverURL: LIMITS.preProdServerUrl,
        queueUrl: LIMITS.queueUrl,
      },
      "preProdPost",
      LIMITS,
    );
    expect(out.forced).toEqual([]);
    expect(out.specificContent["IsApproved"]).toBe(false);
  });

  test("accepts empty serverURL/queueUrl (skill fixtures keep them blank)", () => {
    const out = guardQueueItemSafety(
      { IsApproved: false, orderUid: "abc", serverURL: "", queueUrl: "" },
      "preProdPost",
      NO_LIMITS,
    );
    expect(out.specificContent["orderUid"]).toBe("abc");
  });

  test("still forces IsApproved and reports it", () => {
    const out = guardQueueItemSafety({ IsApproved: true, orderUid: "abc" }, "preProdPost", LIMITS);
    expect(out.specificContent["IsApproved"]).toBe(false);
    expect(out.forced).toEqual(["IsApproved"]);
  });

  test("rejects a non-pre-prod serverURL", () => {
    expect(() =>
      guardQueueItemSafety({ serverURL: "https://prod-be.example.com" }, "preProdPost", LIMITS),
    ).toThrow(/serverURL 'https:\/\/prod-be\.example\.com' is not the configured pre-prod host/);
  });

  test("fails closed when serverURL is present but the limit is unconfigured", () => {
    expect(() =>
      guardQueueItemSafety({ serverURL: LIMITS.preProdServerUrl }, "preProdPost", NO_LIMITS),
    ).toThrow(/serverUrlByEnv\.pre_prod is not configured/);
  });

  test("rejects a foreign queueUrl", () => {
    expect(() =>
      guardQueueItemSafety({ queueUrl: "https://sqs.example.com/prod" }, "preProdPost", LIMITS),
    ).toThrow(/queueUrl .* neither empty nor the configured/);
  });

  test("rejects any queueUrl when uipath.queueUrl is unconfigured", () => {
    expect(() =>
      guardQueueItemSafety({ queueUrl: "https://sqs.example.com/dev" }, "preProdPost", NO_LIMITS),
    ).toThrow(/queueUrl .* must be empty/);
  });

  test("rejects <TO-FILL> placeholders and names the keys", () => {
    expect(() =>
      guardQueueItemSafety(
        { orderUid: "<TO-FILL>", token: "< to_fill after mint >", MemberID: "m1" },
        "preProdPost",
        LIMITS,
      ),
    ).toThrow(/placeholder .* orderUid, token/);
  });

  test("aggregates multiple violations into one error", () => {
    let message = "";
    try {
      guardQueueItemSafety(
        { serverURL: "https://prod.example.com", orderUid: "<TO-FILL>" },
        "preProdPost",
        LIMITS,
      );
    } catch (e) {
      message = e instanceof Error ? e.message : String(e);
    }
    expect(message).toMatch(/serverURL/);
    expect(message).toMatch(/placeholder/);
  });
});

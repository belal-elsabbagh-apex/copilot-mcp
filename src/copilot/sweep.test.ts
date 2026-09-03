import { describe, expect, test } from "bun:test";
import type { QueueItemMatch } from "../uipath/uipath.js";
import { crossCheckUipath, queueSearchSince, type StuckOrder } from "./sweep.js";

const stuckOrder = (orderUid: string | undefined): StuckOrder => ({ orderUid, ageHours: null });

const qItem = (over: Partial<QueueItemMatch>): QueueItemMatch => ({
  id: 1,
  reference: "ref",
  status: "New",
  executorJobKey: "",
  processingExceptionType: "",
  retryNumber: 0,
  creationTime: "2026-07-01T00:00:00Z",
  specificContent: {},
  queueDefinitionId: 0,
  ...over,
});

describe("crossCheckUipath", () => {
  test("skips entries with no orderUid, leaving their uipath field untouched", async () => {
    const stuck = [stuckOrder(undefined)];
    await crossCheckUipath(stuck, async () => []);
    expect(stuck[0]?.uipath).toBeUndefined();
  });

  test("attaches a verdict per order from the injected correlate function", async () => {
    const stuck = [stuckOrder("u1"), stuckOrder("u2")];
    await crossCheckUipath(stuck, async (orderUid) =>
      orderUid === "u1" ? [qItem({ status: "Successful", executorJobKey: "k1" })] : [],
    );
    expect(stuck[0]?.uipath?.verdict).toBe("job-successful-order-stuck");
    expect(stuck[1]?.uipath?.verdict).toBe("no-job");
  });

  test("a Failed queue item is job-faulted and surfaces the exception type", async () => {
    // The faulted consumer job is invisible to an OutputArguments scan; the queue
    // item's Status is what makes it directly determinable.
    const stuck = [stuckOrder("u1")];
    await crossCheckUipath(stuck, async () => [
      qItem({
        status: "Failed",
        executorJobKey: "k1",
        processingExceptionType: "BusinessException",
      }),
    ]);
    expect(stuck[0]?.uipath?.verdict).toBe("job-faulted");
    expect(stuck[0]?.uipath?.queueItemStatus).toBe("Failed");
    expect(stuck[0]?.uipath?.processingExceptionType).toBe("BusinessException");
  });

  test("a New item with no ExecutorJobKey is queued-not-picked-up", async () => {
    const stuck = [stuckOrder("u1")];
    await crossCheckUipath(stuck, async () => [qItem({ status: "New" })]);
    expect(stuck[0]?.uipath?.verdict).toBe("queued-not-picked-up");
  });

  test("InProgress wins over an older Successful item (running)", async () => {
    const stuck = [stuckOrder("u1")];
    await crossCheckUipath(stuck, async () => [
      qItem({ status: "InProgress", executorJobKey: "k2" }),
      qItem({ status: "Successful", executorJobKey: "k1" }),
    ]);
    expect(stuck[0]?.uipath?.verdict).toBe("job-running");
  });

  test("a rejected lookup attaches an error verdict instead of losing the batch", async () => {
    const stuck = [stuckOrder("u1"), stuckOrder("u2")];
    await crossCheckUipath(stuck, async (orderUid) => {
      if (orderUid === "u1") throw new Error("orchestrator timeout");
      return [];
    });
    expect(stuck[0]?.uipath?.verdict).toBe("error: orchestrator timeout");
    expect(stuck[0]?.uipath?.queueItemCount).toBe(0);
    // u2's lookup isn't blocked/lost by u1's rejection.
    expect(stuck[1]?.uipath?.verdict).toBe("no-job");
  });

  test("batches in groups of 10 — the 11th candidate isn't queried until batch 2", async () => {
    const stuck = Array.from({ length: 11 }, (_, i) => stuckOrder(`u${i}`));
    const seenAtCall: number[][] = [];
    let inFlight = 0;
    await crossCheckUipath(stuck, async () => {
      inFlight++;
      seenAtCall.push([inFlight]);
      await Promise.resolve();
      inFlight--;
      return [];
    });
    // First 10 all start concurrently (inFlight reaches 10) before the 11th starts.
    const maxConcurrent = Math.max(...seenAtCall.map(([n]) => n ?? 0));
    expect(maxConcurrent).toBe(10);
  });

  test("reports progress per completed batch, counting only real candidates", async () => {
    // 11 with a uid + 1 without: the uid-less one is never queried, so it's not in the total.
    const stuck = [
      ...Array.from({ length: 11 }, (_, i) => stuckOrder(`u${i}`)),
      stuckOrder(undefined),
    ];
    const steps: string[] = [];
    await crossCheckUipath(
      stuck,
      async () => [],
      (done, total) => steps.push(`${done}/${total}`),
    );
    expect(steps).toEqual(["10/11", "11/11"]);
  });
});

describe("queueSearchSince", () => {
  test("no caller since, no order creationDate: unbounded", () => {
    expect(queueSearchSince(undefined, undefined)).toBeUndefined();
  });

  test("no caller since: floors to the order's creationDate minus the processing margin", () => {
    expect(queueSearchSince(undefined, "2026-08-20T12:00:00.000Z")).toBe(
      "2026-08-19T12:00:00.000Z",
    );
  });

  test("caller since later than the order's own floor: caller wins (tighter)", () => {
    expect(queueSearchSince("2026-08-20T00:00:00.000Z", "2026-08-01T00:00:00.000Z")).toBe(
      "2026-08-20T00:00:00.000Z",
    );
  });

  test("order's own floor later than the caller since: order wins (tighter) — never loosened", () => {
    expect(queueSearchSince("2026-01-01T00:00:00.000Z", "2026-08-20T12:00:00.000Z")).toBe(
      "2026-08-19T12:00:00.000Z",
    );
  });

  test("unparseable creationDate is ignored, falling back to the caller since", () => {
    expect(queueSearchSince("2026-08-20T00:00:00.000Z", "not-a-date")).toBe(
      "2026-08-20T00:00:00.000Z",
    );
  });
});

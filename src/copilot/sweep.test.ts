import { describe, expect, test } from "bun:test";
import type { UiPathJob } from "../uipath/uipath.js";
import { crossCheckUipath, type StuckOrder } from "./sweep.js";

const stuckOrder = (orderUid: string | undefined): StuckOrder => ({ orderUid, ageHours: null });

describe("crossCheckUipath", () => {
  test("skips entries with no orderUid, leaving their uipath field untouched", async () => {
    const stuck = [stuckOrder(undefined)];
    await crossCheckUipath(stuck, async () => []);
    expect(stuck[0]?.uipath).toBeUndefined();
  });

  test("attaches a verdict per order from the injected search function", async () => {
    const stuck = [stuckOrder("u1"), stuckOrder("u2")];
    const job = (state: string): UiPathJob => ({ Key: "k", State: state, ReleaseName: "OPTUM" });
    await crossCheckUipath(stuck, async (orderUid) =>
      orderUid === "u1" ? [job("Successful")] : [],
    );
    expect(stuck[0]?.uipath?.verdict).toBe("job-successful-order-stuck");
    expect(stuck[1]?.uipath?.verdict).toBe("no-job");
  });

  test("a rejected lookup attaches an error verdict instead of losing the batch", async () => {
    const stuck = [stuckOrder("u1"), stuckOrder("u2")];
    await crossCheckUipath(stuck, async (orderUid) => {
      if (orderUid === "u1") throw new Error("orchestrator timeout");
      return [];
    });
    expect(stuck[0]?.uipath?.verdict).toBe("error: orchestrator timeout");
    expect(stuck[0]?.uipath?.jobCount).toBe(0);
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
});

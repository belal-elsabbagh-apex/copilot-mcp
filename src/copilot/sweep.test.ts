import { describe, expect, test } from "bun:test";
import type { FindOrderAcrossResult } from "../orchestrator/find-order.js";
import type { JobAttempt, Orchestrator, QueueItemCore } from "../orchestrator/types.js";
import { crossCheckOrders, itemsOrThrow, queueSearchSince, type StuckOrder } from "./sweep.js";

const APEX = "apex" as const;

const stuckOrder = (orderUid: string | undefined): StuckOrder => ({ orderUid, ageHours: null });

const attempt = (over: Partial<JobAttempt> = {}): JobAttempt => ({
  orchestrator: "uipath",
  id: "j1",
  state: null,
  neverStarted: null,
  ...over,
});

const qItem = (over: Partial<QueueItemCore> = {}): QueueItemCore => ({
  orchestrator: "uipath",
  queueId: "1",
  queueName: "queue",
  itemId: "1",
  reference: "ref",
  orderUid: null,
  status: "New",
  rawStatus: "New",
  exceptionType: null,
  createdAt: "2026-07-01T00:00:00Z",
  endedAt: null,
  jobs: [],
  specificContent: null,
  details: {},
  ...over,
});

describe("crossCheckOrders", () => {
  test("skips entries with no orderUid, leaving their orchestrator field untouched", async () => {
    const stuck = [stuckOrder(undefined)];
    await crossCheckOrders(stuck, async () => []);
    expect(stuck[0]?.orchestrator).toBeUndefined();
  });

  test("attaches a verdict per order from the injected correlate function", async () => {
    const stuck = [stuckOrder("u1"), stuckOrder("u2")];
    await crossCheckOrders(stuck, async (orderUid) =>
      orderUid === "u1" ? [qItem({ status: "Successful", jobs: [attempt()] })] : [],
    );
    expect(stuck[0]?.orchestrator?.verdict).toBe("job-successful-order-stuck");
    expect(stuck[1]?.orchestrator).toEqual({ backend: null, verdict: "no-job", queueItemCount: 0 });
  });

  test("a Failed queue item is job-faulted and surfaces the exception type", async () => {
    // The faulted consumer job is invisible to an OutputArguments scan; the queue
    // item's status is what makes it directly determinable.
    const stuck = [stuckOrder("u1")];
    await crossCheckOrders(stuck, async () => [
      qItem({ status: "Failed", exceptionType: "business", jobs: [attempt()] }),
    ]);
    expect(stuck[0]?.orchestrator).toEqual({
      backend: "uipath",
      verdict: "job-faulted",
      queueItemCount: 1,
      queueItemStatus: "Failed",
      exceptionType: "business",
    });
  });

  test("a New item with no job attempt is queued-not-picked-up; once an attempt exists it is not", async () => {
    const stuck = [stuckOrder("u1"), stuckOrder("u2")];
    await crossCheckOrders(stuck, async (orderUid) =>
      orderUid === "u1"
        ? [qItem({ status: "New" })]
        : [qItem({ status: "New", jobs: [attempt()] })],
    );
    expect(stuck[0]?.orchestrator?.verdict).toBe("queued-not-picked-up");
    expect(stuck[1]?.orchestrator?.verdict).toBe("job-found");
  });

  test("InProgress wins over an older Successful item (running)", async () => {
    const stuck = [stuckOrder("u1")];
    await crossCheckOrders(stuck, async () => [
      qItem({ status: "InProgress", jobs: [attempt({ id: "j2" })] }),
      qItem({ status: "Successful", jobs: [attempt({ id: "j1" })] }),
    ]);
    expect(stuck[0]?.orchestrator?.verdict).toBe("job-running");
  });

  test("a never-started newest attempt is job-never-started, ahead of job-faulted", async () => {
    const stuck = [stuckOrder("u1")];
    await crossCheckOrders(stuck, async () => [
      qItem({
        status: "Failed",
        jobs: [
          attempt({ id: "j2", state: "Faulted", neverStarted: true }),
          attempt({ id: "j1", state: "Faulted", neverStarted: false }),
        ],
      }),
    ]);
    expect(stuck[0]?.orchestrator?.verdict).toBe("job-never-started");
    expect(stuck[0]?.orchestrator?.queueItemStatus).toBe("Failed");
  });

  test("only the newest attempt of the newest item decides never-started", async () => {
    const stuck = [stuckOrder("older-item"), stuckOrder("older-attempt"), stuckOrder("unknown")];
    await crossCheckOrders(stuck, async (orderUid) => {
      if (orderUid === "older-item") {
        // newest item succeeded; the reaped attempt belongs to an older, failed item
        return [
          qItem({ status: "Successful", jobs: [attempt({ neverStarted: false })] }),
          qItem({ status: "Failed", jobs: [attempt({ id: "j0", neverStarted: true })] }),
        ];
      }
      if (orderUid === "older-attempt") {
        // an earlier attempt was reaped, the retry is running
        return [
          qItem({
            status: "InProgress",
            jobs: [
              attempt({ id: "j2", neverStarted: false }),
              attempt({ id: "j1", neverStarted: true }),
            ],
          }),
        ];
      }
      // neverStarted: null means "backend can't tell", never "never started"
      return [qItem({ status: "Failed", jobs: [attempt({ neverStarted: null })] })];
    });
    expect(stuck.map((s) => s.orchestrator?.verdict)).toEqual([
      "job-faulted",
      "job-running",
      "job-faulted",
    ]);
  });

  test("attributes the verdict to the orchestrator holding the newest item", async () => {
    const stuck = [stuckOrder("both"), stuckOrder("none")];
    await crossCheckOrders(stuck, async (orderUid) =>
      orderUid === "both"
        ? [
            qItem({
              orchestrator: APEX,
              status: "InProgress",
              jobs: [attempt({ orchestrator: APEX })],
            }),
            qItem({ orchestrator: "uipath", status: "Failed", jobs: [attempt()] }),
          ]
        : [],
    );
    expect(stuck[0]?.orchestrator?.backend).toBe(APEX);
    expect(stuck[0]?.orchestrator?.queueItemCount).toBe(2);
    expect(stuck[1]?.orchestrator?.backend).toBeNull();
  });

  test("a rejected lookup attaches an error verdict instead of losing the batch", async () => {
    const stuck = [stuckOrder("u1"), stuckOrder("u2")];
    await crossCheckOrders(stuck, async (orderUid) => {
      if (orderUid === "u1") throw new Error("orchestrator timeout");
      return [];
    });
    expect(stuck[0]?.orchestrator).toEqual({
      backend: null,
      verdict: "error: orchestrator timeout",
      queueItemCount: 0,
    });
    // u2's lookup isn't blocked/lost by u1's rejection.
    expect(stuck[1]?.orchestrator?.verdict).toBe("no-job");
  });

  test("batches in groups of 10 — the 11th candidate isn't queried until batch 2", async () => {
    const stuck = Array.from({ length: 11 }, (_, i) => stuckOrder(`u${i}`));
    const seenAtCall: number[][] = [];
    let inFlight = 0;
    await crossCheckOrders(stuck, async () => {
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
    await crossCheckOrders(
      stuck,
      async () => [],
      (done, total) => steps.push(`${done}/${total}`),
    );
    expect(steps).toEqual(["10/11", "11/11"]);
  });
});

describe("itemsOrThrow", () => {
  const across = (
    items: QueueItemCore[],
    searchErrors?: Partial<Record<Orchestrator, string>>,
  ): FindOrderAcrossResult => ({
    count: items.length,
    jobIds: {},
    items,
    searched: ["uipath", APEX],
    backends: {},
    ...(searchErrors ? { searchErrors } : {}),
  });

  test("items found: the verdict stands on them even if another orchestrator's search failed", () => {
    const items = [qItem()];
    expect(itemsOrThrow(across(items, { [APEX]: "boom" }))).toBe(items);
  });

  test("nothing found and a search failed: unknown, not no-job — throws naming each failed orchestrator", () => {
    expect(() => itemsOrThrow(across([], { uipath: "503", [APEX]: "login failed" }))).toThrow(
      `uipath: 503; ${APEX}: login failed`,
    );
  });

  test("nothing found and nothing failed: an empty list (no-job)", () => {
    expect(itemsOrThrow(across([]))).toEqual([]);
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

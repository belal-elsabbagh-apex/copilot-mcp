// src/orchestrator/find-order.test.ts
import { describe, expect, test } from "bun:test";
import { ExpectedError } from "../shared/expected-error.js";
import type { FindOrderArgs, FindOrderResult, OrchestratorBackend } from "./backend.js";
import { findOrderAcross, selectBackends } from "./find-order.js";
import { searchableBackends } from "./registry.js";
import type { JobAttempt, Orchestrator, QueueItemCore } from "./types.js";

const APEX = "apex" as const;

const ARGS: FindOrderArgs = {
  env: "prod",
  orderUid: "order-0001",
  top: 50,
  includeSpecificContent: true,
};

const item = (
  orchestrator: Orchestrator,
  itemId: string,
  createdAt: string,
  jobIds: string[] = [],
): QueueItemCore => ({
  orchestrator,
  queueId: "q-1",
  queueName: "queue",
  itemId,
  reference: `ref-${itemId}`,
  orderUid: ARGS.orderUid,
  status: "New",
  rawStatus: "New",
  exceptionType: null,
  createdAt,
  endedAt: null,
  jobs: jobIds.map((id): JobAttempt => ({ orchestrator, id, state: null, neverStarted: null })),
  specificContent: null,
  details: {},
});

const result = (items: QueueItemCore[], over: Partial<FindOrderResult> = {}): FindOrderResult => ({
  folder: "Authorization",
  searchMode: "direct",
  scanned: items.length,
  items,
  notes: [],
  ...over,
});

const backend = (
  name: Orchestrator,
  find: (args: FindOrderArgs) => Promise<FindOrderResult>,
): OrchestratorBackend => ({ name, findOrderQueueItems: find }) as OrchestratorBackend;

describe("findOrderAcross", () => {
  test("merges every backend's items newest first; an unparseable createdAt sorts oldest", async () => {
    const out = await findOrderAcross(ARGS, [
      backend("uipath", async () =>
        result([
          item("uipath", "u3", "2026-07-03T00:00:00Z"),
          item("uipath", "u1", "2026-07-01T00:00:00Z"),
        ]),
      ),
      backend(APEX, async () =>
        result([
          item(APEX, "bad", "not-a-date"),
          item(APEX, "a2", "2026-07-02T00:00:00.123456+00:00"),
        ]),
      ),
    ]);
    expect(out.items.map((i) => i.itemId)).toEqual(["u3", "a2", "u1", "bad"]);
    expect(out.count).toBe(4);
    expect(out.searched).toEqual(["uipath", APEX]);
  });

  test("jobIds are per backend, deduped, newest item first; an answered backend with no jobs gets []", async () => {
    const out = await findOrderAcross(ARGS, [
      backend("uipath", async () =>
        result([
          item("uipath", "u2", "2026-07-02T00:00:00Z", ["j3", "j2"]),
          item("uipath", "u1", "2026-07-01T00:00:00Z", ["j2", "j1"]),
        ]),
      ),
      backend(APEX, async () => result([item(APEX, "a1", "2026-07-03T00:00:00Z")])),
    ]);
    expect(out.jobIds).toEqual({ uipath: ["j3", "j2", "j1"], [APEX]: [] });
  });

  test("one backend rejecting does not lose the other's items; it is reported, not thrown", async () => {
    const out = await findOrderAcross(ARGS, [
      backend("uipath", async () => result([item("uipath", "u1", "2026-07-01T00:00:00Z", ["j1"])])),
      backend(APEX, async () => {
        throw new Error("apex login failed");
      }),
    ]);
    expect(out.items.map((i) => i.itemId)).toEqual(["u1"]);
    expect(out.searchErrors).toEqual({ [APEX]: "apex login failed" });
    expect(out.searched).toEqual(["uipath", APEX]);
    expect(Object.keys(out.jobIds)).toEqual(["uipath"]);
    expect(Object.keys(out.backends)).toEqual(["uipath"]);
  });

  test("a searchError returned by a backend is reported and keeps that backend's partial items", async () => {
    const out = await findOrderAcross(ARGS, [
      backend(APEX, async () =>
        result([item(APEX, "a1", "2026-07-01T00:00:00Z", ["j1"])], {
          searchError: "queue 7 timed out",
        }),
      ),
    ]);
    expect(out.items.map((i) => i.itemId)).toEqual(["a1"]);
    expect(out.searchErrors).toEqual({ [APEX]: "queue 7 timed out" });
    expect(out.jobIds).toEqual({ [APEX]: ["j1"] });
  });

  test("every backend failing yields count 0 with an error per backend", async () => {
    const boom = (msg: string) => async (): Promise<FindOrderResult> => {
      throw new Error(msg);
    };
    const out = await findOrderAcross(ARGS, [
      backend("uipath", boom("uipath 503")),
      backend(APEX, boom("apex 401")),
    ]);
    expect(out.count).toBe(0);
    expect(out.searchErrors).toEqual({ uipath: "uipath 503", [APEX]: "apex 401" });
  });

  test("per-backend folder, searchMode, scanned and prefixed notes; notes/searchErrors omitted when empty", async () => {
    const out = await findOrderAcross(ARGS, [
      backend("uipath", async () =>
        result([], {
          folder: "Authorization",
          searchMode: "contains",
          scanned: 9,
          notes: ["slow"],
        }),
      ),
      backend(APEX, async () => result([], { folder: "Authorization Test", scanned: 2 })),
    ]);
    expect(out.backends).toEqual({
      uipath: { folder: "Authorization", searchMode: "contains", scanned: 9 },
      [APEX]: { folder: "Authorization Test", searchMode: "direct", scanned: 2 },
    });
    expect(out.notes).toEqual(["uipath: slow"]);
    expect(out.searchErrors).toBeUndefined();

    const quiet = await findOrderAcross(ARGS, [backend("uipath", async () => result([]))]);
    expect(quiet.notes).toBeUndefined();
  });
});

describe("selectBackends", () => {
  test("omitted orchestrator selects every registered backend", () => {
    expect(selectBackends().map((b) => b.name)).toEqual(searchableBackends().map((b) => b.name));
  });

  test("an explicit orchestrator selects only that backend, with or without a folder", () => {
    expect(selectBackends("uipath").map((b) => b.name)).toEqual(["uipath"]);
    expect(selectBackends("uipath", "Authorization").map((b) => b.name)).toEqual(["uipath"]);
  });

  test("a folder without an orchestrator is refused as an expected, user-actionable error", () => {
    expect(() => selectBackends(undefined, "Authorization")).toThrow(ExpectedError);
    expect(() => selectBackends(undefined, "Authorization")).toThrow(/orchestrator/);
  });
});

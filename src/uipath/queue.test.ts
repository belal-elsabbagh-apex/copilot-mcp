import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { resetConfigCache } from "../config/config.js";
import { findOrderQueueItems } from "./queue.js";

// ---- config fixture (mirrors uipath/actions.test.ts / copilot/analyze.test.ts) --------

const envCreds = (name: string) => ({
  be: `https://be.${name}.example.com`,
  email: `${name}@example.com`,
  password: "pw",
});

const FIXTURE = {
  copilot: { prod: envCreds("prod"), pre_prod: envCreds("preprod") },
  uipath: {
    orchestratorUrl: "https://cloud.uipath.com/myorg/mytenant/orchestrator_",
    bearer: "test-bearer",
  },
};

let dir: string;
let prevConfig: string | undefined;

const writeConfig = (config: unknown): void => {
  writeFileSync(join(dir, "config.json"), JSON.stringify(config));
  resetConfigCache();
};

beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), "copilot-mcp-queue-"));
  prevConfig = process.env["COPILOT_MCP_CONFIG"];
  process.env["COPILOT_MCP_CONFIG"] = join(dir, "config.json");
  writeConfig(FIXTURE);
});

afterAll(() => {
  if (prevConfig === undefined) delete process.env["COPILOT_MCP_CONFIG"];
  else process.env["COPILOT_MCP_CONFIG"] = prevConfig;
  resetConfigCache();
  rmSync(dir, { recursive: true, force: true });
});

// ---- fetch stub: routes by endpoint substring, one FIFO queue per endpoint -

interface RecordedCall {
  method: string;
  url: string;
}

let calls: RecordedCall[];
const realFetch = globalThis.fetch;

const json = (obj: unknown, status = 200): Response =>
  new Response(JSON.stringify(obj), { status });

// Non-null indexed access without `!` — throws with a clear message if the test's
// own setup produced fewer entries than expected.
function nth<T>(arr: T[], i: number): T {
  const v = arr[i];
  if (v === undefined) throw new Error(`expected an element at index ${i}`);
  return v;
}

// Each key is a URL substring (e.g. "/odata/QueueItems"); its queue is popped
// FIFO per matching request. Unmatched requests get an empty OData page — this
// lets each test only stock the endpoints it cares about.
const makeRouter = (queues: Record<string, Array<Response | Promise<Response>>>): void => {
  globalThis.fetch = (async (input: string | URL) => {
    const url = String(input);
    calls.push({ method: "GET", url });
    const key = Object.keys(queues).find((k) => url.includes(k));
    if (!key) return json({ value: [] });
    return queues[key]?.shift() ?? json({ value: [] });
  }) as typeof fetch;
};

beforeEach(() => {
  calls = [];
});

afterEach(() => {
  globalThis.fetch = realFetch;
  writeConfig(FIXTURE); // undo per-test config overrides
});

// ---- fixture rows ------------------------------------------------------------

const ORDER_UID = "test-order-uid-1";

// Exactly the fields uipath.ts's QUEUE_ITEM_SELECT projects for — the tier-1 query's
// $select value under test.
const QUEUE_ITEM_SELECT_FIELDS =
  "Id,Reference,Status,ExecutorJobKey,ProcessingExceptionType,RetryNumber,CreationTime,SpecificContent,QueueDefinitionId";

const queueItemRaw = (overrides: Record<string, unknown> = {}): Record<string, unknown> => ({
  Id: 1,
  Reference: "REF-1",
  Status: "New",
  ExecutorJobKey: "",
  ProcessingExceptionType: "",
  RetryNumber: 0,
  CreationTime: "2026-08-26T00:00:00.000Z",
  SpecificContent: { orderUid: ORDER_UID, token: "secret-jwt" },
  QueueDefinitionId: 7,
  ...overrides,
});

// ---- tests --------------------------------------------------------------

describe("findOrderQueueItems", () => {
  test("tier-1 query is time-bounded ($filter) and projected ($select)", async () => {
    makeRouter({ "/odata/QueueItems": [json({ value: [queueItemRaw()] })] });

    await findOrderQueueItems({ orderUid: ORDER_UID, env: "prod", since: "2026-08-25" });

    const queueItemCalls = calls.filter((c) => c.url.includes("/odata/QueueItems"));
    expect(queueItemCalls.length).toBe(1);
    const params = new URL(nth(queueItemCalls, 0).url).searchParams;
    expect(params.get("$select")).toBe(QUEUE_ITEM_SELECT_FIELDS);
    const filter = params.get("$filter") ?? "";
    expect(filter).toContain("contains(SpecificData,");
    expect(filter).toContain("CreationTime gt 2026-08-25T00:00:00.000Z");
  });

  test("since-bounded search retries unbounded when nothing matches", async () => {
    makeRouter({ "/odata/QueueItems": [json({ value: [] }), json({ value: [] })] });

    const result = await findOrderQueueItems({
      orderUid: ORDER_UID,
      env: "prod",
      since: "2026-08-25",
    });

    const queueItemCalls = calls.filter((c) => c.url.includes("/odata/QueueItems"));
    expect(queueItemCalls.length).toBe(2);
    const filter2 = new URL(nth(queueItemCalls, 1).url).searchParams.get("$filter") ?? "";
    expect(filter2).not.toContain("CreationTime");
    expect(result.notes?.some((n) => n.includes("retried without the time bound"))).toBe(true);
  });

  test("jobKeys dedupes across matches and ignores empty keys", async () => {
    makeRouter({
      "/odata/QueueItems": [
        json({
          value: [
            queueItemRaw({ Id: 1, ExecutorJobKey: "k1" }),
            queueItemRaw({ Id: 2, ExecutorJobKey: "k1" }),
            queueItemRaw({ Id: 3, Status: "New", ExecutorJobKey: "" }),
          ],
        }),
      ],
    });

    const result = await findOrderQueueItems({ orderUid: ORDER_UID, env: "prod" });

    expect(result.jobKeys).toEqual(["k1"]);
    expect(result.count).toBe(3);
  });

  test("returns the full specificContent payload with token redacted", async () => {
    makeRouter({
      "/odata/QueueItems": [
        json({
          value: [
            queueItemRaw({
              ExecutorJobKey: "k1",
              SpecificContent: { orderUid: ORDER_UID, MemberID: "m1", token: "jwt.secret" },
            }),
          ],
        }),
      ],
    });

    const result = await findOrderQueueItems({ orderUid: ORDER_UID, env: "prod" });

    expect(result.count).toBe(1);
    expect(result.jobKeys).toEqual(["k1"]);
    expect(result.items[0]?.specificContent["MemberID"]).toBe("m1");
    expect(result.items[0]?.specificContent["token"]).toBe("[redacted]");
    expect(JSON.stringify(result)).not.toContain("jwt.secret");
  });

  test("includeSpecificContent: false empties specificContent per item", async () => {
    makeRouter({
      "/odata/QueueItems": [
        json({
          value: [
            queueItemRaw({
              SpecificContent: { orderUid: ORDER_UID, MemberID: "m1", token: "jwt.secret" },
            }),
          ],
        }),
      ],
    });

    const result = await findOrderQueueItems({
      orderUid: ORDER_UID,
      env: "prod",
      includeSpecificContent: false,
    });

    expect(result.items[0]?.specificContent).toEqual({});
  });
});

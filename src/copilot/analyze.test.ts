import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { resetConfigCache } from "../config/config.js";
import { analyzeOrderExecution } from "./analyze.js";

// ---- config fixture (mirrors uipath/actions.test.ts) -----------------------

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
  dir = mkdtempSync(join(tmpdir(), "copilot-mcp-analyze-"));
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

// Resolved the instant a recorded call's URL matches `substring` — lets a test
// await the actual fetch dispatch instead of guessing a delay.
let callWaiters: Array<{ substring: string; resolve: () => void }>;

const waitForCall = (substring: string): Promise<void> => {
  const { promise, resolve } = Promise.withResolvers<void>();
  callWaiters.push({ substring, resolve });
  return promise;
};

// Each key is a URL substring (e.g. "/odata/QueueItems"); its queue is popped
// FIFO per matching request. Unmatched requests get an empty OData page — this
// lets each test only stock the endpoints it cares about.
const makeRouter = (queues: Record<string, Array<Response | Promise<Response>>>): void => {
  globalThis.fetch = (async (input: string | URL) => {
    const url = String(input);
    calls.push({ method: "GET", url });
    callWaiters = callWaiters.filter((w) => {
      if (!url.includes(w.substring)) return true;
      w.resolve();
      return false;
    });
    const key = Object.keys(queues).find((k) => url.includes(k));
    if (!key) return json({ value: [] });
    return queues[key]?.shift() ?? json({ value: [] });
  }) as typeof fetch;
};

beforeEach(() => {
  calls = [];
  callWaiters = [];
});

afterEach(() => {
  globalThis.fetch = realFetch;
  writeConfig(FIXTURE); // undo per-test config overrides
});

// ---- fixture rows ------------------------------------------------------------

const ORDER_UID = "test-order-uid-1";
const JOB_KEY = "11111111-1111-1111-1111-111111111111";

// Exactly the fields uipath.ts's QUEUE_ITEM_SELECT projects for — the tier-1 query's
// $select value under test.
const QUEUE_ITEM_SELECT_FIELDS =
  "Id,Reference,Status,ExecutorJobKey,ProcessingExceptionType,RetryNumber,CreationTime,SpecificContent";

const queueItemRaw = (overrides: Record<string, unknown> = {}): Record<string, unknown> => ({
  Id: 1,
  Reference: "REF-1",
  Status: "New",
  ExecutorJobKey: "",
  ProcessingExceptionType: "",
  RetryNumber: 0,
  CreationTime: "2026-08-26T00:00:00.000Z",
  SpecificContent: { orderUid: ORDER_UID, token: "secret-jwt" },
  ...overrides,
});

const jobRaw = (key: string, overrides: Record<string, unknown> = {}): Record<string, unknown> => ({
  Id: "42",
  Key: key,
  State: "Successful",
  ReleaseName: "MyProcess",
  CreationTime: "2026-08-26T00:01:00.000Z",
  EndTime: "2026-08-26T00:02:00.000Z",
  OutputArguments: "{}",
  ...overrides,
});

// ---- tests --------------------------------------------------------------

describe("analyzeOrderExecution", () => {
  test("primary QueueItems search is projected ($select) and time-bounded ($filter)", async () => {
    makeRouter({ "/odata/QueueItems": [json({ value: [queueItemRaw()] })] });

    await analyzeOrderExecution(ORDER_UID, { env: "prod", since: "2026-08-25", includeLogs: true });

    const queueItemCalls = calls.filter((c) => c.url.includes("/odata/QueueItems"));
    expect(queueItemCalls.length).toBe(1);
    const params = new URL(nth(queueItemCalls, 0).url).searchParams;
    expect(params.get("$select")).toBe(QUEUE_ITEM_SELECT_FIELDS);
    const filter = params.get("$filter") ?? "";
    expect(filter).toContain("contains(SpecificData,");
    expect(filter).toContain("CreationTime gt 2026-08-25T00:00:00.000Z");
  });

  test("job detail, logs and video go out in one wave, not three serial legs", async () => {
    const jobsResponse = Promise.withResolvers<Response>();

    makeRouter({
      "/odata/QueueItems": [json({ value: [queueItemRaw({ ExecutorJobKey: JOB_KEY })] })],
      "/odata/Jobs": [jobsResponse.promise],
      "/odata/RobotLogs": [json({ value: [] })],
    });

    const resultPromise = analyzeOrderExecution(ORDER_UID, { env: "prod", includeLogs: true });

    // The logs leg (RobotLogs) is issued before the still-pending Jobs response
    // resolves — proof the two legs went out together, not detail-then-logs.
    await waitForCall("/odata/RobotLogs");
    expect(calls.some((c) => c.url.includes("/odata/Jobs"))).toBe(true);

    let settled = false;
    resultPromise.then(() => {
      settled = true;
    });
    // No wait needed: resultPromise structurally cannot settle until the Jobs
    // response (still unresolved) does — Promise.allSettled awaits every leg.
    expect(settled).toBe(false);

    jobsResponse.resolve(json({ value: [jobRaw(JOB_KEY)] }));

    const result = await resultPromise;
    expect(result.jobCount).toBe(1);
  });

  test("$select degradation retries with full rows and confirms the match", async () => {
    makeRouter({
      "/odata/QueueItems": [
        json({ value: [queueItemRaw({ SpecificContent: undefined })] }), // no readable SpecificContent
        json({ value: [queueItemRaw({ ExecutorJobKey: JOB_KEY })] }), // full-row retry, confirmed
      ],
      "/odata/Jobs": [json({ value: [jobRaw(JOB_KEY)] })],
    });

    const result = await analyzeOrderExecution(ORDER_UID, {
      env: "prod",
      includeLogs: false,
      includeVideo: false,
    });

    const queueItemCalls = calls.filter((c) => c.url.includes("/odata/QueueItems"));
    expect(queueItemCalls.length).toBe(2);
    const params1 = new URL(nth(queueItemCalls, 0).url).searchParams;
    const params2 = new URL(nth(queueItemCalls, 1).url).searchParams;
    expect(params1.get("$filter")).toBe(params2.get("$filter"));
    expect(params1.has("$select")).toBe(true);
    expect(params2.has("$select")).toBe(false);
    expect(result.matched).toBe(true);
    expect(result.notes?.some((n) => n.includes("re-queried with full rows"))).toBe(true);
  });

  test("since-bounded search retries unbounded when nothing matches", async () => {
    makeRouter({ "/odata/QueueItems": [json({ value: [] }), json({ value: [] })] });

    const result = await analyzeOrderExecution(ORDER_UID, {
      env: "prod",
      since: "2026-08-25",
      includeLogs: false,
    });

    const queueItemCalls = calls.filter((c) => c.url.includes("/odata/QueueItems"));
    expect(queueItemCalls.length).toBe(2);
    const filter2 = new URL(nth(queueItemCalls, 1).url).searchParams.get("$filter") ?? "";
    expect(filter2).not.toContain("CreationTime");
    expect(result.notes?.some((n) => n.includes("retried without the time bound"))).toBe(true);
  });

  test("includeVideo=false issues no VideoRecording call", async () => {
    makeRouter({
      "/odata/QueueItems": [json({ value: [queueItemRaw({ ExecutorJobKey: JOB_KEY })] })],
      "/odata/Jobs": [json({ value: [jobRaw(JOB_KEY)] })],
      "/odata/RobotLogs": [json({ value: [] })],
    });

    await analyzeOrderExecution(ORDER_UID, { env: "prod" }); // includeVideo defaults false

    expect(calls.some((c) => c.url.includes("/api/VideoRecording/"))).toBe(false);
  });
});

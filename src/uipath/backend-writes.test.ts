import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { resetConfigCache } from "../config/config.js";
import { ExpectedError } from "../shared/expected-error.js";
import {
  uipathAddQueueItem,
  uipathDeleteQueueItem,
  uipathStartJob,
  uipathStopJob,
} from "./backend.js";

const ORCH = "https://cloud.uipath.com/myorg/mytenant/orchestrator_";
const creds = (name: string) => ({
  be: `https://be.${name}.example.com`,
  email: `${name}@example.com`,
  password: "pw",
});
const FIXTURE = {
  copilot: { sessionCache: false, prod: creds("prod"), pre_prod: creds("preprod") },
  uipath: {
    orchestratorUrl: ORCH,
    bearer: "test-bearer",
    serverUrlByEnv: { pre_prod: "https://pre-prod-be.example.com" },
  },
};

let dir: string;
let prevConfig: string | undefined;

beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), "copilot-mcp-uipath-writes-"));
  prevConfig = process.env["COPILOT_MCP_CONFIG"];
  process.env["COPILOT_MCP_CONFIG"] = join(dir, "config.json");
  writeFileSync(join(dir, "config.json"), JSON.stringify(FIXTURE));
  resetConfigCache();
});

afterAll(() => {
  if (prevConfig === undefined) delete process.env["COPILOT_MCP_CONFIG"];
  else process.env["COPILOT_MCP_CONFIG"] = prevConfig;
  resetConfigCache();
  rmSync(dir, { recursive: true, force: true });
});

interface RecordedCall {
  method: string;
  url: string;
  body: unknown;
}

let calls: RecordedCall[];
let responses: Response[];
const realFetch = globalThis.fetch;

// Stub fetch: record each call, pop the next queued response (default 200 {}).
beforeEach(() => {
  calls = [];
  responses = [];
  globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
    calls.push({
      method: init?.method ?? "GET",
      url: String(input),
      body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
    });
    return responses.shift() ?? new Response("{}", { status: 200 });
  }) as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = realFetch;
});

const json = (obj: unknown, status = 200): Response =>
  new Response(JSON.stringify(obj), { status });

describe("uipathAddQueueItem", () => {
  test("posts the guarded item to the queue named by `queue` and reports ids as strings", async () => {
    responses.push(json({ Id: 42, Status: "New", QueueDefinitionId: 79926 }));
    const out = await uipathAddQueueItem({
      env: "pre_prod",
      queue: "Scratch queue",
      reference: "REF-1",
      priority: "High",
      specificContent: { orderUid: "u1", IsApproved: true },
    });
    const body = calls[0]?.body as { itemData: Record<string, unknown> };
    expect(body.itemData["Name"]).toBe("Scratch queue");
    expect(body.itemData["Priority"]).toBe("High");
    expect(out).toEqual({
      env: "pre_prod",
      queueId: "79926",
      queueName: "Scratch queue",
      reference: "REF-1",
      itemId: "42",
      status: "New",
      forced: ["IsApproved"],
    });
  });

  test("an unreported queue id is an empty string, not '0'", async () => {
    responses.push(json({ Id: 1, Status: "New" }));
    const out = await uipathAddQueueItem({
      env: "pre_prod",
      queue: "Scratch queue",
      reference: "REF-1",
      priority: "Normal",
      specificContent: { orderUid: "u1" },
    });
    expect(out.queueId).toBe("");
  });
});

describe("uipathDeleteQueueItem", () => {
  for (const itemId of ["", "abc", "0", "-3", "1.5", "12abc", "99999999999999999999"]) {
    test(`rejects the non-positive-integer id '${itemId}' before any request`, async () => {
      const err = await uipathDeleteQueueItem({ env: "pre_prod", itemId }).catch((e) => e);
      expect(err).toBeInstanceOf(ExpectedError);
      expect(calls.length).toBe(0);
    });
  }

  test("converts the string id to the numeric OData key and returns it as a string", async () => {
    responses.push(json({ Id: 9, Status: "New", Reference: "r9" }));
    responses.push(new Response("", { status: 204 }));
    const out = await uipathDeleteQueueItem({ env: "pre_prod", itemId: "9", queueId: "ignored" });
    expect(calls[1]?.url).toBe(`${ORCH}/odata/QueueItems(9)`);
    expect(out).toEqual({
      env: "pre_prod",
      itemId: "9",
      deleted: true,
      previousStatus: "New",
      reference: "r9",
    });
  });
});

describe("uipathStartJob", () => {
  test("maps processId -> ReleaseKey and input -> InputArguments, and links each job", async () => {
    responses.push(
      json({ value: [{ Id: 5, Key: "job-key-1", State: "Pending", ReleaseName: "Proc.Dev" }] }),
    );
    const out = await uipathStartJob({
      env: "pre_prod",
      processId: "609b3602-b6f2-44b2-9ee2-6a8988fac1f5",
      input: { a: 1 },
      jobsCount: 2,
      matchValue: "ignored-on-uipath",
    });
    expect(calls[0]?.body).toEqual({
      startInfo: {
        ReleaseKey: "609b3602-b6f2-44b2-9ee2-6a8988fac1f5",
        Strategy: "ModernJobsCount",
        JobsCount: 2,
        InputArguments: '{"a":1}',
      },
    });
    expect(out).toEqual({
      env: "pre_prod",
      jobs: [
        {
          id: "job-key-1",
          state: "Pending",
          processName: "Proc.Dev",
          link: `${ORCH}/jobs(sidepanel:sidepanel/jobs/job-key-1/details)`,
        },
      ],
    });
  });
});

describe("uipathStopJob", () => {
  test("resolves the job by key and returns the key as jobId", async () => {
    responses.push(json({ value: [{ Id: 5, Key: "job-key-1", State: "Running" }] }));
    responses.push(json({}));
    const out = await uipathStopJob({ env: "pre_prod", jobId: "job-key-1", strategy: "Kill" });
    expect(calls[1]?.url).toBe(`${ORCH}/odata/Jobs(5)/UiPath.Server.Configuration.OData.StopJob`);
    expect(calls[1]?.body).toEqual({ strategy: "Kill" });
    expect(out).toEqual({ env: "pre_prod", jobId: "job-key-1", strategy: "Kill", stopped: true });
  });
});

describe("pre_prod-only", () => {
  test("every write adapter refuses prod before any request", async () => {
    const attempts = [
      () =>
        uipathAddQueueItem({
          env: "prod",
          queue: "q",
          reference: "r",
          priority: "Normal",
          specificContent: {},
        }),
      () => uipathDeleteQueueItem({ env: "prod", itemId: "1" }),
      () => uipathStartJob({ env: "prod", processId: "rk", input: {}, jobsCount: 1 }),
      () => uipathStopJob({ env: "prod", jobId: "job-key-1", strategy: "SoftStop" }),
    ];
    for (const attempt of attempts) await expect(attempt()).rejects.toThrow(/pre_prod-only/);
    expect(calls.length).toBe(0);
  });
});

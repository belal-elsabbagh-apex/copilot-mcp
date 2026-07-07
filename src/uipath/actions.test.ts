import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { resetConfigCache } from "../config/config.js";
import {
  addQueueItem,
  assertDeletable,
  buildAddQueueItemBody,
  buildStartJobBody,
  deleteQueueItem,
  startJob,
} from "./actions.js";
import type { QueueItem } from "./uipath.js";

// ---- pure helpers ----------------------------------------------------------

describe("buildAddQueueItemBody", () => {
  test("wraps the guarded content in the AddQueueItem itemData envelope", () => {
    const sc = { orderUid: "u1", IsApproved: false };
    expect(buildAddQueueItemBody("My queue", "REF-1", "Normal", sc)).toEqual({
      itemData: { Name: "My queue", Priority: "Normal", Reference: "REF-1", SpecificContent: sc },
    });
  });
});

describe("buildStartJobBody", () => {
  const base = { env: "pre_prod" as const, releaseKey: "rk-guid", jobsCount: 1 };

  test("omits InputArguments when empty", () => {
    expect(buildStartJobBody({ ...base, inputArguments: {} })).toEqual({
      startInfo: { ReleaseKey: "rk-guid", Strategy: "ModernJobsCount", JobsCount: 1 },
    });
  });

  test("JSON-stringifies non-empty InputArguments", () => {
    expect(buildStartJobBody({ ...base, jobsCount: 2, inputArguments: { a: 1 } })).toEqual({
      startInfo: {
        ReleaseKey: "rk-guid",
        Strategy: "ModernJobsCount",
        JobsCount: 2,
        InputArguments: '{"a":1}',
      },
    });
  });
});

describe("assertDeletable", () => {
  const item = (status: string): QueueItem => ({
    id: 7,
    status,
    reference: "r",
    creationTime: "",
    retryNumber: 0,
    queueDefinitionId: 0,
    name: "",
    specificContent: {},
  });

  test("passes a New item", () => {
    expect(() => assertDeletable(item("New"))).not.toThrow();
  });

  for (const status of ["InProgress", "Successful", "Failed", "Deleted", "Abandoned"]) {
    test(`refuses a ${status} item`, () => {
      expect(() => assertDeletable(item(status))).toThrow(/only 'New' items may be deleted/);
    });
  }
});

// ---- HTTP wiring (config fixture + fetch stub) ------------------------------

const envCreds = (name: string) => ({
  be: `https://be.${name}.example.com`,
  email: `${name}@example.com`,
  password: "pw",
});

const UIPATH = {
  orchestratorUrl: "https://cloud.uipath.com/myorg/mytenant/orchestrator_",
  bearer: "test-bearer",
  serverUrlByEnv: { pre_prod: "https://pre-prod-be.example.com" },
};

const FIXTURE = {
  copilot: { prod: envCreds("prod"), pre_prod: envCreds("preprod") },
  uipath: UIPATH,
};

let dir: string;
let prevConfig: string | undefined;

const writeConfig = (config: unknown): void => {
  writeFileSync(join(dir, "config.json"), JSON.stringify(config));
  resetConfigCache();
};

beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), "copilot-mcp-actions-"));
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

interface RecordedCall {
  method: string;
  url: string;
  headers: Record<string, string>;
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
    const headers: Record<string, string> = {};
    for (const [k, v] of new Headers(init?.headers).entries()) headers[k] = v;
    calls.push({
      method: init?.method ?? "GET",
      url: String(input),
      headers,
      body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
    });
    return responses.shift() ?? new Response("{}", { status: 200 });
  }) as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = realFetch;
  writeConfig(FIXTURE); // undo per-test config overrides
});

const json = (obj: unknown, status = 200): Response =>
  new Response(JSON.stringify(obj), { status });

describe("addQueueItem", () => {
  const args = {
    env: "pre_prod" as const,
    queueName: "Scratch queue",
    reference: "REF-1",
    priority: "Normal" as const,
    specificContent: { orderUid: "u1", IsApproved: true },
  };

  test("rejects env=prod before any fetch", async () => {
    await expect(addQueueItem({ ...args, env: "prod" })).rejects.toThrow(/pre_prod-only/);
    expect(calls.length).toBe(0);
  });

  test("guard violations block the POST", async () => {
    await expect(
      addQueueItem({ ...args, specificContent: { orderUid: "<TO-FILL>" } }),
    ).rejects.toThrow(/placeholder/);
    expect(calls.length).toBe(0);
  });

  test("POSTs the guarded payload to the default AddQueueItem path with auth + folder headers", async () => {
    responses.push(json({ Id: 42, Status: "New" }));
    const out = await addQueueItem(args);
    expect(calls.length).toBe(1);
    const call = calls[0];
    expect(call?.method).toBe("POST");
    expect(call?.url).toBe(`${UIPATH.orchestratorUrl}/odata/Queues/UiPathODataSvc.AddQueueItem`);
    expect(call?.headers["authorization"]).toBe("Bearer test-bearer");
    expect(call?.headers["content-type"]).toBe("application/json");
    expect(call?.headers["x-uipath-organizationunitid"]).toBe("434039");
    expect(call?.headers["x-uipath-folderpath"]).toBe("Authorization Dev Clone");
    const body = call?.body as { itemData: Record<string, unknown> };
    expect(body.itemData["Name"]).toBe("Scratch queue");
    expect((body.itemData["SpecificContent"] as Record<string, unknown>)["IsApproved"]).toBe(false);
    expect(out).toEqual({
      env: "pre_prod",
      queueName: "Scratch queue",
      reference: "REF-1",
      itemId: 42,
      status: "New",
      forced: ["IsApproved"],
    });
  });

  test("honors a configured addQueueItemPath", async () => {
    writeConfig({ ...FIXTURE, uipath: { ...UIPATH, addQueueItemPath: "/odata/custom" } });
    responses.push(json({ Id: 1, Status: "New" }));
    await addQueueItem(args);
    expect(calls[0]?.url).toBe(`${UIPATH.orchestratorUrl}/odata/custom`);
  });

  test("surfaces a non-2xx as UiPath POST error", async () => {
    responses.push(json({ message: "queue not found" }, 404));
    await expect(addQueueItem(args)).rejects.toThrow(/UiPath POST .* -> 404/);
  });
});

describe("deleteQueueItem", () => {
  test("rejects env=prod before any fetch", async () => {
    await expect(deleteQueueItem(1, "prod")).rejects.toThrow(/pre_prod-only/);
    expect(calls.length).toBe(0);
  });

  test("fetches first and refuses a non-New item without issuing a DELETE", async () => {
    responses.push(json({ Id: 9, Status: "InProgress", Reference: "r9" }));
    await expect(deleteQueueItem(9, "pre_prod")).rejects.toThrow(/status is 'InProgress'/);
    expect(calls.length).toBe(1);
    expect(calls[0]?.method).toBe("GET");
  });

  test("DELETEs a New item and tolerates an empty 204 body", async () => {
    responses.push(json({ Id: 9, Status: "New", Reference: "r9" }));
    responses.push(new Response("", { status: 204 }));
    const out = await deleteQueueItem(9, "pre_prod");
    expect(calls.length).toBe(2);
    expect(calls[1]?.method).toBe("DELETE");
    expect(calls[1]?.url).toBe(`${UIPATH.orchestratorUrl}/odata/QueueItems(9)`);
    expect(out).toEqual({
      env: "pre_prod",
      itemId: 9,
      deleted: true,
      previousStatus: "New",
      reference: "r9",
    });
  });
});

describe("startJob", () => {
  const args = {
    env: "pre_prod" as const,
    releaseKey: "609b3602-b6f2-44b2-9ee2-6a8988fac1f5",
    inputArguments: {},
    jobsCount: 1,
  };

  test("rejects env=prod before any fetch", async () => {
    await expect(startJob({ ...args, env: "prod" })).rejects.toThrow(/pre_prod-only/);
    expect(calls.length).toBe(0);
  });

  test("POSTs StartJobs and maps the started jobs", async () => {
    responses.push(
      json({ value: [{ Id: 5, Key: "job-key-1", State: "Pending", ReleaseName: "Proc.Dev" }] }),
    );
    const out = await startJob(args);
    expect(calls[0]?.method).toBe("POST");
    expect(calls[0]?.url).toBe(
      `${UIPATH.orchestratorUrl}/odata/Jobs/UiPath.Server.Configuration.OData.StartJobs`,
    );
    expect(out.jobs).toEqual([
      {
        id: 5,
        key: "job-key-1",
        state: "Pending",
        releaseName: "Proc.Dev",
        deepLink:
          "https://cloud.uipath.com/myorg/mytenant/orchestrator_/jobs(sidepanel:sidepanel/jobs/job-key-1/details)",
      },
    ]);
  });
});

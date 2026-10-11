import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { resetConfigCache } from "../config/config.js";

const envCreds = (name: string) => ({
  be: `https://be.${name}.example.com`,
  email: `${name}@example.com`,
  password: "pw",
});

const FIXTURE = {
  copilot: { sessionCache: false, prod: envCreds("prod"), pre_prod: envCreds("preprod") },
  uipath: {
    orchestratorUrl: "https://cloud.uipath.com/myorg/mytenant/orchestrator_",
    bearer: "test-bearer",
  },
};

let dir: string;
let prevConfig: string | undefined;

beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), "copilot-mcp-backend-"));
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

// One handler per test decides each request's response from its URL.
let handler: (url: URL) => Response;
let calls: URL[];
const realFetch = globalThis.fetch;

beforeEach(() => {
  calls = [];
  handler = () => json({ value: [] });
  globalThis.fetch = (async (input: string | URL | Request) => {
    const url = new URL(String(input));
    calls.push(url);
    return handler(url);
  }) as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = realFetch;
});

const json = (obj: unknown, status = 200): Response =>
  new Response(JSON.stringify(obj), { status });

import {
  uipathBuildFaultedJobIssue,
  uipathGetJobLogs,
  uipathGetJobs,
  uipathListJobs,
} from "./backend.js";

const KEY = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee";
const KEY_2 = "bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee";
const KEY_3 = "cccccccc-bbbb-cccc-dddd-eeeeeeeeeeee";

// The job key a /odata/Jobs or /odata/RobotLogs request is filtered on.
const keyOf = (url: URL): string =>
  url.searchParams.get("$filter")?.match(/(?:Job)?Key eq (\S+)/)?.[1] ?? "";

const jobRow = (key: string, overrides: Record<string, unknown> = {}): Record<string, unknown> => ({
  Id: 1,
  Key: key,
  State: "Faulted",
  ReleaseName: "PMG auth submit",
  CreationTime: "2026-07-01T10:00:00Z",
  EndTime: "2026-07-01T10:01:00Z",
  ...overrides,
});

// Jobs by key (missing = no rows), logs by key (missing = no rows); a value of "error" = HTTP 500.
const serve = (
  jobs: Record<string, Record<string, unknown> | "error">,
  logs: Record<string, unknown[] | "error"> = {},
): void => {
  handler = (url) => {
    const key = keyOf(url);
    if (url.pathname.endsWith("/odata/Jobs")) {
      const row = jobs[key];
      if (row === "error") return json({ message: "boom" }, 500);
      return json({ value: row ? [row] : [] });
    }
    if (url.pathname.endsWith("/odata/RobotLogs")) {
      const rows = logs[key];
      if (rows === "error") return json({ message: "boom" }, 500);
      return json({ value: rows ?? [] });
    }
    return json({ value: [] });
  };
};

const logRow = (Level: string, Message: string, extra: Record<string, unknown> = {}) => ({
  Level,
  Message,
  TimeStamp: "2026-07-01T10:00:30Z",
  ...extra,
});

describe("uipathListJobs", () => {
  test("maps rows to JobCores with a per-job link and the resolved folder", async () => {
    handler = () => json({ value: [jobRow(KEY, { ProcessVersion: "1.2.3" })] });
    const res = await uipathListJobs({ env: "prod", top: 5 });
    expect(res.folder).toBe("Authorization");
    expect(res.jobs).toHaveLength(1);
    expect(res.jobs[0]).toMatchObject({
      orchestrator: "uipath",
      id: KEY,
      state: "Faulted",
      terminal: true,
      durationMs: 60_000,
      process: { name: "PMG auth submit", version: "1.2.3" },
    });
    expect(res.jobs[0]?.link).toContain(KEY);
  });

  test("an explicit folder wins over the env default", async () => {
    const res = await uipathListJobs({ env: "prod", top: 5, folder: "Some Folder" });
    expect(res.folder).toBe("Some Folder");
  });
});

describe("uipathGetJobs", () => {
  const base = { env: "prod" as const, includeLogDigest: false };

  test("a single id whose lookup fails fails the call", async () => {
    serve({ [KEY]: "error" });
    await expect(uipathGetJobs({ ...base, jobIds: [KEY], batch: false })).rejects.toThrow(/500/);
  });

  test("a batch keeps going: the failing id carries error, a missing id is found:false", async () => {
    serve({ [KEY]: jobRow(KEY), [KEY_2]: "error" });
    const res = await uipathGetJobs({ ...base, jobIds: [KEY, KEY_2, KEY_3], batch: true });
    expect(res.entries.map((e) => [e.id, e.found])).toEqual([
      [KEY, true],
      [KEY_2, false],
      [KEY_3, false],
    ]);
    expect(res.entries[0]?.job?.id).toBe(KEY);
    expect(res.entries[1]?.error).toMatch(/500/);
    expect(res.entries[2]).toEqual({ id: KEY_3, found: false });
  });

  test("a single id that resolves to nothing is found:false, not an error", async () => {
    serve({});
    const res = await uipathGetJobs({ ...base, jobIds: [KEY], batch: false });
    expect(res.entries).toEqual([{ id: KEY, found: false }]);
  });

  test("without includeLogDigest no logs are fetched and fault stays null", async () => {
    serve({ [KEY]: jobRow(KEY) });
    const res = await uipathGetJobs({ ...base, jobIds: [KEY], batch: false });
    expect(res.entries[0]?.job?.fault).toBeNull();
    expect(res.entries[0]).not.toHaveProperty("logDigest");
    expect(calls.some((u) => u.pathname.endsWith("/odata/RobotLogs"))).toBe(false);
  });

  test("includeLogDigest: fault from the logs, digest with the queue transaction outcome", async () => {
    const ended = JSON.stringify({
      transactionState: "Ended",
      processingExceptionType: "BusinessException",
      processingExceptionReason: "missing member id",
    });
    serve(
      { [KEY]: jobRow(KEY) },
      {
        [KEY]: [
          logRow("Error", "System.InvalidOperationException: submit rejected"),
          logRow("Info", "Transaction Ended", { RawMessage: ended }),
        ],
      },
    );
    const res = await uipathGetJobs({
      ...base,
      includeLogDigest: true,
      jobIds: [KEY],
      batch: false,
    });
    const entry = res.entries[0];
    expect(entry?.job?.fault).toEqual({
      message: "System.InvalidOperationException: submit rejected",
      signature: "system.invalidoperationexception: submit rejected",
      class: "System.InvalidOperationException",
    });
    expect(entry?.logDigest).toMatchObject({
      fetched: 2,
      transactionOutcome: {
        processingExceptionType: "BusinessException",
        processingExceptionReason: "missing member id",
      },
    });
    const logsCall = calls.find((u) => u.pathname.endsWith("/odata/RobotLogs"));
    expect(logsCall?.searchParams.get("$select")).toContain("RawMessage");
  });

  test("a Successful job still gets a digest but no fault", async () => {
    serve({ [KEY]: jobRow(KEY, { State: "Successful" }) }, { [KEY]: [logRow("Info", "done")] });
    const res = await uipathGetJobs({
      ...base,
      includeLogDigest: true,
      jobIds: [KEY],
      batch: false,
    });
    expect(res.entries[0]?.job?.fault).toBeNull();
    expect(res.entries[0]?.logDigest?.fetched).toBe(1);
  });

  test("a logs failure degrades to logDigestError; the job is still returned, even for a single id", async () => {
    serve({ [KEY]: jobRow(KEY) }, { [KEY]: "error" });
    const res = await uipathGetJobs({
      ...base,
      includeLogDigest: true,
      jobIds: [KEY],
      batch: false,
    });
    const entry = res.entries[0];
    expect(entry?.found).toBe(true);
    expect(entry?.job?.id).toBe(KEY);
    expect(entry?.job?.fault).toBeNull();
    expect(entry?.logDigestError).toMatch(/500/);
    expect(entry).not.toHaveProperty("logDigest");
  });

  test("details.output: parsed OutputArguments without the token; null when absent", async () => {
    serve({
      [KEY]: jobRow(KEY, {
        OutputArguments: JSON.stringify({ out_status: "ok", token: "jwt.secret" }),
      }),
      [KEY_2]: jobRow(KEY_2),
    });
    const res = await uipathGetJobs({ ...base, jobIds: [KEY, KEY_2], batch: true });
    const withOutput = res.entries[0]?.job?.details as { output?: unknown };
    expect(JSON.stringify(withOutput.output)).toContain("ok");
    expect(JSON.stringify(res)).not.toContain("jwt.secret");
    expect((res.entries[1]?.job?.details as { output?: unknown }).output).toBeNull();
  });

  test("progress counts every id off, in a batch", async () => {
    serve({ [KEY]: jobRow(KEY), [KEY_2]: jobRow(KEY_2), [KEY_3]: jobRow(KEY_3) });
    const seen: Array<[number, number]> = [];
    await uipathGetJobs({ ...base, jobIds: [KEY, KEY_2, KEY_3], batch: true }, (done, total) =>
      seen.push([done, total]),
    );
    expect(seen).toEqual([
      [1, 3],
      [2, 3],
      [3, 3],
    ]);
  });
});

describe("uipathGetJobLogs", () => {
  const base = {
    env: "prod" as const,
    filter: { onlyFailures: false },
    fullMessages: false,
    includeRawFields: false,
    includeVideo: false,
  };

  test("a single id whose logs fail fails the call; a batch puts it on that entry", async () => {
    serve({}, { [KEY]: "error", [KEY_2]: [logRow("Info", "ok")] });
    await expect(uipathGetJobLogs({ ...base, jobIds: [KEY], batch: false })).rejects.toThrow(/500/);
    const res = await uipathGetJobLogs({ ...base, jobIds: [KEY, KEY_2], batch: true });
    expect(res.entries[0]?.error).toMatch(/500/);
    expect(res.entries[0]?.logs).toEqual([]);
    expect(res.entries[1]).toMatchObject({ jobId: KEY_2, returned: 1, truncated: false });
  });

  test("messages truncate unless fullMessages; RawMessage never leaks, raw parses only on request", async () => {
    const long = "x".repeat(500);
    const raw = JSON.stringify({ transactionState: "Ended", queueName: "PMG" });
    serve({}, { [KEY]: [logRow("Info", long, { RawMessage: raw })] });

    const plain = await uipathGetJobLogs({ ...base, jobIds: [KEY], batch: false });
    const row = plain.entries[0]?.logs[0];
    expect(row?.Message).toBe(`${"x".repeat(400)}…`);
    expect(row).not.toHaveProperty("RawMessage");
    expect(row).not.toHaveProperty("raw");

    const rich = await uipathGetJobLogs({
      ...base,
      jobIds: [KEY],
      batch: false,
      fullMessages: true,
      includeRawFields: true,
    });
    const richRow = rich.entries[0]?.logs[0];
    expect(richRow?.Message).toBe(long);
    expect(richRow).not.toHaveProperty("RawMessage");
    expect(richRow?.raw).toMatchObject({ transactionState: "Ended", queueName: "PMG" });
  });

  test("totalMatching above the 500-row window marks the entry truncated", async () => {
    handler = () => json({ value: [logRow("Info", "a")], "@odata.count": 600 });
    const res = await uipathGetJobLogs({ ...base, jobIds: [KEY], batch: false });
    expect(res.entries[0]).toMatchObject({ totalMatching: 600, truncated: true });
  });

  test("includeVideo: a recording becomes a url VideoRef; none means no video key", async () => {
    const withVideo = "https://cdn.example.com/jobs/recording.webm";
    handler = (url) =>
      url.pathname.includes("/VideoRecording/")
        ? json(url.pathname.includes(KEY) ? [{ uri: withVideo }] : [])
        : json({ value: [] });
    const res = await uipathGetJobLogs({
      ...base,
      includeVideo: true,
      jobIds: [KEY, KEY_2],
      batch: true,
    });
    expect(res.entries[0]?.video).toEqual({ kind: "url", url: withVideo });
    expect(res.entries[1]).not.toHaveProperty("video");
    expect(res.entries[1]).not.toHaveProperty("videoError");
  });

  test("a video failure is videoError and never fails the logs", async () => {
    handler = (url) =>
      url.pathname.includes("/VideoRecording/")
        ? json({ message: "boom" }, 500)
        : json({ value: [logRow("Info", "a")] });
    const res = await uipathGetJobLogs({
      ...base,
      includeVideo: true,
      jobIds: [KEY],
      batch: false,
    });
    expect(res.entries[0]?.returned).toBe(1);
    expect(res.entries[0]?.videoError).toMatch(/500/);
  });
});

describe("uipathBuildFaultedJobIssue", () => {
  test("builds the payload from the job and its logs, honoring repo/labels", async () => {
    serve({ [KEY]: jobRow(KEY) }, { [KEY]: [logRow("Error", "Selector not found")] });
    const res = await uipathBuildFaultedJobIssue({
      env: "prod",
      jobId: KEY,
      repo: "acme/repo",
      labels: ["rpa"],
    });
    expect(res.found).toBe(true);
    expect(res.repo).toBe("acme/repo");
    expect(res.labels).toEqual(["rpa"]);
    expect(res.faultSignature).toBe("selector not found");
    expect(res.searchQuery).toContain("repo:acme/repo");
  });

  test("an unknown job still yields a stub payload with found:false", async () => {
    serve({});
    const res = await uipathBuildFaultedJobIssue({ env: "prod", jobId: KEY });
    expect(res.found).toBe(false);
    expect(res.faultSignature).toBe("job ended in state faulted");
  });
});

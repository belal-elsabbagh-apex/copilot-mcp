import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { resetConfigCache } from "../config/config.js";
import { DEFAULT_REPO } from "../orchestrator/faults.js";
import type { JobLog } from "../orchestrator/types.js";
import {
  buildFaultedJobIssue,
  DEFAULT_LABELS,
  extractFault,
  extractTransactionOutcome,
  formatFaultedJobIssue,
} from "./faults.js";
import type { UiPathJob, UiPathLogRow } from "./uipath.js";

const job: UiPathJob = {
  Id: "12345",
  Key: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
  State: "Faulted",
  CreationTime: "2026-06-30T10:00:00Z",
  EndTime: "2026-06-30T10:01:00Z",
};

const errLog = (msg: string): JobLog => ({
  Level: "Error",
  Message: msg,
  TimeStamp: "2026-06-30T10:00:30Z",
});

describe("extractFault", () => {
  const faultJob: UiPathJob = { Key: "k", State: "Faulted" };

  test("parses the exception type and computes a stable signature", () => {
    const logs = [
      errLog(
        "System.NullReferenceException: Object reference not set. Job 7bfea00c-1111-2222-3333-444444444444",
      ),
    ];
    const fault = extractFault(faultJob, logs);
    expect(fault.exceptionType).toBe("System.NullReferenceException");
    expect(fault.signature).toContain("<id>");
    expect(fault.signature).not.toContain("7bfea00c");
  });

  test("two runs of the same fault share a signature despite different ids", () => {
    const a = extractFault(faultJob, [
      errLog("Timeout on order 7bfea00c-1111-2222-3333-444444444444"),
    ]);
    const b = extractFault(faultJob, [
      errLog("Timeout on order 508debf6-9999-aaaa-bbbb-cccccccccccc"),
    ]);
    expect(a.signature).toBe(b.signature);
  });

  test("falls back to a synthetic message when there are no logs", () => {
    const fault = extractFault(faultJob, []);
    expect(fault.message).toBe("Job ended in state Faulted");
    expect(fault.exceptionType).toBeNull();
  });
});

describe("extractTransactionOutcome", () => {
  const row = (Message: string, TimeStamp: string, RawMessage?: string): UiPathLogRow => ({
    Level: "Info",
    Message,
    TimeStamp,
    ...(RawMessage !== undefined ? { RawMessage } : {}),
  });

  test("reads the queue's own exception classification off the Transaction Ended row", () => {
    const ended = JSON.stringify({
      transactionState: "Ended",
      transactionId: "d211b063-20eb-45a9-801a-5634cd13568a",
      queueName: "CenCal auth sync queue",
      processingExceptionType: "BusinessException",
      processingExceptionReason: "",
      transactionExecutionTime: 49.93,
      queueItemPriority: "Normal",
      queueItemReviewStatus: "None",
    });
    const logs = [
      row("Transaction Started", "2026-07-01T10:00:00Z"),
      row("Transaction Ended", "2026-07-01T10:00:05Z", ended),
    ];
    expect(extractTransactionOutcome(logs)).toEqual({
      transactionId: "d211b063-20eb-45a9-801a-5634cd13568a",
      queueName: "CenCal auth sync queue",
      processingExceptionType: "BusinessException",
      processingExceptionReason: null,
      transactionExecutionTimeSec: 49.93,
      queueItemPriority: "Normal",
      queueItemReviewStatus: "None",
    });
  });

  test("null when RawMessage was never fetched", () => {
    expect(
      extractTransactionOutcome([row("Transaction Ended", "2026-07-01T10:00:00Z")]),
    ).toBeNull();
  });

  test("null when no row ever reaches a Transaction Ended state", () => {
    const logs = [
      row(
        "Transaction Started",
        "2026-07-01T10:00:00Z",
        JSON.stringify({ transactionState: "Started" }),
      ),
      {
        ...row("Job crashed before ending the transaction", "2026-07-01T10:00:01Z"),
        Level: "Error",
      },
    ];
    expect(extractTransactionOutcome(logs)).toBeNull();
  });

  test("takes the LAST Transaction Ended row when a job logs more than one", () => {
    const first = JSON.stringify({
      transactionState: "Ended",
      processingExceptionType: "BusinessException",
    });
    const second = JSON.stringify({ transactionState: "Ended", processingExceptionType: "" });
    const logs = [
      row("Transaction Ended", "2026-07-01T10:00:00Z", first),
      row("Transaction Ended", "2026-07-01T10:05:00Z", second),
    ];
    expect(extractTransactionOutcome(logs)?.processingExceptionType).toBeNull();
  });
});

describe("formatFaultedJobIssue", () => {
  test("builds title/body/labels/marker/searchQuery from the first error log", () => {
    const logs = [errLog("Selector not found for order 998877")];
    const issue = formatFaultedJobIssue(job, logs, {
      env: "prod",
      jobKey: job.Key ?? "",
      deepLink: "https://cloud.uipath.com/org/tenant/orchestrator_/jobs",
    });

    expect(issue.repo).toBe(DEFAULT_REPO);
    expect(issue.labels).toEqual(DEFAULT_LABELS);
    expect(issue.title).toBe("[UiPath Fault] Selector not found for order 998877 (prod)");
    expect(issue.faultSignature).toBe("selector not found for order <n>");

    // body carries the hidden, searchable marker and key metadata
    expect(issue.body).toContain(`<!-- fault-signature: ${issue.faultSignature} -->`);
    expect(issue.body).toContain("**State:** Faulted");
    expect(issue.body).toContain(`**Job Key:** \`${job.Key}\``);
    expect(issue.body).toContain("https://cloud.uipath.com/org/tenant/orchestrator_/jobs");

    // searchQuery targets the repo and the marker phrase
    expect(issue.searchQuery).toBe(
      `repo:${DEFAULT_REPO} is:issue is:open in:body "fault-signature: ${issue.faultSignature}"`,
    );

    // recurrence comment references the job + deep link
    expect(issue.recurrenceComment).toContain(`**Job Key:** \`${job.Key}\``);
    expect(issue.recurrenceComment).toContain("Recurred in `prod`");
  });

  test("respects repo + labels overrides", () => {
    const issue = formatFaultedJobIssue(job, [errLog("boom")], {
      env: "pre_prod",
      jobKey: job.Key ?? "",
      repo: "acme/repo",
      labels: ["rpa"],
    });
    expect(issue.repo).toBe("acme/repo");
    expect(issue.labels).toEqual(["rpa"]);
    expect(issue.searchQuery).toContain("repo:acme/repo");
    expect(issue.title).toContain("(pre_prod)");
  });

  test("falls back to job State when there are no error logs", () => {
    const issue = formatFaultedJobIssue(job, [], { env: "prod", jobKey: job.Key ?? "" });
    expect(issue.title).toBe("[UiPath Fault] Job ended in state Faulted (prod)");
    expect(issue.faultSignature).toBe("job ended in state faulted");
    expect(issue.body).toContain("(no logs)");
  });

  test("adds a queue-exception line when a Transaction Ended row's RawMessage carries one", () => {
    const rawEnded = JSON.stringify({
      transactionState: "Ended",
      processingExceptionType: "BusinessException",
      processingExceptionReason: "missing required field",
    });
    const logs = [
      errLog("Throw: Notification sync request failed with status: 422"),
      {
        Level: "Info",
        Message: "Transaction Ended",
        TimeStamp: "2026-06-30T10:00:31Z",
        RawMessage: rawEnded,
      },
    ];
    const issue = formatFaultedJobIssue(job, logs, { env: "prod", jobKey: job.Key ?? "" });
    expect(issue.body).toContain("**Queue exception:** BusinessException — missing required field");
  });

  test("omits the queue-exception line when RawMessage was never fetched", () => {
    const issue = formatFaultedJobIssue(job, [errLog("boom")], {
      env: "prod",
      jobKey: job.Key ?? "",
    });
    expect(issue.body).not.toContain("**Queue exception:**");
  });
});

// ---- buildFaultedJobIssue (HTTP wiring) --------------------------------------

const envCreds = (name: string) => ({
  be: `https://be.${name}.example.com`,
  email: `${name}@example.com`,
  password: "pw",
});

const UIPATH = {
  orchestratorUrl: "https://cloud.uipath.com/myorg/mytenant/orchestrator_",
  bearer: "test-bearer",
};

const FIXTURE = {
  copilot: { sessionCache: false, prod: envCreds("prod"), pre_prod: envCreds("preprod") },
  uipath: UIPATH,
};

let dir: string;
let prevConfig: string | undefined;

const writeConfig = (config: unknown): void => {
  writeFileSync(join(dir, "config.json"), JSON.stringify(config));
  resetConfigCache();
};

beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), "copilot-mcp-faults-"));
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

let responses: Response[];
const realFetch = globalThis.fetch;

beforeEach(() => {
  responses = [];
  globalThis.fetch = (async (_input: string | URL | Request, _init?: RequestInit) =>
    responses.shift() ?? new Response("{}", { status: 200 })) as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = realFetch;
});

const jobsOdata = (job: Record<string, unknown>): Response =>
  new Response(JSON.stringify({ value: [job] }), { status: 200 });

describe("buildFaultedJobIssue", () => {
  test("returns found:true with no logsError when both the job and logs fetch succeed", async () => {
    responses.push(jobsOdata({ Id: 1, Key: "job-1", State: "Faulted" }));
    responses.push(new Response(JSON.stringify({ value: [] }), { status: 200 }));
    const result = await buildFaultedJobIssue("prod", "job-1");
    expect(result.found).toBe(true);
    expect(result).not.toHaveProperty("logsError");
  });

  test("a transient logs-fetch failure degrades to logsError instead of rejecting the call", async () => {
    responses.push(jobsOdata({ Id: 1, Key: "job-1", State: "Faulted" }));
    responses.push(new Response("server error", { status: 500 }));
    const result = await buildFaultedJobIssue("prod", "job-1");
    expect(result.found).toBe(true);
    expect(result.logsError).toBeDefined();
    expect(result.body).toContain("(no logs)");
  });
});

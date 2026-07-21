import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { resetConfigCache } from "../config/config.js";
import {
  buildFaultedJobIssue,
  DEFAULT_LABELS,
  DEFAULT_REPO,
  formatFaultedJobIssue,
  normalizeError,
} from "./faults.js";
import type { JobLog, UiPathJob } from "./uipath.js";

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

describe("normalizeError", () => {
  test("two faults with different ids/timestamps collapse to the same signature", () => {
    const a = normalizeError(
      "Selector not found for order 998877 at 2026-06-30T10:00:30Z (job 1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d)",
    );
    const b = normalizeError(
      "Selector not found for order 112233 at 2026-06-29T22:14:01Z (job 9f8e7d6c-5b4a-3210-fedc-ba9876543210)",
    );
    expect(a).toBe(b);
    expect(a).toContain("selector not found");
    expect(a).toContain("<id>");
    expect(a).toContain("<n>");
    expect(a).toContain("<ts>");
  });

  test("lowercases, drops quotes/backticks, collapses whitespace, truncates", () => {
    const s = normalizeError(`  Login   FAILED:  "bad creds"  \n\n with \`token\` `);
    expect(s).toBe("login failed: bad creds with token");
    expect(normalizeError("x".repeat(300)).length).toBe(160);
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

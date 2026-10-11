// src/apex/faults.test.ts
import { describe, expect, test } from "bun:test";
import { DEFAULT_REPO, faultSearchQuery, signatureMarker } from "../orchestrator/faults.js";
import type { JobAttempt, JobLog } from "../orchestrator/types.js";
import { APEX_DEFAULT_LABELS, type ApexFaultJob, formatApexFaultIssue } from "./faults.js";

const APEX = "apex" as const;
const SIG = "apex:business_failure: portal rejected order <n> at <ts>";

const faultJob = (over: Partial<ApexFaultJob> = {}): ApexFaultJob => ({
  orchestrator: APEX,
  id: "job-2",
  state: "Faulted",
  rawState: "failed",
  terminal: true,
  process: { id: "wf-1", name: "optum-auth-submit", version: "1.4.2" },
  queueItem: { queueId: "q-1", itemId: "item-1", reference: "0123456789" },
  createdAt: "2026-10-11T09:59:00Z",
  startedAt: "2026-10-11T10:00:00Z",
  endedAt: "2026-10-11T10:01:00Z",
  durationMs: 60000,
  attempt: 2,
  retryOfJobId: "job-1",
  fault: {
    message: "Portal rejected order 998877 at 2026-10-11T10:00:09Z",
    signature: SIG,
    class: "business_failure",
  },
  link: "https://apex.example.com/jobs/job-2",
  details: { errorClass: "permanent", outcome: "business_failure", exitCode: 21 },
  ...over,
});

const attempts: JobAttempt[] = [
  { orchestrator: APEX, id: "job-2", state: "Faulted", neverStarted: false },
  { orchestrator: APEX, id: "job-1", state: "Stopped", neverStarted: false },
];

const row = (Level: string, Message: string): JobLog => ({
  Level,
  Message,
  TimeStamp: "2026-10-11T10:00:00Z",
});

const OPTS = { env: "prod" as const, attempts };

describe("formatApexFaultIssue", () => {
  test("titles by class + first message line, defaults repo/labels, carries the signature everywhere it must", () => {
    const issue = formatApexFaultIssue(faultJob(), [], OPTS);
    expect(issue.title).toBe(
      "[Apex Fault] business_failure: Portal rejected order 998877 at 2026-10-11T10:00:09Z (prod)",
    );
    expect(issue.repo).toBe(DEFAULT_REPO);
    expect(issue.labels).toEqual(APEX_DEFAULT_LABELS);
    expect(APEX_DEFAULT_LABELS).toEqual(["apex-fault", "bug"]);
    expect(issue.faultSignature).toBe(SIG);
    expect(issue.searchQuery).toBe(faultSearchQuery(DEFAULT_REPO, SIG));
    expect(issue.body).toContain(signatureMarker(SIG));
    expect(issue.body).toContain("optum-auth-submit@1.4.2");
    expect(issue.body).toContain("https://apex.example.com/jobs/job-2");
    expect(issue.recurrenceComment).toContain("job-2");
    expect(issue.recurrenceComment).toContain("prod");
  });

  test("a null fault class is titled 'unclassified'", () => {
    const issue = formatApexFaultIssue(
      faultJob({ fault: { message: "boom", signature: "apex:unclassified: boom", class: null } }),
      [],
      OPTS,
    );
    expect(issue.title).toBe("[Apex Fault] unclassified: boom (prod)");
  });

  test("the attempt chain is listed oldest first and marks the faulted job", () => {
    const { body } = formatApexFaultIssue(faultJob(), [], OPTS);
    expect(body).toContain("1. `job-1` — Stopped");
    expect(body).toContain("2. `job-2` — Faulted (this job)");
    expect(body.indexOf("1. `job-1`")).toBeLessThan(body.indexOf("2. `job-2`"));
  });

  test("a never-started attempt is flagged in the chain", () => {
    const { body } = formatApexFaultIssue(faultJob(), [], {
      env: "prod",
      attempts: [{ orchestrator: APEX, id: "job-2", state: "Faulted", neverStarted: true }],
    });
    expect(body).toContain("`job-2` — Faulted, never started (this job)");
  });

  test("exit code 0 is shown; null outcome/class/exit code are omitted", () => {
    const zero = formatApexFaultIssue(
      faultJob({ details: { errorClass: null, outcome: null, exitCode: 0 } }),
      [],
      OPTS,
    );
    expect(zero.body).toContain("Exit code");
    expect(zero.body).not.toContain("Outcome");
    expect(zero.body).not.toContain("Error class");

    const none = formatApexFaultIssue(
      faultJob({ details: { errorClass: null, outcome: null, exitCode: null } }),
      [],
      OPTS,
    );
    expect(none.body).not.toContain("Exit code");
  });

  test("a null process version prints the bare process name", () => {
    const { body } = formatApexFaultIssue(
      faultJob({ process: { id: "wf-1", name: "optum-auth-submit", version: null } }),
      [],
      OPTS,
    );
    expect(body).toContain("optum-auth-submit");
    expect(body).not.toContain("optum-auth-submit@");
  });

  test("custom repo and labels win; an empty labels array falls back to the default", () => {
    const custom = formatApexFaultIssue(faultJob(), [], {
      ...OPTS,
      repo: "acme/other",
      labels: ["triage"],
    });
    expect(custom.repo).toBe("acme/other");
    expect(custom.labels).toEqual(["triage"]);
    expect(custom.searchQuery).toBe(faultSearchQuery("acme/other", SIG));

    expect(formatApexFaultIssue(faultJob(), [], { ...OPTS, labels: [] }).labels).toEqual(
      APEX_DEFAULT_LABELS,
    );
  });

  test("signature, searchQuery and marker are stable across job ids, timestamps and message ids", () => {
    const a = formatApexFaultIssue(faultJob(), [], OPTS);
    const b = formatApexFaultIssue(
      faultJob({
        id: "job-9",
        createdAt: "2026-10-12T01:02:03Z",
        link: "https://apex.example.com/jobs/job-9",
        fault: {
          message: "Portal rejected order 112233 at 2026-10-12T01:02:09Z",
          signature: SIG,
          class: "business_failure",
        },
      }),
      [],
      { env: "prod", attempts: [] },
    );
    expect(b.faultSignature).toBe(a.faultSignature);
    expect(b.searchQuery).toBe(a.searchQuery);
    expect(b.body).toContain(signatureMarker(SIG));
    expect(b.body).not.toBe(a.body);
    expect(b.recurrenceComment).not.toBe(a.recurrenceComment);
  });

  test("the log tail keeps only warn/error rows and, past the cap, the newest ones", () => {
    const { body } = formatApexFaultIssue(
      faultJob(),
      [
        row("INFO", "login ok"),
        row("ERROR", `first-${"x".repeat(5000)}`),
        row("warning", "retrying selector"),
        row("ERROR", "last-row"),
      ],
      OPTS,
    );
    expect(body).not.toContain("login ok");
    expect(body).toContain("retrying selector");
    expect(body).toContain("last-row");
    expect(body).not.toContain("first-");
  });

  test("only INFO rows leave an empty tail, not a crash", () => {
    const { body } = formatApexFaultIssue(faultJob(), [row("INFO", "only info")], OPTS);
    expect(body).not.toContain("only info");
  });
});

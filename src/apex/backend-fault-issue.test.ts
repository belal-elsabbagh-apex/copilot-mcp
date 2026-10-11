// src/apex/backend-fault-issue.test.ts
import { describe, expect, test } from "bun:test";
import { ExpectedError } from "../shared/expected-error.js";
import { apexBuildFaultedJobIssue } from "./backend.js";
import { FOLDER_IDS, json, useApexStub } from "./test-support.js";

type Stub = ReturnType<typeof useApexStub>;

const JOB = "11111111-aaaa-bbbb-cccc-000000000001";
const PARENT = "11111111-aaaa-bbbb-cccc-000000000000";

const rawJob = (over: Record<string, unknown> = {}): Record<string, unknown> => ({
  id: JOB,
  status: "failed",
  workflow_id: "wf-1",
  folder_id: FOLDER_IDS["Authorization"],
  workflow_name: "optum-auth-submit",
  workflow_version: "1.4.2",
  queue_id: "q-1",
  queue_name: "OPTUM auth submit queue",
  queue_item_id: "item-1",
  queue_item_reference: "0123456789",
  match_key: "automationId",
  match_value: "auto-1",
  priority: "normal",
  source: "queue",
  idempotency_key: null,
  credential_id: "cred-1",
  credential_name: "svc-account",
  worker_id: "worker-1",
  retry_of_job_id: null,
  attempt_number: 1,
  error_class: "permanent",
  error_message: "Portal rejected order 998877 at 2026-10-11T10:00:09Z",
  exit_code: 21,
  outcome: "business_failure",
  outcome_label: "Business failure",
  needs_review: false,
  callback_type: null,
  callback_status: null,
  started_at: "2026-10-11T10:00:00Z",
  claimed_at: "2026-10-11T09:59:59Z",
  completed_at: "2026-10-11T10:01:00Z",
  next_eligible_at: null,
  trace_id: null,
  created_at: "2026-10-11T09:59:00Z",
  updated_at: "2026-10-11T10:01:00Z",
  duration_seconds: 60,
  effective_timeout_seconds: 900,
  input_payload: {},
  result_payload: null,
  ...over,
});

const page = (items: unknown[]) =>
  json({ items, total: items.length, page: 1, page_size: 200, total_pages: 1 });

// Routes match by path prefix and the latest registration wins, so the broad detail
// route goes first and the narrower artifacts/list routes override it.
const serveJob = (
  apex: Stub,
  id: string,
  detail: Record<string, unknown>,
  attempts: Record<string, unknown>[],
): void => {
  apex.route("GET", `/jobs/${id}`, () => json(detail));
  apex.route("GET", "/jobs?", () => page(attempts));
  apex.route("GET", `/jobs/${id}/artifacts`, () => json([]));
};

const LOG = [
  "=== apex job x started (workflow=optum-auth-submit runtime=python) ===",
  '{"v":1,"timestamp":"2026-10-11T10:00:01Z","level":"INFO","portal":"OPTUM","message":"login ok"}',
  '{"v":1,"timestamp":"2026-10-11T10:00:09Z","level":"ERROR","portal":"OPTUM","message":"selector not found"}',
  "[apex-adapter] result=FAILED reason=- exit=21 outcomes=-",
  "=== apex job x finished: exit_code=21 timed_out=False cancelled=False suspended=False ===",
].join("\n");

const serveLog = (apex: Stub, id: string, text: string): void => {
  apex.route("GET", `/jobs/${id}/artifacts`, () =>
    json([
      {
        id: "art-1",
        key: `jobs/${id}/job.log`,
        filename: "job.log",
        artifact_type: "logs",
        size_bytes: text.length,
        last_modified: "2026-10-11T10:01:00Z",
        content_type: "text/plain",
        download_url: `/api/v1/jobs/${id}/artifacts/art-1`,
      },
    ]),
  );
  apex.route("GET", `/jobs/${id}/artifacts/art-1`, () => new Response(text, { status: 200 }));
};

describe("apexBuildFaultedJobIssue", () => {
  const apex = useApexStub();

  test("builds the payload from the job, its attempt chain and the warn/error log rows", async () => {
    const detail = rawJob({ retry_of_job_id: PARENT, attempt_number: 2 });
    serveJob(apex, JOB, detail, [
      detail,
      rawJob({ id: PARENT, status: "cancelled", attempt_number: 1, error_class: null }),
      rawJob({ id: "other-item-job", queue_item_id: "item-2" }), // same reference, different item
    ]);
    serveLog(apex, JOB, LOG);

    const out = await apexBuildFaultedJobIssue({ env: "prod", jobId: JOB });

    expect(out.found).toBe(true);
    expect(out.logsError).toBeUndefined();
    expect(out.title).toBe(
      "[Apex Fault] business_failure: Portal rejected order 998877 at 2026-10-11T10:00:09Z (prod)",
    );
    expect(out.labels).toEqual(["apex-fault", "bug"]);
    expect(out.faultSignature.startsWith("apex:")).toBe(true);
    expect(out.body).toContain(`1. \`${PARENT}\``);
    expect(out.body).toContain(`2. \`${JOB}\``);
    expect(out.body).not.toContain("other-item-job");
    expect(out.body).toContain("selector not found");
    expect(out.body).not.toContain("login ok");
    expect(apex.callsTo("GET", "/jobs?")[0]?.path).toContain("reference=0123456789");
  });

  test("a log fetch failure surfaces as logsError and the issue is still built", async () => {
    serveJob(apex, JOB, rawJob(), [rawJob()]);
    apex.route("GET", `/jobs/${JOB}/artifacts`, () => json({ detail: "Not Found" }, 404));

    const out = await apexBuildFaultedJobIssue({ env: "prod", jobId: JOB });

    expect(out.found).toBe(true);
    expect(out.logsError).toBeTruthy();
    expect(out.title).toStartWith("[Apex Fault]");
  });

  test("a never-started job (no job.log artifact) is not a log error and is flagged in the chain", async () => {
    const reaped = rawJob({
      error_class: "missing_credential",
      error_message: "No credential bound for portal",
      outcome: null,
      outcome_label: null,
      exit_code: null,
      started_at: null,
      claimed_at: null,
      worker_id: null,
      credential_id: null,
    });
    serveJob(apex, JOB, reaped, [reaped]);

    const out = await apexBuildFaultedJobIssue({ env: "prod", jobId: JOB });

    expect(out.logsError).toBeUndefined();
    expect(out.title).toBe(
      "[Apex Fault] missing_credential: No credential bound for portal (prod)",
    );
    expect(out.body).toContain("never started");
  });

  test("a job with no queue item reference lists only itself and never queries /jobs", async () => {
    serveJob(apex, JOB, rawJob({ queue_item_reference: null, queue_item_id: null }), []);

    const out = await apexBuildFaultedJobIssue({ env: "prod", jobId: JOB });

    expect(apex.callsTo("GET", "/jobs?")).toHaveLength(0);
    expect(out.body).toContain(`1. \`${JOB}\``);
  });

  test("a job that did not fault is refused instead of filed as a bug", async () => {
    serveJob(
      apex,
      JOB,
      rawJob({
        status: "completed",
        error_class: null,
        error_message: null,
        exit_code: 0,
        outcome: "success",
        outcome_label: "Success",
      }),
      [],
    );

    await expect(apexBuildFaultedJobIssue({ env: "prod", jobId: JOB })).rejects.toBeInstanceOf(
      ExpectedError,
    );
  });

  test("a cancelled job is a cancellation or a superseded retry parent, not a fault", async () => {
    serveJob(
      apex,
      JOB,
      rawJob({ status: "cancelled", error_class: null, error_message: null }),
      [],
    );

    const err = await apexBuildFaultedJobIssue({ env: "prod", jobId: JOB }).catch((e) => e);

    expect(err).toBeInstanceOf(ExpectedError);
    expect((err as Error).message).toMatch(/cancel/);
  });

  test("the same fault on another job (new id, timestamps, message ids) keeps one signature", async () => {
    const other = "22222222-aaaa-bbbb-cccc-000000000002";
    serveJob(
      apex,
      JOB,
      rawJob({
        error_message:
          "Selector not found for order 998877 at 2026-10-11T10:00:09Z (job 1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d)",
      }),
      [],
    );
    const a = await apexBuildFaultedJobIssue({ env: "prod", jobId: JOB });

    serveJob(
      apex,
      other,
      rawJob({
        id: other,
        created_at: "2026-10-12T01:00:00Z",
        error_message:
          "Selector not found for order 112233 at 2026-10-12T01:00:09Z (job 9f8e7d6c-5b4a-3210-fedc-ba9876543210)",
      }),
      [],
    );
    const b = await apexBuildFaultedJobIssue({ env: "prod", jobId: other });

    expect(b.faultSignature).toBe(a.faultSignature);
    expect(b.searchQuery).toBe(a.searchQuery);
    expect(b.body).not.toBe(a.body);
  });
});

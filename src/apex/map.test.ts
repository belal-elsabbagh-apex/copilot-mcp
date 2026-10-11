import { describe, expect, test } from "bun:test";
import { QUEUE_ITEM_STATUSES } from "../orchestrator/types.js";
import { parseJobLog } from "./job-log.js";
import {
  APEX_ITEM_LABELS,
  APEX_JOB_LABELS,
  APEX_TERMINAL_STATUSES,
  apexFault,
  attemptsFromItem,
  toApexItemCore,
  toApexItemStatus,
  toApexJobAttempt,
  toApexJobCore,
  toApexProcessCore,
  toApexQueueCore,
  toApexQueueTriggers,
  toApexRunSummary,
  toApexScheduleTrigger,
} from "./map.js";
import {
  itemDetail,
  itemRow,
  jobDetail,
  jobRow,
  queueRow,
  scheduleRow,
  workflowRow,
} from "./test-rows.js";

// The status lists the dashboard bundle's label maps (`Jr`, `$r`) enumerate.
const BUNDLE_JOB_STATUSES = [
  "queued",
  "claimed",
  "running",
  "completed",
  "failed",
  "cancelled",
  "cancelling",
  "lost",
  "timed_out",
  "suspended",
  "resumed",
];
const BUNDLE_ITEM_STATUSES = [
  "new",
  "in_progress",
  "successful",
  "failed",
  "retried",
  "abandoned",
  "deleted",
];

describe("label maps", () => {
  test("cover exactly the dashboard's job and item statuses", () => {
    expect(Object.keys(APEX_JOB_LABELS).sort()).toEqual([...BUNDLE_JOB_STATUSES].sort());
    expect(Object.keys(APEX_ITEM_LABELS).sort()).toEqual([...BUNDLE_ITEM_STATUSES].sort());
  });

  test("terminal statuses are exactly completed/failed/cancelled/lost/timed_out", () => {
    expect([...APEX_TERMINAL_STATUSES].sort()).toEqual(
      ["cancelled", "completed", "failed", "lost", "timed_out"].sort(),
    );
  });

  test("item labels are UiPath's QueueItemStatus spellings, and the wire map inverts them", () => {
    expect(Object.values(APEX_ITEM_LABELS).sort()).toEqual([...QUEUE_ITEM_STATUSES].sort());
    for (const [wire, label] of Object.entries(APEX_ITEM_LABELS)) {
      expect(toApexItemStatus(label as (typeof QUEUE_ITEM_STATUSES)[number])).toBe(wire);
    }
  });
});

describe("toApexJobCore", () => {
  test("every status maps to its label and terminal flag", () => {
    for (const status of BUNDLE_JOB_STATUSES) {
      const core = toApexJobCore(jobRow({ status }), "");
      expect(core.state).toBe(APEX_JOB_LABELS[status] ?? "");
      expect(core.rawState).toBe(status);
      expect(core.terminal).toBe(APEX_TERMINAL_STATUSES.has(status));
    }
  });

  test("an unknown status passes through verbatim and is not terminal", () => {
    const core = toApexJobCore(jobRow({ status: "paused_by_ops" }), "");
    expect(core).toMatchObject({ state: "paused_by_ops", terminal: false });
  });

  test("shapes ids, timing, retry fields and link", () => {
    const core = toApexJobCore(
      jobRow({ attempt_number: 2, retry_of_job_id: "prev-job" }),
      "https://x/jobs/1",
    );
    expect(core).toMatchObject({
      orchestrator: "apex",
      process: { id: jobRow().workflow_id, name: "demo-auth-submit", version: "1.0.2" },
      queueItem: {
        queueId: jobRow().queue_id,
        itemId: jobRow().queue_item_id,
        reference: "1000000001",
      },
      createdAt: "2026-10-08T00:39:49.044176Z",
      startedAt: "2026-10-08T00:39:51.248748Z",
      endedAt: "2026-10-08T00:44:14.649417Z",
      durationMs: 263401,
      attempt: 2,
      retryOfJobId: "prev-job",
      link: "https://x/jobs/1",
    });
  });

  test("a manual job (no queue item) has queueItem null", () => {
    const core = toApexJobCore(jobRow({ queue_item_id: null, source: "manual" }), "");
    expect(core.queueItem).toBeNull();
    expect(core.details.source).toBe("manual");
  });

  test("terminal + never started => neverStarted; a queued job with no start is not", () => {
    const reaped = jobRow({
      status: "failed",
      started_at: null,
      error_class: "missing_credential",
    });
    expect(toApexJobCore(reaped, "").details.neverStarted).toBe(true);
    expect(
      toApexJobCore(jobRow({ status: "queued", started_at: null }), "").details.neverStarted,
    ).toBe(false);
    expect(toApexJobCore(jobRow({ status: "failed" }), "").details.neverStarted).toBe(false);
  });

  test("a list row leaves detail-only fields null/absent; a detail fills them", () => {
    const row = toApexJobCore(jobRow({ status: "failed", error_class: "permanent" }), "");
    expect(row.details).toMatchObject({
      outcome: null,
      exitCode: null,
      needsReview: null,
      effectiveTimeoutSeconds: null,
      errorClass: "permanent",
    });
    expect("result" in row.details || "input" in row.details).toBe(false);

    const full = toApexJobCore(
      jobDetail({
        status: "failed",
        exit_code: 21,
        outcome: "business_failure",
        needs_review: true,
      }),
      "",
    );
    expect(full.details).toMatchObject({
      outcome: "business_failure",
      exitCode: 21,
      needsReview: true,
      effectiveTimeoutSeconds: 900,
      result: { result: "approved" },
    });
  });

  test("input drops token and callbackContext but keeps everything else", () => {
    const { details } = toApexJobCore(jobDetail(), "");
    expect(details.input).toEqual({ orderUid: "order-1", retryCount: 0 });
  });

  test("a null input_payload yields no input key", () => {
    expect("input" in toApexJobCore(jobDetail({ input_payload: null }), "").details).toBe(false);
  });

  test("durationMs is null when the job never ran", () => {
    expect(toApexJobCore(jobRow({ duration_seconds: null }), "").durationMs).toBeNull();
  });
});

describe("apexFault", () => {
  const failed = (o: Parameters<typeof jobDetail>[0]) => jobDetail({ status: "failed", ...o });

  test("null for completed and for a still-running detail", () => {
    expect(apexFault(jobDetail({ status: "completed", error_message: "ignored" }))).toBeNull();
    expect(apexFault(jobDetail({ status: "running", error_message: "x" }))).toBeNull();
  });

  test("message prefers error_message over outcome_label; class prefers outcome over error_class", () => {
    const fault = apexFault(
      failed({
        error_message: "referred provider not found",
        outcome_label: "Business failure",
        outcome: "business_failure",
        error_class: "permanent",
      }),
    );
    expect(fault).toMatchObject({
      message: "referred provider not found",
      class: "business_failure",
    });
  });

  test("falls back to outcome_label, error_class, then a status headline", () => {
    expect(
      apexFault(failed({ error_message: null, outcome_label: "Callback rejected" }))?.message,
    ).toBe("Callback rejected");
    expect(apexFault(failed({ outcome: null, error_class: "transient" }))?.class).toBe("transient");
    const bare = apexFault(failed({ error_message: null, outcome_label: null, error_class: null }));
    expect(bare).toMatchObject({ message: "Job ended in status failed", class: null });
    expect(bare?.signature).toBe("apex:unclassified: job ended in status failed");
  });

  test("signature is apex:-prefixed and stable across ids and timestamps", () => {
    const sig = (msg: string) => apexFault(failed({ outcome: "x", error_message: msg }))?.signature;
    const a = sig("job aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee failed at 2026-10-08T00:44:14Z");
    const b = sig("job 11111111-2222-3333-4444-555555555555 failed at 2026-10-09T09:00:00Z");
    expect(a).toStartWith("apex:");
    expect(a).toBe(b);
  });

  test("a list row carries only the class", () => {
    expect(apexFault(jobRow({ status: "failed", error_class: "needs_human" }))).toEqual({
      message: null,
      signature: null,
      class: "needs_human",
    });
    expect(apexFault(jobRow({ status: "failed", error_class: null }))).toBeNull();
    expect(apexFault(jobRow({ status: "completed", error_class: "permanent" }))).toBeNull();
  });
});

describe("toApexJobAttempt / toApexRunSummary", () => {
  test("attempt carries label and neverStarted", () => {
    expect(toApexJobAttempt(jobRow({ status: "failed", started_at: null }))).toEqual({
      orchestrator: "apex",
      id: jobRow().id,
      state: "Faulted",
      neverStarted: true,
    });
  });

  test("the log footer outranks the detail; the detail covers a missing footer", () => {
    const detail = jobDetail({
      status: "failed",
      exit_code: 1,
      outcome: "crash",
      error_class: "permanent",
    });
    const withFooter = parseJobLog(
      "=== apex job x finished: exit_code=21 timed_out=True cancelled=False suspended=False ===",
    );
    expect(toApexRunSummary(detail, withFooter)).toEqual({
      exitCode: 21,
      outcome: "crash",
      errorClass: "permanent",
      timedOut: true,
      cancelled: false,
      adapterResult: null,
    });
    const noFooter = toApexRunSummary({ ...detail, status: "timed_out" }, parseJobLog("plain"));
    expect(noFooter).toMatchObject({ exitCode: 1, timedOut: true, cancelled: false });
  });
});

describe("toApexItemCore", () => {
  test("labels the status, keeps the raw one, and extracts orderUid", () => {
    const core = toApexItemCore(
      itemRow({ status: "in_progress" }),
      [],
      false,
      "DEMO auth submit queue",
    );
    expect(core).toMatchObject({
      orchestrator: "apex",
      queueName: "DEMO auth submit queue",
      status: "InProgress",
      rawStatus: "in_progress",
      orderUid: "order-1",
      exceptionType: "business",
      createdAt: "2026-10-08T00:29:26.164862Z",
      endedAt: "2026-10-08T00:44:14.655419Z",
    });
  });

  test("retry fields land in details", () => {
    const core = toApexItemCore(
      itemRow({ retry_count: 2, parent_item_id: "parent-1", priority: 10, match_value: "auto-9" }),
      [],
      false,
      "q",
    );
    expect(core.details).toEqual({
      retryCount: 2,
      parentItemId: "parent-1",
      priority: 10,
      matchValue: "auto-9",
      memberId: "",
      member: "",
    });
  });

  test("memberId and member come from the content even when content is not requested", () => {
    const sc = { orderUid: "o", MemberID: "M-1", MemberFullName: "Doe, Jane", token: "t" };
    const core = toApexItemCore(itemRow({ specific_content: sc }), [], false, "q");
    expect(core.specificContent).toBeNull();
    expect(core.details).toMatchObject({ memberId: "M-1", member: "Doe, Jane" });
    const odd = toApexItemCore(itemRow({ specific_content: { MemberID: 7 } }), [], false, "q");
    expect(odd.details).toMatchObject({ memberId: "", member: "" });
  });

  test("content is null unless requested, and the token is redacted when it is", () => {
    expect(toApexItemCore(itemRow(), [], false, "q").specificContent).toBeNull();
    const sc = toApexItemCore(itemRow(), [], true, "q").specificContent;
    expect(sc?.["token"]).toBe("[redacted]");
    expect(sc?.["orderUid"]).toBe("order-1");
  });

  test("an unexpected exception_type and a missing orderUid degrade to null", () => {
    const core = toApexItemCore(
      itemRow({ exception_type: "weird", specific_content: { orderUid: 5 } }),
      [],
      false,
      "q",
    );
    expect(core).toMatchObject({ exceptionType: null, orderUid: null });
  });

  test("a detail adds callback status and a camelCased lineage with job state", () => {
    const core = toApexItemCore(
      itemDetail({
        callback_status: "failed",
        lineage: [
          {
            item_id: "i1",
            reference: "r1",
            status: "retried",
            exception_type: null,
            job: { id: "j1", status: "cancelled", error_class: "transient" },
          },
          { item_id: "i2", reference: "r2", status: "new", exception_type: null, job: null },
        ],
      }),
      [],
      false,
      "q",
    );
    expect(core.details.callbackStatus).toBe("failed");
    expect(core.details.lineage).toEqual([
      {
        itemId: "i1",
        reference: "r1",
        status: "Retried",
        exceptionType: null,
        jobId: "j1",
        jobStatus: "Stopped",
        errorClass: "transient",
      },
      {
        itemId: "i2",
        reference: "r2",
        status: "New",
        exceptionType: null,
        jobId: null,
        jobStatus: null,
        errorClass: null,
      },
    ]);
  });

  test("attemptsFromItem exposes only the latest job id, state unknown", () => {
    expect(attemptsFromItem(itemRow())).toEqual([
      {
        orchestrator: "apex",
        id: itemRow().job_id ?? "",
        state: null,
        neverStarted: null,
      },
    ]);
    expect(attemptsFromItem(itemRow({ job_id: null }))).toEqual([]);
  });
});

describe("queues, processes, triggers", () => {
  test("queue core", () => {
    expect(toApexQueueCore(queueRow({ is_active: false, max_retries: 3 }))).toMatchObject({
      name: "DEMO auth submit queue",
      processId: queueRow().workflow_id,
      active: false,
      details: {
        autoDrain: true,
        maxConcurrentJobs: 1,
        maxRetries: 3,
        enforceUniqueReference: false,
      },
    });
  });

  test("process core renames the workflow fields", () => {
    expect(toApexProcessCore(workflowRow())).toMatchObject({
      id: workflowRow().id,
      package: "demo-auth-submit",
      version: "1.0.2",
      active: true,
      details: {
        timeoutSeconds: 900,
        maxAttempts: 3,
        backoffStrategy: "exponential",
        artifactPolicy: "on_failure",
      },
    });
  });

  test("a queue trigger is enabled only when active AND auto-draining", () => {
    const [both, paused, noDrain] = toApexQueueTriggers([
      queueRow(),
      queueRow({ is_active: false }),
      queueRow({ auto_drain: false, max_concurrent_jobs: 4 }),
    ]);
    expect(both).toMatchObject({
      kind: "queue",
      enabled: true,
      cron: null,
      queueId: queueRow().id,
      processId: queueRow().workflow_id,
      details: { maxConcurrent: 1 },
    });
    expect(paused?.enabled).toBe(false);
    expect(noDrain).toMatchObject({ enabled: false, details: { maxConcurrent: 4 } });
  });

  test("a schedule becomes a time trigger; paused = disabled", () => {
    expect(toApexScheduleTrigger(scheduleRow())).toMatchObject({
      kind: "time",
      cron: "0 6 * * *",
      enabled: true,
      queueId: null,
      processId: scheduleRow().workflow_id,
    });
    expect(toApexScheduleTrigger(scheduleRow({ is_paused: true })).enabled).toBe(false);
  });
});

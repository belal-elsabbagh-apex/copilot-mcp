import { describe, expect, test } from "bun:test";
import { ExpectedError } from "../shared/expected-error.js";
import {
  apexFindOrderQueueItems,
  apexGetJobLogs,
  apexGetJobs,
  apexListJobs,
  apexListProcesses,
  apexListQueueItems,
  apexListQueues,
  apexListTriggers,
  apexPullQueueItem,
} from "./backend.js";
import {
  artifactRow,
  ITEM_ID,
  itemDetail,
  itemRow,
  JOB_ID,
  jobDetail,
  jobRow,
  QUEUE_ID,
  queueRow,
  scheduleRow,
  workflowRow,
} from "./test-rows.js";
import { type ApexCall, json, useApexStub } from "./test-support.js";

const page = (items: unknown[]) =>
  json({ items, total: items.length, page: 1, page_size: 200, total_pages: 1 });
const query = (c: ApexCall | undefined): Record<string, string> =>
  Object.fromEntries(new URL(`https://x${c?.path ?? ""}`).searchParams);

const T = "2026-10-08T00:40:00.000Z";
const nd = (o: Record<string, unknown>): string =>
  JSON.stringify({ v: 1, portal: "demo", timestamp: T, ...o });
const FOOTER = (exit: number) =>
  `=== apex job ${JOB_ID} finished: exit_code=${exit} timed_out=False cancelled=False suspended=False ===`;
const LOG = [
  `=== apex job ${JOB_ID} started (workflow=demo runtime=python) ===`,
  nd({ level: "INFO", message: "login ok" }),
  nd({ level: "WARN", message: "slow page", fields: { ms: 5200 } }),
  nd({ level: "ERROR", message: "row not found" }),
  nd({ level: "INFO", message: "x".repeat(500) }),
  "[apex-adapter] result=FAILED reason=business_failure exit=21 outcomes=-",
  FOOTER(21),
].join("\n");

const ART = artifactRow();
const artPath = (a = ART) => a.download_url.replace("/api/v1", "");

// Routes for one job: detail, artifact listing, and the job.log download. The more specific
// routes are registered last (the stub takes the last matching prefix).
function routeJob(
  stub: ReturnType<typeof useApexStub>,
  detail: ReturnType<typeof jobDetail>,
  artifacts: ReturnType<typeof artifactRow>[] = [ART],
  log = LOG,
): void {
  stub.route("GET", `/jobs/${detail.id}`, () => json(detail));
  stub.route("GET", `/jobs/${detail.id}/artifacts`, () => json(artifacts));
  stub.route("GET", artPath(), () => new Response(log, { status: 200 }));
}

describe("apexListJobs", () => {
  const stub = useApexStub();

  test("without processName: one newest-first request, since -> created_after, cores mapped", async () => {
    stub.route("GET", "/jobs", () =>
      page([jobRow({ status: "failed", error_class: "permanent" })]),
    );
    const out = await apexListJobs({ env: "pre_prod", since: "2026-10-01T00:00:00Z", top: 5 });
    const calls = stub.callsTo("GET", "/jobs");
    expect(calls).toHaveLength(1);
    expect(query(calls[0])).toMatchObject({
      created_after: "2026-10-01T00:00:00Z",
      sort_by: "created_at",
      sort_dir: "desc",
    });
    expect("workflow_id" in query(calls[0])).toBe(false);
    expect(out.folder).toBe("Authorization Test");
    expect(out.jobs[0]).toMatchObject({
      state: "Faulted",
      link: `https://apex.example.com/jobs/${JOB_ID}`,
      fault: { class: "permanent", message: null },
    });
  });

  test("processName matches workflows by case-insensitive substring and merges newest first", async () => {
    stub.route("GET", "/workflows", () =>
      page([
        workflowRow({ id: "wf-a", name: "demo-auth-submit" }),
        workflowRow({ id: "wf-b", name: "other-thing" }),
        workflowRow({ id: "wf-c", name: "DEMO-eligibility" }),
      ]),
    );
    stub.route("GET", "/jobs", (c) => {
      const wf = query(c)["workflow_id"];
      return page(
        wf === "wf-a"
          ? [
              jobRow({ id: "a2", created_at: "2026-10-08T00:00:05Z" }),
              jobRow({ id: "a1", created_at: "2026-10-08T00:00:01Z" }),
            ]
          : [jobRow({ id: "c1", created_at: "2026-10-08T00:00:03Z" })],
      );
    });
    const out = await apexListJobs({ env: "pre_prod", top: 2, processName: "DEMO" });
    expect(
      stub
        .callsTo("GET", "/jobs")
        .map((c) => query(c)["workflow_id"])
        .sort(),
    ).toEqual(["wf-a", "wf-c"]);
    expect(out.jobs.map((j) => j.id)).toEqual(["a2", "c1"]);
  });

  test("a processName that matches no workflow lists nothing and queries no jobs", async () => {
    stub.route("GET", "/workflows", () => page([workflowRow({ name: "demo-auth-submit" })]));
    const out = await apexListJobs({ env: "pre_prod", top: 5, processName: "zzz" });
    expect(out.jobs).toEqual([]);
    expect(stub.callsTo("GET", "/jobs")).toHaveLength(0);
  });
});

describe("apexGetJobs", () => {
  const stub = useApexStub();
  const args = {
    env: "pre_prod" as const,
    jobIds: [JOB_ID],
    batch: false,
    includeLogDigest: false,
  };

  test("a 404 is found:false, not an error", async () => {
    stub.route("GET", `/jobs/${JOB_ID}`, () => json({ detail: "Job not found" }, 404));
    const out = await apexGetJobs(args);
    expect(out.entries).toEqual([{ id: JOB_ID, found: false }]);
  });

  test("a non-404 failure throws for a single id but lands on the entry in a batch", async () => {
    stub.route("GET", "/jobs/bad", () => json({ detail: "kaput" }, 500));
    stub.route("GET", `/jobs/${JOB_ID}`, () => json(jobDetail()));
    await expect(apexGetJobs({ ...args, jobIds: ["bad"] })).rejects.toThrow(/kaput/);
    const out = await apexGetJobs({ ...args, jobIds: ["bad", JOB_ID], batch: true });
    expect(out.entries[0]).toMatchObject({
      id: "bad",
      found: false,
      error: expect.stringContaining("kaput"),
    });
    expect(out.entries[1]).toMatchObject({ id: JOB_ID, found: true });
  });

  test("includeLogDigest: digests the parsed job.log and summarizes the run from its footer", async () => {
    routeJob(stub, jobDetail({ status: "failed", outcome: "business_failure", exit_code: 21 }));
    const [entry] = (await apexGetJobs({ ...args, includeLogDigest: true })).entries;
    expect(entry?.logsUnavailable).toBeUndefined();
    expect(entry?.logDigest).toMatchObject({
      byLevel: { Info: 5, Warn: 1, Error: 1 },
      run: { exitCode: 21, outcome: "business_failure", timedOut: false, cancelled: false },
    });
    expect(entry?.logDigest?.failures.map((f) => f.message)).toContain("row not found");
    expect(entry?.job?.fault?.signature).toStartWith("apex:");
  });

  test("a running job has no log yet: reason running, no artifact request", async () => {
    stub.route("GET", `/jobs/${JOB_ID}`, () =>
      json(jobDetail({ status: "running", completed_at: null })),
    );
    const [entry] = (await apexGetJobs({ ...args, includeLogDigest: true })).entries;
    expect(entry?.logsUnavailable?.reason).toBe("running");
    expect(stub.callsTo("GET", `/jobs/${JOB_ID}/artifacts`)).toHaveLength(0);
  });

  test("a never-started job has no artifacts: reason never-started, no artifact request", async () => {
    const reaped = jobDetail({
      status: "failed",
      started_at: null,
      error_class: "missing_credential",
    });
    stub.route("GET", `/jobs/${JOB_ID}`, () => json(reaped));
    const [entry] = (await apexGetJobs({ ...args, includeLogDigest: true })).entries;
    expect(entry?.logsUnavailable?.reason).toBe("never-started");
    expect(entry?.job?.details).toMatchObject({ neverStarted: true });
    expect(stub.callsTo("GET", `/jobs/${JOB_ID}/artifacts`)).toHaveLength(0);
  });

  test("a finished job with no job.log artifact: reason artifact-policy", async () => {
    routeJob(stub, jobDetail(), [artifactRow({ filename: "bot.log", artifact_type: "other" })]);
    const [entry] = (await apexGetJobs({ ...args, includeLogDigest: true })).entries;
    expect(entry?.logsUnavailable?.reason).toBe("artifact-policy");
    expect(entry?.logDigest).toBeUndefined();
  });

  test("a failing log download is logDigestError and the job still returns", async () => {
    routeJob(stub, jobDetail());
    stub.route("GET", artPath(), () => json({ detail: "forbidden" }, 403));
    const [entry] = (await apexGetJobs({ ...args, includeLogDigest: true })).entries;
    expect(entry?.found).toBe(true);
    expect(entry?.logDigestError).toContain("forbidden");
    expect(entry?.logDigest).toBeUndefined();
  });

  test("progress is reported once per job", async () => {
    stub.route("GET", "/jobs/", () => json(jobDetail()));
    const seen: number[] = [];
    await apexGetJobs({ ...args, jobIds: ["j1", "j2", "j3"], batch: true }, (done) =>
      seen.push(done),
    );
    expect(seen.sort()).toEqual([1, 2, 3]);
  });
});

describe("apexGetJobLogs", () => {
  const stub = useApexStub();
  const base = {
    env: "pre_prod" as const,
    jobIds: [JOB_ID],
    batch: false,
    filter: { onlyFailures: false },
    fullMessages: false,
    includeRawFields: false,
    includeVideo: false,
  };

  test("returns every row of the whole log, truncating messages unless fullMessages", async () => {
    routeJob(stub, jobDetail({ status: "failed" }));
    const [entry] = (await apexGetJobLogs(base)).entries;
    expect(entry).toMatchObject({ jobId: JOB_ID, returned: 7, totalMatching: 7, truncated: false });
    const long = entry?.logs.find((l) => l.Message.startsWith("xxx"));
    expect(long?.Message).toHaveLength(401);
    const full = (await apexGetJobLogs({ ...base, fullMessages: true })).entries[0];
    expect(full?.logs.find((l) => l.Message.startsWith("xxx"))?.Message).toHaveLength(500);
  });

  test("minLevel, contains and tail narrow client-side; totalMatching is the pre-tail count", async () => {
    routeJob(stub, jobDetail({ status: "failed" }));
    const warn = (
      await apexGetJobLogs({ ...base, filter: { onlyFailures: false, minLevel: "warn" } })
    ).entries[0];
    expect(warn?.logs.map((l) => l.Level)).toEqual(["Warn", "Error"]);
    const err = (
      await apexGetJobLogs({ ...base, filter: { onlyFailures: false, minLevel: "error" } })
    ).entries[0];
    expect(err?.logs.map((l) => l.Message)).toEqual(["row not found"]);
    const tail = (
      await apexGetJobLogs({ ...base, filter: { onlyFailures: false, minLevel: "warn", tail: 1 } })
    ).entries[0];
    expect(tail).toMatchObject({ returned: 1, totalMatching: 2 });
    const has = (
      await apexGetJobLogs({ ...base, filter: { onlyFailures: false, contains: "slow" } })
    ).entries[0];
    expect(has?.logs.map((l) => l.Message)).toEqual(["slow page"]);
  });

  test("onlyFailures keeps error rows and failure wording (adapter result, traceback)", async () => {
    routeJob(stub, jobDetail({ status: "failed" }));
    const out = (await apexGetJobLogs({ ...base, filter: { onlyFailures: true } })).entries[0];
    const msgs = out?.logs.map((l) => l.Message) ?? [];
    expect(msgs).toContain("row not found");
    expect(msgs.some((m) => m.includes("result=FAILED"))).toBe(true);
    expect(msgs).not.toContain("login ok");
  });

  test("includeRawFields returns the NDJSON fields (null when none)", async () => {
    routeJob(stub, jobDetail({ status: "failed" }));
    const out = (await apexGetJobLogs({ ...base, includeRawFields: true })).entries[0];
    expect(out?.logs.find((l) => l.Message === "slow page")?.raw).toEqual({ ms: 5200 });
    expect(out?.logs.find((l) => l.Message === "login ok")?.raw).toBeNull();
  });

  test("includeVideo points at the recording artifact; absent => videoError", async () => {
    const video = artifactRow({
      id: "v1",
      filename: "recording.mp4",
      artifact_type: "videos",
      size_bytes: 77,
    });
    routeJob(stub, jobDetail({ status: "failed" }), [ART, video]);
    const withVideo = (await apexGetJobLogs({ ...base, includeVideo: true })).entries[0];
    expect(withVideo?.video).toEqual({
      kind: "artifact",
      artifact: "recording.mp4",
      sizeBytes: 77,
      fetchWith: "get_job_artifacts",
    });
    routeJob(stub, jobDetail({ status: "failed" }));
    const without = (await apexGetJobLogs({ ...base, includeVideo: true })).entries[0];
    expect(without?.video).toBeUndefined();
    expect(without?.videoError).toMatch(/no video/);
  });

  test("a running job returns no rows, reason running, and explains the video too", async () => {
    stub.route("GET", `/jobs/${JOB_ID}`, () =>
      json(jobDetail({ status: "running", completed_at: null })),
    );
    const out = (await apexGetJobLogs({ ...base, includeVideo: true })).entries[0];
    expect(out).toMatchObject({ logs: [], returned: 0, logsUnavailable: { reason: "running" } });
    expect(out?.videoError).toBeTruthy();
  });

  test("a missing job throws for a single id, lands on the entry in a batch", async () => {
    stub.route("GET", `/jobs/${JOB_ID}`, () => json({ detail: "Job not found" }, 404));
    await expect(apexGetJobLogs(base)).rejects.toThrow(/not found/i);
    const out = await apexGetJobLogs({ ...base, batch: true });
    expect(out.entries[0]).toMatchObject({
      jobId: JOB_ID,
      logs: [],
      error: expect.stringContaining("not found"),
    });
  });
});

describe("queues, processes, triggers", () => {
  const stub = useApexStub();

  test("listQueues filters by name (case-insensitive), keeps inactive queues, cuts to top", async () => {
    stub.route("GET", "/queues", () =>
      page([
        queueRow({ id: "q1", name: "PMG auth submit queue" }),
        queueRow({ id: "q2", name: "CenCal shared-adapter test queue", is_active: false }),
        queueRow({ id: "q3", name: "pmg auth sync queue" }),
      ]),
    );
    const all = await apexListQueues({ env: "pre_prod", top: 10, nameContains: "PMG" });
    expect(all.map((q) => q.id)).toEqual(["q1", "q3"]);
    expect(
      (await apexListQueues({ env: "pre_prod", top: 10, nameContains: "test" }))[0]?.active,
    ).toBe(false);
    expect(await apexListQueues({ env: "pre_prod", top: 1 })).toHaveLength(1);
  });

  test("listProcesses filters workflows by name and renames fields", async () => {
    stub.route("GET", "/workflows", () =>
      page([workflowRow({ name: "demo-auth-submit" }), workflowRow({ id: "w2", name: "other" })]),
    );
    const out = await apexListProcesses({ env: "pre_prod", top: 10, nameContains: "DEMO" });
    expect(out).toHaveLength(1);
    expect(out[0]).toMatchObject({ package: "demo-auth-submit", version: "1.0.2" });
  });

  test("listTriggers: queues as queue triggers plus schedules as time triggers", async () => {
    stub.route("GET", "/queues", () =>
      page([queueRow({ id: "q1" }), queueRow({ id: "q2", auto_drain: false })]),
    );
    stub.route("GET", "/schedules", () => page([scheduleRow()]));
    const out = await apexListTriggers({ env: "pre_prod", top: 10 });
    expect(out.map((t) => [t.kind, t.enabled])).toEqual([
      ["queue", true],
      ["queue", false],
      ["time", true],
    ]);
  });

  test("a queueId filter keeps that queue's trigger only and skips schedules", async () => {
    stub.route("GET", "/queues", () => page([queueRow({ id: "q1" }), queueRow({ id: "q2" })]));
    stub.route("GET", "/schedules", () => page([scheduleRow()]));
    const out = await apexListTriggers({ env: "pre_prod", top: 10, queueId: "q2" });
    expect(out.map((t) => t.id)).toEqual(["q2"]);
    expect(stub.callsTo("GET", "/schedules")).toHaveLength(0);
  });
});

describe("apexListQueueItems", () => {
  const stub = useApexStub();
  const queues = [
    queueRow({ id: "q-pmg", name: "PMG auth submit queue" }),
    queueRow({ id: "q-pmg-sync", name: "PMG auth sync queue" }),
  ];

  test("resolves the portal's queue, maps the status to its wire value, and omits content", async () => {
    stub.route("GET", "/queues?", () => page(queues));
    stub.route("GET", "/queues/q-pmg/items", () => page([itemRow({ status: "in_progress" })]));
    const out = await apexListQueueItems({
      env: "pre_prod",
      queue: "pmg",
      queueKind: "submit",
      status: "InProgress",
      top: 5,
    });
    expect(query(stub.callsTo("GET", "/queues/q-pmg/items")[0])["status"]).toBe("in_progress");
    expect(out).toMatchObject({ queueId: "q-pmg", queueName: "PMG auth submit queue" });
    expect(out.items[0]).toMatchObject({
      status: "InProgress",
      rawStatus: "in_progress",
      specificContent: null,
      jobs: [{ id: JOB_ID, state: null }],
    });
  });

  test("queueKind sync picks the sync queue; no status sends no status param", async () => {
    stub.route("GET", "/queues?", () => page(queues));
    stub.route("GET", "/queues/q-pmg-sync/items", () => page([]));
    await apexListQueueItems({ env: "pre_prod", queue: "PMG", queueKind: "sync", top: 5 });
    const call = stub.callsTo("GET", "/queues/q-pmg-sync/items")[0];
    expect("status" in query(call)).toBe(false);
  });

  test("an unknown queue is an ExpectedError listing what exists", async () => {
    stub.route("GET", "/queues?", () => page(queues));
    const err = await apexListQueueItems({
      env: "pre_prod",
      queue: "nope",
      queueKind: "submit",
      top: 5,
    }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ExpectedError);
    expect((err as Error).message).toContain("PMG auth submit queue");
  });
});

describe("apexFindOrderQueueItems", () => {
  const stub = useApexStub();
  const args = {
    env: "pre_prod" as const,
    orderUid: "order-1",
    top: 10,
    includeSpecificContent: true,
  };
  const exact = itemRow({ id: "i-new", reference: "R2", created_at: "2026-10-08T12:00:00Z" });
  const older = itemRow({ id: "i-old", reference: "R1", created_at: "2026-10-07T12:00:00Z" });
  const fuzzy = itemRow({
    id: "i-fuzzy",
    reference: "R9",
    specific_content: { orderUid: "order-10" },
  });

  function routeFind(queueCount = 2): void {
    const queues = Array.from({ length: queueCount }, (_, i) =>
      queueRow({ id: `q${i}`, name: i === 0 ? "PMG auth submit queue" : `Q${i}` }),
    );
    stub.route("GET", "/queues?", () => page(queues));
    stub.route("GET", "/queues/", () => page([])); // every other queue has no hits
    stub.route("GET", "/queues/q0/items", () => page([exact, fuzzy, older]));
    stub.route("GET", "/jobs", (c) => {
      const ref = query(c)["reference"];
      return page(
        ref === "R2"
          ? [
              jobRow({
                id: "j-retry",
                queue_item_id: "i-new",
                attempt_number: 2,
                status: "failed",
              }),
              jobRow({
                id: "j-first",
                queue_item_id: "i-new",
                attempt_number: 1,
                status: "cancelled",
              }),
              jobRow({ id: "j-other-item", queue_item_id: "someone-else" }),
            ]
          : [jobRow({ id: "j-old", queue_item_id: "i-old" })],
      );
    });
  }

  test("searches every queue, keeps only the exact orderUid, newest first, token redacted", async () => {
    routeFind(8);
    const out = await apexFindOrderQueueItems(args);
    for (let i = 0; i < 8; i++) {
      expect(query(stub.callsTo("GET", `/queues/q${i}/items`)[0])["search"]).toBe("order-1");
    }
    expect(out.items.map((i) => i.itemId)).toEqual(["i-new", "i-old"]);
    expect(out.items[0]?.specificContent?.["token"]).toBe("[redacted]");
    expect(out).toMatchObject({
      searchMode: "search",
      scanned: 3,
      notes: [],
      folder: "Authorization Test",
    });
    expect(out.items[0]?.queueName).toBe("PMG auth submit queue");
  });

  test("jobs[] are the item's own attempts from /jobs?reference, newest first", async () => {
    routeFind();
    const out = await apexFindOrderQueueItems(args);
    expect(out.items[0]?.jobs.map((j) => [j.id, j.state])).toEqual([
      ["j-retry", "Faulted"],
      ["j-first", "Stopped"],
    ]);
  });

  test("falls back to the item's latest job id when the reference listing has none of its jobs", async () => {
    routeFind();
    stub.route("GET", "/jobs", () => page([]));
    const out = await apexFindOrderQueueItems(args);
    expect(out.items[0]?.jobs).toEqual([
      { orchestrator: "apex", id: JOB_ID, state: null, neverStarted: null },
    ]);
  });

  test("includeSpecificContent=false drops the content", async () => {
    routeFind();
    const out = await apexFindOrderQueueItems({ ...args, includeSpecificContent: false });
    expect(out.items.every((i) => i.specificContent === null)).toBe(true);
  });

  test("top keeps the newest matches", async () => {
    routeFind();
    const out = await apexFindOrderQueueItems({ ...args, top: 1 });
    expect(out.items.map((i) => i.itemId)).toEqual(["i-new"]);
  });

  test("since bounds created_at; an empty window returns everything with a note", async () => {
    routeFind();
    const bounded = await apexFindOrderQueueItems({ ...args, since: "2026-10-08T00:00:00Z" });
    expect(bounded.items.map((i) => i.itemId)).toEqual(["i-new"]);
    expect(bounded.notes).toEqual([]);
    const empty = await apexFindOrderQueueItems({ ...args, since: "2026-11-01T00:00:00Z" });
    expect(empty.items).toHaveLength(2);
    expect(empty.notes[0]).toContain("without the time bound");
  });

  test("no exact match: empty items, scanned still counts the fuzzy rows, no job lookups", async () => {
    routeFind();
    const out = await apexFindOrderQueueItems({ ...args, orderUid: "order-1x" });
    expect(out).toMatchObject({ items: [], scanned: 3 });
    expect(stub.callsTo("GET", "/jobs")).toHaveLength(0);
  });
});

describe("apexPullQueueItem", () => {
  const stub = useApexStub();
  const args = { env: "pre_prod" as const, queueId: QUEUE_ID, itemId: ITEM_ID };

  test("forces IsApproved false, keeps the token, resolves the portal and a filename", async () => {
    stub.route("GET", `/queues/${QUEUE_ID}`, () =>
      json(queueRow({ name: "PMG auth submit queue" })),
    );
    stub.route("GET", `/queues/${QUEUE_ID}/items/${ITEM_ID}`, () =>
      json(itemDetail({ status: "failed" })),
    );
    const out = await apexPullQueueItem(args);
    expect(out).toMatchObject({
      env: "pre_prod",
      isApprovedForced: true,
      suggestedFilename: "jane-doe.yaml",
      portal: { matched: true, key: "PMG" },
      item: { status: "Failed", queueName: "PMG auth submit queue", orderUid: "order-1" },
    });
    expect(out.item.specificContent?.["IsApproved"]).toBe(false);
    expect(out.item.specificContent?.["token"]).toBe("synthetic-token");
    expect(out.queueNameError).toBeUndefined();
  });

  test("a failed queue-name lookup is queueNameError, not a failed pull", async () => {
    stub.route("GET", `/queues/${QUEUE_ID}`, () => json({ detail: "kaput" }, 500));
    stub.route("GET", `/queues/${QUEUE_ID}/items/${ITEM_ID}`, () => json(itemDetail()));
    const out = await apexPullQueueItem(args);
    expect(out.queueNameError).toContain("kaput");
    expect(out.portal.matched).toBe(false);
    expect(out.item.itemId).toBe(ITEM_ID);
  });

  test("requires env + queueId + itemId and rejects UiPath-only args", async () => {
    await expect(apexPullQueueItem({ env: "pre_prod", itemId: ITEM_ID })).rejects.toBeInstanceOf(
      ExpectedError,
    );
    await expect(apexPullQueueItem({ queueId: QUEUE_ID, itemId: ITEM_ID })).rejects.toBeInstanceOf(
      ExpectedError,
    );
    await expect(apexPullQueueItem({ ...args, url: "https://x/y" })).rejects.toBeInstanceOf(
      ExpectedError,
    );
    expect(stub.calls.filter((c) => c.path.startsWith("/queues"))).toHaveLength(0);
  });
});

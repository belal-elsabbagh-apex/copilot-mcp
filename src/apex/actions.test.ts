import { describe, expect, test } from "bun:test";
import { ExpectedError } from "../shared/expected-error.js";
import { addApexQueueItem, deleteApexQueueItem, startApexJob, stopApexJob } from "./actions.js";
import { APEX_URL, type ApexCall, FOLDER_IDS, json, useApexStub } from "./test-support.js";

// useApexStub's default apex config: serverUrlByEnv.pre_prod + queueUrl, default folders.
const SERVER_URL = "https://be.pre.example";
const QUEUE_URL = "https://sqs.example/q";
const TEST_FOLDER_ID = FOLDER_IDS["Authorization Test"] as string;
const QUEUE_ID = "aaaaaaaa-1111-4111-8111-111111111111";
const ITEM_ID = "bbbbbbbb-2222-4222-8222-222222222222";
const JOB_ID = "cccccccc-3333-4333-8333-333333333333";
const WORKFLOW_ID = "dddddddd-4444-4444-8444-444444444444";

const paged = (items: unknown[]): Response =>
  json({ items, total: items.length, page: 1, page_size: 200, total_pages: 1 });

// Everything except login + folder resolution: what the action itself did.
const actionCalls = (calls: ApexCall[]): ApexCall[] =>
  calls.filter((c) => !c.path.startsWith("/auth/") && !c.path.startsWith("/folders"));
const writes = (calls: ApexCall[]): ApexCall[] =>
  actionCalls(calls).filter((c) => c.method !== "GET");
const sequence = (calls: ApexCall[]): string[] =>
  actionCalls(calls).map((c) => `${c.method} ${c.path.split("?")[0]}`);

const QUEUE = {
  id: QUEUE_ID,
  folder_id: TEST_FOLDER_ID,
  name: "CENCAL auth submit queue",
  workflow_id: WORKFLOW_ID,
  is_active: false,
};

// ---- addApexQueueItem -------------------------------------------------------

describe("addApexQueueItem", () => {
  const apex = useApexStub();
  const args = {
    env: "pre_prod" as const,
    queue: "cencal auth submit queue",
    reference: "ord-1-1700000000",
    priority: "Normal" as const,
    specificContent: {
      orderUid: "ord-1",
      automationId: "auto-1",
      IsApproved: true,
      serverURL: SERVER_URL,
      queueUrl: QUEUE_URL,
      Units: ["1"],
      callbackContext: { physicianId: 7 },
    },
  };
  const stubQueue = (): void => {
    apex.route("GET", "/queues?", () => paged([QUEUE]));
    apex.route("POST", `/queues/${QUEUE_ID}/items`, () =>
      json({ id: ITEM_ID, status: "new" }, 201),
    );
  };

  test("refuses env=prod in the domain, before any fetch", async () => {
    await expect(addApexQueueItem({ ...args, env: "prod" })).rejects.toThrow(/pre_prod-only/);
    expect(apex.calls.length).toBe(0);
  });

  test.each([
    [
      "a serverURL that is not the configured pre-prod host",
      { serverURL: "https://prod-be.example.com" },
      /serverURL/,
    ],
    [
      "a queueUrl that is not the configured one",
      { queueUrl: "https://sqs.example/other" },
      /queueUrl/,
    ],
    ["a <TO-FILL> placeholder", { Facility: "<TO-FILL>" }, /placeholder/],
  ])("guard violation blocks everything: %s", async (_name, patch, message) => {
    await expect(
      addApexQueueItem({ ...args, specificContent: { ...args.specificContent, ...patch } }),
    ).rejects.toThrow(message);
    expect(apex.calls.length).toBe(0);
  });

  test.each([
    {},
    { automationId: "" },
  ])("refuses content without a usable automationId (%j) before any fetch", async (patch) => {
    const { automationId: _drop, ...rest } = args.specificContent;
    await expect(
      addApexQueueItem({ ...args, specificContent: { ...rest, ...patch } }),
    ).rejects.toThrow(ExpectedError);
    expect(apex.calls.length).toBe(0);
  });

  test("posts the guarded native content with match_value = automationId, in the test folder", async () => {
    stubQueue();
    const out = await addApexQueueItem(args);
    expect(writes(apex.calls).length).toBe(1);
    const post = writes(apex.calls)[0];
    expect(post?.path).toBe(`/queues/${QUEUE_ID}/items`);
    expect(post?.headers["x-folder-id"]).toBe(TEST_FOLDER_ID);
    expect(post?.body).toEqual({
      specific_content: { ...args.specificContent, IsApproved: false },
      reference: "ord-1-1700000000",
      match_value: "auto-1",
    });
    expect(out).toEqual({
      env: "pre_prod",
      queueId: QUEUE_ID,
      queueName: "CENCAL auth submit queue",
      reference: "ord-1-1700000000",
      itemId: ITEM_ID,
      status: "New",
      forced: ["IsApproved"],
    });
  });

  test("resolves the queue by uuid, case-insensitively", async () => {
    stubQueue();
    const out = await addApexQueueItem({ ...args, queue: QUEUE_ID.toUpperCase() });
    expect(out.queueId).toBe(QUEUE_ID);
  });

  test("an unknown queue posts nothing", async () => {
    apex.route("GET", "/queues?", () => paged([QUEUE]));
    await expect(addApexQueueItem({ ...args, queue: "Nope" })).rejects.toThrow(/no queue/);
    expect(writes(apex.calls).length).toBe(0);
  });

  test("a name matching two queues (case differs) is refused, not guessed", async () => {
    apex.route("GET", "/queues?", () =>
      paged([
        QUEUE,
        { ...QUEUE, id: "eeeeeeee-5555-4555-8555-555555555555", name: "Cencal Auth Submit Queue" },
      ]),
    );
    await expect(addApexQueueItem(args)).rejects.toThrow(/matches 2 queues/);
    expect(writes(apex.calls).length).toBe(0);
  });
});

describe("addApexQueueItem without apex.serverUrlByEnv.pre_prod", () => {
  const apex = useApexStub({
    apex: { apiUrl: APEX_URL, username: "tester", password: "pw", queueUrl: QUEUE_URL },
  });

  test("fails closed, naming the apex config key, before any fetch", async () => {
    await expect(
      addApexQueueItem({
        env: "pre_prod",
        queue: "q",
        reference: "r",
        priority: "Normal",
        specificContent: { automationId: "auto-1", serverURL: SERVER_URL },
      }),
    ).rejects.toThrow(/apex\.serverUrlByEnv\.pre_prod/);
    expect(apex.calls.length).toBe(0);
  });
});

// ---- deleteApexQueueItem ----------------------------------------------------

describe("deleteApexQueueItem", () => {
  const apex = useApexStub();
  const itemPath = `/queues/${QUEUE_ID}/items/${ITEM_ID}`;

  test("refuses env=prod in the domain, before any fetch", async () => {
    await expect(
      deleteApexQueueItem({ env: "prod", itemId: ITEM_ID, queueId: QUEUE_ID }),
    ).rejects.toThrow(/pre_prod-only/);
    expect(apex.calls.length).toBe(0);
  });

  test("queueId is required, before any fetch", async () => {
    await expect(deleteApexQueueItem({ env: "pre_prod", itemId: ITEM_ID })).rejects.toThrow(
      /queueId is required/,
    );
    expect(apex.calls.length).toBe(0);
  });

  test.each([
    ["in_progress", "InProgress"],
    ["successful", "Successful"],
    ["failed", "Failed"],
    ["retried", "Retried"],
    ["abandoned", "Abandoned"],
    ["deleted", "Deleted"],
  ])("refuses a '%s' item (names the status) and posts nothing", async (status, label) => {
    apex.route("GET", itemPath, () => json({ id: ITEM_ID, status, reference: "ref-1" }));
    const err = await deleteApexQueueItem({
      env: "pre_prod",
      itemId: ITEM_ID,
      queueId: QUEUE_ID,
    }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ExpectedError);
    expect((err as Error).message).toContain(label);
    expect(writes(apex.calls).length).toBe(0);
  });

  test("deletes a 'new' item: fetch first, then POST :delete {}", async () => {
    apex.route("GET", itemPath, () => json({ id: ITEM_ID, status: "new", reference: "ref-1" }));
    apex.route("POST", `${itemPath}:delete`, () => json({}));
    const out = await deleteApexQueueItem({ env: "pre_prod", itemId: ITEM_ID, queueId: QUEUE_ID });
    expect(out).toEqual({
      env: "pre_prod",
      itemId: ITEM_ID,
      deleted: true,
      previousStatus: "New",
      reference: "ref-1",
    });
    expect(sequence(apex.calls)).toEqual([`GET ${itemPath}`, `POST ${itemPath}:delete`]);
    expect(writes(apex.calls)[0]?.body).toEqual({});
    expect(writes(apex.calls)[0]?.headers["x-folder-id"]).toBe(TEST_FOLDER_ID);
  });

  test("an item that cannot be fetched is never deleted", async () => {
    // no route stubbed -> 404 on the fetch-first GET
    await expect(
      deleteApexQueueItem({ env: "pre_prod", itemId: ITEM_ID, queueId: QUEUE_ID }),
    ).rejects.toThrow();
    expect(writes(apex.calls).length).toBe(0);
  });
});

// ---- startApexJob -----------------------------------------------------------

describe("startApexJob", () => {
  const apex = useApexStub();
  const args = {
    env: "pre_prod" as const,
    processId: WORKFLOW_ID,
    input: { dryRun: true },
    jobsCount: 3,
    matchValue: "auto-1",
  };
  const stubWorkflows = (): void =>
    apex.route("GET", "/workflows?", () =>
      paged([{ id: WORKFLOW_ID, name: "apex-auth-submit-cencal" }]),
    );

  test("refuses env=prod in the domain, before any fetch", async () => {
    await expect(startApexJob({ ...args, env: "prod" })).rejects.toThrow(/pre_prod-only/);
    expect(apex.calls.length).toBe(0);
  });

  test.each([
    undefined,
    "",
    "  ",
  ])("matchValue is required (%j), before any fetch", async (matchValue) => {
    const { matchValue: _drop, ...rest } = args;
    await expect(
      startApexJob(matchValue === undefined ? rest : { ...rest, matchValue }),
    ).rejects.toThrow(/matchValue is required/);
    expect(apex.calls.length).toBe(0);
  });

  test("an unknown Process posts nothing", async () => {
    apex.route("GET", "/workflows?", () => paged([]));
    await expect(startApexJob(args)).rejects.toThrow(/no Process/);
    expect(writes(apex.calls).length).toBe(0);
  });

  test("posts one sequential job per jobsCount with the documented body", async () => {
    stubWorkflows();
    let n = 0;
    apex.route("POST", "/jobs", () => {
      n += 1;
      return json({ id: `aaaaaaaa-0000-4000-8000-00000000000${n}`, status: "queued" }, 201);
    });
    const out = await startApexJob(args);
    expect(writes(apex.calls).length).toBe(3);
    for (const w of writes(apex.calls)) {
      expect(w.path).toBe("/jobs");
      expect(w.headers["x-folder-id"]).toBe(TEST_FOLDER_ID);
      expect(w.body).toEqual({
        workflow_id: WORKFLOW_ID,
        folder_id: TEST_FOLDER_ID,
        match_value: "auto-1",
        input_payload: { dryRun: true },
        bucket_id: null,
      });
    }
    expect(out.env).toBe("pre_prod");
    expect(out.jobs.map((j) => j.id)).toEqual([
      "aaaaaaaa-0000-4000-8000-000000000001",
      "aaaaaaaa-0000-4000-8000-000000000002",
      "aaaaaaaa-0000-4000-8000-000000000003",
    ]);
    expect(out.jobs[0]).toEqual({
      id: "aaaaaaaa-0000-4000-8000-000000000001",
      state: "Pending",
      processName: "apex-auth-submit-cencal",
      link: `${APEX_URL}/jobs/aaaaaaaa-0000-4000-8000-000000000001`,
    });
  });

  test("a failed POST is never retried and the already-started jobs are reported", async () => {
    stubWorkflows();
    let n = 0;
    apex.route("POST", "/jobs", () => {
      n += 1;
      return n === 1 ? json({ id: JOB_ID, status: "queued" }, 201) : json({ detail: "boom" }, 500);
    });
    const err = await startApexJob(args).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ExpectedError);
    expect((err as Error).message).toContain(JOB_ID);
    expect((err as Error).message).toContain("boom");
    expect(writes(apex.calls).length).toBe(2); // 1 ok + 1 failed: no retry, third never attempted
  });

  test("a failure of the first POST is not retried either", async () => {
    stubWorkflows();
    apex.route("POST", "/jobs", () => json({ detail: "boom" }, 500));
    await expect(startApexJob(args)).rejects.toThrow(/boom/);
    expect(writes(apex.calls).length).toBe(1);
  });
});

// ---- stopApexJob ------------------------------------------------------------

describe("stopApexJob", () => {
  const apex = useApexStub();
  const jobPath = `/jobs/${JOB_ID}`;
  const stop = (strategy: "SoftStop" | "Kill") =>
    stopApexJob({ env: "pre_prod", jobId: JOB_ID, strategy });

  test("refuses env=prod in the domain, before any fetch", async () => {
    await expect(stopApexJob({ env: "prod", jobId: JOB_ID, strategy: "Kill" })).rejects.toThrow(
      /pre_prod-only/,
    );
    expect(apex.calls.length).toBe(0);
  });

  test.each([
    ["completed", "Successful"],
    ["failed", "Faulted"],
    ["cancelled", "Stopped"],
    ["lost", "Lost"],
    ["timed_out", "Timed Out"],
  ])("refuses a terminal '%s' job (names the state) and posts nothing", async (status, label) => {
    apex.route("GET", jobPath, () => json({ id: JOB_ID, status }));
    const err = await stop("Kill").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ExpectedError);
    expect((err as Error).message).toContain(label);
    expect(writes(apex.calls).length).toBe(0);
  });

  test("SoftStop on a running job posts cancel {force:false}", async () => {
    apex.route("GET", jobPath, () => json({ id: JOB_ID, status: "running" }));
    apex.route("POST", `${jobPath}/cancel`, () => json({}));
    const out = await stop("SoftStop");
    expect(out).toEqual({ env: "pre_prod", jobId: JOB_ID, strategy: "SoftStop", stopped: true });
    expect(sequence(apex.calls)).toEqual([`GET ${jobPath}`, `POST ${jobPath}/cancel`]);
    expect(writes(apex.calls)[0]?.body).toEqual({ force: false });
    expect(writes(apex.calls)[0]?.headers["x-folder-id"]).toBe(TEST_FOLDER_ID);
  });

  test("Kill posts force_cancel with an empty body", async () => {
    apex.route("GET", jobPath, () => json({ id: JOB_ID, status: "cancelling" }));
    apex.route("POST", `${jobPath}/force_cancel`, () => json({}));
    const out = await stop("Kill");
    expect(out.strategy).toBe("Kill");
    expect(writes(apex.calls)[0]?.path).toBe(`${jobPath}/force_cancel`);
    expect(writes(apex.calls)[0]?.body).toEqual({});
  });

  test("a 403 on Kill becomes an ExpectedError that names jobs:force_cancel", async () => {
    apex.route("GET", jobPath, () => json({ id: JOB_ID, status: "running" }));
    apex.route("POST", `${jobPath}/force_cancel`, () => json({ detail: "forbidden" }, 403));
    const err = await stop("Kill").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ExpectedError);
    expect((err as Error).message).toContain("jobs:force_cancel");
    expect(writes(apex.calls).length).toBe(1);
  });

  test("a job that cannot be fetched is never cancelled", async () => {
    await expect(stop("SoftStop")).rejects.toThrow();
    expect(writes(apex.calls).length).toBe(0);
  });
});

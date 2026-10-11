import { describe, expect, test } from "bun:test";
import {
  downloadArtifact,
  getJob,
  isNotFound,
  listArtifacts,
  listJobs,
  listQueueItems,
} from "./api.js";
import { artifactRow, JOB_ID, jobRow } from "./test-rows.js";
import { FOLDER_IDS, json, useApexStub } from "./test-support.js";

const page = (items: unknown[]) =>
  json({ items, total: items.length, page: 1, page_size: 200, total_pages: 1 });
const query = (path: string) => Object.fromEntries(new URL(`https://x${path}`).searchParams);

describe("filters", () => {
  const stub = useApexStub();

  test("listJobs sends only the verified params, snake_cased, newest first by default", async () => {
    stub.route("GET", "/jobs", () => page([jobRow()]));
    await listJobs(
      "pre_prod",
      undefined,
      {
        status: "failed",
        queueId: "q1",
        workflowId: "w1",
        createdAfter: "2026-10-01T00:00:00Z",
        reference: "r1",
      },
      10,
    );
    const q = query(stub.callsTo("GET", "/jobs")[0]?.path ?? "");
    expect(q).toMatchObject({
      sort_by: "created_at",
      sort_dir: "desc",
      status: "failed",
      queue_id: "q1",
      workflow_id: "w1",
      created_after: "2026-10-01T00:00:00Z",
      reference: "r1",
    });
    expect(Object.keys(q).sort()).toEqual([
      "created_after",
      "page",
      "page_size",
      "queue_id",
      "reference",
      "sort_by",
      "sort_dir",
      "status",
      "workflow_id",
    ]);
  });

  test("undefined filters are omitted, not sent as the string 'undefined'", async () => {
    stub.route("GET", "/jobs", () => page([]));
    await listJobs("pre_prod", undefined, { status: undefined, reference: "r1" }, 10);
    const q = query(stub.callsTo("GET", "/jobs")[0]?.path ?? "");
    expect(q["reference"]).toBe("r1");
    expect("status" in q).toBe(false);
  });

  test("listQueueItems maps status / reference / search", async () => {
    stub.route("GET", "/queues/q1/items", () => page([]));
    await listQueueItems(
      "pre_prod",
      undefined,
      "q1",
      { status: "in_progress", search: "uid-1" },
      10,
    );
    const q = query(stub.callsTo("GET", "/queues/q1/items")[0]?.path ?? "");
    expect(q).toMatchObject({ status: "in_progress", search: "uid-1" });
    expect("reference" in q).toBe(false);
  });
});

describe("folder scoping and downloads", () => {
  const stub = useApexStub();

  test("a folder name override scopes the artifact listing (non-default folders 404 without it)", async () => {
    stub.route("GET", `/jobs/${JOB_ID}/artifacts`, () => json([artifactRow()]));
    await listArtifacts("pre_prod", "Authorization", JOB_ID);
    await listArtifacts("pre_prod", undefined, JOB_ID);
    const [override, byEnv] = stub.callsTo("GET", `/jobs/${JOB_ID}/artifacts`);
    expect(override?.headers["x-folder-id"]).toBe(FOLDER_IDS["Authorization"]);
    expect(byEnv?.headers["x-folder-id"]).toBe(FOLDER_IDS["Authorization Test"]);
  });

  test("a non-list artifact response is an error, not an empty list", async () => {
    stub.route("GET", `/jobs/${JOB_ID}/artifacts`, () => json({ items: [] }));
    await expect(listArtifacts("pre_prod", undefined, JOB_ID)).rejects.toThrow(/not a list/);
  });

  test("downloadArtifact forwards the Range header only when given", async () => {
    const a = artifactRow();
    stub.route(
      "GET",
      a.download_url.replace("/api/v1", ""),
      () => new Response("tail", { status: 206 }),
    );
    await downloadArtifact("pre_prod", a, "bytes=-4");
    await downloadArtifact("pre_prod", a);
    const [ranged, whole] = stub.callsTo("GET", a.download_url.replace("/api/v1", ""));
    expect(ranged?.headers["range"]).toBe("bytes=-4");
    expect(whole?.headers["range"]).toBeUndefined();
  });

  test("getJob 404 is a not-found ApexApiError; a 500 is not", async () => {
    stub.route("GET", "/jobs/gone", () => json({ detail: "Job not found" }, 404));
    stub.route("GET", "/jobs/boom", () => json({ detail: "kaput" }, 500));
    const gone = await getJob("pre_prod", undefined, "gone").catch((e: unknown) => e);
    const boom = await getJob("pre_prod", undefined, "boom").catch((e: unknown) => e);
    expect(isNotFound(gone)).toBe(true);
    expect(isNotFound(boom)).toBe(false);
    expect(isNotFound(new Error("x"))).toBe(false);
  });
});

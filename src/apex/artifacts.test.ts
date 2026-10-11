import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { existsSync, mkdtempSync, readFileSync, rmSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { ExpectedError } from "../shared/expected-error.js";
import { defaultArtifactsRoot, downloadJobArtifacts, listJobArtifacts } from "./artifacts.js";
import { artifactRow, JOB_ID } from "./test-rows.js";
import { FOLDER_IDS, json, useApexStub } from "./test-support.js";

const LOG = artifactRow({
  id: "a-log",
  filename: "job.log",
  artifact_type: "logs",
  size_bytes: 11,
});
const TRACE = artifactRow({
  id: "a-trace",
  filename: "trace.zip",
  artifact_type: "other",
  size_bytes: 4,
});
const body = (a: typeof LOG) => (a === LOG ? "log content" : "ZIP!");

describe("artifacts", () => {
  const stub = useApexStub();
  let root: string;

  beforeEach(() => {
    root = mkdtempSync(join(tmpdir(), "copilot-mcp-artifacts-"));
    stub.route("GET", `/jobs/${JOB_ID}/artifacts`, () => json([LOG, TRACE]));
    for (const a of [LOG, TRACE]) {
      stub.route("GET", a.download_url.replace("/api/v1", ""), () => new Response(body(a)));
    }
  });
  afterEach(() => rmSync(root, { recursive: true, force: true }));

  test("lists filename, type and size", async () => {
    expect(await listJobArtifacts("pre_prod", JOB_ID)).toEqual({
      folder: "Authorization Test",
      artifacts: [
        { filename: "job.log", type: "logs", sizeBytes: 11 },
        { filename: "trace.zip", type: "other", sizeBytes: 4 },
      ],
    });
  });

  test("the listing honours a folder override (non-default folders 404 without the header)", async () => {
    await listJobArtifacts("pre_prod", JOB_ID, "Authorization");
    expect(stub.callsTo("GET", `/jobs/${JOB_ID}/artifacts`)[0]?.headers["x-folder-id"]).toBe(
      FOLDER_IDS["Authorization"],
    );
  });

  test("downloads the named files into <root>/<jobId>/ and returns paths and sizes", async () => {
    const out = await downloadJobArtifacts("pre_prod", JOB_ID, ["trace.zip"], root);
    expect(out.dir).toBe(join(root, JOB_ID));
    expect(out.files).toEqual([
      { filename: "trace.zip", path: join(root, JOB_ID, "trace.zip"), sizeBytes: 4 },
    ]);
    expect(readFileSync(join(root, JOB_ID, "trace.zip"), "utf8")).toBe("ZIP!");
    expect(existsSync(join(root, JOB_ID, "job.log"))).toBe(false);
  });

  test("'all' downloads every listed artifact", async () => {
    const out = await downloadJobArtifacts("pre_prod", JOB_ID, "all", root);
    expect(out.files.map((f) => f.filename)).toEqual(["job.log", "trace.zip"]);
    expect(readFileSync(join(root, JOB_ID, "job.log"), "utf8")).toBe("log content");
  });

  test("the directory is private (0700) and files are owner-only (0600)", async () => {
    const out = await downloadJobArtifacts("pre_prod", JOB_ID, ["job.log"], root);
    expect(statSync(out.dir).mode & 0o777).toBe(0o700);
    expect(statSync(root).mode & 0o777).toBe(0o700);
    expect(statSync(join(out.dir, "job.log")).mode & 0o777).toBe(0o600);
  });

  test("an unknown name is an ExpectedError listing what exists, before any download", async () => {
    const err = await downloadJobArtifacts("pre_prod", JOB_ID, ["job.log", "nope.png"], root).catch(
      (e: unknown) => e,
    );
    expect(err).toBeInstanceOf(ExpectedError);
    expect((err as Error).message).toContain("'nope.png'");
    expect((err as Error).message).toContain("job.log, trace.zip");
    expect(stub.callsTo("GET", "/jobs/").some((c) => c.path.includes("/artifacts/"))).toBe(false);
    expect(existsSync(join(root, JOB_ID))).toBe(false);
  });

  test("a job id that is not a plain id is refused (no path traversal)", async () => {
    await expect(downloadJobArtifacts("pre_prod", "../../etc", "all", root)).rejects.toBeInstanceOf(
      ExpectedError,
    );
    expect(stub.callsTo("GET", "/jobs/")).toHaveLength(0);
  });

  test("a server-supplied filename containing a path is refused", async () => {
    const evil = artifactRow({ id: "a-evil", filename: "../escape.txt" });
    stub.route("GET", `/jobs/${JOB_ID}/artifacts`, () => json([evil]));
    await expect(downloadJobArtifacts("pre_prod", JOB_ID, "all", root)).rejects.toThrow(
      /path in its name/,
    );
    expect(existsSync(join(root, "escape.txt"))).toBe(false);
  });

  test("the default root is ~/.cache/copilot-mcp/artifacts", () => {
    expect(defaultArtifactsRoot().endsWith(join(".cache", "copilot-mcp", "artifacts"))).toBe(true);
  });
});

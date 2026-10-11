import { describe, expect, test } from "bun:test";
import type { ApexLogRow } from "./job-log.js";
import {
  filterLogRows,
  logsUnavailableBeforeFetch,
  mergeNewestFirst,
  pickQueue,
  toLogRow,
  videoPart,
  withinSince,
} from "./read-support.js";
import { artifactRow, jobRow, queueRow } from "./test-rows.js";

const row = (Level: string, Message: string, extra: Partial<ApexLogRow> = {}): ApexLogRow => ({
  Level,
  Message,
  TimeStamp: "t",
  kind: "ndjson",
  ...extra,
});

describe("pickQueue", () => {
  const queues = [
    queueRow({ id: "q-pmg", name: "PMG auth submit queue" }),
    queueRow({ id: "q-pmg-sync", name: "PMG auth sync queue" }),
    queueRow({ id: "q-spmg", name: "Spmg auth submit queue" }),
    queueRow({ id: "q-test", name: "CenCal shared-adapter test queue", is_active: false }),
    queueRow({ id: "q-unrouted", name: "Unrouted" }),
  ];

  test("portal key, case-insensitive, picks the submit or sync queue by kind", () => {
    expect(pickQueue(queues, "pmg", "submit")?.id).toBe("q-pmg");
    expect(pickQueue(queues, "PMG", "sync")?.id).toBe("q-pmg-sync");
  });

  test("a portal alias resolves to the key's queue", () => {
    expect(pickQueue(queues, "sdpmg", "submit")?.id).toBe("q-spmg");
  });

  test("a queue id or a full queue name matches directly", () => {
    expect(pickQueue(queues, "q-unrouted", "submit")?.id).toBe("q-unrouted");
    expect(pickQueue(queues, "cencal shared-adapter test queue", "submit")?.id).toBe("q-test");
  });

  test("an unknown portal, or a kind with no queue, is null", () => {
    expect(pickQueue(queues, "nope", "submit")).toBeNull();
    expect(pickQueue(queues, "spmg", "sync")).toBeNull();
  });

  test("prefers an active queue when the name repeats", () => {
    const dup = [
      queueRow({ id: "old", name: "PMG auth submit queue", is_active: false }),
      queueRow({ id: "new", name: "Pmg auth submit queue" }),
    ];
    expect(pickQueue(dup, "PMG", "submit")?.id).toBe("new");
  });
});

describe("filterLogRows", () => {
  const rows = [
    row("Info", "login ok"),
    row("Warn", "slow page"),
    row("Error", "row not found"),
    row("Debug", "dom dump"),
    row("Info", "request failed with 500"),
    row("Fatal", "kaput"),
  ];

  test("minLevel warn keeps Warn and above; error keeps Error and above", () => {
    expect(
      filterLogRows(rows, { minLevel: "warn", onlyFailures: false }).kept.map((r) => r.Level),
    ).toEqual(["Warn", "Error", "Fatal"]);
    expect(
      filterLogRows(rows, { minLevel: "error", onlyFailures: false }).kept.map((r) => r.Level),
    ).toEqual(["Error", "Fatal"]);
  });

  test("contains is case-sensitive", () => {
    expect(filterLogRows(rows, { contains: "page", onlyFailures: false }).kept).toHaveLength(1);
    expect(filterLogRows(rows, { contains: "PAGE", onlyFailures: false }).kept).toHaveLength(0);
  });

  test("onlyFailures catches failure wording at Info level", () => {
    const kept = filterLogRows(rows, { onlyFailures: true }).kept.map((r) => r.Message);
    expect(kept).toEqual(["row not found", "request failed with 500", "kaput"]);
  });

  test("tail keeps the last N matches but totalMatching counts them all", () => {
    const out = filterLogRows(rows, { minLevel: "warn", onlyFailures: false, tail: 2 });
    expect(out.kept.map((r) => r.Message)).toEqual(["row not found", "kaput"]);
    expect(out.totalMatching).toBe(3);
  });
});

describe("toLogRow", () => {
  const r = row("Info", "x".repeat(500), { fields: { ms: 1 } });

  test("truncates unless fullMessages; raw only when requested", () => {
    expect(toLogRow(r, false, false).Message).toHaveLength(401);
    expect(toLogRow(r, true, false).Message).toHaveLength(500);
    expect("raw" in toLogRow(r, false, false)).toBe(false);
  });

  test("raw is the NDJSON fields, or null when the row had none", () => {
    expect(toLogRow(r, false, true).raw).toEqual({ ms: 1 });
    expect(toLogRow(row("Info", "m"), false, true).raw).toBeNull();
  });
});

describe("logsUnavailableBeforeFetch", () => {
  test("a job in flight => running", () => {
    for (const status of ["queued", "claimed", "running", "cancelling", "suspended", "resumed"]) {
      expect(logsUnavailableBeforeFetch(jobRow({ status, started_at: null }))?.reason).toBe(
        "running",
      );
    }
  });

  test("terminal with no start => never-started (running wins for in-flight ones)", () => {
    const j = jobRow({ status: "failed", started_at: null, error_class: "missing_credential" });
    const out = logsUnavailableBeforeFetch(j);
    expect(out?.reason).toBe("never-started");
    expect(out?.detail).toContain("missing_credential");
  });

  test("terminal and started => nothing known up front", () => {
    expect(logsUnavailableBeforeFetch(jobRow({ status: "failed" }))).toBeNull();
    expect(logsUnavailableBeforeFetch(jobRow({ status: "timed_out" }))).toBeNull();
  });
});

describe("videoPart", () => {
  test("points at the videos artifact by filename", () => {
    const v = artifactRow({ filename: "recording.mp4", artifact_type: "videos", size_bytes: 9 });
    expect(videoPart([artifactRow(), v])).toEqual({
      video: {
        kind: "artifact",
        artifact: "recording.mp4",
        sizeBytes: 9,
        fetchWith: "get_job_artifacts",
      },
    });
  });

  test("no video => videoError", () => {
    expect(videoPart([artifactRow()]).videoError).toMatch(/no video/);
  });
});

describe("mergeNewestFirst / withinSince", () => {
  test("merges lists newest first and cuts to top", () => {
    const a = jobRow({ id: "a", created_at: "2026-10-08T00:00:03Z" });
    const b = jobRow({ id: "b", created_at: "2026-10-08T00:00:05Z" });
    const c = jobRow({ id: "c", created_at: "2026-10-08T00:00:04Z" });
    expect(mergeNewestFirst([[a], [b, c]], 2).map((j) => j.id)).toEqual(["b", "c"]);
  });

  const rows = [{ created_at: "2026-10-08T00:00:00Z" }, { created_at: "2026-10-01T00:00:00Z" }];

  test("keeps rows at or after since", () => {
    expect(withinSince(rows, "2026-10-05T00:00:00Z")).toEqual({
      rows: rows.slice(0, 1),
      note: null,
    });
  });

  test("an empty window falls back to the unbounded rows with a note", () => {
    const out = withinSince(rows, "2026-11-01T00:00:00Z");
    expect(out.rows).toEqual(rows);
    expect(out.note).toContain("without the time bound");
  });

  test("no rows and no since are passed through; a bad date is rejected", () => {
    expect(withinSince([], "2026-11-01T00:00:00Z")).toEqual({ rows: [], note: null });
    expect(withinSince(rows, undefined).rows).toEqual(rows);
    expect(() => withinSince(rows, "yesterday")).toThrow(/not a valid ISO date/);
  });
});

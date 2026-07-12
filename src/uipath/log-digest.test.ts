import { describe, expect, test } from "bun:test";
import {
  collapseLogs,
  digestLogs,
  extractFault,
  findLogStalls,
  MESSAGE_CAP,
  truncate,
} from "./log-digest.js";
import type { JobLog, UiPathJob } from "./uipath.js";

const log = (Level: string, Message: string, TimeStamp: string): JobLog => ({
  Level,
  Message,
  TimeStamp,
});

describe("truncate", () => {
  test("leaves short strings untouched", () => {
    expect(truncate("short message")).toBe("short message");
  });
  test("cuts long strings at MESSAGE_CAP and appends an ellipsis", () => {
    const long = "x".repeat(MESSAGE_CAP + 50);
    const out = truncate(long);
    expect(out).toBe(`${"x".repeat(MESSAGE_CAP)}…`);
    expect(out.length).toBe(MESSAGE_CAP + 1);
  });
});

describe("collapseLogs", () => {
  test("consecutive retries differing only in ids/timestamps collapse into one entry", () => {
    const logs = [
      log(
        "Error",
        "Timeout reaching order 7bfea00c-1111-2222-3333-444444444444 at 2026-07-01T10:00:01Z",
        "2026-07-01T10:00:01Z",
      ),
      log(
        "Error",
        "Timeout reaching order 23ba359f-5555-6666-7777-888888888888 at 2026-07-01T10:00:31Z",
        "2026-07-01T10:00:31Z",
      ),
      log(
        "Error",
        "Timeout reaching order 508debf6-9999-aaaa-bbbb-cccccccccccc at 2026-07-01T10:01:01Z",
        "2026-07-01T10:01:01Z",
      ),
    ];
    const collapsed = collapseLogs(logs);
    expect(collapsed).toHaveLength(1);
    expect(collapsed[0]?.count).toBe(3);
    expect(collapsed[0]?.firstAt).toBe("2026-07-01T10:00:01Z");
    expect(collapsed[0]?.lastAt).toBe("2026-07-01T10:01:01Z");
    // the first raw occurrence is kept, not the normalized form
    expect(collapsed[0]?.message).toContain("7bfea00c");
  });

  test("distinct messages and level changes do not collapse", () => {
    const logs = [
      log("Info", "Opening portal", "2026-07-01T10:00:00Z"),
      log("Error", "Opening portal", "2026-07-01T10:00:01Z"),
      log("Error", "Submit failed", "2026-07-01T10:00:02Z"),
    ];
    expect(collapseLogs(logs)).toHaveLength(3);
  });

  test("non-consecutive repeats stay separate entries", () => {
    const logs = [
      log("Error", "Submit failed", "2026-07-01T10:00:00Z"),
      log("Info", "Retrying", "2026-07-01T10:00:01Z"),
      log("Error", "Submit failed", "2026-07-01T10:00:02Z"),
    ];
    expect(collapseLogs(logs)).toHaveLength(3);
  });
});

describe("findLogStalls", () => {
  test("reports the largest gaps above the threshold, biggest first", () => {
    const logs = [
      log("Info", "step 1", "2026-07-01T10:00:00Z"),
      log("Info", "step 2", "2026-07-01T10:00:05Z"), // 5s — under threshold
      log("Info", "step 3", "2026-07-01T10:01:05Z"), // 60s stall after "step 2"
      log("Info", "step 4", "2026-07-01T10:01:25Z"), // 20s stall after "step 3"
    ];
    const stalls = findLogStalls(logs);
    expect(stalls).toHaveLength(2);
    expect(stalls[0]?.gapMs).toBe(60_000);
    expect(stalls[0]?.beforeMessage).toBe("step 2");
    expect(stalls[1]?.gapMs).toBe(20_000);
  });

  test("no stalls in a tight log stream", () => {
    const logs = [
      log("Info", "a", "2026-07-01T10:00:00Z"),
      log("Info", "b", "2026-07-01T10:00:01Z"),
    ];
    expect(findLogStalls(logs)).toEqual([]);
  });
});

describe("extractFault", () => {
  const job: UiPathJob = { Key: "k", State: "Faulted" };

  test("parses the exception type and computes a stable signature", () => {
    const logs = [
      log(
        "Error",
        "System.NullReferenceException: Object reference not set. Job 7bfea00c-1111-2222-3333-444444444444",
        "2026-07-01T10:00:00Z",
      ),
    ];
    const fault = extractFault(job, logs);
    expect(fault.exceptionType).toBe("System.NullReferenceException");
    expect(fault.signature).toContain("<id>");
    expect(fault.signature).not.toContain("7bfea00c");
  });

  test("two runs of the same fault share a signature despite different ids", () => {
    const a = extractFault(job, [
      log("Error", "Timeout on order 7bfea00c-1111-2222-3333-444444444444", "2026-07-01T10:00:00Z"),
    ]);
    const b = extractFault(job, [
      log("Error", "Timeout on order 508debf6-9999-aaaa-bbbb-cccccccccccc", "2026-07-02T11:00:00Z"),
    ]);
    expect(a.signature).toBe(b.signature);
  });

  test("falls back to a synthetic message when there are no logs", () => {
    const fault = extractFault(job, []);
    expect(fault.message).toBe("Job ended in state Faulted");
    expect(fault.exceptionType).toBeNull();
  });
});

describe("digestLogs", () => {
  test("counts by level, keeps only failure lines, and points at get_job_logs", () => {
    const logs = [
      log("Info", "Opening portal", "2026-07-01T10:00:00Z"),
      log("Info", "Unable to find submit button", "2026-07-01T10:00:01Z"), // failure worded at Info
      log("Warn", "Slow response", "2026-07-01T10:00:02Z"),
      log("Error", "Submit failed", "2026-07-01T10:00:03Z"),
    ];
    const digest = digestLogs(logs);
    expect(digest.fetched).toBe(4);
    expect(digest.byLevel).toEqual({ Info: 2, Warn: 1, Error: 1 });
    expect(digest.failures.map((f) => f.message)).toEqual([
      "Unable to find submit button",
      "Submit failed",
    ]);
    expect(digest.droppedFailures).toBe(0);
    expect(digest.note).toContain("get_job_logs");
  });
});

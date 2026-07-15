import { describe, expect, test } from "bun:test";
import {
  type JobLog,
  jobLogQueryParams,
  refineJobLogs,
  toQueueItem,
  toUiPathJob,
} from "./uipath.js";

const KEY = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee";

const log = (Level: string, Message: string, TimeStamp = "2026-07-01T10:00:00Z"): JobLog => ({
  Level,
  Message,
  TimeStamp,
});

describe("jobLogQueryParams", () => {
  test("no filter keeps the original full query: oldest first, top 500", () => {
    const p = jobLogQueryParams(KEY);
    expect(p["$filter"]).toBe(`JobKey eq ${KEY}`);
    expect(p["$orderby"]).toBe("TimeStamp asc");
    expect(p["$top"]).toBe("500");
    expect(p["$select"]).toBe("Level,Message,TimeStamp");
  });

  test("minLevel warn adds a Warn/Error/Fatal or-chain server-side", () => {
    const p = jobLogQueryParams(KEY, { minLevel: "warn" });
    expect(p["$filter"]).toBe(
      `JobKey eq ${KEY} and (Level eq 'Warn' or Level eq 'Error' or Level eq 'Fatal')`,
    );
  });

  test("minLevel error narrows to Error/Fatal", () => {
    const p = jobLogQueryParams(KEY, { minLevel: "error" });
    expect(p["$filter"]).toContain("(Level eq 'Error' or Level eq 'Fatal')");
  });

  test("contains adds a Message substring clause with OData quote escaping", () => {
    const p = jobLogQueryParams(KEY, { contains: "can't login" });
    expect(p["$filter"]).toBe(`JobKey eq ${KEY} and contains(Message, 'can''t login')`);
  });

  test("tail flips to newest-first and caps $top at the tail size", () => {
    const p = jobLogQueryParams(KEY, { tail: 50 });
    expect(p["$orderby"]).toBe("TimeStamp desc");
    expect(p["$top"]).toBe("50");
  });

  test("tail with onlyFailures keeps $top at 500 (semantic cut is client-side)", () => {
    const p = jobLogQueryParams(KEY, { tail: 50, onlyFailures: true });
    expect(p["$orderby"]).toBe("TimeStamp desc");
    expect(p["$top"]).toBe("500");
  });
});

describe("refineJobLogs", () => {
  const info = log("Info", "Processing order 42");
  const sneaky = log("Info", "Unable to find submit button, retrying"); // failure worded at Info
  const warn = log("Warn", "Slow response from portal");
  const error = log("Error", "System.Exception: boom");

  test("no filter returns logs untouched", () => {
    const logs = [info, warn, error];
    expect(refineJobLogs(logs)).toBe(logs);
  });

  test("onlyFailures keeps error-level logs AND benign-level failure wording", () => {
    expect(refineJobLogs([info, sneaky, warn, error], { onlyFailures: true })).toEqual([
      sneaky,
      error,
    ]);
  });

  test("tail restores oldest-first order from a newest-first fetch", () => {
    const older = log("Info", "first", "2026-07-01T10:00:00Z");
    const newer = log("Info", "second", "2026-07-01T10:05:00Z");
    // a tail fetch comes back newest-first
    expect(refineJobLogs([newer, older], { tail: 2 })).toEqual([older, newer]);
  });

  test("tail with onlyFailures keeps the last N failures", () => {
    const f1 = log("Error", "boom 1", "2026-07-01T10:01:00Z");
    const f2 = log("Error", "boom 2", "2026-07-01T10:02:00Z");
    const f3 = log("Error", "boom 3", "2026-07-01T10:03:00Z");
    // newest-first fetch order, interleaved with benign rows
    const fetched = [f3, info, f2, f1];
    expect(refineJobLogs(fetched, { tail: 2, onlyFailures: true })).toEqual([f2, f3]);
  });
});

describe("toUiPathJob", () => {
  test("flattens $expand=Robot($select=Name),Release($select=ProcessVersion) onto the flat shape", () => {
    const raw = {
      Id: "1",
      Key: "k1",
      State: "Successful",
      ReleaseName: "SPMG Sync",
      Robot: { Name: "ec2-bot-1" },
      Release: { ProcessVersion: "1.0.28" },
    };
    expect(toUiPathJob(raw)).toEqual({
      ...raw,
      RobotName: "ec2-bot-1",
      ProcessVersion: "1.0.28",
    });
  });

  test("omits RobotName/ProcessVersion when the nav objects are null/absent (no assigned robot)", () => {
    const raw = { Id: "2", Key: "k2", State: "Faulted", Robot: null, Release: null };
    const job = toUiPathJob(raw);
    expect(job.RobotName).toBeUndefined();
    expect(job.ProcessVersion).toBeUndefined();
    expect(job.Id).toBe("2");
  });
});

describe("toQueueItem", () => {
  const base = {
    Id: 5,
    Status: "Successful",
    Reference: "ref-1",
    CreationTime: "2026-07-01T00:00:00Z",
    RetryNumber: 0,
    QueueDefinitionId: 79926,
    SpecificContent: { MemberID: "m1" },
  };

  test("prefers the live $expand=QueueDefinition name over the item's own (possibly stale) Name", () => {
    const item = toQueueItem({
      ...base,
      Name: "stale name",
      QueueDefinition: { Name: "Live Queue Name" },
    });
    expect(item.name).toBe("Live Queue Name");
  });

  test("falls back to the item's own Name when QueueDefinition didn't resolve (expand returned null)", () => {
    const item = toQueueItem({ ...base, Name: "own name", QueueDefinition: null });
    expect(item.name).toBe("own name");
  });

  test("robotName comes from $expand=Robot, empty string when unassigned", () => {
    expect(toQueueItem({ ...base, Robot: { Name: "ec2-bot-1" } }).robotName).toBe("ec2-bot-1");
    expect(toQueueItem({ ...base, Robot: null }).robotName).toBe("");
  });
});

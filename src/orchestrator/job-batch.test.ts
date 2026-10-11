import { describe, expect, test } from "bun:test";
import { ExpectedError } from "../shared/expected-error.js";
import { jobEntriesEnvelope, resolveJobIds } from "./job-batch.js";

describe("resolveJobIds", () => {
  test("a lone jobId is a single (non-batch) lookup", () => {
    expect(resolveJobIds("job-1", undefined)).toEqual({ ids: ["job-1"], batch: false });
  });

  test("jobIds is a batch, even with one id", () => {
    expect(resolveJobIds(undefined, ["job-1"])).toEqual({ ids: ["job-1"], batch: true });
  });

  test("giving both, or neither, is a user-actionable error", () => {
    expect(() => resolveJobIds("job-1", ["job-2"])).toThrow(ExpectedError);
    expect(() => resolveJobIds(undefined, undefined)).toThrow(/either jobId or jobIds/);
  });
});

describe("jobEntriesEnvelope", () => {
  const entries = [
    { id: "a", found: true },
    { id: "b", found: false },
  ];

  test("single flattens the only entry beside the envelope fields", () => {
    expect(
      jobEntriesEnvelope("uipath", "prod", "Authorization", false, [entries[0] ?? {}]),
    ).toEqual({
      orchestrator: "uipath",
      env: "prod",
      folder: "Authorization",
      id: "a",
      found: true,
    });
  });

  test("batch nests every entry under jobs with a count", () => {
    expect(jobEntriesEnvelope("uipath", "prod", "Authorization", true, entries)).toEqual({
      orchestrator: "uipath",
      env: "prod",
      folder: "Authorization",
      count: 2,
      jobs: entries,
    });
  });
});

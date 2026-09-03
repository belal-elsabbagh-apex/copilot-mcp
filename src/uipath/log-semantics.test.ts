import { describe, expect, test } from "bun:test";
import { isFailureLog } from "./log-semantics.js";
import type { JobLog } from "./uipath.js";

const log = (Level: string, Message: string): JobLog => ({ Level, Message, TimeStamp: "" });

describe("isFailureLog", () => {
  test("true for error level or failure language", () => {
    expect(isFailureLog(log("Error", "anything"))).toBe(true);
    expect(isFailureLog(log("Info", "connection refused"))).toBe(true);
  });
  test("false for benign info logs", () => {
    expect(isFailureLog(log("Info", "all good, processed 3 items"))).toBe(false);
  });
  test("false when a failure word only appears deep inside a long embedded data blob", () => {
    // Models a robot logging an entire result.json object as one Info-level line — the
    // headline (first ~200 chars) is benign, but the huge tail can incidentally contain a
    // generic word like "failed" (e.g. a screenshot path or page title) with no bearing on
    // the actual outcome.
    const filler = "x".repeat(250);
    const message = `[runner] result.json: { "result": "SUBMITTED", "note": "${filler} this step failed" }`;
    expect(isFailureLog(log("Info", message))).toBe(false);
  });
  test("still true when the failure word is within the scan window", () => {
    const message = `ABORT: ${"x".repeat(150)}`;
    expect(isFailureLog(log("Info", message))).toBe(true);
  });
});

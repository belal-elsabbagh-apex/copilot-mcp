import { describe, expect, test } from "bun:test";
import type { JobLog } from "../uipath/uipath.js";
import { analyzeOutput, isFailureLog } from "./output-analysis.js";

const log = (Level: string, Message: string): JobLog => ({ Level, Message, TimeStamp: "" });

describe("analyzeOutput", () => {
  test("flags out_Result = Failure as an error", () => {
    const comments = analyzeOutput({ out_Result: "Failure" });
    expect(comments).toHaveLength(1);
    expect(comments[0]?.severity).toBe("error");
    expect(comments[0]?.rule).toBe("result-failure");
  });

  test("ignores a successful result", () => {
    expect(analyzeOutput({ out_Result: "Success" })).toEqual([]);
  });

  test("counts error- and warning-level logs separately", () => {
    const comments = analyzeOutput({}, [
      log("Error", "boom"),
      log("Error", "boom again"),
      log("Warning", "heads up"),
    ]);
    const byRule = Object.fromEntries(comments.map((c) => [c.rule, c.message]));
    expect(byRule["log-errors"]).toBe("2 error logs during execution.");
    expect(byRule["log-warnings"]).toBe("1 warning log during execution.");
  });

  test("detects failure language in otherwise-benign (info) logs", () => {
    const comments = analyzeOutput({}, [log("Info", "the request timed out")]);
    expect(comments.map((c) => c.rule)).toContain("log-failure-indicators");
  });

  test("does not double-count: error-level logs are excluded from the language heuristic", () => {
    const comments = analyzeOutput({}, [log("Error", "unhandled exception")]);
    expect(comments.map((c) => c.rule)).toEqual(["log-errors"]);
  });
});

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
